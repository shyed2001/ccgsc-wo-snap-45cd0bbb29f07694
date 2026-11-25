// Path: internal/pool/expire_noop_A3a_R17_test.go | Author: CommandCode for the owner | 2026-11-22
// What/why: WO R17-SCALE item 1 (A3a) - e.expire must not call e.save when nothing
// expired on the job. Every Claim and every Sweep walks every job and called
// e.save unconditionally, so a Claim with no expiry rewrote every job row of the
// pool (the whole-Job JSON rewrite of R16 A3) for no state change at all.
package pool

import (
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/sqlite"
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// jobWrites counts the Put(store.Jobs) calls of the store, so the test can see
// whether a no-op Claim rewrote a job row.
type jobWriteCounter struct {
	store.Store
	mu     sync.Mutex
	writes map[string]int
}

func (s *jobWriteCounter) Update(ctx context.Context, fn func(store.Tx) error) error {
	return s.Store.Update(ctx, func(tx store.Tx) error {
		return fn(&jobWriteTx{Tx: tx, parent: s})
	})
}

func (s *jobWriteCounter) View(ctx context.Context, fn func(store.Reader) error) error {
	return s.Store.View(ctx, fn)
}

func (s *jobWriteCounter) Close() error { return s.Store.Close() }

func (s *jobWriteCounter) count(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes[key]
}

func (s *jobWriteCounter) writtenKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.writes))
	for key := range s.writes {
		out = append(out, key)
	}
	return out
}

type jobWriteTx struct {
	store.Tx
	parent *jobWriteCounter
}

func (t *jobWriteTx) Put(bucket, key string, value any) error {
	if bucket == store.Jobs {
		t.parent.mu.Lock()
		if t.parent.writes == nil {
			t.parent.writes = map[string]int{}
		}
		t.parent.writes[key]++
		t.parent.mu.Unlock()
	}
	return t.Tx.Put(bucket, key, value)
}

func (t *jobWriteTx) Delete(bucket, key string) error {
	deleter, ok := t.Tx.(store.DeleteTx)
	if !ok {
		return store.ErrNotImplemented
	}
	return deleter.Delete(bucket, key)
}

func (t *jobWriteTx) PutIfAbsent(bucket, key string, value any) (bool, error) {
	atomic, ok := t.Tx.(store.PutIfAbsentTx)
	if !ok {
		return false, store.ErrNotImplemented
	}
	return atomic.PutIfAbsent(bucket, key, value)
}

// newCountingHarness is newHarness with the job write counter around the store.
func newCountingHarness(t *testing.T) (*harness, *jobWriteCounter) {
	t.Helper()
	db, e := sqlite.Open(filepath.Join(t.TempDir(), "pool.db"))
	if e != nil {
		t.Fatalf("sqlite.Open: %v", e)
	}
	t.Cleanup(func() { _ = db.Close() })
	counted := &jobWriteCounter{Store: db, writes: map[string]int{}}
	l := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine, e := New(counted, l, "s1", time.Minute)
	if e != nil {
		t.Fatalf("New: %v", e)
	}
	h := &harness{engine: engine, now: time.Unix(1_700_000_000, 0).UTC()}
	engine.Now = func() time.Time { return h.now }
	engine.Verifier.SetClock(func() time.Time { return h.now })
	return h, counted
}

// TestA3aClaimWithoutExpiryWritesNoJobRows is the red test of WO R17 item 1: a
// Claim that takes a lease and expires nothing must not rewrite any job row of
// the pool. The first claim of the pool writes its own job (the lease), which is
// a real change; the second claim of the same worker, which merely counts its
// active leases and finds no free slot, must write nothing.
func TestA3aClaimWithoutExpiryWritesNoJobRows(t *testing.T) {
	h, counted := newCountingHarness(t)
	j := h.seed(t, 1, nil)
	wk := h.worker(t, 1)
	ctx := context.Background()

	first, e := h.engine.Claim(ctx, Thread, "contributor", wk.TabID)
	if e != nil {
		t.Fatalf("first Claim: %v", e)
	}
	if first.Lease.JobID != j.JobID {
		t.Fatalf("first claim leased job %q, want %q", first.Lease.JobID, j.JobID)
	}
	before := counted.count(j.JobID)
	if before == 0 {
		t.Fatal("the claim that took the lease did not write the leased job")
	}

	// The worker holds its only slot: this claim leases nothing and expires
	// nothing either (the lease is live), so it must write nothing. A full tab
	// that cannot lease is ErrEmpty, never an empty assignment.
	if _, e = h.engine.Claim(ctx, Thread, "contributor", wk.TabID); !errors.Is(e, ErrEmpty) {
		t.Fatalf("second Claim = %v, want ErrEmpty for a full worker", e)
	}
	if after := counted.count(j.JobID); after != before {
		t.Fatalf("a Claim that expired nothing wrote %d extra job rows (job %s): written=%v", after-before, j.JobID, counted.writtenKeys())
	}

	// The job itself is still exactly what it was: the lease is live and the
	// task is leased.
	var stored Job
	if e := h.engine.Store.View(ctx, func(r store.Reader) error { return r.Get(store.Jobs, j.JobID, &stored) }); e != nil {
		t.Fatal(e)
	}
	if stored.State != "running" || stored.Tasks[0].Lease == nil || stored.Tasks[0].State != "leased" {
		t.Fatalf("stored job = state %q task %q lease %v, want the live lease untouched", stored.State, stored.Tasks[0].State, stored.Tasks[0].Lease != nil)
	}
}

// TestA3aSweepWithoutExpiryWritesNoJobRows pins the same rule on the Sweep path:
// a sweep that expires nothing rewrites no job row.
func TestA3aSweepWithoutExpiryWritesNoJobRows(t *testing.T) {
	h, counted := newCountingHarness(t)
	h.seed(t, 1, nil)
	ctx := context.Background()
	wk := h.worker(t, 1)
	if _, e := h.engine.Claim(ctx, Thread, "contributor", wk.TabID); e != nil {
		t.Fatalf("Claim: %v", e)
	}
	total := 0
	for _, key := range counted.writtenKeys() {
		total += counted.count(key)
	}
	if e := h.engine.Sweep(ctx); e != nil {
		t.Fatalf("Sweep: %v", e)
	}
	after := 0
	for _, key := range counted.writtenKeys() {
		after += counted.count(key)
	}
	if after != total {
		t.Fatalf("a Sweep that expired nothing wrote %d extra job rows", after-total)
	}
}

// TestA3aExpiredLeaseStillRewritesTheJob pins the other half: the skip is only
// for a no-op expire. A claim whose walk really expires a lease must persist the
// expired task (requeue, generation and the expired-lease evidence).
func TestA3aExpiredLeaseStillRewritesTheJob(t *testing.T) {
	h, counted := newCountingHarness(t)
	j := h.seed(t, 1, nil)
	ctx := context.Background()
	wk := h.worker(t, 1)
	first, e := h.engine.Claim(ctx, Thread, "contributor", wk.TabID)
	if e != nil {
		t.Fatalf("Claim: %v", e)
	}
	before := counted.count(j.JobID)
	generation := first.Lease.Generation
	// G-F12-5: a lease is not given up at the heartbeat TTL. The rewrite this
	// test pins happens when the Dew receipt expires.
	h.now = h.now.Add(11 * time.Minute)
	if e := h.engine.Sweep(ctx); e != nil {
		t.Fatalf("Sweep after expiry: %v", e)
	}
	if after := counted.count(j.JobID); after <= before {
		t.Fatal("the sweep that expired a lease did not persist the job")
	}
	var stored Job
	if e := h.engine.Store.View(ctx, func(r store.Reader) error { return r.Get(store.Jobs, j.JobID, &stored) }); e != nil {
		t.Fatal(e)
	}
	if stored.Tasks[0].Lease != nil {
		t.Fatalf("the expired lease survived the sweep: %+v", stored.Tasks[0].Lease)
	}
	if stored.Tasks[0].State != "queued" {
		t.Fatalf("task state = %q after the expiry, want queued for the retry", stored.Tasks[0].State)
	}
	ev := stored.Tasks[0].ExpiredLease
	if ev == nil || ev.Lease.Generation != generation || ev.Lease.Token != first.Lease.Token {
		t.Fatalf("expired evidence = %+v, want the expired lease of generation %d", ev, generation)
	}
}
