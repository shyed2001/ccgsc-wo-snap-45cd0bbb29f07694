// Path: internal/api/admin_workers_T83_test.go | Author: GLM for Claude Opus 5.5 | 2026-11-19
// What/why: the read-only /admin/v1/workers view of the T83 worker records: role and
// scope gating, the gpuSlots + gpus projection of a GPU tab, and the rule that a tab
// which never reported GPUs stays absent from the GPU fields, never zero.
package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/sqlite"
)

func workersHarness(t *testing.T) (http.Handler, *auth.Service, *pool.Engine) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "workers.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine, err := pool.New(db, logger, "s1", time.Minute)
	if err != nil {
		t.Fatalf("pool.New: %v", err)
	}
	authService := auth.New(db, config.Default().JWTSecret, "s1")
	server := &Server{Engine: engine, Auth: authService, Log: logger, Config: config.Default()}
	return server.Handler(), authService, engine
}

func bearerKey(t *testing.T, svc *auth.Service, storeName, role string) string {
	t.Helper()
	raw, _, err := svc.CreateKey(context.Background(), auth.User{ID: role + "-user", AccountStore: storeName, Roles: []string{role}}, []string{"*"})
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	token, err := svc.Exchange(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	return token.AccessToken
}

func TestT83AdminWorkersGPUReports(t *testing.T) {
	handler, authService, engine := workersHarness(t)

	gpuTab, err := engine.Register(context.Background(), pool.Thread, "contributor", pool.Worker{
		HostID: "host-1", Slots: 4, GPU: true, GPUSlots: 2,
		GPUs:        []pool.GPUInfo{{Vendor: "amd", Architecture: "rdna3"}},
		DeviceClass: "laptop", Charging: true,
	})
	if err != nil {
		t.Fatalf("Register GPU tab: %v", err)
	}
	cpuTab, err := engine.Register(context.Background(), pool.Process, "contributor-2", pool.Worker{
		HostID: "host-1", Slots: 2, DeviceClass: "desktop",
	})
	if err != nil {
		t.Fatalf("Register CPU tab: %v", err)
	}

	admin := bearerKey(t, authService, "A", "admin")

	// Unauthenticated and unauthorized callers are refused like on /admin/v1/metrics.
	req := httptest.NewRequest(http.MethodGet, "http://s/admin/v1/workers", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", rec.Code)
	}

	developer := bearerKey(t, authService, "B", "developer")
	req = httptest.NewRequest(http.MethodGet, "http://s/admin/v1/workers", nil)
	req.Header.Set("Authorization", "Bearer "+developer)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("developer status = %d, want 403", rec.Code)
	}

	// The admin view lists both records with the T83 GPU projection.
	req = httptest.NewRequest(http.MethodGet, "http://s/admin/v1/workers", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Workers []map[string]any `json:"workers"`
		Count   int              `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Count != 2 || len(body.Workers) != 2 {
		t.Fatalf("count = %d, workers = %d, want 2/2", body.Count, len(body.Workers))
	}
	byTab := map[string]map[string]any{}
	for _, wk := range body.Workers {
		byTab[wk["tabId"].(string)] = wk
	}

	got := byTab[gpuTab.TabID]
	if got == nil {
		t.Fatal("GPU tab missing from the listing")
	}
	if got["gpuSlots"].(float64) != 2 {
		t.Fatalf("gpuSlots = %v, want 2", got["gpuSlots"])
	}
	gpus, ok := got["gpus"].([]any)
	if !ok || len(gpus) != 1 {
		t.Fatalf("gpus = %#v, want one reported adapter", got["gpus"])
	}
	first := gpus[0].(map[string]any)
	if first["vendor"] != "amd" || first["architecture"] != "rdna3" {
		t.Fatalf("gpus[0] = %v, want amd/rdna3", first)
	}

	// The CPU tab reported no GPUs: the GPU fields are absent, never zero.
	got = byTab[cpuTab.TabID]
	if got == nil {
		t.Fatal("CPU tab missing from the listing")
	}
	for _, absent := range []string{"gpu", "gpuSlots", "gpus", "charging", "batteryPercent"} {
		if _, present := got[absent]; present {
			t.Fatalf("CPU tab carries %q = %v, want the field absent", absent, got[absent])
		}
	}
	if got["deviceClass"] != "desktop" || got["slots"].(float64) != 2 {
		t.Fatalf("CPU tab identity fields = %v", got)
	}
}

func TestT83AdminWorkersSkipsUnreadableRecord(t *testing.T) {
	handler, authService, engine := workersHarness(t)
	if _, err := engine.Register(context.Background(), pool.Thread, "contributor", pool.Worker{HostID: "host-1", Slots: 1}); err != nil {
		t.Fatal(err)
	}
	if err := engine.Store.Update(context.Background(), func(tx store.Tx) error {
		return tx.Put(store.Workers, "broken-record", []byte("{not json"))
	}); err != nil {
		t.Fatalf("seed broken record: %v", err)
	}

	admin := bearerKey(t, authService, "A", "admin")
	req := httptest.NewRequest(http.MethodGet, "http://s/admin/v1/workers", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"count":1`) {
		t.Fatalf("broken record not skipped: %s", rec.Body.String())
	}
}
