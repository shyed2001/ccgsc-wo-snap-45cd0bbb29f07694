// Path: internal/pool/attempts_r10_test.go | R10 fix lane (web review of fe646643).
// What/why: the glm MINOR row against the fail cap. Engine.fail treated
// t.Attempts >= 3 as exhausted as well as t.Attempts >= j.MaxAttempts, so a job
// created with maxAttempts 4..10 was failed after three tries: the higher ceiling
// the API accepts (1..10) was dead configuration.
package pool

import (
	"context"
	"encoding/json"
	"testing"

	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
)

// TestR10FailHonoursMaxAttemptsAboveThree is the glm MINOR row.
func TestR10FailHonoursMaxAttemptsAboveThree(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	zero := 0.0
	data := []byte("module bytes")
	id := verify.SHA256(data)
	if _, err := h.engine.Upload(ctx, id, "", 0, int64(len(data)), data); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	j, err := h.engine.Create(ctx, Thread, "developer", "", CreateRequest{
		ModuleArtifactIDs: []string{id},
		Tasks:             []TaskSpec{{TaskID: "task-a", Input: json.RawMessage(`{"json":{"n":1}}`)}},
		MaxAttempts:       5,
		RerunPercent:      &zero,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Fail the task three times, then assert it is still retryable. With
	// maxAttempts 5 the third failure must leave the task queued; only running out
	// of the configured attempts ends it.
	for i := 1; i <= 3; i++ {
		w := h.workerAs(t, "contributor", 2)
		a, err := h.engine.Claim(ctx, Thread, "contributor", w.TabID)
		if err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		if _, err = h.engine.Submit(ctx, Thread, "contributor", w.TabID, Submission{
			Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Error: "worker failed",
		}); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	got, err := h.engine.Get(ctx, Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(got.Tasks))
	}
	if got.Tasks[0].State != "queued" {
		t.Fatalf("after three failures of five the task is %q, want queued (maxAttempts 5 must not cap at 3)", got.Tasks[0].State)
	}
	var stored Job
	if err := h.engine.Store.View(ctx, func(r store.Reader) error { return r.Get(store.Jobs, j.JobID, &stored) }); err != nil {
		t.Fatalf("read job: %v", err)
	}
	if stored.MaxAttempts != 5 {
		t.Fatalf("stored maxAttempts = %d, want 5", stored.MaxAttempts)
	}
}
