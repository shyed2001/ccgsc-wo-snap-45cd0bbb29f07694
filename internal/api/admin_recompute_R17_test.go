// Path: internal/api/admin_recompute_R17_test.go | Author: Hermes (MiniMax-M3) for the owner | 2026-11-22
// What/why: WO R17-RECOMPUTE (G-R16-4) row 3 - the red tests of the admin API of
// the per-pool recompute mode: the admin role gate, the validation, the audit
// row, the conflict rule and the durability across a restart.
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

// recomputeHarness builds the recompute harness: an engine on a file backed
// SQLite store, an auth service and the HTTP handler.
func recomputeHarness(t *testing.T) (http.Handler, *auth.Service, *pool.Engine) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "recompute.db"))
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

// recomputeCall drives one recompute endpoint call.
func recomputeCall(t *testing.T, handler http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "http://s"+path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// TestAdminRecomputeModeAdminRoleGate is the red test of the role gate: an
// anonymous caller and a developer are refused, only an admin may write.
func TestAdminRecomputeModeAdminRoleGate(t *testing.T) {
	handler, authService, _ := recomputeHarness(t)
	admin := bearerKey(t, authService, "A", "admin")
	dev := bearerKey(t, authService, "B", "developer")

	// No token at all.
	if rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/tp/recompute", "", `{"mode":"registry"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous status = %d, want 401", rec.Code)
	}
	// A developer is not an admin.
	if rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/tp/recompute", dev, `{"mode":"registry"}`); rec.Code != http.StatusForbidden {
		t.Errorf("developer status = %d, want 403", rec.Code)
	}
	// The admin may write.
	if rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/tp/recompute", admin, `{"mode":"registry"}`); rec.Code != http.StatusOK {
		t.Errorf("admin status = %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAdminRecomputeModeValidatesTheValue is the red test of the validation: an
// unknown mode is a clear error, never a silent fallback.
func TestAdminRecomputeModeValidatesTheValue(t *testing.T) {
	handler, authService, _ := recomputeHarness(t)
	admin := bearerKey(t, authService, "A", "admin")
	if rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/tp/recompute", admin, `{"mode":"sometimes"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown mode = %d, want 400", rec.Code)
	}
	if rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/tp/recompute", admin, `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("an empty body = %d, want 400", rec.Code)
	}
	if rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/nope/recompute", admin, `{"mode":"registry"}`); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown pool = %d, want 404", rec.Code)
	}
}

// TestAdminRecomputeModeIsAuditLogged is the red test of the audit row: every
// write leaves one with the actor.
func TestAdminRecomputeModeIsAuditLogged(t *testing.T) {
	handler, authService, engine := recomputeHarness(t)
	admin := bearerKey(t, authService, "A", "admin")
	if rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/tp/recompute", admin, `{"mode":"registry"}`); rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d: %s", rec.Code, rec.Body.String())
	}
	var found bool
	if err := engine.Store.View(context.Background(), func(r store.Reader) error {
		rows, err := r.List(store.Audit)
		if err != nil {
			return err
		}
		for _, row := range rows {
			raw := string(row.Value)
			if strings.Contains(raw, "recompute") && strings.Contains(raw, "admin-user") {
				found = true
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("read the audit bucket: %v", err)
	}
	if !found {
		t.Fatal("no audit row carries the recompute write and its actor")
	}
}

// TestAdminRecomputeModeSurvivesRestart is the red test of the durability: the
// saved mode is still in force after a restart of the engine.
func TestAdminRecomputeModeSurvivesRestart(t *testing.T) {
	handler, authService, engine := recomputeHarness(t)
	admin := bearerKey(t, authService, "A", "admin")
	if rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/tp/recompute", admin, `{"mode":"registry"}`); rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d: %s", rec.Code, rec.Body.String())
	}
	saved, ok := engine.SavedRecomputeSettings(pool.Thread)
	if !ok {
		t.Fatal("the write left no durable recompute record")
	}
	if saved.Mode != pool.RecomputeModeRegistry || saved.Version != 1 {
		t.Fatalf("saved = %+v, want the registry mode at version 1", saved)
	}
	// Restart: the same store, a new engine.
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	restarted, err := pool.New(engine.Store, logger, "s1", time.Minute)
	if err != nil {
		t.Fatalf("pool.New after the restart: %v", err)
	}
	if got := restarted.RecomputeMode(pool.Thread); got != pool.RecomputeModeRegistry {
		t.Fatalf("the mode after the restart = %q, want %q", got, pool.RecomputeModeRegistry)
	}
	// The pool that was never chosen keeps the default.
	if got := restarted.RecomputeMode(pool.Process); got != pool.RecomputeModeOff {
		t.Fatalf("the mode of the untouched pool = %q, want %q", got, pool.RecomputeModeOff)
	}
}

// TestAdminRecomputeModeStaleVersionAnswers409 is the red test of the conflict
// rule: a stale expectedVersion answers 409 and writes nothing.
func TestAdminRecomputeModeStaleVersionAnswers409(t *testing.T) {
	handler, authService, engine := recomputeHarness(t)
	admin := bearerKey(t, authService, "A", "admin")
	if rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/tp/recompute", admin, `{"mode":"registry"}`); rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/tp/recompute", admin, `{"mode":"off","expectedVersion":0}`); rec.Code != http.StatusConflict {
		t.Errorf("a stale write = %d, want 409", rec.Code)
	}
	if got := engine.RecomputeMode(pool.Thread); got != pool.RecomputeModeRegistry {
		t.Fatalf("the mode = %q, want the refused write to change nothing", got)
	}
}

// TestAdminRecomputeModeReadAnswersTheEffectiveMode is the red test of the
// read: the endpoint answers the effective mode, the modes the panel may
// offer and the version of the record.
func TestAdminRecomputeModeReadAnswersTheEffectiveMode(t *testing.T) {
	handler, authService, _ := recomputeHarness(t)
	admin := bearerKey(t, authService, "A", "admin")
	rec := recomputeCall(t, handler, http.MethodGet, "/admin/v1/pools/tp/recompute", admin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d: %s", rec.Code, rec.Body.String())
	}
	var view map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view["mode"] != string(pool.RecomputeModeOff) {
		t.Errorf("the mode = %v, want the default off", view["mode"])
	}
	modes, ok := view["modes"].([]any)
	if !ok || len(modes) != 2 {
		t.Fatalf("the modes = %v, want the two documented modes", view["modes"])
	}
	if view["source"] != "default" {
		t.Errorf("the source = %v, want the default", view["source"])
	}
	// After a write the read answers the saved mode and the admin source.
	if rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/tp/recompute", admin, `{"mode":"registry"}`); rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d: %s", rec.Code, rec.Body.String())
	}
	rec = recomputeCall(t, handler, http.MethodGet, "/admin/v1/pools/tp/recompute", admin, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view["mode"] != string(pool.RecomputeModeRegistry) {
		t.Errorf("the mode = %v, want the registry mode", view["mode"])
	}
	if view["source"] != "admin" {
		t.Errorf("the source = %v, want admin", view["source"])
	}
	if view["version"] != float64(1) {
		t.Errorf("the version = %v, want 1", view["version"])
	}
}

// TestAdminRecomputeLoggedAuditKeyIsStored checks the actual structured log
// identifier can be read back from the audit store. A synthetic log-only key
// makes investigations misleading even when a row exists elsewhere.
func TestAdminRecomputeLoggedAuditKeyIsStored(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "audit-correlation.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	output := &strings.Builder{}
	logger := slog.New(slog.NewJSONHandler(output, nil))
	engine, err := pool.New(db, logger, "s1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	authService := auth.New(db, config.Default().JWTSecret, "s1")
	handler := (&Server{Engine: engine, Auth: authService, Log: logger, Config: config.Default()}).Handler()
	admin := bearerKey(t, authService, "A", "admin")
	rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/tp/recompute", admin, `{"mode":"registry","expectedVersion":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
	}
	var key string
	for _, line := range strings.Split(output.String(), "\n") {
		if !strings.Contains(line, `"msg":"recompute mode changed"`) {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		key, _ = event["auditKey"].(string)
	}
	if key == "" {
		t.Fatalf("no auditKey in structured log: %s", output.String())
	}
	var saved map[string]any
	if err := db.View(context.Background(), func(tx store.Reader) error {
		return tx.Get(store.Audit, key, &saved)
	}); err != nil {
		t.Fatalf("logged audit key %q did not resolve to stored audit event: %v", key, err)
	}
	if saved["type"] != "recompute.change" || saved["version"] != float64(1) {
		t.Fatalf("wrong event at log key %q: %+v", key, saved)
	}
}

// TestAdminRecomputeUpgradeRequiresExplicitReview ensures a previously configured
// sampling pool is not silently represented as a newly installed pool when no
// recompute-mode record exists. Changing migration policy still needs owner GO.
func TestAdminRecomputeUpgradeRequiresExplicitReview(t *testing.T) {
	handler, authService, engine := recomputeHarness(t)
	admin := bearerKey(t, authService, "A", "admin")
	configured := pool.PoolSettings{
		VerifyMode: pool.VerifyModeSample, SampleRate: 1,
		RedundantRate: 0, IsolationMode: pool.IsolationSeparateLock,
	}
	if _, err := engine.SetPoolSettings(context.Background(), pool.Thread, configured, nil); err != nil {
		t.Fatal(err)
	}
	get := func() map[string]any {
		t.Helper()
		rec := recomputeCall(t, handler, http.MethodGet, "/admin/v1/pools/tp/recompute", admin, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET: %d: %s", rec.Code, rec.Body.String())
		}
		var view map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		return view
	}
	old := get()
	if old["mode"] != string(pool.RecomputeModeOff) || old["source"] != "default" || old["upgradeReviewRequired"] != true {
		t.Fatalf("older saved sample settings must require an explicit mode review: %+v", old)
	}
	rec := recomputeCall(t, handler, http.MethodPut, "/admin/v1/pools/tp/recompute", admin, `{"mode":"off","expectedVersion":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d: %s", rec.Code, rec.Body.String())
	}
	chosen := get()
	if chosen["upgradeReviewRequired"] != false || chosen["source"] != "admin" {
		t.Fatalf("explicit choice must dismiss upgrade warning: %+v", chosen)
	}
}
