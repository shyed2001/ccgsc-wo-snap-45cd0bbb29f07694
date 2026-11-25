// Path: internal/pool/reputation_persist_W9D_test.go | Author: Claude Opus 5.5 | 2026-11-21
// What/why: W9d rows 2 and 3 (GLM review) - a recovery/ban/reseed decided by the
// reputation gate must be durable on every path: when Claim/Submit also carry
// observations (observeCommitted persists them on success), when the Claim/Submit
// transaction fails after generating observations, and through the eligible()
// wrapper whose callers cannot see the changed flag.
package pool

import (
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/sqlite"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ccgsc/poolscore/internal/verify"
)

var errW9DJobsPut = errors.New("w9d: injected jobs put failure")

// failJobsPutStore fails the first Put to the Jobs bucket of one armed Update.
// The job save is the last write of Claim/Submit, so the callback has already
// generated its observations and the gate has already recovered the worker when
// the transaction fails.
type failJobsPutStore struct {
	store.Store
	mu    sync.Mutex
	armed bool
}

func (s *failJobsPutStore) arm() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.armed = true
}

func (s *failJobsPutStore) Update(ctx context.Context, fn func(store.Tx) error) error {
	s.mu.Lock()
	armed := s.armed
	s.armed = false
	s.mu.Unlock()
	if !armed {
		return s.Store.Update(ctx, fn)
	}
	return s.Store.Update(ctx, func(tx store.Tx) error {
		return fn(failJobsPutTx{Tx: tx})
	})
}

type failJobsPutTx struct{ store.Tx }

func (t failJobsPutTx) Put(bucket, key string, value any) error {
	if bucket == store.Jobs {
		return errW9DJobsPut
	}
	return t.Tx.Put(bucket, key, value)
}

func w9dHarnessOn(t *testing.T, s store.Store, ttl time.Duration) *harness {
	t.Helper()
	engine, err := New(s, slog.New(slog.NewJSONHandler(io.Discard, nil)), "s1", ttl)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := &harness{engine: engine, now: time.Unix(1_700_000_000, 0).UTC()}
	engine.Now = func() time.Time { return h.now }
	engine.Verifier.SetClock(func() time.Time { return h.now })
	return h
}

func w9dRestart(t *testing.T, h *harness, id string) *Engine {
	t.Helper()
	engine, err := New(h.engine.Store, h.engine.Log, id, h.engine.TTL)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	engine.Now = func() time.Time { return h.now }
	engine.Verifier.SetClock(func() time.Time { return h.now })
	return engine
}

func w9dWorkerRep(t *testing.T, engine *Engine, tabID string) verify.WorkerReputation {
	t.Helper()
	rec, err := engine.Verifier.Reputation().Worker(tabID)
	if err != nil {
		t.Fatalf("reputation record: %v", err)
	}
	return rec
}

// TestW9DClaimRecoveryWithObservationsPersists answers the row 2 question: when
// Claim decides a recovery and also commits observations, observeCommitted does
// persist the tracker. The success path was already correct and stays covered.
func TestW9DClaimRecoveryWithObservationsPersists(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	zero := 0.0
	h.seed(t, 1, &zero)
	w := h.worker(t, 1)
	if _, err := h.engine.Claim(ctx, Thread, "contributor", w.TabID); err != nil {
		t.Fatalf("Claim first: %v", err)
	}
	h.engine.Verifier.Reputation().Seed(w.TabID, 0.4)
	// G-F12-5: the timeout observation is written when the Dew receipt expires
	// and the task is requeued, not at the 1 minute heartbeat lease. The nil
	// heartbeat refreshes the tab without renewing that lease.
	h.now = h.now.Add(11 * time.Minute)
	if _, err := h.engine.Heartbeat(ctx, Thread, "contributor", w.TabID, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := h.engine.Claim(ctx, Thread, "contributor", w.TabID); err != nil {
		t.Fatalf("Claim after the quiet window: %v", err)
	}
	inMemory := w9dWorkerRep(t, h.engine, w.TabID)
	if inMemory.Recoveries != 1 || inMemory.Timeouts != 1 {
		t.Fatalf("in-memory record = %+v, want Recoveries 1 and the committed timeout", inMemory)
	}
	after := w9dWorkerRep(t, w9dRestart(t, h, "s2"), w.TabID)
	if after.Recoveries != 1 || after.Timeouts != 1 {
		t.Fatalf("restored record = %+v, want the recovery and the observation persisted", after)
	}
}

// TestW9DClaimRecoverySurvivesFailedJobWrite is the row 2 defect: with
// observations present the old code skipped the explicit persist and never ran
// observeCommitted because the transaction failed, so a recovery decided on that
// claim was lost on restart.
func TestW9DClaimRecoverySurvivesFailedJobWrite(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	wrapped := &failJobsPutStore{Store: db}
	h := w9dHarnessOn(t, wrapped, time.Minute)
	ctx := context.Background()
	zero := 0.0
	h.seed(t, 1, &zero)
	w := h.worker(t, 1)
	if _, err := h.engine.Claim(ctx, Thread, "contributor", w.TabID); err != nil {
		t.Fatalf("Claim first: %v", err)
	}
	h.engine.Verifier.Reputation().Seed(w.TabID, 0.4)
	// Same Dew fence as the success path: the job write this test injects a
	// failure into is the requeue at receipt expiry.
	h.now = h.now.Add(11 * time.Minute)
	if _, err := h.engine.Heartbeat(ctx, Thread, "contributor", w.TabID, nil); err != nil {
		t.Fatal(err)
	}

	wrapped.arm()
	if _, err := h.engine.Claim(ctx, Thread, "contributor", w.TabID); !errors.Is(err, errW9DJobsPut) {
		t.Fatalf("Claim err = %v, want the injected job write failure", err)
	}
	if inMemory := w9dWorkerRep(t, h.engine, w.TabID); inMemory.Recoveries != 1 {
		t.Fatalf("in-memory record = %+v, want the recovery decided by the gate", inMemory)
	}
	after := w9dWorkerRep(t, w9dRestart(t, h, "s2"), w.TabID)
	if after.Recoveries != 1 {
		t.Fatalf("restored record = %+v, want the recovery persisted after the failed claim", after)
	}
}

// TestW9DSubmitRecoveryWithObservationsPersists is the success-path Submit half
// of row 2: observeCommitted persists the tracker when it commits the pass.
func TestW9DSubmitRecoveryWithObservationsPersists(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h := w9dHarnessOn(t, db, time.Hour)
	ctx := context.Background()
	zero := 0.0
	h.seed(t, 1, &zero)
	w := h.worker(t, 1)
	a, err := h.engine.Claim(ctx, Thread, "contributor", w.TabID)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	h.engine.Verifier.Reputation().Seed(w.TabID, 0.4)
	h.now = h.now.Add(16 * time.Minute)

	out := json.RawMessage(`{"json":{"answer":42}}`)
	if _, err := h.engine.Submit(ctx, Thread, "contributor", w.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt,
		Output: out, SHA256: verify.SHA256(out),
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if inMemory := w9dWorkerRep(t, h.engine, w.TabID); inMemory.Recoveries != 1 {
		t.Fatalf("in-memory record = %+v, want the recovery", inMemory)
	}
	after := w9dWorkerRep(t, w9dRestart(t, h, "s2"), w.TabID)
	if after.Recoveries != 1 || after.Passes != 1 {
		t.Fatalf("restored record = %+v, want the recovery and the pass persisted", after)
	}
}

// TestW9DSubmitRecoverySurvivesFailedJobWrite is the Submit half of the row 2
// defect: a failed transaction after observations were generated dropped the
// recovery because neither persist branch ran.
func TestW9DSubmitRecoverySurvivesFailedJobWrite(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	wrapped := &failJobsPutStore{Store: db}
	h := w9dHarnessOn(t, wrapped, time.Hour)
	ctx := context.Background()
	zero := 0.0
	h.seed(t, 1, &zero)
	w := h.worker(t, 1)
	a, err := h.engine.Claim(ctx, Thread, "contributor", w.TabID)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	h.engine.Verifier.Reputation().Seed(w.TabID, 0.4)
	h.now = h.now.Add(16 * time.Minute)

	wrapped.arm()
	if _, err := h.engine.Submit(ctx, Thread, "contributor", w.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt,
		Error: "worker exploded",
	}); !errors.Is(err, errW9DJobsPut) {
		t.Fatalf("Submit err = %v, want the injected job write failure", err)
	}
	if inMemory := w9dWorkerRep(t, h.engine, w.TabID); inMemory.Recoveries != 1 {
		t.Fatalf("in-memory record = %+v, want the recovery decided by the gate", inMemory)
	}
	after := w9dWorkerRep(t, w9dRestart(t, h, "s2"), w.TabID)
	if after.Recoveries != 1 {
		t.Fatalf("restored record = %+v, want the recovery persisted after the failed submit", after)
	}
}

// TestW9DEligiblePersistsRecovery is row 3: the eligible() wrapper discards the
// changed flag, so a recovery it decides was never written to the store.
func TestW9DEligiblePersistsRecovery(t *testing.T) {
	h := newHarness(t)
	w := h.worker(t, 1)
	h.engine.Verifier.Reputation().Seed(w.TabID, 0.4)

	ok, st := h.engine.eligible(w.TabID)
	if !ok || st != verify.StatusWarn {
		t.Fatalf("eligible = %v / %s, want a recovered warn worker", ok, st)
	}
	if inMemory := w9dWorkerRep(t, h.engine, w.TabID); inMemory.Recoveries != 1 {
		t.Fatalf("in-memory record = %+v, want the recovery", inMemory)
	}
	after := w9dWorkerRep(t, w9dRestart(t, h, "s2"), w.TabID)
	if after.Recoveries != 1 {
		t.Fatalf("restored record = %+v, want the eligible() recovery persisted", after)
	}
}
