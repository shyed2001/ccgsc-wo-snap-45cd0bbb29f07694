// Path: internal/pool/reputation_persist_r10_test.go | R10 fix lane (web review of fe646643).
// What/why: the glm MAJOR row against persistReputation, checked for reachability.
// The row claims an unsynchronized Snapshot->Update can let an older snapshot
// overwrite a newer one. The test below forces exactly that order with a store
// barrier instead of relying on goroutine scheduling: the first persist takes its
// snapshot and parks at the store boundary, the newer event is recorded and
// persisted to completion, and only then is the older write released. No mutex is
// held across the store call, so the older transaction really commits last.
//
// Row verdict: the ordering race is real at the persistence boundary (AT22-L3-4
// review: serialising the wrapper hid it). persistReputation now stamps every
// snapshot with a monotonic sequence under the same lock that takes it, and the
// write is skipped when the store already holds a newer sequence. The stored
// record therefore keeps the newer state even when the older transaction commits last.
package pool

import (
	"context"
	"sync"
	"testing"
	"time"

	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
)

// r10RepStore stages the stale commit. Update #1 parks before it touches SQLite;
// Update #2 runs and commits completely; only then does the test release #1.
type r10RepStore struct {
	store.Store
	mu           sync.Mutex
	calls        int
	firstParked  chan struct{}
	releaseFirst chan struct{}
	secondDone   chan struct{}
}

func newR10RepStore(inner store.Store) *r10RepStore {
	return &r10RepStore{
		Store:        inner,
		firstParked:  make(chan struct{}),
		releaseFirst: make(chan struct{}),
		secondDone:   make(chan struct{}),
	}
}

func (s *r10RepStore) View(ctx context.Context, fn func(store.Reader) error) error {
	return s.Store.View(ctx, fn)
}

func (s *r10RepStore) Close() error { return s.Store.Close() }

func (s *r10RepStore) Update(ctx context.Context, fn func(store.Tx) error) error {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.mu.Unlock()
	switch n {
	case 1:
		// persistReputation already took the older snapshot when it reaches the
		// store; hold the write until the newer one has committed.
		close(s.firstParked)
		<-s.releaseFirst
	case 2:
		err := s.Store.Update(ctx, fn)
		close(s.secondDone)
		return err
	}
	return s.Store.Update(ctx, fn)
}

// TestR10PersistReputationNeverOverwritesANewerSnapshot checks the glm MAJOR row.
func TestR10PersistReputationNeverOverwritesANewerSnapshot(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	inner := h.engine.Store
	rep := newR10RepStore(inner)
	h.engine.Store = rep

	// Persist #1 snapshots the clean state and is parked before its write.
	first := make(chan struct{})
	go func() {
		h.engine.persistReputation()
		close(first)
	}()
	<-rep.firstParked

	// A newer pass lands and is persisted while the old write is still parked;
	// the test waits until that newer transaction has actually committed.
	if _, err := h.engine.Verifier.Reputation().Observe("worker-r10", verify.EventPass, "", ""); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	second := make(chan struct{})
	go func() {
		h.engine.persistReputation()
		close(second)
	}()
	select {
	case <-rep.secondDone:
	case <-first:
		t.Fatal("the older write ran before the newer one committed")
	}

	// Release the old write; it commits last and must not undo the newer state.
	close(rep.releaseFirst)
	<-first
	<-second

	var stored verify.ReputationSnapshot
	if err := inner.View(ctx, func(r store.Reader) error { return r.Get(store.Metrics, "reputation", &stored) }); err != nil {
		t.Fatalf("read the stored reputation: %v", err)
	}
	var passes int
	for _, w := range stored.Workers {
		if w.WorkerID == "worker-r10" {
			passes = w.Passes
		}
	}
	if passes != 1 {
		t.Fatalf("stored passes = %d, want 1 (an older snapshot overwrote the newer one)", passes)
	}
}

// TestR10PersistReputationSequenceSurvivesRestart guards the other half of the
// sequence guard: a fresh engine continues at the stored sequence instead of
// starting over, or every write after a restart would be skipped as stale.
func TestR10PersistReputationSequenceSurvivesRestart(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	inner := h.engine.Store
	h.engine.persistReputation()
	h.engine.persistReputation()

	restarted, err := New(inner, h.engine.Log, "s2", time.Minute)
	if err != nil {
		t.Fatalf("restart engine: %v", err)
	}
	if _, err := restarted.Verifier.Reputation().Observe("worker-restart", verify.EventPass, "", ""); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	restarted.persistReputation()

	var stored verify.ReputationSnapshot
	if err := inner.View(ctx, func(r store.Reader) error { return r.Get(store.Metrics, "reputation", &stored) }); err != nil {
		t.Fatalf("read the stored reputation: %v", err)
	}
	var passes int
	for _, w := range stored.Workers {
		if w.WorkerID == "worker-restart" {
			passes = w.Passes
		}
	}
	if passes != 1 {
		t.Fatalf("stored passes = %d, want 1 (the restarted engine skipped its write as stale)", passes)
	}
}
