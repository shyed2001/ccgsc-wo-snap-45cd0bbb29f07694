package pool

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestR3BConcurrentStartReservesOneGeneration(t *testing.T) {
	h := newPGEngineHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		stop func()
		err  error
	}
	const callers = 6
	start := make(chan struct{})
	results := make(chan outcome, callers)
	for i := 0; i < callers; i++ {
		go func() {
			<-start
			stop, err := h.e.StartRecomputeSettingsNotify(ctx, h.dsn)
			results <- outcome{stop, err}
		}()
	}
	close(start)
	successes, rejected := 0, 0
	for i := 0; i < callers; i++ {
		got := <-results
		if got.err == nil {
			successes++
			defer got.stop()
		} else if errors.Is(got.err, ErrInvalid) {
			rejected++
		} else {
			t.Errorf("unexpected start error: %v", got.err)
		}
	}
	if successes != 1 || rejected != callers-1 {
		t.Fatalf("concurrent start successes=%d rejected=%d", successes, rejected)
	}
	if !h.e.RecomputeNotifyEnabled() {
		t.Fatal("winning listener not active")
	}
}

func TestR3BParentCancelClearsGenerationAndPermitsRestart(t *testing.T) {
	h := newPGEngineHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	stop, err := h.e.StartRecomputeSettingsNotify(ctx, h.dsn)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer stop()
	cancel()
	deadline := time.Now().Add(6 * time.Second)
	for h.e.RecomputeNotifyEnabled() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if h.e.RecomputeNotifyEnabled() {
		t.Fatal("parent cancellation left stale enabled state")
	}
	restart, err := h.e.StartRecomputeSettingsNotify(context.Background(), h.dsn)
	if err != nil {
		t.Fatalf("restart after parent cancellation: %v", err)
	}
	defer restart()
	stop() // old stop must not erase a new generation
	if !h.e.RecomputeNotifyEnabled() {
		t.Fatal("old stop cleared restarted listener")
	}
}

func TestR3BFailedStartupReleasesReservation(t *testing.T) {
	h := newPGEngineHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if stop, err := h.e.StartRecomputeSettingsNotify(ctx, h.dsn); err == nil {
		stop()
		t.Fatal("startup succeeded with cancelled parent")
	}
	stop, err := h.e.StartRecomputeSettingsNotify(context.Background(), h.dsn)
	if err != nil {
		t.Fatalf("restart after failed start: %v", err)
	}
	defer stop()
}
