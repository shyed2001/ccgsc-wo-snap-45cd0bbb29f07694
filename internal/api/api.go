// Path: internal/api/api.go | Author: Codex for Claude Opus 5.5 | 2026-11-17
// What/why: Go ServeMux REST adapter, shared auth and no implicit unsupported behavior; WWPP-R32.1..R32.9.
// WO-T2.2 (hermes MiniMax-M3, 2026-11-17): the agy pages are served from the embedded page tree
// under /pools/, one shared nav under /nav/, and the old /web/ placeholder routes redirect to
// /pools/monitor.html.
package api

import (
	"archive/tar"
	"bytes"
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/httpx"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/redisadmin"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/worker"
	"ccgsc/poolscore/web"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/klauspost/compress/zstd"
)

// emptyFS stands in for a failed embed so the router never serves a nil file tree.
type emptyFS struct{}

func (emptyFS) Open(name string) (fs.File, error) { return nil, fs.ErrNotExist }

type Server struct {
	// T91 (Codex, 2026-11-20): shared Redis policy, independent of lease traffic.
	RedisAdmin    *redisadmin.Runner
	redisConfigMu sync.Mutex
	Configs       AdminConfigs
	Engine        *pool.Engine
	Auth          *auth.Service
	Log           *slog.Logger
	Config        config.Config
	// R15 AT22-GM2-3: the store that is really open (the config driver field can
	// disagree with it when POOLS_PG_DSN points at a PostgreSQL). The admin caps
	// view reports it, never a secret.
	StoreDriver string
	RateLimit   *auth.RateLimiter
	loginLimit  *auth.RateLimiter
	loginOnce   sync.Once
	anonLimit   *auth.RateLimiter
	anonOnce    sync.Once
	eventOnce   sync.Once
	eventBus    *eventStore
	webhookOnce sync.Once
	webhookSem  chan struct{}
	p2pOnce     sync.Once
	p2pMux      http.Handler
	P2PDatabase *pgxpool.Pool
}
type action func(http.ResponseWriter, *http.Request, auth.Claims, string)

func (s *Server) secureArtifactRead(p string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			httpx.Error(w, r, auth.ErrUnauthorized)
			return
		}
		c, err := s.Auth.Validate(r.Context(), strings.TrimPrefix(h, "Bearer "))
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		// R15: the director reads artifacts alongside developer and admin.
		if !((c.HasRole("developer") || c.HasRole("admin") || c.HasRole("director")) && c.Allows("artifacts:read") || c.HasRole("contributor") && c.Allows("worker")) {
			httpx.Error(w, r, pool.ErrForbidden)
			return
		}
		if s.rateLimited(c.KeyID) {
			rateLimitResponse(w, r)
			return
		}
		// T100 (owner decision 2026-11-21, option A): a Store A admin reads every
		// artifact, a contributor tab keeps the worker read rule it has today, and
		// a developer must hold a per-owner reference for the artifact.
		if !c.HasRole("contributor") {
			if err := s.requireArtifactUse(r.Context(), c, []string{r.PathValue("artifactId")}); err != nil {
				httpx.Error(w, r, err)
				return
			}
		}
		a, err := s.Engine.Artifact(r.Context(), r.PathValue("artifactId"))
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Artifact-Sha256", a.SHA256)
		_, _ = w.Write(a.Data)
	}
}

// requireArtifactUse enforces the T100 owner-reference rule for developer
// callers: every listed artifact must carry a reference for the caller's key
// subject. Store A admins and directors bypass the rule, and the contributor
// worker read rule is checked by the caller before this helper runs.
func (s *Server) requireArtifactUse(ctx context.Context, c auth.Claims, ids []string) error {
	if c.HasRole("admin") || c.HasRole("director") {
		return nil
	}
	return s.Engine.CanUseArtifacts(ctx, c.KeyID, false, ids)
}

func (s *Server) secure(p, role, scope string, fn action) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie("ccgsc_admin_session"); err == nil && cookie.Value != "" {
			if sess, err := s.Auth.ValidateSession(r.Context(), cookie.Value); err == nil {
				if role == "admin" || role == "director" {
					if slices.Contains(sess.Roles, "admin") || slices.Contains(sess.Roles, "director") {
						if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions &&
							r.Header.Get("X-CSRF-Token") != sess.CSRFToken {
							httpx.Problem(w, r, http.StatusForbidden, "invalid-csrf", "A valid session CSRF token is required.")
							return
						}
						claims := auth.Claims{
							Subject: sess.Username,
							User:    auth.User{ID: sess.Username, AccountStore: "A", Roles: sess.Roles},
							Scopes:  []string{"*"},
						}
						if s.rateLimited(claims.KeyID) {
							rateLimitResponse(w, r)
							return
						}
						fn(w, r, claims, p)
						return
					}
					httpx.Error(w, r, pool.ErrForbidden)
					return
				}
			}
		}
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			httpx.Error(w, r, auth.ErrUnauthorized)
			return
		}
		c, e := s.Auth.Validate(r.Context(), strings.TrimPrefix(h, "Bearer "))
		if e != nil {
			httpx.Error(w, r, e)
			return
		}
		if !allowsRoute(c, role, scope) {
			httpx.Error(w, r, pool.ErrForbidden)
			return
		}
		if s.rateLimited(c.KeyID) {
			rateLimitResponse(w, r)
			return
		}
		fn(w, r, c, p)
	}
}

func allowsRoute(c auth.Claims, role, scope string) bool {
	if role == "admin" {
		return (c.HasRole("admin") || c.HasRole("director")) && c.Allows(scope)
	}
	return c.HasRole(role) && c.Allows(scope)
}

func (s *Server) Handler() http.Handler {
	s.configureAdminStore()
	s.Engine.WebhookAllowPrivateIPs = s.Config.WebhookAllowPrivateIPs
	// R16-FIX (F01/F02): the caps resolution the engine owns needs the knobs of
	// the configuration file. Recording them here means every server, including a
	// test harness, resolves the same precedence: saved admin value >
	// configuration file > default.
	s.Engine.SetConfiguredCaps(s.Config.Caps())
	if s.Config.AnonymousContributors.MaxLiveTabsPerIP > 0 || s.Config.AnonymousContributors.MaxTotalTabs > 0 {
		s.Engine.SetAnonymousLimits(pool.AnonymousLimits{
			MaxLiveTabsPerIP: s.Config.AnonymousContributors.MaxLiveTabsPerIP,
			MaxTotalTabs:     s.Config.AnonymousContributors.MaxTotalTabs,
		})
	}
	// Install before serving requests or starting maintenance; all engine event writes
	// then publish only after their transaction commits (review-01, 2026-11-17).
	s.eventOnce.Do(func() {
		s.eventBus = &eventStore{Store: s.Engine.Store, subscribers: make(map[chan []store.Record]struct{})}
		s.Engine.Store = s.eventBus
	})
	s.webhookOnce.Do(func() {
		s.webhookSem = make(chan struct{}, 8)
		go s.webhookLoop()
	})
	mux := http.NewServeMux()
	s.Engine.SetAppEgressReady(s.egressRunOriginReady())
	s.appEgressRoutes(mux)
	s.mountP2P(mux)
	s.configRoutes(mux)
	// W10C row 3: the Director pool settings (verifyMode, rates, isolationMode).
	s.poolSettingsRoutes(mux)
	// F12 (G-F12-1, G-F12-4): admin Dew window, per-app override, recorded results.
	s.dewRoutes(mux)
	// WO R17-RECOMPUTE (G-R16-4) row 2: the per-pool recompute mode of the
	// admin panel.
	s.recomputeRoutes(mux)
	s.workerRoutes(mux)
	s.legacyPoolRoutes(mux) // T92 | Codex | 2026-11-20: read-only Node bridge.
	s.denyRoutes(mux)
	mux.HandleFunc("POST /admin/v1/task-keys:rotate", s.secure("", "admin", "config:write", s.rotateTaskKey))
	s.capsRoutes(mux)
	s.processAppRoutes(mux) // F9: admin-approved Process application quotas
	s.threadAppRoutes(mux)  // F10: separate, approved per-worker Thread application quotas
	s.metricsRoutes(mux)
	ws := worker.Service{Engine: s.Engine, Auth: s.Auth, ClientIP: s.clientIP}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, 200, map[string]any{"status": "ok", "service": "pools-core", "mode": "skeleton"})
	})
	for _, spec := range []struct{ prefix, pool string }{{"/thread-pool/v1", pool.Thread}, {"/process-pool/v1", pool.Process}} {
		prefix, p := spec.prefix, spec.pool
		mux.HandleFunc("POST "+prefix+"/workers/anonymous", func(w http.ResponseWriter, r *http.Request) {
			s.anonymousWorkerRoute(w, r, p)
		})
		for _, path := range []string{"/auth/token", "/auth/refresh"} {
			mux.HandleFunc("POST "+prefix+path, func(w http.ResponseWriter, r *http.Request) {
				var q struct {
					APIKey string   `json:"apiKey"`
					Scopes []string `json:"scopes"`
				}
				if !httpx.Decode(w, r, &q) {
					return
				}
				if !s.loginRateLimiter().Allow(s.clientIP(r)) {
					rateLimitResponse(w, r)
					return
				}
				v, e := s.Auth.Exchange(r.Context(), q.APIKey, q.Scopes)
				if e != nil {
					httpx.Error(w, r, e)
					return
				}
				v.PoolID = p
				httpx.JSON(w, 200, v)
			})
		}
		mux.HandleFunc("POST "+prefix+"/auth/keys", s.secure(p, "admin", "keys:write", s.createKey))
		mux.HandleFunc("POST "+prefix+"/auth/keys/{action}", s.secure(p, "admin", "keys:write", s.revokeKey))
		mux.HandleFunc("PUT "+prefix+"/artifacts", s.secure(p, "developer", "artifacts:write", s.upload))
		// The F8 admin routes of the upload scan and the review gate.
		s.uploadScanRoutes(mux, prefix, p)
		mux.HandleFunc("GET "+prefix+"/artifacts/{artifactId}", s.secureArtifactRead(p))
		// T100: the delete removes only the caller's reference; the blob is
		// deleted when no reference remains.
		mux.HandleFunc("DELETE "+prefix+"/artifacts/{artifactId}", s.secure(p, "developer", "artifacts:write", s.deleteArtifact))
		mux.HandleFunc("POST "+prefix+"/jobs", s.secure(p, "developer", "jobs:write", s.createJob))
		mux.HandleFunc("POST "+prefix+"/jobs:validate", s.secure(p, "developer", "jobs:write", s.validate))
		mux.HandleFunc("GET "+prefix+"/jobs/{jobId}", s.secure(p, "developer", "jobs:read", s.getJob))
		mux.HandleFunc("GET "+prefix+"/jobs/{jobId}/chain", s.secure(p, "developer", "jobs:read", s.jobChain))
		// ServeMux cannot attach :cancel to a wildcard segment; validate the complete segment.
		mux.HandleFunc("POST "+prefix+"/jobs/{action}", s.secure(p, "developer", "jobs:write", s.cancel))
		mux.HandleFunc("GET "+prefix+"/jobs/{jobId}/results", s.secure(p, "developer", "jobs:read", s.results))
		mux.HandleFunc("GET "+prefix+"/jobs/{jobId}/results:bulk", s.secure(p, "developer", "jobs:read", s.bulk))
		mux.HandleFunc("GET "+prefix+"/events", s.secure(p, "developer", "jobs:read", s.renewableStream(s.events)))
		mux.HandleFunc("GET "+prefix+"/capabilities", s.secure(p, "developer", "jobs:read", s.capabilities))
		mux.HandleFunc("GET "+prefix+"/merkle:checkpoints", s.secure(p, "developer", "jobs:read", s.merkleCheckpoints))
		mux.HandleFunc("POST "+prefix+"/workers/register", s.secure(p, "contributor", "worker", ws.Register))
		mux.HandleFunc("POST "+prefix+"/workers/{tabId}/heartbeat", s.secure(p, "contributor", "worker", ws.Heartbeat))
		mux.HandleFunc("POST "+prefix+"/workers/{tabId}/leases", s.secure(p, "contributor", "worker", ws.Claim))
		mux.HandleFunc("GET "+prefix+"/workers/{tabId}/tasks", s.secure(p, "contributor", "worker", s.renewableStream(ws.Tasks)))
		mux.HandleFunc("POST "+prefix+"/workers/{tabId}/results", s.secure(p, "contributor", "worker", ws.Submit))
		if p == pool.Process {
			mux.HandleFunc("POST "+prefix+"/sessions", s.secure(p, "developer", "jobs:write", s.openSession))
			mux.HandleFunc("GET "+prefix+"/sessions/{sessionId}", s.secure(p, "developer", "jobs:read", s.getSession))
			mux.HandleFunc("POST "+prefix+"/sessions/{sessionId}/tasks", s.secure(p, "developer", "jobs:write", s.sessionTasks))
			mux.HandleFunc("POST "+prefix+"/sessions/{action}", s.secure(p, "developer", "jobs:write", s.closeSession))
		}
	}
	// One shared nav for every page: served at /nav/ (the /pools/ pages reference
	// /nav/nav.js) and at /web/ for the historical links. Same files, one source.
	navRoot, e := web.NavRoot()
	if e != nil {
		s.Log.Error("web.nav.embed", "error", e)
		navRoot = emptyFS{}
	}
	pagesRoot, e := web.PagesRoot()
	if e != nil {
		s.Log.Error("web.pages.embed", "error", e)
		pagesRoot = emptyFS{}
	}
	mux.HandleFunc("GET /pools/auth/csrf", s.csrfEndpoint)
	mux.HandleFunc("GET /pools/runner-config", s.runnerConfig)
	// CR-53: the MOCK job gets separate workload bytes at runtime; platform JS
	// must never embed a generated copy of a workload implementation.
	mux.HandleFunc("GET /pools/mock-artifact", s.serveMockArtifact)
	mux.HandleFunc("GET /pools/auth/session", s.sessionEndpoint)
	mux.HandleFunc("POST /pools/auth/login", s.loginEndpoint)
	mux.HandleFunc("POST /pools/login", s.loginEndpoint)
	mux.HandleFunc("POST /pools/auth/logout", s.logoutEndpoint)
	mux.HandleFunc("POST /pools/auth/change-password", s.changePasswordEndpoint)
	// T107: the embedded files have no mtime, so no Last-Modified/ETag; without
	// Cache-Control a browser kept the old contribute.js after a deploy (2026-11-21).
	mux.Handle("GET /nav/", noStore(http.StripPrefix("/nav/", http.FileServer(http.FS(navRoot)))))
	mux.Handle("GET /pools/", noStore(s.servePoolsTree(pagesRoot)))
	mux.HandleFunc("GET /pools/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/pools/monitor.html", http.StatusTemporaryRedirect)
	})
	// The old placeholder routes keep working, but the admin and monitor pages
	// now live under /pools/ (WO-T2.2, WWPP-R38.5).
	mux.HandleFunc("GET /web/admin/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/pools/monitor.html", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("GET /web/monitor/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/pools/monitor.html", http.StatusTemporaryRedirect)
	})
	// R15: the /web/ tree is behind the same admin gate as /pools/.
	mux.Handle("GET /web/", s.adminPageGate(http.StripPrefix("/web/", http.FileServer(http.FS(web.Files)))))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/pools/monitor.html", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpx.Problem(w, r, 404, "not-found", "Unknown endpoint.")
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.isRunOriginRequest(r) {
			s.serveRunOrigin(pagesRoot, w, r)
			return
		}
		start := time.Now()
		id := auth.ID()
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", mainContentSecurityPolicy(s.Config.RunOrigin))
		rec := &response{ResponseWriter: w, status: 200}
		defer func() {
			if v := recover(); v != nil {
				s.Log.Error("request.panic", "traceId", id, "error", fmt.Sprint(v))
				if !rec.wrote {
					httpx.Problem(rec, r, 500, "internal-error", "The request failed.")
				}
			}
			s.Log.Info("request", "traceId", id, "method", r.Method, "path", r.URL.Path, "status", rec.status, "durationMs", time.Since(start).Milliseconds())
		}()
		mux.ServeHTTP(rec, r)
	})
}

type response struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *response) WriteHeader(n int) {
	if !w.wrote {
		w.status = n
		w.wrote = true
		w.ResponseWriter.WriteHeader(n)
	}
}
func (w *response) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}
func (w *response) Flush() {
	if !w.wrote {
		w.WriteHeader(200)
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (s *Server) createKey(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	var q struct {
		User   auth.User `json:"user"`
		Scopes []string  `json:"scopes"`
	}
	if !httpx.Decode(w, r, &q) {
		return
	}
	raw, k, e := s.Auth.CreateKey(r.Context(), q.User, q.Scopes)
	if e != nil {
		httpx.Problem(w, r, 400, "invalid-key", e.Error())
		return
	}
	s.Log.Info("security.key.created", "issuer", c.Subject, "keyId", k.ID, "accountStore", q.User.AccountStore)
	httpx.JSON(w, 201, map[string]any{"apiKey": raw, "keyId": k.ID, "user": k.User})
}
func (s *Server) upload(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	offset := r.Header.Get("Upload-Offset")
	if offset == "" && r.ContentLength >= 0 {
		offset = "0"
	}
	o, e := strconv.ParseInt(offset, 10, 64)
	if e != nil {
		httpx.Error(w, r, pool.ErrInvalid)
		return
	}
	n, e := strconv.ParseInt(r.Header.Get("Upload-Length"), 10, 64)
	if e != nil {
		if r.ContentLength >= 0 && o == 0 {
			n = r.ContentLength
		} else {
			httpx.Error(w, r, pool.ErrInvalid)
			return
		}
	}
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, pool.MaxArtifactBytes))
	if e != nil {
		httpx.Problem(w, r, 413, "artifact-too-large", "Invalid or oversized upload.")
		return
	}
	if v := r.Header.Get("Content-Range"); v != "" {
		want := fmt.Sprintf("bytes %d-%d/%d", o, o+int64(len(b))-1, n)
		if v != want {
			httpx.Error(w, r, pool.ErrInvalid)
			return
		}
	}
	a, e := s.Engine.UploadOwned(r.Context(), r.Header.Get("X-Artifact-Sha256"), r.Header.Get("Content-Type"), o, n, b, c.KeyID, uploadScanOwnerCode(c))
	if e != nil {
		// A failed upload scan still leaves the artifact and its report stored, so
		// the admin review queue keeps the evidence (WWPP-R36.1). The response is
		// an RFC 9457 problem body with the scan report attached.
		if errors.Is(e, pool.ErrArtifactRejected) && a.Scan != nil {
			uploadScanProblem(w, r, 422, "artifact-rejected", e.Error(), a.Scan)
			return
		}
		httpx.Error(w, r, e)
		return
	}
	a.Data = nil
	if a.Complete {
		httpx.JSON(w, http.StatusCreated, uploadScanArtifactView(a))
		return
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(a.NextOffset, 10))
	httpx.JSON(w, http.StatusOK, uploadScanArtifactView(a))
}
func (s *Server) createJob(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	var q pool.CreateRequest
	if !httpx.Decode(w, r, &q) {
		return
	}
	if q.Webhook != nil {
		if !s.Config.WebhooksEnabled {
			httpx.Error(w, r, pool.ErrUnsupported)
			return
		}
		if e := q.Webhook.Validate(s.Config.WebhookAllowPrivateIPs); e != nil {
			httpx.Error(w, r, pool.ErrInvalid)
			return
		}
	}
	// T100: job use of a module artifact requires that the caller holds a
	// reference for it (a Store A admin bypasses). The module_sha256 alias
	// resolves to the same check the engine applies.
	ids := q.ModuleArtifactIDs
	if len(ids) == 0 && q.ModuleSHA256 != "" {
		ids = []string{q.ModuleSHA256}
	}
	if err := s.requireArtifactUse(r.Context(), c, ids); err != nil {
		httpx.Error(w, r, err)
		return
	}
	j, e := s.Engine.Create(r.Context(), p, c.KeyID, r.Header.Get("Idempotency-Key"), q)
	if e != nil {
		httpx.Error(w, r, e)
		return
	}
	w.Header().Set("Location", strings.TrimSuffix(r.URL.Path, "/jobs")+"/jobs/"+j.JobID)
	httpx.JSON(w, 201, s.jobView(j))
}

// deleteArtifact answers DELETE /{pool}/v1/artifacts/{artifactId} (T100): it
// removes only the caller's reference and reports whether the blob itself was
// deleted because no reference remained.
func (s *Server) deleteArtifact(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	out, err := s.Engine.DeleteArtifactReference(r.Context(), r.PathValue("artifactId"), c.KeyID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"artifactId": out.ArtifactID, "sha256": out.SHA256, "removed": out.Removed,
		"blobDeleted": out.BlobDeleted, "remainingReferences": out.RemainingRefs,
	})
}
func (s *Server) jobView(j pool.Job) map[string]any {
	return map[string]any{
		"jobId": j.JobID, "serverId": j.ServerID, "poolId": j.PoolID, "state": j.State,
		"createdAt": j.CreatedAt, "startedAt": nil, "finishedAt": j.FinishedAt,
		"needs": j.Needs, "policy": j.Policy, "maxAttempts": j.MaxAttempts, "priority": j.Priority,
		"progress": j.Progress, "warnings": j.Warnings, "sessionId": j.SessionID,
		"groupBy": rawObjectOrNil(j.Request.GroupBy), "pin": rawObjectOrNil(j.Request.Pin),
		"resultCursor": "", "configVersion": j.ConfigVersion, "chosenPolicy": j.ChosenPolicy,
		"rerunPercent": s.Engine.JobRerunPercent(j), "chainMode": j.ChainMode,
		"chainHead": j.ChainHead, "chainEntries": j.ChainEntries, "auditGap": j.AuditGap,
	}
}

func rawObjectOrNil(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return value
}
func (s *Server) validate(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	var q pool.CreateRequest
	if !httpx.Decode(w, r, &q) {
		return
	}
	q, warnings, e := s.Engine.Validate(q)
	if e != nil {
		httpx.Error(w, r, e)
		return
	}
	estimated := len(q.Tasks)
	if q.Split != nil {
		estimated = q.Split.Count
	}
	httpx.JSON(w, 200, map[string]any{"valid": true, "warnings": warnings, "estimatedTasks": estimated, "deviceClasses": []string{"cpu", "gpu"}})
}
func (s *Server) getJob(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	j, e := s.Engine.Get(r.Context(), p, c.KeyID, r.PathValue("jobId"))
	if e != nil {
		httpx.Error(w, r, e)
		return
	}
	httpx.JSON(w, 200, s.jobView(j))
}
func (s *Server) cancel(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	v := r.PathValue("action")
	if !strings.HasSuffix(v, ":cancel") {
		httpx.Problem(w, r, 404, "not-found", "Unknown job action.")
		return
	}
	var q struct {
		Reason  string `json:"reason"`
		GraceMS int    `json:"graceMs"`
	}
	if r.ContentLength != 0 && !httpx.Decode(w, r, &q) {
		return
	}
	if q.Reason != "" || q.GraceMS != 0 {
		httpx.Error(w, r, pool.ErrUnsupported)
		return
	}
	j, alreadyTerminal, e := s.Engine.Cancel(r.Context(), p, c.KeyID, strings.TrimSuffix(v, ":cancel"))
	if e != nil {
		httpx.Error(w, r, e)
		return
	}
	status := http.StatusAccepted
	if alreadyTerminal {
		status = http.StatusOK
	}
	httpx.JSON(w, status, s.jobView(j))
}
func (s *Server) results(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	j, e := s.Engine.Get(r.Context(), p, c.KeyID, r.PathValue("jobId"))
	if e != nil {
		httpx.Error(w, r, e)
		return
	}
	items, hasMore, nextCursor, e := resultPage(r, j.Results)
	if e != nil {
		httpx.Error(w, r, e)
		return
	}
	views := make([]any, 0, len(items))
	for _, item := range items {
		views = append(views, resultView(item))
	}
	httpx.JSON(w, 200, resultPageView(j, views, hasMore, nextCursor))
}

// Include the developer-owned audit anchor with the result page.
func resultPageView(j pool.Job, views []any, hasMore bool, nextCursor string) map[string]any {
	out := map[string]any{"items": views, "order": "as_completed", "hasMore": hasMore, "nextCursor": nextCursor}
	if j.ChainMode == pool.ChainModePerRun {
		out["chainHead"] = j.ChainHead
		out["chainEntries"] = j.ChainEntries
		out["auditGap"] = j.AuditGap
	}
	return out
}
func resultView(result pool.Result) map[string]any {
	view := map[string]any{
		"jobId": result.JobID, "taskId": result.TaskID, "status": result.Status,
		"attempts": result.Attempts, "durationMs": 0, "completedAt": result.At, "late": result.Late,
		"verified": result.Verified, "hashOk": result.HashOK,
		"canaryRan": result.CanaryRan, "canaryState": result.CanaryState, "canaryOk": result.CanaryOK,
		"trustComplete": result.TrustComplete, "rerun": result.Rerun, "rerunOk": result.RerunOK,
		"flagged": result.Flagged, "flagReason": result.FlagReason,
	}
	if len(result.Output) != 0 {
		var output any
		if json.Unmarshal(result.Output, &output) == nil {
			if object, ok := output.(map[string]any); ok {
				if object["inline"] != nil || object["json"] != nil || object["url"] != nil || object["artifactId"] != nil {
					view["output"] = object
				} else {
					view["output"] = map[string]any{"json": object}
				}
			} else {
				view["output"] = map[string]any{"inline": base64.StdEncoding.EncodeToString(result.Output)}
			}
		} else {
			view["output"] = map[string]any{"inline": base64.StdEncoding.EncodeToString(result.Output)}
		}
	}
	if result.Error != "" {
		view["error"] = map[string]any{
			"type": "about:blank", "title": "Task failed", "status": http.StatusInternalServerError,
			"code": "task-failed", "detail": result.Error, "retryable": false,
		}
	}
	return view
}
func resultPage(r *http.Request, results []pool.Result) ([]pool.Result, bool, string, error) {
	offset := 0
	limit := 100
	if cur := r.URL.Query().Get("cursor"); cur != "" {
		b, er := base64.RawURLEncoding.DecodeString(cur)
		if er != nil {
			return nil, false, "", pool.ErrInvalid
		}
		offset, er = strconv.Atoi(string(b))
		if er != nil || offset < 0 || offset > len(results) {
			return nil, false, "", pool.ErrInvalid
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		var e error
		limit, e = strconv.Atoi(v)
		if e != nil || limit < 1 || limit > 1000 {
			return nil, false, "", pool.ErrInvalid
		}
	}
	items := []pool.Result{}
	for offset < len(results) && len(items) < limit {
		v := results[offset]
		offset++
		if r.URL.Query().Get("includeFailed") == "false" && v.Status == "failed" {
			continue
		}
		items = append(items, v)
	}
	return items, offset < len(results), base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset))), nil
}
func (s *Server) bulk(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	j, e := s.Engine.Get(r.Context(), p, c.KeyID, r.PathValue("jobId"))
	if e != nil {
		httpx.Error(w, r, e)
		return
	}
	items, hasMore, nextCursor, e := bulkPage(r, j.Results)
	if e != nil {
		httpx.Error(w, r, e)
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "ndjson"
	}
	for len(items) > 1 && bulkEstimate(items) > 8<<20 {
		items = items[:len(items)-1]
		hasMore = true
	}
	var body []byte
	switch format {
	case "ndjson":
		var b strings.Builder
		for _, item := range items {
			raw, er := json.Marshal(resultView(item))
			if er != nil {
				httpx.Error(w, r, er)
				return
			}
			b.Write(raw)
			b.WriteByte('\n')
		}
		body = []byte(b.String())
	case "json":
		var er error
		views := make([]any, 0, len(items))
		for _, item := range items {
			views = append(views, resultView(item))
		}
		body, er = json.Marshal(resultPageView(j, views, hasMore, nextCursor))
		if er != nil {
			httpx.Error(w, r, er)
			return
		}
	case "zstd-tar":
		var archive bytes.Buffer
		encoder, er := zstd.NewWriter(&archive)
		if er != nil {
			httpx.Error(w, r, er)
			return
		}
		tw := tar.NewWriter(encoder)
		for _, item := range items {
			raw, er := json.Marshal(resultView(item))
			if er != nil {
				_ = tw.Close()
				_ = encoder.Close()
				httpx.Error(w, r, er)
				return
			}
			name := item.TaskID + "-" + item.SHA256
			if item.TaskID == "" && item.SHA256 == "" {
				hash := sha256.Sum256(raw)
				name = fmt.Sprintf("%x", hash[:])
			}
			if er = tw.WriteHeader(&tar.Header{Name: name + ".json", Mode: 0o600, Size: int64(len(raw))}); er != nil {
				_ = tw.Close()
				_ = encoder.Close()
				httpx.Error(w, r, er)
				return
			}
			if _, er = tw.Write(raw); er != nil {
				_ = tw.Close()
				_ = encoder.Close()
				httpx.Error(w, r, er)
				return
			}
		}
		if er = tw.Close(); er != nil {
			_ = encoder.Close()
			httpx.Error(w, r, er)
			return
		}
		if er = encoder.Close(); er != nil {
			httpx.Error(w, r, er)
			return
		}
		body = archive.Bytes()
	default:
		httpx.Error(w, r, pool.ErrInvalid)
		return
	}
	if len(body) > 8<<20 {
		if len(items) == 1 {
			// A single result is never dropped solely for exceeding the page cap.
			// The caller can request a smaller result through the normal artifact limits.
		} else {
			httpx.Problem(w, r, http.StatusRequestEntityTooLarge, "bulk-too-large", "Bulk page exceeds the byte limit.")
			return
		}
	}
	sum := sha256.Sum256(body)
	etag := `"` + fmt.Sprintf("%x", sum[:]) + `"`
	w.Header().Set("ETag", etag)
	if j.ChainMode == pool.ChainModePerRun {
		w.Header().Set("X-Chain-Head", j.ChainHead)
		w.Header().Set("X-Chain-Entries", strconv.Itoa(j.ChainEntries))
		w.Header().Set("X-Audit-Gap", strconv.FormatBool(j.AuditGap))
	}
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	if format == "ndjson" {
		w.Header().Set("Content-Type", "application/x-ndjson")
	} else if format == "zstd-tar" {
		w.Header().Set("Content-Type", "application/zstd")
		w.Header().Set("Content-Encoding", "zstd")
		w.Header().Set("X-Artifact-Sha256", fmt.Sprintf("%x", sum[:]))
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("X-Has-More", strconv.FormatBool(hasMore))
	if hasMore {
		w.Header().Set("X-Next-Cursor", nextCursor)
	}
	w.Header().Set("Content-Disposition", `attachment; filename="results-`+r.PathValue("jobId")+`.`+format+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func bulkEstimate(items []pool.Result) int {
	total := 0
	for _, item := range items {
		raw, err := json.Marshal(resultView(item))
		if err != nil {
			return 8 << 20
		}
		total += len(raw) + 1
	}
	return total
}

type bulkCursor struct {
	Offset int    `json:"offset"`
	Bind   string `json:"bind"`
}

func bulkPage(r *http.Request, results []pool.Result) ([]pool.Result, bool, string, error) {
	q := r.URL.Query()
	limit := 1000
	if v := q.Get("limit"); v != "" {
		var err error
		limit, err = strconv.Atoi(v)
		if err != nil || limit < 1 || limit > 10000 {
			return nil, false, "", pool.ErrInvalid
		}
	}
	bind := sha256.Sum256([]byte(strings.Join([]string{q.Get("format"), q.Get("state"), q.Get("verified"), q.Get("includeFailed")}, "\x00")))
	binding := fmt.Sprintf("%x", bind[:])
	offset := 0
	if cur := q.Get("cursor"); cur != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cur)
		if err != nil {
			return nil, false, "", pool.ErrInvalid
		}
		var c bulkCursor
		if json.Unmarshal(raw, &c) != nil || c.Offset < 0 || c.Offset > len(results) || c.Bind != binding {
			return nil, false, "", pool.ErrInvalid
		}
		offset = c.Offset
	}
	state, wantVerified := q.Get("state"), q.Get("verified")
	includeFailed := q.Get("includeFailed") != "false"
	items := make([]pool.Result, 0, limit)
	i := offset
	for i < len(results) && len(items) < limit {
		result := results[i]
		i++
		if !includeFailed && result.Status == "failed" {
			continue
		}
		if state != "" && result.Status != state {
			continue
		}
		if wantVerified == "true" && !result.Verified || wantVerified == "false" && result.Verified {
			continue
		}
		items = append(items, result)
	}
	hasMore := i < len(results)
	next := ""
	if hasMore {
		raw, _ := json.Marshal(bulkCursor{Offset: i, Bind: binding})
		next = base64.RawURLEncoding.EncodeToString(raw)
	}
	return items, hasMore, next, nil
}
func (s *Server) capabilities(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	cfg := s.Engine.TrustConfig()
	quota := s.Engine.QuotaConfig()
	scan := s.Engine.UploadScanConfig()
	chain := s.Engine.ChainConfig()
	gpu := s.Engine.GPUConfig()
	httpx.JSON(w, 200, s.applyPolicyCapabilities(r.Context(), p, map[string]any{
		"serverId": s.Engine.ServerID, "poolId": p, "apiVersion": "1",
		"anonymousJoin": s.Config.AnonymousContributors.Enabled,
		"serverLimits": map[string]any{
			"maxTasksPerJob": pool.MaxTasksPerJob, "maxInlineBytes": 262144,
			"maxBatchSize": pool.MaxBatchSize, "maxTaskBytes": pool.MaxArtifactBytes,
			"maxTaskDurationMs": 0, "maxArtifactBytes": pool.MaxArtifactBytes,
			"defaultBatchSize": map[string]int{"cpu": 1, "gpu": 1},
		},
		"gpu": map[string]any{"supported": gpu.Enabled, "webgpuRequired": true, "maxSlotsPerTab": gpu.MaxSlotsPerTab, "onBatteryAllowed": gpu.OnBatteryAllowed},
		"quotas": map[string]any{
			"runningJobs": quota.MaxRunningJobs, "queuedTasks": quota.MaxQueuedTasks,
			"requestsPerMinute": quota.RequestsPerMinute, "taskBytes": 0, "taskTimeMs": 0,
			"priorityHighAllowed": quota.HighPriority,
		},
		"priorityClasses": []string{"low", "normal", "high"},
		"sessions":        map[string]any{"supported": p == pool.Process, "warmInitializerSupported": p == pool.Process},
		"rerun":           map[string]any{"available": cfg.RerunEnabled, "defaultPercent": cfg.RerunPercent, "maxPercent": cfg.RerunMaxPercent},
		"tamperEvidence":  map[string]any{"securityLogEnabled": true, "perJobChainAvailable": chain.ChainEnabled, "merkleAvailable": chain.MerkleEnabled},
		"uploadChecks":    map[string]any{"formatSizeHash": scan.Enabled, "importPermissionScan": scan.Enabled, "reviewGate": scan.RequireReview, "malwareAbuseScan": false},
		"webhooks":        map[string]any{"enabled": s.Config.WebhooksEnabled, "allowPrivateIPs": s.Config.WebhookAllowPrivateIPs},
		"p2p":             map[string]any{"enabled": false, "shareMode": "hash-addressed-only", "resultsToServer": true},
		"dew": map[string]any{
			"enabled": true, "receiptWindowSeconds": pool.DefaultDewReceiptWindowSeconds,
			"minWindowSeconds": pool.DewMinWindowSeconds, "maxWindowSeconds": pool.DewMaxWindowSeconds,
			"receiptAlgorithm": "ed25519",
		},
	}))
}

// renewableStream gives both SSE routes an explicit renewal protocol without
// extending token validity. The handler finishes before the final frame is written.
func (s *Server) renewableStream(fn action) action {
	return func(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
		remaining := time.Unix(c.Expires, 0).Sub(s.Auth.Now()) - 30*time.Second
		ctx, cancel := context.WithTimeout(r.Context(), remaining)
		defer cancel()
		if remaining > 0 {
			fn(w, r.WithContext(ctx), c, p)
		}
		if r.Context().Err() == nil && (remaining <= 0 || errors.Is(ctx.Err(), context.DeadlineExceeded)) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			fmt.Fprint(w, "event: reauth\ndata: {\"reason\":\"token_expiring\",\"action\":\"refresh_and_reconnect\"}\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}
}

// eventStore preserves the store transaction boundary and broadcasts committed
// records. Each subscriber has a bounded queue; overflow closes the stream so
// Last-Event-ID replay can recover, rather than silently losing an event.
type eventStore struct {
	store.Store
	mu          sync.Mutex
	subscribers map[chan []store.Record]struct{}
}
type eventTx struct {
	store.Tx
	events map[string]store.Record
}

type webhookDelivery struct {
	Job        pool.Job           `json:"job"`
	Webhook    pool.WebhookConfig `json:"webhook"`
	Event      string             `json:"event"`
	DeliveryID string             `json:"deliveryId"`
	Attempts   int                `json:"attempts"`
	Status     string             `json:"status"`
	Delivered  bool               `json:"delivered,omitempty"`
	UpdatedAt  time.Time          `json:"updatedAt"`
	Error      string             `json:"error,omitempty"`
	Payload    json.RawMessage    `json:"payload,omitempty"`
}

func (s *Server) claimWebhook(ctx context.Context, key string, delivery *webhookDelivery) bool {
	claimed := false
	err := s.Engine.Store.Update(ctx, func(tx store.Tx) error {
		var current webhookDelivery
		if err := tx.Get(store.WebhookDeliveries, key, &current); err != nil {
			return err
		}
		now := time.Now().UTC()
		if current.Delivered || current.Status == "failed" || current.Status == "delivering" || current.UpdatedAt.After(now) {
			return nil
		}
		current.Status = "delivering"
		current.Attempts++
		current.UpdatedAt = now.Add(36*time.Minute + 50*time.Second)
		if err := tx.Put(store.WebhookDeliveries, key, current); err != nil {
			return err
		}
		*delivery = current
		claimed = true
		return nil
	})
	if err != nil {
		// An unavailable delivery store must not fail silently: the task is
		// left unclaimed for a later delivery attempt without losing the error.
		s.Log.Warn("webhook.claimFailed", "error", err)
		return false
	}
	return claimed
}

func (t *eventTx) Put(bucket, key string, value any) error {
	if err := t.Tx.Put(bucket, key, value); err != nil {
		return err
	}
	if bucket == store.Events {
		b, err := json.Marshal(value)
		if err != nil {
			return err
		}
		t.events[key] = store.Record{Key: key, Value: b}
		var event pool.Event
		if err := json.Unmarshal(b, &event); err == nil && (event.Type == "job.done" || event.Type == "job.failed") {
			var job pool.Job
			if err := t.Tx.Get(store.Jobs, event.JobID, &job); err == nil && job.Request.Webhook != nil {
				delivery := webhookDelivery{
					Job: job, Webhook: *job.Request.Webhook, Event: event.Type,
					DeliveryID: event.ID, Status: "pending", UpdatedAt: time.Now().UTC(),
				}
				delivery.Payload, err = pool.WebhookBodyAt(job, event.ID, event.At, event.Type)
				if err != nil {
					return err
				}
				if err := t.Tx.Put(store.WebhookDeliveries, event.ID, delivery); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// PutIfAbsent forwards the optional atomic create contract across the
// event-store wrapper. Without this, a first recompute settings write cannot
// use durable CAS and the admin route fails with 500 (N4/A04).
func (t *eventTx) PutIfAbsent(bucket, key string, value any) (bool, error) {
	atomic, ok := t.Tx.(store.PutIfAbsentTx)
	if !ok || bucket == store.Events {
		// Never silently bypass event broadcast semantics through this path.
		return false, store.ErrNotImplemented
	}
	return atomic.PutIfAbsent(bucket, key, value)
}

// Delete forwards the optional store.DeleteTx contract of the wrapped
// transaction, so the engine can remove records through this wrapper. T100
// needs it to drop a per-owner artifact reference and to delete a blob whose
// last reference went away. A wrapped transaction without removal support
// answers ErrNotImplemented instead of pretending to delete.
func (t *eventTx) Delete(bucket, key string) error {
	deleter, ok := t.Tx.(store.DeleteTx)
	if !ok {
		return store.ErrNotImplemented
	}
	return deleter.Delete(bucket, key)
}

// NextSequence forwards the optional store.SequenceTx contract of the wrapped
// transaction, so the PostgreSQL event sequence keeps answering through the event bus.
func (t *eventTx) NextSequence(name string) (uint64, error) {
	sequence, ok := t.Tx.(store.SequenceTx)
	if !ok {
		return 0, store.ErrNotImplemented
	}
	return sequence.NextSequence(name)
}
func (s *eventStore) Update(ctx context.Context, fn func(store.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var captured *eventTx
	err := s.Store.Update(ctx, func(tx store.Tx) error {
		captured = &eventTx{Tx: tx, events: make(map[string]store.Record)}
		return fn(captured)
	})
	if err != nil || captured == nil || len(captured.events) == 0 {
		return err
	}
	batch := make([]store.Record, 0, len(captured.events))
	for _, row := range captured.events {
		batch = append(batch, row)
	}
	sort.Slice(batch, func(i, j int) bool { return batch[i].Key < batch[j].Key })
	for ch := range s.subscribers {
		select {
		case ch <- batch:
		default:
			close(ch)
			delete(s.subscribers, ch)
		}
	}
	return nil
}
func (s *eventStore) subscribe() (chan []store.Record, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan []store.Record, 128)
	s.subscribers[ch] = struct{}{}
	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.subscribers[ch]; ok {
			delete(s.subscribers, ch)
			close(ch)
		}
	}
}

func (s *Server) webhookLoop() {
	updates, unsubscribe := s.eventBus.subscribe()
	defer unsubscribe()
	for batch := range updates {
		for _, row := range batch {
			var event pool.Event
			if err := json.Unmarshal(row.Value, &event); err != nil || (event.Type != "job.done" && event.Type != "job.failed") {
				continue
			}
			job, err := s.Engine.Get(context.Background(), event.PoolID, event.Owner, event.JobID)
			if err != nil || job.Request.Webhook == nil || !s.Config.WebhooksEnabled {
				continue
			}
			if len(job.Request.Webhook.Events) > 0 && !slices.Contains(job.Request.Webhook.Events, event.Type) {
				continue
			}
			var delivery webhookDelivery
			if err := s.Engine.Store.View(context.Background(), func(rd store.Reader) error {
				return rd.Get(store.WebhookDeliveries, row.Key, &delivery)
			}); err != nil || delivery.Status == "failed" || delivery.Delivered {
				continue
			}
			if s.claimWebhook(context.Background(), row.Key, &delivery) {
				s.deliverWebhook(row.Key, delivery)
			}
		}
	}
}

func (s *Server) deliverWebhook(key string, delivery webhookDelivery) {
	select {
	case s.webhookSem <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-s.webhookSem }()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		delivery.Error = ""
		payloadHex := ""
		if len(delivery.Payload) > 0 {
			payloadHex = fmt.Sprintf("%x", delivery.Payload)
		}
		if err := pool.DeliverWebhookAttempt(ctx, delivery.Webhook, delivery.Job, s.Config.WebhookAllowPrivateIPs, delivery.Attempts, delivery.Event, delivery.DeliveryID, payloadHex); err != nil {
			delivery.Error = "delivery failed"
			delivery.UpdatedAt = time.Now().UTC().Add(time.Minute)
			if pool.IsPermanentWebhookError(err) || delivery.Attempts >= 5 {
				delivery.Status = "failed"
			} else {
				delivery.Status = "pending"
			}
			s.Log.Error("webhook.delivery_failed", "jobId", delivery.Job.JobID, "error", delivery.Error)
			if err := s.Engine.Store.Update(context.Background(), func(tx store.Tx) error {
				if metricErr := s.recordWebhookOutboxMetric(tx, delivery); metricErr != nil {
					s.Log.Warn("webhook.metric", "error", metricErr, "jobId", delivery.Job.JobID)
				}
				return tx.Put(store.WebhookDeliveries, key, delivery)
			}); err != nil {
				s.Log.Warn("webhook.persist", "error", err, "jobId", delivery.Job.JobID)
			}
			return
		}
		delivery.Status = "delivered"
		delivery.Delivered = true
		delivery.UpdatedAt = time.Now().UTC()
		if err := s.Engine.Store.Update(context.Background(), func(tx store.Tx) error {
			if metricErr := s.recordWebhookOutboxMetric(tx, delivery); metricErr != nil {
				s.Log.Warn("webhook.metric", "error", metricErr, "jobId", delivery.Job.JobID)
			}
			return tx.Put(store.WebhookDeliveries, key, delivery)
		}); err != nil {
			s.Log.Warn("webhook.persist", "error", err, "jobId", delivery.Job.JobID)
		}
	}()
}

func (s *Server) drainWebhookOutbox(ctx context.Context) {
	var rows []store.Record
	if err := s.Engine.Store.View(ctx, func(rd store.Reader) error {
		var err error
		rows, err = rd.List(store.WebhookDeliveries)
		return err
	}); err != nil {
		return
	}
	now := time.Now().UTC()
	for _, row := range rows {
		var delivery webhookDelivery
		if json.Unmarshal(row.Value, &delivery) != nil || delivery.Delivered || delivery.Status == "failed" || delivery.Status == "delivering" || delivery.UpdatedAt.After(now) {
			continue
		}
		if s.claimWebhook(ctx, row.Key, &delivery) {
			s.deliverWebhook(row.Key, delivery)
		}
	}
}

// writeSSEError sends the terminal error frame of an event stream: the same
// code, message, hint, requestId and retryable as the REST problem body, in
// the five-field SSE shape of WO R17. It is best effort, because the fault may
// be the connection itself.
func (s *Server) writeSSEError(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	fields := httpx.ProblemFields(r, w, status, code, detail)
	frame, err := json.Marshal(map[string]any{
		"code":      fields["code"],
		"message":   fields["message"],
		"hint":      fields["hint"],
		"requestId": fields["requestId"],
		"retryable": fields["retryable"],
	})
	if err != nil {
		return
	}
	if _, err := fmt.Fprintf(w, "event: error\ndata: %s\n\n", frame); err != nil {
		return
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) events(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	last := r.Header.Get("Last-Event-ID")
	// last0 is the resume cursor of the client; last is the highest id this stream has
	// consumed. R18B review row 4: rows are deduplicated by exact id instead of by the
	// cursor, because a transaction may commit out of id order (another coordinator
	// consumed a higher id first): a row below the cursor can still be new, and skipping
	// it would lose the event forever. The cursor only ever advances forward.
	last0 := last
	sent := map[string]struct{}{}
	f, ok := w.(http.Flusher)
	if !ok {
		httpx.Problem(w, r, 500, "stream-unavailable", "Streaming unavailable.")
		return
	}
	// Subscribe before replay to close the snapshot/live race. Duplicate records
	// already in the snapshot are discarded by the sent set.
	updates, unsubscribe := s.eventBus.subscribe()
	defer unsubscribe()
	var rows []store.Record
	if err := s.Engine.Store.View(r.Context(), func(rd store.Reader) error {
		var err error
		rows, err = rd.List(store.Events)
		return err
	}); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, ": connected\n\n")
	f.Flush()
	match := func(filter, value string) bool {
		return filter == "" || strings.Contains(","+filter+",", ","+value+",")
	}
	send := func(batch []store.Record) error {
		for _, row := range batch {
			if row.Key <= last0 {
				continue
			}
			if _, seen := sent[row.Key]; seen {
				continue
			}
			if row.Key > last {
				last = row.Key // also advance past filtered records
			}
			sent[row.Key] = struct{}{}
			var v pool.Event
			if err := json.Unmarshal(row.Value, &v); err != nil {
				return err
			}
			if v.Owner != c.KeyID || v.PoolID != p || !match(r.URL.Query().Get("jobIds"), v.JobID) ||
				!match(r.URL.Query().Get("sessionIds"), v.SessionID) || !match(r.URL.Query().Get("types"), v.Type) {
				continue
			}
			if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", row.Key, v.Type, row.Value); err != nil {
				return err
			}
			f.Flush()
		}
		return nil
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	if err := send(rows); err != nil {
		return
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case batch, open := <-updates:
			if !open {
				s.writeSSEError(w, r, http.StatusServiceUnavailable, "service-unavailable", "The event stream fell behind; reconnect with the last event id.")
				return
			}
			if _, err := s.Auth.Validate(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")); err != nil {
				s.writeSSEError(w, r, http.StatusUnauthorized, "unauthorized", err.Error())
				return
			}
			if err := send(batch); err != nil {
				return
			}
		case <-ticker.C:
			if _, err := s.Auth.Validate(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")); err != nil {
				s.writeSSEError(w, r, http.StatusUnauthorized, "unauthorized", err.Error())
				return
			}
		}
	}
}
func (s *Server) openSession(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	var q pool.SessionRequest
	if !httpx.Decode(w, r, &q) {
		return
	}
	v, e := s.Engine.OpenSession(r.Context(), c.KeyID, r.Header.Get("Idempotency-Key"), q)
	if e != nil {
		httpx.Error(w, r, e)
		return
	}
	httpx.JSON(w, 201, v)
}
func (s *Server) getSession(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	v, e := s.Engine.GetSession(r.Context(), c.KeyID, r.PathValue("sessionId"))
	if e != nil {
		httpx.Error(w, r, e)
		return
	}
	httpx.JSON(w, 200, v)
}
func (s *Server) sessionTasks(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	var q pool.CreateRequest
	if !httpx.Decode(w, r, &q) {
		return
	}
	if q.Webhook != nil {
		if !s.Config.WebhooksEnabled {
			httpx.Error(w, r, pool.ErrUnsupported)
			return
		}
		if e := q.Webhook.Validate(s.Config.WebhookAllowPrivateIPs); e != nil {
			httpx.Error(w, r, pool.ErrInvalid)
			return
		}
	}
	v, e := s.Engine.SessionTasks(r.Context(), c.KeyID, r.PathValue("sessionId"), r.Header.Get("Idempotency-Key"), q)
	if e != nil {
		httpx.Error(w, r, e)
		return
	}
	ids := []string{}
	for _, t := range v.Tasks {
		ids = append(ids, t.TaskID)
	}
	httpx.JSON(w, 202, map[string]any{"sessionId": v.SessionID, "jobId": v.JobID, "batchId": v.JobID, "acceptedTaskIds": ids, "taskCount": len(ids), "state": "accepted", "acceptedAt": v.CreatedAt, "warnings": v.Warnings})
}
func (s *Server) closeSession(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	v := r.PathValue("action")
	if !strings.HasSuffix(v, ":close") {
		httpx.Problem(w, r, 404, "not-found", "Unknown session action.")
		return
	}
	q := struct {
		Reason  string `json:"reason"`
		GraceMS int    `json:"graceMs"`
		Drain   bool   `json:"drain"`
	}{Drain: true}
	if r.ContentLength != 0 && !httpx.Decode(w, r, &q) {
		return
	}
	if q.GraceMS != 0 {
		httpx.Error(w, r, pool.ErrUnsupported)
		return
	}
	out, e := s.Engine.CloseSession(r.Context(), c.KeyID, strings.TrimSuffix(v, ":close"), q.Drain)
	if e != nil {
		httpx.Error(w, r, e)
		return
	}
	httpx.JSON(w, 202, out)
}

// RunMaintenance belongs to the application lifetime, never a detached global goroutine.
func (s *Server) RunMaintenance(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if e := s.Engine.Sweep(ctx); e != nil && ctx.Err() == nil {
				s.Log.Error("lease.sweep", "error", e)
			}
			s.drainWebhookOutbox(ctx)
			// Deny entries are forgotten once their own expiry passes, so the list
			// cannot grow without bound. Uses the maintenance ctx, never a request.
			if n, e := s.Auth.PruneDenylist(ctx); e != nil && ctx.Err() == nil {
				s.Log.Error("token.denylist.prune", "error", e)
			} else if n > 0 {
				s.Log.Info("token.denylist.pruned", "count", n)
			}
		}
	}
}

// noStore marks static page responses no-cache so a deploy reaches every open tab
// on its next load (T107).
func noStore(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}
