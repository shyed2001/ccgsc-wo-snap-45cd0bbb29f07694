// Path: internal/pool/lease_fence_A2_R17_test.go | Author: CommandCode for the owner | 2026-11-22
// What/why: WO R17-SCALE item 3 (A2) - the Claim no longer takes FOR UPDATE on every job
// row, so the fence of a settlement is the per-row version check plus the Redis lease
// fencing. The test pins both halves on the engine: a submission of a stale generation is
// refused, a submission of a reissued token is refused, and the Redis lease key of the
// settled task is released exactly when the settlement commits.
package pool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"ccgsc/poolscore/internal/store/redislease"
	"ccgsc/poolscore/internal/verify"
)

// TestA2StaleGenerationOrTokenIsRefused pins the version guard the store no longer
// provides with a row lock: a submission whose lease generation or token moved is refused
// before anything settles.
func TestA2StaleGenerationOrTokenIsRefused(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	j := h.seed(t, 1, nil)
	ctx := context.Background()
	w := h.worker(t, 1)
	a, err := h.engine.Claim(ctx, Thread, "contributor", w.TabID)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	out := json.RawMessage(`{"answer":1}`)
	sha := verify.SHA256(out)

	// A forged generation is refused.
	forged := a.Lease
	forged.Generation++
	if _, err := h.engine.Submit(ctx, Thread, "contributor", w.TabID, Submission{
		Lease: forged, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: sha,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("forged generation = %v, want ErrConflict", err)
	}
	// A forged token is refused.
	forged = a.Lease
	forged.Token = "not-the-issued-token"
	if _, err := h.engine.Submit(ctx, Thread, "contributor", w.TabID, Submission{
		Lease: forged, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: sha,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("forged token = %v, want ErrConflict", err)
	}
	// The genuine lease still settles, so the refusals above did not consume it.
	res, err := h.engine.Submit(ctx, Thread, "contributor", w.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: sha,
	})
	if err != nil {
		t.Fatalf("genuine Submit: %v", err)
	}
	if res.Status != "done" {
		t.Fatalf("genuine result = %+v, want done", res)
	}
	stored, err := h.engine.Get(ctx, Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(stored.Results) != 1 {
		t.Fatalf("results = %d, want exactly one settlement", len(stored.Results))
	}
}

// TestA2RedisFenceIsReleasedOnSettlement pins the other half of the fence: with a Redis
// lease client the claim holds the key, and the settlement releases exactly that key.
func TestA2RedisFenceIsReleasedOnSettlement(t *testing.T) {
	h, _, addr := r10RedisEngine(t)
	client, err := redislease.Dial(addr)
	if err != nil {
		t.Fatalf("dial the fake lease store: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	j := h.seed(t, 1, nil)
	w := h.worker(t, 1)
	a, err := h.engine.Claim(ctx, Thread, "contributor", w.TabID)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	key := "lease:" + j.JobID + ":" + a.Lease.TaskID
	if current, err := client.Get(key); err != nil || current != a.Lease.Token {
		t.Fatalf("lease key after claim = %q (%v), want the issued token", current, err)
	}
	out := json.RawMessage(`{"answer":1}`)
	if _, err := h.engine.Submit(ctx, Thread, "contributor", w.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out),
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if current, err := client.Get(key); err != nil || current != "" {
		t.Fatalf("lease key after settlement = %q (%v), want it released", current, err)
	}
}
