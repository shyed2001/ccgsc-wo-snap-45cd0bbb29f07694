// Path: internal/pool/metrics_MVPW.go
// What/why: Slice B engine metric recording into durable pool_metrics (WWPP-R37.2, R40.1).
package pool

import (
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
	"context"
	"time"
)

type verifyFailureMetric struct {
	poolID string
	jobID  string
	op     string
	dur    time.Duration
}

func (e *Engine) recordMetric(tx store.Tx, j Job, name string, value float64, unit, source string) error {
	if e == nil || tx == nil {
		return nil
	}
	return store.RecordMetricSample(tx, store.MetricSample{
		TS:     e.Now().UTC(),
		Scope:  j.PoolID,
		JobID:  j.JobID,
		Name:   name,
		Value:  value,
		Unit:   unit,
		Source: source,
	})
}

func (e *Engine) recordLeaseIssued(tx store.Tx, j Job, count int) error {
	if count < 1 {
		return nil
	}
	return e.recordMetric(tx, j, "lease_issued", float64(count), "count", "pool")
}

func (e *Engine) recordLeaseExpired(tx store.Tx, j Job) error {
	return e.recordMetric(tx, j, "lease_expired", 1, "count", "pool")
}

// recordReplicaLateEvidence records that a redundant task closed its window with
// a replica lease still unanswered. R11-G6: the evidence is what makes the gap
// visible instead of a silent accept of the first result.
func (e *Engine) recordReplicaLateEvidence(tx store.Tx, j Job, held int, why string) error {
	return e.recordMetric(tx, j, "replica_late_evidence", float64(held), "count", why)
}

func (e *Engine) recordLeaseSettled(tx store.Tx, j Job, lease *Lease) error {
	if err := e.recordMetric(tx, j, "lease_settled", 1, "count", "pool"); err != nil {
		return err
	}
	if lease != nil && !lease.StartedAt.IsZero() {
		latencyMS := float64(e.Now().Sub(lease.StartedAt).Nanoseconds()) / 1e6
		if latencyMS < 0 {
			latencyMS = 0
		}
		if err := e.recordMetric(tx, j, "task_latency_ms", latencyMS, "ms", "pool"); err != nil {
			return err
		}
	}
	return nil
}

func verifyMetricName(op string, pass bool) string {
	if pass {
		return "verify_" + op + "_pass"
	}
	return "verify_" + op + "_fail"
}

func (e *Engine) recordVerifyOutcome(tx store.Tx, j Job, op string, d time.Duration, ok bool, recordInDefault bool) error {
	if recordInDefault {
		verify.DefaultMetrics().Record(op, d, ok)
	}
	return e.recordMetric(tx, j, verifyMetricName(op, ok), 1, "count", "verify")
}

func (e *Engine) persistVerifyFailure(ctx context.Context, m *verifyFailureMetric) {
	if e == nil || m == nil || e.Store == nil {
		return
	}
	verify.DefaultMetrics().Record(m.op, m.dur, false)
	if err := e.Store.Update(ctx, func(tx store.Tx) error {
		return store.RecordMetricSample(tx, store.MetricSample{
			TS:     e.Now().UTC(),
			Scope:  m.poolID,
			JobID:  m.jobID,
			Name:   verifyMetricName(m.op, false),
			Value:  1,
			Unit:   "count",
			Source: "verify",
		})
	}); err != nil {
		e.Log.Warn("verify.failure_metric", "error", err, "op", m.op, "poolId", m.poolID, "jobId", m.jobID)
	}
}

func (e *Engine) recordCanaryOutcome(tx store.Tx, j Job, pass bool) error {
	name := "canary_fail"
	if pass {
		name = "canary_pass"
	}
	if err := e.recordMetric(tx, j, name, 1, "count", "canary"); err != nil {
		return err
	}
	return nil
}

func (e *Engine) recordReputationOutcome(tx store.Tx, j Job, kind verify.EventKind) error {
	if e == nil || !e.TrustConfig().ReputationEnabled {
		return nil
	}
	pass := kind == verify.EventPass
	return e.recordMetric(tx, j, verifyMetricName(verify.OpReputation, pass), 1, "count", "verify")
}

// recordGPUResultMetrics records the thesis GPU metrics of one settled result
// through the existing recordMetric path (docs/THESIS_METRICS_PLAN.md GPU rows,
// WWPP-R37.2): the worker-measured gpu_adapter_detected (count), gpu_compile_ms
// (ms), gpu_dispatch_ms (ms) samples and the per-task-kind gpu_vs_cpu count of
// results computed on GPU vs CPU. The task kind travels in the sample source
// label ("gpu" / "cpu"), so the Prometheus exposition and the admin metrics
// panel separate the two series.
//
// Absence is preserved (owner rule: unmeasured metrics are absent, never zero):
// a field the worker did not measure produces no sample at all, and a CPU-only
// submission (gpu == nil) records only the gpu_vs_cpu{source="cpu"} count. A
// measured false (adapterDetected=false) or a measured zero (compileMs=0) is a
// real observation and is recorded as 0.
func (e *Engine) recordGPUResultMetrics(tx store.Tx, j Job, gpu *GPUResultMetrics) error {
	if e == nil || tx == nil {
		return nil
	}
	if gpu == nil {
		return e.recordMetric(tx, j, "gpu_vs_cpu", 1, "count", "cpu")
	}
	if gpu.AdapterDetected != nil {
		value := 0.0
		if *gpu.AdapterDetected {
			value = 1
		}
		if err := e.recordMetric(tx, j, "gpu_adapter_detected", value, "count", "gpu"); err != nil {
			return err
		}
	}
	if gpu.CompileMS != nil {
		if err := e.recordMetric(tx, j, "gpu_compile_ms", *gpu.CompileMS, "ms", "gpu"); err != nil {
			return err
		}
	}
	if gpu.DispatchMS != nil {
		if err := e.recordMetric(tx, j, "gpu_dispatch_ms", *gpu.DispatchMS, "ms", "gpu"); err != nil {
			return err
		}
	}
	return e.recordMetric(tx, j, "gpu_vs_cpu", 1, "count", "gpu")
}
