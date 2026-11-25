// File: pools-core/internal/pool/r11_fix_test.go (go)
// R11 Part C: a canary substitution is judged by the canary only (R11-G1), the
// workload file cap is enforced on every signer and job validation (R11-G2), the
// tie-break gets a fresh deadline (R11-G3) and the G5/G6/G8 evidence rules.
package pool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"ccgsc/poolscore/internal/verify"
)

// canaryToken registers one canary fixture with its known answer.
func (h *harness) canaryToken(t *testing.T) {
	t.Helper()
	if _, err := h.engine.RegisterCanaryWithAnswer("r11-canary-1", "task-canary",
		[]byte(`{"canary":1}`), []byte(`4`)); err != nil {
		t.Fatalf("RegisterCanaryWithAnswer: %v", err)
	}
}

// forceCanary makes every task carry a canary.
func (h *harness) forceCanary(t *testing.T) {
	t.Helper()
	cfg := h.engine.TrustConfig()
	cfg.CanaryEnabled = true
	cfg.CanaryRate = 100
	h.engine.SetTrustConfig(cfg)
}

// TestCanaryAnswerNeverRecomputeJudged is the GLM red test of R11-G1: the output
// of a canary-substituted submission must never reach the server recompute (a
// canary answer is not a matmul output), and must never enter the redundant
// quorum. The canary path answers "canary" and requeues the developer task.
func TestCanaryAnswerNeverRecomputeJudged(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.forceCanary(t)
	h.canaryToken(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})
	h.enableRecompute(t)
	j := h.matmulSeed(t, 160)
	w := h.workerAt(t, "contributor-1", "host-1", "10.20.0.5")

	a, err := h.claimAs(t, w)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if a.Lease.TaskID == "task-m" {
		t.Fatal("the first lease of this task must be the canary substitution")
	}
	// The honest answer to the canary is the raw known answer, not a matmul digest,
	// and must never be judged by the recompute.
	out, err := submitAs(t, h.engine, w, a, `4`)
	if err != nil {
		t.Fatalf("Submit canary: %v", err)
	}
	if out.Status != "canary" || !out.CanaryOK || out.RecomputeVerified {
		t.Fatalf("canary result = %+v, want a canary verdict and no recompute", out)
	}
	got, err := h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Tasks[0].State != "queued" || got.Tasks[0].CanaryState != "pass" {
		t.Fatalf("task = %+v, want the developer task requeued", got.Tasks[0])
	}
	// The reputation gate must not see the canary as a failed developer result:
	// the honest worker keeps a clean record (an unknown worker has no fail).
	rep, err := h.engine.Verifier.Reputation().Worker(w.TabID)
	if err == nil && rep.Fails != 0 {
		t.Errorf("worker reputation = %+v, want no fail for the canary answer", rep)
	}
	if err != nil && !errors.Is(err, verify.ErrUnknownWorker) {
		t.Fatalf("Reputation.Worker: %v", err)
	}
}

// TestCanaryAnswerNeverEntersRedundantQuorum is the redundancy variant R11-G1 asks
// for: at redundantMode=1 the canary submission must not be held as a replica and
// must not make the task wait for a quorum.
func TestCanaryAnswerNeverEntersRedundantQuorum(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.forceCanary(t)
	h.canaryToken(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeRedundant, RedundantRate: 1, SampleRate: 0, IsolationMode: IsolationSeparateLock})
	h.seed(t, 1, nil)
	w := h.workerAt(t, "contributor-1", "host-1", "10.21.0.5")

	a, err := h.claimAs(t, w)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	out, err := submitAs(t, h.engine, w, a, `4`)
	if err != nil {
		t.Fatalf("Submit canary: %v", err)
	}
	if out.Status == StatePendingVerification || out.Redundant {
		t.Fatalf("canary result = %+v, want no redundant quorum for a canary", out)
	}
	if out.Status != "canary" {
		t.Fatalf("canary result = %+v, want the canary verdict", out)
	}
}

// TestEnvelopeRejectsMoreThanEightWorkloadFiles is the GLM red test of R11-G2 at
// the envelope layer.
func TestEnvelopeRejectsMoreThanEightWorkloadFiles(t *testing.T) {
	files := make([]verify.WorkloadFile, 0, 9)
	for i := 0; i < 9; i++ {
		files = append(files, verify.WorkloadFile{Path: "f" + string(rune('0'+i)), SHA256: verify.SHA256([]byte("f" + string(rune('0'+i))))})
	}
	env := verify.NewTaskEnvelope("job", "task", files[0].SHA256, verify.SHA256([]byte("in")), "key", time.Unix(1_700_000_000, 0), time.Minute)
	env.Workload = &verify.WorkloadDescriptor{ID: "w", Version: 1, Files: files[:8]}
	if err := env.Valid(); err != nil {
		t.Fatalf("eight files = %v, want valid", err)
	}
	env.Workload.Files = files
	if err := env.Valid(); err == nil {
		t.Fatal("nine workload files were accepted")
	}
}

// TestSignTaskEnvelopeRefusesMoreThanEightArtifacts is the R11-G2 red test at the
// signer: a job that names nine module artifacts can never get a signed envelope.
func TestSignTaskEnvelopeRefusesMoreThanEightArtifacts(t *testing.T) {
	h := newHarness(t)
	ids := make([]string, 0, 9)
	for i := 0; i < 9; i++ {
		ids = append(ids, verify.SHA256([]byte("module"+string(rune('0'+i)))))
	}
	job := Job{JobID: "job-cap", PoolID: Thread, Owner: "developer", Request: CreateRequest{ModuleArtifactIDs: ids}}
	task := Task{TaskSpec: TaskSpec{TaskID: "task-cap"}}
	lease := Lease{JobID: job.JobID, TaskID: task.TaskID, WorkerID: "tab", Token: "tok", ExpiresAt: h.now.Add(time.Minute)}
	if _, err := h.engine.signTaskEnvelope(&job, &task, lease, []byte("{}")); err == nil {
		t.Fatal("signTaskEnvelope signed a nine-artifact job")
	}
	job.Request.ModuleArtifactIDs = ids[:8]
	if _, err := h.engine.signTaskEnvelope(&job, &task, lease, []byte("{}")); err != nil {
		t.Fatalf("signTaskEnvelope(eight) = %v, want a signed envelope", err)
	}
}

// TestJobValidateRejectsMoreThanEightArtifacts is the R11-G2 red test at job
// admissions: an over-cap job is refused before it is stored.
func TestJobValidateRejectsMoreThanEightArtifacts(t *testing.T) {
	h := newHarness(t)
	ids := make([]string, 0, 9)
	for i := 0; i < 9; i++ {
		ids = append(ids, verify.SHA256([]byte("mod"+string(rune('0'+i)))))
	}
	q := CreateRequest{ModuleArtifactIDs: ids[:8], Tasks: []TaskSpec{{TaskID: "t", Input: json.RawMessage(`{"json":{}}`)}}}
	if _, _, err := h.engine.Validate(q); err != nil {
		t.Fatalf("Validate(eight) = %v, want valid", err)
	}
	q.ModuleArtifactIDs = ids
	if _, _, err := h.engine.Validate(q); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Validate(nine) = %v, want ErrInvalid", err)
	}
}

// TestTieBreakGetsFreshDeadline is the GLM red test of R11-G3: the tie-breaker
// replica gets a new window from the moment it starts, not the deadline of the
// first hold.
func TestTieBreakGetsFreshDeadline(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeRedundant, RedundantRate: 1, SampleRate: 0, IsolationMode: IsolationSeparateLock})
	j := h.seed(t, 1, nil)
	w1 := h.workerAt(t, "contributor-1", "host-1", "10.22.0.5")
	w2 := h.workerAt(t, "contributor-2", "host-2", "10.22.1.5")

	a, err := h.claimAs(t, w1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = submitAs(t, h.engine, w1, a, `{"answer":"A"}`); err != nil {
		t.Fatal(err)
	}
	got, err := h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatal(err)
	}
	holdDeadline := got.Tasks[0].Redundant.Deadline
	// Most of the first window passes before the replica answers, but not all of
	// it: the hold is still live, so the replica is accepted and starts a tie-break.
	h.now = h.now.Add(90 * time.Second)
	b, err := h.claimAs(t, w2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = submitAs(t, h.engine, w2, b, `{"answer":"B"}`); err != nil {
		t.Fatal(err)
	}
	got, err = h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatal(err)
	}
	state := got.Tasks[0].Redundant
	if state == nil || state.Phase != redundantPhaseTieBreak {
		t.Fatalf("task = %+v, want a tie-break", got.Tasks[0])
	}
	if !state.Deadline.After(holdDeadline) {
		t.Fatalf("tie-break deadline = %v, want a fresh window after %v", state.Deadline, holdDeadline)
	}
	want := h.now.Add(h.engine.reRunWindow()).UTC()
	if d := state.Deadline.Sub(want); d < -time.Second || d > time.Second {
		t.Fatalf("tie-break deadline = %v, want %v", state.Deadline, want)
	}
}

// TestReplicaLateEvidenceOnDeadline is the R11-G6 red test: a lease that is still
// live when the verification window closes leaves evidence instead of vanishing.
func TestReplicaLateEvidenceOnDeadline(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeRedundant, RedundantRate: 1, SampleRate: 0, IsolationMode: IsolationSeparateLock})
	j := h.seed(t, 1, nil)
	w1 := h.workerAt(t, "contributor-1", "host-1", "10.23.0.5")
	w2 := h.workerAt(t, "contributor-2", "host-2", "10.23.1.5")

	a, err := h.claimAs(t, w1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = submitAs(t, h.engine, w1, a, `{"answer":"A"}`); err != nil {
		t.Fatal(err)
	}
	// The replica claims and never answers; the deadline passes with its lease live.
	b, err := h.claimAs(t, w2)
	if err != nil {
		t.Fatal(err)
	}
	if b.Lease.TaskID != "task-a" {
		t.Fatalf("replica lease = %+v", b.Lease)
	}
	h.now = h.now.Add(3 * time.Minute)
	if err := h.engine.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	got, err := h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatal(err)
	}
	accepted := got.Results
	if len(accepted) != 1 || !accepted[0].Flagged {
		t.Fatalf("job results = %+v, want the first result accepted and flagged", accepted)
	}
	// R11-G6: the expiry is recorded and the still-live replica lease gets a late
	// evidence result instead of vanishing without a trace.
	samples := h.r8JobSamples(t, j.JobID)
	held := r8Named(samples, "lease_expired")
	if len(held) < 1 {
		t.Fatalf("lease_expired samples = %+v, want the replica lease recorded", held)
	}
	late := r8Named(samples, "replica_late_evidence")
	if len(late) != 1 || !strings.Contains(late[0].Source, "expired") {
		t.Fatalf("replica_late_evidence = %+v, want one expired replica evidence", late)
	}
}

// TestFreshDeadlineOnTieBreakStartOnly checks the G3 rule does not move the
// deadline of an ordinary hold.
func TestFreshDeadlineOnTieBreakStartOnly(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeRedundant, RedundantRate: 1, SampleRate: 0, IsolationMode: IsolationSeparateLock})
	h.seed(t, 1, nil)
	w := h.workerAt(t, "contributor-1", "host-1", "10.24.0.5")
	a, err := h.claimAs(t, w)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = submitAs(t, h.engine, w, a, `{"answer":"A"}`); err != nil {
		t.Fatal(err)
	}
	// The hold window starts at the first result and is unchanged by a claim.
	got, err := h.engine.Get(context.Background(), Thread, "developer", a.Lease.JobID)
	if err != nil {
		t.Fatal(err)
	}
	state := got.Tasks[0].Redundant
	if state == nil || state.Phase != redundantPhaseReplica {
		t.Fatalf("task = %+v, want the replica phase", got.Tasks[0])
	}
	want := h.now.Add(h.engine.reRunWindow()).UTC()
	if d := state.Deadline.Sub(want); d < -time.Second || d > time.Second {
		t.Fatalf("hold deadline = %v, want %v", state.Deadline, want)
	}
}

// TestHeldResultCarriesGPU is the R11-G8 red test: the accepted result rebuilt
// from held bytes still carries the GPU metrics of that submission.
func TestHeldResultCarriesGPU(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeRedundant, RedundantRate: 1, SampleRate: 0, IsolationMode: IsolationSeparateLock})
	h.seed(t, 1, nil)
	w1 := h.workerAt(t, "contributor-1", "host-1", "10.25.0.5")
	w2 := h.workerAt(t, "contributor-2", "host-2", "10.25.1.5")
	detected, compileMS, dispatchMS := true, 3.0, 4.0
	gpu := &GPUResultMetrics{AdapterDetected: &detected, CompileMS: &compileMS, DispatchMS: &dispatchMS}

	a, err := h.claimAs(t, w1)
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"answer":"same"}`)
	if _, err = h.engine.Submit(context.Background(), Thread, w1.Owner, w1.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: raw, SHA256: verify.SHA256(raw), GPU: gpu,
	}); err != nil {
		t.Fatal(err)
	}
	b, err := h.claimAs(t, w2)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.engine.Submit(context.Background(), Thread, w2.Owner, w2.TabID, Submission{
		Lease: b.Lease, TaskEnvelope: b.TaskEnvelope, LeaseReceipt: b.LeaseReceipt, Output: raw, SHA256: verify.SHA256(raw), GPU: gpu,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Redundant || second.Status != "done" {
		t.Fatalf("second = %+v, want a settled quorum", second)
	}
	// The GPU metrics of the accepted submission are recorded, not lost with the
	// held replica this result was rebuilt from.
	samples := h.r8JobSamples(t, a.Lease.JobID)
	if got := r8Named(samples, "gpu_adapter_detected"); len(got) != 1 {
		t.Fatalf("gpu_adapter_detected samples = %+v, want the quorum submission measured once", got)
	}
	if got := r8Named(samples, "gpu_compile_ms"); len(got) != 1 || got[0].Value != 3 {
		t.Fatalf("gpu_compile_ms samples = %+v, want the accepted submission's 3 ms", got)
	}
}

// TestEmptyIP24RequiresDistinctHost is the R11-G5 red test: a replica may not
// hide behind an empty /24 network.
func TestEmptyIP24RequiresDistinctHost(t *testing.T) {
	held := []HeldResult{{PendingResult: PendingResult{WorkerID: "tab-1"}, HostID: "host-1", IP24: ""}}
	if redundancyExcluded(&Task{Redundant: &RedundantState{Held: held}}, Worker{TabID: "tab-2", HostID: "host-2", ClientIP: ""}) {
		t.Fatal("two distinct hosts with no address may serve as replicas")
	}
	if !redundancyExcluded(&Task{Redundant: &RedundantState{Held: held}}, Worker{TabID: "tab-2", HostID: "host-1", ClientIP: ""}) {
		t.Fatal("the same host with no address must stay excluded")
	}
	if !redundancyExcluded(&Task{Redundant: &RedundantState{Held: held}}, Worker{TabID: "tab-2", HostID: "", ClientIP: ""}) {
		t.Fatal("an unknown host and no address may not serve as a replica")
	}
}

// TestRecomputeVerdictBeforeTx is the R11-G7 guard: the verdict of a sampled task
// is computed without touching the store, so it can run before the write tx.
func TestRecomputeVerdictBeforeTx(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})
	h.enableRecompute(t)
	j := h.matmulSeed(t, 160)
	task := &j.Tasks[0]
	verdict, ran := h.engine.recomputeVerdict(&j, task, json.RawMessage(`{"n":160,"sha256":"`+matmulRef160+`"}`))
	if !ran || !verdict {
		t.Fatalf("verdict = %v ran = %v, want a match", verdict, ran)
	}
	if !strings.Contains(string(task.Input), "160") {
		t.Fatalf("the task input changed: %s", task.Input)
	}
}
