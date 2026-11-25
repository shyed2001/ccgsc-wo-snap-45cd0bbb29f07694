// Path: internal/api/upload_scan_F8.go | Author: hermes (MiniMax-M3) for the owner | 2026-11-18
// What/why: T42 F8 HTTP surface of the module upload scan and the admin review gate:
// the upload route runs the scan, the admin routes read the review queue, record one
// review decision and read or write the scan configuration.
// Sources: WWPP-R36.1, R12.1 (review quorum), R13.1 (owner code exempt), R10.0 (no shared memory).
package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/httpx"
	"ccgsc/poolscore/internal/pool"
)

// artifactReviewView is the developer visible projection of one artifact. The scan
// report and the review state are part of it; the module bytes never are.
func artifactReviewView(a pool.Artifact) map[string]any {
	return map[string]any{
		"artifactId":   a.ArtifactID,
		"sha256":       a.SHA256,
		"owner":        a.Owner,
		"sizeBytes":    a.SizeBytes,
		"contentType":  a.ContentType,
		"complete":     a.Complete,
		"createdAt":    a.CreatedAt,
		"reviewState":  a.ReviewState,
		"reviewReason": a.ReviewReason,
		"reviewers":    a.Reviewers,
		"reviewQuorum": a.ReviewQuorum,
		"reviewedAt":   a.ReviewedAt,
		"scan":         a.Scan,
	}
}

// uploadScanRoutes registers the F8 routes of one pool prefix. The upload route is
// registered by the shared route table of api.go; the admin routes live here.
func (s *Server) uploadScanRoutes(mux *http.ServeMux, prefix, p string) {
	// The review queue and the scan report of one artifact.
	mux.HandleFunc("GET "+prefix+"/admin/reviews", s.secure(p, "admin", "artifacts:read", s.reviewQueue))
	mux.HandleFunc("GET "+prefix+"/admin/reviews/{artifactId}", s.secure(p, "admin", "artifacts:read", s.reviewArtifactView))
	// ServeMux cannot attach :rescan to a wildcard segment; the complete segment
	// is validated by the handler, like the job :cancel route.
	mux.HandleFunc("POST "+prefix+"/admin/reviews/{action}", s.secure(p, "admin", "artifacts:write", s.reviewArtifactAction))
	// The scan configuration of the pool.
	mux.HandleFunc("GET "+prefix+"/admin/upload-scan", s.secure(p, "admin", "artifacts:read", s.readUploadScanConfig))
	mux.HandleFunc("PUT "+prefix+"/admin/upload-scan", s.secure(p, "admin", "artifacts:write", s.writeUploadScanConfig))
}

// reviewQueue lists the artifacts of one review state.
func (s *Server) reviewQueue(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	state := r.URL.Query().Get("state")
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 1000 {
			httpx.Error(w, r, pool.ErrInvalid)
			return
		}
		limit = v
	}
	items, err := s.Engine.ReviewQueue(r.Context(), state, limit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, a := range items {
		out = append(out, artifactReviewView(a))
	}
	httpx.JSON(w, 200, map[string]any{"items": out, "state": state, "count": len(out)})
}

// reviewArtifactView returns the scan report and the review state of one artifact.
func (s *Server) reviewArtifactView(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	a, err := s.Engine.Artifact(r.Context(), r.PathValue("artifactId"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, 200, artifactReviewView(a))
}

// reviewArtifactAction dispatches the POST review routes. The decision route uses
// the artifact id as its segment; the rescan route appends ":rescan".
func (s *Server) reviewArtifactAction(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	segment := r.PathValue("action")
	if strings.HasSuffix(segment, ":rescan") {
		s.rescanArtifact(w, r, c, p, strings.TrimSuffix(segment, ":rescan"))
		return
	}
	if strings.Contains(segment, ":") {
		httpx.Problem(w, r, 404, "not-found", "Unknown review action.")
		return
	}
	s.reviewArtifactDecision(w, r, c, p, segment)
}

// reviewArtifactDecision records one admin review decision.
func (s *Server) reviewArtifactDecision(w http.ResponseWriter, r *http.Request, c auth.Claims, p, artifactID string) {
	var q struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if !httpx.Decode(w, r, &q) {
		return
	}
	if q.Decision != pool.ReviewDecisionApprove && q.Decision != pool.ReviewDecisionReject {
		httpx.Error(w, r, pool.ErrInvalid)
		return
	}
	reviewer := c.User.ID
	if reviewer == "" {
		reviewer = c.Subject
	}
	a, err := s.Engine.ReviewArtifact(r.Context(), artifactID, reviewer, q.Decision, q.Reason)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	s.Log.Info("security.artifact.review", "artifactId", a.ArtifactID, "reviewer", reviewer, "decision", q.Decision, "reviewState", a.ReviewState)
	httpx.JSON(w, 200, artifactReviewView(a))
}

// rescanArtifact runs the upload scan again over a stored artifact.
func (s *Server) rescanArtifact(w http.ResponseWriter, r *http.Request, c auth.Claims, p, artifactID string) {
	a, err := s.Engine.RescanArtifact(r.Context(), artifactID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	s.Log.Info("security.artifact.rescan", "artifactId", a.ArtifactID, "reviewState", a.ReviewState, "reviewer", c.User.ID)
	httpx.JSON(w, 200, artifactReviewView(a))
}

// readUploadScanConfig returns the active scan configuration of the pool.
func (s *Server) readUploadScanConfig(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	cfg := s.Engine.UploadScanConfig()
	httpx.JSON(w, 200, map[string]any{
		"enabled":            cfg.Enabled,
		"maxBytes":           cfg.MaxBytes,
		"requireWasmMagic":   cfg.RequireWasmMagic,
		"allowedImports":     cfg.AllowedImports,
		"allowSharedMemory":  cfg.AllowSharedMemory,
		"requireReview":      cfg.RequireReview,
		"reviewQuorum":       cfg.ReviewQuorum,
		"maxWasmBytes":       cfg.MaxWASMBytes,
		"maxJsBytes":         cfg.MaxJSBytes,
		"maxJsonBytes":       cfg.MaxJSONBytes,
		"maxZipEntries":      cfg.MaxZipEntries,
		"maxZipUncompressed": cfg.MaxZipUncompressed,
		"maxWasmPages":       cfg.MaxWASMPages,
		"reviewStates":       []string{pool.ReviewNotRequired, pool.ReviewPending, pool.ReviewApproved, pool.ReviewRejected},
	})
}

// writeUploadScanConfig stores a new scan configuration. Every field is optional:
// an omitted field keeps its current value, an explicit value is applied as given.
// The whole configuration is replaced, so an admin can tighten or relax every
// switch in one call.
func (s *Server) writeUploadScanConfig(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	var q struct {
		Enabled            *bool     `json:"enabled"`
		MaxBytes           *int64    `json:"maxBytes"`
		RequireWasmMagic   *bool     `json:"requireWasmMagic"`
		AllowedImports     *[]string `json:"allowedImports"`
		AllowSharedMemory  *bool     `json:"allowSharedMemory"`
		RequireReview      *bool     `json:"requireReview"`
		ReviewQuorum       *int      `json:"reviewQuorum"`
		MaxWASMBytes       *int64    `json:"maxWasmBytes"`
		MaxJSBytes         *int64    `json:"maxJsBytes"`
		MaxJSONBytes       *int64    `json:"maxJsonBytes"`
		MaxZipEntries      *int      `json:"maxZipEntries"`
		MaxZipUncompressed *int64    `json:"maxZipUncompressed"`
		MaxWASMPages       *uint64   `json:"maxWasmPages"`
	}
	if !httpx.Decode(w, r, &q) {
		return
	}
	cfg := s.Engine.UploadScanConfig()
	if q.Enabled != nil {
		cfg.Enabled = *q.Enabled
	}
	if q.MaxBytes != nil {
		cfg.MaxBytes = *q.MaxBytes
	}
	if q.RequireWasmMagic != nil {
		cfg.RequireWasmMagic = *q.RequireWasmMagic
	}
	if q.AllowedImports != nil {
		cfg.AllowedImports = *q.AllowedImports
	}
	if q.AllowSharedMemory != nil {
		cfg.AllowSharedMemory = *q.AllowSharedMemory
	}
	if q.RequireReview != nil {
		cfg.RequireReview = *q.RequireReview
	}
	if q.ReviewQuorum != nil {
		cfg.ReviewQuorum = *q.ReviewQuorum
	}
	if q.MaxWASMBytes != nil {
		cfg.MaxWASMBytes = *q.MaxWASMBytes
	}
	if q.MaxJSBytes != nil {
		cfg.MaxJSBytes = *q.MaxJSBytes
	}
	if q.MaxJSONBytes != nil {
		cfg.MaxJSONBytes = *q.MaxJSONBytes
	}
	if q.MaxZipEntries != nil {
		cfg.MaxZipEntries = *q.MaxZipEntries
	}
	if q.MaxZipUncompressed != nil {
		cfg.MaxZipUncompressed = *q.MaxZipUncompressed
	}
	if q.MaxWASMPages != nil {
		cfg.MaxWASMPages = *q.MaxWASMPages
	}
	if err := s.Engine.SetUploadScanConfig(r.Context(), cfg); err != nil {
		httpx.Error(w, r, err)
		return
	}
	s.Log.Info("security.uploadScan.config", "reviewer", c.User.ID, "enabled", cfg.Enabled, "requireReview", cfg.RequireReview, "reviewQuorum", cfg.ReviewQuorum)
	s.readUploadScanConfig(w, r, c, p)
}

// uploadScanProblem writes the RFC 9457 problem body of a failed upload scan.
// The standard envelope matches httpx.Problem; the scan report is added so the
// developer sees why the upload was refused.
func uploadScanProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string, scan *pool.ScanReport) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	body := httpx.ProblemFields(r, w, status, code, detail)
	body["scan"] = scan
	_ = json.NewEncoder(w).Encode(body)
}

// uploadScanOwnerCode reports whether the claims are owner code. Owner, admin and
// director code is auto-approved by the review gate (R13.1).
func uploadScanOwnerCode(c auth.Claims) bool {
	return c.HasRole("admin") || c.HasRole("director")
}

// uploadScanArtifactView renders the artifact metadata of one upload response.
func uploadScanArtifactView(a pool.Artifact) map[string]any {
	return map[string]any{
		"artifactId":    a.ArtifactID,
		"sha256":        a.SHA256,
		"owner":         a.Owner,
		"sizeBytes":     a.SizeBytes,
		"receivedBytes": a.ReceivedBytes,
		"nextOffset":    a.NextOffset,
		"complete":      a.Complete,
		"contentType":   a.ContentType,
		"createdAt":     a.CreatedAt,
		"reviewState":   a.ReviewState,
		"reviewReason":  a.ReviewReason,
		"reviewQuorum":  a.ReviewQuorum,
		"scan":          a.Scan,
	}
}
