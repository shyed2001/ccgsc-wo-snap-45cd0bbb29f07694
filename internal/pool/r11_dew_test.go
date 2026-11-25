// File: pools-core/internal/pool/r11_dew_test.go (go)
// R11 Part E (owner decision 2026-11-21): Dew defaults ON. A job that names no
// dew option, or an empty one, gets enabled receipts; only an explicit
// {"dew":{"enabled":false}} turns the measure off.
//
// Part E addendum (DeepSeek R14 review, 2026-11-22): Enabled is a *bool so an
// absent field means ON and an explicit false means OFF, a parse error fails
// closed, an active lease never fails on receipt expiry, pre-change leases keep
// working until their TTL, and every dew rejection writes a dead letter.
//
// F12 (G-F12-1) replaced the 24 h window: the default is 600 s, and a job
// window outside 60..1800 s fails closed. Receipts are per task.
package pool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
)

func TestDewDefaultEnabled(t *testing.T) {
	cases := []struct {
		name  string
		opts  string
		on    bool
		shift int64
	}{
		{name: "no options at all", opts: "", on: true},
		{name: "empty options object", opts: "{}", on: true},
		{name: "dew is null", opts: `{"dew":null}`, on: true},
		{name: "empty dew object", opts: `{"dew":{}}`, on: true},
		{name: "explicitly disabled", opts: `{"dew":{"enabled":false}}`, on: false},
		{name: "explicitly enabled", opts: `{"dew":{"enabled":true}}`, on: true},
		{name: "window only", opts: `{"dew":{"receiptWindowSeconds":900}}`, on: true, shift: 900},
	}
	for _, c := range cases {
		var raw json.RawMessage
		if c.opts != "" {
			raw = json.RawMessage(c.opts)
		}
		d, err := parseDewOptions(raw)
		if err != nil {
			t.Fatalf("%s: parseDewOptions: %v", c.name, err)
		}
		if d.on() != c.on {
			t.Fatalf("%s: enabled = %v, want %v", c.name, d.on(), c.on)
		}
		if c.on && d.Enabled != nil && !*d.Enabled {
			t.Fatalf("%s: the default ON must be a nil flag, not an explicit true", c.name)
		}
		if !c.on && (d.Enabled == nil || *d.Enabled) {
			t.Fatalf("%s: off must be an explicit false", c.name)
		}
		want := c.shift
		if want == 0 {
			want = DefaultDewReceiptWindowSeconds
		}
		if d.ReceiptWindowSeconds != want {
			t.Fatalf("%s: receiptWindowSeconds = %d, want %d", c.name, d.ReceiptWindowSeconds, want)
		}
	}
}

// TestDewParseErrorFailsClosed pins the addendum rule for a malformed option: an
// explicit false is the only way off, so an unreadable option answers off rather
// than guessing.
func TestDewParseErrorFailsClosed(t *testing.T) {
	for _, opts := range []string{
		`{"dew":{"enabled":"yes"}}`, `{"dew":{"unknown":1}}`, `{"dew":5}`,
		`{"dew":{"receiptWindowSeconds":1}}`,
		`{"dew":{"receiptWindowSeconds":3600}}`,
		`{"dew":{"receiptWindowSeconds":86400}}`,
	} {
		d, err := parseDewOptions(json.RawMessage(opts))
		if err == nil {
			t.Fatalf("%s: parseDewOptions accepted a malformed option", opts)
		}
		if d.on() {
			t.Fatalf("%s: a malformed option enabled dew", opts)
		}
	}
}

// TestDewAbsentFlagIsNil pins the addendum's *bool rule directly: only an
// explicit false may ever be off, so the zero value of the parse answers ON.
func TestDewAbsentFlagIsNil(t *testing.T) {
	for _, opts := range []string{"", "{}", `{"dew":null}`, `{"dew":{}}`} {
		var raw json.RawMessage
		if opts != "" {
			raw = json.RawMessage(opts)
		}
		d, err := parseDewOptions(raw)
		if err != nil {
			t.Fatalf("%s: %v", opts, err)
		}
		if d.Enabled != nil {
			t.Fatalf("%s: enabled = %v, want nil (the default ON)", opts, *d.Enabled)
		}
		if !d.on() {
			t.Fatalf("%s: the nil flag must answer ON", opts)
		}
	}
	if d, err := parseDewOptions(json.RawMessage(`{"dew":{"enabled":false}}`)); err != nil || d.Enabled == nil || *d.Enabled {
		t.Fatalf("explicit false = %+v / %v, want the only OFF form", d.Enabled, err)
	}
}

// TestDewPresentFlag pins the parse presence flag the addendum asks for.
func TestDewPresentFlag(t *testing.T) {
	for _, c := range []struct {
		opts    string
		present bool
	}{
		{"", false},
		{"{}", false},
		{`{"dew":null}`, false},
		{`{"dew":{}}`, true},
		{`{"dew":{"enabled":false}}`, true},
	} {
		var raw json.RawMessage
		if c.opts != "" {
			raw = json.RawMessage(c.opts)
		}
		d, err := parseDewOptions(raw)
		if err != nil {
			t.Fatalf("%s: %v", c.opts, err)
		}
		if d.present != c.present {
			t.Fatalf("%s: present = %v, want %v", c.opts, d.present, c.present)
		}
	}
}

// claimDewTask claims until the lease of one of want task ids is granted,
// submitting every other lease of the same worker so the queue reaches it.
func claimDewTask(t *testing.T, h *harness, w Worker, want ...string) Assignment {
	t.Helper()
	for i := 0; i < 8; i++ {
		a, err := h.engine.Claim(context.Background(), Thread, w.Owner, w.TabID)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		for _, id := range want {
			if a.Lease.TaskID == id {
				return a
			}
		}
		if _, err = h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, lateSubmission(a, `{"seed":true}`)); err != nil {
			t.Fatalf("Submit(%s): %v", a.Lease.TaskID, err)
		}
	}
	t.Fatalf("never claimed any of %v", want)
	return Assignment{}
}

// TestDewDefaultIssuesReceipt: with no dew option the batch still carries a
// signed receipt, and dewEnabled (the envelope requirement of engine.go:1018 and
// batch_F1.go:154) reports true.
func TestDewDefaultIssuesReceipt(t *testing.T) {
	h := newHarness(t)
	q := h.seed(t, 1, nil).Request
	q.Tasks = []TaskSpec{
		{TaskID: "dew-a", Input: json.RawMessage(`{"json":{"n":160}}`)},
		{TaskID: "dew-b", Input: json.RawMessage(`{"json":{"n":240}}`)},
	}
	q.BatchSize = 2
	zero := 0.0
	q.RerunPercent = &zero
	if _, err := h.engine.Create(context.Background(), Thread, "developer", "", q); err != nil {
		t.Fatal(err)
	}
	w := h.worker(t, 2)
	a := claimDewTask(t, h, w, "dew-a")
	if a.LeaseReceipt == nil {
		t.Fatal("a batch with no dew option never carried a signed receipt")
	}
	if a.LeaseReceipt.WindowSeconds != DefaultDewReceiptWindowSeconds {
		t.Fatalf("receipt window = %d, want the 10 min default", a.LeaseReceipt.WindowSeconds)
	}
	if len(a.LeaseReceipt.TaskIDs) != 1 || a.LeaseReceipt.TaskIDs[0] != a.Lease.TaskID {
		t.Fatalf("receipt task ids = %v, want only %s", a.LeaseReceipt.TaskIDs, a.Lease.TaskID)
	}
	if len(a.Batch) != 2 {
		t.Fatalf("batch = %d, want both tasks", len(a.Batch))
	}
	for _, m := range a.Batch {
		if m.LeaseReceipt == nil || m.LeaseReceipt.WindowSeconds != DefaultDewReceiptWindowSeconds ||
			len(m.LeaseReceipt.TaskIDs) != 1 || m.LeaseReceipt.TaskIDs[0] != m.Lease.TaskID {
			t.Fatalf("member %s receipt = %+v, want one task and the 10 min window", m.Lease.TaskID, m.LeaseReceipt)
		}
	}
}

// TestDewExplicitlyDisabled has no receipt and no envelope requirement of its own.
func TestDewExplicitlyDisabled(t *testing.T) {
	h := newHarness(t)
	// The admin envelope measure is unrelated to dew, so turn it off to prove the
	// disabled dew adds no envelope requirement of its own (batch_F1.go:154).
	cfg := h.engine.TrustConfig()
	cfg.EnvelopeEnabled = false
	h.engine.SetTrustConfig(cfg)
	q := h.seed(t, 1, nil).Request
	q.Tasks = []TaskSpec{
		{TaskID: "dew-a", Input: json.RawMessage(`{"json":{"n":160}}`)},
		{TaskID: "dew-b", Input: json.RawMessage(`{"json":{"n":240}}`)},
	}
	q.BatchSize = 2
	zero := 0.0
	q.RerunPercent = &zero
	q.Options = json.RawMessage(`{"dew":{"enabled":false}}`)
	if _, err := h.engine.Create(context.Background(), Thread, "developer", "", q); err != nil {
		t.Fatal(err)
	}
	w := h.worker(t, 2)
	a := claimDewTask(t, h, w, "dew-a")
	if a.LeaseReceipt != nil {
		t.Fatal("an explicitly disabled dew still issued a receipt")
	}
	if len(a.TaskEnvelope.Payload) != 0 {
		t.Fatal("an explicitly disabled dew still required the signed envelope")
	}
	if _, err := h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, lateSubmission(a, `{"ok":true}`)); err != nil {
		t.Fatalf("a dew-off submission was refused: %v", err)
	}
}

// dewSubmitAssignment seeds one job and claims the first ordinary lease of a
// worker, so a test can drive the submit path directly.
func dewSubmitAssignment(t *testing.T, h *harness, opts string) (Job, Worker, Assignment) {
	t.Helper()
	q := CreateRequest{}
	data := []byte("dew submit module")
	id := sha256Upload(t, h, data)
	q.ModuleArtifactIDs = []string{id}
	q.Tasks = []TaskSpec{{TaskID: "task-dew", Input: json.RawMessage(`{"json":{"n":160}}`)}}
	zero := 0.0
	q.RerunPercent = &zero
	if opts != "" {
		q.Options = json.RawMessage(opts)
	}
	j, err := h.engine.Create(context.Background(), Thread, "developer", "", q)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	w := h.workerAt(t, "contributor-1", "host-1", "10.30.0.5")
	a, err := h.engine.Claim(context.Background(), Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if a.LeaseReceipt == nil {
		t.Fatalf("assignment = %+v, want the receipt dew default ON issues", a)
	}
	return j, w, a
}

// TestDewReceiptCoversTheLease pins the F12 window: the receipt of a job with no
// dew option carries the 10 min default, which still outlives the 1 min lease.
func TestDewReceiptCoversTheLease(t *testing.T) {
	h := newHarness(t)
	_, _, a := dewSubmitAssignment(t, h, "")
	if a.LeaseReceipt.WindowSeconds != DefaultDewReceiptWindowSeconds {
		t.Fatalf("receipt window = %d, want the 10 min default", a.LeaseReceipt.WindowSeconds)
	}
	if !time.Unix(a.LeaseReceipt.Deadline, 0).After(a.Lease.ExpiresAt) {
		t.Fatalf("receipt deadline = %v, want the 24 h window to outlive the %v lease", a.LeaseReceipt.Deadline, a.Lease.ExpiresAt)
	}
}

// TestDewActiveLeaseAcceptsSubmissionAfterReceiptWindow is the addendum's late
// submit rule: an active lease must not fail on receipt expiry. A receipt whose
// window is shorter than the lease is built directly, then the clock passes its
// deadline while the lease is still live.
func TestDewActiveLeaseAcceptsSubmissionAfterReceiptWindow(t *testing.T) {
	h := newHarness(t)
	// 60 s is the shortest legal window. The lease is lengthened so it is still
	// live after that window, which is the grace the addendum requires.
	h.engine.TTL = 3 * time.Minute
	j, w, a := dewSubmitAssignment(t, h, `{"dew":{"receiptWindowSeconds":60}}`)
	if a.LeaseReceipt.WindowSeconds != 60 {
		t.Fatalf("receipt window = %d, want the 60 s window of the job", a.LeaseReceipt.WindowSeconds)
	}
	h.now = time.Unix(a.LeaseReceipt.Deadline+1, 0).UTC()
	if !a.Lease.ExpiresAt.After(h.now) {
		t.Fatal("the lease must still be live after the 60 s receipt")
	}
	q := lateSubmission(a, `{"ok":true}`)
	q.LeaseReceipt = a.LeaseReceipt
	if _, err := h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, q); err != nil {
		t.Fatalf("Submit after the receipt window but inside the lease: %v", err)
	}
	got, err := h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "completed" {
		t.Fatalf("job = %+v, want the submission settled", got)
	}
}

// TestDewExpiredReceiptOnExpiredLeaseIsStillRejected is the negative half of the
// grace: once the lease is gone too, the expired receipt proves nothing.
func TestDewExpiredReceiptOnExpiredLeaseIsStillRejected(t *testing.T) {
	h := newHarness(t)
	h.engine.TTL = 3 * time.Minute
	_, w, a := dewSubmitAssignment(t, h, `{"dew":{"receiptWindowSeconds":60}}`)
	h.now = time.Unix(a.LeaseReceipt.Deadline+1, 0).UTC()
	if !a.Lease.ExpiresAt.After(h.now) {
		t.Fatal("the lease must still be live after the 60 s receipt")
	}
	h.now = a.Lease.ExpiresAt.Add(time.Second)
	q := lateSubmission(a, `{"ok":true}`)
	q.LeaseReceipt = a.LeaseReceipt
	if _, err := h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, q); err == nil {
		t.Fatal("an expired receipt on an expired lease was accepted")
	}
}

// TestDewSubmissionWithoutReceiptIsAcceptedDuringTransition covers the addendum
// transition rule: a lease claimed before dew default ON carries no receipt, and
// a submission that presents none is still accepted while that lease is live.
// The pre-change state is simulated by clearing the marker the claim path sets.
func TestDewSubmissionWithoutReceiptIsAcceptedDuringTransition(t *testing.T) {
	h := newHarness(t)
	_, w, a := dewSubmitAssignment(t, h, "")
	clearDewMarker(t, h, a.Lease.JobID, a.Lease.TaskID)
	a.LeaseReceipt = nil
	if _, err := h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, lateSubmission(a, `{"ok":true}`)); err != nil {
		t.Fatalf("a live lease without a receipt was refused: %v", err)
	}
}

// TestDewReceiptedLeaseRequiresItsReceipt is the other side of the transition:
// once the claim path marked the task as receipted, a submission without one is
// refused. Without this a page could simply drop the receipt it was issued.
func TestDewReceiptedLeaseRequiresItsReceipt(t *testing.T) {
	h := newHarness(t)
	_, w, a := dewSubmitAssignment(t, h, "")
	a.LeaseReceipt = nil
	if _, err := h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, lateSubmission(a, `{"ok":true}`)); !errors.Is(err, ErrConflict) {
		t.Fatalf("receipted lease without its receipt = %v, want ErrConflict", err)
	}
	letters := deadLetters(t, h)
	if len(letters) == 0 {
		t.Fatal("the refusal left no dead letter")
	}
}

// clearDewMarker rewrites one stored task with its pre-change dew state, so the
// transition rule can be exercised on a job this build claimed.
func clearDewMarker(t *testing.T, h *harness, jobID, taskID string) {
	t.Helper()
	if err := h.engine.Store.Update(context.Background(), func(tx store.Tx) error {
		var j Job
		if err := tx.Get(store.Jobs, jobID, &j); err != nil {
			return err
		}
		for i := range j.Tasks {
			if j.Tasks[i].TaskID == taskID {
				j.Tasks[i].DewReceipt = false
			}
		}
		return tx.Put(store.Jobs, jobID, j)
	}); err != nil {
		t.Fatalf("clear dew marker: %v", err)
	}
}

// TestDewRejectionWritesDeadLetter pins the addendum rule that a dew rejection
// leaves evidence: the refusal is recorded, not merely returned.
func TestDewRejectionWritesDeadLetter(t *testing.T) {
	h := newHarness(t)
	_, w, a := dewSubmitAssignment(t, h, "")
	q := lateSubmission(a, `{"ok":true}`)
	// A receipt is required and this one names another lease token.
	r := *a.LeaseReceipt
	// F12 receipt ids are SHA-256. A different digest is authentic in shape
	// and still not the id derived from this lease.
	r.ReceiptID = verify.SHA256([]byte("another-token"))
	signed, err := verify.SignLeaseReceipt(r, h.engine.TaskKey, h.engine.TaskKeyID)
	if err != nil {
		t.Fatal(err)
	}
	q.LeaseReceipt = &signed
	if _, err = h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, q); err == nil {
		t.Fatal("a receipt for another lease was accepted")
	}
	letters := deadLetters(t, h)
	found := false
	for _, reason := range letters {
		if reason == "dew receipt rejected" {
			found = true
		}
	}
	if !found {
		t.Fatalf("dead letters = %v, want the dew rejection recorded", letters)
	}
}

// deadLetters lists the recorded dead-letter reasons of a harness store.
func deadLetters(t *testing.T, h *harness) []string {
	t.Helper()
	var out []string
	err := h.engine.Store.View(context.Background(), func(rd store.Reader) error {
		records, err := rd.List(store.DeadLetters)
		if err != nil {
			return err
		}
		for _, rec := range records {
			var entry struct{ Reason string }
			if json.Unmarshal(rec.Value, &entry) == nil {
				out = append(out, entry.Reason)
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("List dead letters: %v", err)
	}
	return out
}

// sha256Upload uploads bytes and answers their digest.
func sha256Upload(t *testing.T, h *harness, data []byte) string {
	t.Helper()
	id := verify.SHA256(data)
	if _, err := h.engine.Upload(context.Background(), id, "", 0, int64(len(data)), data); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	return id
}
