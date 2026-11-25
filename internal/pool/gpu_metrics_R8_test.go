// Path: internal/pool/gpu_metrics_R8_test.go
// What/why: Round 8 GPU thesis metrics (docs/THESIS_METRICS_PLAN.md GPU rows):
// record + query tests over SQLite for the gpu_adapter_detected / gpu_compile_ms /
// gpu_dispatch_ms samples and the per-task-kind gpu_vs_cpu count, plus the
// CPU-only no-sample rule (unmeasured metrics are absent, never zero).
package pool

import (
	"context"
	"testing"
	"time"

	"ccgsc/poolscore/internal/store"
)

// r8GPUJob seeds a job with n tasks, canaries off and re-run off, so every
// submission settles as a done result deterministically.
func (h *harness) r8GPUJob(t *testing.T, n int) Job {
	t.Helper()
	zero := 0.0
	j := h.seed(t, n, &zero)
	cfg := h.engine.TrustConfig()
	cfg.CanaryEnabled = false
	h.engine.SetTrustConfig(cfg)
	return j
}

// r8Submit submits one settled result for the assignment, carrying the given
// GPU measurement pointer (nil for a CPU-only result).
func (h *harness) r8Submit(t *testing.T, w Worker, a Assignment, output string, gpu *GPUResultMetrics) {
	t.Helper()
	q := lateSubmission(a, output)
	q.GPU = gpu
	if _, err := h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, q); err != nil {
		t.Fatalf("Submit: %v", err)
	}
}

// r8JobSamples queries the pool_metrics samples of one job.
func (h *harness) r8JobSamples(t *testing.T, jobID string) []store.MetricSample {
	t.Helper()
	samples, err := store.QueryMetricSamples(context.Background(), h.engine.Store, store.MetricFilter{JobID: jobID})
	if err != nil {
		t.Fatalf("QueryMetricSamples: %v", err)
	}
	return samples
}

func r8Named(samples []store.MetricSample, name string) []store.MetricSample {
	var out []store.MetricSample
	for _, s := range samples {
		if s.Name == name {
			out = append(out, s)
		}
	}
	return out
}

func r8Values(samples []store.MetricSample) []float64 {
	out := make([]float64, 0, len(samples))
	for _, s := range samples {
		out = append(out, s.Value)
	}
	return out
}

func r8Sources(samples []store.MetricSample) []string {
	out := make([]string, 0, len(samples))
	for _, s := range samples {
		out = append(out, s.Source)
	}
	return out
}

func TestR8GPUResultMetricsRecorded(t *testing.T) {
	h := newHarness(t)
	j := h.r8GPUJob(t, 3)
	w := h.worker(t, 1)
	ctx := context.Background()

	// Result 1: GPU result with all three fields measured.
	a, err := h.engine.Claim(ctx, Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatalf("Claim 1: %v", err)
	}
	detected := true
	compileMS, dispatchMS := 12.5, 3.25
	h.r8Submit(t, w, a, `{"ok":1}`, &GPUResultMetrics{AdapterDetected: &detected, CompileMS: &compileMS, DispatchMS: &dispatchMS})

	// Result 2: CPU-only result without any GPU object.
	b, err := h.engine.Claim(ctx, Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatalf("Claim 2: %v", err)
	}
	h.r8Submit(t, w, b, `{"ok":2}`, nil)

	// Result 3: GPU result with only a measured-false adapter (absent timings).
	c, err := h.engine.Claim(ctx, Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatalf("Claim 3: %v", err)
	}
	notDetected := false
	h.r8Submit(t, w, c, `{"ok":3}`, &GPUResultMetrics{AdapterDetected: &notDetected})

	samples := h.r8JobSamples(t, j.JobID)

	// gpu_adapter_detected: one sample per result that measured the adapter.
	adapter := r8Named(samples, "gpu_adapter_detected")
	if len(adapter) != 2 {
		t.Fatalf("gpu_adapter_detected samples = %d (%v), want 2 (measured true + measured false)", len(adapter), r8Values(adapter))
	}
	gotValues := append([]float64(nil), r8Values(adapter)...)
	sum := gotValues[0] + gotValues[1]
	if sum != 1 {
		t.Fatalf("gpu_adapter_detected values %v, want one 1 and one 0", gotValues)
	}
	for _, s := range adapter {
		if s.Unit != "count" || s.Source != "gpu" {
			t.Fatalf("gpu_adapter_detected sample unit/source = %q/%q, want count/gpu", s.Unit, s.Source)
		}
		if s.JobID != j.JobID {
			t.Fatalf("gpu_adapter_detected sample job = %q, want %q", s.JobID, j.JobID)
		}
	}

	// gpu_compile_ms: only the result that measured it, value preserved.
	compile := r8Named(samples, "gpu_compile_ms")
	if len(compile) != 1 || compile[0].Value != 12.5 || compile[0].Unit != "ms" || compile[0].Source != "gpu" {
		t.Fatalf("gpu_compile_ms = %+v, want one 12.5 ms gpu sample", compile)
	}

	// gpu_dispatch_ms: only the result that measured it, value preserved.
	dispatch := r8Named(samples, "gpu_dispatch_ms")
	if len(dispatch) != 1 || dispatch[0].Value != 3.25 || dispatch[0].Unit != "ms" || dispatch[0].Source != "gpu" {
		t.Fatalf("gpu_dispatch_ms = %+v, want one 3.25 ms gpu sample", dispatch)
	}

	// gpu_vs_cpu: one sample per settled result, task kind in the source label.
	vsCPU := r8Named(samples, "gpu_vs_cpu")
	if len(vsCPU) != 3 {
		t.Fatalf("gpu_vs_cpu samples = %d, want 3 (2 gpu + 1 cpu)", len(vsCPU))
	}
	kinds := map[string]int{}
	for _, s := range vsCPU {
		if s.Unit != "count" || s.Value != 1 {
			t.Fatalf("gpu_vs_cpu sample = %+v, want unit count value 1", s)
		}
		kinds[s.Source]++
	}
	if kinds["gpu"] != 2 || kinds["cpu"] != 1 {
		t.Fatalf("gpu_vs_cpu sources %v, want 2 gpu and 1 cpu", r8Sources(vsCPU))
	}

	// Query filters: scope and since narrow the same samples; an unknown job
	// yields nothing.
	scoped, err := store.QueryMetricSamples(ctx, h.engine.Store, store.MetricFilter{Scope: "tp", JobID: j.JobID, Since: time.Unix(1_699_999_999, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if len(r8Named(scoped, "gpu_vs_cpu")) != 3 {
		t.Fatalf("scoped query gpu_vs_cpu = %d, want 3", len(r8Named(scoped, "gpu_vs_cpu")))
	}
	future, err := store.QueryMetricSamples(ctx, h.engine.Store, store.MetricFilter{JobID: j.JobID, Since: time.Unix(1_700_000_001, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if len(future) != 0 {
		t.Fatalf("future since returned %d samples, want 0", len(future))
	}
	other, err := store.QueryMetricSamples(ctx, h.engine.Store, store.MetricFilter{JobID: "s1.tp.absent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("unknown job returned %d samples, want 0", len(other))
	}
}

func TestR8CPUOnlyResultWritesNoGPUTimingSamples(t *testing.T) {
	h := newHarness(t)
	j := h.r8GPUJob(t, 1)
	w := h.worker(t, 1)
	a, err := h.engine.Claim(context.Background(), Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	h.r8Submit(t, w, a, `{"ok":true}`, nil)

	samples := h.r8JobSamples(t, j.JobID)
	for _, name := range []string{"gpu_adapter_detected", "gpu_compile_ms", "gpu_dispatch_ms"} {
		if got := r8Named(samples, name); len(got) != 0 {
			t.Fatalf("CPU-only result fabricated %s: %+v", name, got)
		}
	}
	// The result still counts in the per-kind gpu_vs_cpu comparison as CPU work.
	vsCPU := r8Named(samples, "gpu_vs_cpu")
	if len(vsCPU) != 1 || vsCPU[0].Source != "cpu" || vsCPU[0].Value != 1 || vsCPU[0].Unit != "count" {
		t.Fatalf("gpu_vs_cpu = %+v, want one cpu count sample", vsCPU)
	}
}

func TestR8GPUMeasuredZeroAndFalseRecorded(t *testing.T) {
	h := newHarness(t)
	j := h.r8GPUJob(t, 2)
	w := h.worker(t, 1)
	ctx := context.Background()

	// A measured zero (compileMs=0) is a real observation and must be recorded,
	// while the unmeasured dispatchMs produces no sample at all.
	a, err := h.engine.Claim(ctx, Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	zero := 0.0
	h.r8Submit(t, w, a, `{"ok":1}`, &GPUResultMetrics{CompileMS: &zero})

	// A measured false adapter is recorded as a 0 count, not dropped.
	b, err := h.engine.Claim(ctx, Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	notDetected := false
	h.r8Submit(t, w, b, `{"ok":2}`, &GPUResultMetrics{AdapterDetected: &notDetected})

	samples := h.r8JobSamples(t, j.JobID)
	compile := r8Named(samples, "gpu_compile_ms")
	if len(compile) != 1 || compile[0].Value != 0 || compile[0].Unit != "ms" {
		t.Fatalf("measured zero gpu_compile_ms = %+v, want one 0 ms sample", compile)
	}
	if got := r8Named(samples, "gpu_dispatch_ms"); len(got) != 0 {
		t.Fatalf("absent dispatchMs fabricated gpu_dispatch_ms: %+v", got)
	}
	adapter := r8Named(samples, "gpu_adapter_detected")
	if len(adapter) != 1 || adapter[0].Value != 0 {
		t.Fatalf("measured false gpu_adapter_detected = %+v, want one 0 count sample", adapter)
	}
	if got := r8Named(samples, "gpu_vs_cpu"); len(got) != 2 {
		t.Fatalf("gpu_vs_cpu = %+v, want 2 gpu samples", got)
	}
}
