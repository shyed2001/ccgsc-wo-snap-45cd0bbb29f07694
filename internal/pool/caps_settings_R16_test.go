// Path: internal/pool/caps_settings_R16_test.go | Author: Codex for Claude Opus 5.5 | 2026-11-22
// What/why: R16-FIX (ChatGPT review F01/F02) - the admin caps of the server are
// durable in the same store the pool settings use, they merge a partial write
// against the effective configuration, they are versioned like the pool settings
// (R11-D2) and they are reloaded by a new engine on the same store.
package pool

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ccgsc/poolscore/internal/caps"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/sqlite"
)

// newCapsHarness builds an engine whose configured caps are a nondefault set.
func newCapsHarness(t *testing.T) (*Engine, caps.Config) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "caps.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	engine, err := New(db, slog.New(slog.NewJSONHandler(io.Discard, nil)), "s1", time.Minute)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	configured := caps.Config{MaxProcs: 2, MemLimitMB: 512, WorkerPool: 3, DBMaxOpenConns: 8}
	engine.SetConfiguredCaps(configured)
	return engine, configured
}

// TestCapsSettingsPartialWriteMergesOntoTheConfiguration pins F02 at the engine:
// a delta naming one field keeps the configured values of the others and the
// durable record carries them, not zeros.
func TestCapsSettingsPartialWriteMergesOntoTheConfiguration(t *testing.T) {
	engine, configured := newCapsHarness(t)
	ctx := context.Background()
	mem := 256
	saved, err := engine.CommitCapsSettings(ctx, CapsSettingsDelta{MemLimitMB: &mem}, nil, "admin-user")
	if err != nil {
		t.Fatalf("CommitCapsSettings: %v", err)
	}
	if saved.MaxProcs != configured.MaxProcs || saved.WorkerPool != configured.WorkerPool || saved.DBMaxOpenConns != configured.DBMaxOpenConns {
		t.Fatalf("saved = %+v, want the configured values of the unnamed fields", saved)
	}
	if saved.MemLimitMB != 256 || saved.Version != 1 {
		t.Fatalf("saved = %+v, want memLimitMB=256 version=1", saved)
	}
	// The next delta merges onto the stored record, not onto the configuration.
	procs := 1
	saved, err = engine.CommitCapsSettings(ctx, CapsSettingsDelta{MaxProcs: &procs}, nil, "admin-user")
	if err != nil {
		t.Fatalf("CommitCapsSettings: %v", err)
	}
	if saved.MaxProcs != 1 || saved.MemLimitMB != 256 || saved.Version != 2 {
		t.Fatalf("merged = %+v, want both deltas", saved)
	}
	// A fresh engine on the same store loads the record (F01).
	reloaded, err := New(engine.Store, engine.Log, "s1", time.Minute)
	if err != nil {
		t.Fatalf("New after restart: %v", err)
	}
	got, ok := reloaded.SavedCapsSettings()
	if !ok || got != saved {
		t.Fatalf("reloaded = %+v (ok=%v), want %+v", got, ok, saved)
	}
}

// TestCapsSettingsStaleVersionBeatsInvalidDelta pins R11-D2 for the caps: a
// stale expectedVersion answers ErrSettingsConflict even when the merged record
// would be invalid; a fresh one still answers ErrInvalid.
func TestCapsSettingsStaleVersionBeatsInvalidDelta(t *testing.T) {
	engine, _ := newCapsHarness(t)
	ctx := context.Background()
	zero := 0
	if _, err := engine.CommitCapsSettings(ctx, CapsSettingsDelta{}, &zero, "admin-user"); err != nil {
		t.Fatalf("CommitCapsSettings: %v", err)
	}
	bad := -1
	if _, err := engine.CommitCapsSettings(ctx, CapsSettingsDelta{MemLimitMB: &bad}, &zero, "admin-user"); !errors.Is(err, ErrSettingsConflict) {
		t.Fatalf("stale invalid delta = %v, want ErrSettingsConflict", err)
	}
	one := 1
	if _, err := engine.CommitCapsSettings(ctx, CapsSettingsDelta{MemLimitMB: &bad}, &one, "admin-user"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("fresh invalid delta = %v, want ErrInvalid", err)
	}
	if got, _ := engine.SavedCapsSettings(); got.Version != 1 {
		t.Fatalf("version = %d, want the refused writes to change nothing", got.Version)
	}
}

// TestCapsSettingsConcurrentDeltas runs the two-writer merge against the engine
// itself, so -race covers the caps write lock.
func TestCapsSettingsConcurrentDeltas(t *testing.T) {
	engine, _ := newCapsHarness(t)
	ctx := context.Background()
	procs, mem := 2, 256
	for i := 0; i < 50; i++ {
		start := make(chan struct{})
		errs := make([]error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		for n, delta := range []CapsSettingsDelta{
			{MaxProcs: &procs},
			{MemLimitMB: &mem},
		} {
			n, delta := n, delta
			go func() {
				defer wg.Done()
				<-start
				_, errs[n] = engine.CommitCapsSettings(ctx, delta, nil, "admin-user")
			}()
		}
		close(start)
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatalf("iteration %d: CommitCapsSettings: %v", i, err)
			}
		}
		got, ok := engine.SavedCapsSettings()
		if !ok || got.MaxProcs != 2 || got.MemLimitMB != 256 {
			t.Fatalf("iteration %d: stored = %+v, want every delta preserved", i, got)
		}
		if got.Version != 2*(i+1) {
			t.Fatalf("iteration %d: version = %d, want %d", i, got.Version, 2*(i+1))
		}
	}
}

// capsFailUpdateStore fails every update, so the persist path can be injected
// to fail: the record and the in-memory cache must both stay unchanged.
type capsFailUpdateStore struct {
	store.Store
	fail bool
}

func (s *capsFailUpdateStore) Update(ctx context.Context, fn func(store.Tx) error) error {
	if s.fail {
		return errCapsSettingsStoreDown
	}
	return s.Store.Update(ctx, fn)
}

var errCapsSettingsStoreDown = capsSettingsStoreError("the store is down")

type capsSettingsStoreError string

func (e capsSettingsStoreError) Error() string { return string(e) }

// TestCapsSettingsStoreFailureChangesNothing pins F03 at the engine: a failed
// store answers the error, leaves the cache unchanged and writes nothing.
func TestCapsSettingsStoreFailureChangesNothing(t *testing.T) {
	engine, configured := newCapsHarness(t)
	ctx := context.Background()
	failing := &capsFailUpdateStore{Store: engine.Store, fail: true}
	engine.Store = failing
	procs := 1
	if _, err := engine.CommitCapsSettings(ctx, CapsSettingsDelta{MaxProcs: &procs}, nil, "admin-user"); err == nil {
		t.Fatal("CommitCapsSettings with a failing store = nil, want an error")
	}
	if got, ok := engine.SavedCapsSettings(); ok {
		t.Fatalf("a failed store must leave no saved record, got %+v", got)
	}
	eff, err := engine.EffectiveCaps()
	if err != nil {
		t.Fatalf("EffectiveCaps: %v", err)
	}
	if eff.MaxProcs != configured.MaxProcs {
		t.Fatalf("effective maxProcs = %d, want the configured %d", eff.MaxProcs, configured.MaxProcs)
	}
	if err := engine.Store.View(ctx, func(r store.Reader) error {
		return r.Get(store.Metrics, capsSettingsKey, &struct{}{})
	}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the caps record = %v, want store.ErrNotFound", err)
	}
}
