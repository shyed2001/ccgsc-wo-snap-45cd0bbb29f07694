package pool

import (
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/sqlite"
	"ccgsc/poolscore/internal/verify"
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

func newSessionR2Engine(t *testing.T) (*Engine, func()) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)), "r2", time.Minute)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	e.Now = func() time.Time { return now }
	return e, func() { _ = db.Close() }
}

func TestSessionR2StaleTabEntersDrain(t *testing.T) {
	e, done := newSessionR2Engine(t)
	defer done()
	ctx := context.Background()
	data := []byte("module")
	id := verify.SHA256(data)
	if _, err := e.Upload(ctx, id, "application/octet-stream", 0, int64(len(data)), data); err != nil {
		t.Fatal(err)
	}
	w, err := e.Register(ctx, Process, "worker-owner", Worker{HostID: "host", Slots: 1})
	if err != nil {
		t.Fatal(err)
	}
	s, err := e.OpenSession(ctx, "developer", "", SessionRequest{TabCount: 1, ModuleArtifactIDs: []string{id}})
	if err != nil {
		t.Fatal(err)
	}
	now := e.Now().Add(3 * e.TTL)
	e.Now = func() time.Time { return now }
	if _, err = e.GetSession(ctx, "developer", s.SessionID); err != nil {
		t.Fatal(err)
	}
	got, err := e.GetSession(ctx, "developer", s.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if (got.State != "draining" && got.State != "closed") || len(got.Warnings) == 0 || got.Tabs[0].TabID != w.TabID {
		t.Fatalf("stale session = %+v", got)
	}
}

func TestRegisterR2ReattachesStaleHost(t *testing.T) {
	e, done := newSessionR2Engine(t)
	defer done()
	ctx := context.Background()
	first, err := e.Register(ctx, Process, "owner", Worker{HostID: "host", Slots: 1})
	if err != nil {
		t.Fatal(err)
	}
	now := e.Now().Add(3 * e.TTL)
	e.Now = func() time.Time { return now }
	second, err := e.Register(ctx, Process, "owner", Worker{HostID: "host", Slots: 1})
	if err != nil {
		t.Fatal(err)
	}
	if second.TabID != first.TabID {
		t.Fatalf("reattach minted tab %q instead of reusing %q", second.TabID, first.TabID)
	}
}

func TestArtifactR2ErrorsDoNotReturnData(t *testing.T) {
	e, done := newSessionR2Engine(t)
	defer done()
	data := []byte("module")
	_, err := e.Upload(context.Background(), verify.SHA256(data), "application/x-unknown", 0, int64(len(data)), data)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Upload error = %v, want ErrInvalid", err)
	}
	a, err := e.Artifact(context.Background(), verify.SHA256(data))
	if !errors.Is(err, store.ErrNotFound) || a.Data != nil {
		t.Fatalf("Artifact = %+v, error %v", a, err)
	}
}
