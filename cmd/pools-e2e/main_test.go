// Path: cmd/pools-e2e/main_test.go | Author: Copilot | Date: 2026-11-20
// What/why: In-process SQLite acceptance test proving the builtin worker completes
// the same matmul flow through both REST pool prefixes.
package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"ccgsc/poolscore/internal/api"
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store/sqlite"
)

func TestRunBothPoolsBuiltinWorker(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "e2e.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine, err := pool.New(db, log, "e2e-server", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	as := auth.New(db, config.Default().JWTSecret, "e2e-server")
	adminKey, _, err := as.CreateKey(context.Background(), auth.User{
		ID: "e2e-admin", AccountStore: "A", Roles: []string{"admin"},
	}, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&api.Server{Engine: engine, Auth: as, Log: log}).Handler())
	defer server.Close()

	r, err := run(context.Background(), options{
		base: server.URL, pool: "both", worker: "builtin", size: 160, timeout: 10 * time.Second,
	}, adminKey)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Pass || len(r.Pools) != 2 {
		t.Fatalf("report = %+v", r)
	}
	if r.RevokedKeys != 2 {
		t.Fatalf("revokedKeys = %d, want 2", r.RevokedKeys)
	}
	rawReport, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	reportText := string(rawReport)
	if strings.Contains(reportText, "pc_") || strings.Contains(reportText, "contributorKey") {
		t.Fatalf("report leaked key material or the raw contributor key: %s", reportText)
	}
	for _, p := range r.Pools {
		if p.Status != "PASS" || p.FinalState != "completed" || p.AcceptedResults != 1 {
			t.Fatalf("pool report = %+v", p)
		}
	}
}

// TestRerunSharesArtifactBlob (T100): the same server serves two full runs in
// a row. The fixed artifact bytes of the second run join the blob of the first
// run with a fresh per-owner reference, so a rerun no longer needs unique
// bytes (owner decision 2026-11-21, option A).
func TestRerunSharesArtifactBlob(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "e2e-rerun.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine, err := pool.New(db, log, "e2e-server", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	as := auth.New(db, config.Default().JWTSecret, "e2e-server")
	adminKey, _, err := as.CreateKey(context.Background(), auth.User{
		ID: "e2e-admin", AccountStore: "A", Roles: []string{"admin"},
	}, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&api.Server{Engine: engine, Auth: as, Log: log}).Handler())
	defer server.Close()

	opts := options{base: server.URL, pool: "both", worker: "builtin", size: 160, timeout: 10 * time.Second}
	first, err := run(context.Background(), opts, adminKey)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Pass {
		t.Fatalf("first run = %+v", first)
	}
	second, err := run(context.Background(), opts, adminKey)
	if err != nil {
		t.Fatalf("rerun with identical artifact bytes: %v", err)
	}
	if !second.Pass {
		t.Fatalf("rerun = %+v", second)
	}
	for _, p := range second.Pools {
		if p.Status != "PASS" || p.FinalState != "completed" || p.AcceptedResults != 1 {
			t.Fatalf("rerun pool report = %+v", p)
		}
	}
}

// TestRunNoneWorkerKeyGoesToSidecarNotReport is the T101 acceptance test: in
// -worker none mode the contributor API key is handed to the browser worker
// through a separate 0600 file, and neither the report struct nor the JSON
// report file carries any key material.
func TestRunNoneWorkerKeyGoesToSidecarNotReport(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "e2e.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine, err := pool.New(db, log, "e2e-server", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	as := auth.New(db, config.Default().JWTSecret, "e2e-server")
	adminKey, _, err := as.CreateKey(context.Background(), auth.User{
		ID: "e2e-admin", AccountStore: "A", Roles: []string{"admin"},
	}, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&api.Server{Engine: engine, Auth: as, Log: log}).Handler())
	defer server.Close()

	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "pools-e2e.json")
	// One pool keeps the run inside the 2s timeout: the none-mode observation
	// loop polls for the whole timeout because no browser worker completes
	// the job, and the key revocations must still run inside the same context.
	r, err := run(context.Background(), options{
		base: server.URL, pool: "thread", worker: "none", size: 160, timeout: 2 * time.Second, out: outPath,
	}, adminKey)
	if err != nil {
		t.Fatal(err)
	}
	if r.WorkerKeyFile == "" {
		t.Fatal("none mode did not record the worker key sidecar file")
	}
	sidecar, err := os.ReadFile(r.WorkerKeyFile)
	if err != nil {
		t.Fatalf("sidecar file: %v", err)
	}
	var doc struct {
		ContributorKey string `json:"contributorKey"`
	}
	if err := json.Unmarshal(sidecar, &doc); err != nil || !strings.HasPrefix(doc.ContributorKey, "pc_") {
		t.Fatalf("sidecar does not carry the contributor key: %q (%v)", sidecar, err)
	}
	info, err := os.Stat(r.WorkerKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0600 {
		t.Fatalf("sidecar file mode = %o, want 600", got)
	}
	if want := filepath.Join(outDir, "pools-e2e-worker-key.json"); r.WorkerKeyFile != want {
		t.Fatalf("sidecar path = %q, want %q", r.WorkerKeyFile, want)
	}

	// Neither the marshaled report nor the written report file may carry the
	// key value or any pc_ key material.
	rawReport, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	reportText := string(rawReport)
	if strings.Contains(reportText, doc.ContributorKey) || strings.Contains(reportText, "pc_") {
		t.Fatalf("report struct leaked key material: %s", reportText)
	}
	if err := writeReport(outPath, r); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(written), doc.ContributorKey) || strings.Contains(string(written), "pc_") {
		t.Fatalf("JSON report file leaked key material: %s", written)
	}
	if !strings.Contains(string(written), `"workerKeyFile"`) {
		t.Fatalf("JSON report does not point at the sidecar file: %s", written)
	}
	// Key revocations are not asserted here: without a browser worker the job
	// stays pending for the whole timeout, so the run context has expired by
	// the time revocations run. That is the pre-existing none-mode behaviour
	// and outside the T101 scope of this test.
}
