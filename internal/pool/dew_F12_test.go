package pool

// F12 behaviour tests (owner decisions 2026-11-24, WO section 16): per-task
// run-capped receipts (G-F12-2), receipts that die with the run (G-F12-2),
// first-valid-settles with compute credit only for later valid results and
// every result recorded (G-F12-4), and refusal evidence that keeps no bearer
// capability.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
)

// G-F12-2: one signed receipt per leased task. Every receipt covers exactly
// its own task, is bound to the lease it was issued with, and carries the
// admin window of the moment - here the 600 s default caps the job's 900 s
// request, because each layer can only shorten (G-F12-1).
func TestF12PerTaskReceipts(t *testing.T) {
	_, _, a := dewAssignment(t)
	if len(a.Batch) != 2 {
		t.Fatalf("batch = %d members, want 2", len(a.Batch))
	}
	seen := map[string]bool{}
	for _, m := range a.Batch {
		r := m.LeaseReceipt
		if r == nil {
			t.Fatalf("task %s leased without a receipt", m.Lease.TaskID)
		}
		if r.Version != verify.TaskReceiptVersion {
			t.Fatalf("task %s: receipt version = %d, want %d", m.Lease.TaskID, r.Version, verify.TaskReceiptVersion)
		}
		if len(r.TaskIDs) != 1 || r.TaskIDs[0] != m.Lease.TaskID {
			t.Fatalf("task %s: receipt tasks = %v", m.Lease.TaskID, r.TaskIDs)
		}
		if r.WindowSeconds != DefaultDewReceiptWindowSeconds {
			t.Fatalf("task %s: window = %d, want the admin default %d", m.Lease.TaskID, r.WindowSeconds, DefaultDewReceiptWindowSeconds)
		}
		if r.LeaseTokenSHA256 != verify.SHA256([]byte(m.Lease.Token)) {
			t.Fatalf("task %s: receipt is not bound to its lease token", m.Lease.TaskID)
		}
		if r.EnvelopeSHA256 != dewEnvelopeDigest(m.TaskEnvelope) {
			t.Fatalf("task %s: receipt is not bound to its task envelope", m.Lease.TaskID)
		}
		if r.WorkerID != m.Lease.WorkerID || r.Generation != m.Lease.Generation {
			t.Fatalf("task %s: receipt worker/generation mismatch", m.Lease.TaskID)
		}
		if seen[r.ReceiptID] {
			t.Fatal("two tasks of one run share a receipt id")
		}
		seen[r.ReceiptID] = true
	}
	m := a.Batch[0]
	want := verify.TaskReceiptID(m.Lease.JobID, m.Lease.TaskID, m.Lease.Generation, verify.SHA256([]byte(m.Lease.Token)))
	if m.LeaseReceipt.ReceiptID != want {
		t.Fatalf("receipt id = %s, want derived %s", m.LeaseReceipt.ReceiptID, want)
	}
}

// G-F12-2: the receipt expires at min(window, run end). A still-valid signed
// deadline proves nothing once the run is completed or cancelled.
func TestF12RunEndKillsReceipt(t *testing.T) {
	h, _, a := dewAssignment(t)
	q := lateSubmission(a, `{"ok":true}`)
	q.LeaseReceipt = a.LeaseReceipt
	q.Offline = true
	j, err := h.engine.Get(context.Background(), Thread, "developer", a.Lease.JobID)
	if err != nil {
		t.Fatal(err)
	}
	task := &Task{DewReceipt: true}
	if err := h.engine.verifyDewSubmission(&j, task, q, a.Lease.ExpiresAt); err != nil {
		t.Fatalf("active run refused its own receipt: %v", err)
	}
	j.State = "completed"
	if err := h.engine.verifyDewSubmission(&j, task, q, a.Lease.ExpiresAt); !errors.Is(err, ErrConflict) {
		t.Fatalf("completed run accepted a receipt: %v", err)
	}
	j.State = "cancelled"
	if err := h.engine.verifyDewSubmission(&j, task, q, a.Lease.ExpiresAt); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancelled run accepted a receipt: %v", err)
	}
	// G-F12-6: a failed run ends the receipts too.
	j.State = "failed"
	if err := h.engine.verifyDewSubmission(&j, task, q, a.Lease.ExpiresAt); !errors.Is(err, ErrConflict) {
		t.Fatalf("failed run accepted a receipt: %v", err)
	}
}

// G-F12-4: the first valid result settles the task; a later valid result of
// the same lease inside the receipt window is recorded for the developer and
// earns compute-time credit only. A byte-identical resubmission is a replay,
// and once the run ended the receipt is dead (G-F12-2).
func TestF12DuplicateComputeCreditOnly(t *testing.T) {
	h, w, a := dewAssignment(t)
	ctx := context.Background()
	q := lateSubmission(a, `{"ok":true}`)
	q.LeaseReceipt = a.LeaseReceipt
	q.Offline = true
	if _, err := h.engine.Submit(ctx, Thread, w.Owner, w.TabID, q); err != nil {
		t.Fatal(err)
	}
	j, err := h.engine.Get(ctx, Thread, "developer", a.Lease.JobID)
	if err != nil {
		t.Fatal(err)
	}
	task := &Task{EnvelopeUsed: true}
	dup := q
	dup.Output = json.RawMessage(`{"ok":"a different valid result"}`)
	dup.SHA256 = verify.SHA256(dup.Output)
	dup.ComputeMS = 1 << 40
	var credited int64
	err = h.engine.Store.Update(ctx, func(tx store.Tx) (er error) {
		credited, er = h.engine.submitDewDuplicateTx(tx, &j, task, dup)
		return er
	})
	if err != nil {
		t.Fatal(err)
	}
	// The server bounds the claim by the span the receipt covers, never more
	// than the issued window (G-F12-4).
	if credited < 0 || credited > DefaultDewReceiptWindowSeconds*1000 {
		t.Fatalf("compute credit %d ms outside the receipt window", credited)
	}
	rows, err := h.engine.ListDewResults(ctx, Thread, "developer", a.Lease.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TaskID != a.Lease.TaskID || rows[0].OutputSHA256 != verify.SHA256(dup.Output) {
		t.Fatalf("recorded results = %+v", rows)
	}
	// The byte-identical (lease, output) pair is a replay, not a new result.
	err = h.engine.Store.Update(ctx, func(tx store.Tx) error {
		_, err := h.engine.submitDewDuplicateTx(tx, &j, task, dup)
		return err
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("identical replay accepted: %v", err)
	}
	// Another different result inside the window: recorded and credited again.
	dup2 := dup
	dup2.Output = json.RawMessage(`{"ok":"a third result"}`)
	dup2.SHA256 = verify.SHA256(dup2.Output)
	err = h.engine.Store.Update(ctx, func(tx store.Tx) error {
		_, err := h.engine.submitDewDuplicateTx(tx, &j, task, dup2)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// G-F12-2: when the run ends, every receipt dies - no further result.
	j.State = "completed"
	dup3 := dup
	dup3.Output = json.RawMessage(`{"ok":"too late"}`)
	dup3.SHA256 = verify.SHA256(dup3.Output)
	err = h.engine.Store.Update(ctx, func(tx store.Tx) error {
		_, err := h.engine.submitDewDuplicateTx(tx, &j, task, dup3)
		return err
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("result after run end accepted: %v", err)
	}
	rows, err = h.engine.ListDewResults(ctx, Thread, "developer", a.Lease.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("recorded results = %d, want 2 (the run-end refusal is not a result)", len(rows))
	}
}

// Refusals must retain audit identity without copying bearer capabilities or
// developer-controlled payloads into a second, less restricted data store.
func TestF12DewDeadLetterRedactsSubmission(t *testing.T) {
	for _, replay := range []bool{false, true} {
		name := "receipt refusal"
		if replay {
			name = "replay refusal"
		}
		t.Run(name, func(t *testing.T) {
			h, w, a := dewAssignment(t)
			q := lateSubmission(a, `{"private":"F12-private-result-sentinel"}`)
			q.LeaseReceipt = a.LeaseReceipt
			q.Offline = true
			if replay {
				if _, err := h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, q); err != nil {
					t.Fatal(err)
				}
			} else {
				q.LeaseReceipt.Signature[0] ^= 1
			}
			q.Error = "F12-private-error-sentinel"
			if _, err := h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, q); err == nil {
				t.Fatal("invalid submission accepted")
			}
			err := h.engine.Store.View(context.Background(), func(rd store.Reader) error {
				records, err := rd.List(store.DeadLetters)
				if err != nil {
					return err
				}
				if len(records) != 1 {
					t.Fatalf("dead letters = %d, want 1", len(records))
				}
				for _, rec := range records {
					for _, secret := range []string{
						q.Lease.Token,
						"F12-private-result-sentinel",
						q.Error,
						base64.StdEncoding.EncodeToString(q.TaskEnvelope.Payload),
						base64.StdEncoding.EncodeToString(q.LeaseReceipt.Signature),
					} {
						if strings.Contains(string(rec.Value), secret) {
							t.Error("dead letter contains submission capability or private payload")
						}
					}
					var audit map[string]json.RawMessage
					if err := json.Unmarshal(rec.Value, &audit); err != nil {
						return err
					}
					if _, ok := audit["Submission"]; ok {
						t.Error("dead letter stores full Submission")
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// G-F12-5: Sweep does not re-assign a task while its Dew receipt is open.
// After the deadline the next Sweep frees it for a fresh worker.
func TestF12SweepKeepsTaskUntilDewDeadline(t *testing.T) {
	h, w, a := dewAssignment(t)
	ctx := context.Background()
	h.now = a.Lease.ExpiresAt.Add(time.Second)
	if err := h.engine.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := h.engine.Get(ctx, Thread, "developer", a.Lease.JobID)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range stored.Tasks {
		if task.Lease == nil || task.Dew == nil || !task.Dew.Deadline.After(h.now) {
			t.Fatalf("task %s released inside the dew window: lease=%v dew=%v", task.TaskID, task.Lease != nil, task.Dew)
		}
	}
	// The seed job may still be queued. It may be claimed. The Dew tasks may not.
	w2 := h.workerAs(t, "other", 2)
	if b, err := h.engine.Claim(ctx, Thread, w2.Owner, w2.TabID); err == nil {
		if b.Lease.TaskID == "dew-a" || b.Lease.TaskID == "dew-b" {
			t.Fatalf("re-assigned %s inside the dew window", b.Lease.TaskID)
		}
		if _, err = h.engine.Submit(ctx, Thread, w2.Owner, w2.TabID, lateSubmission(b, `{"seed":true}`)); err != nil {
			t.Fatal(err)
		}
	} else if !errors.Is(err, ErrEmpty) {
		t.Fatal(err)
	}
	q := lateSubmission(a, `{"kept":true}`)
	q.LeaseReceipt = a.LeaseReceipt
	got, err := h.engine.Submit(ctx, Thread, w.Owner, w.TabID, q)
	if err != nil || !got.Late {
		t.Fatalf("late submit inside the window = %+v, %v", got, err)
	}
	h.now = time.Unix(a.LeaseReceipt.Deadline, 0).Add(time.Second)
	if err := h.engine.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	// The original worker's heartbeat is older than two lease TTLs after the
	// jump. The new worker is registered at the new clock.
	w3 := h.workerAs(t, "later", 2)
	b, err := h.engine.Claim(ctx, Thread, w3.Owner, w3.TabID)
	if err != nil {
		t.Fatalf("claim after the dew deadline: %v", err)
	}
	if b.Lease.TaskID == "" {
		t.Fatal("claim after the dew deadline returned no task")
	}
}

// G-F12-4 through Engine.Submit: a later different result of a still-running
// job is recorded and credited, and the task is not settled again. The
// byte-identical original stays a conflict.
func TestF12SubmitDuplicateInsideWindow(t *testing.T) {
	h, w, a := dewAssignment(t)
	ctx := context.Background()
	q := lateSubmission(a, `{"ok":true}`)
	q.LeaseReceipt = a.LeaseReceipt
	if _, err := h.engine.Submit(ctx, Thread, w.Owner, w.TabID, q); err != nil {
		t.Fatal(err)
	}
	dup := q
	dup.Output = json.RawMessage(`{"ok":"later"}`)
	dup.SHA256 = verify.SHA256(dup.Output)
	dup.ComputeMS = 50
	got, err := h.engine.Submit(ctx, Thread, w.Owner, w.TabID, dup)
	if err != nil {
		t.Fatalf("duplicate submit: %v", err)
	}
	if got.Status != "recorded" || got.SHA256 != verify.SHA256(dup.Output) {
		t.Fatalf("duplicate result = %+v, want status recorded", got)
	}
	j, err := h.engine.Get(ctx, Thread, "developer", a.Lease.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if j.State == "completed" || j.State == "failed" || j.State == "cancelled" {
		t.Fatalf("duplicate ended the run: %s", j.State)
	}
	settled := 0
	for _, r := range j.Results {
		if r.TaskID == a.Lease.TaskID {
			settled++
		}
	}
	if settled != 1 {
		t.Fatalf("settled results for the task = %d, want 1", settled)
	}
	rows, err := h.engine.ListDewResults(ctx, Thread, "developer", a.Lease.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].OutputSHA256 != verify.SHA256(dup.Output) {
		t.Fatalf("recorded results = %+v", rows)
	}
	if _, err = h.engine.Submit(ctx, Thread, w.Owner, w.TabID, q); !errors.Is(err, ErrConflict) {
		t.Fatalf("byte-identical original = %v, want ErrConflict", err)
	}
	if _, err = h.engine.Submit(ctx, Thread, w.Owner, w.TabID, dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("byte-identical duplicate = %v, want ErrConflict", err)
	}
}
