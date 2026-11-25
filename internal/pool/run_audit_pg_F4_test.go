package pool

import (
	"context"
	"encoding/json"
	"testing"

	"ccgsc/poolscore/internal/verify"
)

func TestRunAuditF4PGTwoInstancesAndRestart(t *testing.T) {
	h := newPGEngineHarness(t)
	j := h.job(t, 2, 0, 0, `{"tamperEvidence":{"mode":"per-run-chain"}}`)
	if j.ChainEntries != 3 {
		t.Fatalf("initial entries=%d want 3", j.ChainEntries)
	}
	w1 := h.worker(t, Thread, "worker1", 1)
	w2 := h.worker(t, Thread, "worker2", 1)
	a1 := h.claim(t, w1)
	a2 := h.claim(t, w2)
	e1 := h.e
	h.open(t)
	e2 := h.e
	ctx := context.Background()
	out := json.RawMessage(`{ "json": { "answer": 42 } }`)
	done := make(chan error, 2)
	submit := func(e *Engine, w Worker, a Assignment) {
		_, err := e.Submit(ctx, Thread, w.Owner, w.TabID, Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out)})
		done <- err
	}
	go submit(e1, w1, a1)
	go submit(e2, w2, a2)
	for i := 0; i < 2; i++ {
		pgEngineOK(t, "concurrent submit", <-done)
	}
	report, err := e2.JobChain(ctx, Thread, j.Owner, j.JobID)
	pgEngineOK(t, "report", err)
	if !report.Verification.Valid || len(report.RunEntries) != 5 || report.AuditGap {
		t.Fatalf("invalid or duplicated chain: %+v", report)
	}
	for _, entry := range report.RunEntries {
		if entry.Kind == "result" && entry.Digest != verify.SHA256(out) {
			t.Fatalf("result bytes changed: %+v", entry)
		}
	}
	h.restart(t)
	again, err := h.e.JobChain(ctx, Thread, j.Owner, j.JobID)
	pgEngineOK(t, "restart report", err)
	if !again.Verification.Valid || len(again.RunEntries) != 5 || again.Head != report.Head {
		t.Fatalf("chain changed on restart: %+v", again)
	}
}
