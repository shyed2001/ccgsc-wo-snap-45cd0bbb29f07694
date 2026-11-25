// Path: internal/pool/recompute_defer_R18.go | WO R18-RECOMPUTE-POLICY (G-R17-1)
// What/why: the defer policy of an unavailable sampled recompute verdict.
//
// When a sampled task is submitted but the recompute cannot produce a verdict
// (the verifier pool is busy, the prepared verdict is stale, or the reference
// cannot answer), the per-pool busy policy decides:
//
//   - "skip" (default, pre-R18): the submission settles untouched and accuses
//     no one,
//   - "defer": the result is held on the task (DeferredRecompute), the task
//     stays pending and the engine retries the recompute in the background
//     when a worker slot frees. The retries are bounded; when the bound is
//     spent the held result is accepted without comparison (the skip
//     fallback) and logged. A deferred record whose window expired is settled
//     the same way by the sweep, so a restarted engine never keeps a task
//     pending forever.
package pool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
)

// deferredRecomputeDefaultRetries is the bound of the background retries: after
// this many attempts without a verdict the held result is accepted (skip).
const deferredRecomputeDefaultRetries = 3

// deferredRecomputeRef identifies the deferred recompute of one task.
type deferredRecomputeRef struct {
	jobID  string
	taskID string
}

// deferRecomputeRetryLimit is the bounded number of background retries of a
// deferred recompute. A zero engine knob resolves to the default; tests may
// set the knob directly.
func (e *Engine) deferRecomputeRetryLimit() int {
	if e.deferRecomputeRetries > 0 {
		return e.deferRecomputeRetries
	}
	return deferredRecomputeDefaultRetries
}

// deferRecomputeWait is how long one background retry waits for a free worker
// slot before counting the attempt. A zero engine knob resolves to the re-run
// window; tests may set the knob directly.
func (e *Engine) deferRecomputeWait() time.Duration {
	if e.deferRecomputeWaitDur > 0 {
		return e.deferRecomputeWaitDur
	}
	return e.reRunWindow()
}

// findDeferredTask finds the developer task of a job by its task id.
func findDeferredTask(j *Job, taskID string) *Task {
	for i := range j.Tasks {
		if j.Tasks[i].TaskID == taskID {
			return &j.Tasks[i]
		}
	}
	return nil
}

// deferRecomputeResult holds the submission of a sampled task whose recompute
// verdict is unavailable (G-R17-1, defer policy). It mirrors the re-run hold:
// the task stays pending, the lease is released and the returned result tells
// the caller the submission is pending_verification.
func (e *Engine) deferRecomputeResult(tx store.Tx, j *Job, t *Task, w Worker, l *Lease, q Submission, out Result, metered *[]meteredRuntime, settledRedis *[]redisLeaseRef) (Result, error) {
	held := heldResult(w, t, q, e.Now().UTC())
	t.DeferredRecompute = &DeferredRecompute{HeldResult: held, At: e.Now().UTC()}
	t.State = StatePendingVerification
	t.Lease = nil
	t.EnvelopeDigest = ""
	t.EnvelopeUsed = false
	out.Status = StatePendingVerification
	out.RecomputeDeferred = true
	if e.redis != nil && l != nil {
		*settledRedis = append(*settledRedis, redisLeaseRef{key: "lease:" + j.JobID + ":" + l.TaskID, token: l.Token})
	}
	if err := e.recordLeaseSettled(tx, *j, l); err != nil {
		return out, err
	}
	e.Log.Info("recompute.deferred", "jobId", j.JobID, "taskId", t.TaskID, "workerId", w.TabID)
	return out, e.event(tx, *j, "task.recomputeDeferred", &out)
}

// settleDeferredRecompute is the background retry loop of one deferred
// recompute. Each attempt waits (bounded) for a worker slot and reruns the
// reference verdict against the held bytes; the first available verdict
// settles the task. When the bounded retries are spent the held result is
// accepted without comparison (skip) and logged.
func (e *Engine) settleDeferredRecompute(ref deferredRecomputeRef) {
	for attempt := 0; attempt < e.deferRecomputeRetryLimit(); attempt++ {
		j, task, held, ok := e.loadDeferredRecompute(ref)
		if !ok {
			// Settled or cancelled elsewhere: nothing left to retry.
			return
		}
		// Retry when a slot frees. The acquire blocks up to one retry wait, so
		// the loop never spins on a busy verifier pool.
		sem := e.workerPool()
		ctx, cancel := context.WithTimeout(context.Background(), e.deferRecomputeWait())
		if err := sem.Acquire(ctx); err != nil {
			cancel()
			continue
		}
		// The retry already owns the only permit on a one-slot pool. Running
		// the verifier here must NOT call TryAcquire on the same semaphore.
		v := e.recomputeVerdictWithSlot(&j, task, held.Output, true)
		sem.Release()
		cancel()
		if v.ran {
			e.commitDeferredVerdict(ref, v)
			return
		}
	}
	e.fallbackDeferredRecompute(ref)
}

// loadDeferredRecompute reads the held record of a deferred recompute. false
// means the task is settled, cancelled or no longer deferred.
func (e *Engine) loadDeferredRecompute(ref deferredRecomputeRef) (Job, *Task, DeferredRecompute, bool) {
	var j Job
	if err := e.Store.View(context.Background(), func(r store.Reader) error {
		return r.Get(store.Jobs, ref.jobID, &j)
	}); err != nil {
		return j, nil, DeferredRecompute{}, false
	}
	if terminal(j) {
		return j, nil, DeferredRecompute{}, false
	}
	task := findDeferredTask(&j, ref.taskID)
	if task == nil || task.DeferredRecompute == nil {
		return j, nil, DeferredRecompute{}, false
	}
	return j, task, *task.DeferredRecompute, true
}

// commitDeferredVerdict settles the deferred task with a recompute verdict. It
// re-checks inside the transaction, so a task settled by the sweep in the
// meantime is never double-settled.
func (e *Engine) commitDeferredVerdict(ref deferredRecomputeRef, v recomputeVerdictOutcome) {
	var observations []reputationObservation
	var settledRedis []redisLeaseRef
	err := e.Store.Update(context.Background(), func(tx store.Tx) error {
		var j Job
		if er := tx.Get(store.Jobs, ref.jobID, &j); er != nil {
			return er
		}
		task := findDeferredTask(&j, ref.taskID)
		if task == nil || task.DeferredRecompute == nil {
			return nil
		}
		held := *task.DeferredRecompute
		task.DeferredRecompute = nil
		// A held result always carries a well-formed digest (the malformed
		// policy settled it at submit time); defensively, a malformed held
		// claim is accepted without comparison, never accused.
		if v.invalid || (!v.match && !v.pinned) {
			// G-R17-3: only a pinned (self-checked) reference may accuse.
			reason := "unpinned reference mismatch; result accepted unverified"
			out := deferredResultFromHeld(j, task, held)
			out.RecomputeUnverified = true
			task.State = "done"
			task.Lease = nil
			j.Results = append(j.Results, out)
			e.scheduler.completed.Add(1)
			if er := e.recordLeaseSettled(tx, j, nil); er != nil {
				return er
			}
			e.Log.Warn("recompute.unverified", "jobId", j.JobID, "taskId", task.TaskID, "workerId", held.WorkerID, "reason", reason)
			if er := e.finishAccepted(tx, &j, &out, nil); er != nil {
				return er
			}
			return e.save(tx, &j)
		}
		if !v.match {
			// G-R17-3: a pinned reference that disagrees still accuses.
			reason := "server recompute mismatch"
			task.Flagged = true
			task.FlagReason = reason
			observations = append(observations, reputationObservation{held.WorkerID, verify.EventFail, task.TaskID, reason})
			if er := e.recordReputationOutcome(tx, j, verify.EventFail); er != nil {
				return er
			}
			out := Result{JobID: j.JobID, TaskID: task.TaskID, Status: "failed", Attempts: held.Attempts, At: e.Now().UTC(), Error: reason, WorkerID: held.WorkerID, Flagged: true, FlagReason: reason, RecomputeDeferred: true}
			e.Log.Warn("recompute.mismatch", "jobId", j.JobID, "taskId", task.TaskID, "workerId", held.WorkerID)
			if er := e.event(tx, j, "task.recomputeMismatch", &out); er != nil {
				return er
			}
			if er := e.failWith(tx, &j, task, &out, false, &settledRedis); er != nil {
				return er
			}
			return e.save(tx, &j)
		}
		// The background recompute verified the held result.
		out := deferredResultFromHeld(j, task, held)
		out.RecomputeVerified = true
		task.State = "done"
		task.Lease = nil
		j.Results = append(j.Results, out)
		e.scheduler.completed.Add(1)
		observations = append(observations, reputationObservation{held.WorkerID, verify.EventPass, task.TaskID, ""})
		if er := e.recordReputationOutcome(tx, j, verify.EventPass); er != nil {
			return er
		}
		if er := e.recordLeaseSettled(tx, j, nil); er != nil {
			return er
		}
		if er := e.recordGPUResultMetrics(tx, j, held.GPU); er != nil {
			return er
		}
		e.Log.Info("recompute.deferredSettled", "jobId", j.JobID, "taskId", task.TaskID, "workerId", held.WorkerID)
		if er := e.finishAccepted(tx, &j, &out, nil); er != nil {
			return er
		}
		return e.save(tx, &j)
	})
	if err != nil {
		e.Log.Error("the deferred recompute settlement failed", "jobId", ref.jobID, "taskId", ref.taskID, "error", err)
		return
	}
	if e.redis != nil {
		e.cleanupRedisLeases(settledRedis)
	}
	e.observeCommitted(observations)
}

// fallbackDeferredRecompute accepts the held result without comparison after
// the bounded retries were spent (the skip fallback of G-R17-1). It accuses
// no one and logs the fallback.
func (e *Engine) fallbackDeferredRecompute(ref deferredRecomputeRef) {
	var observations []reputationObservation
	err := e.Store.Update(context.Background(), func(tx store.Tx) error {
		var j Job
		if er := tx.Get(store.Jobs, ref.jobID, &j); er != nil {
			return er
		}
		task := findDeferredTask(&j, ref.taskID)
		if task == nil || task.DeferredRecompute == nil {
			return nil
		}
		held := *task.DeferredRecompute
		task.DeferredRecompute = nil
		out := deferredResultFromHeld(j, task, held)
		task.State = "done"
		task.Lease = nil
		j.Results = append(j.Results, out)
		e.scheduler.completed.Add(1)
		reason := "deferred recompute retries exhausted, result accepted without comparison"
		observations = append(observations, reputationObservation{held.WorkerID, verify.EventInconclusive, task.TaskID, reason})
		if er := e.recordReputationOutcome(tx, j, verify.EventInconclusive); er != nil {
			return er
		}
		if er := e.recordLeaseSettled(tx, j, nil); er != nil {
			return er
		}
		if er := e.recordGPUResultMetrics(tx, j, held.GPU); er != nil {
			return er
		}
		e.Log.Warn("recompute.deferredExhausted", "jobId", j.JobID, "taskId", task.TaskID, "workerId", held.WorkerID, "reason", reason)
		if er := e.finishAccepted(tx, &j, &out, nil); er != nil {
			return er
		}
		return e.save(tx, &j)
	})
	if err != nil {
		e.Log.Error("the deferred recompute skip fallback failed", "jobId", ref.jobID, "taskId", ref.taskID, "error", err)
		return
	}
	e.observeCommitted(observations)
}

// expireDeferredRecompute is the sweep branch of a deferred record whose
// window expired (G-R17-1): the held result is accepted without comparison,
// exactly like the bounded-retry fallback, so a restarted engine never keeps
// the task pending forever.
func (e *Engine) expireDeferredRecompute(tx store.Tx, j *Job, t *Task, observations *[]reputationObservation) error {
	held := *t.DeferredRecompute
	t.DeferredRecompute = nil
	out := deferredResultFromHeld(*j, t, held)
	t.State = "done"
	t.Lease = nil
	j.Results = append(j.Results, out)
	e.scheduler.completed.Add(1)
	reason := "deferred recompute window expired, result accepted without comparison"
	*observations = append(*observations, reputationObservation{held.WorkerID, verify.EventInconclusive, t.TaskID, reason})
	if err := e.recordReputationOutcome(tx, *j, verify.EventInconclusive); err != nil {
		return err
	}
	if err := e.recordLeaseSettled(tx, *j, nil); err != nil {
		return err
	}
	if err := e.recordGPUResultMetrics(tx, *j, held.GPU); err != nil {
		return err
	}
	e.Log.Warn("recompute.deferredWindowExpired", "jobId", j.JobID, "taskId", t.TaskID, "workerId", held.WorkerID, "reason", reason)
	return e.finishAccepted(tx, j, &out, nil)
}

// deferredResultFromHeld rebuilds the settled result of a held deferred
// submission from its exact bytes.
func deferredResultFromHeld(j Job, t *Task, held DeferredRecompute) Result {
	raw := held.Output
	if b, err := base64.StdEncoding.DecodeString(held.Bytes); err == nil && len(b) > 0 {
		raw = append(json.RawMessage(nil), b...)
	}
	return Result{
		JobID: j.JobID, TaskID: t.TaskID, Status: "done", Attempts: held.Attempts,
		Output: raw, SHA256: held.SHA256, At: held.At, WorkerID: held.WorkerID,
		HashOK: true, RecomputeDeferred: true,
	}
}
