// File: pools-core/internal/pool/redundancy_W10C.go (go)
// W10C-SERVER row 1: redundant 2-of-N result verification with a Sybil guard on
// the replica worker, one tie-breaker replica after a mismatch, majority
// settlement, reputation evidence through the existing Tracker, and a deadline
// on the PENDING_VERIFICATION state.
package pool

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
)

// Redundant verification phases.
const (
	redundantPhaseReplica  = "replica"
	redundantPhaseTieBreak = "tie-break"
)

// redundantRequired reports whether this first result needs a replica. Every task
// of a new/unknown worker is replicated regardless of the configured rate; the
// rest is sampled deterministically at redundantRate.
func (e *Engine) redundantRequired(j *Job, t *Task, w Worker) bool {
	if t.Redundant != nil || t.Pending != nil || t.CanaryID != "" {
		return false
	}
	s := e.PoolSettings(j.PoolID)
	if s.VerifyMode != VerifyModeRedundant && s.VerifyMode != VerifyModeBoth {
		return false
	}
	if e.redundantSampled(j, t) {
		return true
	}
	_, err := e.Verifier.Reputation().Worker(w.TabID)
	return errors.Is(err, verify.ErrUnknownWorker)
}

// redundantSampled draws the stable replica decision of a task.
func (e *Engine) redundantSampled(j *Job, t *Task) bool {
	rate := e.PoolSettings(j.PoolID).RedundantRate
	if rate <= 0 {
		return false
	}
	if rate >= 1 {
		return true
	}
	return verify.RerunBucketWithKey(e.rerunSecret, j.JobID+":"+t.TaskID+":redundant") < rate*100
}

// ipv24 maps a client address to its /24 network (IPv4) or /64 prefix (IPv6).
// An empty or unparseable address yields "", which never counts as a shared
// network: TabID and HostID still keep replicas distinct.
func ipv24(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		addr = host
	}
	addr = strings.Trim(addr, "[]")
	ip := net.ParseIP(addr)
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d.0/24", v4[0], v4[1], v4[2])
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// redundancyExcluded enforces the distinct-worker rule of a replica: a different
// TabID, a different HostID and a different IP network than every participant.
func redundancyExcluded(t *Task, w Worker) bool {
	if t.Redundant == nil {
		return false
	}
	network := ipv24(w.ClientIP)
	for _, held := range t.Redundant.Held {
		if held.WorkerID != "" && held.WorkerID == w.TabID {
			return true
		}
		if held.HostID != "" && held.HostID == w.HostID {
			return true
		}
		// R11-G5: the /24 guard can only speak when the candidate has a network
		// AND a host label to fall back on. An empty address with an unknown or
		// shared host label used to walk straight through the guard, so one device
		// could serve as its own replica.
		if held.IP24 == "" || network == "" {
			if w.HostID == "" || held.HostID == "" || w.HostID == held.HostID {
				return true
			}
			continue
		}
		if held.IP24 == network {
			return true
		}
	}
	return false
}

// heldResult captures one submitted result together with its worker identity.
func heldResult(w Worker, t *Task, q Submission, at time.Time) HeldResult {
	return HeldResult{
		PendingResult: PendingResult{
			WorkerID: w.TabID,
			SHA256:   verify.SHA256(q.Output),
			Output:   q.Output,
			Bytes:    base64.StdEncoding.EncodeToString(q.Output),
			Attempts: t.Attempts,
			At:       at,
		},
		HostID: w.HostID,
		IP24:   ipv24(w.ClientIP),
		GPU:    q.GPU,
	}
}

// resultFromHeld rebuilds the accepted result of a held replica.
func resultFromHeld(j *Job, t *Task, held HeldResult, compared int) Result {
	raw := held.Output
	if b, err := base64.StdEncoding.DecodeString(held.Bytes); err == nil && len(b) > 0 {
		raw = append(json.RawMessage(nil), b...)
	}
	return Result{
		JobID: j.JobID, TaskID: t.TaskID, Status: "done", Attempts: held.Attempts,
		Output: raw, SHA256: held.SHA256, At: held.At, WorkerID: held.WorkerID,
		HashOK: true, Redundant: true, RedundantReplicas: compared,
	}
}

// holdRedundantResult keeps the first result of a redundancy-sampled task and
// requeues it for a distinct replica (task state PENDING_VERIFICATION).
func (e *Engine) holdRedundantResult(tx store.Tx, j *Job, t *Task, w Worker, l *Lease, q Submission, out Result, metered *[]meteredRuntime, settledRedis *[]redisLeaseRef) (Result, error) {
	out.Status = StatePendingVerification
	out.Redundant = true
	out.RedundantReplicas = 1
	t.Redundant = &RedundantState{
		Target:   2,
		Phase:    redundantPhaseReplica,
		Held:     []HeldResult{heldResult(w, t, q, e.Now().UTC())},
		Deadline: e.Now().Add(e.reRunWindow()).UTC(),
	}
	t.State = StatePendingVerification
	t.Lease = nil
	t.Pending = nil
	t.EnvelopeDigest = ""
	t.EnvelopeUsed = false
	t.RerunExclude = append(t.RerunExclude, w.TabID)
	if e.redis != nil && l != nil {
		*settledRedis = append(*settledRedis, redisLeaseRef{key: "lease:" + j.JobID + ":" + l.TaskID, token: l.Token})
	}
	if err := e.recordLeaseSettled(tx, *j, l); err != nil {
		return out, err
	}
	e.Log.Info("redundant.pending", "jobId", j.JobID, "taskId", t.TaskID, "workerId", w.TabID, "deadline", t.Redundant.Deadline)
	if err := e.event(tx, *j, "task.pendingVerification", &out); err != nil {
		return out, err
	}
	return out, e.save(tx, j, metered)
}

// settleRedundantResult handles a replica submission: it settles on matching
// hashes, starts the single tie-breaker on a mismatch, and lets the majority win.
func (e *Engine) settleRedundantResult(tx store.Tx, j *Job, t *Task, w Worker, l *Lease, q Submission, out Result, observations *[]reputationObservation, metered *[]meteredRuntime, settledRedis *[]redisLeaseRef) (Result, error) {
	state := t.Redundant
	held := heldResult(w, t, q, e.Now().UTC())
	out.Redundant = true
	if state.Phase == redundantPhaseTieBreak {
		all := append(append([]HeldResult(nil), state.Held...), held)
		counts := map[string]int{}
		hashes := make([]string, 0, len(all))
		for _, h := range all {
			if counts[h.SHA256] == 0 {
				hashes = append(hashes, h.SHA256)
			}
			counts[h.SHA256]++
		}
		sort.Strings(hashes)
		majority, votes := "", 0
		for _, hash := range hashes {
			if counts[hash] > votes {
				majority, votes = hash, counts[hash]
			}
		}
		if votes < 2 {
			// Three different results: no majority, conclusive failure.
			reason := "redundant verification has no majority"
			out.Status = "failed"
			out.Error = reason
			out.Flagged = true
			out.FlagReason = reason
			for _, h := range all {
				*observations = append(*observations, reputationObservation{h.WorkerID, verify.EventFail, t.TaskID, reason})
				if err := e.recordReputationOutcome(tx, *j, verify.EventFail); err != nil {
					return out, err
				}
			}
			t.Redundant = nil
			t.State = "failed"
			t.Quarantined = true
			t.Lease = nil
			if e.redis != nil && l != nil {
				*settledRedis = append(*settledRedis, redisLeaseRef{key: "lease:" + j.JobID + ":" + l.TaskID, token: l.Token})
			}
			j.Results = append(j.Results, out)
			e.Log.Warn("redundant.noMajority", "jobId", j.JobID, "taskId", t.TaskID)
			if err := e.recordLeaseSettled(tx, *j, l); err != nil {
				return out, err
			}
			if err := e.event(tx, *j, "task.failed", &out); err != nil {
				return out, err
			}
			return out, e.save(tx, j, metered)
		}
		accepted := out
		acceptedCurrent := verify.CompareResultHashes(out.SHA256, majority)
		acceptedGPU := q.GPU
		if !acceptedCurrent {
			for _, h := range all {
				if verify.CompareResultHashes(h.SHA256, majority) {
					accepted = resultFromHeld(j, t, h, len(all))
					// R11-G8: the accepted replica carries its own measurements.
					acceptedGPU = h.GPU
					break
				}
			}
		}
		var passes, fails []string
		for _, h := range all {
			if verify.CompareResultHashes(h.SHA256, majority) {
				passes = append(passes, h.WorkerID)
			} else {
				fails = append(fails, h.WorkerID)
			}
		}
		e.Log.Info("redundant.tieBreak", "jobId", j.JobID, "taskId", t.TaskID, "winner", majority, "losers", fails)
		return e.completeRedundant(tx, j, t, accepted, len(all), passes, fails, "redundant tie-break minority result", l, acceptedGPU, observations, metered, settledRedis)
	}
	first := state.Held[0]
	if verify.CompareResultHashes(first.SHA256, out.SHA256) && verify.CompareResults(first.exactBytes(), q.Output) {
		return e.completeRedundant(tx, j, t, out, 2, []string{first.WorkerID, w.TabID}, nil, "", l, q.GPU, observations, metered, settledRedis)
	}
	// A mismatch starts exactly one tie-breaker replica. R11-G3: the tie-break
	// gets its own window from the moment it starts. Keeping the hold-time
	// deadline gave the tie-breaker almost none of the re-run window when the
	// replica answered near the end of it.
	state.Held = append(state.Held, held)
	state.Phase = redundantPhaseTieBreak
	state.Target = 3
	state.Deadline = e.Now().Add(e.reRunWindow()).UTC()
	t.State = StatePendingVerification
	t.Lease = nil
	t.EnvelopeDigest = ""
	t.EnvelopeUsed = false
	t.RerunExclude = append(t.RerunExclude, w.TabID)
	if e.redis != nil && l != nil {
		*settledRedis = append(*settledRedis, redisLeaseRef{key: "lease:" + j.JobID + ":" + l.TaskID, token: l.Token})
	}
	if err := e.recordLeaseSettled(tx, *j, l); err != nil {
		return out, err
	}
	out.Status = StatePendingVerification
	out.Flagged = true
	out.FlagReason = "redundant replica mismatch; tie-breaker requested"
	e.Log.Warn("redundant.mismatch", "jobId", j.JobID, "taskId", t.TaskID, "firstWorkerId", first.WorkerID, "workerId", w.TabID)
	if err := e.event(tx, *j, "task.redundantMismatch", &out); err != nil {
		return out, err
	}
	return out, e.save(tx, j, metered)
}

// completeRedundant settles a task from the quorum/majority outcome: the winners
// take a pass event, the losers a fail event, and an accepted held result is
// rebuilt from its exact bytes.
// R11-G8: gpu is the measurement of the submission the quorum actually accepted,
// which is not always the one that closed it.
func (e *Engine) completeRedundant(tx store.Tx, j *Job, t *Task, accepted Result, compared int, passes, fails []string, loserReason string, l *Lease, gpu *GPUResultMetrics, observations *[]reputationObservation, metered *[]meteredRuntime, settledRedis *[]redisLeaseRef) (Result, error) {
	accepted.Redundant = true
	accepted.RedundantReplicas = compared
	for _, workerID := range passes {
		*observations = append(*observations, reputationObservation{workerID, verify.EventPass, t.TaskID, ""})
		if err := e.recordReputationOutcome(tx, *j, verify.EventPass); err != nil {
			return accepted, err
		}
	}
	for _, workerID := range fails {
		*observations = append(*observations, reputationObservation{workerID, verify.EventFail, t.TaskID, loserReason})
		if err := e.recordReputationOutcome(tx, *j, verify.EventFail); err != nil {
			return accepted, err
		}
	}
	t.Redundant = nil
	t.State = "done"
	t.Lease = nil
	j.Results = append(j.Results, accepted)
	e.scheduler.completed.Add(1)
	if e.redis != nil && l != nil {
		*settledRedis = append(*settledRedis, redisLeaseRef{key: "lease:" + j.JobID + ":" + l.TaskID, token: l.Token})
	}
	if err := e.recordLeaseSettled(tx, *j, l); err != nil {
		return accepted, err
	}
	if err := e.recordGPUResultMetrics(tx, *j, gpu); err != nil {
		return accepted, err
	}
	if err := e.finishAccepted(tx, j, &accepted, l); err != nil {
		return accepted, err
	}
	e.Log.Info("redundant.settled", "jobId", j.JobID, "taskId", t.TaskID, "replicas", compared, "workerId", accepted.WorkerID)
	return accepted, e.save(tx, j, metered)
}

// expireRedundant settles a task whose verification deadline passed: the first
// result is accepted, flagged, and the comparison is recorded as inconclusive
// rather than as a worker failure.
func (e *Engine) expireRedundant(tx store.Tx, j *Job, t *Task, observations *[]reputationObservation, released *[]redisLeaseRef) error {
	state := t.Redundant
	if state == nil || len(state.Held) == 0 {
		t.Redundant = nil
		return nil
	}
	if t.State == "done" || t.State == "failed" || t.State == "cancelled" {
		t.Redundant = nil
		return nil
	}
	out := resultFromHeld(j, t, state.Held[0], len(state.Held))
	out.Flagged = true
	out.FlagReason = "redundant verification deadline exceeded, first result accepted without quorum"
	for _, held := range state.Held {
		*observations = append(*observations, reputationObservation{held.WorkerID, verify.EventInconclusive, t.TaskID, out.FlagReason})
		if err := e.recordReputationOutcome(tx, *j, verify.EventInconclusive); err != nil {
			return err
		}
	}
	// R11-G6: the window closes while a replica lease may still be live. That
	// lease is snapshotted and recorded as expired before it is dropped: without
	// this the late submission could never be matched to the task and the replica
	// left no evidence at all. The sweep reaches this branch before the ordinary
	// lease-expiry one, so nothing else would have recorded it.
	if t.Lease != nil && !t.Lease.ExpiresAt.After(e.Now()) {
		if err := e.snapshotExpiredLease(tx, j, t); err != nil {
			return err
		}
		if err := e.recordLeaseExpired(tx, *j); err != nil {
			return err
		}
		if err := e.recordReplicaLateEvidence(tx, *j, len(state.Held), "expired"); err != nil {
			return err
		}
	}
	if e.redis != nil && t.Lease != nil {
		*released = append(*released, redisLeaseRef{key: "lease:" + j.JobID + ":" + t.Lease.TaskID, token: t.Lease.Token})
	}
	t.Redundant = nil
	t.State = "done"
	t.Lease = nil
	j.Results = append(j.Results, out)
	e.scheduler.completed.Add(1)
	e.Log.Info("redundant.deadline", "jobId", j.JobID, "taskId", t.TaskID, "held", len(state.Held))
	if err := e.recordLeaseSettled(tx, *j, nil); err != nil {
		return err
	}
	return e.finishAccepted(tx, j, &out, nil)
}
