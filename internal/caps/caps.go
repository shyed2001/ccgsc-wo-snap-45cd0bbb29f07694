// Path: internal/caps/caps.go | Author: MCode (MiniMax-M3.1) | 2026-11-22
// What/why: R15 AT22-GM2-3 (TD-17) - the resource caps of the server. The owner rule is
// "many cores, never all cores, never all RAM": the server may use every core but one
// and at most 75 % of the physical RAM, and every cap is configurable.
//
// The package owns three things:
//   - the resolution of the four knobs (maxProcs, memLimitMB, workerPool,
//     dbMaxOpenConns) into the effective values,
//   - the application of the effective values to the Go runtime,
//   - the bounded worker pool (a semaphore) that keeps the CPU heavy platform work
//     (hashing, native recompute) inside its lane.
//
// It never imports workload code (CR-53) and it never imports the config package, so
// the config package can embed the knobs without a cycle.
package caps

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"runtime/debug"
)

// Config carries the four owner knobs. The zero value of every knob means "the server
// decides" (auto), never "unlimited".
type Config struct {
	// MaxProcs is the GOMAXPROCS value. 0 or negative means auto (NumCPU-1, minimum
	// 1); a value at or above NumCPU is clamped down to NumCPU-1.
	MaxProcs int `json:"maxProcs"`
	// MemLimitMB is the soft memory limit in megabytes (debug.SetMemoryLimit). 0
	// means auto (75 % of the memory the process may plan for: the smaller of the
	// cgroup budget and the physical RAM); a negative value is a configuration
	// error.
	//
	// R15-FIX-CAPS (c): the limit is a SOFT GC target, not a hard RSS cap. The
	// garbage collector works harder as the heap approaches the limit, but the
	// process may still grow past it (a cgroup or an OS limit is the only hard
	// ceiling). An explicit value is never lowered silently by the package.
	MemLimitMB int `json:"memLimitMB"`
	// WorkerPool is the number of slots of the bounded worker pool that runs the
	// CPU heavy platform work. 0 means auto (NumCPU-1, minimum 1); a negative value
	// is a configuration error.
	WorkerPool int `json:"workerPool"`
	// DBMaxOpenConns is the PostgreSQL connection cap. 0 means auto; effective
	// values are bounded to [4,64] independently of CPU worker limits. A negative
	// value is invalid. SQLite keeps its single writer.
	DBMaxOpenConns int `json:"dbMaxOpenConns"`
}

// Caps is the resolved and effective cap set of one running server.
type Caps struct {
	// NumCPU is the number of logical CPUs of the machine.
	NumCPU int `json:"numCPU"`
	// MaxProcs is the effective GOMAXPROCS value.
	MaxProcs int `json:"maxProcs"`
	// MemLimitMB is the effective soft memory limit in megabytes (0 = off).
	MemLimitMB int `json:"memLimitMB"`
	// WorkerPool is the effective number of slots of the bounded worker pool.
	WorkerPool int `json:"workerPool"`
	// DBMaxOpenConns is the effective connection cap of the PostgreSQL pool.
	DBMaxOpenConns int `json:"dbMaxOpenConns"`
	// MemLimitBytes is MemLimitMB in bytes (0 = off).
	MemLimitBytes int64 `json:"memLimitBytes"`
	// PhysicalMemMB is the physical RAM of the machine in megabytes (0 = unknown).
	PhysicalMemMB int64 `json:"physicalMemMB"`
	// CgroupMemMB is the memory the cgroup of the process may use, in megabytes
	// (0 = no cgroup budget).
	CgroupMemMB int64 `json:"cgroupMemMB"`
	// CgroupSource names where the cgroup budget came from ("cgroup-v2",
	// "cgroup-v1"); it is empty when there is no cgroup budget.
	CgroupSource string `json:"cgroupSource"`
	// AvailableMemMB is the memory the process may plan for: the smaller of the
	// cgroup budget and the physical RAM (0 = unknown).
	AvailableMemMB int64 `json:"availableMemMB"`
	// MemLimitSource says where the effective limit came from: "explicit",
	// "auto" or "off".
	MemLimitSource string `json:"memLimitSource"`
	// SoftLimit is true: the memory limit is a soft GC target, never a hard RSS cap.
	SoftLimit bool `json:"softLimit"`
}

// RuntimeStats are the live counters of the process.
type RuntimeStats struct {
	NumCPU          int    `json:"numCPU"`
	GOMAXPROCS      int    `json:"gomaxprocs"`
	NumGoroutine    int    `json:"numGoroutine"`
	HeapAllocBytes  uint64 `json:"heapAllocBytes"`
	HeapSysBytes    uint64 `json:"heapSysBytes"`
	HeapInuseBytes  uint64 `json:"heapInuseBytes"`
	StackInuseBytes uint64 `json:"stackInuseBytes"`
	SysBytes        uint64 `json:"sysBytes"`
	TotalAllocBytes uint64 `json:"totalAllocBytes"`
	NumGC           uint32 `json:"numGC"`
	MemLimitBytes   int64  `json:"memLimitBytes"`
}

// defaultDBMaxOpenConns is the floor of the automatic connection cap: an unlimited
// pool would take every connection of the database server.
const defaultDBMaxOpenConns = 4

// maxDBMaxOpenConns is the ceiling of the automatic connection cap.
const maxDBMaxOpenConns = 64

// Resolve turns the four knobs into the effective caps. An invalid knob is an error
// with a clear message; a knob that would take every core is clamped, never refused.
//
// R15-FIX-CAPS (a): the automatic memory limit comes from the memory budget of the
// process, which is the smaller of the cgroup budget and the physical RAM.
func Resolve(c Config) (Caps, error) {
	return resolveWithBudget(c, DetectMemoryBudget())
}

// errNegativeMemLimit is the error of a negative memory limit.
func errNegativeMemLimit(v int) error {
	return fmt.Errorf("memLimitMB must not be negative, got %d", v)
}

// Apply sets the runtime caps: GOMAXPROCS and the soft memory limit. The applied
// values are logged once, so the log of the start carries the real numbers.
//
// R15-FIX-CAPS (b): a platform that cannot detect the memory of the machine keeps
// the automatic limit off, and the reason is logged as
// "memLimit auto: unavailable on <os>".
// R15-FIX-CAPS (c): the memory limit is a soft GC target, never a hard RSS cap.
func Apply(c Caps, log *slog.Logger) {
	runtime.GOMAXPROCS(c.MaxProcs)
	if c.MemLimitBytes > 0 {
		debug.SetMemoryLimit(c.MemLimitBytes)
	}
	if log != nil {
		if c.MemLimitSource == "off" {
			log.Info(memLimitAutoUnavailableMessage(runtime.GOOS))
		}
		log.Info("server.caps",
			"numCPU", c.NumCPU,
			"maxProcs", c.MaxProcs,
			"gomaxprocs", runtime.GOMAXPROCS(0),
			"memLimitMB", c.MemLimitMB,
			"memLimitBytes", c.MemLimitBytes,
			"memLimitSource", c.MemLimitSource,
			"memLimitSoft", c.SoftLimit,
			"physicalMemMB", c.PhysicalMemMB,
			"cgroupMemMB", c.CgroupMemMB,
			"cgroupSource", c.CgroupSource,
			"availableMemMB", c.AvailableMemMB,
			"workerPool", c.WorkerPool,
			"dbMaxOpenConns", c.DBMaxOpenConns,
		)
	}
}

// Stats reads the live counters of the process.
func Stats() RuntimeStats {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return RuntimeStats{
		NumCPU:          runtime.NumCPU(),
		GOMAXPROCS:      runtime.GOMAXPROCS(0),
		NumGoroutine:    runtime.NumGoroutine(),
		HeapAllocBytes:  m.HeapAlloc,
		HeapSysBytes:    m.HeapSys,
		HeapInuseBytes:  m.HeapInuse,
		StackInuseBytes: m.StackInuse,
		SysBytes:        m.Sys,
		TotalAllocBytes: m.TotalAlloc,
		NumGC:           m.NumGC,
		MemLimitBytes:   debug.SetMemoryLimit(-1),
	}
}

// Semaphore is the bounded worker pool: at most Cap tasks run at the same time and
// every caller waits for a free slot. It is the only way the platform code runs its
// CPU heavy work, so the number of busy threads never exceeds the cap.
type Semaphore struct {
	ch chan struct{}
}

// NewSemaphore builds a semaphore with n slots. A capacity of zero means "no limit"
// (the knob resolves to at least one slot before it reaches the engine).
func NewSemaphore(n int) *Semaphore {
	if n <= 0 {
		return &Semaphore{}
	}
	return &Semaphore{ch: make(chan struct{}, n)}
}

// Cap returns the number of slots.
func (s *Semaphore) Cap() int {
	if s == nil || s.ch == nil {
		return 0
	}
	return cap(s.ch)
}

// TryAcquire takes a slot without waiting. It returns false when every slot is busy,
// so a caller may skip the work instead of queueing it.
func (s *Semaphore) TryAcquire() bool {
	if s == nil || s.ch == nil {
		return true
	}
	select {
	case s.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

// Release gives the slot back.
func (s *Semaphore) Release() {
	if s == nil || s.ch == nil {
		return
	}
	<-s.ch
}

// Acquire takes a slot and waits for it. A cancelled context ends the wait.
func (s *Semaphore) Acquire(ctx context.Context) error {
	if s == nil || s.ch == nil {
		return nil
	}
	select {
	case s.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Do runs f in one slot of the pool. The slot is released when f returns.
func (s *Semaphore) Do(ctx context.Context, f func() error) error {
	if err := s.Acquire(ctx); err != nil {
		return err
	}
	defer s.Release()
	return f()
}
