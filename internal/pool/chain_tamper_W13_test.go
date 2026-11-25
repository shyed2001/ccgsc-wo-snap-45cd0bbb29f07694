// Path: internal/pool/chain_tamper_W13_test.go
// R13 WS2 row 62 (Qwen web reply, 2026-11-21): tamper and replay tests for the audit chain.
package pool

import (
	"context"
	"encoding/json"
	"testing"

	"ccgsc/poolscore/internal/store"
)

func TestTamper_ChangedPayload(t *testing.T) {
	h := newChainHarness(t)
	h.chainEnabled(t)
	j := h.job(t, "developer", 1, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	h.settle(t, j, "contributor")
	if err := h.engine.Store.Update(context.Background(), func(tx store.Tx) error {
		var job Job
		if err := tx.Get(store.Jobs, j.JobID, &job); err != nil {
			return err
		}
		job.Results[0].Output = json.RawMessage(`{"json":{"answer":99}}`)
		return tx.Put(store.Jobs, j.JobID, job)
	}); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	report, err := h.engine.JobChain(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("JobChain: %v", err)
	}
	if report.Verification.Valid {
		t.Fatalf("changed payload verified: %+v", report.Verification)
	}
}

func TestTamper_DeletedRecord(t *testing.T) {
	h := newChainHarness(t)
	h.chainEnabled(t)
	j := h.job(t, "developer", 2, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	h.settle(t, j, "contributor")
	h.settle(t, j, "contributor")
	if err := h.engine.Store.Update(context.Background(), func(tx store.Tx) error {
		var job Job
		if err := tx.Get(store.Jobs, j.JobID, &job); err != nil {
			return err
		}
		job.Results = append(job.Results[:0], job.Results[1:]...)
		return tx.Put(store.Jobs, j.JobID, job)
	}); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	report, err := h.engine.JobChain(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("JobChain: %v", err)
	}
	if report.Verification.Valid {
		t.Fatalf("deleted record verified: %+v", report.Verification)
	}
}

func TestTamper_SwappedOrder(t *testing.T) {
	h := newChainHarness(t)
	h.chainEnabled(t)
	j := h.job(t, "developer", 2, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	h.settle(t, j, "contributor")
	h.settle(t, j, "contributor")
	if err := h.engine.Store.Update(context.Background(), func(tx store.Tx) error {
		var job Job
		if err := tx.Get(store.Jobs, j.JobID, &job); err != nil {
			return err
		}
		job.Results[0], job.Results[1] = job.Results[1], job.Results[0]
		return tx.Put(store.Jobs, j.JobID, job)
	}); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	report, err := h.engine.JobChain(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("JobChain: %v", err)
	}
	if report.Verification.Valid {
		t.Fatalf("swapped order verified: %+v", report.Verification)
	}
}

func TestTamper_ReplayedRecord(t *testing.T) {
	h := newChainHarness(t)
	h.chainEnabled(t)
	j := h.job(t, "developer", 2, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	h.settle(t, j, "contributor")
	h.settle(t, j, "contributor")
	report, err := h.engine.JobChain(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("JobChain: %v", err)
	}
	entries := append([]ChainEntry{}, report.Entries...)
	entries = append(entries, entries[0])
	if v := VerifyChain(entries, nil, report.Head); v.Valid {
		t.Fatalf("replayed record verified: %+v", v)
	}
}

func TestTamper_TruncatedTail(t *testing.T) {
	h := newChainHarness(t)
	h.chainEnabled(t)
	j := h.job(t, "developer", 2, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	h.settle(t, j, "contributor")
	h.settle(t, j, "contributor")
	report, err := h.engine.JobChain(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("JobChain: %v", err)
	}
	entries := report.Entries[:len(report.Entries)-1]
	if v := VerifyChain(entries, nil, report.Head); v.Valid {
		t.Fatalf("truncated tail verified: %+v", v)
	}
}

func TestTamper_ForgedMerkleRoot(t *testing.T) {
	h := newChainHarness(t)
	h.merkleEnabled(t)
	j := h.job(t, "developer", 1, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	h.settle(t, j, "contributor")
	cp, err := h.engine.ForceCheckpoint(context.Background())
	if err != nil {
		t.Fatalf("ForceCheckpoint: %v", err)
	}
	if err := h.engine.Store.Update(context.Background(), func(tx store.Tx) error {
		cp.Root = "0000000000000000000000000000000000000000000000000000000000000000"
		return tx.Put(store.Audit, merkleEntryKey(cp.Seq), cp)
	}); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	v, err := h.engine.VerifyMerkleCheckpoint(context.Background(), cp.Seq)
	if err != nil {
		t.Fatalf("VerifyMerkleCheckpoint: %v", err)
	}
	if v.Valid {
		t.Fatalf("forged merkle root verified: %+v", v)
	}
}

func TestTamper_ForgedMerklePrefix(t *testing.T) {
	h := newChainHarness(t)
	h.merkleEnabled(t)
	j := h.job(t, "developer", 1, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	h.settle(t, j, "contributor")
	if _, err := h.engine.ForceCheckpoint(context.Background()); err != nil {
		t.Fatalf("ForceCheckpoint 1: %v", err)
	}
	h.now = h.now.Add(2 * h.engine.ChainConfig().MerkleInterval)
	cp2, err := h.engine.ForceCheckpoint(context.Background())
	if err != nil {
		t.Fatalf("ForceCheckpoint 2: %v", err)
	}
	if err := h.engine.Store.Update(context.Background(), func(tx store.Tx) error {
		cp2.PrevHash = "0000000000000000000000000000000000000000000000000000000000000000"
		return tx.Put(store.Audit, merkleEntryKey(cp2.Seq), cp2)
	}); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	v, err := h.engine.VerifyMerkleCheckpoint(context.Background(), cp2.Seq)
	if err != nil {
		t.Fatalf("VerifyMerkleCheckpoint: %v", err)
	}
	if v.Valid {
		t.Fatalf("forged merkle prefix verified: %+v", v)
	}
}

func TestTamper_EmptyChain(t *testing.T) {
	if v := VerifyChain(nil, nil, "0000000000000000000000000000000000000000000000000000000000000000"); v.Valid {
		t.Fatalf("empty chain with forged head verified: %+v", v)
	}
}

func TestTamper_OneRecordChain(t *testing.T) {
	h := newChainHarness(t)
	h.chainEnabled(t)
	j := h.job(t, "developer", 1, `{"tamperEvidence":{"mode":"per-job-chain"}}`)
	h.settle(t, j, "contributor")
	report, err := h.engine.JobChain(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("JobChain: %v", err)
	}
	if !report.Verification.Valid {
		t.Fatalf("one-record chain did not verify: %+v", report.Verification)
	}
	entries := append([]ChainEntry{}, report.Entries...)
	entries[0].Prev = "0000000000000000000000000000000000000000000000000000000000000000"
	if v := VerifyChain(entries, nil, report.Head); v.Valid {
		t.Fatalf("one-record chain with forged prev verified: %+v", v)
	}
}
