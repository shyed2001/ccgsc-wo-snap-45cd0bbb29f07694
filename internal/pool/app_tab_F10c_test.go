package pool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"ccgsc/poolscore/internal/appegress"
	"ccgsc/poolscore/internal/store"
)

// f10cFence reads the durable physical-tab fence of (host, tab) without locking.
func f10cFence(t *testing.T, e *Engine, host, tab string) appegress.TabFence {
	t.Helper()
	id, err := appegress.TabIdentity(host, tab)
	if err != nil {
		t.Fatal(err)
	}
	var f appegress.TabFence
	if err = e.Store.View(context.Background(), func(r store.Reader) error {
		var er error
		f, er = appegress.ReadTabTx(r, id)
		return er
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

func f10cThreadTab(t *testing.T, h *harness, host, tab string, slots int) Worker {
	t.Helper()
	w, err := h.engine.Register(context.Background(), Thread, "contributor", Worker{HostID: host, BrowserTabID: tab, Slots: slots})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// F10b-03/F10b-22: W=3, device limit 2. The first tab fills to the device
// limit, the next tab takes the remainder and stays locked to the app, and a
// second approved app can never use the partly used last tab.
func TestF10cThreadFillToWKeepsPartlyUsedTabLocked(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, app := range []string{"AppAlpha", "AppBeta"} {
		if err := h.engine.ApproveThreadApp(ctx, "developer", app, 3); err != nil {
			t.Fatal(err)
		}
	}
	f10ThreadJob(t, h, "AppAlpha", "alpha", 5)
	t1 := f10cThreadTab(t, h, "host", "tab1", 2)
	t2 := f10cThreadTab(t, h, "host", "tab2", 2)
	t3 := f10cThreadTab(t, h, "host", "tab3", 2)
	for i := 0; i < 2; i++ {
		if _, err := h.engine.Claim(ctx, Thread, "contributor", t1.TabID); err != nil {
			t.Fatalf("tab1 slot %d: %v", i, err)
		}
	}
	if _, err := h.engine.Claim(ctx, Thread, "contributor", t2.TabID); err != nil {
		t.Fatalf("tab2 remainder: %v", err)
	}
	if _, err := h.engine.Claim(ctx, Thread, "contributor", t2.TabID); !errors.Is(err, ErrEmpty) {
		t.Fatalf("W=3 exceeded on the partly used tab: %v", err)
	}
	f10ThreadJob(t, h, "AppBeta", "beta", 2)
	// The partly used last tab stays Alpha's: Beta must not take its free slot.
	if _, err := h.engine.Claim(ctx, Thread, "contributor", t2.TabID); !errors.Is(err, ErrEmpty) {
		t.Fatalf("partly used tab shared with another app: %v", err)
	}
	if f := f10cFence(t, h.engine, "host", "tab2"); f.AppID != "AppAlpha" || len(f.ThreadSlots) != 1 {
		t.Fatalf("tab2 fence: %+v", f)
	}
	// An untouched tab is free for the other approved app.
	b, err := h.engine.Claim(ctx, Thread, "contributor", t3.TabID)
	if err != nil {
		t.Fatalf("fresh tab for Beta: %v", err)
	}
	if f := f10cFence(t, h.engine, "host", "tab3"); f.AppID != "AppBeta" || f.Owner != "developer" {
		t.Fatalf("tab3 fence: %+v", f)
	}
	if b.Lease.WorkerSlotID == "" {
		t.Fatal("Beta lease without a slot")
	}
}

// F10b-02/F10b-19/F10b-23: the fence stays through an idle partly used tab,
// busy heartbeats never start the idle clock, the release happens at exactly
// IdleSince+30 s and the released slot's W quota is freed.
func TestF10cPartialTabLockThroughIdleBoundaryAndRepeatedHeartbeat(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, app := range []string{"AppAlpha", "AppBeta"} {
		if err := h.engine.ApproveThreadApp(ctx, "developer", app, 2); err != nil {
			t.Fatal(err)
		}
	}
	f10ThreadJob(t, h, "AppAlpha", "alpha", 1)
	w := f10cThreadTab(t, h, "host", "tab", 2)
	a, err := h.engine.Claim(ctx, Thread, "contributor", w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	// Busy heartbeats keep the app bound and never start the idle clock.
	for i := 0; i < 3; i++ {
		h.now = h.now.Add(5 * time.Second)
		if _, err = h.engine.Heartbeat(ctx, Thread, "contributor", w.TabID, nil); err != nil {
			t.Fatalf("busy heartbeat %d: %v", i, err)
		}
		if f := f10cFence(t, h.engine, "host", "tab"); f.AppID != "AppAlpha" || !f.IdleSince.IsZero() {
			t.Fatalf("busy tab fence %d: %+v", i, f)
		}
	}
	// The developer ends the job; the first heartbeat that observes the idle tab
	// starts the idle clock at the observation time, not earlier.
	if _, _, err = h.engine.Cancel(ctx, Thread, "developer", a.Lease.JobID); err != nil {
		t.Fatal(err)
	}
	h.now = a.Lease.ExpiresAt.Add(time.Second)
	observed := h.now
	if _, err = h.engine.Heartbeat(ctx, Thread, "contributor", w.TabID, nil); err != nil {
		t.Fatal(err)
	}
	f := f10cFence(t, h.engine, "host", "tab")
	if f.AppID != "AppAlpha" || !f.IdleSince.Equal(observed) {
		t.Fatalf("idle clock (observed %v): %+v", observed, f)
	}
	// Repeated heartbeats inside the window never move IdleSince, never release.
	for _, d := range []time.Duration{5, 10, 20, 29} {
		h.now = observed.Add(d * time.Second)
		if _, err = h.engine.Heartbeat(ctx, Thread, "contributor", w.TabID, nil); err != nil {
			t.Fatal(err)
		}
		f = f10cFence(t, h.engine, "host", "tab")
		if f.AppID != "AppAlpha" || !f.IdleSince.Equal(observed) {
			t.Fatalf("idle +%ds: %+v", d, f)
		}
	}
	f10ThreadJob(t, h, "AppBeta", "beta", 1)
	if _, err = h.engine.Claim(ctx, Thread, "contributor", w.TabID); !errors.Is(err, ErrEmpty) {
		t.Fatalf("idle tab released one second early: %v", err)
	}
	// Exact boundary: IdleSince + 30 s releases the lock and the Alpha slot.
	h.now = observed.Add(30 * time.Second)
	if _, err = h.engine.Claim(ctx, Thread, "contributor", w.TabID); err != nil {
		t.Fatalf("release at exact boundary: %v", err)
	}
	if f = f10cFence(t, h.engine, "host", "tab"); f.AppID != "AppBeta" {
		t.Fatalf("fence after release: %+v", f)
	}
	p, err := h.engine.GetThreadApp(ctx, "developer", "AppAlpha")
	if err != nil {
		t.Fatal(err)
	}
	for id, slot := range p.Active {
		if slot.ExpiresAt.After(h.now) {
			t.Fatalf("Alpha slot %s still bound after release", id)
		}
	}
}

// F10b-23: immediate mode releases at the first idle observation and never while
// a live lease exists on the physical tab.
func TestF10cImmediateModeProtectsActiveLease(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.engine.WriteTabRelease(ctx, Thread, appegress.ReleasePolicy{Mode: appegress.Immediate, IdleSeconds: 30}); err != nil {
		t.Fatal(err)
	}
	for _, app := range []string{"AppAlpha", "AppBeta"} {
		if err := h.engine.ApproveThreadApp(ctx, "developer", app, 2); err != nil {
			t.Fatal(err)
		}
	}
	// G-F12-5: this F10c test exercises heartbeat-lease release, so disable
	// Dew explicitly rather than reassigning before the default 10-minute receipt expires.
	f10ThreadJobWithOptions(t, h, "AppAlpha", "alpha", 1, json.RawMessage(`{"dew":{"enabled":false}}`))
	w := f10cThreadTab(t, h, "host", "tab", 2)
	a, err := h.engine.Claim(ctx, Thread, "contributor", w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	f10ThreadJob(t, h, "AppBeta", "beta", 1)
	h.now = h.now.Add(time.Second)
	if _, err = h.engine.Claim(ctx, Thread, "contributor", w.TabID); !errors.Is(err, ErrEmpty) {
		t.Fatalf("immediate mode released a tab with a live lease: %v", err)
	}
	h.now = a.Lease.ExpiresAt.Add(time.Second)
	if _, err = h.engine.Claim(ctx, Thread, "contributor", w.TabID); err != nil {
		t.Fatalf("immediate mode kept an idle tab: %v", err)
	}
}
