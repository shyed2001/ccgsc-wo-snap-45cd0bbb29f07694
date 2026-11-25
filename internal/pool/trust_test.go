// Path: internal/pool/trust_test.go | Author: Hermes (MiniMax) for the owner | 2026-11-17
// What/why: tests of the tamper measures wired into the pool engine: the signed task
// envelope, the result hash check, the reputation gate, the re-run comparison and the
// canary injection; WWPP-R35.1, R37.1, R37.1b, R39.1 (WO T4.2).
package pool

import (
	"ccgsc/poolscore/internal/store/sqlite"
	"ccgsc/poolscore/internal/verify"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// harness is a pool engine on a file backed SQLite store, with a fixed clock.
type harness struct {
	engine *Engine
	now    time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, e := sqlite.Open(filepath.Join(t.TempDir(), "pool.db"))
	if e != nil {
		t.Fatalf("sqlite.Open: %v", e)
	}
	t.Cleanup(func() { _ = db.Close() })
	l := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine, e := New(db, l, "s1", time.Minute)
	if e != nil {
		t.Fatalf("New: %v", e)
	}
	h := &harness{engine: engine, now: time.Unix(1_700_000_000, 0).UTC()}
	engine.Now = func() time.Time { return h.now }
	engine.Verifier.SetClock(func() time.Time { return h.now })
	return h
}

// seed creates a job with n tasks and one module artifact.
func (h *harness) seed(t *testing.T, n int, rerunPercent *float64) Job {
	t.Helper()
	ctx := context.Background()
	data := []byte("module bytes")
	id := verify.SHA256(data)
	if _, e := h.engine.Upload(ctx, id, "", 0, int64(len(data)), data); e != nil {
		t.Fatalf("Upload: %v", e)
	}
	q := CreateRequest{ModuleArtifactIDs: []string{id}, RerunPercent: rerunPercent}
	for i := 0; i < n; i++ {
		q.Tasks = append(q.Tasks, TaskSpec{TaskID: "task-" + string(rune('a'+i)), Input: json.RawMessage(`{"json":{"n":1}}`)})
	}
	j, e := h.engine.Create(ctx, Thread, "developer", "", q)
	if e != nil {
		t.Fatalf("Create: %v", e)
	}
	return j
}

// worker registers a contributor tab.
func (h *harness) worker(t *testing.T, slots int) Worker {
	return h.workerAs(t, "contributor", slots)
}

func (h *harness) workerAs(t *testing.T, owner string, slots int) Worker {
	t.Helper()
	w, e := h.engine.Register(context.Background(), Thread, owner, Worker{HostID: "host", Slots: slots})
	if e != nil {
		t.Fatalf("Register: %v", e)
	}
	return w
}

// --- 1. happy path: the signed task envelope travels with the assignment --------

func TestClaimSignsTaskEnvelope(t *testing.T) {
	h := newHarness(t)
	j := h.seed(t, 1, nil)
	wk := h.worker(t, 1)
	a, e := h.engine.Claim(context.Background(), Thread, "contributor", wk.TabID)
	if e != nil {
		t.Fatalf("Claim: %v", e)
	}

	// The envelope is a signed canonical JSON payload over the task facts.
	if len(a.TaskEnvelope.Payload) == 0 || len(a.TaskEnvelope.Signature) == 0 {
		t.Fatalf("assignment carries no signed task envelope: %+v", a.TaskEnvelope)
	}

	if a.TaskEnvelope.KeyID != h.engine.TaskKeyID {
		t.Errorf("envelope key id = %s, want %s", a.TaskEnvelope.KeyID, h.engine.TaskKeyID)
	}
	if a.TaskEnvelope.Algorithm != verify.EnvelopeSignature {
		t.Errorf("envelope algorithm = %s, want %s", a.TaskEnvelope.Algorithm, verify.EnvelopeSignature)
	}
	// The payload is canonical JSON: sorted keys, no spaces.
	text := string(a.TaskEnvelope.Payload)
	if containsAny(text, ": ", ", ") {
		t.Errorf("envelope payload is not canonical: %s", text)
	}
	// It verifies against the key ring of the engine.
	if e := h.engine.VerifyTaskEnvelope(a.TaskEnvelope); e != nil {
		t.Fatalf("VerifyTaskEnvelope: %v", e)
	}
	dec, e := verify.DecodeTaskEnvelope(a.TaskEnvelope)
	if e != nil {
		t.Fatalf("DecodeTaskEnvelope: %v", e)
	}
	if dec.JobID != j.JobID || dec.TaskID != "task-a" {
		t.Errorf("envelope = %+v, want job %s / task-a", dec, j.JobID)
	}
	if dec.ArtifactSHA256 != j.Request.ModuleArtifactIDs[0] {
		t.Errorf("envelope artifact hash = %s, want %s", dec.ArtifactSHA256, j.Request.ModuleArtifactIDs[0])
	}
	if dec.Workload == nil || dec.Workload.ID != dec.ArtifactSHA256 ||
		dec.Workload.Version != 1 || len(dec.Workload.Files) != 1 ||
		dec.Workload.Files[0].SHA256 != j.Request.ModuleArtifactIDs[0] {
		t.Errorf("D-W10-3: signed workload identity/version/per-file digest missing: %+v", dec.Workload)
	}
	if dec.InputSHA256 != verify.SHA256(json.RawMessage(`{"json":{"n":1}}`)) {
		t.Errorf("envelope input hash = %s, want the task input hash", dec.InputSHA256)
	}
	if dec.ExpiresAt <= dec.IssuedAt {
		t.Errorf("envelope time bounds are wrong: %+v", dec)
	}
	// A forged envelope (different key) is rejected.
	forged := a.TaskEnvelope
	forged.Signature = append([]byte(nil), forged.Signature...)
	forged.Signature[0] ^= 0xff
	if e := h.engine.VerifyTaskEnvelope(forged); !errors.Is(e, verify.ErrBadSignature) {
		t.Errorf("VerifyTaskEnvelope(forged) = %v, want ErrBadSignature", e)
	}
}

func TestHappyPathCompletesWithTrustEvidence(t *testing.T) {
	h := newHarness(t)
	j := h.seed(t, 1, nil)
	wk := h.worker(t, 1)
	ctx := context.Background()
	a, e := h.engine.Claim(ctx, Thread, "contributor", wk.TabID)
	if e != nil {
		t.Fatalf("Claim: %v", e)
	}
	out := json.RawMessage(`{"json":{"answer":42}}`)
	r, e := h.engine.Submit(ctx, Thread, "contributor", wk.TabID, Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out)})
	if e != nil {
		t.Fatalf("Submit: %v", e)
	}
	if r.Status != "done" || r.Verified || !r.HashOK || r.TrustComplete {
		t.Fatalf("result = %+v, want hash verification without canary trust completion", r)
	}
	if r.SHA256 != verify.SHA256(out) {
		t.Errorf("result hash = %s, want %s", r.SHA256, verify.SHA256(out))
	}
	got, e := h.engine.Get(ctx, Thread, "developer", j.JobID)
	if e != nil {
		t.Fatalf("Get: %v", e)
	}
	if got.State != "completed" || got.Progress.Completed != 1 {
		t.Fatalf("job = %+v, want completed", got)
	}
	if len(got.Results) != 1 || got.Results[0].Verified || !got.Results[0].HashOK {
		t.Fatalf("job results = %+v, want one hash-verified result", got.Results)
	}
	// The pass is a reputation event of the worker.
	rep, e := h.engine.Verifier.Reputation().Worker(wk.TabID)
	if e != nil {
		t.Fatalf("Reputation.Worker: %v", e)
	}
	if rep.Passes != 1 || rep.Score != 1.0 {
		t.Errorf("reputation = %+v, want one pass at score 1", rep)
	}
}

func TestCanarySelectionUsesPersistedSamplingKey(t *testing.T) {
	h := newHarness(t)
	cfg := h.engine.TrustConfig()
	cfg.CanaryRate = 50
	h.engine.SetTrustConfig(cfg)
	if _, err := h.engine.RegisterCanaryWithAnswer("canary-1", "task-canary", []byte(`{"canary":1}`), []byte(`"known answer"`)); err != nil {
		t.Fatal(err)
	}
	j := h.seed(t, 1, nil)
	foundMismatch := false
	for i := 0; i < 1000; i++ {
		taskID := "sampling-" + string(rune(i))
		want := verify.ShouldRerunWithKey(h.engine.rerunSecret, j.JobID+":"+taskID, cfg.CanaryRate)
		got := h.engine.pickCanary(&j, taskID) != nil
		if got != want {
			t.Fatalf("canary decision for %q = %v, want keyed decision %v", taskID, got, want)
		}
		if want != verify.ShouldRerun(j.JobID+":"+taskID, cfg.CanaryRate) {
			foundMismatch = true
		}
	}
	if !foundMismatch {
		t.Fatal("test data did not include a public/keyed sampling mismatch")
	}
}

// --- 2. tampered result -------------------------------------------------------

func TestTamperedResultIsRejectedAndFlagged(t *testing.T) {
	h := newHarness(t)
	j := h.seed(t, 1, nil)
	wk := h.worker(t, 1)
	ctx := context.Background()
	a, e := h.engine.Claim(ctx, Thread, "contributor", wk.TabID)
	if e != nil {
		t.Fatalf("Claim: %v", e)
	}

	// The declared hash does not match the bytes: the result is a tamper.
	r, e := h.engine.Submit(ctx, Thread, "contributor", wk.TabID, Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: json.RawMessage(`{"answer":1}`), SHA256: verify.SHA256([]byte("other"))})
	if e != nil {
		t.Fatalf("Submit: %v", e)
	}
	if r.Status != "failed" || r.Error != "result hash mismatch" {
		t.Fatalf("result = %+v, want a failed result with a hash mismatch", r)
	}
	if !r.Flagged {
		t.Error("tampered result is not flagged")
	}
	got, e := h.engine.Get(ctx, Thread, "developer", j.JobID)
	if e != nil {
		t.Fatalf("Get: %v", e)
	}
	if !got.Tasks[0].Flagged || got.Tasks[0].FlagReason != "result hash mismatch" {
		t.Errorf("task = %+v, want a flagged task", got.Tasks[0])
	}
	// No accepted result exists for the tampered task.
	for _, res := range got.Results {
		if res.Status == "done" {
			t.Errorf("a tampered result was accepted: %+v", res)
		}
	}
	// The tamper is a reputation failure of the worker.
	rep, e := h.engine.Verifier.Reputation().Worker(wk.TabID)
	if e != nil {
		t.Fatalf("Reputation.Worker: %v", e)
	}
	if rep.Fails != 1 {
		t.Errorf("reputation fails = %d, want 1", rep.Fails)
	}
}

func TestMissingResultHashIsRejected(t *testing.T) {
	h := newHarness(t)
	h.seed(t, 1, nil)
	wk := h.worker(t, 1)
	ctx := context.Background()
	a, err := h.engine.Claim(ctx, Thread, "contributor", wk.TabID)
	if err != nil {
		t.Fatal(err)
	}
	out := json.RawMessage(`{"answer":1}`)
	_, err = h.engine.Submit(ctx, Thread, "contributor", wk.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Submit without result hash = %v, want ErrInvalid", err)
	}
}

func TestBarredWorkerCannotSettleExistingLease(t *testing.T) {
	h := newHarness(t)
	h.seed(t, 1, nil)
	wk := h.worker(t, 1)
	ctx := context.Background()
	a, err := h.engine.Claim(ctx, Thread, "contributor", wk.TabID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err = h.engine.Verifier.Reputation().Observe(wk.TabID, verify.EventFail, "", "test"); err != nil {
			t.Fatal(err)
		}
	}
	out := json.RawMessage(`{"answer":1}`)
	if _, err = h.engine.Submit(ctx, Thread, "contributor", wk.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out),
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("barred worker settlement = %v, want ErrForbidden", err)
	}
}

// --- 3. re-run mismatch -------------------------------------------------------

func TestReRunMismatchFailsBothWorkersAndFlagsTask(t *testing.T) {
	h := newHarness(t)
	// 100 % re-run: every task is sampled.
	pct := 100.0
	j := h.seed(t, 1, &pct)
	w1 := h.worker(t, 1)
	w2 := h.workerAs(t, "contributor-2", 1)
	ctx := context.Background()
	a, e := h.engine.Claim(ctx, Thread, "contributor", w1.TabID)
	if e != nil {
		t.Fatalf("Claim(w1): %v", e)
	}
	first := json.RawMessage(`{"answer":"first"}`)
	if _, e = h.engine.Submit(ctx, Thread, "contributor", w1.TabID, Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: first, SHA256: verify.SHA256(first)}); e != nil {
		t.Fatalf("Submit(w1): %v", e)
	}
	// The first result waits for the re-run: the task is queued again.
	got, e := h.engine.Get(ctx, Thread, "developer", j.JobID)
	if e != nil {
		t.Fatalf("Get: %v", e)
	}
	if got.Tasks[0].Pending == nil || got.Tasks[0].State != "queued" {
		t.Fatalf("task = %+v, want a queued task with a pending result", got.Tasks[0])
	}
	// The re-run goes to a different worker.
	b, e := h.engine.Claim(ctx, Thread, "contributor-2", w2.TabID)
	if e != nil {
		t.Fatalf("Claim(w2): %v", e)
	}
	if b.Lease.WorkerID == w1.TabID {
		t.Fatal("the re-run went to the same worker")
	}
	second := json.RawMessage(`{"answer":"second"}`)
	r, e := h.engine.Submit(ctx, Thread, "contributor-2", w2.TabID, Submission{Lease: b.Lease, TaskEnvelope: b.TaskEnvelope, LeaseReceipt: b.LeaseReceipt, Output: second, SHA256: verify.SHA256(second)})
	if e != nil {
		t.Fatalf("Submit(w2): %v", e)
	}
	if !r.Rerun || r.RerunOK {
		t.Fatalf("result = %+v, want a re-run with a mismatch verdict", r)
	}
	if r.Status != "failed" || r.Error != "re-run mismatch" || !r.Flagged {
		t.Fatalf("result = %+v, want a failed and flagged re-run mismatch", r)
	}
	// The task is flagged and not retried.
	got, e = h.engine.Get(ctx, Thread, "developer", j.JobID)
	if e != nil {
		t.Fatalf("Get: %v", e)
	}
	if !got.Tasks[0].Flagged || got.Tasks[0].FlagReason != "re-run mismatch" {
		t.Errorf("task = %+v, want a flagged task", got.Tasks[0])
	}
	if got.Tasks[0].State != "failed" {
		t.Errorf("task state = %s, want failed", got.Tasks[0].State)
	}
	// Both workers took a reputation failure.
	for _, wk := range []Worker{w1, w2} {
		rep, er := h.engine.Verifier.Reputation().Worker(wk.TabID)
		if er != nil {
			t.Fatalf("Reputation.Worker: %v", er)
		}
		if rep.Fails != 1 {
			t.Errorf("worker %s fails = %d, want 1", wk.TabID, rep.Fails)
		}
		if wk.TabID == w1.TabID && rep.Passes != 0 {
			t.Errorf("first worker passes = %d, want 0 before re-run comparison", rep.Passes)
		}
	}
}

func TestReRunMatchAcceptsResult(t *testing.T) {
	h := newHarness(t)
	pct := 100.0
	j := h.seed(t, 1, &pct)
	w1 := h.worker(t, 1)
	w2 := h.workerAs(t, "contributor-2", 1)
	ctx := context.Background()
	a, e := h.engine.Claim(ctx, Thread, "contributor", w1.TabID)
	if e != nil {
		t.Fatalf("Claim(w1): %v", e)
	}
	same := json.RawMessage(`{"answer":"same"}`)
	if _, e = h.engine.Submit(ctx, Thread, "contributor", w1.TabID, Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: same, SHA256: verify.SHA256(same)}); e != nil {
		t.Fatalf("Submit(w1): %v", e)
	}
	b, e := h.engine.Claim(ctx, Thread, "contributor-2", w2.TabID)
	if e != nil {
		t.Fatalf("Claim(w2): %v", e)
	}
	r, e := h.engine.Submit(ctx, Thread, "contributor-2", w2.TabID, Submission{Lease: b.Lease, TaskEnvelope: b.TaskEnvelope, LeaseReceipt: b.LeaseReceipt, Output: same, SHA256: verify.SHA256(same)})
	if e != nil {
		t.Fatalf("Submit(w2): %v", e)
	}
	if !r.Rerun || !r.RerunOK || r.Status != "done" {
		t.Fatalf("result = %+v, want a matched re-run", r)
	}
	got, e := h.engine.Get(ctx, Thread, "developer", j.JobID)
	if e != nil {
		t.Fatalf("Get: %v", e)
	}
	if got.State != "completed" || len(got.Results) != 1 {
		t.Fatalf("job = %+v, want one accepted result", got)
	}
}

func TestReRunComparesTheExactSubmittedBytes(t *testing.T) {
	h := newHarness(t)
	pct := 100.0
	j := h.seed(t, 1, &pct)
	w1 := h.worker(t, 1)
	w2 := h.workerAs(t, "contributor-2", 1)
	ctx := context.Background()
	a, e := h.engine.Claim(ctx, Thread, "contributor", w1.TabID)
	if e != nil {
		t.Fatalf("Claim(w1): %v", e)
	}
	// The first worker submits bytes with whitespace. The stored raw message is
	// compacted by the JSON round trip, so the comparison must use the original
	// bytes: the same bytes are a match, not a mismatch.
	spaced := json.RawMessage(`{ "answer" : "same" }`)
	if _, e = h.engine.Submit(ctx, Thread, "contributor", w1.TabID, Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: spaced, SHA256: verify.SHA256(spaced)}); e != nil {
		t.Fatalf("Submit(w1): %v", e)
	}
	b, e := h.engine.Claim(ctx, Thread, "contributor-2", w2.TabID)
	if e != nil {
		t.Fatalf("Claim(w2): %v", e)
	}
	r, e := h.engine.Submit(ctx, Thread, "contributor-2", w2.TabID, Submission{Lease: b.Lease, TaskEnvelope: b.TaskEnvelope, LeaseReceipt: b.LeaseReceipt, Output: spaced, SHA256: verify.SHA256(spaced)})
	if e != nil {
		t.Fatalf("Submit(w2): %v", e)
	}
	if !r.RerunOK {
		t.Fatalf("result = %+v, want a matching re-run for identical bytes", r)
	}
	got, e := h.engine.Get(ctx, Thread, "developer", j.JobID)
	if e != nil {
		t.Fatalf("Get: %v", e)
	}
	if got.State != "completed" {
		t.Fatalf("job = %+v, want a completed job", got)
	}
}

func TestReRunNotSampledBelowRate(t *testing.T) {
	h := newHarness(t)
	pct := 0.0
	j := h.seed(t, 1, &pct)
	wk := h.worker(t, 1)
	ctx := context.Background()
	a, e := h.engine.Claim(ctx, Thread, "contributor", wk.TabID)
	if e != nil {
		t.Fatalf("Claim: %v", e)
	}
	out := json.RawMessage(`{"answer":1}`)
	r, e := h.engine.Submit(ctx, Thread, "contributor", wk.TabID, Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out)})
	if e != nil {
		t.Fatalf("Submit: %v", e)
	}
	if r.Rerun {
		t.Fatalf("result = %+v, want no re-run at 0 percent", r)
	}
	got, e := h.engine.Get(ctx, Thread, "developer", j.JobID)
	if e != nil {
		t.Fatalf("Get: %v", e)
	}
	if got.Tasks[0].Pending != nil || got.State != "completed" {
		t.Fatalf("job = %+v, want a completed job without a pending re-run", got)
	}
}

// --- 4. canary failure drives the worker to bar --------------------------------

func TestCanaryFailureBarsTheWorker(t *testing.T) {
	h := newHarness(t)
	// A canary rate of 100 % injects a canary into every task.
	cfg := h.engine.TrustConfig()
	cfg.CanaryRate = 100
	// The admin raises the auto-ban threshold, so the standing of the worker walks
	// down to bar instead of jumping to ban (R37.1b: every threshold is tunable).
	cfg.Reputation.BanCanaryFails = 100
	h.engine.SetTrustConfig(cfg)
	if _, e := h.engine.RegisterCanaryWithAnswer("canary-1", "task-canary", []byte(`{"canary":1}`), []byte(`"known answer"`)); e != nil {
		t.Fatalf("RegisterCanaryWithAnswer: %v", e)
	}

	j := h.seed(t, 1, nil)
	wk := h.worker(t, 1)
	ctx := context.Background()
	a, e := h.engine.Claim(ctx, Thread, "contributor", wk.TabID)
	if e != nil {
		t.Fatalf("Claim: %v", e)
	}
	// The canary is indistinguishable from a normal task assignment.
	if string(a.Task.Input) != `{"canary":1}` {
		t.Fatalf("task input = %s, want the canary input", a.Task.Input)
	}
	if containsAny(string(a.Task.Input), verify.SHA256([]byte(`"known answer"`))) {
		t.Errorf("canary input leaks the expected hash: %s", a.Task.Input)
	}
	// A wrong answer fails the canary.
	r, e := h.engine.Submit(ctx, Thread, "contributor", wk.TabID, Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: json.RawMessage(`"wrong answer"`), SHA256: verify.SHA256([]byte(`"wrong answer"`))})
	if e != nil {
		t.Fatalf("Submit: %v", e)
	}
	if r.Status == "done" || r.Verified {
		t.Fatalf("result = %+v, want a rejected canary", r)
	}
	if !r.Flagged {
		t.Error("a failed canary is not flagged")
	}
	// The canary failure is a reputation event.
	rep, e := h.engine.Verifier.Reputation().Worker(wk.TabID)
	if e != nil {
		t.Fatalf("Reputation.Worker: %v", e)
	}
	if rep.CanaryFails != 1 {
		t.Fatalf("canary fails = %d, want 1", rep.CanaryFails)
	}
	// One canary failure warns (score 0.75).
	if rep.Status != verify.StatusWarn {
		t.Errorf("status = %s, want warn after one canary failure", rep.Status)
	}
	// Three more canary failures take the score to 0.25: the worker is barred.
	for i := 0; i < 3; i++ {
		if _, e = h.engine.Verifier.Reputation().Observe(wk.TabID, verify.EventCanaryFail, "", "canary failed"); e != nil {
			t.Fatalf("Observe: %v", e)
		}
	}
	rep, e = h.engine.Verifier.Reputation().Worker(wk.TabID)
	if e != nil {
		t.Fatalf("Reputation.Worker: %v", e)
	}
	if rep.Status != verify.StatusBar {
		t.Fatalf("status = %s (score %v), want bar", rep.Status, rep.Score)
	}
	// A barred worker gets no lease: the eligibility gate blocks it.
	if ok, st := h.engine.eligible(wk.TabID); ok {
		t.Errorf("a barred worker is still eligible (status %q)", st)
	}
	// The job of the canary failure is flagged.
	got, e := h.engine.Get(ctx, Thread, "developer", j.JobID)
	if e != nil {
		t.Fatalf("Get: %v", e)
	}
	if !got.Tasks[0].Flagged {
		t.Errorf("task = %+v, want a flagged task", got.Tasks[0])
	}
}

func TestSuccessfulCanaryRequeuesDeveloperTask(t *testing.T) {
	h := newHarness(t)
	cfg := h.engine.TrustConfig()
	cfg.CanaryRate = 100
	cfg.RerunEnabled = false
	h.engine.SetTrustConfig(cfg)
	if _, err := h.engine.RegisterCanaryWithAnswer("canary-1", "task-canary", []byte(`{"canary":1}`), []byte(`"known answer"`)); err != nil {
		t.Fatal(err)
	}

	j := h.seed(t, 1, nil)
	wk := h.worker(t, 1)
	a, err := h.engine.Claim(context.Background(), Thread, "contributor", wk.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if string(a.Task.Input) != `{"canary":1}` {
		t.Fatalf("assignment input = %s, want canary input", a.Task.Input)
	}
	out, err := h.engine.Submit(context.Background(), Thread, "contributor", wk.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: json.RawMessage(`"known answer"`), SHA256: verify.SHA256([]byte(`"known answer"`)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "canary" || out.Verified || !out.CanaryOK {
		t.Fatalf("canary result = %+v, want non-settling canary pass", out)
	}
	got, err := h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tasks[0].State != "queued" || got.Tasks[0].CanaryID != "" || len(got.Results) != 0 {
		t.Fatalf("job after canary = %+v, want original task requeued without result", got)
	}
	next, err := h.engine.Claim(context.Background(), Thread, "contributor", wk.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if string(next.Task.Input) != `{"json":{"n":1}}` {
		t.Fatalf("requeued task input = %s, want developer input", next.Task.Input)
	}
	if next.Task.TaskID == a.Task.TaskID || next.Lease.TaskID == a.Lease.TaskID {
		t.Fatalf("developer task reused canary task id: canary=%q developer=%q", a.Task.TaskID, next.Task.TaskID)
	}
	if next.Lease.Attempt != 1 {
		t.Fatalf("developer attempt = %d, want 1 after canary", next.Lease.Attempt)
	}
	developerOutput := json.RawMessage(`{"json":{"n":1}}`)
	settled, err := h.engine.Submit(context.Background(), Thread, "contributor", wk.TabID, Submission{
		Lease: next.Lease, TaskEnvelope: next.TaskEnvelope, LeaseReceipt: next.LeaseReceipt, Output: developerOutput, SHA256: verify.SHA256(developerOutput),
	})
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != "done" || !settled.CanaryRan || settled.CanaryState != "pass" || !settled.CanaryOK || !settled.Verified || !settled.TrustComplete {
		t.Fatalf("settled developer result = %+v, want persisted canary pass evidence", settled)
	}
}

func TestCanaryPassIsBoundToResultWorker(t *testing.T) {
	h := newHarness(t)
	cfg := h.engine.TrustConfig()
	cfg.CanaryRate = 100
	cfg.RerunEnabled = false
	h.engine.SetTrustConfig(cfg)
	if _, err := h.engine.RegisterCanaryWithAnswer("canary-1", "task-canary", []byte(`{"canary":1}`), []byte(`"known answer"`)); err != nil {
		t.Fatal(err)
	}
	j := h.seed(t, 1, nil)
	workerA := h.workerAs(t, "contributor-a", 1)
	workerB := h.workerAs(t, "contributor-b", 1)
	a, err := h.engine.Claim(context.Background(), Thread, workerA.Owner, workerA.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.engine.Submit(context.Background(), Thread, workerA.Owner, workerA.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: json.RawMessage(`"known answer"`), SHA256: verify.SHA256([]byte(`"known answer"`)),
	}); err != nil {
		t.Fatal(err)
	}
	b, err := h.engine.Claim(context.Background(), Thread, workerB.Owner, workerB.TabID)
	if err != nil {
		t.Fatal(err)
	}
	out := json.RawMessage(`{"json":{"n":1}}`)
	settled, err := h.engine.Submit(context.Background(), Thread, workerB.Owner, workerB.TabID, Submission{
		Lease: b.Lease, TaskEnvelope: b.TaskEnvelope, LeaseReceipt: b.LeaseReceipt, Output: out, SHA256: verify.SHA256(out),
	})
	if err != nil {
		t.Fatal(err)
	}
	if settled.Verified || settled.TrustComplete {
		t.Fatalf("worker B inherited worker A canary trust: %+v", settled)
	}
	got, err := h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tasks[0].CanaryWorkerID != workerA.TabID || got.Tasks[0].CanaryLeaseToken != a.Lease.Token {
		t.Fatalf("canary evidence binding = %+v, want worker A lease", got.Tasks[0])
	}
}

func TestConcurrentEngineStartupUsesPersistedTrustKeys(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	l := slog.New(slog.NewJSONHandler(io.Discard, nil))
	type result struct {
		engine *Engine
		err    error
	}
	results := make(chan result, 2)
	for _, id := range []string{"s1", "s2"} {
		go func(id string) {
			engine, err := New(db, l, id, time.Minute)
			results <- result{engine, err}
		}(id)
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent New errors: %v / %v", first.err, second.err)
	}
	if string(first.engine.rerunSecret) != string(second.engine.rerunSecret) ||
		string(first.engine.TaskKey) != string(second.engine.TaskKey) {
		t.Fatal("concurrent engine startup returned split-brain trust keys")
	}
}

func TestTaskEnvelopeKeyPersistsAcrossEngineRestart(t *testing.T) {
	h := newHarness(t)
	keyID := h.engine.TaskKeyID
	key := append([]byte(nil), h.engine.TaskKey...)
	reloaded, err := New(h.engine.Store, h.engine.Log, "reloaded", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.TaskKeyID != keyID || string(reloaded.TaskKey) != string(key) {
		t.Fatalf("task key changed across restart: old=%s new=%s", keyID, reloaded.TaskKeyID)
	}
}

func TestRetiredTaskEnvelopeKeyPersistsAcrossRestartUntilOverlapEnds(t *testing.T) {
	h := newHarness(t)
	h.seed(t, 1, nil)
	wk := h.worker(t, 1)
	assignment, err := h.engine.Claim(context.Background(), Thread, "contributor", wk.TabID)
	if err != nil {
		t.Fatal(err)
	}
	oldEnvelope := assignment.TaskEnvelope
	oldKeyID := h.engine.TaskKeyID

	if err := h.engine.RotateTaskKey(context.Background(), time.Minute); err != nil {
		t.Fatalf("RotateTaskKey: %v", err)
	}
	reloaded, err := New(h.engine.Store, h.engine.Log, "reloaded", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.Now = func() time.Time { return h.now }
	reloaded.Verifier.SetClock(func() time.Time { return h.now })
	if reloaded.TaskKeyID == oldKeyID {
		t.Fatal("rotation did not publish a successor key")
	}
	if err := reloaded.VerifyTaskEnvelope(oldEnvelope); err != nil {
		t.Fatalf("retired key was not restored across restart: %v", err)
	}

	h.now = h.now.Add(time.Minute)
	if err := reloaded.VerifyTaskEnvelope(oldEnvelope); !errors.Is(err, verify.ErrUnknownKey) {
		t.Fatalf("retired key remained valid after overlap: %v", err)
	}
}

func TestNoCanaryIsExplicitlyUnverified(t *testing.T) {
	h := newHarness(t)
	cfg := h.engine.TrustConfig()
	cfg.CanaryRate = 0
	cfg.RerunEnabled = false
	h.engine.SetTrustConfig(cfg)
	h.seed(t, 1, nil)
	wk := h.worker(t, 1)
	a, err := h.engine.Claim(context.Background(), Thread, "contributor", wk.TabID)
	if err != nil {
		t.Fatal(err)
	}
	out := json.RawMessage(`{"answer":1}`)
	r, err := h.engine.Submit(context.Background(), Thread, "contributor", wk.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out),
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.CanaryState != "not-run" || r.CanaryOK || r.Verified || !r.HashOK || r.CanaryRan {
		t.Fatalf("result = %+v, want explicit not-run and unverified canary coverage", r)
	}
}

func TestRenewedLeaseAcceptsOriginalEnvelopeUntilRenewedDeadline(t *testing.T) {
	h := newHarness(t)
	cfg := h.engine.TrustConfig()
	cfg.RerunEnabled = false
	h.engine.SetTrustConfig(cfg)
	h.seed(t, 1, nil)
	wk := h.worker(t, 1)
	a, err := h.engine.Claim(context.Background(), Thread, "contributor", wk.TabID)
	if err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(50 * time.Second)
	if _, err = h.engine.Heartbeat(context.Background(), Thread, "contributor", wk.TabID, &a.Lease); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(20 * time.Second)
	out := json.RawMessage(`{"answer":1}`)
	if _, err = h.engine.Submit(context.Background(), Thread, "contributor", wk.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out),
	}); err != nil {
		t.Fatalf("Submit after renewal: %v", err)
	}
}

func TestDisabledHashCheckDoesNotClaimVerification(t *testing.T) {
	h := newHarness(t)
	cfg := h.engine.TrustConfig()
	cfg.HashCheckEnabled = false
	cfg.RerunEnabled = false
	h.engine.SetTrustConfig(cfg)
	h.seed(t, 1, nil)
	wk := h.worker(t, 1)
	a, err := h.engine.Claim(context.Background(), Thread, "contributor", wk.TabID)
	if err != nil {
		t.Fatal(err)
	}
	out := json.RawMessage(`{"answer":1}`)
	r, err := h.engine.Submit(context.Background(), Thread, "contributor", wk.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256([]byte("different")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "done" || r.Verified || r.HashOK {
		t.Fatalf("result = %+v, want accepted but unverified", r)
	}
}

func TestCanaryFailuresBanTheWorkerAtTheDefaultThreshold(t *testing.T) {
	h := newHarness(t)
	cfg := h.engine.TrustConfig()
	cfg.CanaryRate = 100
	h.engine.SetTrustConfig(cfg)
	if _, e := h.engine.RegisterCanaryWithAnswer("canary-1", "task-canary", nil, []byte("known answer")); e != nil {
		t.Fatalf("RegisterCanaryWithAnswer: %v", e)
	}
	h.seed(t, 1, nil)
	wk := h.worker(t, 1)
	ctx := context.Background()
	a, e := h.engine.Claim(ctx, Thread, "contributor", wk.TabID)
	if e != nil {
		t.Fatalf("Claim: %v", e)
	}
	_, _ = h.engine.Submit(ctx, Thread, "contributor", wk.TabID, Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: json.RawMessage(`"wrong"`), SHA256: verify.SHA256([]byte(`"wrong"`))})
	// The R37.1 default bans a worker after three canary failures.
	for i := 0; i < 2; i++ {
		if _, e = h.engine.Verifier.Reputation().Observe(wk.TabID, verify.EventCanaryFail, "", "canary failed"); e != nil {
			t.Fatalf("Observe: %v", e)
		}
	}
	rep, e := h.engine.Verifier.Reputation().Worker(wk.TabID)
	if e != nil {
		t.Fatalf("Reputation.Worker: %v", e)
	}
	if !rep.Banned || rep.Status != verify.StatusBan {
		t.Fatalf("reputation = %+v, want a banned worker after three canary failures", rep)
	}
	if ok, _ := h.engine.eligible(wk.TabID); ok {
		t.Error("a banned worker is still eligible")
	}
}

func TestBarredWorkerGetsNoLease(t *testing.T) {
	h := newHarness(t)
	h.seed(t, 1, nil)
	wk := h.worker(t, 1)
	ctx := context.Background()
	// The admin lifts the auto-ban threshold, so the standing walks down to bar.
	cfg := h.engine.TrustConfig()
	cfg.Reputation.BanCanaryFails = 100
	h.engine.SetTrustConfig(cfg)
	// Four canary failures take the score to 0: the worker is barred.
	for i := 0; i < 4; i++ {
		if _, e := h.engine.Verifier.Reputation().Observe(wk.TabID, verify.EventCanaryFail, "", "canary failed"); e != nil {
			t.Fatalf("Observe: %v", e)
		}
	}
	rep, e := h.engine.Verifier.Reputation().Worker(wk.TabID)
	if e != nil {
		t.Fatalf("Reputation.Worker: %v", e)
	}
	if rep.Status != verify.StatusBar {
		t.Fatalf("status = %s, want bar", rep.Status)
	}
	if _, e = h.engine.Claim(ctx, Thread, "contributor", wk.TabID); !errors.Is(e, ErrForbidden) {
		t.Fatalf("Claim of a barred worker = %v, want ErrForbidden", e)
	}
}

func TestCanaryNotInjectedWhenMeasureIsOff(t *testing.T) {
	h := newHarness(t)
	cfg := h.engine.TrustConfig()
	cfg.CanaryEnabled = false
	h.engine.SetTrustConfig(cfg)
	if _, e := h.engine.RegisterCanaryWithAnswer("canary-1", "task-canary", nil, []byte("known")); e != nil {
		t.Fatalf("RegisterCanaryWithAnswer: %v", e)
	}
	h.seed(t, 1, nil)
	wk := h.worker(t, 1)
	_, e := h.engine.Claim(context.Background(), Thread, "contributor", wk.TabID)
	if e != nil {
		t.Fatalf("Claim: %v", e)
	}
}

// --- 5. the switches of verify.Config -----------------------------------------

func TestTrustSwitchesComeFromVerifyConfig(t *testing.T) {
	h := newHarness(t)
	// Two tasks: the first is used for the re-run switch, the second for the
	// reputation switch.
	h.seed(t, 2, nil)
	wk := h.worker(t, 1)
	ctx := context.Background()
	a, e := h.engine.Claim(ctx, Thread, "contributor", wk.TabID)
	if e != nil {
		t.Fatalf("Claim: %v", e)
	}
	// The re-run measure switched off by the admin never runs.
	cfg := h.engine.TrustConfig()
	cfg.RerunEnabled = false
	h.engine.SetTrustConfig(cfg)
	out := json.RawMessage(`{"answer":1}`)
	r, e := h.engine.Submit(ctx, Thread, "contributor", wk.TabID, Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out)})
	if e != nil {
		t.Fatalf("Submit: %v", e)
	}
	if r.Rerun {
		t.Errorf("result = %+v, want no re-run while the measure is off", r)
	}
	// The reputation measure switched off records nothing new.
	cfg = h.engine.TrustConfig()
	cfg.ReputationEnabled = false
	h.engine.SetTrustConfig(cfg)
	before, e := h.engine.Verifier.Reputation().Worker(wk.TabID)
	if e != nil {
		t.Fatalf("Reputation.Worker: %v", e)
	}
	// W9b row 2: the second tab needs its own host id, otherwise the
	// re-register reuses the tab above (and its reputation record).
	second, e := h.engine.Register(ctx, Thread, "contributor", Worker{HostID: "host-2", Slots: 1})
	if e != nil {
		t.Fatalf("Register second: %v", e)
	}
	b, e := h.engine.Claim(ctx, Thread, "contributor", second.TabID)
	if e != nil {
		t.Fatalf("Claim: %v", e)
	}
	if _, e = h.engine.Submit(ctx, Thread, "contributor", second.TabID, Submission{Lease: b.Lease, TaskEnvelope: b.TaskEnvelope, LeaseReceipt: b.LeaseReceipt, Output: out, SHA256: verify.SHA256(out)}); e != nil {
		t.Fatalf("Submit: %v", e)
	}
	after, e := h.engine.Verifier.Reputation().Worker(wk.TabID)
	if e != nil {
		t.Fatalf("Reputation.Worker: %v", e)
	}
	if after.TotalEvents != before.TotalEvents {
		t.Errorf("reputation events = %d, want %d while the measure is off", after.TotalEvents, before.TotalEvents)
	}
	if _, e = h.engine.Verifier.Reputation().Worker(second.TabID); !errors.Is(e, verify.ErrUnknownWorker) {
		t.Errorf("Reputation.Worker = %v, want ErrUnknownWorker while the measure is off", e)
	}
	// The defaults of the engine are the defaults of R37.1.
	def := DefaultTrustConfig()
	if !def.HashCheckEnabled || !def.EnvelopeEnabled || !def.CanaryEnabled || !def.ReputationEnabled {
		t.Errorf("defaults = %+v, want every measure on", def)
	}
	if def.RerunPercent != 1 {
		t.Errorf("default re-run rate = %v, want 1", def.RerunPercent)
	}
	if def.Reputation.WarnBelow != 0.8 || def.Reputation.BarBelow != 0.5 || def.Reputation.BanCanaryFails != 3 {
		t.Errorf("default reputation thresholds = %+v, want the R37.1 defaults", def.Reputation)
	}
}

func TestEnvelopeSwitchIsHonoured(t *testing.T) {
	h := newHarness(t)
	// R11 Part E: dew defaults ON and it is itself an envelope requirement, so the
	// admin switch alone no longer decides. Disable dew to observe the switch.
	data := []byte("envelope switch module")
	id := verify.SHA256(data)
	if _, e := h.engine.Upload(context.Background(), id, "", 0, int64(len(data)), data); e != nil {
		t.Fatalf("Upload: %v", e)
	}
	if _, e := h.engine.Create(context.Background(), Thread, "developer", "", CreateRequest{
		ModuleArtifactIDs: []string{id},
		Tasks:             []TaskSpec{{TaskID: "task-env", Input: json.RawMessage(`{"json":{"n":1}}`)}},
		Options:           json.RawMessage(`{"dew":{"enabled":false}}`),
	}); e != nil {
		t.Fatalf("Create: %v", e)
	}
	wk := h.worker(t, 1)
	// The envelope measure switched off by the admin signs nothing.
	cfg := h.engine.TrustConfig()
	cfg.EnvelopeEnabled = false
	h.engine.SetTrustConfig(cfg)
	a, e := h.engine.Claim(context.Background(), Thread, "contributor", wk.TabID)
	if e != nil {
		t.Fatalf("Claim: %v", e)
	}
	if len(a.TaskEnvelope.Signature) != 0 {
		t.Errorf("assignment carries a task envelope although the measure is off: %+v", a.TaskEnvelope)
	}
}

func TestTrustStatsAreRecorded(t *testing.T) {
	h := newHarness(t)
	h.seed(t, 1, nil)
	wk := h.worker(t, 1)
	ctx := context.Background()
	a, e := h.engine.Claim(ctx, Thread, "contributor", wk.TabID)
	if e != nil {
		t.Fatalf("Claim: %v", e)
	}
	out := json.RawMessage(`{"answer":1}`)
	if _, e = h.engine.Submit(ctx, Thread, "contributor", wk.TabID, Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out)}); e != nil {
		t.Fatalf("Submit: %v", e)
	}
	s := h.engine.TrustStats()
	if s.TotalChecks == 0 {
		t.Fatalf("trust stats = %+v, want recorded checks", s)
	}
	if _, ok := s.Ops[verify.OpHash]; !ok {
		t.Errorf("trust stats have no %q op: %+v", verify.OpHash, s.Ops)
	}
}

func TestCanaryLeaseAndLegacyEnvelopeUseOpaqueWireTask(t *testing.T) {
	h := newHarness(t)
	if _, err := h.engine.RegisterCanaryWithAnswer("canary-1", "task-canary", []byte(`{"canary":1}`), []byte(`"known"`)); err != nil {
		t.Fatal(err)
	}
	h.seed(t, 1, nil)
	wk := h.worker(t, 1)
	a, err := h.engine.Claim(context.Background(), Thread, wk.Owner, wk.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(a.Task.TaskID, "canary:") {
		t.Fatalf("canary discriminator exposed on wire: %+v", a.Task)
	}
	var signed struct {
		Lease Lease    `json:"lease"`
		Task  TaskSpec `json:"task"`
	}
	if err := json.Unmarshal(a.Envelope.Payload, &signed); err != nil {
		t.Fatal(err)
	}
	if signed.Task.TaskID != a.Task.TaskID || string(signed.Task.Input) != string(a.Task.Input) {
		t.Fatalf("legacy envelope task = %+v, wire task = %+v", signed.Task, a.Task)
	}
	if _, err := h.engine.Heartbeat(context.Background(), Thread, wk.Owner, wk.TabID, &a.Lease); err != nil {
		t.Fatalf("valid canary renewal rejected: %v", err)
	}
	stale := a.Lease
	stale.Generation++
	if _, err := h.engine.Heartbeat(context.Background(), Thread, wk.Owner, wk.TabID, &stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale canary renewal = %v, want ErrConflict", err)
	}
	h.now = h.now.Add(2 * time.Minute)
	if _, err := h.engine.Heartbeat(context.Background(), Thread, wk.Owner, wk.TabID, &a.Lease); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired canary renewal = %v, want ErrConflict", err)
	}
}

// --- helpers -------------------------------------------------------------------

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
