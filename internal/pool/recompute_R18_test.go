// Path: internal/pool/recompute_R18_test.go | WO R18-RECOMPUTE-POLICY (G-R17-1..3)
// What/why: the red tests of the per-pool recompute policy of WO R18:
//
//   - G-R17-1: an unavailable sampled verdict (verifier busy / prepared verdict
//     stale) skips by default and defers when the admin chose defer - the held
//     result is retried in the background, the retries are bounded, and the
//     bound falls back to skip (settle, accuse no one) and logs,
//   - G-R17-2: a malformed claimed digest is rejected by default (refuse the
//     submission as invalid, re-lease, no fraud mark) and only marked fraud when
//     the admin chose the fraud policy,
//   - G-R17-3: only a pinned (self-checked) reference may accuse; a recompute
//     mismatch on an unpinned size settles unverified instead of fraud.
package pool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ccgsc/poolscore/internal/matmulref"
	"ccgsc/poolscore/internal/recompute"
	"ccgsc/poolscore/internal/store"
)

// recomputeBusyPolicy sets the busy policy of one pool (G-R17-1).
func (h *harness) recomputeBusyPolicy(t *testing.T, p string, policy RecomputeBusyPolicy) {
	t.Helper()
	if _, err := h.engine.CommitRecomputeSettings(context.Background(), p,
		RecomputeSettingsDelta{BusyPolicy: &policy}, nil, "test"); err != nil {
		t.Fatalf("CommitRecomputeSettings: %v", err)
	}
}

// recomputeMalformedPolicy sets the malformed policy of one pool (G-R17-2).
func (h *harness) recomputeMalformedPolicy(t *testing.T, p string, policy RecomputeMalformedPolicy) {
	t.Helper()
	if _, err := h.engine.CommitRecomputeSettings(context.Background(), p,
		RecomputeSettingsDelta{MalformedPolicy: &policy}, nil, "test"); err != nil {
		t.Fatalf("CommitRecomputeSettings: %v", err)
	}
}

// matmulVerifier is a reference verifier that reproduces the matmul answer for
// the reference kernel module.
func matmulVerifier() recompute.Verifier {
	return func(input json.RawMessage) (recompute.Answer, error) {
		n, ok := matmulref.Size(input)
		if !ok {
			return recompute.Answer{}, recompute.ErrInvalid
		}
		out, ok := matmulref.Output(n)
		if !ok {
			return recompute.Answer{}, recompute.ErrInvalid
		}
		return recompute.Answer{Size: n, Output: out}, nil
	}
}

// registerMatmulReference registers a reference for the reference matmul kernel
// digest with the given verifier and pins (nil pins = unpinned, G-R17-3).
func (h *harness) registerMatmulReference(t *testing.T, verifier recompute.Verifier, pins map[string]string) {
	t.Helper()
	reg := recompute.New()
	if err := reg.Register(recompute.Reference{
		Kind:            "matmul-test",
		ModuleSHA256:    matmulref.ModuleSHA256,
		ReferenceHashes: pins,
		Verifier:        verifier,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	h.engine.SetRecomputeRegistry(reg)
}

// taskByID finds the developer task of a job by its task id.
func taskByID(j *Job, taskID string) *Task {
	for i := range j.Tasks {
		if j.Tasks[i].TaskID == taskID {
			return &j.Tasks[i]
		}
	}
	return nil
}

// lastResultFor returns the last persisted result of one task.
func lastResultFor(t *testing.T, j *Job, taskID string) Result {
	t.Helper()
	for i := len(j.Results) - 1; i >= 0; i-- {
		if j.Results[i].TaskID == taskID {
			return j.Results[i]
		}
	}
	t.Fatalf("no result for task %s in %+v", taskID, j.Results)
	return Result{}
}

// waitDeferredCleared polls until the deferred record of a task is gone and the
// task satisfies the condition.
func (h *harness) waitDeferredCleared(t *testing.T, jobID, taskID string, ok func(*Task) bool, desc string) Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		j, err := h.engine.Get(context.Background(), Thread, "developer", jobID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		task := taskByID(&j, taskID)
		if task == nil {
			t.Fatalf("task %s missing", taskID)
		}
		if task.DeferredRecompute == nil && ok(task) {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s: %s never became true (state=%q flagged=%v deferred=%v)", taskID, desc, task.State, task.Flagged, task.DeferredRecompute != nil)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// G-R17-1 (default): an unavailable verdict settles untouched and accuses no
// one - the reference whose own self-check always fails never produces one.
func TestR18UnavailableVerdictSkipsByDefault(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.recomputeMode(t, Thread, RecomputeModeRegistry)
	h.registerMatmulReference(t, matmulVerifier(), map[string]string{"160": strings.Repeat("0", 64)})
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})
	if got := h.engine.RecomputeBusyPolicy(Thread); got != RecomputeBusySkip {
		t.Fatalf("the default busy policy = %q, want %q", got, RecomputeBusySkip)
	}
	j := h.matmulSeed(t, 160)
	if !h.engine.recomputeSampled(&j, &j.Tasks[0]) {
		t.Fatal("the task was not sampled for the recompute")
	}
	w := h.workerAt(t, "contributor-1", "host-1", "10.55.0.5")
	a, err := h.claimAs(t, w)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	out, err := submitAs(t, h.engine, w, a, `{"n":160,"sha256":"`+matmulRef160+`"}`)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if out.Status != "done" || out.Flagged || out.RecomputeVerified {
		t.Fatalf("the skipped verdict = %+v, want an untouched settlement", out)
	}
}

// G-R17-1 (defer): the held result is retried in the background and settles
// with the recompute verdict once the reference can answer.
func TestR18DeferredRecomputeRetriesAndSettles(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	// Single-slot deployments must not reacquire an already-held recompute
	// permit: the submitted result must still receive a real verdict.
	h.engine.SetWorkerPool(1)
	h.recomputeMode(t, Thread, RecomputeModeRegistry)
	h.recomputeBusyPolicy(t, Thread, RecomputeBusyDefer)
	var calls atomic.Int32
	gate := make(chan struct{})
	h.registerMatmulReference(t, func(input json.RawMessage) (recompute.Answer, error) {
		if calls.Add(1) == 1 {
			return recompute.Answer{}, errors.New("transient reference failure")
		}
		<-gate
		return matmulVerifier()(input)
	}, matmulref.ReferenceHashes)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})
	j := h.matmulSeed(t, 160)
	w := h.workerAt(t, "contributor-1", "host-1", "10.56.0.5")
	a, err := h.claimAs(t, w)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	out, err := submitAs(t, h.engine, w, a, `{"n":160,"sha256":"`+matmulRef160+`"}`)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if out.Status != StatePendingVerification || !out.RecomputeDeferred {
		t.Fatalf("the deferred submission = %+v, want a held pending_verification result", out)
	}
	held, err := h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if task := taskByID(&held, "task-m"); task == nil || task.DeferredRecompute == nil {
		t.Fatal("the task holds no deferred recompute record")
	}
	close(gate)
	settled := h.waitDeferredCleared(t, j.JobID, "task-m", func(task *Task) bool {
		return task.State == "done"
	}, "retried recompute settling the task")
	if task := taskByID(&settled, "task-m"); task == nil || task.Flagged {
		t.Fatalf("the settled task = %+v, want done without a fraud mark", task)
	}
	res := lastResultFor(t, &settled, "task-m")
	if res.Status != "done" || !res.RecomputeVerified {
		t.Fatalf("the deferred settlement = %+v, want a verified result", res)
	}
	if calls.Load() < 2 {
		t.Fatalf("the verifier ran %d times, want the submit plus at least one retry", calls.Load())
	}
}

// G-R17-1 (retry bound): after the bounded retries the held result is accepted
// without comparison (the skip fallback), accused no one and was logged.
func TestR18DeferredRecomputeBoundedRetriesFallBackToSkip(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.recomputeMode(t, Thread, RecomputeModeRegistry)
	h.recomputeBusyPolicy(t, Thread, RecomputeBusyDefer)
	h.engine.deferRecomputeRetries = 2
	h.engine.deferRecomputeWaitDur = 20 * time.Millisecond
	var calls atomic.Int32
	h.registerMatmulReference(t, func(input json.RawMessage) (recompute.Answer, error) {
		calls.Add(1)
		return recompute.Answer{}, errors.New("reference permanently unavailable")
	}, nil)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})
	j := h.matmulSeed(t, 160)
	w := h.workerAt(t, "contributor-1", "host-1", "10.56.1.5")
	a, err := h.claimAs(t, w)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	out, err := submitAs(t, h.engine, w, a, `{"n":160,"sha256":"`+matmulRef160+`"}`)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if out.Status != StatePendingVerification || !out.RecomputeDeferred {
		t.Fatalf("the deferred submission = %+v, want a held pending_verification result", out)
	}
	settled := h.waitDeferredCleared(t, j.JobID, "task-m", func(task *Task) bool {
		return task.State == "done"
	}, "the bounded retries falling back to skip")
	res := lastResultFor(t, &settled, "task-m")
	if res.Status != "done" || res.Flagged || res.RecomputeVerified || !res.RecomputeDeferred {
		t.Fatalf("the skip fallback = %+v, want an accepted result without accusation", res)
	}
	if calls.Load() < 3 {
		t.Fatalf("the verifier ran %d times, want the submit plus the bounded retries", calls.Load())
	}
}

// G-R17-1 + G-R17-3: a deferred recompute that finally disagrees with a pinned
// (self-checked) reference still accuses the worker.
func TestR18DeferredRecomputeMismatchAccuses(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.recomputeMode(t, Thread, RecomputeModeRegistry)
	h.recomputeBusyPolicy(t, Thread, RecomputeBusyDefer)
	var calls atomic.Int32
	h.registerMatmulReference(t, func(input json.RawMessage) (recompute.Answer, error) {
		if calls.Add(1) == 1 {
			return recompute.Answer{}, errors.New("transient reference failure")
		}
		return matmulVerifier()(input)
	}, matmulref.ReferenceHashes)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})
	j := h.matmulSeed(t, 160)
	w := h.workerAt(t, "contributor-1", "host-1", "10.56.2.5")
	a, err := h.claimAs(t, w)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	out, err := submitAs(t, h.engine, w, a, `{"n":160,"sha256":"`+strings.Repeat("f", 64)+`"}`)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if out.Status != StatePendingVerification || !out.RecomputeDeferred {
		t.Fatalf("the deferred submission = %+v, want a held pending_verification result", out)
	}
	settled := h.waitDeferredCleared(t, j.JobID, "task-m", func(task *Task) bool {
		return task.Flagged && task.FlagReason == "server recompute mismatch"
	}, "the deferred mismatch accusing the worker")
	if task := taskByID(&settled, "task-m"); task == nil || task.DeferredRecompute != nil {
		t.Fatalf("the deferred record of the accused task = %+v, want it cleared", task)
	}
}

// G-R17-2 (default): a malformed claimed digest is rejected as invalid, the
// lease stays live and no fraud mark is written.
func TestR18MalformedDigestRejectsByDefault(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.enableRecompute(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})
	if got := h.engine.RecomputeMalformedPolicy(Thread); got != RecomputeMalformedReject {
		t.Fatalf("the default malformed policy = %q, want %q", got, RecomputeMalformedReject)
	}
	j := h.matmulSeed(t, 160)
	w := h.workerAt(t, "contributor-1", "host-1", "10.57.0.5")
	a, err := h.claimAs(t, w)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if _, err = submitAs(t, h.engine, w, a, `{"n":160,"sha256":"not-hex"}`); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the malformed submission = %v, want ErrInvalid", err)
	}
	got, err := h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	task := taskByID(&got, "task-m")
	if task == nil || task.State != "queued" || task.Flagged || task.Lease != nil {
		t.Fatalf("the malformed submission was not re-leased without a fraud mark: %+v", task)
	}
	// A rejected malformed claim must release the old lease; another worker
	// receives a new lease and can submit the reference answer successfully.
	other := h.workerAt(t, "contributor-2", "host-2", "10.57.0.6")
	fresh, err := h.claimAs(t, other)
	if err != nil {
		t.Fatalf("Claim after invalid submission: %v", err)
	}
	if fresh.Lease.Token == a.Lease.Token {
		t.Fatal("re-lease reused an invalidated token")
	}
	out, err := submitAs(t, h.engine, other, fresh, `{"n":160,"sha256":"`+matmulRef160+`"}`)
	if err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	if out.Status != "done" || !out.RecomputeVerified {
		t.Fatalf("the resubmitted result = %+v, want a verified settlement", out)
	}
}

// G-R17-2 (fraud): under the fraud policy a malformed claimed digest keeps the
// pre-R18 accusation (a recompute mismatch with a fraud mark).
func TestR18MalformedDigestFraudPolicyAccuses(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.enableRecompute(t)
	h.recomputeMalformedPolicy(t, Thread, RecomputeMalformedFraud)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})
	h.matmulSeed(t, 160)
	w := h.workerAt(t, "contributor-1", "host-1", "10.57.1.5")
	a, err := h.claimAs(t, w)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	out, err := submitAs(t, h.engine, w, a, `{"n":160,"sha256":"not-hex"}`)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if out.Status != "failed" || out.Error != "server recompute mismatch" || !out.Flagged {
		t.Fatalf("the malformed output under fraud = %+v, want the fraud mark", out)
	}
}

// G-R17-3: a recompute mismatch on an unpinned size never accuses - the result
// settles unverified and 2-of-N redundancy stays the only remaining judge.
func TestR18UnpinnedReferenceMismatchIsNotFraud(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.recomputeMode(t, Thread, RecomputeModeRegistry)
	h.registerMatmulReference(t, matmulVerifier(), nil)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})
	h.matmulSeed(t, 160)
	w := h.workerAt(t, "contributor-1", "host-1", "10.58.0.5")
	a, err := h.claimAs(t, w)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	out, err := submitAs(t, h.engine, w, a, `{"n":160,"sha256":"`+strings.Repeat("f", 64)+`"}`)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if out.Status != "done" || !out.RecomputeUnverified || out.Verified || out.TrustComplete {
		t.Fatalf("the unpinned mismatch = %+v, want an unverified settlement without verified/trustComplete", out)
	}
	if out.Flagged {
		t.Fatalf("an unpinned reference accused the worker: %+v", out)
	}
}

// G-R17-3 companion: an unpinned reference that agrees with the worker still
// verifies the result.
func TestR18UnpinnedReferenceMatchVerifies(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.recomputeMode(t, Thread, RecomputeModeRegistry)
	h.registerMatmulReference(t, matmulVerifier(), nil)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})
	h.matmulSeed(t, 160)
	w := h.workerAt(t, "contributor-1", "host-1", "10.58.1.5")
	a, err := h.claimAs(t, w)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	out, err := submitAs(t, h.engine, w, a, `{"n":160,"sha256":"`+matmulRef160+`"}`)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if out.Status != "done" || !out.RecomputeVerified || out.RecomputeUnverified {
		t.Fatalf("the unpinned match = %+v, want a verified settlement", out)
	}
}

// G-R17-3 guard: a pinned (self-checked) reference still accuses a mismatch.
func TestR18PinnedReferenceMismatchAccuses(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.enableRecompute(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})
	h.matmulSeed(t, 160)
	w := h.workerAt(t, "contributor-1", "host-1", "10.58.2.5")
	a, err := h.claimAs(t, w)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	out, err := submitAs(t, h.engine, w, a, `{"n":160,"sha256":"`+strings.Repeat("f", 64)+`"}`)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if out.Status != "failed" || out.Error != "server recompute mismatch" || !out.Flagged {
		t.Fatalf("the pinned mismatch = %+v, want the fraud mark", out)
	}
}

// G-R17-1..2 durability: the policies are saved and audited like the mode, and
// a legacy record without the policy fields loads the defaults.
func TestR18RecomputePoliciesPersistAuditAndLegacyDefaults(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if h.engine.RecomputeBusyPolicy(Thread) != RecomputeBusySkip ||
		h.engine.RecomputeMalformedPolicy(Thread) != RecomputeMalformedReject {
		t.Fatalf("the fresh defaults = %q/%q, want %q/%q",
			h.engine.RecomputeBusyPolicy(Thread), h.engine.RecomputeMalformedPolicy(Thread),
			RecomputeBusySkip, RecomputeMalformedReject)
	}
	bp := RecomputeBusyDefer
	mp := RecomputeMalformedFraud
	saved, err := h.engine.CommitRecomputeSettings(ctx, Thread,
		RecomputeSettingsDelta{BusyPolicy: &bp, MalformedPolicy: &mp}, nil, "admin-user")
	if err != nil {
		t.Fatalf("CommitRecomputeSettings: %v", err)
	}
	if saved.BusyPolicy != RecomputeBusyDefer || saved.MalformedPolicy != RecomputeMalformedFraud ||
		saved.Mode != RecomputeModeOff || saved.Version != 1 {
		t.Fatalf("saved = %+v, want defer/fraud on the default mode at version 1", saved)
	}
	key := h.engine.RecomputeSettingsAuditKey(Thread, saved.Version)
	var event map[string]any
	if err := h.engine.Store.View(ctx, func(r store.Reader) error {
		return r.Get(store.Audit, key, &event)
	}); err != nil {
		t.Fatalf("read the audit row: %v", err)
	}
	if event["busyPolicy"] != string(RecomputeBusyDefer) || event["malformedPolicy"] != string(RecomputeMalformedFraud) || event["actor"] != "admin-user" {
		t.Fatalf("the audit row = %+v, want the policies and the actor", event)
	}
	// Restart: the same store, a new engine.
	restarted, err := New(h.engine.Store, h.engine.Log, "restarted", time.Minute)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if restarted.RecomputeBusyPolicy(Thread) != RecomputeBusyDefer ||
		restarted.RecomputeMalformedPolicy(Thread) != RecomputeMalformedFraud {
		t.Fatalf("the policies after the restart = %q/%q, want defer/fraud",
			restarted.RecomputeBusyPolicy(Thread), restarted.RecomputeMalformedPolicy(Thread))
	}
	// A legacy record without the policy fields (a pre-R18 write) loads the
	// documented defaults instead of failing the startup.
	if err := restarted.Store.Update(ctx, func(tx store.Tx) error {
		return tx.Put(store.Metrics, recomputeSettingsKey(Process), map[string]any{"version": 1, "mode": "registry"})
	}); err != nil {
		t.Fatalf("write the legacy record: %v", err)
	}
	reloaded, err := New(restarted.Store, restarted.Log, "reloaded", time.Minute)
	if err != nil {
		t.Fatalf("New over the legacy record: %v", err)
	}
	if reloaded.RecomputeMode(Process) != RecomputeModeRegistry {
		t.Fatalf("the legacy mode = %q, want registry", reloaded.RecomputeMode(Process))
	}
	if reloaded.RecomputeBusyPolicy(Process) != RecomputeBusySkip ||
		reloaded.RecomputeMalformedPolicy(Process) != RecomputeMalformedReject {
		t.Fatalf("the legacy policies = %q/%q, want the defaults skip/reject",
			reloaded.RecomputeBusyPolicy(Process), reloaded.RecomputeMalformedPolicy(Process))
	}
}

// The policy values are validated: an unknown value is refused, never silently
// the default.
func TestR18RecomputePoliciesRejectUnknownValues(t *testing.T) {
	h := newHarness(t)
	badBusy := RecomputeBusyPolicy("sometimes")
	if _, err := h.engine.CommitRecomputeSettings(context.Background(), Thread,
		RecomputeSettingsDelta{BusyPolicy: &badBusy}, nil, "admin-user"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown busy policy = %v, want ErrInvalid", err)
	}
	badMalformed := RecomputeMalformedPolicy("sometimes")
	if _, err := h.engine.CommitRecomputeSettings(context.Background(), Thread,
		RecomputeSettingsDelta{MalformedPolicy: &badMalformed}, nil, "admin-user"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown malformed policy = %v, want ErrInvalid", err)
	}
	if _, err := ParseRecomputeBusyPolicy("sometimes"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ParseRecomputeBusyPolicy = %v, want ErrInvalid", err)
	}
	if _, err := ParseRecomputeMalformedPolicy("sometimes"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ParseRecomputeMalformedPolicy = %v, want ErrInvalid", err)
	}
	for _, p := range RecomputeBusyPolicies() {
		if _, err := ParseRecomputeBusyPolicy(p); err != nil {
			t.Errorf("ParseRecomputeBusyPolicy(%q) = %v, want it accepted", p, err)
		}
	}
	for _, p := range RecomputeMalformedPolicies() {
		if _, err := ParseRecomputeMalformedPolicy(p); err != nil {
			t.Errorf("ParseRecomputeMalformedPolicy(%q) = %v, want it accepted", p, err)
		}
	}
}

// G-R17-1 (restart resilience): a deferred record whose window expired is
// accepted without comparison by the sweep (skip fallback), so a restarted
// engine never keeps a task pending forever.
func TestR18DeferredWindowExpiryFallsBackToSkip(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.recomputeMode(t, Thread, RecomputeModeRegistry)
	h.recomputeBusyPolicy(t, Thread, RecomputeBusyDefer)
	h.registerMatmulReference(t, matmulVerifier(), matmulref.ReferenceHashes)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})
	j := h.matmulSeed(t, 160)
	w := h.workerAt(t, "contributor-1", "host-1", "10.59.0.5")
	// Hold the result directly (as the settlement does) without the submit: the
	// engine restart has no background goroutine, exactly like a restarted node.
	err := h.engine.Store.Update(context.Background(), func(tx store.Tx) error {
		var got Job
		if er := tx.Get(store.Jobs, j.JobID, &got); er != nil {
			return er
		}
		task := taskByID(&got, "task-m")
		if task == nil {
			return store.ErrNotFound
		}
		l := task.Lease
		if _, er := h.engine.deferRecomputeResult(tx, &got, task, w, l, Submission{}, Result{}, nil, nil); er != nil {
			return er
		}
		return tx.Put(store.Jobs, got.JobID, got)
	})
	if err != nil {
		t.Fatalf("hold the deferred result: %v", err)
	}
	h.now = h.now.Add(h.engine.reRunWindow() + time.Minute)
	if err := h.engine.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	got, err := h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	task := taskByID(&got, "task-m")
	if task == nil || task.DeferredRecompute != nil || task.State != "done" || task.Flagged {
		t.Fatalf("the expired deferred task = %+v, want an accepted, unaccused settlement", task)
	}
	res := lastResultFor(t, &got, "task-m")
	if res.Status != "done" || res.RecomputeVerified || !res.RecomputeDeferred {
		t.Fatalf("the sweep fallback = %+v, want an accepted result without a verdict", res)
	}
}

// G-R17-3: a purportedly pinned reference is only trustworthy after its
// self-check. A worker's claimed size differs, but a defective local reference
// must not accuse the worker before validating its own published pin.
func TestR18PinnedReferenceSelfCheckPrecedesSizeMismatch(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.recomputeMode(t, Thread, RecomputeModeRegistry)
	h.registerMatmulReference(t, matmulVerifier(), map[string]string{"160": strings.Repeat("0", 64)})
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})
	h.matmulSeed(t, 160)
	w := h.workerAt(t, "contributor-ref", "host-ref", "10.58.4.5")
	a, err := h.claimAs(t, w)
	if err != nil {
		t.Fatal(err)
	}
	res, err := submitAs(t, h.engine, w, a, `{"n":240,"sha256":"`+strings.Repeat("f", 64)+`"}`)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.Status != "done" || res.Flagged || res.RecomputeVerified {
		t.Fatalf("incorrectly accused worker using a reference that fails its own pin: %+v", res)
	}
}
