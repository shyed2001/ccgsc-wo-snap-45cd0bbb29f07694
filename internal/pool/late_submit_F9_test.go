package pool

import (
	"ccgsc/poolscore/internal/verify"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func lateSubmission(a Assignment, output string) Submission {
	body := json.RawMessage(output)
	return Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: body, SHA256: verify.SHA256(body)}
}

func TestF9LateResultBeforeReLeaseAccepted(t *testing.T) {
	h := newHarness(t)
	j := h.seed(t, 1, nil)
	w := h.worker(t, 1)
	a, err := h.engine.Claim(context.Background(), Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	h.now = a.Lease.ExpiresAt.Add(time.Second)
	got, err := h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, lateSubmission(a, `{"late":true}`))
	if err != nil {
		t.Fatalf("late submit: %v", err)
	}
	if !got.Late || got.LateAcceptedAt == nil {
		t.Fatalf("result = %+v, want late metadata", got)
	}
	stored, err := h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Results) != 1 || !stored.Tasks[0].ExpiredLease.Consumed {
		t.Fatalf("stored late evidence/results = %+v / %+v", stored.Tasks[0].ExpiredLease, stored.Results)
	}
	if _, err = h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, lateSubmission(a, `{"late":true}`)); !errors.Is(err, ErrConflict) {
		t.Fatalf("late replay = %v, want ErrConflict", err)
	}
}

func TestF9LateResultAfterReLeaseRejected(t *testing.T) {
	h := newHarness(t)
	h.seed(t, 1, nil)
	w1 := h.worker(t, 1)
	a, err := h.engine.Claim(context.Background(), Thread, w1.Owner, w1.TabID)
	if err != nil {
		t.Fatal(err)
	}
	// G-F12-5: one second after the heartbeat lease expires, the Dew receipt
	// still fences the task. Re-assignment waits until that deadline.
	h.now = a.Lease.ExpiresAt.Add(time.Second)
	if err = h.engine.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	wEarly := h.workerAs(t, "contributor-early", 1)
	if _, err = h.engine.Claim(context.Background(), Thread, wEarly.Owner, wEarly.TabID); !errors.Is(err, ErrEmpty) {
		t.Fatalf("claim inside the dew window = %v, want ErrEmpty", err)
	}
	if a.LeaseReceipt == nil {
		t.Fatal("dew default ON did not issue a receipt")
	}
	h.now = time.Unix(a.LeaseReceipt.Deadline, 0).Add(time.Second)
	if err = h.engine.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	w2 := h.workerAs(t, "contributor-2", 1)
	b, err := h.engine.Claim(context.Background(), Thread, w2.Owner, w2.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if b.Lease.Generation != a.Lease.Generation+1 {
		t.Fatalf("generation = %d, want %d", b.Lease.Generation, a.Lease.Generation+1)
	}
	if _, err = h.engine.Submit(context.Background(), Thread, w1.Owner, w1.TabID, lateSubmission(a, `{"stale":true}`)); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale late submit = %v, want ErrConflict", err)
	}
}

func TestF9ExpiredEnvelopeAuthenticity(t *testing.T) {
	h := newHarness(t)
	h.seed(t, 1, nil)
	w := h.worker(t, 1)
	a, err := h.engine.Claim(context.Background(), Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	h.now = a.Lease.ExpiresAt.Add(time.Second)
	tampered := lateSubmission(a, `{"valid":true}`)
	tampered.TaskEnvelope.Signature = append([]byte(nil), tampered.TaskEnvelope.Signature...)
	tampered.TaskEnvelope.Signature[0] ^= 1
	if _, err = h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, tampered); !errors.Is(err, ErrConflict) {
		t.Fatalf("bad envelope = %v, want ErrConflict", err)
	}
}

func TestF9TerminalJobRejected(t *testing.T) {
	h := newHarness(t)
	j := h.seed(t, 1, nil)
	w := h.worker(t, 1)
	a, err := h.engine.Claim(context.Background(), Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	h.now = a.Lease.ExpiresAt.Add(time.Second)
	if _, _, err = h.engine.Cancel(context.Background(), Thread, "developer", j.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err = h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, lateSubmission(a, `{"late":true}`)); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal late submit = %v, want ErrConflict", err)
	}
}
