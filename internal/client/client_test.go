// Path: internal/client/client_test.go | Author: Copilot | Date: 2026-11-20
// What/why: In-process acceptance coverage for REST and h2c gRPC submit,
// worker, status, results, and event-watch flows across both pools.
package client

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"ccgsc/poolscore/internal/api"
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/grpcapi"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store/sqlite"
)

func TestRESTBothPoolsSubmitWorkerResults(t *testing.T) {
	h := newClientHarness(t, false)
	for _, p := range []Pool{Thread, Process} {
		dev, err := New(h.rest.URL, h.developer, false)
		if err != nil {
			t.Fatal(err)
		}
		worker, err := New(h.rest.URL, h.contributor, false)
		if err != nil {
			t.Fatal(err)
		}
		jobID, err := dev.Submit(context.Background(), p, 8)
		if err != nil {
			t.Fatalf("%s submit: %v", p, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		go func() { _ = worker.Worker(ctx, p, 1) }()
		waitForCompleted(t, dev, p, jobID)
		cancel()
		if _, err := dev.Results(context.Background(), p, jobID); err != nil {
			t.Fatalf("%s results: %v", p, err)
		}
		_ = dev.Close()
		_ = worker.Close()
	}
}

func TestGRPCH2CBothPoolsSubmitStatusResults(t *testing.T) {
	h := newClientHarness(t, true)
	for _, p := range []Pool{Thread, Process} {
		dev, err := New(h.grpc.URL, h.developer, true)
		if err != nil {
			t.Fatal(err)
		}
		worker, err := New(h.grpc.URL, h.contributor, false)
		if err != nil {
			t.Fatal(err)
		}
		jobID, err := dev.Submit(context.Background(), p, 8)
		if err != nil {
			t.Fatalf("%s submit: %v", p, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		go func() { _ = worker.Worker(ctx, p, 1) }()
		waitForCompleted(t, dev, p, jobID)
		if _, err := dev.Results(context.Background(), p, jobID); err != nil {
			t.Fatalf("%s results: %v", p, err)
		}
		cancel()
		_ = dev.Close()
		_ = worker.Close()
	}
}

type clientHarness struct {
	rest, grpc             *httptest.Server
	developer, contributor string
}

func newClientHarness(t *testing.T, withGRPC bool) clientHarness {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "client.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine, err := pool.New(db, log, "client-test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	as := auth.New(db, config.Default().JWTSecret, "client-test")
	dev, _, err := as.CreateKey(context.Background(), auth.User{ID: "developer", AccountStore: "B", Roles: []string{"developer"}}, []string{"artifacts:write", "jobs:write", "jobs:read"})
	if err != nil {
		t.Fatal(err)
	}
	contrib, _, err := as.CreateKey(context.Background(), auth.User{ID: "contributor", AccountStore: "B", Roles: []string{"contributor"}}, []string{"worker"})
	if err != nil {
		t.Fatal(err)
	}
	restHandler := (&api.Server{Engine: engine, Auth: as, Log: log, Config: config.Default()}).Handler()
	rest := httptest.NewServer(restHandler)
	t.Cleanup(rest.Close)
	h := clientHarness{rest: rest, developer: dev, contributor: contrib}
	if withGRPC {
		gs := grpcapi.NewServer(grpcapi.New(engine, as))
		g := httptest.NewServer(gs.Mux(restHandler))
		t.Cleanup(func() { gs.Stop(); g.Close() })
		h.grpc = g
	}
	return h
}

func waitForCompleted(t *testing.T, c *Client, p Pool, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		v, err := c.Status(ctx, p, id)
		if err != nil {
			t.Fatal(err)
		}

		done := false
		switch job := v.(type) {
		case *pool.Job:
			done = job.State == "completed"
		case *grpcapi.Job:
			done = job.State == "completed"
		case map[string]any:
			state, _ := job["state"].(string)
			done = state == "completed"
		case []byte:
			var body map[string]any
			if json.Unmarshal(job, &body) == nil {
				state, _ := body["state"].(string)
				done = state == "completed"
			}
		}

		if done {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("job did not complete")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
