// Path: internal/pool/engine_events_T108_test.go | Author: GLM for Claude Opus 5.5 | 2026-11-21
// What/why: T108 lifecycle events: the engine emits job.queued on create, one
// task.leased per leased wire task on claim, and keeps the queued -> leased ->
// done order in the durable event sequence. The idempotent replay never
// re-emits the queued event.
package pool

import (
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
	"context"
	"encoding/json"
	"testing"
)

// t108Events reads the durable events in sequence order.
func t108Events(t *testing.T, h *harness) []Event {
	t.Helper()
	var rows []store.Record
	if err := h.engine.Store.View(context.Background(), func(r store.Reader) error {
		var e error
		rows, e = r.List(store.Events)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	out := make([]Event, 0, len(rows))
	for _, row := range rows {
		var ev Event
		if json.Unmarshal(row.Value, &ev) != nil {
			t.Fatalf("event %s did not parse", row.Key)
		}
		out = append(out, ev)
	}
	return out
}

func TestT108JobLifecycleEventsQueuedLeasedDone(t *testing.T) {
	h := newHarness(t)
	// RerunPercent 0 keeps the sampling deterministic: the default 1 percent
	// would occasionally sample the seeded task and emit task.rerun first.
	zero := 0.0
	j := h.seed(t, 2, &zero)

	// Create emitted exactly one queued event for the job.
	events := t108Events(t, h)
	if len(events) != 1 || events[0].Type != "job.queued" || events[0].JobID != j.JobID {
		t.Fatalf("after create = %+v, want one job.queued event for %s", events, j.JobID)
	}

	wk := h.worker(t, 1)
	a, err := h.engine.Claim(context.Background(), Thread, "contributor", wk.TabID)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	events = t108Events(t, h)
	if len(events) != 2 || events[1].Type != "task.leased" || events[1].TaskID != a.Lease.TaskID {
		t.Fatalf("after claim = %+v, want one task.leased event for wire task %s", events, a.Lease.TaskID)
	}

	out := json.RawMessage(`{"answer":1}`)
	if _, err := h.engine.Submit(context.Background(), Thread, "contributor", wk.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out),
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	events = t108Events(t, h)
	// The second task is still queued, so the job is not terminal and no
	// job.done event exists yet: queued, leased, done.
	if len(events) != 3 || events[2].Type != "task.done" || events[2].TaskID != a.Lease.TaskID {
		t.Fatalf("after submit = %+v, want task.done after queued and leased", events)
	}
	if !(events[0].ID < events[1].ID && events[1].ID < events[2].ID) {
		t.Fatalf("event ids out of order: %s %s %s", events[0].ID, events[1].ID, events[2].ID)
	}
}

func TestT108QueuedEventEmittedOncePerJob(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	data := []byte("module bytes")
	id := verify.SHA256(data)
	if _, err := h.engine.Upload(ctx, id, "", 0, int64(len(data)), data); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	q := CreateRequest{
		ModuleArtifactIDs: []string{id},
		Tasks:             []TaskSpec{{TaskID: "t1", Input: json.RawMessage(`{"json":{"n":1}}`)}},
	}
	first, err := h.engine.Create(ctx, Thread, "developer", "idem-t108", q)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	second, err := h.engine.Create(ctx, Thread, "developer", "idem-t108", q)
	if err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	if second.JobID != first.JobID {
		t.Fatal("idempotent replay returned a different job")
	}
	events := t108Events(t, h)
	if len(events) != 1 || events[0].Type != "job.queued" {
		t.Fatalf("idempotent replay emitted extra events: %+v", events)
	}
}

func TestT108BatchClaimEmitsLeasedPerMember(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	data := []byte("module bytes")
	id := verify.SHA256(data)
	if _, err := h.engine.Upload(ctx, id, "", 0, int64(len(data)), data); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	batch := 3
	q := CreateRequest{
		ModuleArtifactIDs: []string{id},
		Tasks: []TaskSpec{
			{TaskID: "t1", Input: json.RawMessage(`{"json":{"n":1}}`)},
			{TaskID: "t2", Input: json.RawMessage(`{"json":{"n":1}}`)},
			{TaskID: "t3", Input: json.RawMessage(`{"json":{"n":1}}`)},
		},
		BatchSize: batch,
	}
	if _, err := h.engine.Create(ctx, Thread, "developer", "", q); err != nil {
		t.Fatalf("Create: %v", err)
	}
	wk := h.worker(t, 4)
	a, err := h.engine.Claim(ctx, Thread, "contributor", wk.TabID)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(a.Batch) != 3 {
		t.Fatalf("batch = %d members, want 3", len(a.Batch))
	}
	events := t108Events(t, h)
	if len(events) != 1+3 {
		t.Fatalf("events = %+v, want one queued plus three leased", events)
	}
	for i, want := range []string{"job.queued", "task.leased", "task.leased", "task.leased"} {
		if events[i].Type != want {
			t.Fatalf("event %d = %s, want %s", i, events[i].Type, want)
		}
	}
	seen := map[string]bool{}
	for _, m := range a.Batch {
		seen[m.Lease.TaskID] = true
	}
	for _, ev := range events[1:] {
		if !seen[ev.TaskID] {
			t.Fatalf("leased event task %s is not a batch member", ev.TaskID)
		}
	}
}
