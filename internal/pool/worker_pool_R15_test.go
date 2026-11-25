// Path: internal/pool/worker_pool_R15_test.go | Author: MCode (MiniMax-M3.1) | 2026-11-22
// What/why: R15 AT22-GM2-3 (TD-17) - the bounded worker pool of the engine. The CPU
// heavy platform work (the native recompute) runs in a slot of that pool, so the
// number of busy threads never exceeds the cap.
package pool

import "testing"

// TestEngineWorkerPoolSize pins that the engine keeps the size the server resolved.
func TestEngineWorkerPoolSize(t *testing.T) {
	h := newHarness(t)
	if got := h.engine.WorkerPoolSize(); got != 0 {
		t.Fatalf("WorkerPoolSize = %d, want 0 before the pool is set", got)
	}
	h.engine.SetWorkerPool(3)
	if got := h.engine.WorkerPoolSize(); got != 3 {
		t.Fatalf("WorkerPoolSize = %d, want 3", got)
	}
	h.engine.SetWorkerPool(1)
	if got := h.engine.WorkerPoolSize(); got != 1 {
		t.Fatalf("WorkerPoolSize = %d, want 1", got)
	}
}
