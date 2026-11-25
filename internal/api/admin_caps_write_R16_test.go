// Path: internal/api/admin_caps_write_R16_test.go | Author: Codex for Claude Opus 5.5 | 2026-11-22
// What/why: R16-FIX (ChatGPT review F01-F03) - the admin caps write must be
// durable, must merge a partial body against the effective configuration, must
// answer a stale expectedVersion with 409, and must never touch the runtime
// before the store commit succeeded.
package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/caps"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/sqlite"
)

// capsTestConfig is a nondefault configuration: a partial caps write must keep
// these values instead of resolving zeros for the unnamed fields (R16 F02).
func capsTestConfig() config.Config {
	c := config.Default()
	c.MaxProcs = 2
	c.WorkerPool = 2
	c.DBMaxOpenConns = 8
	return c
}

// capsHarnessConfig builds the caps harness with explicit server configuration.
func capsHarnessConfig(t *testing.T, cfg config.Config) (http.Handler, *auth.Service, *pool.Engine) {
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
	authService := auth.New(db, cfg.JWTSecret, "s1")
	server := &Server{Engine: engine, Auth: authService, Log: logger, Config: cfg}
	return server.Handler(), authService, engine
}

// capsGetCall reads the admin caps view with the given token.
func capsGetCall(t *testing.T, handler http.Handler, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://s/admin/v1/caps", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// TestAdminCapsWriteSurvivesRestart pins F01: a write is persisted in the same
// settings store the pool settings use, and a new engine/server on that store
// resolves the saved values (saved admin value > configuration file > default).
func TestAdminCapsWriteSurvivesRestart(t *testing.T) {
	if runtime.NumCPU() < 3 {
		t.Skip("the caps values need at least 3 logical CPUs to stay below the clamp")
	}
	handler, authService, engine := capsHarnessConfig(t, capsTestConfig())
	admin := bearerKey(t, authService, "A", "admin")
	old := runtime.GOMAXPROCS(0)
	t.Cleanup(func() { runtime.GOMAXPROCS(old) })
	rec := capsWriteCall(t, handler, admin, `{"maxProcs":2,"memLimitMB":512,"workerPool":2,"dbMaxOpenConns":8}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d: %s", rec.Code, rec.Body.String())
	}
	saved, ok := engine.SavedCapsSettings()
	if !ok {
		t.Fatal("the write left no durable caps record")
	}
	want := caps.Config{MaxProcs: 2, MemLimitMB: 512, WorkerPool: 2, DBMaxOpenConns: 8}
	if saved.Config() != want {
		t.Fatalf("saved config = %+v, want %+v", saved.Config(), want)
	}
	if saved.Version != 1 {
		t.Fatalf("saved version = %d, want 1", saved.Version)
	}
	// Restart: the same store, a new engine and a new server. The server carries
	// the zero (auto) configuration, so only the saved record can produce the
	// asserted values.
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	restarted, err := pool.New(engine.Store, logger, "s1", time.Minute)
	if err != nil {
		t.Fatalf("pool.New after restart: %v", err)
	}
	reloaded, ok := restarted.SavedCapsSettings()
	if !ok {
		t.Fatal("the restarted engine did not load the saved caps")
	}
	if reloaded != saved {
		t.Fatalf("reloaded = %+v, want %+v", reloaded, saved)
	}
	effective, err := caps.Resolve(reloaded.Config())
	if err != nil {
		t.Fatalf("resolve the saved caps: %v", err)
	}
	if effective.MaxProcs != 2 || effective.MemLimitMB != 512 || effective.WorkerPool != 2 || effective.DBMaxOpenConns != 8 {
		t.Fatalf("effective = %+v, want the saved values applied after the restart", effective)
	}
	// The start of the server applies the resolved caps (cmd/pools-core/main.go).
	caps.Apply(effective, nil)
	if got := runtime.GOMAXPROCS(0); got != 2 {
		t.Fatalf("GOMAXPROCS after the simulated restart = %d, want 2", got)
	}
	restartedServer := &Server{Engine: restarted, Auth: authService, Log: logger, Config: config.Default()}
	view := capsGetCall(t, restartedServer.Handler(), admin)
	if view.Code != http.StatusOK {
		t.Fatalf("admin GET status = %d: %s", view.Code, view.Body.String())
	}
	var body struct {
		MaxProcs       int    `json:"maxProcs"`
		MemLimitMB     int    `json:"memLimitMB"`
		WorkerPool     int    `json:"workerPool"`
		DBMaxOpenConns int    `json:"dbMaxOpenConns"`
		Version        int    `json:"version"`
		CapsSource     string `json:"capsSource"`
	}
	if err := json.Unmarshal(view.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.MaxProcs != 2 || body.MemLimitMB != 512 || body.WorkerPool != 2 || body.DBMaxOpenConns != 8 {
		t.Fatalf("admin view = %+v, want the saved values after the restart", body)
	}
	if body.Version != 1 || body.CapsSource != "admin" {
		t.Fatalf("admin view version/source = %d/%q, want 1/admin", body.Version, body.CapsSource)
	}
}

// TestAdminCapsWritePartialUpdateKeepsTheOthers pins F02: a body naming one
// field merges against the effective configuration (config file, then the saved
// record) instead of resolving zeros for the unnamed fields.
func TestAdminCapsWritePartialUpdateKeepsTheOthers(t *testing.T) {
	handler, authService, engine := capsHarnessConfig(t, capsTestConfig())
	admin := bearerKey(t, authService, "A", "admin")
	old := runtime.GOMAXPROCS(0)
	t.Cleanup(func() { runtime.GOMAXPROCS(old) })
	base, err := caps.Resolve(capsTestConfig().Caps())
	if err != nil {
		t.Fatalf("resolve the configured caps: %v", err)
	}
	rec := capsWriteCall(t, handler, admin, `{"memLimitMB":512}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		MaxProcs       int `json:"maxProcs"`
		MemLimitMB     int `json:"memLimitMB"`
		WorkerPool     int `json:"workerPool"`
		DBMaxOpenConns int `json:"dbMaxOpenConns"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.MaxProcs != base.MaxProcs || body.WorkerPool != base.WorkerPool || body.DBMaxOpenConns != base.DBMaxOpenConns {
		t.Fatalf("reply = %+v, want the configured maxProcs=%d workerPool=%d dbMaxOpenConns=%d kept",
			body, base.MaxProcs, base.WorkerPool, base.DBMaxOpenConns)
	}
	if body.MemLimitMB != 512 {
		t.Fatalf("memLimitMB = %d, want 512", body.MemLimitMB)
	}
	saved, ok := engine.SavedCapsSettings()
	if !ok {
		t.Fatal("the write left no durable caps record")
	}
	// The record carries the configured values of the unnamed fields, not zeros.
	if saved.MaxProcs != base.MaxProcs || saved.WorkerPool != base.WorkerPool || saved.DBMaxOpenConns != base.DBMaxOpenConns {
		t.Fatalf("stored = %+v, want the configured values of the unnamed fields", saved)
	}
	view := capsGetCall(t, handler, admin)
	var read struct {
		MaxProcs       int `json:"maxProcs"`
		MemLimitMB     int `json:"memLimitMB"`
		WorkerPool     int `json:"workerPool"`
		DBMaxOpenConns int `json:"dbMaxOpenConns"`
	}
	if err := json.Unmarshal(view.Body.Bytes(), &read); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if read.MaxProcs != base.MaxProcs || read.WorkerPool != base.WorkerPool || read.DBMaxOpenConns != base.DBMaxOpenConns || read.MemLimitMB != 512 {
		t.Fatalf("GET caps = %+v, want the partial write merged onto the configuration", read)
	}
}

// TestAdminCapsConcurrentPartialWritesKeepEveryField pins F02's lost-update
// half: two simultaneous partial writes of disjoint fields must keep both.
func TestAdminCapsConcurrentPartialWritesKeepEveryField(t *testing.T) {
	handler, authService, engine := capsHarnessConfig(t, capsTestConfig())
	admin := bearerKey(t, authService, "A", "admin")
	old := runtime.GOMAXPROCS(0)
	t.Cleanup(func() { runtime.GOMAXPROCS(old) })
	for i := 0; i < 20; i++ {
		start := make(chan struct{})
		codes := make([]int, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		for n, body := range []string{`{"maxProcs":2}`, `{"memLimitMB":512}`} {
			n, body := n, body
			go func() {
				defer wg.Done()
				<-start
				codes[n] = capsWriteCall(t, handler, admin, body).Code
			}()
		}
		close(start)
		wg.Wait()
		for n, code := range codes {
			if code != http.StatusOK {
				t.Fatalf("iteration %d: write %d status = %d, want 200", i, n, code)
			}
		}
		saved, ok := engine.SavedCapsSettings()
		if !ok {
			t.Fatalf("iteration %d: the writes left no durable caps record", i)
		}
		if saved.MaxProcs != 2 || saved.MemLimitMB != 512 {
			t.Fatalf("iteration %d: stored = %+v, want both concurrent fields", i, saved)
		}
		if saved.Version != 2*(i+1) {
			t.Fatalf("iteration %d: version = %d, want %d", i, saved.Version, 2*(i+1))
		}
	}
}

// TestAdminCapsWriteStaleVersionConflicts pins the R11-D2 conflict rule of F02:
// a stale expectedVersion answers 409 and changes nothing.
func TestAdminCapsWriteStaleVersionConflicts(t *testing.T) {
	handler, authService, engine := capsHarnessConfig(t, capsTestConfig())
	admin := bearerKey(t, authService, "A", "admin")
	old := runtime.GOMAXPROCS(0)
	t.Cleanup(func() { runtime.GOMAXPROCS(old) })
	first := capsWriteCall(t, handler, admin, `{"maxProcs":2}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first write status = %d: %s", first.Code, first.Body.String())
	}
	stale := capsWriteCall(t, handler, admin, `{"maxProcs":1,"expectedVersion":0}`)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale write status = %d, want 409: %s", stale.Code, stale.Body.String())
	}
	if saved, _ := engine.SavedCapsSettings(); saved.MaxProcs != 2 || saved.Version != 1 {
		t.Fatalf("stored after the stale write = %+v, want the first write intact", saved)
	}
	fresh := capsWriteCall(t, handler, admin, `{"maxProcs":1,"expectedVersion":1}`)
	if fresh.Code != http.StatusOK {
		t.Fatalf("fresh write status = %d: %s", fresh.Code, fresh.Body.String())
	}
}

// capsFailUpdateStore fails every update, so the persist/audit transaction can
// be injected to fail (R16 F03).
type capsFailUpdateStore struct {
	store.Store
	fail bool
}

func (s *capsFailUpdateStore) Update(ctx context.Context, fn func(store.Tx) error) error {
	if s.fail {
		return errCapsStoreDown
	}
	return s.Store.Update(ctx, fn)
}

var errCapsStoreDown = capsStoreError("the store is down")

type capsStoreError string

func (e capsStoreError) Error() string { return string(e) }

// TestAdminCapsWriteStoreFailureLeavesTheRuntime pins F03: the persist and the
// audit row are one transaction, and the runtime changes only after it commits.
// A failing store answers an error and leaves GOMAXPROCS and the record alone.
func TestAdminCapsWriteStoreFailureLeavesTheRuntime(t *testing.T) {
	handler, authService, engine := capsHarnessConfig(t, capsTestConfig())
	admin := bearerKey(t, authService, "A", "admin")
	old := runtime.GOMAXPROCS(0)
	t.Cleanup(func() { runtime.GOMAXPROCS(old) })
	failing := &capsFailUpdateStore{Store: engine.Store, fail: true}
	engine.Store = failing
	rec := capsWriteCall(t, handler, admin, `{"maxProcs":2}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if got := runtime.GOMAXPROCS(0); got != old {
		t.Fatalf("GOMAXPROCS = %d, want %d: a failed store must leave the runtime alone", got, old)
	}
	if _, ok := engine.SavedCapsSettings(); ok {
		t.Fatal("a failed store must leave no saved caps record")
	}
	failing.fail = false
	if err := engine.Store.View(t.Context(), func(r store.Reader) error {
		rows, err := r.List(store.Audit)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if strings.Contains(string(row.Value), "caps.change") {
				t.Fatalf("a failed store must leave no audit row, found %s", row.Value)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("read the audit bucket: %v", err)
	}
}
