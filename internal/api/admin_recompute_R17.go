// Path: internal/api/admin_recompute_R17.go | Author: Hermes (MiniMax-M3) for the owner | 2026-11-22
// What/why: WO R17-RECOMPUTE (G-R16-4) row 2 - the admin API of the per-pool
// recompute mode.
//
// The rules of the endpoint:
//   - the admin role only (the route gate refuses an anonymous caller; the
//     handler refuses every role but admin, exactly like the caps write),
//   - every value is validated (an unknown mode is a clear error),
//   - every write leaves an audit row with the actor,
//   - the record is durable and versioned like the caps settings: a stale
//     expectedVersion answers 409 and writes nothing, and the record survives a
//     restart of the server.
//
// The mode is per pool: "registry" runs the reference recompute through the
// generic registry, "off" leaves only the 2-of-N redundancy and the canaries.
package api

import (
	"errors"
	"net/http"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/httpx"
	"ccgsc/poolscore/internal/pool"
)

// recomputeRoutes registers the recompute mode endpoints of both pools.
func (s *Server) recomputeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/v1/pools/{pool}/recompute", s.secure("", "admin", "config:read", s.readRecomputeMode))
	mux.HandleFunc("PUT /admin/v1/pools/{pool}/recompute", s.secure("", "admin", "config:write", s.writeRecomputeMode))
	for _, spec := range []struct{ prefix, pool string }{{"/thread-pool/v1", pool.Thread}, {"/process-pool/v1", pool.Process}} {
		prefix, p := spec.prefix, spec.pool
		mux.HandleFunc("GET "+prefix+"/admin/recompute", s.secure(p, "admin", "config:read", s.readRecomputeMode))
		mux.HandleFunc("PUT "+prefix+"/admin/recompute", s.secure(p, "admin", "config:write", s.writeRecomputeMode))
	}
}

// recomputeModeID resolves the pool of a recompute request. The path form
// accepts the pool ids (tp/pp) and the page names (thread-pool/process-pool).
func recomputeModeID(r *http.Request, p string) (string, bool) {
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

// readRecomputeMode answers GET /admin/v1/pools/{pool}/recompute with the
// effective recompute mode of one pool.
func (s *Server) readRecomputeMode(w http.ResponseWriter, r *http.Request, _ auth.Claims, p string) {
	id, ok := recomputeModeID(r, p)
	if !ok {
		httpx.Problem(w, r, http.StatusNotFound, "not-found", "Unknown pool.")
		return
	}
	httpx.JSON(w, http.StatusOK, recomputeModeView(s, id))
}

// recomputeModeView is the reply of the read: the effective mode and the two
// R18 policies, the values the panel may offer, the version of the durable
// record and whether the mode came from the admin record or from the default.
func recomputeModeView(s *Server, id string) map[string]any {
	saved, hasSaved := s.Engine.SavedRecomputeSettings(id)
	version := 0
	if hasSaved {
		version = saved.Version
	}
	source := "default"
	if hasSaved {
		source = "admin"
	}
	kinds := []string{}
	if reg := s.Engine.RecomputeRegistry(); reg != nil {
		kinds = reg.Kinds()
	}
	return map[string]any{
		"poolId":            id,
		"mode":              s.Engine.RecomputeMode(id),
		"modes":             pool.RecomputeModes(),
		"busyPolicy":        s.Engine.RecomputeBusyPolicy(id),
		"busyPolicies":      pool.RecomputeBusyPolicies(),
		"malformedPolicy":   s.Engine.RecomputeMalformedPolicy(id),
		"malformedPolicies": pool.RecomputeMalformedPolicies(),
		"version":           version,
		"source":            source,
		// Existing pools can have saved sampling settings but no recompute-mode
		// record (this field did not exist before R17). Do not silently present
		// that upgrade as a deliberate admin choice. The default stays off
		// per owner WO; the operator must review/confirm the intended mode.
		"upgradeReviewRequired": !hasSaved && s.Engine.PoolSettings(id).SampleRate > 0,
		// The registered references of the platform. An empty list means the
		// platform has no reference, so the registry mode has nothing to
		// recompute with and the mode is effectively off.
		"referenceKinds": kinds,
	}
}

// recomputeModeWriteRequest is the body of the write. Every field is a
// pointer, so an absent field is "not changed" and a zero is a real value.
// The R18 policies (busyPolicy, malformedPolicy) ride the same record.
type recomputeModeWriteRequest struct {
	Mode            *string `json:"mode"`
	BusyPolicy      *string `json:"busyPolicy"`
	MalformedPolicy *string `json:"malformedPolicy"`
	ExpectedVersion *int    `json:"expectedVersion"`
}

// writeRecomputeMode applies the mode of the body and answers the new
// effective mode.
func (s *Server) writeRecomputeMode(w http.ResponseWriter, r *http.Request, c auth.Claims, p string) {
	id, ok := recomputeModeID(r, p)
	if !ok {
		httpx.Problem(w, r, http.StatusNotFound, "not-found", "Unknown pool.")
		return
	}
	// The addendum: the admin role only. The route gate already refuses an
	// anonymous caller; every other role is refused here.
	if !c.HasRole("admin") {
		httpx.Problem(w, r, http.StatusForbidden, "forbidden", "Only an admin may change the recompute mode.")
		return
	}
	var q recomputeModeWriteRequest
	if !httpx.Decode(w, r, &q) {
		return
	}
	if q.Mode == nil && q.BusyPolicy == nil && q.MalformedPolicy == nil {
		httpx.Problem(w, r, http.StatusBadRequest, "invalid-recompute", "The body carries no recompute change.")
		return
	}
	if q.ExpectedVersion != nil && *q.ExpectedVersion < 0 {
		httpx.Problem(w, r, http.StatusBadRequest, "invalid-recompute", "expectedVersion must not be negative.")
		return
	}
	// The values are validated here: an unknown mode or policy is a clear
	// error, never a silent fallback to the default.
	delta := pool.RecomputeSettingsDelta{}
	if q.Mode != nil {
		mode, err := pool.ParseRecomputeMode(*q.Mode)
		if err != nil {
			httpx.Problem(w, r, http.StatusBadRequest, "invalid-recompute", err.Error())
			return
		}
		delta.Mode = &mode
	}
	if q.BusyPolicy != nil {
		busy, err := pool.ParseRecomputeBusyPolicy(*q.BusyPolicy)
		if err != nil {
			httpx.Problem(w, r, http.StatusBadRequest, "invalid-recompute", err.Error())
			return
		}
		delta.BusyPolicy = &busy
	}
	if q.MalformedPolicy != nil {
		malformed, err := pool.ParseRecomputeMalformedPolicy(*q.MalformedPolicy)
		if err != nil {
			httpx.Problem(w, r, http.StatusBadRequest, "invalid-recompute", err.Error())
			return
		}
		delta.MalformedPolicy = &malformed
	}
	actor := c.User.ID
	if actor == "" {
		actor = c.Subject
	}
	// The record and the audit row are ONE transaction, and the engine applies
	// the record only after the commit: a failing store answers an error and
	// leaves the runtime unchanged.
	saved, err := s.Engine.CommitRecomputeSettings(r.Context(), id, delta, q.ExpectedVersion, actor)
	switch {
	case errors.Is(err, pool.ErrSettingsConflict):
		httpx.Problem(w, r, http.StatusConflict, "config-conflict", "Config version conflict.")
		return
	case errors.Is(err, pool.ErrInvalid):
		httpx.Problem(w, r, http.StatusBadRequest, "invalid-recompute", err.Error())
		return
	case err != nil:
		s.Log.Error("the recompute mode persist/audit transaction failed; nothing changed", "error", err, "actor", actor)
		httpx.Problem(w, r, http.StatusInternalServerError, "recompute-write-failed", "The recompute mode was not changed: the persist/audit transaction failed. Inspect the server before retrying.")
		return
	}
	s.Log.Info("recompute mode changed", "actor", actor, "poolId", id, "mode", saved.Mode,
		"busyPolicy", saved.BusyPolicy, "malformedPolicy", saved.MalformedPolicy,
		"version", saved.Version, "auditKey", s.Engine.RecomputeSettingsAuditKey(id, saved.Version))
	view := recomputeModeView(s, id)
	view["version"] = saved.Version
	view["mode"] = saved.Mode
	view["busyPolicy"] = saved.BusyPolicy
	view["malformedPolicy"] = saved.MalformedPolicy
	httpx.JSON(w, http.StatusOK, view)
}
