package pool

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// N4 A04: even if a second Engine was loaded before the first write,
// a stale expectedVersion must be checked against the durable record.
func TestN4SecondEngineCannotOverwriteCommittedRecomputeVersion(t *testing.T) {
	h := newHarness(t)
	other, err := New(h.engine.Store, h.engine.Log, "other-server", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	zero := 0
	a := RecomputeModeRegistry
	if _, err := h.engine.CommitRecomputeSettings(context.Background(), Thread, RecomputeSettingsDelta{Mode: &a}, &zero, "first"); err != nil {
		t.Fatal(err)
	}
	b := RecomputeModeOff
	if _, err := other.CommitRecomputeSettings(context.Background(), Thread, RecomputeSettingsDelta{Mode: &b}, &zero, "second"); !errors.Is(err, ErrSettingsConflict) {
		t.Fatalf("a second server overwrote a committed version despite expectedVersion=0: %v", err)
	}
	restarted, err := New(h.engine.Store, h.engine.Log, "restarted", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := restarted.SavedRecomputeSettings(Thread)
	if !ok || got.Version != 1 || got.Mode != RecomputeModeRegistry {
		t.Fatalf("durable settings after rejected stale write = %+v ok=%v", got, ok)
	}
}

// N4 A04 PG: separate Engine caches, same PostgreSQL transaction store,
// simultaneous first writers. Exactly one may create version 1; another
// must lose the conditional insert and get a version conflict.
func TestN4PGConcurrentFirstRecomputeSettingsWriteIsCAS(t *testing.T) {
	h := newPGEngineHarness(t)
	other, err := New(h.e.Store, h.e.Log, "second-server", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	mode := RecomputeModeRegistry
	zero := 0
	engines := []*Engine{h.e, other}
	results := make(chan error, len(engines))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, e := range engines {
		wg.Add(1)
		go func(i int, e *Engine) {
			defer wg.Done()
			<-start
			_, err := e.CommitRecomputeSettings(context.Background(), Thread, RecomputeSettingsDelta{Mode: &mode}, &zero, "writer")
			results <- err
		}(i, e)
	}
	close(start)
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrSettingsConflict):
			conflicts++
		default:
			t.Fatalf("unexpected PG settings write error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent PG first-writes: successes=%d conflicts=%d, require one each", successes, conflicts)
	}
}

// A04: a durable existing version is row-locked and checked inside the
// transaction. Separate engines using version 1 cannot both write version 2.
func TestN4PGConcurrentExistingRecomputeSettingsWriteIsCAS(t *testing.T) {
	h := newPGEngineHarness(t)
	mode := RecomputeModeRegistry
	zero := 0
	seeded, err := h.e.CommitRecomputeSettings(context.Background(), Thread, RecomputeSettingsDelta{Mode: &mode}, &zero, "seed")
	if err != nil || seeded.Version != 1 {
		t.Fatalf("seed: %+v %v", seeded, err)
	}
	second, err := New(h.e.Store, h.e.Log, "second-server", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	one := 1
	busy := RecomputeBusyDefer
	malformed := RecomputeMalformedFraud
	results := make(chan error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, fn := range []func() error{
		func() error {
			_, er := h.e.CommitRecomputeSettings(context.Background(), Thread, RecomputeSettingsDelta{BusyPolicy: &busy}, &one, "first")
			return er
		},
		func() error {
			_, er := second.CommitRecomputeSettings(context.Background(), Thread, RecomputeSettingsDelta{MalformedPolicy: &malformed}, &one, "second")
			return er
		},
	} {
		wg.Add(1)
		go func(fn func() error) { defer wg.Done(); <-start; results <- fn() }(fn)
	}
	close(start)
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrSettingsConflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected PG error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("existing version CAS: successes=%d conflicts=%d", successes, conflicts)
	}
	restarted, err := New(h.e.Store, h.e.Log, "third-server", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	stored, ok := restarted.SavedRecomputeSettings(Thread)
	if !ok || stored.Version != 2 || stored.Mode != mode {
		t.Fatalf("durable after contention: %+v ok=%v", stored, ok)
	}
}
