// MVPW-F, Codex, 2026-11-19: optional worker-reported WebGPU measurements.
package pool

import "math"

// Pointers distinguish an absent measurement from a measured false or zero.
// DispatchMS is wall time from command encoding through readback completion.
type GPUResultMetrics struct {
	AdapterDetected *bool    `json:"adapterDetected,omitempty"`
	CompileMS       *float64 `json:"compileMs,omitempty"`
	DispatchMS      *float64 `json:"dispatchMs,omitempty"`
}

func validGPUResultMetrics(m *GPUResultMetrics) bool {
	if m == nil {
		return true
	}
	for _, v := range []*float64{m.CompileMS, m.DispatchMS} {
		if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0) {
			return false
		}
	}
	return true
}
