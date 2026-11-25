// Path: cmd/pools-client/main_test.go | Author: GLM for Claude Opus 5.5 | 2026-11-21
// What/why: the check subcommand runs the shared T108 suite against an
// in-process REST + h2c gRPC server and fails with an error when a check fails.
package main

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
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

func TestCheckCommandRunsSharedSuite(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "client-check.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine, err := pool.New(db, log, "client-check", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	as := auth.New(db, config.Default().JWTSecret, "client-check")
	raw, _, err := as.CreateKey(context.Background(), auth.User{
		ID: "client-check-admin", AccountStore: "A", Roles: []string{"admin"},
	}, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	rest := (&api.Server{Engine: engine, Auth: as, Log: log, Config: config.Default()}).Handler()
	gs := grpcapi.NewServer(grpcapi.New(engine, as))
	srv := httptest.NewServer(gs.Mux(rest))
	t.Cleanup(func() { gs.Stop(); srv.Close() })

	o := options{base: srv.URL, pool: "thread", size: 160, key: raw, timeout: 60 * time.Second}
	if err := runCheck(context.Background(), o); err != nil {
		t.Fatalf("check command failed: %v", err)
	}
}

func TestCheckCommandRejectsBadPool(t *testing.T) {
	o := options{base: "http://127.0.0.1:1", pool: "nope", key: "k"}
	err := runCheck(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "-pool must be") {
		t.Fatalf("runCheck with a bad pool = %v, want the pool error", err)
	}
}
