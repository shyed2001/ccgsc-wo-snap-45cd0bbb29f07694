// Path: cmd/pools-e2e/full_T108_test.go | Author: GLM for Claude Opus 5.5 | 2026-11-21
// What/why: T108 -full suite test, split from main_test.go at merge time so the
// r10 acct 1/2 tests and this test do not collide.
package main

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ccgsc/poolscore/internal/api"
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/grpcapi"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store/sqlite"
)

// TestFullFlagSuite runs the whole T108 -full suite against an in-process
// server with REST and h2c gRPC on one port, exactly like cmd/pools-core wires
// them. Every check must pass and the written report must stay secret-free.
func TestFullFlagSuite(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "full.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine, err := pool.New(db, log, "full-e2e", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	as := auth.New(db, config.Default().JWTSecret, "full-e2e")
	adminKey, _, err := as.CreateKey(context.Background(), auth.User{
		ID: "full-e2e-admin", AccountStore: "A", Roles: []string{"admin"},
	}, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	rest := (&api.Server{Engine: engine, Auth: as, Log: log, Config: config.Default()}).Handler()
	gs := grpcapi.NewServer(grpcapi.New(engine, as))
	srv := httptest.NewServer(gs.Mux(rest))
	t.Cleanup(func() { gs.Stop(); srv.Close() })

	out := filepath.Join(t.TempDir(), "full-report.json")
	rep, err := runFull(context.Background(), options{
		base: srv.URL, pool: "both", size: 160, timeout: 60 * time.Second,
	}, adminKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Pass {
		t.Fatalf("full suite failed:\n%s", rep.Table())
	}
	if rep.RevokedKeys != 2 {
		t.Fatalf("revokedKeys = %d, want 2:\n%s", rep.RevokedKeys, rep.Table())
	}
	if err := writeFullReport(out, rep); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, adminKey) || strings.Contains(text, "pc_") {
		t.Fatal("full report leaked key material:\n" + text)
	}
	if !strings.Contains(text, `"mode": "full"`) || !strings.Contains(text, `"pass": true`) {
		t.Fatal("full report misses the mode or pass field:\n" + text)
	}
}
