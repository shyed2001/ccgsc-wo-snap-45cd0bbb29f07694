// Path: internal/pool/runner_pin_W9C_test.go | Author: Claude Opus 5.5 | 2026-11-21
// What/why: W9c row 1 (REVIEW_W9_deepseek, BLOCKER+MAJOR) - a stale-runner refund
// is granted only for a task key id the server really retired on an admin
// rotation. A random or unknown key id is a failed try with a reputation fail.
package pool

import (
	"ccgsc/poolscore/internal/verify"
	"context"
	"errors"
	"testing"
	"time"
)

// TestW9CRandomPinIsPenalty proves the DeepSeek BLOCKER: an arbitrary lower-hex
// key id that the server never issued must not buy a refund.
func TestW9CRandomPinIsPenalty(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	zero := 0.0
	j := h.seed(t, 1, &zero)
	randomKeyID := "0123456789abcdef"
	if randomKeyID == h.engine.TaskKeyID {
		randomKeyID = "fedcba9876543210"
	}
	w := w9RegisterPinned(t, h, "contributor", "host-random", w9dPin(t, randomKeyID))
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
		t.Fatalf("result = %+v, want a failed try with 1 attempt (an unknown key id must not refund)", r)
	}
	job, err := h.engine.Get(ctx, Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if job.Tasks[0].Refunds != 0 {
		t.Fatalf("task refunds = %d, want 0 for an unknown key id", job.Tasks[0].Refunds)
	}
	rec, err := h.engine.Verifier.Reputation().Worker(w.TabID)
	if err != nil || rec.Fails != 1 {
		t.Fatalf("reputation record = %+v, err = %v, want one fail", rec, err)
	}
	// W9c ruling: a pin that is not the live key still marks the tab stale, so
	// the lying page gets no further lease either.
	if stored := w9Worker(t, h, w.TabID); !stored.RunnerStale {
		t.Fatal("a tab reporting an unknown key id must be marked runner-stale")
	}
}

// TestW9CRetiredPinRefundsOnce is the honest case under the new rule: the tab
// pins the key id that was current before an admin rotation, so the try is
// refunded and the tab is barred from leases until it reloads.
func TestW9CRetiredPinRefundsOnce(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	zero := 0.0
	j := h.seed(t, 1, &zero)
	oldID := h.engine.TaskKeyID
	if err := h.engine.RotateTaskKey(ctx, time.Minute); err != nil {
		t.Fatalf("RotateTaskKey: %v", err)
	}
	w := w9RegisterPinned(t, h, "contributor", "host-retired", w9dPin(t, oldID))
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
		t.Fatalf("result = %+v, want requeued with the attempt refunded for a retired key", r)
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
		t.Fatal("honest retired-key tab must be marked runner-stale")
	}
}

// TestW9CRetiredKeySetPersistsAcrossRestart pins the persistence half of row 1:
// the set of ids that were current before a rotation is durable, so the refund
// rule is the same after a server restart.
func TestW9CRetiredKeySetPersistsAcrossRestart(t *testing.T) {
	h := newHarness(t)
	first := w9RotateKey(t, h)
	second := w9RotateKey(t, h)
	current := h.engine.TaskKeyID
	restarted, err := New(h.engine.Store, h.engine.Log, "s1-restart", time.Minute)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !restarted.taskKeyRetired(first) || !restarted.taskKeyRetired(second) {
		t.Fatal("the retired task key ids did not survive the restart")
	}
	if restarted.taskKeyRetired(current) {
		t.Fatal("the current task key id must not be in the retired set")
	}
	if restarted.taskKeyRetired("0123456789abcdef") {
		t.Fatal("an id the server never issued must not be in the retired set")
	}
}
