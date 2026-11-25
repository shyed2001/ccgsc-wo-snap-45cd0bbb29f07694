package pool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func seedNeedsT83(t *testing.T, h *harness, needs string, n int) Job {
	t.Helper()
	id := uploadF1(t, h.engine, []byte("gpu-module"))
	q := CreateRequest{ModuleArtifactIDs: []string{id}, Needs: needs}
	for i := 0; i < n; i++ {
		q.Tasks = append(q.Tasks, TaskSpec{TaskID: "gpu-task-" + string(rune('a'+i)), Input: json.RawMessage(`{"n":1}`)})
	}
	j, err := h.engine.Create(context.Background(), Thread, "developer", "", q)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestT83CPUAndGPULeasesShareTab(t *testing.T) {
	h := newHarness(t)
	seedNeedsT83(t, h, "cpu", 1)
	seedNeedsT83(t, h, "gpu", 1)
	w, err := h.engine.Register(context.Background(), Thread, "worker", Worker{HostID: "host", Slots: 1, GPU: true, GPUSlots: 1})
	if err != nil {
		t.Fatal(err)
	}
	// The two jobs are created in the same fixed-clock instant with equal
	// priority and fair share, so their claim order is the store's random
	// job-id order and not FIFO. The invariant under test is that one CPU
	// lease and one GPU lease share the tab, in either order.
	first, err := h.engine.Claim(context.Background(), Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.engine.Claim(context.Background(), Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Lease.GPU == second.Lease.GPU {
		t.Fatalf("leases = %+v / %+v, want one CPU and one GPU lease", first.Lease, second.Lease)
	}
}

func TestT83GPUCapAndCPUOnlyRejection(t *testing.T) {
	h := newHarness(t)
	seedNeedsT83(t, h, "gpu", 3)
	onlyCPU, err := h.engine.Register(context.Background(), Thread, "cpu", Worker{HostID: "cpu-host", Slots: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Claim(context.Background(), Thread, onlyCPU.Owner, onlyCPU.TabID); !errors.Is(err, ErrEmpty) {
		t.Fatalf("CPU-only worker claimed GPU task: %v", err)
	}
	gpu, err := h.engine.Register(context.Background(), Thread, "gpu", Worker{HostID: "gpu-host", Slots: 1, GPU: true, GPUSlots: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Claim(context.Background(), Thread, gpu.Owner, gpu.TabID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Claim(context.Background(), Thread, gpu.Owner, gpu.TabID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Claim(context.Background(), Thread, gpu.Owner, gpu.TabID); !errors.Is(err, ErrEmpty) {
		t.Fatalf("third GPU lease exceeded cap: %v", err)
	}
}

func TestT83GPUConfigAndBatteryGuards(t *testing.T) {
	h := newHarness(t)
	seedNeedsT83(t, h, "gpu-preferred", 1)
	if err := h.engine.SetGPUConfig(GPUConfig{Enabled: false, MaxSlotsPerTab: 2}); err != nil {
		t.Fatal(err)
	}
	w, err := h.engine.Register(context.Background(), Thread, "worker", Worker{HostID: "host", Slots: 1, GPU: true, GPUSlots: 1, DeviceClass: "phone", Charging: false})
	if err != nil {
		t.Fatal(err)
	}
	a, err := h.engine.Claim(context.Background(), Thread, w.Owner, w.TabID)
	if err != nil || a.Lease.GPU {
		t.Fatalf("disabled GPU preferred claim = %+v, err=%v", a.Lease, err)
	}

	h = newHarness(t)
	seedNeedsT83(t, h, "gpu", 1)
	if err := h.engine.SetGPUConfig(GPUConfig{Enabled: true, MaxSlotsPerTab: 2}); err != nil {
		t.Fatal(err)
	}
	w, err = h.engine.Register(context.Background(), Thread, "worker", Worker{HostID: "host", Slots: 1, GPU: true, GPUSlots: 1, DeviceClass: "laptop", Charging: false})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Claim(context.Background(), Thread, w.Owner, w.TabID); !errors.Is(err, ErrEmpty) {
		t.Fatalf("battery GPU claim = %v, want no lease", err)
	}
}
