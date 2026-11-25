// Path: internal/api/admin_caps_R15_test.go | Author: MCode (MiniMax-M3.1) | 2026-11-22
// What/why: R15 AT22-GM2-3 row 3.3 - the read-only admin view of the resource caps and
// the live runtime counters. It sits behind the same admin auth as /admin/v1/metrics
// and /admin/v1/workers and it never writes anything.
package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store/sqlite"
)

func capsHarness(t *testing.T) (http.Handler, *auth.Service) {
	handler, authService, _ := capsHarnessWithEngine(t)
	return handler, authService
}

// capsHarnessWithEngine is the same harness, with the engine: the addendum test
// reads the audit bucket of the store of the engine.
func capsHarnessWithEngine(t *testing.T) (http.Handler, *auth.Service, *pool.Engine) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "caps.db"))
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

// TestAdminCapsIsBehindTheAdminAuth pins the gate: anonymous callers are refused, a
// developer is refused and only an admin reads the view.
func TestAdminCapsIsBehindTheAdminAuth(t *testing.T) {
	handler, _ := capsHarness(t)
	req := httptest.NewRequest(http.MethodGet, "http://s/admin/v1/caps", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", rec.Code)
	}
}

// TestAdminCapsReportsTheCapsAndTheRuntime pins the payload: the effective caps and the
// live counters of the process.
func TestAdminCapsReportsTheCapsAndTheRuntime(t *testing.T) {
	handler, authService := capsHarness(t)
	admin := bearerKey(t, authService, "A", "admin")
	req := httptest.NewRequest(http.MethodGet, "http://s/admin/v1/caps", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		NumCPU         int    `json:"numCPU"`
		GOMAXPROCS     int    `json:"gomaxprocs"`
		NumGoroutine   int    `json:"numGoroutine"`
		HeapAllocBytes uint64 `json:"heapAllocBytes"`
		HeapSysBytes   uint64 `json:"heapSysBytes"`
		MemLimitBytes  int64  `json:"memLimitBytes"`
		MaxProcs       int    `json:"maxProcs"`
		MemLimitMB     int    `json:"memLimitMB"`
		WorkerPool     int    `json:"workerPool"`
		DBMaxOpenConns int    `json:"dbMaxOpenConns"`
		StoreDriver    string `json:"storeDriver"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.NumCPU != runtime.NumCPU() {
		t.Fatalf("numCPU = %d, want %d", body.NumCPU, runtime.NumCPU())
	}
	if body.GOMAXPROCS != runtime.GOMAXPROCS(0) {
		t.Fatalf("gomaxprocs = %d, want %d", body.GOMAXPROCS, runtime.GOMAXPROCS(0))
	}
	if body.NumGoroutine <= 0 {
		t.Fatalf("numGoroutine = %d, want at least 1", body.NumGoroutine)
	}
	if body.HeapAllocBytes == 0 {
		t.Fatal("heapAllocBytes = 0")
	}
	if body.MaxProcs < 1 {
		t.Fatalf("maxProcs = %d, want at least 1", body.MaxProcs)
	}
	if body.WorkerPool < 1 {
		t.Fatalf("workerPool = %d, want at least 1", body.WorkerPool)
	}
	if body.DBMaxOpenConns < 1 {
		t.Fatalf("dbMaxOpenConns = %d, want at least 1", body.DBMaxOpenConns)
	}
	if body.StoreDriver == "" {
		t.Fatal("storeDriver is empty")
	}
}

// TestAdminCapsRefusesADeveloper pins that a developer key does not read the view.
func TestAdminCapsRefusesADeveloper(t *testing.T) {
	handler, authService := capsHarness(t)
	developer := bearerKey(t, authService, "B", "developer")
	req := httptest.NewRequest(http.MethodGet, "http://s/admin/v1/caps", nil)
	req.Header.Set("Authorization", "Bearer "+developer)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("developer status = %d, want 403", rec.Code)
	}
}
