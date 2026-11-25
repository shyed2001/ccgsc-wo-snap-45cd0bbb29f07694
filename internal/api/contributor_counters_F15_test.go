// Path: internal/api/contributor_counters_F15_test.go
// What/why: F15 read routes. A contributor reads only their own counters. The
// monitor reads the totals. Both use the same secure() gate as the metrics routes.
package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/sqlite"
)

func countersHarness(t *testing.T) (http.Handler, *auth.Service, *pool.Engine) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "counters.db"))
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

func recordCounter(t *testing.T, engine *pool.Engine, name, source string, value float64, unit string) {
	t.Helper()
	err := engine.Store.Update(context.Background(), func(tx store.Tx) error {
		return store.RecordMetricSample(tx, store.MetricSample{
			Scope: "tp", JobID: "job-1", Name: name, Value: value, Unit: unit, Source: source,
		})
	})
	if err != nil {
		t.Fatalf("RecordMetricSample: %v", err)
	}
}

func getJSON(t *testing.T, handler http.Handler, token, path string, headers ...map[string]string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://s"+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for key, value := range firstHeader(headers) {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var body map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
	}
	return rec.Code, body
}

func firstHeader(headers []map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	return headers[0]
}

func TestF15ContributorCounterRoutes(t *testing.T) {
	handler, authService, engine := countersHarness(t)
	admin := bearerKey(t, authService, "A", "admin")
	developer := bearerKey(t, authService, "B", "developer")
	contributor := bearerKey(t, authService, "B", "contributor")
	anonToken, err := authService.IssueAnonymous("anon-browser", nil)
	if err != nil {
		t.Fatalf("IssueAnonymous: %v", err)
	}
	attackerToken, err := authService.IssueAnonymous("anon-attacker", nil)
	if err != nil {
		t.Fatalf("IssueAnonymous attacker: %v", err)
	}
	if _, err = engine.Register(context.Background(), pool.Thread, "anon-browser", pool.Worker{HostID: "host-browser", Slots: 1}); err != nil {
		t.Fatalf("Register anonymous: %v", err)
	}
	if _, err = engine.Register(context.Background(), pool.Thread, "anon-attacker", pool.Worker{HostID: "host-attacker", Slots: 1}); err != nil {
		t.Fatalf("Register attacker: %v", err)
	}

	recordCounter(t, engine, "contributor_accepted_tasks", "account:contributor-user\x1ftask-a\x1ftask", 1, "count")
	recordCounter(t, engine, "contributor_compute_ms", "account:contributor-user\x1ftask-a\x1flease:one", 1500, "ms")
	recordCounter(t, engine, "contributor_accepted_tasks", "account:other-user\x1ftask-b\x1ftask", 1, "count")
	recordCounter(t, engine, "contributor_compute_ms", "install:host-browser\x1ftask-c\x1flease:two", 800, "ms")
	recordCounter(t, engine, "contributor_accepted_tasks", "install:host-browser\x1ftask-c\x1ftask", 1, "count")
	recordCounter(t, engine, "lease_settled", "pool", 9, "count")

	code, _ := getJSON(t, handler, "", "/contributor/v1/counters")
	if code != http.StatusUnauthorized {
		t.Fatalf("no token status = %d, want 401", code)
	}
	code, _ = getJSON(t, handler, developer, "/contributor/v1/counters")
	if code != http.StatusForbidden {
		t.Fatalf("developer own status = %d, want 403", code)
	}
	code, _ = getJSON(t, handler, contributor, "/admin/v1/contributor-counters")
	if code != http.StatusForbidden {
		t.Fatalf("contributor totals status = %d, want 403", code)
	}
	code, _ = getJSON(t, handler, "", "/admin/v1/contributor-counters")
	if code != http.StatusUnauthorized {
		t.Fatalf("open totals status = %d, want 401", code)
	}

	code, own := getJSON(t, handler, contributor, "/contributor/v1/counters")
	if code != http.StatusOK {
		t.Fatalf("own status = %d, body %v", code, own)
	}
	if own["acceptedTasks"] != float64(1) || own["computeMs"] != float64(1500) {
		t.Fatalf("own counters = %v, want 1 task and 1500 ms", own)
	}

	code, anonMissing := getJSON(t, handler, anonToken.AccessToken, "/contributor/v1/counters")
	if code != http.StatusNotFound {
		t.Fatalf("anonymous without install = %d, want 404 (%v)", code, anonMissing)
	}
	code, anonOther := getJSON(t, handler, anonToken.AccessToken, "/contributor/v1/counters", map[string]string{"X-CCGSC-Install": "host-other"})
	if code != http.StatusNotFound {
		t.Fatalf("foreign install = %d, want 404 (%v)", code, anonOther)
	}
	code, spoofed := getJSON(t, handler, attackerToken.AccessToken, "/contributor/v1/counters", map[string]string{"X-CCGSC-Install": "host-browser"})
	if code != http.StatusNotFound {
		t.Fatalf("spoofed install = %d, want 404 (%v)", code, spoofed)
	}
	code, anonOwn := getJSON(t, handler, anonToken.AccessToken, "/contributor/v1/counters", map[string]string{"X-CCGSC-Install": "host-browser"})
	if code != http.StatusOK {
		t.Fatalf("install status = %d, body %v", code, anonOwn)
	}
	if anonOwn["acceptedTasks"] != float64(1) || anonOwn["computeMs"] != float64(800) {
		t.Fatalf("install counters = %v, want 1 task and 800 ms", anonOwn)
	}

	code, totals := getJSON(t, handler, admin, "/admin/v1/contributor-counters")
	if code != http.StatusOK {
		t.Fatalf("totals status = %d, body %v", code, totals)
	}
	// lease_settled is not a contributor counter. Three accepted samples and
	// 1500+800 ms of compute are.
	if totals["acceptedTasks"] != float64(3) || totals["computeMs"] != float64(2300) {
		t.Fatalf("totals = %v, want 3 tasks and 2300 ms", totals)
	}
	code, alias := getJSON(t, handler, admin, "/admin/contributor-counters")
	if code != http.StatusOK || alias["acceptedTasks"] != totals["acceptedTasks"] {
		t.Fatalf("alias status/body = %d %v", code, alias)
	}
}
