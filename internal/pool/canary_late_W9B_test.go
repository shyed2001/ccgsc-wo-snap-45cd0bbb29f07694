// Path: internal/pool/canary_late_W9B_test.go | Author: Claude Opus 5.5 | 2026-11-21
// What/why: W9b row 1 (REVIEW_W9_gemini part 2) - a late submission of a canary
// lease must not read task.Lease: the accepted late path authenticates the lease
// through LateLeaseEvidence and task.Lease may be nil.
package pool

import (
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// TestW9BEngineLateCanaryNilLease seeds the durable state left by a build whose
// lease expiry did not clear the canary binding (Lease nil, canary bound,
// expired lease evidence kept) and submits the late canary result. The accepted
// late path must record the authenticated lease `l`, not dereference task.Lease.
func TestW9BEngineLateCanaryNilLease(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	cfg := h.engine.TrustConfig()
	cfg.CanaryRate = 100
	cfg.RerunEnabled = false
	h.engine.SetTrustConfig(cfg)
	if _, err := h.engine.RegisterCanaryWithAnswer("canary-1", "task-canary", []byte(`{"canary":1}`), []byte(`"known answer"`)); err != nil {
		t.Fatal(err)
	}
	j := h.seed(t, 1, nil)
	wk := h.worker(t, 1)
	a, err := h.engine.Claim(ctx, Thread, "contributor", wk.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Lease.TaskID == "task-a" {
		t.Fatalf("probe needs a canary assignment, got %+v", a.Lease)
	}
	h.now = a.Lease.ExpiresAt.Add(time.Second)

	// The stored record of the old build: the lease was cleared and the expired
	// evidence written, but the canary binding survived the requeue.
	if err := h.engine.Store.Update(ctx, func(tx store.Tx) error {
		var job Job
		if er := tx.Get(store.Jobs, j.JobID, &job); er != nil {
			return er
		}
		task := &job.Tasks[0]
		if task.CanaryID == "" || task.EnvelopeDigest == "" {
			return ErrInvalid
		}
		task.Lease = nil
		task.State = "queued"
		task.ExpiredLease = &LateLeaseEvidence{Lease: a.Lease, ContributorKeyID: "contributor", EnvelopeDigest: task.EnvelopeDigest, ExpiredAt: h.now.UTC()}
		return tx.Put(store.Jobs, j.JobID, job)
	}); err != nil {
		t.Fatal(err)
	}

	output := json.RawMessage(`"known answer"`)
	got, err := h.engine.Submit(ctx, Thread, "contributor", wk.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: output, SHA256: verify.SHA256(output),
	})
	if err != nil {
		t.Fatalf("late canary submit: %v", err)
	}
	if got.Status != "canary" || !got.CanaryOK {
		t.Fatalf("result = %+v, want a late canary pass", got)
	}
	stored, err := h.engine.Get(ctx, Thread, "developer", j.JobID)
	if err != nil {
		t.Fatal(err)
	}
	task := stored.Tasks[0]
	if task.ExpiredLease == nil || !task.ExpiredLease.Consumed {
		t.Fatalf("expired lease evidence = %+v, want the late submission consumed it", task.ExpiredLease)
	}
	if task.State != "queued" || task.CanaryID != "" || task.Lease != nil {
		t.Fatalf("task after the late canary = %+v, want the developer task requeued", task)
	}
	if task.CanaryLeaseToken != a.Lease.Token || task.CanaryGeneration != a.Lease.Generation {
		t.Fatalf("canary evidence = token %q / gen %d, want the authenticated late lease %q / %d", task.CanaryLeaseToken, task.CanaryGeneration, a.Lease.Token, a.Lease.Generation)
	}
}

// TestW9BEngineLateCanaryAfterExpiryIsNotFound is the reachability check of the
// in-tree API: expire() clears the canary binding with the lease, so the late
// submission can no longer be matched to the task. No panic, no result.
func TestW9BEngineLateCanaryAfterExpiryIsNotFound(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	cfg := h.engine.TrustConfig()
	cfg.CanaryRate = 100
	cfg.RerunEnabled = false
	h.engine.SetTrustConfig(cfg)
	if _, err := h.engine.RegisterCanaryWithAnswer("canary-1", "task-canary", []byte(`{"canary":1}`), []byte(`"known answer"`)); err != nil {
		t.Fatal(err)
	}
	h.seed(t, 1, nil)
	wk := h.worker(t, 1)
	a, err := h.engine.Claim(ctx, Thread, "contributor", wk.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Lease.TaskID == "task-a" {
		t.Fatalf("probe needs a canary assignment, got %+v", a.Lease)
	}
	// G-F12-5: the canary binding stays until the Dew receipt expires, not at
	// the heartbeat lease. Sweep past that deadline, with a fresh worker
	// heartbeat so the refusal is "not found" rather than a stale tab.
	if a.LeaseReceipt == nil {
		t.Fatal("dew default ON did not issue a receipt")
	}
	h.now = time.Unix(a.LeaseReceipt.Deadline, 0).Add(time.Second)
	if _, err := h.engine.Heartbeat(ctx, Thread, "contributor", wk.TabID, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	output := json.RawMessage(`"known answer"`)
	_, err = h.engine.Submit(ctx, Thread, "contributor", wk.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: output, SHA256: verify.SHA256(output),
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("late canary after expiry = %v, want store.ErrNotFound", err)
	}
}
