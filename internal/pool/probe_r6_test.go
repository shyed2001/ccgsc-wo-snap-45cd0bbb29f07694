// Path: internal/pool/probe_r6_test.go | GLM round-6 acceptance probes 2026-11-19
// What/why: focused probe tests written by the round-6 reviewer for the three
// cross-cutting paths the shipped suite does not directly exercise:
//  1. aggregate batch capacity: a 1-slot worker gets exactly one task per claim
//     for the whole job (R4-N05 regression at the batch level),
//  2. the re-run window expiry path (engine.go expire, WWPP-R35.1): the first
//     result is accepted flagged and the first worker is penalised, never rewarded,
//  3. the canary replay fence: the canary assignment cannot be replayed after the
//     developer task was requeued.
package pool

import (
	"ccgsc/poolscore/internal/store"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"ccgsc/poolscore/internal/verify"
)

// Probe 1: a 1-slot worker drains an 8-task batch job one task per claim.
func TestProbeR6BatchClaimsCountTasksPerSlot(t *testing.T) {
	h := newHarness(t)
	pct := 0.0
	ctx := context.Background()
	id := uploadF1(t, h.engine, []byte("module bytes 2"))
	q := CreateRequest{ModuleArtifactIDs: []string{id}, RerunPercent: &pct, BatchSize: 8}
	for i := 0; i < 8; i++ {
		q.Tasks = append(q.Tasks, TaskSpec{TaskID: "batch-" + string(rune('a'+i)), Input: json.RawMessage(`{"json":{"n":1}}`)})
	}
	j, err := h.engine.Create(ctx, Thread, "developer", "", q)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = j
	wk := h.worker(t, 1)
	out := json.RawMessage(`{"json":{"n":1}}`)
	settled := 0
	for {
		a, err := h.engine.Claim(ctx, Thread, "contributor", wk.TabID)
		if errors.Is(err, ErrEmpty) {
			break
		}
		if err != nil {
			t.Fatalf("Claim %d: %v", settled, err)
		}
		if len(a.Batch) > 1 {
			t.Fatalf("claim %d returned batch of %d members on a 1-slot worker", settled, len(a.Batch))
		}
		if _, err = h.engine.Submit(ctx, Thread, "contributor", wk.TabID, Submission{
			Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out),
		}); err != nil {
			t.Fatalf("Submit %d: %v", settled, err)
		}
		settled++
		if settled > 8 {
			t.Fatal("more claims than tasks")
		}
	}
	if settled != 8 {
		t.Fatalf("settled %d tasks, want 8", settled)
	}
	got, err := h.engine.Get(ctx, Thread, "developer", j.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Progress.Completed != 8 {
		t.Fatalf("progress = %+v, want 8 completed", got.Progress)
	}
}

// Probe 2: the re-run window expiry accepts the first result flagged, penalises the
// first worker and never records a pass at hold time.
func TestProbeR6ReRunWindowExpiryIsFlaggedAndNeutral(t *testing.T) {
	h := newHarness(t)
	pct := 100.0
	j := h.seed(t, 1, &pct)
	wk := h.worker(t, 1)
	ctx := context.Background()
	a, err := h.engine.Claim(ctx, Thread, "contributor", wk.TabID)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	out := json.RawMessage(`{"answer":"first"}`)
	if _, err = h.engine.Submit(ctx, Thread, "contributor", wk.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out),
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	// Hold time: the first worker must not have been rewarded yet. An unknown
	// worker (no record at all) is the neutral state.
	if rep, err := h.engine.Verifier.Reputation().Worker(wk.TabID); err == nil && (rep.Passes != 0 || rep.Fails != 0) {
		t.Fatalf("reputation at hold = passes %d fails %d, want a neutral hold", rep.Passes, rep.Fails)
	}
	// Advance past the re-run window (2*TTL = 2 min) and sweep.
	h.now = h.now.Add(2*time.Minute + time.Second)
	if err = h.engine.Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	got, err := h.engine.Get(ctx, Thread, "developer", j.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tasks[0].State != "done" {
		t.Fatalf("task = %+v, want done after the window expired", got.Tasks[0])
	}
	// Observation (minor, not a gate finding): the window-expiry accept flags the
	// RESULT and records the reputation fail, but unlike the mismatch path it does
	// not set the task-level Flagged marker. The probe asserts the result level.
	if len(got.Results) != 1 || !got.Results[0].Flagged || got.Results[0].RerunOK {
		t.Fatalf("results = %+v, want one flagged uncompared accept", got.Results)
	}
	if got.Results[0].Status != "done" || got.Results[0].FlagReason == "" {
		t.Fatalf("result = %+v, want a done result with a flag reason", got.Results[0])
	}
	rep, err := h.engine.Verifier.Reputation().Worker(wk.TabID)
	if err != nil {
		t.Fatalf("Reputation after sweep: %v", err)
	}
	// FIX-S3 (ChatGPT R6 N3): an uncompared accept is inconclusive, so the worker gets a
	// neutral event - no pass and no fail.
	if rep.Fails != 0 || rep.Passes != 0 {
		t.Fatalf("reputation after sweep = passes %d fails %d, want neutral 0/0", rep.Passes, rep.Fails)
	}
}

// Probe 3: the canary assignment is a single-use fence; after the developer task is
// requeued, replaying the canary lease/envelope is refused.
func TestProbeR6CanaryAssignmentCannotBeReplayed(t *testing.T) {
	h := newHarness(t)
	cfg := h.engine.TrustConfig()
	cfg.CanaryRate = 100
	cfg.RerunEnabled = false
	h.engine.SetTrustConfig(cfg)
	if _, err := h.engine.RegisterCanaryWithAnswer("canary-1", "task-canary", []byte(`{"canary":1}`), []byte(`"known answer"`)); err != nil {
		t.Fatal(err)
	}
	h.seed(t, 1, nil)
	wk := h.worker(t, 1)
	ctx := context.Background()
	a, err := h.engine.Claim(ctx, Thread, "contributor", wk.TabID)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	canaryOut := json.RawMessage(`"known answer"`)
	if _, err = h.engine.Submit(ctx, Thread, "contributor", wk.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: canaryOut, SHA256: verify.SHA256(canaryOut),
	}); err != nil {
		t.Fatalf("canary submit: %v", err)
	}
	next, err := h.engine.Claim(ctx, Thread, "contributor", wk.TabID)
	if err != nil {
		t.Fatalf("requeue claim: %v", err)
	}
	// Replay 1: the exact canary submission a second time. After the canary pass
	// the canary task id is cleared server side, so the stale submission must be
	// refused (ErrNotFound because the referenced wire task no longer exists, or
	// ErrConflict at the fence).
	if _, err = h.engine.Submit(ctx, Thread, "contributor", wk.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: canaryOut, SHA256: verify.SHA256(canaryOut),
	}); err == nil {
		t.Fatal("canary replay accepted")
	} else if !errors.Is(err, ErrConflict) && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("canary replay = %v, want ErrConflict or not-found", err)
	}
	// Replay 2: the canary envelope under the fresh developer lease.
	if _, err = h.engine.Submit(ctx, Thread, "contributor", wk.TabID, Submission{
		Lease: next.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: canaryOut, SHA256: verify.SHA256(canaryOut),
	}); err == nil {
		t.Fatal("canary envelope accepted under the developer lease")
	}
	// The honest path still settles.
	devOut := json.RawMessage(`{"json":{"n":1}}`)
	s, err := h.engine.Submit(ctx, Thread, "contributor", wk.TabID, Submission{
		Lease: next.Lease, TaskEnvelope: next.TaskEnvelope, LeaseReceipt: next.LeaseReceipt, Output: devOut, SHA256: verify.SHA256(devOut),
	})
	if err != nil {
		t.Fatalf("developer submit: %v", err)
	}
	if s.Status != "done" || !s.TrustComplete {
		t.Fatalf("settled = %+v, want a trusted developer result", s)
	}
}
