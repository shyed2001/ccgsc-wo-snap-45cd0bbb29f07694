// Path: internal/caps/caps_test.go | Author: MCode (MiniMax-M3.1) | 2026-11-22
// What/why: R15 AT22-GM2-3 (TD-17) - the resource caps of the server. The owner rule is
// "many cores, never all cores, never all RAM": every knob resolves to a value that is
// strictly below the machine capacity, and an invalid knob is a clear error, never a
// silent fallback.
package caps

import (
	"context"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// TestResolveClampsMaxProcs pins the clamp rules of maxProcs: 0, a negative value and
// every value at or above NumCPU resolve to NumCPU-1 (minimum 1), so the server never
// takes every core.
func TestResolveClampsMaxProcs(t *testing.T) {
	n := runtime.NumCPU()
	head := n - 1
	if head < 1 {
		head = 1
	}
	for _, tc := range []struct {
		name string
		in   int
		want int
	}{
		{"zero is the auto default", 0, head},
		{"negative is the auto default", -5, head},
		{"exactly NumCPU is clamped", n, head},
		{"above NumCPU is clamped", n + 7, head},
		{"one is kept", 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(Config{MaxProcs: tc.in})
			if err != nil {
				t.Fatalf("Resolve(%+v): %v", Config{MaxProcs: tc.in}, err)
			}
			if got.MaxProcs != tc.want {
				t.Fatalf("MaxProcs = %d, want %d", got.MaxProcs, tc.want)
			}
			if got.MaxProcs >= n && n > 1 {
				t.Fatalf("MaxProcs = %d takes every core of %d", got.MaxProcs, n)
			}
			if got.MaxProcs < 1 {
				t.Fatalf("MaxProcs = %d, want at least 1", got.MaxProcs)
			}
		})
	}
}

// TestResolveKeepsAValueBelowNumCPU pins that a value the owner chose inside the cap
// survives the resolution.
func TestResolveKeepsAValueBelowNumCPU(t *testing.T) {
	n := runtime.NumCPU()
	if n < 3 {
		t.Skip("the machine has fewer than three cores")
	}
	got, err := Resolve(Config{MaxProcs: 2})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.MaxProcs != 2 {
		t.Fatalf("MaxProcs = %d, want 2", got.MaxProcs)
	}
}

// TestResolveRejectsInvalidValues pins that an invalid knob is a clear error and never a
// silent fallback.
func TestResolveRejectsInvalidValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   Config
		want string
	}{
		{"negative memory limit", Config{MemLimitMB: -1}, "memLimitMB"},
		{"negative worker pool", Config{WorkerPool: -1}, "workerPool"},
		{"negative connection cap", Config{DBMaxOpenConns: -1}, "dbMaxOpenConns"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Resolve(tc.in)
			if err == nil {
				t.Fatalf("Resolve(%+v) accepted an invalid value", tc.in)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want the key %q", err.Error(), tc.want)
			}
		})
	}
}

// TestResolveMemoryLimitDefault pins the memory default: 75 % of the physical RAM when
// the machine reports it, off otherwise.
func TestResolveMemoryLimitDefault(t *testing.T) {
	got, err := Resolve(Config{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	phys, ok := PhysicalMemoryMB()
	if !ok || phys <= 0 {
		if got.MemLimitMB != 0 {
			t.Fatalf("MemLimitMB = %d, want 0 (off) when the RAM is not detectable", got.MemLimitMB)
		}
		return
	}
	want := int(phys * 3 / 4)
	if want < 1 {
		want = 1
	}
	if got.MemLimitMB != want {
		t.Fatalf("MemLimitMB = %d, want %d (75 %% of %d MB)", got.MemLimitMB, want, phys)
	}
	if got.MemLimitBytes != int64(got.MemLimitMB)<<20 {
		t.Fatalf("MemLimitBytes = %d, want %d", got.MemLimitBytes, int64(got.MemLimitMB)<<20)
	}
}

// TestResolveKeepsAnExplicitMemoryLimit pins that an explicit limit is never replaced by
// the default.
func TestResolveKeepsAnExplicitMemoryLimit(t *testing.T) {
	got, err := Resolve(Config{MemLimitMB: 512})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.MemLimitMB != 512 {
		t.Fatalf("MemLimitMB = %d, want 512", got.MemLimitMB)
	}
}

// TestResolveWorkerPoolDefault pins the bounded worker pool default: one slot per core
// below the cap, never more.
func TestResolveWorkerPoolDefault(t *testing.T) {
	n := runtime.NumCPU()
	head := n - 1
	if head < 1 {
		head = 1
	}
	got, err := Resolve(Config{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.WorkerPool != head {
		t.Fatalf("WorkerPool = %d, want %d", got.WorkerPool, head)
	}
	// An explicit pool is kept while it stays inside the head room; a pool that
	// would take every core is clamped, never refused.
	explicit := 3
	if explicit > head {
		explicit = head
	}
	got, err = Resolve(Config{WorkerPool: explicit})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.WorkerPool != explicit {
		t.Fatalf("WorkerPool = %d, want %d", got.WorkerPool, explicit)
	}
	got, err = Resolve(Config{WorkerPool: n + 10})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.WorkerPool > head {
		t.Fatalf("WorkerPool = %d, want at most %d", got.WorkerPool, head)
	}
}

// TestResolveConnectionCapDefault pins the database pool independent of CPU
// worker headroom (N13). Four slots prevents read/write starvation and 64
// bounds connection usage; explicit values are clamped to this safe interval.
func TestResolveConnectionCapDefault(t *testing.T) {
	got, err := Resolve(Config{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.DBMaxOpenConns < defaultDBMaxOpenConns {
		t.Fatalf("DBMaxOpenConns = %d, want >= %d to avoid connection starvation", got.DBMaxOpenConns, defaultDBMaxOpenConns)
	}
	if got.DBMaxOpenConns > maxDBMaxOpenConns {
		t.Fatalf("DBMaxOpenConns = %d, want <= %d", got.DBMaxOpenConns, maxDBMaxOpenConns)
	}
	// An explicit value below the safe floor cannot starve the connection pool.
	got, err = Resolve(Config{DBMaxOpenConns: 1})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.DBMaxOpenConns != defaultDBMaxOpenConns {
		t.Fatalf("DBMaxOpenConns = %d, want safe floor %d", got.DBMaxOpenConns, defaultDBMaxOpenConns)
	}
	// A configured cap above the safe ceiling is clamped, never unlimited.
	got, err = Resolve(Config{DBMaxOpenConns: 1000})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.DBMaxOpenConns != maxDBMaxOpenConns {
		t.Fatalf("DBMaxOpenConns = %d, want the safe ceiling %d", got.DBMaxOpenConns, maxDBMaxOpenConns)
	}
}

// TestApplySetsGOMAXPROCSAndMemoryLimit pins that Apply really changes the runtime.
func TestApplySetsGOMAXPROCSAndMemoryLimit(t *testing.T) {
	oldProcs := runtime.GOMAXPROCS(0)
	oldLimit := debug.SetMemoryLimit(-1)
	t.Cleanup(func() {
		runtime.GOMAXPROCS(oldProcs)
		debug.SetMemoryLimit(oldLimit)
	})
	c, err := Resolve(Config{MaxProcs: 1, MemLimitMB: 256})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	Apply(c, nil)
	if got := runtime.GOMAXPROCS(0); got != 1 {
		t.Fatalf("GOMAXPROCS = %d, want 1", got)
	}
	if got := debug.SetMemoryLimit(-1); got != int64(256)<<20 {
		t.Fatalf("memory limit = %d, want %d", got, int64(256)<<20)
	}
}

// TestApplyWithoutMemoryLimitKeepsTheRuntimeLimit pins that an undetectable RAM leaves
// the runtime memory limit alone (off).
func TestApplyWithoutMemoryLimitKeepsTheRuntimeLimit(t *testing.T) {
	oldLimit := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(oldLimit) })
	debug.SetMemoryLimit(-1)
	c, err := Resolve(Config{MaxProcs: 0})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := PhysicalMemoryMB(); ok {
		t.Skip("the machine reports its physical RAM, the default limit is not off")
	}
	Apply(c, nil)
	if got := debug.SetMemoryLimit(-1); got != -1 {
		t.Fatalf("memory limit = %d, want -1 (off)", got)
	}
}

// TestSemaphoreBoundsTheConcurrentWork pins the bounded worker pool: at most Cap tasks
// run at the same time and a caller waits for a free slot.
func TestSemaphoreBoundsTheConcurrentWork(t *testing.T) {
	sem := NewSemaphore(2)
	if sem.Cap() != 2 {
		t.Fatalf("Cap = %d, want 2", sem.Cap())
	}
	var running, peak int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sem.Do(context.Background(), func() error {
				now := atomic.AddInt64(&running, 1)
				for {
					old := atomic.LoadInt64(&peak)
					if now <= old || atomic.CompareAndSwapInt64(&peak, old, now) {
						break
					}
				}
				atomic.AddInt64(&running, -1)
				return nil
			}); err != nil {
				t.Errorf("Do: %v", err)
			}
		}()
	}
	wg.Wait()
	if peak > 2 {
		t.Fatalf("peak concurrency = %d, want at most 2", peak)
	}
}

// TestSemaphoreRejectsACancelledContext pins that a caller never waits forever.
func TestSemaphoreRejectsACancelledContext(t *testing.T) {
	sem := NewSemaphore(1)
	if !sem.TryAcquire() {
		t.Fatal("the first slot was not free")
	}
	defer sem.Release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sem.Do(ctx, func() error { return nil }); err == nil {
		t.Fatal("Do accepted a cancelled context")
	}
}

// TestSemaphoreWithZeroSlotsIsUnbounded pins that a zero capacity never blocks a caller
// (the knob resolves to at least one slot before it reaches the engine).
func TestSemaphoreWithZeroSlotsIsUnbounded(t *testing.T) {
	sem := NewSemaphore(0)
	if sem.Cap() != 0 {
		t.Fatalf("Cap = %d, want 0", sem.Cap())
	}
	done := make(chan struct{})
	go func() {
		_ = sem.Do(context.Background(), func() error { return nil })
		close(done)
	}()
	<-done
}
