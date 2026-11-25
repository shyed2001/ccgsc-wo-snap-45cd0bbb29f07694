// Path: internal/pool/submit_race_R17_test.go | Author: CommandCode for the owner | 2026-11-22
// What/why: WO R17-SCALE item 2 - the settlement race test of the pre-transaction
// verdict. 16 goroutines submit duplicate, late and expiring claims with the server
// sample recompute at rate 1.0: no task may settle twice and a replayed submission must
// be rejected whatever order the goroutines reach the settlement transaction. The last
// test proves the expensive work really left the write transaction.
package pool

import (
	"ccgsc/poolscore/internal/matmulref"
	"ccgsc/poolscore/internal/recompute"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/sqlite"
	"ccgsc/poolscore/internal/verify"
	"ccgsc/poolscore/web"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// registerGoroutine registers one contributor tab of the race test.
func (h *harness) registerGoroutine(t *testing.T, i int) Worker {
	t.Helper()
	w, err := h.engine.Register(context.Background(), Thread, "contributor-"+string(rune('a'+i)),
		Worker{HostID: "host-" + string(rune('a'+i)), Slots: 1, ClientIP: "10.0." + string(rune('0'+i%10)) + ".5"})
	if err != nil {
		t.Fatalf("Register(%d): %v", i, err)
	}
	return w
}

// matmulJobTasks seeds one job with n matmul tasks of dimension dim.
func (h *harness) matmulJobTasks(t *testing.T, n, dim int) Job {
	t.Helper()
	kernel, err := fs.ReadFile(web.Workloads, "workloads/matmul/matmul_kernel.js")
	if err != nil {
		t.Fatalf("read reference kernel: %v", err)
	}
	module := uploadF1(t, h.engine, kernel)
	input, err := json.Marshal(map[string]any{"json": map[string]any{"kind": matmulref.Kind, "n": dim}})
	if err != nil {
		t.Fatal(err)
	}
	q := CreateRequest{ModuleArtifactIDs: []string{module}}
	for i := 0; i < n; i++ {
		q.Tasks = append(q.Tasks, TaskSpec{TaskID: "task-m" + string(rune('a'+i)), Input: input})
	}
	j, err := h.engine.Create(context.Background(), Thread, "developer", "", q)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return j
}

// TestR17ConcurrentSubmitsSettleOnceAndReplayIsRejected is the WO R17 race test. 16
// goroutines claim and submit the tasks of one job, then replay the exact same
// submission: every task settles exactly once and every replay is refused as a
// conflict. The sample recompute runs at rate 1.0, so the concurrent prepares also
// exercise the bounded worker pool.
func TestR17ConcurrentSubmitsSettleOnceAndReplayIsRejected(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	// R17-RECOMPUTE defaults to off; this scale test requires a real
	// registered reference and explicitly enabled registry mode.
	h.enableRecompute(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})
	h.engine.SetWorkerPool(4)
	ctx := context.Background()
	const goroutines = 16
	job := h.matmulJobTasks(t, goroutines, 160)

	var settled int64
	var replaysRejected int64
	var unexpected int64
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		w := h.registerGoroutine(t, i)
		wg.Add(1)
		go func(w Worker) {
			defer wg.Done()
			for {
				a, err := h.engine.Claim(ctx, Thread, w.Owner, w.TabID)
				if errors.Is(err, ErrEmpty) {
					return // every task of the job is leased or settled
				}
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					return
				}
				out := json.RawMessage(`{"n":160,"sha256":"` + matmulRef160 + `"}`)
				q := Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out)}
				res, err := h.engine.Submit(ctx, Thread, w.Owner, w.TabID, q)
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					return
				}
				if res.Status != "done" || !res.RecomputeVerified {
					atomic.AddInt64(&unexpected, 1)
				}
				atomic.AddInt64(&settled, 1)
				// The replay of the exact same submission must be refused, never
				// settled a second time.
				if _, err := h.engine.Submit(ctx, Thread, w.Owner, w.TabID, q); err != nil {
					atomic.AddInt64(&replaysRejected, 1)
				} else {
					atomic.AddInt64(&unexpected, 1)
				}
			}
		}(w)
	}
	wg.Wait()

	if firstErr != nil {
		t.Fatalf("a correct submission was refused: %v", firstErr)
	}
	if settled != int64(goroutines) {
		t.Fatalf("settled submissions = %d, want %d", settled, goroutines)
	}
	if replaysRejected != int64(goroutines) {
		t.Fatalf("replays rejected = %d, want %d (one replay per settlement)", replaysRejected, goroutines)
	}
	if unexpected != 0 {
		t.Fatalf("unexpected settlements = %d (a replayed result must never settle twice)", unexpected)
	}
	got, err := h.engine.Get(ctx, Thread, "developer", job.JobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Results) != goroutines || got.Progress.Completed != goroutines || got.State != "completed" {
		t.Fatalf("job = state %q completed %d results %d, want %d settlements", got.State, got.Progress.Completed, len(got.Results), goroutines)
	}
}

// TestR17DuplicateSubmitsSettleOnce pins the narrow form of the same race: two
// goroutines submit the same lease at the same time. Exactly one settles and the other
// is refused; the job never holds two results for one task.
func TestR17DuplicateSubmitsSettleOnce(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	ctx := context.Background()
	j := h.seed(t, 1, nil)
	w1 := h.workerAt(t, "contributor-1", "host-1", "10.0.0.5")
	w2 := h.workerAt(t, "contributor-2", "host-2", "10.0.2.5")
	a, err := h.claimAs(t, w1)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	out := json.RawMessage(`{"answer":42}`)
	q := Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out)}
	var settled, refused int64
	var wg sync.WaitGroup
	for _, w := range []Worker{w1, w2} {
		wg.Add(1)
		go func(w Worker) {
			defer wg.Done()
			if _, err := h.engine.Submit(ctx, Thread, w.Owner, w.TabID, q); err != nil {
				atomic.AddInt64(&refused, 1)
				return
			}
			atomic.AddInt64(&settled, 1)
		}(w)
	}
	wg.Wait()
	if settled != 1 || refused != 1 {
		t.Fatalf("settled=%d refused=%d, want exactly one settlement and one refusal", settled, refused)
	}
	got, err := h.engine.Get(ctx, Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Results) != 1 {
		t.Fatalf("results = %d, want exactly one settlement", len(got.Results))
	}
}

// TestR17LateSubmitAfterExpirySettlesOnce pins the late path under the prepared
// verdict: a submission that reaches the store after its lease expired settles through
// the recorded expired-lease evidence exactly once, and a second submission of the same
// evidence is refused.
func TestR17LateSubmitAfterExpirySettlesOnce(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	j := h.seed(t, 1, nil)
	ctx := context.Background()
	w := h.worker(t, 1)
	a, err := h.engine.Claim(ctx, Thread, "contributor", w.TabID)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	// The sweep records the expired lease before the late submission arrives.
	h.now = h.now.Add(2 * time.Minute)
	if err := h.engine.Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	out := json.RawMessage(`{"late":true}`)
	q := Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out)}
	res, err := h.engine.Submit(ctx, Thread, "contributor", w.TabID, q)
	if err != nil {
		t.Fatalf("late Submit: %v", err)
	}
	if !res.Late || res.Status != "done" {
		t.Fatalf("late result = %+v, want one late settlement", res)
	}
	if _, err := h.engine.Submit(ctx, Thread, "contributor", w.TabID, q); !errors.Is(err, ErrConflict) && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second late Submit = %v, want the consumed evidence refused", err)
	}
	got, err := h.engine.Get(ctx, Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Results) != 1 {
		t.Fatalf("results = %d, want exactly one late settlement", len(got.Results))
	}
}

// TestR17ExpiringLeaseSettlesOnce pins the expiring submit: the lease is still live when
// the submission is prepared and settles exactly once; the identical submission after
// the settlement is refused.
func TestR17ExpiringLeaseSettlesOnce(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	j := h.seed(t, 1, nil)
	ctx := context.Background()
	w := h.worker(t, 1)
	a, err := h.engine.Claim(ctx, Thread, "contributor", w.TabID)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	// One second before the lease expires: the submission is still on time.
	h.now = a.Lease.ExpiresAt.Add(-time.Second)
	out := json.RawMessage(`{"answer":7}`)
	q := Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out)}
	if _, err := h.engine.Submit(ctx, Thread, "contributor", w.TabID, q); err != nil {
		t.Fatalf("expiring Submit: %v", err)
	}
	// The same submission one second after the expiry: the lease is gone.
	h.now = a.Lease.ExpiresAt.Add(time.Second)
	if _, err := h.engine.Submit(ctx, Thread, "contributor", w.TabID, q); !errors.Is(err, ErrConflict) {
		t.Fatalf("Submit after expiry = %v, want ErrConflict", err)
	}
	got, err := h.engine.Get(ctx, Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Results) != 1 {
		t.Fatalf("results = %d, want exactly one settlement", len(got.Results))
	}
}

// txRecorder records the actual transaction callback boundary, as well as timing
// diagnostics. Host load and SQLite I/O must not determine a functional test.
type txRecorder struct {
	store.Store
	mu     sync.Mutex
	total  time.Duration
	writes int
	active atomic.Int32
}

func (s *txRecorder) Update(ctx context.Context, fn func(store.Tx) error) error {
	start := time.Now()
	err := s.Store.Update(ctx, func(tx store.Tx) error {
		s.active.Add(1)
		defer s.active.Add(-1)
		return fn(tx)
	})
	d := time.Since(start)
	s.mu.Lock()
	s.total += d
	s.writes++
	s.mu.Unlock()
	return err
}

func (s *txRecorder) View(ctx context.Context, fn func(store.Reader) error) error {
	return s.Store.View(ctx, fn)
}

func (s *txRecorder) Close() error { return s.Store.Close() }

func (s *txRecorder) totals() (time.Duration, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total, s.writes
}

// TestR17RecomputeRunsOutsideTheSettlementTransaction is the red test of WO R17 item 2
// (A1). Before the change the Ed25519 envelope verify and the native recompute ran
// inside the settlement transaction. Observe the exact transaction callback boundary
// at each actual verifier invocation instead of comparing noisy wall-clock timings.
// Keep the timings as diagnostic measurements, not a proxy for control flow.
func TestR17RecomputeRunsOutsideTheSettlementTransaction(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(t.TempDir() + "/pool.db")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rec := &txRecorder{Store: db}
	engine, err := New(rec, slog.New(slog.NewJSONHandler(io.Discard, nil)), "s1", time.Minute)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := &harness{engine: engine, now: time.Unix(1_700_000_000, 0).UTC()}
	engine.Now = func() time.Time { return h.now }
	engine.Verifier.SetClock(func() time.Time { return h.now })
	engine.SetWorkerPool(2)
	h.disableRerun(t)
	// Keep the original assertion meaningful: opt in to the registered
	// reference so four settlements really exercise the native kernel.
	h.enableRecompute(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})

	// Validate the instrumentation itself before using it to observe verifier calls.
	if err := rec.Update(ctx, func(store.Tx) error {
		if rec.active.Load() != 1 {
			return errors.New("transaction callback not detected")
		}
		return nil
	}); err != nil || rec.active.Load() != 0 {
		t.Fatalf("transaction boundary probe: %v active=%d", err, rec.active.Load())
	}
	reg := recompute.New()
	ref := matmulref.Reference()
	native := ref.Verifier
	var calls, inside atomic.Int32
	ref.Verifier = func(input json.RawMessage) (recompute.Answer, error) {
		calls.Add(1)
		if rec.active.Load() != 0 {
			inside.Add(1)
		}
		return native(input)
	}
	if err := reg.Register(ref); err != nil {
		t.Fatal(err)
	}
	engine.SetRecomputeRegistry(reg)

	const dim = 240
	// Record native and transaction timing for diagnosis only.
	kernelStart := time.Now()
	if _, ok := matmulref.Hash(dim); !ok {
		t.Fatal("the native kernel refused the reference dimension")
	}
	kernel := time.Since(kernelStart)

	job := h.matmulJobTasks(t, 4, dim)
	w := h.worker(t, 4)
	before, _ := rec.totals()
	for settled := 0; settled < 4; settled++ {
		a, err := h.engine.Claim(ctx, Thread, "contributor", w.TabID)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		out := json.RawMessage(`{"n":240,"sha256":"` + matmulRef240 + `"}`)
		res, err := h.engine.Submit(ctx, Thread, "contributor", w.TabID, Submission{
			Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out),
		})
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if !res.RecomputeVerified {
			t.Fatalf("the sample recompute did not run: %+v", res)
		}
	}
	after, _ := rec.totals()
	stored, err := h.engine.Get(ctx, Thread, "developer", job.JobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(stored.Results) != 4 {
		t.Fatalf("results = %d, want 4", len(stored.Results))
	}
	inTx := after - before
	t.Logf("native recompute %s; write transactions of four submissions %s", kernel, inTx)
	if calls.Load() != 4 || inside.Load() != 0 {
		t.Fatalf("native verifier calls=%d, inside settlement/write transaction=%d; want 4 and 0", calls.Load(), inside.Load())
	}
}
