package pool

import (
	"context"
	"errors"
	"testing"
	"time"

	"ccgsc/poolscore/internal/store"
)

func TestR3BBackoffCeiling(t *testing.T) {
	want := []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second}
	got := want[0]
	for i, d := range want {
		if got != d {
			t.Fatalf("backoff[%d]=%s want %s", i, got, d)
		}
		got = nextRecomputeNotifyBackoff(got)
	}
}

func TestR3BPublisherConnectionReused(t *testing.T) {
	h := newPGEngineHarness(t)
	stop, err := h.e.StartRecomputeSettingsNotify(context.Background(), h.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	h.e.recomputeNotify.mu.RLock()
	publisher := h.e.recomputeNotify.run.publisher
	h.e.recomputeNotify.mu.RUnlock()
	zero := 0
	mode := RecomputeModeRegistry
	if _, err := h.e.CommitRecomputeSettings(context.Background(), Thread, RecomputeSettingsDelta{Mode: &mode}, &zero, "writer"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.e.CommitRecomputeSettings(context.Background(), Process, RecomputeSettingsDelta{Mode: &mode}, &zero, "writer"); err != nil {
		t.Fatal(err)
	}
	stat := publisher.Stat()
	if stat.NewConnsCount() != 1 || stat.AcquireCount() < 2 || stat.MaxConns() != 1 {
		t.Fatalf("expected one reused publisher connection, created=%d acquired=%d max=%d", stat.NewConnsCount(), stat.AcquireCount(), stat.MaxConns())
	}
}

type r3bContextViewStore struct {
	store.Store
	deadline bool
}

func (s *r3bContextViewStore) View(ctx context.Context, fn func(store.Reader) error) error {
	_, s.deadline = ctx.Deadline()
	return ctx.Err()
}
func TestR3BRefreshUsesLifecycleDeadline(t *testing.T) {
	h := newHarness(t)
	wrapped := &r3bContextViewStore{Store: h.engine.Store}
	h.engine.Store = wrapped
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.engine.refreshRecomputePool(ctx, Thread); !errors.Is(err, context.Canceled) {
		t.Fatalf("refresh ignored cancellation: %v", err)
	}
	if !wrapped.deadline {
		t.Fatal("refresh read lacked a deadline")
	}
}
