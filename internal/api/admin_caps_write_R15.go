// Path: internal/api/admin_caps_write_R15.go | Author: Hermes (MiniMax-M3) | 2026-11-22
// What/why: R15-FIX-CAPS Addendum (G-R15-3) - the EDIT of the resource caps in the
// admin panel (the server pools settings section). Until now the caps were
// read-only (GET /admin/v1/caps) and only settable in the configuration file.
//
// The rules of the addendum:
//   - the admin role only (a developer and an anonymous caller are refused),
//   - every value is validated (a negative value is a clear error) and clamped
//     (a value that would take every core is clamped, never refused),
//   - every write leaves an audit row with the actor,
//   - maxProcs and memLimitMB apply LIVE (runtime.GOMAXPROCS,
//     debug.SetMemoryLimit),
//   - workerPool and dbMaxOpenConns need a restart of the process: the reply names
//     them, so the admin panel can show "applies after restart".
//
// R16-FIX (ChatGPT review F01/F02/F03):
//   - the approved values are persisted in the same settings store the pool
//     settings use, and a new engine loads them at startup: saved admin value >
//     configuration file > default (internal/pool/caps_settings_R16.go),
//   - a body naming one field merges onto the effective configuration, so it never
//     resets the other knobs; concurrent writes are serialized and a stale
//     expectedVersion answers 409 (R11-D1/R11-D2),
//   - the record and the audit row are one transaction and the runtime is applied
//     only after it commits; a store failure answers an error and leaves the
//     runtime unchanged.
//
// The values live in the pool engine and the caps package, so the admin API, the
// start of the server and the admin view all resolve the same rules.
package api

import (
	"errors"
	"net/http"
	"runtime"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/caps"
	"ccgsc/poolscore/internal/httpx"
	"ccgsc/poolscore/internal/pool"
)

// capsPendingRestart are the fields that need a restart of the process: the
// bounded worker pool is built at the start of the engine and the connection cap
// is set when the store opens.
var capsPendingRestart = []string{"workerPool", "dbMaxOpenConns"}

// capsWriteRequest is the body of the write. Every field is a pointer, so an
// absent field is "not changed" and a zero is a real value. expectedVersion is
// the conflict rule of the pool settings (R11-D2): a stale value answers 409 and
// writes nothing.
type capsWriteRequest struct {
	MaxProcs        *int `json:"maxProcs"`
	MemLimitMB      *int `json:"memLimitMB"`
	WorkerPool      *int `json:"workerPool"`
	DBMaxOpenConns  *int `json:"dbMaxOpenConns"`
	ExpectedVersion *int `json:"expectedVersion"`
}

// writeCaps applies the caps of the body and answers the new effective caps.
func (s *Server) writeCaps(w http.ResponseWriter, r *http.Request, c auth.Claims, _ string) {
	// The addendum: the admin role only. The route gate already refuses an
	// anonymous caller; a developer is refused here.
	if !c.HasRole("admin") {
		httpx.Problem(w, r, http.StatusForbidden, "forbidden", "Only an admin may change the resource caps.")
		return
	}
	var q capsWriteRequest
	if !httpx.Decode(w, r, &q) {
		return
	}
	// A body without any known field changes nothing.
	if q.MaxProcs == nil && q.MemLimitMB == nil && q.WorkerPool == nil && q.DBMaxOpenConns == nil {
		httpx.Problem(w, r, http.StatusBadRequest, "invalid-caps", "The body carries no caps field.")
		return
	}
	if q.ExpectedVersion != nil && *q.ExpectedVersion < 0 {
		httpx.Problem(w, r, http.StatusBadRequest, "invalid-caps", "expectedVersion must not be negative.")
		return
	}
	// The values are validated here: a negative value is a clear error, never a
	// silent fallback.
	for _, f := range []struct {
		name  string
		value *int
	}{
		{"maxProcs", q.MaxProcs},
		{"memLimitMB", q.MemLimitMB},
		{"workerPool", q.WorkerPool},
		{"dbMaxOpenConns", q.DBMaxOpenConns},
	} {
		if f.value != nil && *f.value < 0 {
			httpx.Problem(w, r, http.StatusBadRequest, "invalid-caps", f.name+" must not be negative.")
			return
		}
	}
	actor := c.User.ID
	if actor == "" {
		actor = c.Subject
	}
	key := "caps-change:" + auth.ID() + ":" + time.Now().UTC().Format(time.RFC3339Nano)
	// The record and the audit row are ONE transaction, and the engine applies
	// the caps only after the commit: a failing store answers an error and leaves
	// the runtime unchanged (R16 F03). The merge and the version check happen on
	// the stored record under the engine lock, so a partial write never drops the
	// other knobs and a stale expectedVersion leaves no change (R16 F02).
	saved, err := s.Engine.CommitCapsSettings(r.Context(), pool.CapsSettingsDelta{
		MaxProcs:       q.MaxProcs,
		MemLimitMB:     q.MemLimitMB,
		WorkerPool:     q.WorkerPool,
		DBMaxOpenConns: q.DBMaxOpenConns,
	}, q.ExpectedVersion, actor)
	switch {
	case errors.Is(err, pool.ErrSettingsConflict):
		httpx.Problem(w, r, http.StatusConflict, "config-conflict", "Config version conflict.")
		return
	case errors.Is(err, pool.ErrInvalid):
		httpx.Problem(w, r, http.StatusBadRequest, "invalid-caps", err.Error())
		return
	case err != nil:
		s.Log.Error("the caps persist/audit transaction failed; nothing changed", "error", err, "actor", actor)
		httpx.Problem(w, r, http.StatusInternalServerError, "caps-write-failed", "The caps were not changed: the persist/audit transaction failed. Inspect the server before retrying.")
		return
	}
	// The resolution clamps the values (never every core) and refuses an invalid
	// one. The clamped values are what the server runs with.
	effective, err := caps.Resolve(saved.Config())
	if err != nil {
		s.Log.Error("the saved caps do not resolve; nothing applied", "error", err)
		httpx.Problem(w, r, http.StatusInternalServerError, "caps-write-failed", "The caps were saved but do not resolve: inspect the server.")
		return
	}
	// Only now, after the commit, does the runtime move.
	caps.Apply(effective, nil)
	s.Engine.SetAppliedCaps(effective, saved.Version)
	s.Log.Info("caps changed", "actor", actor, "version", saved.Version, "auditKey", key,
		"maxProcs", effective.MaxProcs, "memLimitMB", effective.MemLimitMB,
		"workerPool", effective.WorkerPool, "dbMaxOpenConns", effective.DBMaxOpenConns)
	httpx.JSON(w, http.StatusOK, capsWriteView(effective, true, saved.Version))
}

// capsWriteView is the reply of the write: the new effective caps, the flag that
// the write applied, the version and the fields that need a restart.
func capsWriteView(applied caps.Caps, appliedLive bool, version int) map[string]any {
	return map[string]any{
		"maxProcs":       applied.MaxProcs,
		"memLimitMB":     applied.MemLimitMB,
		"memLimitBytes":  applied.MemLimitBytes,
		"workerPool":     applied.WorkerPool,
		"dbMaxOpenConns": applied.DBMaxOpenConns,
		"numCPU":         applied.NumCPU,
		"physicalMemMB":  applied.PhysicalMemMB,
		"cgroupMemMB":    applied.CgroupMemMB,
		"cgroupSource":   applied.CgroupSource,
		"availableMemMB": applied.AvailableMemMB,
		"memLimitSource": applied.MemLimitSource,
		"softLimit":      applied.SoftLimit,
		"softLimitNote":  caps.SoftLimitExplanation(),
		"applied":        appliedLive,
		"version":        version,
		"pendingRestart": capsPendingRestart,
		"gomaxprocs":     runtime.GOMAXPROCS(0),
	}
}
