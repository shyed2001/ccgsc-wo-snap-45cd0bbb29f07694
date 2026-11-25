// Path: internal/pool/runner_stale_T114_test.go | Author: Claude Opus 5.5 | 2026-11-21
// What/why: T114 - a tab whose runner reports unknown_operator_key does not burn
// the task attempt and gets no further lease until it registers again.
package pool

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestT114RunnerStaleRefundsAttemptAndBlocksLeases(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	zero := 0.0
	j := h.seed(t, 1, &zero)
	// W9 F1 + W9c row 1: a refund is granted only for a pin the server really
	// retired on an admin rotation, so rotate the key and register the tab with
	// the id that was current before it.
	oldKeyID := h.engine.TaskKeyID
	if err := h.engine.RotateTaskKey(ctx, time.Minute); err != nil {
		t.Fatalf("RotateTaskKey: %v", err)
	}
	stale, err := h.engine.Register(ctx, Thread, "contributor", Worker{HostID: "host", Slots: 1, RunnerPin: w9dPin(t, oldKeyID)})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	a, err := h.engine.Claim(ctx, Thread, "contributor", stale.TabID)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	r, err := h.engine.Submit(ctx, Thread, "contributor", stale.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Error: "verify failed: unknown_operator_key",
	})
	if err != nil {
		t.Fatalf("Submit error report: %v", err)
	}
	if r.Status != "requeued" || r.Attempts != 0 {
		t.Fatalf("result = %+v, want requeued with the attempt refunded", r)
	}
	if _, err := h.engine.Claim(ctx, Thread, "contributor", stale.TabID); !errors.Is(err, ErrRunnerStale) {
		t.Fatalf("stale tab claim err = %v, want ErrRunnerStale", err)
	}
	// W9c row 2: a pin-less re-register of the same host no longer clears the
	// stale mark, so the second tab is a genuinely fresh host. Its claim proves
	// the requeued task is offered to a working tab.
	fresh, err := h.engine.Register(ctx, Thread, "contributor", Worker{HostID: "host-fresh", Slots: 1})
	if err != nil {
		t.Fatalf("Register fresh tab: %v", err)
	}
	b, err := h.engine.Claim(ctx, Thread, "contributor", fresh.TabID)
	if err != nil {
		t.Fatalf("fresh tab claim: %v", err)
	}
	if b.Lease.JobID != j.JobID {
		t.Fatalf("fresh tab leased %s, want %s", b.Lease.JobID, j.JobID)
	}
}

func TestT114StaleTabDoesNotHoldLiveSlot(t *testing.T) {
	h := newHarness(t)
	now := h.engine.Now()
	w := Worker{Anonymous: true, Heartbeat: now}
	if !h.engine.liveAnonymousTab(w, now) {
		t.Fatal("fresh anonymous tab must count as live")
	}
	w.RunnerStale = true
	if h.engine.liveAnonymousTab(w, now) {
		t.Fatal("stale-runner tab must not hold a live slot")
	}
}

func TestT115AnonymousSeedAboveBar(t *testing.T) {
	if got := anonymousSeedScore(0.5); got <= 0.5 {
		t.Fatalf("seed = %v, want above the bar line", got)
	}
}
