// File: pools-core/internal/api/pool_settings_W10C.go (go)
// W10C-SERVER row 3: Director/admin pool settings for the verification mode,
// the redundant and sample rates, and the hostile-workload isolation mode.
package api

import (
	"errors"
	"net/http"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/httpx"
	"ccgsc/poolscore/internal/pool"
)

// poolSettingsRoutes registers the settings endpoints of both pools. The pool
// scoped aliases sit next to the existing per-pool admin routes; the
// /admin/v1/pools/{pool}/settings form names the pool in the path.
func (s *Server) poolSettingsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/v1/pools/{pool}/settings", s.secure("", "admin", "config:read", s.readPoolSettings))
	mux.HandleFunc("PUT /admin/v1/pools/{pool}/settings", s.secure("", "admin", "config:write", s.writePoolSettings))
	for _, spec := range []struct{ prefix, pool string }{{"/thread-pool/v1", pool.Thread}, {"/process-pool/v1", pool.Process}} {
		prefix, p := spec.prefix, spec.pool
		mux.HandleFunc("GET "+prefix+"/admin/settings", s.secure(p, "admin", "config:read", s.readPoolSettings))
		mux.HandleFunc("PUT "+prefix+"/admin/settings", s.secure(p, "admin", "config:write", s.writePoolSettings))
	}
}

// poolSettingsID resolves the pool of a settings request. The path form accepts
// the pool ids (tp/pp) and the page names (thread-pool/process-pool).
func poolSettingsID(r *http.Request, p string) (string, bool) {
	id := p
	if id == "" {
		id = r.PathValue("pool")
	}
	switch id {
	case "thread-pool":
		id = pool.Thread
	case "process-pool":
		id = pool.Process
	}
	if id != pool.Thread && id != pool.Process {
		return "", false
	}
	return id, true
}

func poolSettingsView(p string, s pool.PoolSettings) map[string]any {
	return map[string]any{
		"poolId":        p,
		"version":       s.Version,
		"verifyMode":    s.VerifyMode,
		"redundantRate": s.RedundantRate,
		"sampleRate":    s.SampleRate,
		"isolationMode": s.IsolationMode,
		"modes":         []string{string(pool.VerifyModeOff), string(pool.VerifyModeSample), string(pool.VerifyModeRedundant), string(pool.VerifyModeBoth)},
		"isolationModes": []string{
			string(pool.IsolationSeparateLock), string(pool.IsolationLockOnly),
		},
		// W10D row 1 (D-W10-11): the contributor controls of this pool.
		"contributorControls":     s.ContributorControls,
		"contributorControlModes": pool.ContributorControlsModes(),
	}
}

func (s *Server) readPoolSettings(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	id, ok := poolSettingsID(r, p)
	if !ok {
		httpx.Problem(w, r, 404, "not-found", "Unknown pool.")
		return
	}
	httpx.JSON(w, 200, poolSettingsView(id, s.Engine.PoolSettings(id)))
}

// writePoolSettings applies the fields present in the body. expectedVersion is
// the conflict rule of the existing settings endpoints: a stale value answers
// 409 and writes nothing.
func (s *Server) writePoolSettings(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	id, ok := poolSettingsID(r, p)
	if !ok {
		httpx.Problem(w, r, 404, "not-found", "Unknown pool.")
		return
	}
	var q struct {
		VerifyMode    *string  `json:"verifyMode"`
		RedundantRate *float64 `json:"redundantRate"`
		SampleRate    *float64 `json:"sampleRate"`
		IsolationMode *string  `json:"isolationMode"`
		// W10D row 1 (D-W10-11): a supplied value must be one of the three
		// documented modes; an empty string is not "off", it is invalid.
		ContributorControls *string `json:"contributorControls"`
		ExpectedVersion     *int    `json:"expectedVersion"`
	}
	if !httpx.Decode(w, r, &q) {
		return
	}
	if q.ExpectedVersion != nil && *q.ExpectedVersion < 0 {
		httpx.Error(w, r, pool.ErrInvalid)
		return
	}
	// R11-D1: build only the named fields. The merge happens on the stored
	// record under settingsMu inside UpdatePoolSettings, so no snapshot read here
	// can clobber a concurrent write of another field.
	var delta pool.SettingsDelta
	if q.VerifyMode != nil {
		mode := pool.VerifyMode(*q.VerifyMode)
		delta.VerifyMode = &mode
	}
	if q.RedundantRate != nil {
		delta.RedundantRate = q.RedundantRate
	}
	if q.SampleRate != nil {
		delta.SampleRate = q.SampleRate
	}
	if q.IsolationMode != nil {
		mode := pool.IsolationMode(*q.IsolationMode)
		delta.IsolationMode = &mode
	}
	if q.ContributorControls != nil {
		controls, err := pool.ParseContributorControls(*q.ContributorControls)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		delta.ContributorControls = &controls
	}
	saved, err := s.Engine.UpdatePoolSettings(r.Context(), id, delta, q.ExpectedVersion)
	if err != nil {
		if errors.Is(err, pool.ErrSettingsConflict) {
			httpx.Problem(w, r, 409, "config-conflict", "Config version conflict.")
			return
		}
		httpx.Error(w, r, err)
		return
	}
	s.Log.Info("security.poolSettings.write", "director", c.User.ID, "poolId", id, "version", saved.Version, "verifyMode", saved.VerifyMode, "isolationMode", saved.IsolationMode, "contributorControls", saved.ContributorControls)
	httpx.JSON(w, 200, poolSettingsView(id, saved))
}
