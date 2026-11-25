package pool

import (
	"ccgsc/poolscore/internal/store/pg"
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// Two isolated DB pools, distinct Engine caches, the same private PostgreSQL
// schema. No restart or in-process settings pointers shared.
func TestN11PostgresNotifyRefreshesSecondEngineWithoutRestart(t *testing.T) {
	h := newPGEngineHarness(t)
	secondStore, err := pg.Open(h.dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondStore.Close() })
	second, err := New(secondStore, slog.New(slog.NewTextHandler(io.Discard, nil)), "n11-second", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second.RecomputeMode(Thread) != RecomputeModeOff {
		t.Fatal("expected default off")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, err := second.StartRecomputeSettingsNotify(ctx, h.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	stopWriter, err := h.e.StartRecomputeSettingsNotify(ctx, h.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer stopWriter()
	mode := RecomputeModeRegistry
	zero := 0
	if _, err = h.e.CommitRecomputeSettings(context.Background(), Thread, RecomputeSettingsDelta{Mode: &mode}, &zero, "n11-first"); err != nil {
		t.Fatal(err)
	}
	// A plain commit requires the caller opt in to publishing (see N11); ensure
	// a notifier-equipped writer sends the notification, and reader refreshes.

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s, ok := second.SavedRecomputeSettings(Thread); ok && s.Version == 1 && s.Mode == RecomputeModeRegistry {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("second engine did not refresh after PostgreSQL NOTIFY: %+v", second.RecomputeMode(Thread))
}

func TestN11SingleServerKeepsNoNotifyListener(t *testing.T) {
	h := newPGEngineHarness(t)
	if h.e.RecomputeNotifyEnabled() {
		t.Fatal("listener must be disabled by default")
	}
}

func TestN11PostgresNotifySubsequentVersionAcrossEngines(t *testing.T) {
	h := newPGEngineHarness(t)
	secondStore, err := pg.Open(h.dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondStore.Close() })
	second, err := New(secondStore, slog.New(slog.NewTextHandler(io.Discard, nil)), "n11-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, err := second.StartRecomputeSettingsNotify(ctx, h.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	stopWriter, err := h.e.StartRecomputeSettingsNotify(ctx, h.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer stopWriter()
	mode := RecomputeModeRegistry
	zero := 0
	if _, err = h.e.CommitRecomputeSettings(ctx, Process, RecomputeSettingsDelta{Mode: &mode}, &zero, "first"); err != nil {
		t.Fatal(err)
	}
	waitVersion := func(version int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if saved, ok := second.SavedRecomputeSettings(Process); ok && saved.Version == version {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("Process version %d not observed, got %+v", version, second.RecomputeMode(Process))
	}
	waitVersion(1)
	busy := RecomputeBusyDefer
	one := 1
	if _, err = h.e.CommitRecomputeSettings(ctx, Process, RecomputeSettingsDelta{BusyPolicy: &busy}, &one, "second"); err != nil {
		t.Fatal(err)
	}
	waitVersion(2)
	if second.RecomputeBusyPolicy(Process) != RecomputeBusyDefer {
		t.Fatal("new busy policy not visible")
	}
	if second.RecomputeNotifyEnabled() != true {
		t.Fatal("listener unexpectedly disabled")
	}
	stop()
	if second.RecomputeNotifyEnabled() {
		t.Fatal("listener remains enabled after stop")
	}
}
