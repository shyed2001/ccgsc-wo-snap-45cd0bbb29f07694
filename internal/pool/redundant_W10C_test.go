// File: pools-core/internal/pool/redundant_W10C_test.go (go)
// W10C-SERVER row 4: quorum match/mismatch/tie-break, the distinct-worker Sybil
// guard, the server sample recompute, settings round-trip and the signed
// envelope isolation mode.
package pool

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"sync"
	"testing"
	"time"

	"ccgsc/poolscore/internal/matmulref"
	"ccgsc/poolscore/internal/recompute"
	"ccgsc/poolscore/internal/verify"
	"ccgsc/poolscore/web"
)

const (
	matmulRef160 = "1a5a6c39c56eada3b4ca01eaed913972ed5e48d1318884748e1abf3646abc2d2"
	matmulRef240 = "049db7c32a56cc68f03c9bff543f27f7733d1510104782ccc4510e5754cb320c"
)

// recomputeRegistry builds the reference-recompute registry of a test engine
// and registers the matmul reference in it.
//
// WO R17-RECOMPUTE (G-R16-4) row 1: the platform holds a GENERIC registry and
// never knows the matmul kernel. The reference registers itself from the test
// layer (or from the main wiring, cmd/pools-core/main.go), so internal/pool
// never imports workload code (owner hard rule CR-53).
func (h *harness) recomputeRegistry(t *testing.T) *recompute.Registry {
	t.Helper()
	reg := recompute.New()
	if err := matmulref.Register(reg); err != nil {
		t.Fatalf("register the matmul reference: %v", err)
	}
	h.engine.SetRecomputeRegistry(reg)
	return reg
}

// recomputeMode sets the recompute mode of one pool. The default mode is off,
// so every test that expects a recompute must ask for the registry mode
// explicitly.
func (h *harness) recomputeMode(t *testing.T, p string, mode RecomputeMode) {
	t.Helper()
	if _, err := h.engine.CommitRecomputeSettings(context.Background(), p,
		RecomputeSettingsDelta{Mode: &mode}, nil, "test"); err != nil {
		t.Fatalf("CommitRecomputeSettings: %v", err)
	}
}

// enableRecompute registers the matmul reference and turns the recompute of
// the thread pool on. It is the setup of every test that expects the server to
// recompute a sampled result.
func (h *harness) enableRecompute(t *testing.T) {
	t.Helper()
	h.recomputeRegistry(t)
	h.recomputeMode(t, Thread, RecomputeModeRegistry)
}

func (h *harness) disableRerun(t *testing.T) {
	t.Helper()
	cfg := h.engine.TrustConfig()
	cfg.RerunEnabled = false
	h.engine.SetTrustConfig(cfg)
}

func (h *harness) poolSettings(t *testing.T, s PoolSettings) {
	t.Helper()
	if _, err := h.engine.SetPoolSettings(context.Background(), Thread, s, nil); err != nil {
		t.Fatalf("SetPoolSettings: %v", err)
	}
}

func (h *harness) workerAt(t *testing.T, owner, host, clientIP string) Worker {
	t.Helper()
	w, err := h.engine.Register(context.Background(), Thread, owner, Worker{HostID: host, Slots: 1, ClientIP: clientIP})
	if err != nil {
		t.Fatalf("Register(%s/%s): %v", owner, host, err)
	}
	return w
}

// matmulSeed creates a one task job whose module is the embedded reference
// matmul kernel and whose input is a matmul workload input.
func (h *harness) matmulSeed(t *testing.T, n int) Job {
	t.Helper()
	kernel, err := fs.ReadFile(web.Workloads, "workloads/matmul/matmul_kernel.js")
	if err != nil {
		t.Fatalf("read reference kernel: %v", err)
	}
	module := uploadF1(t, h.engine, kernel)
	input, err := json.Marshal(map[string]any{"json": map[string]any{"kind": matmulref.Kind, "n": n}})
	if err != nil {
		t.Fatal(err)
	}
	j, err := h.engine.Create(context.Background(), Thread, "developer", "", CreateRequest{
		ModuleArtifactIDs: []string{module},
		Tasks:             []TaskSpec{{TaskID: "task-m", Input: input}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return j
}

func submitAs(t *testing.T, e *Engine, w Worker, a Assignment, out string) (Result, error) {
	t.Helper()
	raw := json.RawMessage(out)
	return e.Submit(context.Background(), Thread, w.Owner, w.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt,
		Output: raw, SHA256: verify.SHA256(raw),
	})
}

func (h *harness) claimAs(t *testing.T, w Worker) (Assignment, error) {
	t.Helper()
	return h.engine.Claim(context.Background(), Thread, w.Owner, w.TabID)
}

func TestRedundantQuorumMatchSettlesBothWorkers(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeRedundant, RedundantRate: 1, SampleRate: 0, IsolationMode: IsolationSeparateLock})
	j := h.seed(t, 1, nil)
	w1 := h.workerAt(t, "contributor-1", "host-1", "10.0.0.5")
	w2 := h.workerAt(t, "contributor-2", "host-2", "10.0.2.5")

	a, err := h.claimAs(t, w1)
	if err != nil {
		t.Fatalf("Claim(w1): %v", err)
	}
	first, err := submitAs(t, h.engine, w1, a, `{"answer":"same"}`)
	if err != nil {
		t.Fatalf("Submit(w1): %v", err)
	}
	if first.Status != StatePendingVerification || !first.Redundant {
		t.Fatalf("first result = %+v, want a held result pending verification", first)
	}
	got, err := h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Tasks[0].State != StatePendingVerification || got.Tasks[0].Redundant == nil || got.Tasks[0].Redundant.Phase != redundantPhaseReplica {
		t.Fatalf("task = %+v, want PENDING_VERIFICATION in the replica phase", got.Tasks[0])
	}
	if got.State == "completed" {
		t.Fatal("job completed before the quorum")
	}

	b, err := h.claimAs(t, w2)
	if err != nil {
		t.Fatalf("Claim(w2): %v", err)
	}
	if b.Lease.WorkerID == w1.TabID {
		t.Fatal("the replica went to the same tab")
	}
	second, err := submitAs(t, h.engine, w2, b, `{"answer":"same"}`)
	if err != nil {
		t.Fatalf("Submit(w2): %v", err)
	}
	if second.Status != "done" || !second.Redundant || second.RedundantReplicas != 2 {
		t.Fatalf("second result = %+v, want a settled 2-of-2 quorum", second)
	}
	got, err = h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != "completed" || got.Tasks[0].State != "done" || got.Tasks[0].Redundant != nil || len(got.Results) != 1 {
		t.Fatalf("job = %+v, want one completed result", got)
	}
	for _, w := range []Worker{w1, w2} {
		rep, err := h.engine.Verifier.Reputation().Worker(w.TabID)
		if err != nil {
			t.Fatalf("Reputation.Worker(%s): %v", w.TabID, err)
		}
		if rep.Passes != 1 || rep.Fails != 0 {
			t.Errorf("worker %s reputation = %+v, want one pass", w.TabID, rep)
		}
	}
}

func TestRedundantMismatchTieBreakMajorityWins(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeRedundant, RedundantRate: 1, SampleRate: 0, IsolationMode: IsolationSeparateLock})
	j := h.seed(t, 1, nil)
	w1 := h.workerAt(t, "contributor-1", "host-1", "10.1.0.5")
	w2 := h.workerAt(t, "contributor-2", "host-2", "10.1.1.5")
	w3 := h.workerAt(t, "contributor-3", "host-3", "10.1.2.5")

	a, err := h.claimAs(t, w1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = submitAs(t, h.engine, w1, a, `{"answer":"A"}`); err != nil {
		t.Fatal(err)
	}
	b, err := h.claimAs(t, w2)
	if err != nil {
		t.Fatal(err)
	}
	mismatch, err := submitAs(t, h.engine, w2, b, `{"answer":"B"}`)
	if err != nil {
		t.Fatal(err)
	}
	if mismatch.Status != StatePendingVerification || !mismatch.Flagged {
		t.Fatalf("mismatch result = %+v, want a pending tie-break", mismatch)
	}
	got, err := h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tasks[0].Redundant == nil || got.Tasks[0].Redundant.Phase != redundantPhaseTieBreak || got.Tasks[0].Redundant.Target != 3 {
		t.Fatalf("task = %+v, want one tie-breaker replica", got.Tasks[0])
	}

	c, err := h.claimAs(t, w3)
	if err != nil {
		t.Fatalf("Claim(w3): %v", err)
	}
	settled, err := submitAs(t, h.engine, w3, c, `{"answer":"A"}`)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != "done" || !settled.Redundant || settled.RedundantReplicas != 3 {
		t.Fatalf("tie-break result = %+v, want the majority result", settled)
	}
	if settled.SHA256 != verify.SHA256([]byte(`{"answer":"A"}`)) {
		t.Fatalf("accepted hash = %s, want the majority hash", settled.SHA256)
	}
	got, err = h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "completed" || len(got.Results) != 1 || got.Results[0].SHA256 != settled.SHA256 {
		t.Fatalf("job = %+v, want the majority result settled", got)
	}
	for _, w := range []Worker{w1, w3} {
		rep, err := h.engine.Verifier.Reputation().Worker(w.TabID)
		if err != nil {
			t.Fatal(err)
		}
		if rep.Passes != 1 || rep.Fails != 0 {
			t.Errorf("winner %s reputation = %+v, want one pass", w.TabID, rep)
		}
	}
	loser, err := h.engine.Verifier.Reputation().Worker(w2.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if loser.Fails != 1 || loser.Passes != 0 {
		t.Errorf("tie-break loser reputation = %+v, want one fail and no pass", loser)
	}
}

func TestRedundantReplicaRefusesSameIP24(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeRedundant, RedundantRate: 1, SampleRate: 0, IsolationMode: IsolationSeparateLock})
	h.seed(t, 1, nil)
	w1 := h.workerAt(t, "contributor-1", "host-1", "10.2.3.4")
	sameNetwork := h.workerAt(t, "contributor-2", "host-2", "10.2.3.99")
	sameHost := h.workerAt(t, "contributor-3", "host-1", "10.9.0.4")
	distinct := h.workerAt(t, "contributor-4", "host-4", "10.3.3.4")

	a, err := h.claimAs(t, w1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = submitAs(t, h.engine, w1, a, `{"answer":"same"}`); err != nil {
		t.Fatal(err)
	}
	if _, err = h.claimAs(t, sameNetwork); !errors.Is(err, ErrEmpty) {
		t.Fatalf("Claim(same /24) = %v, want ErrEmpty", err)
	}
	if _, err = h.claimAs(t, sameHost); !errors.Is(err, ErrEmpty) {
		t.Fatalf("Claim(same host) = %v, want ErrEmpty", err)
	}
	if _, err = h.claimAs(t, distinct); err != nil {
		t.Fatalf("Claim(distinct) = %v, want a replica lease", err)
	}
}

func TestRedundantEveryTaskOfAnUnknownWorker(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeRedundant, RedundantRate: 0, SampleRate: 0, IsolationMode: IsolationSeparateLock})
	j := h.seed(t, 1, nil)
	w1 := h.workerAt(t, "contributor-1", "host-1", "10.4.0.5")
	w2 := h.workerAt(t, "contributor-2", "host-2", "10.4.1.5")

	a, err := h.claimAs(t, w1)
	if err != nil {
		t.Fatal(err)
	}
	first, err := submitAs(t, h.engine, w1, a, `{"answer":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != StatePendingVerification {
		t.Fatalf("first task of an unknown worker = %+v, want a replica at rate 0", first)
	}
	b, err := h.claimAs(t, w2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = submitAs(t, h.engine, w2, b, `{"answer":"x"}`); err != nil {
		t.Fatal(err)
	}

	// The same worker is known now: at rate 0 its next task settles directly.
	module := j.Request.ModuleArtifactIDs[0]
	second, err := h.engine.Create(context.Background(), Thread, "developer", "", CreateRequest{
		ModuleArtifactIDs: []string{module},
		Tasks:             []TaskSpec{{TaskID: "task-known", Input: json.RawMessage(`{"json":{"n":1}}`)}},
	})
	if err != nil {
		t.Fatalf("Create second job: %v", err)
	}
	c, err := h.engine.Claim(context.Background(), Thread, w1.Owner, w1.TabID)
	if err != nil {
		t.Fatalf("Claim known worker: %v", err)
	}
	if c.Lease.JobID != second.JobID {
		t.Fatalf("claimed job = %s, want %s", c.Lease.JobID, second.JobID)
	}
	direct, err := submitAs(t, h.engine, w1, c, `{"answer":"y"}`)
	if err != nil {
		t.Fatal(err)
	}
	if direct.Status != "done" || direct.Redundant {
		t.Fatalf("known worker at rate 0 = %+v, want a direct settlement", direct)
	}
}

func TestRedundantDeadlineAcceptsFirstResult(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeRedundant, RedundantRate: 1, SampleRate: 0, IsolationMode: IsolationSeparateLock})
	j := h.seed(t, 1, nil)
	w1 := h.workerAt(t, "contributor-1", "host-1", "10.5.0.5")

	a, err := h.claimAs(t, w1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = submitAs(t, h.engine, w1, a, `{"answer":"first"}`); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(3 * time.Minute)
	if err := h.engine.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	got, err := h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "completed" || len(got.Results) != 1 {
		t.Fatalf("job = %+v, want the first result accepted at the deadline", got)
	}
	accepted := got.Results[0]
	if !accepted.Flagged || accepted.WorkerID != w1.TabID || accepted.RedundantReplicas != 1 {
		t.Fatalf("deadline result = %+v, want a flagged single-replica result", accepted)
	}
}

func TestServerRecomputeMatchesNativeKernel(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})
	h.enableRecompute(t)
	j := h.matmulSeed(t, 160)
	w := h.workerAt(t, "contributor-1", "host-1", "10.6.0.5")

	a, err := h.claimAs(t, w)
	if err != nil {
		t.Fatal(err)
	}
	out, err := submitAs(t, h.engine, w, a, `{"n":160,"sha256":"`+matmulRef160+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "done" || !out.RecomputeVerified {
		t.Fatalf("result = %+v, want a recompute-verified settlement", out)
	}
	got, err := h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "completed" || !got.Results[0].RecomputeVerified {
		t.Fatalf("job = %+v, want the recompute evidence stored", got)
	}
}

func TestServerRecomputeMismatchRequeuesAndFailsWorker(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})
	h.enableRecompute(t)
	j := h.matmulSeed(t, 160)
	w := h.workerAt(t, "contributor-1", "host-1", "10.7.0.5")

	a, err := h.claimAs(t, w)
	if err != nil {
		t.Fatal(err)
	}
	bad := `{"n":160,"sha256":"` + verify.SHA256([]byte("not the matmul bytes")) + `"}`
	out, err := submitAs(t, h.engine, w, a, bad)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "failed" || out.Error != "server recompute mismatch" || !out.Flagged {
		t.Fatalf("result = %+v, want a recompute mismatch", out)
	}
	got, err := h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tasks[0].State != "queued" || !got.Tasks[0].Flagged {
		t.Fatalf("task = %+v, want the task re-queued", got.Tasks[0])
	}
	rep, err := h.engine.Verifier.Reputation().Worker(w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Fails != 1 {
		t.Errorf("worker reputation = %+v, want one fail", rep)
	}

	// The re-queued task is claimable again and settles with the correct output.
	b, err := h.claimAs(t, w)
	if err != nil {
		t.Fatalf("Claim after mismatch: %v", err)
	}
	good, err := submitAs(t, h.engine, w, b, `{"n":160,"sha256":"`+matmulRef160+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if good.Status != "done" || !good.RecomputeVerified {
		t.Fatalf("retry result = %+v, want the correct output accepted", good)
	}
}

func TestServerRecomputeIgnoresLookAlikeModule(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})
	h.enableRecompute(t)
	// The matmul input kind alone never authorizes the native kernel: a custom
	// module keeps its own output and its worker is not accused.
	module := uploadF1(t, h.engine, []byte("custom module with a matmul input"))
	j, err := h.engine.Create(context.Background(), Thread, "developer", "", CreateRequest{
		ModuleArtifactIDs: []string{module},
		Tasks:             []TaskSpec{{TaskID: "task-custom", Input: json.RawMessage(`{"json":{"kind":"matmul-f32-gpu","n":160}}`)}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	w := h.workerAt(t, "contributor-1", "host-1", "10.10.0.5")
	a, err := h.claimAs(t, w)
	if err != nil {
		t.Fatal(err)
	}
	out, err := submitAs(t, h.engine, w, a, `{"n":160,"sha256":"`+verify.SHA256([]byte("custom output"))+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "done" || out.RecomputeVerified {
		t.Fatalf("look-alike module result = %+v, want an untouched settlement", out)
	}
	got, err := h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "completed" {
		t.Fatalf("job = %+v, want completed", got)
	}
}

func TestNativeMatmulMatchesPublishedReferences(t *testing.T) {
	for n, want := range map[int]string{160: matmulRef160, 240: matmulRef240} {
		got, ok := matmulref.Hash(n)
		if !ok {
			t.Fatalf("matmulref.Hash(%d) unavailable", n)
		}
		if got != want {
			t.Errorf("matmulref.Hash(%d) = %s, want %s", n, got, want)
		}
	}
	if _, ok := matmulref.Output(257); ok {
		t.Error("the recompute accepted a size above 256")
	}
	if _, ok := matmulref.Output(0); ok {
		t.Error("the recompute accepted size 0")
	}
}

func TestPoolSettingsRoundTripAndVersionConflict(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	def := h.engine.PoolSettings(Thread)
	if def.Version != 0 || def.VerifyMode != VerifyModeSample || def.RedundantRate != DefaultRedundantRate ||
		def.SampleRate != DefaultSampleRate || def.IsolationMode != IsolationSeparateLock {
		t.Fatalf("defaults = %+v, want the documented settings", def)
	}
	zero := 0
	saved, err := h.engine.SetPoolSettings(ctx, Thread, PoolSettings{
		VerifyMode: VerifyModeBoth, RedundantRate: 0.25, SampleRate: 0.5, IsolationMode: IsolationLockOnly,
	}, &zero)
	if err != nil {
		t.Fatalf("SetPoolSettings: %v", err)
	}
	if saved.Version != 1 || saved.VerifyMode != VerifyModeBoth || saved.IsolationMode != IsolationLockOnly {
		t.Fatalf("saved = %+v, want version 1", saved)
	}
	if _, err = h.engine.SetPoolSettings(ctx, Thread, saved, &zero); !errors.Is(err, ErrSettingsConflict) {
		t.Fatalf("stale write = %v, want ErrSettingsConflict", err)
	}
	if _, err = h.engine.SetPoolSettings(ctx, Thread, PoolSettings{VerifyMode: "sometimes", IsolationMode: IsolationLockOnly}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid mode = %v, want ErrInvalid", err)
	}
	if _, err = h.engine.SetPoolSettings(ctx, Thread, PoolSettings{VerifyMode: VerifyModeOff, RedundantRate: 2, IsolationMode: IsolationLockOnly}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid rate = %v, want ErrInvalid", err)
	}
	reloaded, err := New(h.engine.Store, h.engine.Log, "reloaded", time.Minute)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := reloaded.PoolSettings(Thread)
	if got.Version != 1 || got.VerifyMode != VerifyModeBoth || got.RedundantRate != 0.25 || got.SampleRate != 0.5 || got.IsolationMode != IsolationLockOnly {
		t.Fatalf("reloaded = %+v, want the durable settings", got)
	}
	if other := reloaded.PoolSettings(Process); other.Version != 0 || other.VerifyMode != VerifyModeSample {
		t.Fatalf("process pool = %+v, want untouched defaults", other)
	}
}

// TestUpdatePoolSettingsMergesDeltaFromStoredRecord pins R11-D1 at the engine:
// two deltas naming disjoint fields merge onto each other, and a delta that
// names the W10D contributor controls carries them without a full record.
func TestUpdatePoolSettingsMergesDeltaFromStoredRecord(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sample, redundant := 0.5, 0.3
	controls := ContributorControlsPauseResume
	saved, err := h.engine.UpdatePoolSettings(ctx, Thread, SettingsDelta{
		SampleRate:          &sample,
		ContributorControls: &controls,
	}, nil)
	if err != nil {
		t.Fatalf("UpdatePoolSettings: %v", err)
	}
	if saved.SampleRate != 0.5 || saved.ContributorControls != ContributorControlsPauseResume || saved.Version != 1 {
		t.Fatalf("saved = %+v, want the sample rate and the contributor controls", saved)
	}
	saved, err = h.engine.UpdatePoolSettings(ctx, Thread, SettingsDelta{RedundantRate: &redundant}, nil)
	if err != nil {
		t.Fatalf("UpdatePoolSettings: %v", err)
	}
	if saved.SampleRate != 0.5 || saved.RedundantRate != 0.3 || saved.ContributorControls != ContributorControlsPauseResume {
		t.Fatalf("merged = %+v, want both deltas and the carried controls", saved)
	}
	got := h.engine.PoolSettings(Thread)
	if got.VerifyMode != VerifyModeSample || got.IsolationMode != IsolationSeparateLock || got.Version != 2 {
		t.Fatalf("stored = %+v, want the unnamed defaults kept", got)
	}
}

// TestUpdatePoolSettingsStaleVersionBeatsInvalidDelta pins R11-D2 at the
// engine: a stale expectedVersion answers ErrSettingsConflict even when the
// merged record would be invalid, and a fresh one still answers ErrInvalid.
func TestUpdatePoolSettingsStaleVersionBeatsInvalidDelta(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	zero := 0
	if _, err := h.engine.UpdatePoolSettings(ctx, Thread, SettingsDelta{}, &zero); err != nil {
		t.Fatalf("UpdatePoolSettings: %v", err)
	}
	bad := VerifyMode("sometimes")
	if _, err := h.engine.UpdatePoolSettings(ctx, Thread, SettingsDelta{VerifyMode: &bad}, &zero); !errors.Is(err, ErrSettingsConflict) {
		t.Fatalf("stale invalid delta = %v, want ErrSettingsConflict", err)
	}
	one := 1
	if _, err := h.engine.UpdatePoolSettings(ctx, Thread, SettingsDelta{VerifyMode: &bad}, &one); !errors.Is(err, ErrInvalid) {
		t.Fatalf("fresh invalid delta = %v, want ErrInvalid", err)
	}
	if got := h.engine.PoolSettings(Thread); got.Version != 1 || got.VerifyMode != VerifyModeSample {
		t.Fatalf("settings = %+v, want the refused writes to change nothing", got)
	}
}

// TestUpdatePoolSettingsConcurrentDeltas runs the two-writer merge against the
// engine itself, so -race covers the settingsMu window.
func TestUpdatePoolSettingsConcurrentDeltas(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sample, redundant := 0.5, 0.3
	controls := ContributorControlsPauseResume
	for i := 0; i < 50; i++ {
		start := make(chan struct{})
		errs := make([]error, 3)
		var wg sync.WaitGroup
		wg.Add(3)
		for n, delta := range []SettingsDelta{
			{SampleRate: &sample},
			{RedundantRate: &redundant},
			{ContributorControls: &controls},
		} {
			n, delta := n, delta
			go func() {
				defer wg.Done()
				<-start
				_, errs[n] = h.engine.UpdatePoolSettings(ctx, Thread, delta, nil)
			}()
		}
		close(start)
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatalf("iteration %d: UpdatePoolSettings: %v", i, err)
			}
		}
		got := h.engine.PoolSettings(Thread)
		if got.SampleRate != 0.5 || got.RedundantRate != 0.3 || got.ContributorControls != ContributorControlsPauseResume {
			t.Fatalf("iteration %d: settings = %+v, want every delta preserved", i, got)
		}
	}
}

func TestSignedEnvelopeCarriesIsolationMode(t *testing.T) {
	h := newHarness(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeOff, RedundantRate: 0, SampleRate: 0, IsolationMode: IsolationLockOnly})
	h.seed(t, 1, nil)
	w := h.workerAt(t, "contributor-1", "host-1", "10.8.0.5")
	a, err := h.claimAs(t, w)
	if err != nil {
		t.Fatal(err)
	}
	env, err := verify.DecodeTaskEnvelope(a.TaskEnvelope)
	if err != nil {
		t.Fatalf("DecodeTaskEnvelope: %v", err)
	}
	if env.IsolationMode != string(IsolationLockOnly) {
		t.Fatalf("envelope isolationMode = %q, want %q", env.IsolationMode, IsolationLockOnly)
	}
	if err = h.engine.VerifyTaskEnvelope(a.TaskEnvelope); err != nil {
		t.Fatalf("the isolation mode broke the signature: %v", err)
	}
	if def := DefaultPoolSettings().IsolationMode; def != IsolationSeparateLock {
		t.Fatalf("default isolation = %q, want %q", def, IsolationSeparateLock)
	}
}
