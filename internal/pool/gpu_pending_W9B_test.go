// Path: internal/pool/gpu_pending_W9B_test.go | Author: Claude Opus 5.5 | 2026-11-21
// What/why: W9b row 3 (REVIEW_W9_gemini part 2) - a run worker that holds a
// GPU re-run (Pending) lease must not receive a second GPU lease beyond its
// gpuSlots budget: the claim must count t.Lease.GPU even when Pending is set.
package pool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func gpuPendingJobW9B(t *testing.T, h *harness, n int) Job {
	t.Helper()
	id := uploadF1(t, h.engine, []byte("w9b-gpu-module"))
	rerun := 100.0
	q := CreateRequest{ModuleArtifactIDs: []string{id}, Needs: "gpu", RerunPercent: &rerun}
	for i := 0; i < n; i++ {
		q.Tasks = append(q.Tasks, TaskSpec{TaskID: "gpu-task-" + string(rune('a'+i)), Input: json.RawMessage(`{"n":1}`)})
	}
	j, err := h.engine.Create(context.Background(), Thread, "developer", "", q)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestW9BGPUPendingLeaseCountsAgainstGPUSlots(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	cfg := h.engine.TrustConfig()
	cfg.CanaryEnabled = false
	h.engine.SetTrustConfig(cfg)
	gpuPendingJobW9B(t, h, 2)

	first, err := h.engine.Register(ctx, Thread, "contributor", Worker{HostID: "host-1", Slots: 1, GPU: true, GPUSlots: 1})
	if err != nil {
		t.Fatal(err)
	}
	a, err := h.engine.Claim(ctx, Thread, first.Owner, first.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Lease.GPU {
		t.Fatalf("first lease = %+v, want a GPU lease", a.Lease)
	}
	submitF1(t, h.engine, first, a)

	// The re-run of the held task goes to a different tab with a GPU slot.
	second, err := h.engine.Register(ctx, Thread, "contributor-2", Worker{HostID: "host-2", Slots: 1, GPU: true, GPUSlots: 1})
	if err != nil {
		t.Fatal(err)
	}
	rerun, err := h.engine.Claim(ctx, Thread, second.Owner, second.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if rerun.Lease.TaskID != a.Lease.TaskID || !rerun.Lease.GPU {
		t.Fatalf("re-run lease = %+v, want the pending GPU task %q", rerun.Lease, a.Lease.TaskID)
	}

	// The held re-run already uses the only GPU slot of the tab.
	extra, err := h.engine.Claim(ctx, Thread, second.Owner, second.TabID)
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("second GPU lease = %+v, err = %v; want ErrEmpty at gpuSlots", extra.Lease, err)
	}
}
