// MVPW-F, Codex, 2026-11-19: receipt enforcement on the real SQLite submit path.
package pool

import (
	"ccgsc/poolscore/internal/verify"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestMVPWDewOptionAccepted(t *testing.T) {
	h := newHarness(t)
	q := h.seed(t, 1, nil).Request
	// F12 (G-F12-1): the owner ceiling is 30 minutes, so the retired 3600 s
	// job window would now fail closed; the test uses an in-range window.
	q.Options = json.RawMessage(`{"dew":{"enabled":true,"receiptWindowSeconds":900}}`)
	if _, _, err := h.engine.Validate(q); err != nil {
		t.Fatalf("dew option rejected: %v", err)
	}
}

func dewAssignment(t *testing.T) (*harness, Worker, Assignment) {
	t.Helper()
	h := newHarness(t)
	zero := 0.0
	// AT22-L3-4: the seed job is scaffolding, so its re-run rate is pinned to 0.
	// The keyed 1 % default sampled task-a in about one run of forty and held it
	// pending, which made the next Claim serve that re-run first and left dew-a
	// un-re-leased in the re-leased case below.
	q := h.seed(t, 1, &zero).Request
	q.Tasks = []TaskSpec{{TaskID: "dew-a", Input: json.RawMessage(`{"json":{"n":160}}`)}, {TaskID: "dew-b", Input: json.RawMessage(`{"json":{"n":240}}`)}}
	q.BatchSize = 2
	q.RerunPercent = &zero
	// F12 (G-F12-1): 900 s is inside the 1-30 min owner range; the effective
	// window is min(admin default 600 s, job 900 s) = 600 s.
	q.Options = json.RawMessage(`{"dew":{"enabled":true,"receiptWindowSeconds":900}}`)
	if _, err := h.engine.Create(context.Background(), Thread, "developer", "", q); err != nil {
		t.Fatal(err)
	}
	// Complete the seed job first, so this helper exercises public scheduling.
	// Part E addendum: the seed job now carries a receipt of its own, so the loop
	// has to match the dew task ids instead of taking the first receipt it sees.
	w := h.worker(t, 2)
	for {
		a, err := h.engine.Claim(context.Background(), Thread, w.Owner, w.TabID)
		if err != nil {
			t.Fatal(err)
		}
		if a.Lease.TaskID == "dew-a" && a.LeaseReceipt != nil {
			return h, w, a
		}
		if _, err = h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, lateSubmission(a, `{"seed":true}`)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMVPWDewBatchSubmit(t *testing.T) {
	h, w, a := dewAssignment(t)
	if len(a.Batch) != 2 || len(a.LeaseReceipt.TaskIDs) != 1 {
		t.Fatalf("missing per-task batch receipt: %+v", a)
	}
	h.now = a.Lease.ExpiresAt.Add(time.Second)
	for _, m := range a.Batch {
		q := lateSubmission(m, `{"ok":true}`)
		q.LeaseReceipt = m.LeaseReceipt
		q.Offline = true
		q.PeerRelayed = true
		r, err := h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, q)
		if err != nil || !r.Late {
			t.Fatalf("late dew = %+v, %v", r, err)
		}
		if _, err = h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, q); err == nil {
			t.Fatal("replay accepted")
		}
	}
}

func TestMVPWDewSubmitRejects(t *testing.T) {
	for _, name := range []string{"missing receipt", "missing envelope", "taskIds", "inputHashes", "expired", "unknown key", "wrong receipt", "wrong envelope", "re-leased"} {
		t.Run(name, func(t *testing.T) {
			h, w, a := dewAssignment(t)
			q := lateSubmission(a, `{"ok":true}`)
			q.LeaseReceipt = a.LeaseReceipt
			q.Offline = true
			switch name {
			case "missing receipt":
				q.LeaseReceipt = nil
			case "missing envelope":
				q.TaskEnvelope = verify.SignedTaskEnvelope{}
			case "taskIds":
				q.LeaseReceipt.TaskIDs[0] = "tampered"
			case "inputHashes":
				q.LeaseReceipt.InputHashes[0] = verify.SHA256(nil)
			case "expired":
				h.now = time.Unix(q.LeaseReceipt.Deadline, 0)
			case "unknown key":
				q.LeaseReceipt.KeyID = "unknown"
			case "wrong receipt":
				// A receipt for ANOTHER lease of the same task: authentic, but
				// not derived from the lease token this submission presents.
				// (F12 v2 receipt ids are derived, so the retired
				// "another-token" id would no longer pass Valid.)
				r := *q.LeaseReceipt
				r.ReceiptID = verify.TaskReceiptID(a.Lease.JobID, a.Lease.TaskID, r.Generation, verify.SHA256([]byte("another-lease-token")))
				signed, err := verify.SignLeaseReceipt(r, h.engine.TaskKey, h.engine.TaskKeyID)
				if err != nil {
					t.Fatal(err)
				}
				q.LeaseReceipt = &signed
			case "wrong envelope":
				// A re-signed envelope that names another task id: authentic, but not
				// the task this lease covers.
				env, err := verify.DecodeTaskEnvelope(q.TaskEnvelope)
				if err != nil {
					t.Fatal(err)
				}
				env.TaskID = "another-task"
				signed, err := verify.SignTaskEnvelope(env, h.engine.TaskKey, h.engine.TaskKeyID)
				if err != nil {
					t.Fatal(err)
				}
				q.TaskEnvelope = signed
			case "re-leased":
				// F12 (G-F12-5): the receipt, not the lease TTL, is the
				// re-assignment fence, so sweep past the receipt deadline.
				h.now = time.Unix(q.LeaseReceipt.Deadline, 0).Add(time.Second)
				if err := h.engine.Sweep(context.Background()); err != nil {
					t.Fatal(err)
				}
				w2 := h.workerAs(t, "other", 2)
				// The rejection below needs the task to be re-leased, so claim
				// until this worker actually holds dew-a again. A claim may
				// legitimately serve a different pending task first (AT22-L3-4:
				// a held re-run of the seed job used to win the scheduler here),
				// and while the task sits unclaimed the late result of the
				// original lease is accepted on purpose (F9 transition grace).
				released := false
				for i := 0; i < 10 && !released; i++ {
					b, err := h.engine.Claim(context.Background(), Thread, w2.Owner, w2.TabID)
					if err != nil && !errors.Is(err, ErrEmpty) {
						t.Fatal(err)
					}
					members := b.Batch
					if len(members) == 0 {
						members = []Assignment{b}
					}
					for _, m := range members {
						if m.Lease.TaskID == a.Lease.TaskID {
							released = true
						}
					}
				}
				if !released {
					t.Fatalf("no claim re-leased %s", a.Lease.TaskID)
				}
			}
			if _, err := h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, q); err == nil {
				t.Fatal("invalid dew submission accepted")
			}
		})
	}
}

// AT22-L3-4: pins the engine behaviour behind the re-leased rejection. A late
// dew result is refused only once another worker re-leased the task; while the
// expired task sits unclaimed, the authenticated result of the original lease
// stays acceptable (F9 transition grace) with its still-valid receipt, the
// same rule the plain F9 test pins without dew.
func TestMVPWDewLateAcceptedWithoutReLease(t *testing.T) {
	h, w, a := dewAssignment(t)
	h.now = a.Lease.ExpiresAt.Add(time.Second)
	if err := h.engine.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	q := lateSubmission(a, `{"ok":true}`)
	q.LeaseReceipt = a.LeaseReceipt
	q.Offline = true
	r, err := h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, q)
	if err != nil || !r.Late {
		t.Fatalf("late dew with no re-lease = %+v, %v", r, err)
	}
}

func TestMVPWDewRotationOverlap(t *testing.T) {
	h, w, a := dewAssignment(t)
	if err := h.engine.RotateTaskKey(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	q := lateSubmission(a, `{"ok":true}`)
	q.LeaseReceipt = a.LeaseReceipt
	q.Offline = true
	if _, err := h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, q); err != nil {
		t.Fatal(err)
	}
}

func TestMVPWPeerRequiresReceiptAndEnvelope(t *testing.T) {
	h := newHarness(t)
	// R11 Part E addendum: seed with dew disabled, so this regression tests the peer
	// rule alone and not the default-ON receipt of every lease.
	data := []byte("peer module bytes")
	id := verify.SHA256(data)
	if _, err := h.engine.Upload(context.Background(), id, "", 0, int64(len(data)), data); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Create(context.Background(), Thread, "developer", "", CreateRequest{
		ModuleArtifactIDs: []string{id},
		Tasks:             []TaskSpec{{TaskID: "task-peer", Input: json.RawMessage(`{"json":{"n":1}}`)}},
		Options:           json.RawMessage(`{"dew":{"enabled":false}}`),
	}); err != nil {
		t.Fatal(err)
	}
	w := h.worker(t, 1)
	a, err := h.engine.Claim(context.Background(), Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if a.LeaseReceipt != nil {
		t.Fatalf("a dew-disabled job issued a receipt: %+v", a.LeaseReceipt)
	}
	body := json.RawMessage(`{"ok":true}`)
	sub := Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, Output: body, SHA256: verify.SHA256(body)}
	// Decode the wire flag so this regression can run before the field exists.
	if err := json.Unmarshal([]byte(`{"peerRelayed":true}`), &sub); err != nil {
		t.Fatal(err)
	}
	if _, err = h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, sub); err == nil {
		t.Fatal("peer result without its signed receipt was accepted")
	}
}
