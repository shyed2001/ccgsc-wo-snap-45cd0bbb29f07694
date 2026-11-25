// File: pools-core/internal/pool/r11_sample_test.go (go)
// R11 Part D (owner decision 2026-11-21): the server sample recompute is
// OPTIONAL. The shipped default is off, so a matmul result is accepted untouched
// until the Director sets verifyMode sample/both AND sampleRate > 0.
package pool

import (
	"context"
	"testing"
)

// TestSampleRecomputeOffByDefault is the Part D red test: default settings never
// recompute a matmul task, and the developer-led re-run default is untouched.
//
// WO R17-RECOMPUTE (G-R16-4) row 2: the recompute mode of a fresh install is
// "off", so the recompute never runs before the Director asked for it.
func TestSampleRecomputeOffByDefault(t *testing.T) {
	h := newHarness(t)
	def := DefaultPoolSettings()
	if def.SampleRate != 0 {
		t.Fatalf("DefaultSampleRate = %v, want the recompute off by default", def.SampleRate)
	}
	if def.VerifyMode != VerifyModeSample {
		t.Fatalf("default verifyMode = %q, want the developer-led re-run default %q", def.VerifyMode, VerifyModeSample)
	}
	if got := h.engine.RecomputeMode(Thread); got != RecomputeModeOff {
		t.Fatalf("default recompute mode = %q, want %q", got, RecomputeModeOff)
	}
	if DefaultRecomputeMode != RecomputeModeOff {
		t.Fatalf("DefaultRecomputeMode = %q, want %q", DefaultRecomputeMode, RecomputeModeOff)
	}
	h.disableRerun(t)
	// The registry is registered, but the mode is still off: the mode alone
	// keeps the recompute off.
	h.recomputeRegistry(t)
	j := h.matmulSeed(t, 160)
	task := &j.Tasks[0]
	if h.engine.recomputeSampled(&j, task) {
		t.Fatal("default settings recompute a matmul task")
	}
	if h.engine.PoolSettings(Thread).SampleRate != 0 {
		t.Fatalf("stored default sampleRate = %v, want 0", h.engine.PoolSettings(Thread).SampleRate)
	}
}

// TestSampleRecomputeAfterDirectorEnable is the other half of the Part D red
// test: once the Director sets a sample rate on a sampling mode the recompute
// runs again, and mode off still wins over any rate.
//
// WO R17-RECOMPUTE (G-R16-4) row 2: the recompute mode must be "registry" too,
// and the verify mode "off" still wins over any rate.
func TestSampleRecomputeAfterDirectorEnable(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.enableRecompute(t)
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeSample, RedundantRate: 0, SampleRate: 0.5, IsolationMode: IsolationSeparateLock})
	enabled := 0
	offMode := 0
	for i := 0; i < 40; i++ {
		j := h.matmulSeed(t, 160)
		j.JobID = "job-sample-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		if h.engine.recomputeSampled(&j, &j.Tasks[0]) {
			enabled++
		}
	}
	if enabled == 0 {
		t.Fatal("sampleRate 0.5 on mode sample never selected a matmul task")
	}
	h.poolSettings(t, PoolSettings{VerifyMode: VerifyModeOff, RedundantRate: 0, SampleRate: 1, IsolationMode: IsolationSeparateLock})
	for i := 0; i < 10; i++ {
		j := h.matmulSeed(t, 160)
		j.JobID = "job-off-" + string(rune('a'+i))
		if h.engine.recomputeSampled(&j, &j.Tasks[0]) {
			offMode++
		}
	}
	if offMode != 0 {
		t.Fatalf("mode off recomputed %d tasks at sampleRate 1", offMode)
	}
}

// TestDefaultSampleRateDoesNotRecomputeMatmulEndToEnd drives the whole submit
// path: a matmul result under default settings is accepted, unflagged and without
// any recompute evidence.
func TestDefaultSampleRateDoesNotRecomputeMatmulEndToEnd(t *testing.T) {
	h := newHarness(t)
	h.disableRerun(t)
	h.recomputeRegistry(t)
	j := h.matmulSeed(t, 160)
	w := h.workerAt(t, "contributor-1", "host-1", "10.26.0.5")
	a, err := h.claimAs(t, w)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	// A wrong digest would be refused by the recompute if it ran; the default
	// accepts it untouched, so the assertion is that no recompute evidence exists.
	out, err := submitAs(t, h.engine, w, a, `{"n":160,"sha256":"`+matmulRef160+`"}`)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if out.Status != "done" || out.RecomputeVerified {
		t.Fatalf("result = %+v, want an untouched default settlement", out)
	}
	got, err := h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != "completed" || got.Results[0].RecomputeVerified {
		t.Fatalf("job = %+v, want no recompute evidence under default settings", got)
	}
}
