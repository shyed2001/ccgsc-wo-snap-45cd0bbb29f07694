// Path: tests/anonymous_T96_test.go | Author: Grok 4.7 for Claude Opus 5.5 | 2026-11-20
// What/why: SQLite httptest of the anonymous contributor join of WO_T96.
// A volunteer tab is issued a token, registers, takes a lease and has its result
// accepted. The off switch, the per-IP issue rate, the per-IP live tab cap, the
// developer and admin refusal, and a deny-listed jti are covered here too.
package tests

import (
	"bytes"
	"ccgsc/poolscore/internal/api"
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/sqlite"
	"ccgsc/poolscore/internal/verify"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupAnonymous(t *testing.T, anon config.AnonymousContributorConfig) *fixture {
	t.Helper()
	db, e := sqlite.Open(filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = db.Close() })
	l := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine, e := pool.New(db, l, "s1", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	a := auth.New(db, config.Default().JWTSecret, "s1")
	cfg := config.Default()
	cfg.AnonymousContributors = anon
	s := &api.Server{Engine: engine, Auth: a, Log: l, Config: cfg}
	return &fixture{db, engine, a, s.Handler()}
}

func (f *fixture) callFrom(t *testing.T, method, path, token string, body any, want int, remote string) []byte {
	t.Helper()
	var b []byte
	switch v := body.(type) {
	case []byte:
		b = v
	case nil:
	default:
		var e error
		b, e = json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	if remote != "" {
		r.RemoteAddr = remote
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	if want >= 400 && w.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatal("missing RFC 9457 content type")
	}
	return w.Body.Bytes()
}

func anonymousClaims(t *testing.T, access string) auth.Claims {
	t.Helper()
	p := strings.Split(access, ".")
	if len(p) != 3 {
		t.Fatal("anonymous token is not a JWT")
	}
	b, e := base64.RawURLEncoding.DecodeString(p[1])
	if e != nil {
		t.Fatal(e)
	}
	var c auth.Claims
	if e = json.Unmarshal(b, &c); e != nil {
		t.Fatal(e)
	}
	return c
}

func problemCode(t *testing.T, body []byte) string {
	t.Helper()
	var p struct {
		Code string `json:"code"`
	}
	if e := json.Unmarshal(body, &p); e != nil {
		t.Fatal(e)
	}
	return p.Code
}

func (f *fixture) issueAnonymous(t *testing.T, base, remote string, want int) auth.Token {
	t.Helper()
	body := f.callFrom(t, http.MethodPost, base+"/workers/anonymous", "", nil, want, remote)
	if want != http.StatusOK {
		return auth.Token{}
	}
	return decode[auth.Token](t, body)
}

func TestAnonymousJoinRegisterLeaseResult(t *testing.T) {
	for _, base := range []string{"/thread-pool/v1", "/process-pool/v1"} {
		t.Run(base, func(t *testing.T) {
			f := setupAnonymous(t, config.DefaultAnonymousContributorConfig())
			cfg := decode[struct {
				AnonymousJoin bool `json:"anonymousJoin"`
			}](t, f.request(t, http.MethodGet, "/pools/runner-config", "", nil, http.StatusOK, nil))
			if !cfg.AnonymousJoin {
				t.Fatal("runner-config anonymousJoin must be true when the join is enabled")
			}

			token := f.issueAnonymous(t, base, "198.51.100.20:4242", http.StatusOK)
			if token.ExpiresIn != 900 || token.TokenType != "Bearer" || token.AccessToken == "" {
				t.Fatalf("bad anonymous token view: %+v", token)
			}
			if strings.Join(token.Scopes, ",") != strings.Join(auth.ContributorWorkerScopes, ",") {
				t.Fatalf("scopes = %v, want %v", token.Scopes, auth.ContributorWorkerScopes)
			}
			claims := anonymousClaims(t, token.AccessToken)
			if !claims.Anonymous || !auth.IsAnonymousID(claims.Subject) || claims.KeyID != claims.Subject {
				t.Fatalf("bad anonymous claims: %+v", claims)
			}
			if claims.User.AccountStore != "B" || !claims.HasRole("contributor") || len(claims.User.Roles) != 1 {
				t.Fatalf("anonymous user is not a store B contributor: %+v", claims.User)
			}
			if len(claims.Scopes) != 1 || claims.Scopes[0] != "worker" {
				t.Fatalf("claim scopes = %v, want [worker]", claims.Scopes)
			}
			e := f.db.View(context.Background(), func(r store.Reader) error {
				var k auth.Key
				err := r.Get(store.Keys, claims.KeyID, &k)
				if errors.Is(err, store.ErrNotFound) {
					return nil
				}
				if err != nil {
					return err
				}
				return errors.New("anonymous identity was stored as an API key")
			})
			if e != nil {
				t.Fatal(e)
			}

			worker := decode[pool.Worker](t, f.callFrom(t, http.MethodPost, base+"/workers/register", token.AccessToken, map[string]any{"hostId": "anon-host", "slots": 1}, http.StatusCreated, "198.51.100.20:4242"))
			if !worker.Anonymous || worker.TabID == "" || worker.Owner != claims.Subject {
				t.Fatalf("bad anonymous worker: %+v", worker)
			}

			_, admin := f.key(t, "admin")
			issued := decode[struct {
				APIKey string `json:"apiKey"`
			}](t, f.request(t, http.MethodPost, base+"/auth/keys", admin, map[string]any{"user": auth.User{ID: "developer", AccountStore: "B", Roles: []string{"developer"}}, "scopes": []string{"artifacts:write", "jobs:write", "jobs:read"}}, http.StatusCreated, nil))
			dev := decode[auth.Token](t, f.request(t, http.MethodPost, base+"/auth/token", "", map[string]any{"apiKey": issued.APIKey}, http.StatusOK, nil))
			module := f.artifact(t, base, dev.AccessToken)
			zero := 0.0
			job := decode[pool.Job](t, f.request(t, http.MethodPost, base+"/jobs", dev.AccessToken, pool.CreateRequest{
				ModuleArtifactIDs: []string{module},
				Tasks:             []pool.TaskSpec{{TaskID: "one", Input: json.RawMessage(`{"json":{"n":1}}`)}},
				BatchSize:         1,
				Needs:             "cpu",
				RerunPercent:      &zero,
			}, http.StatusCreated, nil))
			assignment := decode[pool.Assignment](t, f.request(t, http.MethodPost, base+"/workers/"+worker.TabID+"/leases", token.AccessToken, nil, http.StatusOK, nil))
			if assignment.Lease.TaskID == "" || assignment.Lease.Token == "" {
				t.Fatalf("lease was not issued: %+v", assignment.Lease)
			}
			output := []byte(`{"json":{"answer":42}}`)
			result := decode[pool.Result](t, f.request(t, http.MethodPost, base+"/workers/"+worker.TabID+"/results", token.AccessToken, pool.Submission{
				Lease:        assignment.Lease,
				TaskEnvelope: assignment.TaskEnvelope, LeaseReceipt: assignment.LeaseReceipt,
				Output: json.RawMessage(output),
				SHA256: verify.SHA256(output),
			}, http.StatusOK, nil))
			if result.Status != "done" {
				t.Fatalf("result status = %q, want done", result.Status)
			}
			got := decode[pool.Job](t, f.request(t, http.MethodGet, base+"/jobs/"+job.JobID, dev.AccessToken, nil, http.StatusOK, nil))
			if got.State != "completed" || got.Progress.Completed != 1 {
				t.Fatalf("job was not accepted to completion: %+v", got.Progress)
			}
		})
	}
}

func TestAnonymousJoinDisabled(t *testing.T) {
	off := config.DefaultAnonymousContributorConfig()
	off.Enabled = false
	f := setupAnonymous(t, off)
	cfg := decode[struct {
		AnonymousJoin bool `json:"anonymousJoin"`
	}](t, f.request(t, http.MethodGet, "/pools/runner-config", "", nil, http.StatusOK, nil))
	if cfg.AnonymousJoin {
		t.Fatal("runner-config anonymousJoin must be false when the join is off")
	}
	for _, base := range []string{"/thread-pool/v1", "/process-pool/v1"} {
		body := f.callFrom(t, http.MethodPost, base+"/workers/anonymous", "", nil, http.StatusForbidden, "198.51.100.21:4242")
		if problemCode(t, body) != "anonymous-disabled" {
			t.Fatalf("%s off switch code = %s", base, problemCode(t, body))
		}
	}
}

func TestAnonymousPerIPRateAndTabCap(t *testing.T) {
	t.Run("rate", func(t *testing.T) {
		limits := config.DefaultAnonymousContributorConfig()
		limits.IssueRatePerMinute = 2
		f := setupAnonymous(t, limits)
		const remote = "198.51.100.30:4242"
		f.issueAnonymous(t, "/thread-pool/v1", remote, http.StatusOK)
		f.issueAnonymous(t, "/thread-pool/v1", remote, http.StatusOK)
		body := f.callFrom(t, http.MethodPost, "/thread-pool/v1/workers/anonymous", "", nil, http.StatusTooManyRequests, remote)
		if problemCode(t, body) != "rate-limit-exceeded" {
			t.Fatalf("rate code = %s", problemCode(t, body))
		}
		// A different client IP still has its own quota.
		f.issueAnonymous(t, "/thread-pool/v1", "198.51.100.31:4242", http.StatusOK)
	})

	t.Run("tab cap", func(t *testing.T) {
		limits := config.DefaultAnonymousContributorConfig()
		limits.IssueRatePerMinute = 30
		limits.MaxLiveTabsPerIP = 1
		f := setupAnonymous(t, limits)
		const remote = "198.51.100.40:4242"
		first := f.issueAnonymous(t, "/thread-pool/v1", remote, http.StatusOK)
		second := f.issueAnonymous(t, "/thread-pool/v1", remote, http.StatusOK)
		worker := decode[pool.Worker](t, f.callFrom(t, http.MethodPost, "/thread-pool/v1/workers/register", first.AccessToken, map[string]any{"hostId": "cap-a", "slots": 1}, http.StatusCreated, remote))
		if !worker.Anonymous {
			t.Fatal("first tab was not marked anonymous")
		}
		body := f.callFrom(t, http.MethodPost, "/thread-pool/v1/workers/register", second.AccessToken, map[string]any{"hostId": "cap-b", "slots": 1}, http.StatusTooManyRequests, remote)
		if problemCode(t, body) != "anonymous-quota-exceeded" {
			t.Fatalf("tab cap code = %s", problemCode(t, body))
		}
		// The next issue from the same IP is refused because the live tab fills the cap.
		body = f.callFrom(t, http.MethodPost, "/thread-pool/v1/workers/anonymous", "", nil, http.StatusTooManyRequests, remote)
		if problemCode(t, body) != "anonymous-quota-exceeded" {
			t.Fatalf("issue cap code = %s", problemCode(t, body))
		}
		// Another IP can still join and register.
		other := f.issueAnonymous(t, "/thread-pool/v1", "198.51.100.41:4242", http.StatusOK)
		decode[pool.Worker](t, f.callFrom(t, http.MethodPost, "/thread-pool/v1/workers/register", other.AccessToken, map[string]any{"hostId": "cap-c", "slots": 1}, http.StatusCreated, "198.51.100.41:4242"))
	})
}

// WO_T96B: a tab at the live-tab cap renews its token and keeps its subject, so its
// lease and heartbeat calls on the existing tab id still pass. A token expired by
// up to 60 s still renews to the same subject; older tokens get a fresh identity.
func TestAnonymousRenewalKeepsSubjectAtCap(t *testing.T) {
	limits := config.DefaultAnonymousContributorConfig()
	limits.IssueRatePerMinute = 30
	limits.MaxLiveTabsPerIP = 1
	f := setupAnonymous(t, limits)
	const remote = "198.51.100.70:4242"
	first := f.issueAnonymous(t, "/thread-pool/v1", remote, http.StatusOK)
	sub := anonymousClaims(t, first.AccessToken).Subject
	worker := decode[pool.Worker](t, f.callFrom(t, http.MethodPost, "/thread-pool/v1/workers/register", first.AccessToken, map[string]any{"hostId": "renew", "slots": 1}, http.StatusCreated, remote))

	renewed := decode[auth.Token](t, f.callFrom(t, http.MethodPost, "/thread-pool/v1/workers/anonymous", first.AccessToken, nil, http.StatusOK, remote))
	if got := anonymousClaims(t, renewed.AccessToken).Subject; got != sub {
		t.Fatalf("renewal subject = %s, want %s", got, sub)
	}
	f.callFrom(t, http.MethodPost, "/thread-pool/v1/workers/"+worker.TabID+"/leases", renewed.AccessToken, nil, http.StatusNoContent, remote)

	base := time.Now()
	f.auth.Now = func() time.Time { return base.Add(930 * time.Second) } // 30 s past exp
	grace := decode[auth.Token](t, f.callFrom(t, http.MethodPost, "/thread-pool/v1/workers/anonymous", renewed.AccessToken, nil, http.StatusOK, remote))
	if got := anonymousClaims(t, grace.AccessToken).Subject; got != sub {
		t.Fatalf("grace renewal subject = %s, want %s", got, sub)
	}

	f.auth.Now = func() time.Time { return base.Add(1800 * time.Second) } // far past exp of grace token
	stale := f.callFrom(t, http.MethodPost, "/thread-pool/v1/workers/anonymous", renewed.AccessToken, nil, http.StatusTooManyRequests, remote)
	if problemCode(t, stale) != "anonymous-quota-exceeded" {
		t.Fatalf("stale token must be treated as a new identity at the cap, code = %s", problemCode(t, stale))
	}
}

func TestAnonymousTokenForbiddenOnDeveloperAndAdminRoutes(t *testing.T) {
	f := setupAnonymous(t, config.DefaultAnonymousContributorConfig())
	token := f.issueAnonymous(t, "/thread-pool/v1", "198.51.100.50:4242", http.StatusOK)
	developerRoutes := []struct{ method, path string }{
		{http.MethodPost, "/thread-pool/v1/jobs"},
		{http.MethodGet, "/thread-pool/v1/capabilities"},
		{http.MethodPost, "/thread-pool/v1/auth/keys"},
		{http.MethodPost, "/admin/v1/tokens:revoke"},
		{http.MethodPost, "/admin/v1/task-keys:rotate"},
	}
	for _, route := range developerRoutes {
		body := f.callFrom(t, route.method, route.path, token.AccessToken, map[string]any{}, http.StatusForbidden, "198.51.100.50:4242")
		if problemCode(t, body) != "forbidden" {
			t.Fatalf("%s %s code = %s", route.method, route.path, problemCode(t, body))
		}
	}
}

func TestAnonymousDeniedJTIUnauthorized(t *testing.T) {
	f := setupAnonymous(t, config.DefaultAnonymousContributorConfig())
	token := f.issueAnonymous(t, "/thread-pool/v1", "198.51.100.60:4242", http.StatusOK)
	claims := anonymousClaims(t, token.AccessToken)
	_, admin := f.key(t, "admin")
	f.request(t, http.MethodPost, "/admin/v1/tokens:revoke", admin, map[string]any{"tokenId": claims.ID, "reason": "t96 deny"}, http.StatusCreated, nil)
	body := f.callFrom(t, http.MethodPost, "/thread-pool/v1/workers/register", token.AccessToken, map[string]any{"hostId": "denied", "slots": 1}, http.StatusUnauthorized, "198.51.100.60:4242")
	if problemCode(t, body) != "unauthorized" {
		t.Fatalf("denied jti code = %s", problemCode(t, body))
	}
}

// T107: static pages carry Cache-Control: no-cache so a deploy reaches open tabs.
func TestPoolsStaticNoCache(t *testing.T) {
	f := setupAnonymous(t, config.DefaultAnonymousContributorConfig())
	for _, p := range []string{"/pools/contribute.html", "/pools/js/contribute.js", "/nav/nav.js"} {
		r := httptest.NewRequest(http.MethodGet, p, nil)
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s: code %d cache-control %q", p, w.Code, w.Header().Get("Cache-Control"))
		}
	}
}

// W7 review: renewal is single use; a second renewal with the same bearer does
// not keep the subject, so a copied token cannot fork a renewal chain.
func TestAnonymousRenewalSingleUse(t *testing.T) {
	f := setupAnonymous(t, config.DefaultAnonymousContributorConfig())
	const remote = "198.51.100.80:4242"
	first := f.issueAnonymous(t, "/thread-pool/v1", remote, http.StatusOK)
	sub := anonymousClaims(t, first.AccessToken).Subject
	renewed := decode[auth.Token](t, f.callFrom(t, http.MethodPost, "/thread-pool/v1/workers/anonymous", first.AccessToken, nil, http.StatusOK, remote))
	if anonymousClaims(t, renewed.AccessToken).Subject != sub {
		t.Fatal("first renewal must keep the subject")
	}
	again := decode[auth.Token](t, f.callFrom(t, http.MethodPost, "/thread-pool/v1/workers/anonymous", first.AccessToken, nil, http.StatusOK, remote))
	if anonymousClaims(t, again.AccessToken).Subject == sub {
		t.Fatal("a reused bearer must not renew the same subject again")
	}
	f.callFrom(t, http.MethodPost, "/thread-pool/v1/workers/register", first.AccessToken, map[string]any{"hostId": "x", "slots": 1}, http.StatusUnauthorized, remote)
}

// TestAnonymousRenewalConcurrentSingleWinner (W7 review, qwen): N concurrent
// renewals with one bearer must keep the subject for exactly one request. The
// jti is consumed atomically before any token is minted.
func TestAnonymousRenewalConcurrentSingleWinner(t *testing.T) {
	f := setupAnonymous(t, config.DefaultAnonymousContributorConfig())
	const remote = "198.51.100.81:4242"
	first := f.issueAnonymous(t, "/thread-pool/v1", remote, http.StatusOK)
	sub := anonymousClaims(t, first.AccessToken).Subject
	const n = 6
	type res struct {
		code int
		body []byte
	}
	out := make(chan res, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			<-start
			r := httptest.NewRequest(http.MethodPost, "/thread-pool/v1/workers/anonymous", nil)
			r.RemoteAddr = remote
			r.Header.Set("Authorization", "Bearer "+first.AccessToken)
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, r)
			out <- res{w.Code, w.Body.Bytes()}
		}()
	}
	close(start)
	same := 0
	for i := 0; i < n; i++ {
		r := <-out
		if r.code != http.StatusOK {
			continue // a loser may hit the live-tab cap on the fresh-identity path
		}
		var tok auth.Token
		if err := json.Unmarshal(r.body, &tok); err != nil {
			t.Fatal(err)
		}
		if anonymousClaims(t, tok.AccessToken).Subject == sub {
			same++
		}
	}
	if same != 1 {
		t.Fatalf("renewals that kept the subject = %d, want exactly 1", same)
	}
}

// TestAnonymousRateLimitAtomic (T110): concurrent fresh joins from one IP never
// exceed the per-minute issue rate.
func TestAnonymousRateLimitAtomic(t *testing.T) {
	cfg := config.DefaultAnonymousContributorConfig()
	cfg.IssueRatePerMinute = 3
	cfg.MaxLiveTabsPerIP = 100
	f := setupAnonymous(t, cfg)
	const n = 12
	codes := make(chan int, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			<-start
			r := httptest.NewRequest(http.MethodPost, "/thread-pool/v1/workers/anonymous", nil)
			r.RemoteAddr = "198.51.100.90:5000"
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, r)
			codes <- w.Code
		}()
	}
	close(start)
	ok := 0
	for i := 0; i < n; i++ {
		if <-codes == http.StatusOK {
			ok++
		}
	}
	if ok != 3 {
		t.Fatalf("issued %d tokens, want exactly the rate 3", ok)
	}
}

// TestAnonymousSessionDeadline (T110): renewal keeps the absolute session end;
// once it has passed, the bearer no longer renews the same subject.
func TestAnonymousSessionDeadline(t *testing.T) {
	f := setupAnonymous(t, config.DefaultAnonymousContributorConfig())
	const remote = "198.51.100.91:4242"
	first := f.issueAnonymous(t, "/thread-pool/v1", remote, http.StatusOK)
	c := anonymousClaims(t, first.AccessToken)
	if c.SessionExpires == 0 || c.SessionExpires > time.Now().Add(auth.AnonymousSessionMax+time.Minute).Unix() {
		t.Fatalf("fresh session end = %d", c.SessionExpires)
	}
	renewed := decode[auth.Token](t, f.callFrom(t, http.MethodPost, "/thread-pool/v1/workers/anonymous", first.AccessToken, nil, http.StatusOK, remote))
	rc := anonymousClaims(t, renewed.AccessToken)
	if rc.Subject != c.Subject || rc.SessionExpires != c.SessionExpires {
		t.Fatalf("renewal must keep subject and session end: %+v vs %+v", rc, c)
	}
	// A token whose session already ended does not renew the same subject.
	ended, err := f.auth.IssueAnonymousSession(c.Subject, auth.ContributorWorkerScopes, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	next := decode[auth.Token](t, f.callFrom(t, http.MethodPost, "/thread-pool/v1/workers/anonymous", ended.AccessToken, nil, http.StatusOK, remote))
	if anonymousClaims(t, next.AccessToken).Subject == c.Subject {
		t.Fatal("a bearer past its session end must not renew the subject")
	}
}

// TestAnonymousTabCapPerSubject (T110): one anonymous token registers at most
// AnonymousMaxTabsPerSubject distinct hosts per pool, whatever its client IP.
func TestAnonymousTabCapPerSubject(t *testing.T) {
	cfg := config.DefaultAnonymousContributorConfig()
	cfg.MaxLiveTabsPerIP = 100
	f := setupAnonymous(t, cfg)
	tok := f.issueAnonymous(t, "/thread-pool/v1", "198.51.100.92:1", http.StatusOK)
	for i := 0; i < pool.AnonymousMaxTabsPerSubject; i++ {
		f.callFrom(t, http.MethodPost, "/thread-pool/v1/workers/register", tok.AccessToken,
			map[string]any{"hostId": "host-" + string(rune('a'+i)), "slots": 1}, http.StatusCreated, "198.51.100.1"+string(rune('0'+i))+":1")
	}
	r := httptest.NewRequest(http.MethodPost, "/thread-pool/v1/workers/register", strings.NewReader(`{"hostId":"host-z","slots":1}`))
	r.RemoteAddr = "203.0.113.7:1"
	r.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code < 400 {
		t.Fatalf("tab beyond the per-subject cap: got %d: %s", w.Code, w.Body.String())
	}
}
