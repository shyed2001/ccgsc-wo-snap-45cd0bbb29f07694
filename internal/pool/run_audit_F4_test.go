package pool

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ccgsc/poolscore/internal/store"
)

func TestRunAuditF4BindsCodeInputAndResult(t *testing.T) {
	h := newChainHarness(t)
	j := h.job(t, "developer", 1, `{"tamperEvidence":{"mode":"per-run-chain"}}`)
	if j.ChainEntries != 2 || j.ChainHead == "" {
		t.Fatalf("initial code/input chain not published: %+v", j)
	}
	h.settle(t, j, "worker")
	var stored Job
	if err := h.engine.Store.View(context.Background(), func(r store.Reader) error { return r.Get(store.Jobs, j.JobID, &stored) }); err != nil {
		t.Fatal(err)
	}
	if stored.ChainEntries != 3 || stored.ChainHead == j.ChainHead {
		t.Fatalf("result audit not committed: %+v", stored)
	}
	report, err := h.engine.JobChain(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Verification.Valid || len(report.RunEntries) != 3 {
		t.Fatalf("invalid complete chain: %+v", report)
	}
	for i, kind := range []string{"module", "input", "result"} {
		if report.RunEntries[i].Kind != kind {
			t.Fatalf("entry %d = %+v", i, report.RunEntries[i])
		}
	}
	changed := append([]RunAuditEntry(nil), report.RunEntries...)
	changed[1].Digest = strings.Repeat("0", 64)
	if VerifyRunAudit(changed, stored).Valid {
		t.Fatal("changed input digest verified")
	}
	if VerifyRunAudit(report.RunEntries[:2], stored).Valid {
		t.Fatal("truncated chain verified")
	}
}

type f4FailAuditStore struct {
	store.Store
	fail bool
}
type f4FailAuditTx struct{ store.Tx }

func (s *f4FailAuditStore) Update(ctx context.Context, fn func(store.Tx) error) error {
	return s.Store.Update(ctx, func(tx store.Tx) error {
		if s.fail {
			return fn(f4FailAuditTx{tx})
		}
		return fn(tx)
	})
}
func (t f4FailAuditTx) Put(bucket, key string, value any) error {
	if bucket == store.Audit && strings.HasPrefix(key, runAuditEntryPrefix) {
		return errors.New("injected F4 audit append failure")
	}
	return t.Tx.Put(bucket, key, value)
}

// PutIfAbsent forwards the optional atomic-create capability, so wrapped
// transactions still support the F15 once-only credit markers.
func (t f4FailAuditTx) PutIfAbsent(bucket, key string, value any) (bool, error) {
	atomic, ok := t.Tx.(store.PutIfAbsentTx)
	if !ok {
		return false, store.ErrNotImplemented
	}
	return atomic.PutIfAbsent(bucket, key, value)
}

func TestRunAuditF4FailOpen(t *testing.T) {
	h := newChainHarness(t)
	j := h.job(t, "developer", 1, `{"tamperEvidence":{"mode":"per-run-chain"}}`)
	wrapped := &f4FailAuditStore{Store: h.engine.Store, fail: true}
	h.engine.Store = wrapped
	result := h.settle(t, j, "worker")
	if result.Status != "done" {
		t.Fatalf("result not accepted: %+v", result)
	}
	var stored Job
	if err := wrapped.View(context.Background(), func(r store.Reader) error { return r.Get(store.Jobs, j.JobID, &stored) }); err != nil {
		t.Fatal(err)
	}
	if len(stored.Results) != 1 || !stored.AuditGap || !hasRunAuditGapWarning(stored.Warnings) {
		t.Fatalf("missing committed result or durable gap: %+v", stored)
	}
	wrapped.fail = false
	report, err := h.engine.JobChain(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Verification.Valid || !report.AuditGap {
		t.Fatalf("gap falsely verified: %+v", report)
	}
	found := false
	for _, entry := range report.RunEntries {
		if entry.Kind == "gap" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing gap marker: %+v", report)
	}
}
