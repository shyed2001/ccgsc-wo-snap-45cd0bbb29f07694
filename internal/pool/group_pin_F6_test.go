package pool

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"ccgsc/poolscore/internal/store/sqlite"
	"ccgsc/poolscore/internal/verify"
)

func newF6Engine(t *testing.T) (*Engine, func()) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)), "f6", time.Minute)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return e, func() { _ = db.Close() }
}

func TestF6GroupByBatchesOneGroup(t *testing.T) {
	e, done := newF6Engine(t)
	defer done()
	ctx := context.Background()
	module := []byte("module")
	moduleID := verify.SHA256(module)
	if _, err := e.Upload(ctx, moduleID, "application/octet-stream", 0, int64(len(module)), module); err != nil {
		t.Fatal(err)
	}
	w, err := e.Register(ctx, Process, "owner", Worker{HostID: "host", Slots: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Create(ctx, Process, "owner", "", CreateRequest{
		ModuleArtifactIDs: []string{moduleID},
		BatchSize:         3,
		GroupBy:           json.RawMessage(`{"mode":"tab","key":"group"}`),
		Tasks: []TaskSpec{
			{TaskID: "a", Input: json.RawMessage(`{"group":"one","value":1}`)},
			{TaskID: "b", Input: json.RawMessage(`{"group":"one","value":2}`)},
			{TaskID: "c", Input: json.RawMessage(`{"group":"two","value":3}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.Claim(ctx, Process, "owner", w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Batch) != 2 {
		t.Fatalf("batch size = %d, want two same-group tasks", len(a.Batch))
	}
}

func TestF6PinAndWarmInitializer(t *testing.T) {
	e, done := newF6Engine(t)
	defer done()
	ctx := context.Background()
	data := []byte("warm")
	id := verify.SHA256(data)
	if _, err := e.Upload(ctx, id, "application/octet-stream", 0, int64(len(data)), data); err != nil {
		t.Fatal(err)
	}

	first, err := e.Register(ctx, Process, "owner", Worker{HostID: "host-a", Slots: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.Register(ctx, Process, "owner", Worker{HostID: "host-b", Slots: 1})
	if err != nil {
		t.Fatal(err)
	}
	s, err := e.OpenSession(ctx, "developer", "", SessionRequest{
		TabCount:                  1,
		ModuleArtifactIDs:         []string{id},
		WarmInitializerArtifactID: id,
		Pin:                       json.RawMessage(`{"hostIds":["host-b"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.WarmInitializerArtifactID != id || s.WarmState != "warm" || len(s.Tabs) != 1 || s.Tabs[0].TabID != second.TabID || s.Tabs[0].TabID == first.TabID {
		t.Fatalf("session placement = %+v", s)
	}
}

func TestF8WarmInitializerGateWithoutModules(t *testing.T) {
	e, done := newF6Engine(t)
	defer done()
	ctx := context.Background()
	data := []byte("not wasm")
	id := verify.SHA256(data)
	if _, err := e.Upload(ctx, id, "application/wasm", 0, int64(len(data)), data); !errors.Is(err, ErrArtifactRejected) {
		t.Fatalf("warm initializer fixture was not rejected: %v", err)
	}
	if _, err := e.Register(ctx, Process, "owner", Worker{HostID: "host", Slots: 1}); err != nil {
		t.Fatal(err)
	}
	_, err := e.OpenSession(ctx, "developer", "", SessionRequest{
		TabCount:                  1,
		WarmInitializerArtifactID: id,
	})
	if !errors.Is(err, ErrArtifactRejected) {
		t.Fatalf("rejected warm initializer bypassed the gate: %v", err)
	}
}
