// Path: internal/pool/chain_bench_test.go | Author: Hermes (MiniMax) for the owner | 2026-11-18
// What/why: the thesis benchmark of the audit chain: the overhead of the chain ON
// against the chain OFF, and the cost of the Merkle checkpoint; WWPP-R27.2 (WO T42 F3).
package pool

import (
	"ccgsc/poolscore/internal/store/sqlite"
	"ccgsc/poolscore/internal/verify"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

// benchJob creates a job with n tasks and settles every task, so the chain (when it is
// on) holds one entry per accepted result.
func benchJob(t testing.TB, h *chainHarness, n int) Job {
	t.Helper()
	ctx := context.Background()
	data := []byte("module bytes")
	id := verify.SHA256(data)
	if _, e := h.engine.Upload(ctx, id, "", 0, int64(len(data)), data); e != nil {
		t.Fatalf("Upload: %v", e)
	}
	q := CreateRequest{ModuleArtifactIDs: []string{id}}
	for i := 0; i < n; i++ {
		q.Tasks = append(q.Tasks, TaskSpec{TaskID: "task-" + string(rune('a'+i)), Input: json.RawMessage(`{"json":{"n":1}}`)})
	}
	j, e := h.engine.Create(ctx, Thread, "developer", "", q)
	if e != nil {
		t.Fatalf("Create: %v", e)
	}
	return j
}

// benchSettle settles every task of the job with one worker.
func benchSettle(t testing.TB, h *chainHarness, j Job) {
	t.Helper()
	ctx := context.Background()
	w, e := h.engine.Register(ctx, Thread, "contributor", Worker{HostID: "host", Slots: 16})
	if e != nil {
		t.Fatalf("Register: %v", e)
	}
	out := json.RawMessage(`{"json":{"answer":42}}`)
	for i := 0; i < len(j.Tasks); i++ {
		a, e := h.engine.Claim(ctx, Thread, "contributor", w.TabID)
		if e != nil {
			t.Fatalf("Claim: %v", e)
		}
		if _, e = h.engine.Submit(ctx, Thread, "contributor", w.TabID, Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out)}); e != nil {
			t.Fatalf("Submit: %v", e)
		}
	}
}

// BenchmarkChainOverhead measures the cost of the per-job hash chain: the same job
// with the chain OFF and with the chain ON.
func BenchmarkChainOverhead(b *testing.B) {
	for _, chain := range []bool{false, true} {
		name := "chain-off"
		if chain {
			name = "chain-on"
		}
		b.Run(name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				h := newChainBenchmarkHarness(b)
				if chain {
					cfg := h.engine.ChainConfig()
					cfg.ChainEnabled = true
					if e := h.engine.SetChainConfig(cfg); e != nil {
						b.Fatalf("SetChainConfig: %v", e)
					}
				}
				j := benchJob(b, h, 20)
				b.StartTimer()
				benchSettle(b, h, j)
				b.StopTimer()
			}
		})
	}
}

// newChainBenchmarkHarness builds an engine for a benchmark.
func newChainBenchmarkHarness(b testing.TB) *chainHarness {
	b.Helper()
	db, e := sqlite.Open(filepath.Join(b.TempDir(), "pool.db"))
	if e != nil {
		b.Fatalf("sqlite.Open: %v", e)
	}
	b.Cleanup(func() { _ = db.Close() })
	engine, e := New(db, slog.New(slog.NewJSONHandler(io.Discard, nil)), "s1", time.Minute)
	if e != nil {
		b.Fatalf("New: %v", e)
	}
	return &chainHarness{engine: engine}
}

// BenchmarkMerkleCheckpoint measures the cost of one global Merkle checkpoint over the
// tips of the job chains.
func BenchmarkMerkleCheckpoint(b *testing.B) {
	h := newChainBenchmarkHarness(b)
	cfg := h.engine.ChainConfig()
	cfg.ChainEnabled = true
	cfg.MerkleEnabled = true
	if e := h.engine.SetChainConfig(cfg); e != nil {
		b.Fatalf("SetChainConfig: %v", e)
	}
	for i := 0; i < 5; i++ {
		j := benchJob(b, h, 10)
		benchSettle(b, h, j)
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, e := h.engine.ForceCheckpoint(ctx); e != nil {
			b.Fatalf("ForceCheckpoint: %v", e)
		}
	}
}

// BenchmarkChainEntryHash measures the cost of one chain entry hash, the hot path of
// the chain.
func BenchmarkChainEntryHash(b *testing.B) {
	entry := ChainEntry{Seq: 1, JobID: "s1.tp.abc", TaskID: "task-a", ResultIndex: 0, ResultSHA: verify.SHA256([]byte("output")), WorkerID: "w1", Prev: ""}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, e := chainEntryHash(entry); e != nil {
			b.Fatalf("chainEntryHash: %v", e)
		}
	}
}
