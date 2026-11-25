// Path: internal/caps/mem_cgroup_other.go | Author: Hermes (MiniMax-M3) | 2026-11-22
// What/why: R15-FIX-CAPS (a) - the cgroup hierarchy is a Linux concept, so every
// other platform has no cgroup budget: the budget of the process is its physical
// RAM alone.
//go:build !linux

package caps

// cgroupBudgetMB reads the cgroup budget of the process in megabytes. The second
// value names the source, the third value is false when there is no budget.
func cgroupBudgetMB() (int64, string, bool) {
	return 0, "", false
}
