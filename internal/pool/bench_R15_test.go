// Path: internal/pool/bench_R15_test.go | Author: MCode (MiniMax-M3.1) | 2026-11-22
// What/why: R15 AT22-GM2-1 row 1.3 / AT22-GM2-3 - the benchmark of the engine: it
// submits N tasks through the engine (claim + submit) and reports the tasks per
// second. The number of parallel contributors is the number of cores the runtime was
// given, so the table of "go test -bench . -cpu 1,2,4,8" shows the scaling of the
// engine (and where it stops scaling).
package pool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"ccgsc/poolscore/internal/caps"
	"ccgsc/poolscore/internal/matmulref"
	"ccgsc/poolscore/internal/recompute"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/pg"
	"ccgsc/poolscore/internal/store/sqlite"
	"ccgsc/poolscore/internal/verify"
	"ccgsc/poolscore/web"

	"github.com/jackc/pgx/v5"
)

// benchTasksPerJob is the number of tasks one benchmark job carries.
const benchTasksPerJob = 32

// benchDBMaxOpenConns is the connection cap the benchmark opens with. R17-A2 (WO
// R17-SCALE item 3): the server caps its store at the resolved dbMaxOpenConns, which is
// CPU-1, so the benchmark resolves the same knob instead of opening one connection per
// goroutine of the runtime.
func benchDBMaxOpenConns() int {
	resolved, err := caps.Resolve(caps.Config{})
	if err != nil {
		return 1
	}
	return resolved.DBMaxOpenConns
}

// benchStore opens the store of one benchmark iteration. R17 (WO R17-SCALE item 4): the
// benchmark uses PostgreSQL when POOLS_PG_DSN is set, so the same table measures the store
// the production server runs, not only the single-writer SQLite debug store. Each iteration
// gets its own isolated schema, so a run never reads the rows of another. The returned
// cleanup closes the store and drops the schema.
func benchStore(b *testing.B, i int) (store.Store, func()) {
	if dsn := strings.TrimSpace(os.Getenv("POOLS_PG_DSN")); dsn != "" {
		ctx := context.Background()
		schema := fmt.Sprintf("r17_bench_%d_%d", time.Now().UnixNano(), i)
		admin, err := pgx.Connect(ctx, dsn)
		if err != nil {
			b.Fatalf("connect for the benchmark schema: %v", err)
		}
		if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
			b.Fatalf("create the benchmark schema: %v", err)
		}
		scoped := dsn
		if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
			u, err := url.Parse(dsn)
			if err != nil {
				b.Fatalf("parse POOLS_PG_DSN: %v", err)
			}
			q := u.Query()
			q.Set("search_path", schema+",public")
			q.Set("connect_timeout", "10")
			u.RawQuery = q.Encode()
			scoped = u.String()
		} else {
			scoped += " search_path=" + schema + ",public connect_timeout=10"
		}
		db, err := pg.OpenWithMaxConns(scoped, benchDBMaxOpenConns())
		if err != nil {
			b.Fatalf("pg.OpenWithMaxConns: %v", err)
		}
		return db, func() {
			_ = db.Close()
			_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
			_ = admin.Close(ctx)
		}
	}
	db, err := sqlite.Open(filepath.Join(b.TempDir(), fmt.Sprintf("bench-%d.db", i)))
	if err != nil {
		b.Fatalf("sqlite.Open: %v", err)
	}
	return db, func() { _ = db.Close() }
}

// BenchmarkEngineTaskCycle submits 32 tasks through the engine and reports the
// throughput. The setup (the store, the engine, the tabs and the job) is outside the
// timer, so the number is the cost of the engine and not the cost of the store. The store
// is PostgreSQL when POOLS_PG_DSN is set and SQLite otherwise.
func BenchmarkEngineTaskCycle(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	var total int
	var tx []time.Duration
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		db, cleanup := benchStore(b, i)
		defer cleanup()
		logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
		timed := &timedStore{Store: db}
		engine, err := New(timed, logger, "s1", time.Hour)
		if err != nil {
			b.Fatalf("pool.New: %v", err)
		}
		// R17: the per-pool store cap of A2 is resolved like the server resolves it,
		// so the benchmark cannot open more connections than production would.
		engine.SetWorkerPool(runtime.GOMAXPROCS(0))
		data := []byte("module bytes")
		artifactID := verify.SHA256(data)
		if _, err = engine.Upload(ctx, artifactID, "", 0, int64(len(data)), data); err != nil {
			b.Fatalf("Upload: %v", err)
		}
		workers := runtime.GOMAXPROCS(0)
		tabs := make([]string, 0, workers)
		for w := 0; w < workers; w++ {
			wk, err := engine.Register(ctx, Thread, "contributor", Worker{HostID: "host", Slots: 1})
			if err != nil {
				b.Fatalf("Register: %v", err)
			}
			tabs = append(tabs, wk.TabID)
		}
		q := CreateRequest{ModuleArtifactIDs: []string{artifactID}}
		for t := 0; t < benchTasksPerJob; t++ {
			q.Tasks = append(q.Tasks, TaskSpec{TaskID: fmt.Sprintf("task-%03d", t), Input: json.RawMessage(`{"json":{"n":1}}`)})
		}
		if _, err = engine.Create(ctx, Thread, "developer", "", q); err != nil {
			b.Fatalf("Create: %v", err)
		}
		b.StartTimer()
		completed := drainTasks(ctx, b, engine, tabs)
		b.StopTimer()
		total += completed
		tx = append(tx, timed.durations()...)
		_ = db.Close()
	}
	elapsed := b.Elapsed().Seconds()
	if elapsed > 0 {
		b.ReportMetric(float64(total)/elapsed, "tasks/s")
	}
	b.ReportMetric(float64(total), "tasks")
	// The transaction durations of the store (p50, p95, p99) show where the engine
	// waits: a long tail is a contended store, a flat curve is a contended lock.
	for name, value := range percentiles(tx) {
		b.ReportMetric(value, name)
	}
}

// BenchmarkEngineRecomputeTasks measures the CPU heavy path of the engine: the server
// side sample recompute of the reference matmul workload. The recompute runs inside
// the store transaction of the submission, so this benchmark shows the cost of the
// recompute and of the transaction that carries it.
func BenchmarkEngineRecomputeTasks(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	var total int
	var recomputed int
	var tx []time.Duration
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		db, cleanup := benchStore(b, i)
		defer cleanup()
		logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
		timed := &timedStore{Store: db}
		engine, err := New(timed, logger, "s1", time.Hour)
		if err != nil {
			b.Fatalf("pool.New: %v", err)
		}
		// R17 A1: the recompute runs in a slot of the bounded worker pool, so the pool
		// is sized like the server sizes it (CPU-1, minimum one).
		engine.SetWorkerPool(runtime.GOMAXPROCS(0))
		// Every task of the job is recomputed by the server.
		if _, err = engine.SetPoolSettings(ctx, Thread, PoolSettings{
			VerifyMode: VerifyModeSample, SampleRate: 1, RedundantRate: 0, IsolationMode: IsolationSeparateLock,
		}, nil); err != nil {
			b.Fatalf("SetPoolSettings: %v", err)
		}
		// WO R17-RECOMPUTE (G-R16-4): the recompute runs through the generic
		// reference registry, and the pool runs it in the "registry" mode.
		registry := recompute.New()
		if err = matmulref.Register(registry); err != nil {
			b.Fatalf("register the matmul reference: %v", err)
		}
		engine.SetRecomputeRegistry(registry)
		mode := RecomputeModeRegistry
		if _, err = engine.CommitRecomputeSettings(ctx, Thread, RecomputeSettingsDelta{Mode: &mode}, nil, "bench"); err != nil {
			b.Fatalf("CommitRecomputeSettings: %v", err)
		}
		kernel, err := fs.ReadFile(web.Workloads, "workloads/matmul/matmul_kernel.js")
		if err != nil {
			b.Fatalf("read the reference kernel: %v", err)
		}
		artifactID := verify.SHA256(kernel)
		if _, err = engine.Upload(ctx, artifactID, "", 0, int64(len(kernel)), kernel); err != nil {
			b.Fatalf("Upload: %v", err)
		}
		workers := runtime.GOMAXPROCS(0)
		tabs := make([]string, 0, workers)
		for w := 0; w < workers; w++ {
			wk, err := engine.Register(ctx, Thread, "contributor", Worker{HostID: "host", Slots: 1})
			if err != nil {
				b.Fatalf("Register: %v", err)
			}
			tabs = append(tabs, wk.TabID)
		}
		input, err := json.Marshal(map[string]any{"json": map[string]any{"kind": matmulref.Kind, "n": 160}})
		if err != nil {
			b.Fatal(err)
		}
		q := CreateRequest{ModuleArtifactIDs: []string{artifactID}}
		for t := 0; t < benchRecomputeTasks; t++ {
			q.Tasks = append(q.Tasks, TaskSpec{TaskID: fmt.Sprintf("task-%03d", t), Input: input})
		}
		job, err := engine.Create(ctx, Thread, "developer", "", q)
		if err != nil {
			b.Fatalf("Create: %v", err)
		}
		b.StartTimer()
		completed := drainRecomputeTasks(ctx, b, engine, tabs)
		b.StopTimer()
		total += completed
		tx = append(tx, timed.durations()...)
		// The evidence that the server really recomputed: the stored result of
		// every task carries the recompute verdict.
		stored, err := engine.Get(ctx, Thread, "developer", job.JobID)
		if err != nil {
			b.Fatalf("Get: %v", err)
		}
		for _, res := range stored.Results {
			if res.RecomputeVerified {
				recomputed++
			}
		}
		_ = db.Close()
	}
	elapsed := b.Elapsed().Seconds()
	if elapsed > 0 {
		b.ReportMetric(float64(total)/elapsed, "tasks/s")
	}
	b.ReportMetric(float64(total), "tasks")
	b.ReportMetric(float64(recomputed), "recomputed")
	for name, value := range percentiles(tx) {
		b.ReportMetric(value, name)
	}
}

// drainRecomputeTasks claims and submits the reference matmul results, so the server
// recomputes every one of them.
func drainRecomputeTasks(ctx context.Context, b *testing.B, engine *Engine, tabs []string) int {
	var mu sync.Mutex
	var completed int
	var wg sync.WaitGroup
	for _, tab := range tabs {
		wg.Add(1)
		go func(tab string) {
			defer wg.Done()
			for {
				a, err := engine.Claim(ctx, Thread, "contributor", tab)
				if err != nil {
					return
				}
				out := json.RawMessage(`{"n":160,"sha256":"` + matmulReferenceHash160 + `"}`)
				if _, err = engine.Submit(ctx, Thread, "contributor", tab, Submission{
					Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out),
				}); err != nil {
					return
				}
				mu.Lock()
				completed++
				mu.Unlock()
			}
		}(tab)
	}
	wg.Wait()
	return completed
}

// matmulReferenceHash160 is the published hash of the reference matmul output for
// n=160 (the same pin the server recompute uses).
const matmulReferenceHash160 = "1a5a6c39c56eada3b4ca01eaed913972ed5e48d1318884748e1abf3646abc2d2"

// benchRecomputeTasks is the number of recomputed tasks of one benchmark job.
const benchRecomputeTasks = 8

// timedStore measures the duration of every store transaction of the benchmark.
type timedStore struct {
	store.Store
	mu   sync.Mutex
	durs []time.Duration
}

func (s *timedStore) Update(ctx context.Context, fn func(store.Tx) error) error {
	start := time.Now()
	err := s.Store.Update(ctx, fn)
	s.mu.Lock()
	s.durs = append(s.durs, time.Since(start))
	s.mu.Unlock()
	return err
}

func (s *timedStore) durations() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]time.Duration, len(s.durs))
	copy(out, s.durs)
	return out
}

// percentiles returns the p50, p95 and p99 of the measured durations in milliseconds.
func percentiles(d []time.Duration) map[string]float64 {
	out := map[string]float64{}
	if len(d) == 0 {
		return out
	}
	sorted := make([]time.Duration, len(d))
	copy(sorted, d)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	at := func(p float64) float64 {
		idx := int(p * float64(len(sorted)-1))
		return float64(sorted[idx].Microseconds()) / 1000
	}
	out["tx-p50-ms"] = at(0.50)
	out["tx-p95-ms"] = at(0.95)
	out["tx-p99-ms"] = at(0.99)
	return out
}

// drainTasks claims and submits until the job has no task left. It returns the number
// of settled tasks.
func drainTasks(ctx context.Context, b *testing.B, engine *Engine, tabs []string) int {
	var mu sync.Mutex
	var completed int
	var wg sync.WaitGroup
	for _, tab := range tabs {
		wg.Add(1)
		go func(tab string) {
			defer wg.Done()
			for {
				a, err := engine.Claim(ctx, Thread, "contributor", tab)
				if err != nil {
					return
				}
				out := json.RawMessage(`{"json":{"answer":42}}`)
				if _, err = engine.Submit(ctx, Thread, "contributor", tab, Submission{
					Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out),
				}); err != nil {
					return
				}
				mu.Lock()
				completed++
				mu.Unlock()
			}
		}(tab)
	}
	wg.Wait()
	return completed
}
