package pool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"ccgsc/poolscore/internal/verify"
)

func f10ThreadJob(t *testing.T, h *harness, app, key string, tasks int) Job {
	return f10ThreadJobWithOptions(t, h, app, key, tasks, nil)
}

func f10ThreadJobWithOptions(t *testing.T, h *harness, app, key string, tasks int, options json.RawMessage) Job {
	t.Helper()
	b := []byte("F10 thread job module")
	digest := verify.SHA256(b)
	if _, err := h.engine.Upload(context.Background(), digest, "", 0, int64(len(b)), b); err != nil {
		t.Fatal(err)
	}
	q := CreateRequest{AppID: app, ModuleArtifactIDs: []string{digest}, Options: options}
	for i := 0; i < tasks; i++ {
		q.Tasks = append(q.Tasks, TaskSpec{TaskID: "thread-" + string(rune('a'+i)), Input: json.RawMessage(`{"json":{"n":1}}`)})
	}
	j, err := h.engine.Create(context.Background(), Thread, "developer", key, q)
	if err != nil {
		t.Fatalf("thread Create: %v", err)
	}
	return j
}

func TestF10ApprovedThreadAppClaimsDistinctWebWorkerSlots(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.engine.ApproveThreadApp(ctx, "developer", "AppAlpha", 2); err != nil {
		t.Fatal(err)
	}
	job := f10ThreadJob(t, h, "AppAlpha", "f10-1", 3)
	if job.AppID != "AppAlpha" {
		t.Fatalf("created appId lost: %+v", job)
	}
	w1, err := h.engine.Register(ctx, Thread, "contributor", Worker{HostID: "host-one", BrowserTabID: "tab-one", Slots: 2})
	if err != nil {
		t.Fatal(err)
	}
	w2, err := h.engine.Register(ctx, Thread, "contributor", Worker{HostID: "host-two", BrowserTabID: "tab-two", Slots: 2})
	if err != nil {
		t.Fatal(err)
	}
	a, err := h.engine.Claim(ctx, Thread, "contributor", w1.TabID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.engine.Claim(ctx, Thread, "contributor", w1.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Lease.WorkerSlotID == "" || b.Lease.WorkerSlotID == "" || a.Lease.WorkerSlotID == b.Lease.WorkerSlotID {
		t.Fatalf("expected two distinct signed whole Web Worker slots, got %q / %q", a.Lease.WorkerSlotID, b.Lease.WorkerSlotID)
	}
	if _, err = h.engine.Claim(ctx, Thread, "contributor", w2.TabID); !errors.Is(err, ErrEmpty) {
		t.Fatalf("W=2 admitted third worker slot from another tab: %v", err)
	}
	// The tab that has two active app tasks has no vacant slot for a third task.
	if _, err = h.engine.Claim(ctx, Thread, "contributor", w1.TabID); !errors.Is(err, ErrEmpty) {
		t.Fatalf("tab acquired >2 slots: %v", err)
	}
}

func TestF10UnapprovedThreadAppRejectsAndLegacyStillRuns(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	data := []byte("F10 unapproved")
	id := verify.SHA256(data)
	if _, err := h.engine.Upload(ctx, id, "", 0, int64(len(data)), data); err != nil {
		t.Fatal(err)
	}
	q := CreateRequest{AppID: "AppAlpha", ModuleArtifactIDs: []string{id}, Tasks: []TaskSpec{{TaskID: "one", Input: json.RawMessage(`{"json":{"n":1}}`)}}}
	if _, err := h.engine.Create(ctx, Thread, "developer", "", q); !errors.Is(err, ErrForbidden) {
		t.Errorf("unapproved thread app created: %v", err)
	}
	if err := h.engine.ApproveProcessApp(ctx, "developer", "AppAlpha", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Create(ctx, Thread, "developer", "", q); !errors.Is(err, ErrForbidden) {
		t.Errorf("Process approval escalated to Thread: %v", err)
	}
	job := h.seed(t, 1, nil) // existing legacy thread job remains unchanged
	w := h.worker(t, 1)
	a, err := h.engine.Claim(ctx, Thread, "contributor", w.TabID)
	if err != nil {
		t.Fatalf("legacy claim broken: %v", err)
	}
	if a.Lease.JobID != job.JobID || a.Lease.WorkerSlotID != "" {
		t.Errorf("legacy lease unexpectedly app-bound: %+v", a.Lease)
	}
}

func TestF10ThreadQuotaIndependentOfProcessPool(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.engine.ApproveThreadApp(ctx, "developer", "AppAlpha", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.GetProcessApp(ctx, "developer", "AppAlpha"); err == nil {
		t.Fatal("Thread-only approval appeared in Process registry")
	}
	if err := h.engine.ApproveProcessApp(ctx, "developer", "AppAlpha", 3); err != nil {
		t.Fatal(err)
	}
	thread, err := h.engine.GetThreadApp(ctx, "developer", "AppAlpha")
	if err != nil {
		t.Fatal(err)
	}
	process, err := h.engine.GetProcessApp(ctx, "developer", "AppAlpha")
	if err != nil {
		t.Fatal(err)
	}
	if thread.MaxWorkers != 1 || process.MaxTabs != 3 {
		t.Fatalf("coupled Thread/Process policies: thread=%+v process=%+v", thread, process)
	}
}
