package pool

import (
	"context"
	"errors"
	"testing"
	"time"

	"ccgsc/poolscore/internal/appegress"
)

func TestF10bPhysicalTabCrossPool(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "approved", true: "legacy"}[legacy], func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			app := "AppAlpha"
			if legacy {
				app = ""
			} else if err := h.engine.ApproveThreadApp(ctx, "developer", app, 2); err != nil {
				t.Fatal(err)
			}
			f10ThreadJob(t, h, app, "thread", 2)
			if err := h.engine.ApproveProcessApp(ctx, "developer", "AppBeta", 1); err != nil {
				t.Fatal(err)
			}
			f9ProcessJob(t, h, "AppBeta", "process", 1)
			a, err := h.engine.Register(ctx, Thread, "contributor", Worker{HostID: "host", BrowserTabID: "physical", Slots: 2})
			if err != nil {
				t.Fatal(err)
			}
			b := f9Worker(t, h, "host", "physical")
			if _, err = h.engine.Claim(ctx, Thread, "contributor", a.TabID); err != nil {
				t.Fatal(err)
			}
			if _, err = h.engine.Claim(ctx, Process, "contributor", b.TabID); !errors.Is(err, ErrEmpty) {
				t.Fatalf("physical tab shared across pools: %v", err)
			}
		})
	}
}

func TestF10bPartlyFilledTabLocked(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, app := range []string{"AppAlpha", "AppBeta"} {
		if err := h.engine.ApproveThreadApp(ctx, "developer", app, 1); err != nil {
			t.Fatal(err)
		}
	}
	f10ThreadJob(t, h, "AppAlpha", "alpha", 1)
	w, err := h.engine.Register(ctx, Thread, "contributor", Worker{HostID: "host", BrowserTabID: "tab", Slots: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.engine.Claim(ctx, Thread, "contributor", w.TabID); err != nil {
		t.Fatal(err)
	}
	f10ThreadJob(t, h, "AppBeta", "beta", 1)
	if _, err = h.engine.Claim(ctx, Thread, "contributor", w.TabID); !errors.Is(err, ErrEmpty) {
		t.Fatalf("partly filled tab shared: %v", err)
	}
}

func TestF10bIdleHeartbeatDoesNotRetainTab(t *testing.T) {
	for _, mode := range []string{appegress.Immediate, appegress.IdleTimeout} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			if _, err := h.engine.WriteTabRelease(ctx, Process, appegress.ReleasePolicy{Mode: mode, IdleSeconds: 30}); err != nil {
				t.Fatal(err)
			}
			if err := h.engine.ApproveProcessApp(ctx, "developer", "AppAlpha", 1); err != nil {
				t.Fatal(err)
			}
			f9ProcessJob(t, h, "AppAlpha", "a", 1)
			w := f9Worker(t, h, "host", "tab")
			a, err := h.engine.Claim(ctx, Process, "contributor", w.TabID)
			if err != nil {
				t.Fatal(err)
			}
			busy, err := h.engine.Heartbeat(ctx, Process, "contributor", w.TabID, nil)
			if err != nil || busy.AppID != "AppAlpha" {
				t.Fatalf("busy tab released: %v", err)
			}
			// G-F12-3: a missed heartbeat does not start idle while the Dew
			// receipt is open. Idle starts only after that deadline.
			h.now = a.Lease.ExpiresAt.Add(time.Second)
			held, err := h.engine.Heartbeat(ctx, Process, "contributor", w.TabID, nil)
			if err != nil || held.AppID != "AppAlpha" {
				t.Fatalf("dew hold released the app at lease expiry: %q %v", held.AppID, err)
			}
			if a.LeaseReceipt == nil {
				t.Fatal("dew default ON did not issue a receipt")
			}
			h.now = time.Unix(a.LeaseReceipt.Deadline, 0).Add(time.Second)
			idle, err := h.engine.Heartbeat(ctx, Process, "contributor", w.TabID, nil)
			if err != nil {
				t.Fatal(err)
			}
			if mode == appegress.IdleTimeout {
				if idle.AppID == "" {
					t.Fatal("released before timeout")
				}
				h.now = h.now.Add(30 * time.Second)
				idle, err = h.engine.Heartbeat(ctx, Process, "contributor", w.TabID, nil)
			}
			if err != nil || idle.AppID != "" {
				t.Fatalf("idle heartbeat retained app: %q %v", idle.AppID, err)
			}
		})
	}
}
