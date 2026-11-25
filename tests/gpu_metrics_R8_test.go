// Path: tests/gpu_metrics_R8_test.go
// What/why: Round 8 acceptance tests for the GPU thesis metrics (docs/THESIS_METRICS_PLAN.md
// GPU rows): a settled GPU result records gpu_adapter_detected / gpu_compile_ms /
// gpu_dispatch_ms and the per-task-kind gpu_vs_cpu count into pool_metrics (SQLite),
// they surface in GET /admin/v1/metrics and GET /metrics (Prometheus), and a
// CPU-only result writes no gpu_* timing sample at all (unmeasured = absent).
package tests

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/verify"
)

func TestR8GPUMetricsAdminAndPrometheus(t *testing.T) {
	cfg := config.Default()
	cfg.MetricsLoopbackAnonymous = true
	f, _ := setupMetricsServer(t, filepath.Join(t.TempDir(), "gpu_metrics_r8.db"), cfg)
	_, adminToken := f.key(t, "admin")
	_, devToken := f.key(t, "developer")
	_, contribToken := f.key(t, "contributor")

	module := f.artifact(t, "/thread-pool/v1", devToken)
	zero := 0.0

	// Job 1: one GPU-settled result and one CPU-only result.
	job1 := decode[pool.Job](t, f.request(t, "POST", "/thread-pool/v1/jobs", devToken, pool.CreateRequest{
		ModuleArtifactIDs: []string{module},
		Tasks: []pool.TaskSpec{
			{TaskID: "gpu-1", Input: json.RawMessage(`{"n":1}`)},
			{TaskID: "cpu-1", Input: json.RawMessage(`{"n":2}`)},
		},
		RerunPercent: &zero,
	}, http.StatusCreated, nil))

	w := decode[pool.Worker](t, f.request(t, "POST", "/thread-pool/v1/workers/register", contribToken, map[string]any{"hostId": "gpu-host", "slots": 2}, http.StatusCreated, nil))

	// First result carries the worker-measured GPU fields.
	a := decode[pool.Assignment](t, f.request(t, "POST", "/thread-pool/v1/workers/"+w.TabID+"/leases", contribToken, nil, http.StatusOK, nil))
	out1 := json.RawMessage(`{"ok":1}`)
	detected := true
	compileMS, dispatchMS := 12.5, 3.25
	f.request(t, "POST", "/thread-pool/v1/workers/"+w.TabID+"/results", contribToken, pool.Submission{
		Lease:        a.Lease,
		TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt,
		Output: out1,
		SHA256: verify.SHA256(out1),
		GPU:    &pool.GPUResultMetrics{AdapterDetected: &detected, CompileMS: &compileMS, DispatchMS: &dispatchMS},
	}, http.StatusOK, nil)

	// Second result of the same job is CPU-only: no GPU object on the wire.
	b := decode[pool.Assignment](t, f.request(t, "POST", "/thread-pool/v1/workers/"+w.TabID+"/leases", contribToken, nil, http.StatusOK, nil))
	out2 := json.RawMessage(`{"ok":2}`)
	f.request(t, "POST", "/thread-pool/v1/workers/"+w.TabID+"/results", contribToken, pool.Submission{
		Lease:        b.Lease,
		TaskEnvelope: b.TaskEnvelope, LeaseReceipt: b.LeaseReceipt,
		Output: out2,
		SHA256: verify.SHA256(out2),
	}, http.StatusOK, nil)

	// Job 2: CPU-only result.
	job2 := decode[pool.Job](t, f.request(t, "POST", "/thread-pool/v1/jobs", devToken, pool.CreateRequest{
		ModuleArtifactIDs: []string{module},
		Tasks:             []pool.TaskSpec{{TaskID: "cpu-2", Input: json.RawMessage(`{"n":3}`)}},
		RerunPercent:      &zero,
	}, http.StatusCreated, nil))
	c := decode[pool.Assignment](t, f.request(t, "POST", "/thread-pool/v1/workers/"+w.TabID+"/leases", contribToken, nil, http.StatusOK, nil))
	out3 := json.RawMessage(`{"ok":3}`)
	f.request(t, "POST", "/thread-pool/v1/workers/"+w.TabID+"/results", contribToken, pool.Submission{
		Lease:        c.Lease,
		TaskEnvelope: c.TaskEnvelope, LeaseReceipt: c.LeaseReceipt,
		Output: out3,
		SHA256: verify.SHA256(out3),
	}, http.StatusOK, nil)

	// --- /admin/v1/metrics, job 1: the four GPU names with measured values ---
	got := decode[metricsAdminResponse](t, f.request(t, "GET", "/admin/v1/metrics?job="+job1.JobID, adminToken, nil, http.StatusOK, nil))
	if got.Counters["gpu_adapter_detected"] != 1 {
		t.Fatalf("gpu_adapter_detected counter = %v, want 1", got.Counters["gpu_adapter_detected"])
	}
	compile := got.Metrics["gpu_compile_ms"]
	if compile.Count != 1 || compile.Avg != 12.5 || compile.Unit != "ms" || compile.Source != "gpu" {
		t.Fatalf("gpu_compile_ms aggregate = %+v, want count 1 avg 12.5 unit ms source gpu", compile)
	}
	dispatch := got.Metrics["gpu_dispatch_ms"]
	if dispatch.Count != 1 || dispatch.Max != 3.25 || dispatch.Unit != "ms" || dispatch.Source != "gpu" {
		t.Fatalf("gpu_dispatch_ms aggregate = %+v, want count 1 max 3.25 unit ms source gpu", dispatch)
	}
	if got.Counters["gpu_vs_cpu"] != 2 {
		t.Fatalf("gpu_vs_cpu counter = %v, want 2 (1 gpu + 1 cpu)", got.Counters["gpu_vs_cpu"])
	}
	gpuKinds := map[string]int{}
	for _, s := range got.Samples {
		if s.Name == "gpu_vs_cpu" {
			gpuKinds[s.Source]++
		}
	}
	if gpuKinds["gpu"] != 1 || gpuKinds["cpu"] != 1 {
		t.Fatalf("gpu_vs_cpu samples by kind = %v, want one gpu and one cpu", gpuKinds)
	}

	// --- /admin/v1/metrics, job 2: CPU-only writes no gpu_* timing sample ---
	cpuOnly := decode[metricsAdminResponse](t, f.request(t, "GET", "/admin/v1/metrics?job="+job2.JobID, adminToken, nil, http.StatusOK, nil))
	for _, name := range []string{"gpu_adapter_detected", "gpu_compile_ms", "gpu_dispatch_ms"} {
		if _, ok := cpuOnly.Metrics[name]; ok {
			t.Fatalf("CPU-only job fabricated %s: %+v", name, cpuOnly.Metrics[name])
		}
	}
	if cpuOnly.Counters["gpu_vs_cpu"] != 1 {
		t.Fatalf("CPU-only job gpu_vs_cpu counter = %v, want 1", cpuOnly.Counters["gpu_vs_cpu"])
	}
	for _, s := range cpuOnly.Samples {
		if s.Name == "gpu_vs_cpu" && s.Source != "cpu" {
			t.Fatalf("CPU-only job gpu_vs_cpu source = %q, want cpu", s.Source)
		}
	}

	// --- /metrics (Prometheus), job 1: all four names, right types and values ---
	prom1 := doMetricsHTTP(t, f.handler, "GET", "/metrics?job="+job1.JobID, "127.0.0.1:42001", "", nil)
	if prom1.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d: %s", prom1.Code, prom1.Body.String())
	}
	parsed := parsePrometheusText(t, prom1.Body.String())
	if parsed.types["gpu_adapter_detected"] != "counter" || parsed.types["gpu_vs_cpu"] != "counter" {
		t.Fatalf("gpu count types = %q / %q, want counter", parsed.types["gpu_adapter_detected"], parsed.types["gpu_vs_cpu"])
	}
	if parsed.types["gpu_compile_ms"] != "gauge" || parsed.types["gpu_dispatch_ms"] != "gauge" {
		t.Fatalf("gpu timing types = %q / %q, want gauge", parsed.types["gpu_compile_ms"], parsed.types["gpu_dispatch_ms"])
	}
	if sum := sumFloatSlice(parsed.values["gpu_adapter_detected"]); sum != 1 {
		t.Fatalf("Prometheus gpu_adapter_detected = %v, want 1 (body=%s)", sum, prom1.Body.String())
	}
	if sum := sumFloatSlice(parsed.values["gpu_compile_ms"]); sum != 12.5 {
		t.Fatalf("Prometheus gpu_compile_ms = %v, want 12.5", sum)
	}
	if sum := sumFloatSlice(parsed.values["gpu_dispatch_ms"]); sum != 3.25 {
		t.Fatalf("Prometheus gpu_dispatch_ms = %v, want 3.25", sum)
	}
	if vs := parsed.values["gpu_vs_cpu"]; len(vs) != 2 || sumFloatSlice(vs) != 2 {
		t.Fatalf("Prometheus gpu_vs_cpu = %v, want two series (gpu + cpu) summing to 2", vs)
	}

	// --- /metrics (Prometheus), job 2: only the cpu kind of gpu_vs_cpu ---
	prom2 := doMetricsHTTP(t, f.handler, "GET", "/metrics?job="+job2.JobID, "127.0.0.1:42002", "", nil)
	if prom2.Code != http.StatusOK {
		t.Fatalf("GET /metrics (job 2) = %d: %s", prom2.Code, prom2.Body.String())
	}
	parsed2 := parsePrometheusText(t, prom2.Body.String())
	for _, name := range []string{"gpu_adapter_detected", "gpu_compile_ms", "gpu_dispatch_ms"} {
		if _, ok := parsed2.values[name]; ok {
			t.Fatalf("CPU-only job Prometheus fabricated %s: %s", name, prom2.Body.String())
		}
	}
	if sum := sumFloatSlice(parsed2.values["gpu_vs_cpu"]); sum != 1 {
		t.Fatalf("CPU-only job Prometheus gpu_vs_cpu = %v, want 1", sum)
	}
	if !strings.Contains(prom2.Body.String(), `source="cpu"`) {
		t.Fatalf("CPU-only job Prometheus gpu_vs_cpu series misses source=\"cpu\": %s", prom2.Body.String())
	}
}
