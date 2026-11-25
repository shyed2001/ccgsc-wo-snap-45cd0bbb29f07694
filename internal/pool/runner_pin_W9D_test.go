// Path: internal/pool/runner_pin_W9D_test.go | Author: Claude Opus 5.5 | 2026-11-21
// What/why: W9d row 1 (GLM review) - the refund pin must carry the sha256 of the
// runner.html the server actually serves (the web embed); a pin with a foreign
// sha, or a bare key id without the page hash, is a lie case.
package pool

import (
	"ccgsc/poolscore/internal/verify"
	"context"
	"testing"
)

// w9dServedRunnerSHA is the sha the page the run origin serves claims for itself
// (D-W10-9): its data-runner-pin attribute, or the exact inline script hash on a
// page built before that attribute existed. The whole-file hash is not the pin.
func w9dServedRunnerSHA(t *testing.T) string {
	t.Helper()
	sha := runnerPagePinSHA()
	if sha == "" {
		t.Fatal("read embedded pages/runner.html: no pin")
	}
	return sha
}

// w9dPin builds the pin of a page that really runs the served runner.html.
func w9dPin(t *testing.T, keyID string) string {
	t.Helper()
	return w9dServedRunnerSHA(t) + ":" + keyID
}

// TestW9DWrongRunnerPageSHAIsPenalty proves the W9d row 1 defect: the refund
// path checked only the key id half, so a page could report a really retired key
// with a made-up runner.html hash and still buy the try back.
func TestW9DWrongRunnerPageSHAIsPenalty(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	zero := 0.0
	j := h.seed(t, 1, &zero)
	oldID := w9RotateKey(t, h)
	foreign := verify.SHA256([]byte("a runner page the server does not serve"))
	if foreign == w9dServedRunnerSHA(t) {
		t.Fatal("test fixture collision: foreign sha equals the served page sha")
	}
	w := w9RegisterPinned(t, h, "contributor", "host-bad-page", foreign+":"+oldID)
	a, err := h.engine.Claim(ctx, Thread, "contributor", w.TabID)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	r, err := h.engine.Submit(ctx, Thread, "contributor", w.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt,
		Error: "verify failed: unknown_operator_key",
	})
	if err != nil {
		t.Fatalf("Submit error report: %v", err)
	}
	if r.Status != "failed" || r.Attempts != 1 {
		t.Fatalf("result = %+v, want a failed try with 1 attempt (a foreign page hash must not refund)", r)
	}
	job, err := h.engine.Get(ctx, Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if job.Tasks[0].Refunds != 0 {
		t.Fatalf("task refunds = %d, want 0 for a foreign page hash", job.Tasks[0].Refunds)
	}
	rec, err := h.engine.Verifier.Reputation().Worker(w.TabID)
	if err != nil || rec.Fails != 1 {
		t.Fatalf("reputation record = %+v, err = %v, want one fail", rec, err)
	}
}

// TestW9DBareRetiredKeyIsPenalty closes the bypass of the check: a pin without
// the page hash cannot prove which runner the tab runs, so it must not refund
// either.
func TestW9DBareRetiredKeyIsPenalty(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	zero := 0.0
	j := h.seed(t, 1, &zero)
	oldID := w9RotateKey(t, h)
	w := w9RegisterPinned(t, h, "contributor", "host-bare", oldID)
	a, err := h.engine.Claim(ctx, Thread, "contributor", w.TabID)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	r, err := h.engine.Submit(ctx, Thread, "contributor", w.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt,
		Error: "verify failed: unknown_operator_key",
	})
	if err != nil {
		t.Fatalf("Submit error report: %v", err)
	}
	if r.Status != "failed" || r.Attempts != 1 {
		t.Fatalf("result = %+v, want a failed try with 1 attempt (a bare key id must not refund)", r)
	}
	job, err := h.engine.Get(ctx, Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if job.Tasks[0].Refunds != 0 {
		t.Fatalf("task refunds = %d, want 0 for a bare key id", job.Tasks[0].Refunds)
	}
	if rec, err := h.engine.Verifier.Reputation().Worker(w.TabID); err != nil || rec.Fails != 1 {
		t.Fatalf("reputation record = %+v, err = %v, want one fail", rec, err)
	}
}

// TestW9DServedPagePinRefundsRetiredKey keeps the honest case: the served page
// hash plus a really retired key id still refunds once. (The W9c retired-pin
// test also covers this with the served hash.)
func TestW9DServedPagePinRefundsRetiredKey(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	zero := 0.0
	j := h.seed(t, 1, &zero)
	oldID := w9RotateKey(t, h)
	w := w9RegisterPinned(t, h, "contributor", "host-honest", w9dPin(t, oldID))
	a, err := h.engine.Claim(ctx, Thread, "contributor", w.TabID)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	r, err := h.engine.Submit(ctx, Thread, "contributor", w.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt,
		Error: "verify failed: unknown_operator_key",
	})
	if err != nil {
		t.Fatalf("Submit error report: %v", err)
	}
	if r.Status != "requeued" || r.Attempts != 0 {
		t.Fatalf("result = %+v, want requeued with the attempt refunded", r)
	}
	job, err := h.engine.Get(ctx, Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if job.Tasks[0].Refunds != 1 {
		t.Fatalf("task refunds = %d, want 1", job.Tasks[0].Refunds)
	}
}
