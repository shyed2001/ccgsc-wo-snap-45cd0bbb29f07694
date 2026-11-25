// Path: internal/caps/mem_budget_R15_test.go | Author: Hermes (MiniMax-M3) | 2026-11-22
// What/why: R15-FIX-CAPS (a) - the memory budget of the process. The budget is the
// minimum of the cgroup budget and the physical RAM: a container that may only use
// 1 GB must not plan for the 64 GB of the machine it runs on.
//
// The tests drive the budget with fixtures, so they need no real cgroup.
package caps

import (
	"testing"
)

// TestMemoryBudgetUsesTheSmallerOfCgroupAndPhysical pins the core rule of the
// budget: the smaller of the cgroup budget and the physical RAM.
func TestMemoryBudgetUsesTheSmallerOfCgroupAndPhysical(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cgroup   int64
		physical int64
		want     int64
	}{
		{"the cgroup is the smaller one", 1024, 65536, 1024},
		{"the physical RAM is the smaller one", 131072, 2048, 2048},
		{"they are equal", 4096, 4096, 4096},
		{"no cgroup budget falls back to the physical RAM", 0, 8192, 8192},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := MemoryBudget{CgroupMB: tc.cgroup, PhysicalMB: tc.physical}
			if got := b.AvailableMB(); got != tc.want {
				t.Fatalf("AvailableMB = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestMemoryBudgetIsOffWithoutAnyMemory pins that a machine that reports neither a
// cgroup budget nor its physical RAM runs without an automatic limit instead of a
// fabricated one.
func TestMemoryBudgetIsOffWithoutAnyMemory(t *testing.T) {
	b := MemoryBudget{}
	if got := b.AvailableMB(); got != 0 {
		t.Fatalf("AvailableMB = %d, want 0 (off)", got)
	}
}

// TestResolveMemoryLimitUsesTheBudget pins that the automatic memory limit is the
// 75 % of the available memory of the budget, and never more than the budget.
func TestResolveMemoryLimitUsesTheBudget(t *testing.T) {
	// The budget of the process: 1 GB of cgroup on a machine with 64 GB of RAM.
	budget := MemoryBudget{CgroupMB: 1024, PhysicalMB: 65536}
	got, err := resolveWithBudget(Config{}, budget)
	if err != nil {
		t.Fatalf("resolveWithBudget: %v", err)
	}
	want := int(budget.AvailableMB() * 3 / 4)
	if want < 1 {
		want = 1
	}
	if got.MemLimitMB != want {
		t.Fatalf("MemLimitMB = %d, want %d (75 %% of the %d MB budget)", got.MemLimitMB, want, budget.AvailableMB())
	}
	// The limit never exceeds the budget itself.
	if int64(got.MemLimitMB) > budget.AvailableMB() {
		t.Fatalf("MemLimitMB = %d exceeds the budget of %d MB", got.MemLimitMB, budget.AvailableMB())
	}
}

// TestResolveMemoryLimitNeverExceedsTheCgroupBudget pins the container rule: the
// automatic limit of a process in a 512 MB cgroup stays under the cgroup budget.
func TestResolveMemoryLimitNeverExceedsTheCgroupBudget(t *testing.T) {
	budget := MemoryBudget{CgroupMB: 512, PhysicalMB: 65536}
	got, err := resolveWithBudget(Config{}, budget)
	if err != nil {
		t.Fatalf("resolveWithBudget: %v", err)
	}
	if int64(got.MemLimitMB) > 512 {
		t.Fatalf("MemLimitMB = %d exceeds the cgroup budget of 512 MB", got.MemLimitMB)
	}
	if got.MemLimitMB != 384 {
		t.Fatalf("MemLimitMB = %d, want 384 (75 %% of 512 MB)", got.MemLimitMB)
	}
}

// TestResolveKeepsAnExplicitLimitAboveTheBudget pins that an explicit limit is the
// operator's decision: the package never lowers it silently. The operator who asks
// for more than the cgroup allows owns the outcome (the cgroup kills the process).
func TestResolveKeepsAnExplicitLimitAboveTheBudget(t *testing.T) {
	budget := MemoryBudget{CgroupMB: 512, PhysicalMB: 65536}
	got, err := resolveWithBudget(Config{MemLimitMB: 4096}, budget)
	if err != nil {
		t.Fatalf("resolveWithBudget: %v", err)
	}
	if got.MemLimitMB != 4096 {
		t.Fatalf("MemLimitMB = %d, want the explicit 4096", got.MemLimitMB)
	}
}

// TestResolveMemoryLimitOffWithoutABudget pins that a machine without any detectable
// memory budget leaves the automatic limit off.
func TestResolveMemoryLimitOffWithoutABudget(t *testing.T) {
	got, err := resolveWithBudget(Config{}, MemoryBudget{})
	if err != nil {
		t.Fatalf("resolveWithBudget: %v", err)
	}
	if got.MemLimitMB != 0 {
		t.Fatalf("MemLimitMB = %d, want 0 (off)", got.MemLimitMB)
	}
	if got.MemLimitBytes != 0 {
		t.Fatalf("MemLimitBytes = %d, want 0 (off)", got.MemLimitBytes)
	}
}

// TestMemoryBudgetCarriesTheSource pins that the budget knows where it came from, so
// the admin view can tell the operator why the limit is what it is.
func TestMemoryBudgetCarriesTheSource(t *testing.T) {
	b := MemoryBudget{CgroupMB: 1024, PhysicalMB: 65536, CgroupSource: "cgroup-v2"}
	if b.CgroupSource != "cgroup-v2" {
		t.Fatalf("CgroupSource = %q, want cgroup-v2", b.CgroupSource)
	}
}
