// Path: internal/caps/mem_budget.go | Author: Hermes (MiniMax-M3) | 2026-11-22
// What/why: R15-FIX-CAPS (a) and (b) - the memory budget of the process.
//
// The budget has two parts:
//   - the cgroup budget: the memory the container (or the systemd unit) may use.
//     On Linux it is cgroup v2 memory.max, on cgroup v1 memory.limit_in_bytes.
//   - the physical RAM of the machine.
//
// The memory the process may plan for is the SMALLER of the two: a container that
// may only use 1 GB must not plan for the 64 GB of the machine it runs on.
//
// The platform policy is explicit:
//   - Linux: the cgroup budget (v2, then v1) and the physical RAM of /proc/meminfo.
//   - Windows: the physical RAM of kernel32 (GlobalMemoryStatusEx).
//   - The BSD family: the physical RAM of the sysctl hw.memsize.
//   - Every other platform: the automatic limit stays off and the reason is logged
//     as "memLimit auto: unavailable on <os>".
//
// The budget never imports workload code (CR-53) and never the config package.
package caps

import (
	"fmt"
	"os"
	"runtime"
)

// MemoryBudget is the memory the process of the server may plan for.
type MemoryBudget struct {
	// CgroupMB is the memory the cgroup of the process may use, in megabytes.
	// Zero means "no cgroup budget" (no cgroup, or an unlimited cgroup).
	CgroupMB int64
	// PhysicalMB is the physical RAM of the machine, in megabytes. Zero means
	// "the machine does not report it".
	PhysicalMB int64
	// CgroupSource names where the cgroup budget came from ("cgroup-v2",
	// "cgroup-v1"). It is empty when there is no cgroup budget.
	CgroupSource string
}

// AvailableMB returns the memory the process may plan for: the smaller of the
// cgroup budget and the physical RAM. Zero means "unknown", and the automatic
// memory limit then stays off.
func (b MemoryBudget) AvailableMB() int64 {
	if b.CgroupMB > 0 && (b.PhysicalMB <= 0 || b.CgroupMB < b.PhysicalMB) {
		return b.CgroupMB
	}
	return b.PhysicalMB
}

// DetectMemoryBudget reads the memory budget of the running process: the cgroup
// budget (any platform that has a readable cgroup hierarchy) and the physical RAM
// (the platform specific reader).
func DetectMemoryBudget() MemoryBudget {
	b := MemoryBudget{}
	if mb, source, ok := cgroupMemoryMB(); ok {
		b.CgroupMB = mb
		b.CgroupSource = source
	}
	if phys, ok := PhysicalMemoryMB(); ok {
		b.PhysicalMB = phys
	}
	return b
}

// PhysicalMemoryMB returns the physical RAM of the machine in megabytes. The second
// value is false when the machine does not report it.
func PhysicalMemoryMB() (int64, bool) {
	return physicalMemoryMB()
}

// memLimitAutoUnavailableMessage is the log line of a platform that cannot detect
// the memory of the machine: the automatic memory limit stays off and the operator
// sees why.
func memLimitAutoUnavailableMessage(goos string) string {
	return "memLimit auto: unavailable on " + goos
}

// SoftLimitIsSoft reports that the memory limit of the server is a soft limit.
// It is always true: the limit is the GC target of the runtime, never a hard RSS
// ceiling.
func SoftLimitIsSoft() bool { return true }

// SoftLimitExplanation is the wording that the admin caps view and the config
// comment share: the limit is a soft GC target, not a hard RSS cap.
func SoftLimitExplanation() string {
	return "the memory limit is a soft GC target, not a hard limit on the RSS of the process: " +
		"the garbage collector works harder as the heap approaches the limit, but the process may still grow past it"
}

// resolveWithBudget turns the knobs into the effective caps, using the given memory
// budget for the automatic memory limit. The budget is a parameter, so the rule is
// testable without a real cgroup.
func resolveWithBudget(c Config, budget MemoryBudget) (Caps, error) {
	if c.MemLimitMB < 0 {
		return Caps{}, errNegativeMemLimit(c.MemLimitMB)
	}
	if c.WorkerPool < 0 {
		return Caps{}, fmt.Errorf("workerPool must not be negative, got %d", c.WorkerPool)
	}
	if c.DBMaxOpenConns < 0 {
		return Caps{}, fmt.Errorf("dbMaxOpenConns must not be negative, got %d", c.DBMaxOpenConns)
	}
	n := runtime.NumCPU()
	head := n - 1
	if head < 1 {
		head = 1
	}
	out := Caps{NumCPU: n, SoftLimit: SoftLimitIsSoft()}
	switch {
	case c.MaxProcs <= 0 || c.MaxProcs >= n:
		out.MaxProcs = head
	default:
		out.MaxProcs = c.MaxProcs
	}
	switch {
	case c.WorkerPool == 0:
		out.WorkerPool = head
	case c.WorkerPool > head:
		out.WorkerPool = head
	default:
		out.WorkerPool = c.WorkerPool
	}
	if c.DBMaxOpenConns == 0 {
		out.DBMaxOpenConns = defaultDBMaxOpenConns
		if n > defaultDBMaxOpenConns {
			out.DBMaxOpenConns = n
		}
		if out.DBMaxOpenConns > maxDBMaxOpenConns {
			out.DBMaxOpenConns = maxDBMaxOpenConns
		}
	} else {
		out.DBMaxOpenConns = c.DBMaxOpenConns
	}
	// N13 / R18B row 15: CPU-1 is a CPU/worker concurrency policy, not a
	// PostgreSQL connection policy. A tiny database pool (1-2 slots) can starve
	// overlapping transactions and event/config reads. Enforce a four-connection
	// floor and a bounded 64-connection ceiling for auto AND explicit settings;
	// never clamp the DB pool to the CPU worker headroom.
	if out.DBMaxOpenConns < defaultDBMaxOpenConns {
		out.DBMaxOpenConns = defaultDBMaxOpenConns
	}
	if out.DBMaxOpenConns > maxDBMaxOpenConns {
		out.DBMaxOpenConns = maxDBMaxOpenConns
	}
	// memLimitMB: the automatic limit is 75 % of the memory the process may plan
	// for (the smaller of the cgroup budget and the physical RAM). Without a
	// budget the automatic limit stays off.
	out.PhysicalMemMB = budget.PhysicalMB
	out.CgroupMemMB = budget.CgroupMB
	out.CgroupSource = budget.CgroupSource
	out.AvailableMemMB = budget.AvailableMB()
	switch {
	case c.MemLimitMB < 0:
		return Caps{}, errNegativeMemLimit(c.MemLimitMB)
	case c.MemLimitMB > 0:
		// An explicit limit is the operator's decision: the package never lowers
		// it silently, even when it is above the budget.
		out.MemLimitMB = c.MemLimitMB
		out.MemLimitSource = "explicit"
	case out.AvailableMemMB > 0:
		out.MemLimitMB = int(out.AvailableMemMB * 3 / 4)
		if out.MemLimitMB < 1 {
			out.MemLimitMB = 1
		}
		out.MemLimitSource = "auto"
	default:
		out.MemLimitMB = 0
		out.MemLimitSource = "off"
	}
	out.MemLimitBytes = int64(out.MemLimitMB) << 20
	return out, nil
}

// osReadFile is the default reader of the cgroup files.
func osReadFile(name string) ([]byte, error) {
	return os.ReadFile(name)
}
