// Path: internal/api/admin_workers_T83.go | Author: GLM for Claude Opus 5.5 | 2026-11-19
// What/why: read-only listing of the registered contributor tab worker records so the
// monitor page can show which GPUs a tab reported (wave T83 worker record gpuSlots +
// gpus list). Mirrors the metrics route gating (Store A admin/director, metrics:read).
// Owner rule: a tab that never reported GPUs stays absent from the gpu fields of the
// response, never zero.
package api

import (
	"encoding/json"
	"net/http"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/httpx"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
)

// workerRoutes registers the read-only worker record view used by the monitor page.
func (s *Server) workerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/v1/workers", s.secure("", "admin", "metrics:read", s.listWorkerRecords))
}

// workerView projects one stored worker record into the read-only view. Identity,
// capacity and heartbeat are always present; the GPU fields appear only when the tab
// actually reported them, so an unreported GPU is absent rather than zero.
func workerView(w pool.Worker) map[string]any {
	out := map[string]any{
		"tabId":     w.TabID,
		"hostId":    w.HostID,
		"owner":     w.Owner,
		"poolId":    w.PoolID,
		"slots":     w.Slots,
		"heartbeat": w.Heartbeat.UTC().Format(time.RFC3339Nano),
	}
	if w.GPU {
		out["gpu"] = true
	}
	if w.GPUSlots > 0 {
		out["gpuSlots"] = w.GPUSlots
	}
	if len(w.GPUs) > 0 {
		gpus := make([]map[string]any, 0, len(w.GPUs))
		for _, gpu := range w.GPUs {
			gpus = append(gpus, map[string]any{"vendor": gpu.Vendor, "architecture": gpu.Architecture})
		}
		out["gpus"] = gpus
	}
	if w.DeviceClass != "" {
		out["deviceClass"] = w.DeviceClass
	}
	if w.Charging {
		out["charging"] = true
	}
	if w.BatteryPercent > 0 {
		out["batteryPercent"] = w.BatteryPercent
	}
	return out
}

// listWorkerRecords answers GET /admin/v1/workers with the projected worker records.
// It is a pure read: no lease tokens, no writes, no secrets.
func (s *Server) listWorkerRecords(w http.ResponseWriter, r *http.Request, _ auth.Claims, _ string) {
	var records []store.Record
	err := s.Engine.Store.View(r.Context(), func(rd store.Reader) error {
		var e error
		records, e = rd.List(store.Workers)
		return e
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	workers := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		var wk pool.Worker
		if e := json.Unmarshal(rec.Value, &wk); e != nil {
			// One unreadable record must not hide every other tab; the corruption
			// is logged and the record skipped, never fabricated.
			s.Log.Warn("admin.workers.unreadable_record", "key", rec.Key, "error", e.Error())
			continue
		}
		workers = append(workers, workerView(wk))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"workers": workers, "count": len(workers)})
}
