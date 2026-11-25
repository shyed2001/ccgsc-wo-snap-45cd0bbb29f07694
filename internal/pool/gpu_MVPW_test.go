// MVPW-F, Codex, 2026-11-19: preserve absence vs measured false/zero on result wire.
package pool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMVPWGPUOptionalMetrics(t *testing.T) {
	h := newHarness(t)
	// Re-run holds and canary injection key on the engine's random sampling
	// secret (default 1 % each), which would occasionally settle this single
	// task as a canary/re-run hold instead of a done result. The wire contract
	// under test does not depend on the tamper measures, so both are off here.
	zero := 0.0
	h.seed(t, 1, &zero)
	cfg := h.engine.TrustConfig()
	cfg.CanaryEnabled = false
	h.engine.SetTrustConfig(cfg)
	w := h.worker(t, 1)
	a, err := h.engine.Claim(context.Background(), Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	q := lateSubmission(a, `{"n":160,"sha256":"raw-f32-digest"}`)
	detected := false
	q.GPU = &GPUResultMetrics{AdapterDetected: &detected}
	got, err := h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, q)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"adapterDetected":false`) || strings.Contains(string(raw), "compileMs") || strings.Contains(string(raw), "dispatchMs") {
		t.Fatalf("metrics: %s", raw)
	}
	stored, err := h.engine.Get(context.Background(), Thread, "developer", a.Lease.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Results) != 1 || stored.Results[0].GPU == nil || *stored.Results[0].GPU.AdapterDetected {
		t.Fatal("measured false not persisted")
	}
	empty, _ := json.Marshal(Result{})
	if strings.Contains(string(empty), `"gpu"`) {
		t.Fatal("fabricated GPU metric")
	}
	measured, _ := json.Marshal(GPUResultMetrics{CompileMS: &zero})
	if string(measured) != `{"compileMs":0}` {
		t.Fatalf("measured zero lost: %s", measured)
	}
}
