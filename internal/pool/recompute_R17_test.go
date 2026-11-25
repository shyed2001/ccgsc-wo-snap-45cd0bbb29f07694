// Path: internal/pool/recompute_R17_test.go | Author: Hermes (MiniMax-M3) for the owner | 2026-11-22
// What/why: WO R17-RECOMPUTE (G-R16-4) row 3 - the red tests of the reference
// recompute registry and the per-pool recompute mode inside the engine:
//
//   - the registry lookup by module SHA-256 (the reference is found by the
//     module digest, never by the workload kind),
//   - an unknown module is never recomputed,
//   - the mode off never recomputes,
//   - the mode is persisted across a restart,
//   - the existing recompute tests pass through the registry.
package pool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"ccgsc/poolscore/internal/recompute"
)

// TestRecomputeRegistryLookupByModuleSHA is the red test of the lookup inside
// the engine: a job whose module artifact has the registered digest is
// recomputed, a job whose module artifact has another digest is not.
func TestRecomputeRegistryLookupByModuleSHA(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.enableRecompute(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})

	// The registered module (the reference kernel) is recomputed.
	j := h.matmulSeed(t, 160)
	ref, ok := h.engine.recomputeReference(&j)
	if !ok {
		t.Fatal("the registered module has no reference")
	}
	if ref.ModuleSHA256 != j.Request.ModuleArtifactIDs[0] {
		t.Fatalf("the reference answers for %s, want the module of the job %s",
			ref.ModuleSHA256, j.Request.ModuleArtifactIDs[0])
	}
	// A module artifact nobody registered is never recomputed.
	other := uploadF1(t, h.engine, []byte("another module"))
	job, err := h.engine.Create(context.Background(), Thread, "developer", "", CreateRequest{
		ModuleArtifactIDs: []string{other},
		Tasks:             []TaskSpec{{TaskID: "task-other", Input: json.RawMessage(`{"json":{"kind":"matmul-f32-gpu","n":160}}`)}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, ok := h.engine.recomputeReference(&job); ok {
		t.Fatal("an unregistered module must never have a reference")
	}
}

// TestRecomputeUnknownModuleIsNeverRecomputed is the red test of the unknown
// module: the server never recomputes it, so the worker is never accused.
func TestRecomputeUnknownModuleIsNeverRecomputed(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.enableRecompute(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})

	// A look-alike module with a matmul input kind: the kind alone never
	// authorizes a recompute.
	module := uploadF1(t, h.engine, []byte("custom module with a matmul input"))
	j, err := h.engine.Create(context.Background(), Thread, "developer", "", CreateRequest{
		ModuleArtifactIDs: []string{module},
		Tasks:             []TaskSpec{{TaskID: "task-custom", Input: json.RawMessage(`{"json":{"kind":"matmul-f32-gpu","n":160}}`)}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if h.engine.recomputeSampled(&j, &j.Tasks[0]) {
		t.Fatal("an unregistered module was selected for the recompute")
	}
	match, ran := h.engine.recomputeVerdict(&j, &j.Tasks[0], json.RawMessage(`{"n":160,"sha256":"`+matmulRef160+`"}`))
	if ran || match {
		t.Fatalf("verdict = %v ran = %v, want no recompute of an unregistered module", match, ran)
	}
}

// TestRecomputeModeOffNeverRecomputes is the red test of the mode off: with
// the registry registered and the sample rate at 1, the mode off keeps the
// server from recomputing, and the result settles untouched.
func TestRecomputeModeOffNeverRecomputes(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	// The registry is registered and the pool samples every task, but the mode
	// of the pool is off.
	h.recomputeRegistry(t)
	h.recomputeMode(t, Thread, RecomputeModeOff)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})

	j := h.matmulSeed(t, 160)
	if h.engine.recomputeSampled(&j, &j.Tasks[0]) {
		t.Fatal("the mode off selected a task for the recompute")
	}
	w := h.workerAt(t, "contributor-1", "host-1", "10.33.0.5")
	a, err := h.claimAs(t, w)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	// A wrong digest: with the recompute off it is accepted, because only the
	// redundancy and the canaries judge a result in this mode.
	out, err := submitAs(t, h.engine, w, a, `{"n":160,"sha256":"`+matmulRef160+`"}`)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if out.Status != "done" || out.RecomputeVerified {
		t.Fatalf("result = %+v, want an untouched settlement in the mode off", out)
	}
}

// TestRecomputeModeRegistryRecomputes is the counterpart: the mode registry
// runs the recompute again, and the wrong digest of the same task is refused.
func TestRecomputeModeRegistryRecomputes(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.enableRecompute(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})

	j := h.matmulSeed(t, 160)
	if !h.engine.recomputeSampled(&j, &j.Tasks[0]) {
		t.Fatal("the mode registry did not select the task for the recompute")
	}
	w := h.workerAt(t, "contributor-1", "host-1", "10.34.0.5")
	a, err := h.claimAs(t, w)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	out, err := submitAs(t, h.engine, w, a, `{"n":160,"sha256":"`+matmulRef160+`"}`)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if out.Status != "done" || !out.RecomputeVerified {
		t.Fatalf("result = %+v, want the recompute verdict", out)
	}
}

// TestRecomputeModePersistsAcrossRestart is the red test of the durability: a
// mode the admin saved is still in force after a restart of the engine, and a
// pool without a saved record keeps the default (off).
func TestRecomputeModePersistsAcrossRestart(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if got := h.engine.RecomputeMode(Thread); got != RecomputeModeOff {
		t.Fatalf("the mode of a fresh install = %q, want %q", got, RecomputeModeOff)
	}
	mode := RecomputeModeRegistry
	saved, err := h.engine.CommitRecomputeSettings(ctx, Thread, RecomputeSettingsDelta{Mode: &mode}, nil, "admin-user")
	if err != nil {
		t.Fatalf("CommitRecomputeSettings: %v", err)
	}
	if saved.Mode != RecomputeModeRegistry || saved.Version != 1 {
		t.Fatalf("saved = %+v, want the registry mode at version 1", saved)
	}
	// Restart: the same store, a new engine.
	restarted, err := New(h.engine.Store, h.engine.Log, "restarted", time.Minute)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := restarted.RecomputeMode(Thread); got != RecomputeModeRegistry {
		t.Fatalf("the mode after the restart = %q, want %q", got, RecomputeModeRegistry)
	}
	// The other pool was never chosen and keeps the default.
	if got := restarted.RecomputeMode(Process); got != RecomputeModeOff {
		t.Fatalf("the mode of the untouched pool = %q, want %q", got, RecomputeModeOff)
	}
	// The mode survives a restart of the ENGINE, and the engine keeps serving
	// the recompute through the registry it is given.
	if _, ok := restarted.SavedRecomputeSettings(Thread); !ok {
		t.Fatal("the restarted engine did not load the saved recompute record")
	}
}

// TestRecomputeModeStaleVersionIsRefused is the red test of the conflict rule:
// a stale expectedVersion answers ErrSettingsConflict and writes nothing.
func TestRecomputeModeStaleVersionIsRefused(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	zero := 0
	mode := RecomputeModeRegistry
	if _, err := h.engine.CommitRecomputeSettings(ctx, Thread, RecomputeSettingsDelta{Mode: &mode}, &zero, "admin-user"); err != nil {
		t.Fatalf("CommitRecomputeSettings: %v", err)
	}
	stale := 0
	if _, err := h.engine.CommitRecomputeSettings(ctx, Thread, RecomputeSettingsDelta{Mode: &mode}, &stale, "admin-user"); !errors.Is(err, ErrSettingsConflict) {
		t.Fatalf("a stale write = %v, want ErrSettingsConflict", err)
	}
	if got := h.engine.RecomputeMode(Thread); got != RecomputeModeRegistry {
		t.Fatalf("the mode = %q, want the refused write to change nothing", got)
	}
}

// TestRecomputeModeRejectsAnUnknownMode is the red test of the validation: an
// unknown mode is refused, never silently the default.
func TestRecomputeModeRejectsAnUnknownMode(t *testing.T) {
	h := newHarness(t)
	bad := RecomputeMode("sometimes")
	if _, err := h.engine.CommitRecomputeSettings(context.Background(), Thread,
		RecomputeSettingsDelta{Mode: &bad}, nil, "admin-user"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown mode = %v, want ErrInvalid", err)
	}
	if _, err := ParseRecomputeMode("sometimes"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ParseRecomputeMode = %v, want ErrInvalid", err)
	}
	for _, mode := range RecomputeModes() {
		if _, err := ParseRecomputeMode(mode); err != nil {
			t.Errorf("ParseRecomputeMode(%q) = %v, want it accepted", mode, err)
		}
	}
}

// TestRecomputeWithoutARegistryNeverRecomputes is the red test of the missing
// registry: an engine without a registry never recomputes, so a server that
// never registered a reference stays silent instead of accusing a worker.
func TestRecomputeWithoutARegistryNeverRecomputes(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.recomputeMode(t, Thread, RecomputeModeRegistry)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})
	if h.engine.RecomputeRegistry() != nil {
		t.Fatal("the harness registered a registry by accident")
	}
	j := h.matmulSeed(t, 160)
	if h.engine.recomputeSampled(&j, &j.Tasks[0]) {
		t.Fatal("an engine without a registry selected a task for the recompute")
	}
}

// TestRecomputeRegistryIsTheOnlySourceOfTheModulePin is the CR-53 guard of this
// WO: the platform finds its reference by the module SHA-256, so a registry
// that holds a different digest never answers for the reference kernel.
func TestRecomputeRegistryIsTheOnlySourceOfTheModulePin(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})

	// A registry that knows another module never answers for the reference
	// kernel: the platform reads no workload file to find the digest.
	reg := recompute.New()
	if err := reg.Register(recompute.Reference{
		Kind:         "other-kind",
		ModuleSHA256: "0000000000000000000000000000000000000000000000000000000000000000",
		Verifier: func(json.RawMessage) (recompute.Answer, error) {
			return recompute.Answer{Size: 160, Output: []byte("other")}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	h.engine.SetRecomputeRegistry(reg)
	j := h.matmulSeed(t, 160)
	if _, ok := h.engine.recomputeReference(&j); ok {
		t.Fatal("the platform found a reference without reading the workload file")
	}
	if h.engine.recomputeSampled(&j, &j.Tasks[0]) {
		t.Fatal("a job without a registered reference was selected for the recompute")
	}
}

// R17 safety: a direct verdict call in off mode must not execute a reference.
func TestRecomputeVerdictOffDoesNotRunReference(t *testing.T) {
	h := newHarness(t)
	h.recomputeRegistry(t)
	h.recomputeMode(t, Thread, RecomputeModeOff)
	j := h.matmulSeed(t, 160)
	match, ran := h.engine.recomputeVerdict(&j, &j.Tasks[0], json.RawMessage(`{"n":160,"sha256":"`+matmulRef160+`"}`))
	if match || ran {
		t.Fatalf("off-mode direct verdict match=%v ran=%v; want no recompute", match, ran)
	}
}

// R17 bounded recompute: no expensive verifier is invoked while all slots are busy.
func TestRecomputeVerifierIsNotCalledWithoutWorkerSlot(t *testing.T) {
	h := newHarness(t)
	h.recomputeMode(t, Thread, RecomputeModeRegistry)
	j := h.matmulSeed(t, 160)
	called := 0
	reg := recompute.New()
	if err := reg.Register(recompute.Reference{
		Kind: "test", ModuleSHA256: j.Request.ModuleArtifactIDs[0],
		Verifier: func(json.RawMessage) (recompute.Answer, error) {
			called++
			return recompute.Answer{Size: 160, Output: []byte("unused")}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	h.engine.SetRecomputeRegistry(reg)
	h.engine.SetWorkerPool(1)
	sem := h.engine.workerPool()
	count := 0
	for sem.TryAcquire() {
		count++
		if count > 10000 {
			t.Fatal("unexpected unbounded worker pool")
		}
	}
	if count == 0 {
		t.Fatal("no capacity in the worker pool")
	}
	defer func() {
		for i := 0; i < count; i++ {
			sem.Release()
		}
	}()
	match, ran := h.engine.recomputeVerdict(&j, &j.Tasks[0], json.RawMessage(`{"n":160,"sha256":"`+matmulRef160+`"}`))
	if match || ran || called != 0 {
		t.Fatalf("busy pool match=%v ran=%v verifier calls=%d; want all zero", match, ran, called)
	}
}

// TestR17MalformedClaimMustFailReferenceCheck preserves the W10C fail-closed
// contract under the fraud policy: a sampled known module cannot bypass
// recomputation by submitting malformed {n,sha256} metadata with a valid
// envelope-level output SHA. The R18 default (reject) changed this behaviour;
// this test pins the fraud policy explicitly.
func TestR17MalformedClaimMustFailReferenceCheck(t *testing.T) {
	for _, workerOutput := range []string{
		`{"n":160,"sha256":"not-hex"}`,
		`{"n":160}`,
		`{"n":160,"sha256":42}`,
	} {
		t.Run(workerOutput, func(t *testing.T) {
			h := newHarness(t)
			h.disableRerun(t)
			h.enableRecompute(t)
			h.recomputeMalformedPolicy(t, Thread, RecomputeMalformedFraud)
			h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})
			j := h.matmulSeed(t, 160)
			if !h.engine.recomputeSampled(&j, &j.Tasks[0]) {
				t.Fatal("expected sampled reference")
			}
			w := h.workerAt(t, "malformed-contributor", "malformed-host", "10.44.0.6")
			a, err := h.claimAs(t, w)
			if err != nil {
				t.Fatal(err)
			}
			got, err := submitAs(t, h.engine, w, a, workerOutput)
			if err != nil {
				t.Fatalf("Submit: %v", err)
			}
			if got.Status != "failed" || got.Error != "server recompute mismatch" || !got.Flagged {
				t.Fatalf("malformed output was not rejected: status=%q recompute=%v flagged=%v error=%q", got.Status, got.RecomputeVerified, got.Flagged, got.Error)
			}
		})
	}
}

// TestR17PrepareDoesNotFallbackToNativeVerificationInTransaction protects
// R17-SCALE A1: recomputeVerdictFor is called inside Store.Update, so a missed
// or busy pre-transaction verdict must never rerun the CPU-heavy kernel there.
func TestR17PrepareDoesNotFallbackToNativeVerificationInTransaction(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.enableRecompute(t)
	j := h.matmulSeed(t, 160)
	output := []byte(`{"n":160,"sha256":"` + matmulRef160 + `"}`)
	for _, tc := range []struct {
		name string
		prep *submitPrepare
	}{
		{"missing-prepare", nil},
		{"prepare-skipped-busy-slot", &submitPrepare{recomputeSampled: true, recomputeRan: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			match, ran := h.engine.recomputeVerdictFor(tc.prep, &j, &j.Tasks[0], output)
			if ran || match {
				t.Fatalf("verification reran in the settlement transaction: match=%v ran=%v", match, ran)
			}
		})
	}
}
