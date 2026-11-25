// Path: internal/api/admin_caps_R15.go | Author: MCode (MiniMax-M3.1) | 2026-11-22
// What/why: R15 AT22-GM2-3 row 3.3 - the read-only admin view of the resource caps and
// the live counters of the process. It sits behind the same admin auth as
// /admin/v1/metrics and /admin/v1/workers, it never writes anything and it never
// reports a secret.
package api

import (
	"net/http"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/caps"
	"ccgsc/poolscore/internal/httpx"
)

// capsRoutes registers the read-only view of the caps.
func (s *Server) capsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/v1/caps", s.secure("", "admin", "metrics:read", s.readCaps))
	// R15-FIX-CAPS Addendum (G-R15-3): the caps are editable in the admin panel.
	// The route gate refuses an anonymous caller; writeCaps refuses every role but
	// admin.
	mux.HandleFunc("PUT /admin/v1/caps", s.secure("", "admin", "config:write", s.writeCaps))
}

// readCaps answers GET /admin/v1/caps with the effective caps of the configuration and
// the live counters of the process.
//
// R15-FIX-CAPS (c): the payload carries softLimit:true and the explanation, so an
// operator never reads the memory limit as a hard RSS ceiling.
func (s *Server) readCaps(w http.ResponseWriter, r *http.Request, _ auth.Claims, _ string) {
	// R16 F01/F02: the engine owns the one resolution every caller shares - the
	// saved admin record wins over the configuration file, and the zero value of
	// every knob stays auto.
	effective, err := s.Engine.EffectiveCaps()
	if err != nil {
		// The configuration was validated at the start of the server, so this can
		// only happen when a caller built a server or a record by hand. The view
		// reports the error instead of a fabricated cap.
		httpx.Error(w, r, err)
		return
	}
	saved, hasSaved := s.Engine.SavedCapsSettings()
	version := 0
	if hasSaved {
		version = saved.Version
	}
	live := caps.Stats()
	driver := s.StoreDriver
	if driver == "" {
		driver = s.Config.Driver
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		// The live counters of the process.
		"numCPU":          live.NumCPU,
		"gomaxprocs":      live.GOMAXPROCS,
		"numGoroutine":    live.NumGoroutine,
		"heapAllocBytes":  live.HeapAllocBytes,
		"heapSysBytes":    live.HeapSysBytes,
		"heapInuseBytes":  live.HeapInuseBytes,
		"stackInuseBytes": live.StackInuseBytes,
		"sysBytes":        live.SysBytes,
		"totalAllocBytes": live.TotalAllocBytes,
		"numGC":           live.NumGC,
		"memLimitBytes":   live.MemLimitBytes,
		// The effective caps.
		"maxProcs":       effective.MaxProcs,
		"memLimitMB":     effective.MemLimitMB,
		"workerPool":     effective.WorkerPool,
		"dbMaxOpenConns": effective.DBMaxOpenConns,
		"physicalMemMB":  effective.PhysicalMemMB,
		"storeDriver":    driver,
		// R16 F01: the version of the durable record and where the effective caps
		// came from ("admin" saved record, "config" configuration file), so the
		// panel can tell a saved write from a configured default.
		"version":    version,
		"capsSource": s.Engine.CapsSource(),
		// R15-FIX-CAPS (a) and (c): the memory budget of the process and the
		// nature of the limit. The limit is a soft GC target, never a hard RSS
		// cap.
		"cgroupMemMB":      effective.CgroupMemMB,
		"cgroupSource":     effective.CgroupSource,
		"availableMemMB":   effective.AvailableMemMB,
		"memLimitSource":   effective.MemLimitSource,
		"softLimit":        effective.SoftLimit,
		"softLimitExplain": caps.SoftLimitExplanation(),
	})
}
