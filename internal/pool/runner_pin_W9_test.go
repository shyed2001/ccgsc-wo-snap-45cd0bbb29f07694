// Path: internal/pool/runner_pin_W9_test.go | Author: Claude Opus 5.5 | 2026-11-21
// What/why: W9 F1 - a stale-runner refund is granted only when the tab reports a
// really stale runner pin (an old task key id); a current or missing pin is a
// failed try, and one task refunds at most one stale report.
package pool

import (
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
	"context"
	"errors"
	"testing"
	"time"
)

// w9RotateKey retires the live task key the way an admin rotation does, and
// returns the id the page would still trust. W9c rows 1-2: only a really
// retired id proves a stale runner; a made-up id is a lie now.
func w9RotateKey(t *testing.T, h *harness) string {
	t.Helper()
	oldID := h.engine.TaskKeyID
	if err := h.engine.RotateTaskKey(context.Background(), time.Minute); err != nil {
		t.Fatalf("RotateTaskKey: %v", err)
	}
	return oldID
}

func w9RegisterPinned(t *testing.T, h *harness, owner, host, pin string) Worker {
	t.Helper()
	w, err := h.engine.Register(context.Background(), Thread, owner, Worker{HostID: host, Slots: 1, RunnerPin: pin})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	return w
}

func w9Worker(t *testing.T, h *harness, id string) Worker {
	t.Helper()
	var w Worker
	if err := h.engine.Store.View(context.Background(), func(r store.Reader) error {
		return r.Get(store.Workers, id, &w)
	}); err != nil {
		t.Fatalf("read worker %s: %v", id, err)
	}
	return w
}

// TestW9F1RunnerPinWireContract pins the register/heartbeat carrier: a pin is
// stored, an empty heartbeat pin keeps the stored one, and a malformed pin is
// refused.
func TestW9F1RunnerPinWireContract(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.engine.Register(ctx, Thread, "contributor", Worker{HostID: "host", Slots: 1, RunnerPin: "not-a-pin"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Register with a malformed pin err = %v, want ErrInvalid", err)
	}
	w := h.worker(t, 1)
	pin := w9dPin(t, h.engine.TaskKeyID)
	got, err := h.engine.HeartbeatWithPin(ctx, Thread, "contributor", w.TabID, pin, nil)
	if err != nil {
		t.Fatalf("HeartbeatWithPin: %v", err)
	}
	if got.RunnerPin != pin {
		t.Fatalf("heartbeat pin = %q, want %q", got.RunnerPin, pin)
	}
	if stored := w9Worker(t, h, w.TabID); stored.RunnerPin != pin {
		t.Fatalf("stored pin = %q, want %q", stored.RunnerPin, pin)
	}
	got, err = h.engine.Heartbeat(ctx, Thread, "contributor", w.TabID, nil)
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if got.RunnerPin != pin {
		t.Fatalf("pin after a pin-less heartbeat = %q, want %q kept", got.RunnerPin, pin)
	}
	if _, err := h.engine.HeartbeatWithPin(ctx, Thread, "contributor", w.TabID, "zz", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("HeartbeatWithPin malformed err = %v, want ErrInvalid", err)
	}
}

// TestW9F1CurrentPinUnknownKeyIsPenalty is the lie case: the tab claims the
// current server pin yet reports unknown_operator_key, so the try is burned and
// the worker earns a reputation fail event.
func TestW9F1CurrentPinUnknownKeyIsPenalty(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	zero := 0.0
	j := h.seed(t, 1, &zero)
	w := w9RegisterPinned(t, h, "contributor", "host-lie", w9dPin(t, h.engine.TaskKeyID))
	a, err := h.engine.Claim(ctx, Thread, "contributor", w.TabID)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	r, err := h.engine.Submit(ctx, Thread, "contributor", w.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt,
		Error: "verify failed: unknown_operator_key",
	})
	if err != nil {
		t.Fatalf("Submit error report: %v", err)
	}
	if r.Status != "failed" || r.Attempts != 1 {
		t.Fatalf("result = %+v, want a failed try with 1 attempt (no refund)", r)
	}
	job, err := h.engine.Get(ctx, Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if job.Tasks[0].Refunds != 0 {
		t.Fatalf("task refunds = %d, want 0", job.Tasks[0].Refunds)
	}
	rec, err := h.engine.Verifier.Reputation().Worker(w.TabID)
	if err != nil {
		t.Fatalf("reputation record: %v", err)
	}
	if rec.Fails != 1 {
		t.Fatalf("reputation fails = %d, want 1", rec.Fails)
	}
	if stored := w9Worker(t, h, w.TabID); stored.RunnerStale {
		t.Fatal("a tab reporting the current pin must not be marked runner-stale")
	}
}

// TestW9F1MissingPinIsPenalty pins the "or missing" half of the owner rule.
func TestW9F1MissingPinIsPenalty(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	zero := 0.0
	h.seed(t, 1, &zero)
	w := w9RegisterPinned(t, h, "contributor", "host-nopin", "")
	a, err := h.engine.Claim(ctx, Thread, "contributor", w.TabID)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	r, err := h.engine.Submit(ctx, Thread, "contributor", w.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt,
		Error: "verify failed: unknown_operator_key",
	})
	if err != nil {
		t.Fatalf("Submit error report: %v", err)
	}
	if r.Status != "failed" || r.Attempts != 1 {
		t.Fatalf("result = %+v, want a failed try with 1 attempt (no refund)", r)
	}
	rec, err := h.engine.Verifier.Reputation().Worker(w.TabID)
	if err != nil || rec.Fails != 1 {
		t.Fatalf("reputation record = %+v, err = %v, want one fail", rec, err)
	}
}

// TestW9F1OldPinRefundsOnce is the honest stale case: the tab reports an old
// task key id, so the attempt is refunded, the tab is barred from leases until it
// reloads, and the worker reputation is untouched.
func TestW9F1OldPinRefundsOnce(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	zero := 0.0
	j := h.seed(t, 1, &zero)
	w := w9RegisterPinned(t, h, "contributor", "host-old", w9dPin(t, w9RotateKey(t, h)))
	a, err := h.engine.Claim(ctx, Thread, "contributor", w.TabID)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	r, err := h.engine.Submit(ctx, Thread, "contributor", w.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt,
		Error: "verify failed: unknown_operator_key",
	})
	if err != nil {
		t.Fatalf("Submit error report: %v", err)
	}
	if r.Status != "requeued" || r.Attempts != 0 {
		t.Fatalf("result = %+v, want requeued with the attempt refunded", r)
	}
	job, err := h.engine.Get(ctx, Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if job.Tasks[0].Refunds != 1 {
		t.Fatalf("task refunds = %d, want 1", job.Tasks[0].Refunds)
	}
	if _, err := h.engine.Verifier.Reputation().Worker(w.TabID); !errors.Is(err, verify.ErrUnknownWorker) {
		t.Fatalf("reputation record err = %v, want no record (no fail event)", err)
	}
	if stored := w9Worker(t, h, w.TabID); !stored.RunnerStale {
		t.Fatal("honest stale tab must be marked runner-stale")
	}
}

// TestW9F1SecondRefundRefused is the backstop: the second stale report on the
// same task is a normal failed try with a reputation fail event.
func TestW9F1SecondRefundRefused(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	zero := 0.0
	j := h.seed(t, 1, &zero)
	oldPin := w9dPin(t, w9RotateKey(t, h))
	first := w9RegisterPinned(t, h, "contributor", "host-old-1", oldPin)
	a, err := h.engine.Claim(ctx, Thread, "contributor", first.TabID)
	if err != nil {
		t.Fatalf("Claim first: %v", err)
	}
	if _, err = h.engine.Submit(ctx, Thread, "contributor", first.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt,
		Error: "verify failed: unknown_operator_key",
	}); err != nil {
		t.Fatalf("Submit first: %v", err)
	}
	second := w9RegisterPinned(t, h, "contributor", "host-old-2", oldPin)
	b, err := h.engine.Claim(ctx, Thread, "contributor", second.TabID)
	if err != nil {
		t.Fatalf("Claim second: %v", err)
	}
	r, err := h.engine.Submit(ctx, Thread, "contributor", second.TabID, Submission{
		Lease: b.Lease, TaskEnvelope: b.TaskEnvelope, LeaseReceipt: b.LeaseReceipt,
		Error: "verify failed: unknown_operator_key",
	})
	if err != nil {
		t.Fatalf("Submit second: %v", err)
	}
	if r.Status != "failed" || r.Attempts != 1 {
		t.Fatalf("second result = %+v, want a failed try with 1 attempt", r)
	}
	job, err := h.engine.Get(ctx, Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if job.Tasks[0].Refunds != 1 {
		t.Fatalf("task refunds = %d, want the cap at 1", job.Tasks[0].Refunds)
	}
	rec, err := h.engine.Verifier.Reputation().Worker(second.TabID)
	if err != nil || rec.Fails != 1 {
		t.Fatalf("second worker reputation = %+v, err = %v, want one fail", rec, err)
	}
	// The page is genuinely stale, so it still gets no further lease.
	if stored := w9Worker(t, h, second.TabID); !stored.RunnerStale {
		t.Fatal("the second really-stale tab must be marked runner-stale without a refund")
	}
}
