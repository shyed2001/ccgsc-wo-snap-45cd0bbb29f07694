// T42 F1, 2026-11-18: one atomic batch per slot, with per-task fencing and signatures.
package pool

import (
	"encoding/json"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
)

// Shared tokens identify a batch while task IDs and generations fence each result.
func activeBatchCount(j Job, worker string) int {
	count := 0
	for _, t := range j.Tasks {
		if t.Lease != nil && t.Lease.WorkerID == worker {
			count++
		}
	}
	return count
}

func activeLeaseCounts(j Job, worker string) (ordinary, verification int) {
	for _, t := range j.Tasks {
		if t.Lease == nil || t.Lease.WorkerID != worker {
			continue
		}
		if t.Pending != nil || t.CanaryID != "" || t.Redundant != nil {
			verification++
		} else {
			ordinary++
		}
	}
	return ordinary, verification
}

type redisLeaseRef struct {
	key   string
	token string
}

func (e *Engine) claimBatch(rd store.Reader, j *Job, w Worker, gpu bool, acquired *[]redisLeaseRef) (Assignment, bool, error) {
	limit := j.Request.BatchSize
	if limit < 1 {
		limit = 1
	}
	if max := e.maxBatchSize(w, MaxBatchSize, false); limit > max {
		limit = max
	}
	members := make([]Assignment, 0, min(limit, len(j.Tasks)))
	token := auth.ID()
	groupValue := ""
	seen := make(map[string]struct{}, limit)
	for i := range j.Tasks {
		t := &j.Tasks[i]
		if !claimableTask(t) || excluded(t, w.TabID, w.Owner) || redundancyExcluded(t, w) {
			continue
		}
		if _, ok := seen[t.TaskID]; ok {
			continue
		}
		value, ok := e.placementAllows(j, t, w, groupValue)
		if !ok {
			continue
		}
		groupValue = value
		a, err := e.claimBatchTask(j, t, w.TabID, token, gpu, acquired)
		if err != nil {
			return Assignment{}, false, err
		}
		seen[t.TaskID] = struct{}{}
		members = append(members, a)
		if len(members) == limit {
			break
		}
	}
	if len(members) == 0 {
		return Assignment{}, false, nil
	}
	if err := e.attachDewReceipt(rd, j, members); err != nil {
		return Assignment{}, false, err
	}
	if len(members) > 0 && members[0].LeaseReceipt != nil {
		// R11 Part E + F12: the receipt is a property of this lease. DewReceipt
		// makes a later submission present it, and Dew is the re-assignment
		// fence (G-F12-5), written in the same claim transaction.
		for _, m := range members {
			if m.LeaseReceipt == nil {
				continue
			}
			for i := range j.Tasks {
				if j.Tasks[i].TaskID == m.Lease.TaskID || j.Tasks[i].CanaryTaskID == m.Lease.TaskID {
					j.Tasks[i].DewReceipt = true
					j.Tasks[i].Dew = &DewLease{
						ReceiptID:        m.LeaseReceipt.ReceiptID,
						WorkerID:         m.Lease.WorkerID,
						ContributorOwner: m.LeaseReceipt.ContributorOwner,
						Generation:       m.Lease.Generation,
						LeaseTokenSHA256: verify.SHA256([]byte(m.Lease.Token)),
						IssuedAt:         time.Unix(m.LeaseReceipt.IssuedAt, 0).UTC(),
						Deadline:         time.Unix(m.LeaseReceipt.Deadline, 0).UTC(),
					}
				}
			}
		}
	}
	out := members[0]
	if j.Request.BatchSize > 1 {
		out.Batch = members
	}
	return out, true, nil
}

func (e *Engine) claimBatchTask(j *Job, t *Task, worker, token string, gpu bool, acquired *[]redisLeaseRef) (Assignment, error) {
	var out Assignment
	var er error
	var canary *verify.Canary
	if t.Pending == nil && t.Attempts == 0 && t.CanaryAttempts == 0 {
		canary = e.pickCanary(j, t.TaskID)
	}
	isCanary := canary != nil
	if isCanary {
		t.CanaryAttempts++
	} else {
		t.Attempts++
	}
	t.Generation++
	t.State = "leased"
	t.ExpiredLease = nil
	now := e.Now()
	ttl := e.TTL
	if t.TimeoutMS > 0 && time.Duration(t.TimeoutMS)*time.Millisecond < ttl {
		ttl = time.Duration(t.TimeoutMS) * time.Millisecond
	}
	if ttl < time.Second {
		ttl = time.Second
	}
	leaseTaskID := t.TaskID
	if isCanary {
		t.CanaryID = canary.ID
		t.CanaryTaskID = auth.ID()
		leaseTaskID = t.CanaryTaskID
	}
	l := Lease{JobID: j.JobID, TaskID: leaseTaskID, WorkerID: worker, SessionID: j.SessionID, Token: token, Generation: t.Generation, Attempt: t.Attempts, StartedAt: now.UTC(), ExpiresAt: now.Add(ttl).UTC(), GPU: gpu}
	// MVPW-D redis lease: issue via SET NX (fencing token). Small race ok; settle in PG.
	if e.redis != nil {
		key := "lease:" + j.JobID + ":" + leaseTaskID
		ms := int(ttl / time.Millisecond)
		if ms < 500 {
			ms = 500
		}
		if ok, err := e.redis.SetNX(key, l.Token, ms); err != nil {
			return Assignment{}, err
		} else if !ok {
			t.State = "queued"
			t.Lease = nil
			e.Log.Info("lease.redis.contended", "jobId", j.JobID, "taskId", leaseTaskID)
			return Assignment{}, ErrLeaseContended
		}
		*acquired = append(*acquired, redisLeaseRef{key: key, token: l.Token})
	}
	t.Lease = &l
	out = Assignment{Lease: l, Task: t.TaskSpec, ModuleArtifactIDs: j.Request.ModuleArtifactIDs}
	if isCanary {
		out.Task.TaskID = t.CanaryTaskID
		out.Task.Input = append(json.RawMessage(nil), canary.Input...)
	}
	out.InputJSON = string(out.Task.Input)
	out.Envelope, er = e.Signer.Sign(struct {
		Lease   Lease    `json:"lease"`
		Task    TaskSpec `json:"task"`
		Modules []string `json:"moduleArtifactIds"`
	}{l, out.Task, j.Request.ModuleArtifactIDs})
	if er != nil {
		return Assignment{}, er
	}
	// The signed task envelope (WWPP-R37.1): Ed25519 over canonical JSON.
	// The measure can be switched off by the admin (R37.1b).
	if e.TrustConfig().EnvelopeEnabled || dewEnabled(j) {
		out.TaskEnvelope, er = e.signTaskEnvelope(j, t, l, out.Task.Input)
		if er != nil {
			return Assignment{}, er
		}
		t.EnvelopeDigest = verify.SHA256(append(append([]byte(nil), out.TaskEnvelope.Payload...), out.TaskEnvelope.Signature...))
		t.EnvelopeUsed = false
	}
	e.mu.RLock()
	keyID := e.TaskKeyID
	e.mu.RUnlock()
	e.Log.Info("lease.granted", "jobId", j.JobID, "taskId", t.TaskID, "workerId", worker, "generation", t.Generation, "keyId", keyID, "canaryId", t.CanaryID)
	return out, nil
}

func (e *Engine) maxBatchSize(w Worker, available int, verification bool) int {
	limit := MaxBatchSize
	if available < limit {
		limit = available
	}
	if verification && limit < 1 {
		return 1
	}
	return limit
}

// Called only after an active member's worker/token/generation was authenticated.
// Settled or expired members are never revived by a sibling's heartbeat.
func (e *Engine) renewBatch(j *Job, anchor Lease) error {
	now := e.Now()
	for i := range j.Tasks {
		t := &j.Tasks[i]
		l := t.Lease
		if t.State == "leased" && l != nil && l.WorkerID == anchor.WorkerID && l.Token == anchor.Token && l.ExpiresAt.After(now) {
			// No Lua/EVAL per owner decision: GET+PEXPIRE remains a documented
			// race, but every Redis error is surfaced to the heartbeat caller.
			if e.redis != nil {
				key := "lease:" + j.JobID + ":" + l.TaskID
				v, err := e.redis.Get(key)
				if err != nil {
					return err
				}
				if v != l.Token {
					return ErrConflict
				}
				ms := int(e.TTL / time.Millisecond)
				if err := e.redis.PExpire(key, ms); err != nil {
					return err
				}
			}
			l.ExpiresAt = renewedLeaseExpiry(t, now, e.TTL)
		}
	}
	return nil
}

func renewedLeaseExpiry(t *Task, now time.Time, ttl time.Duration) time.Time {
	extension := ttl
	if t.TimeoutMS > 0 {
		if t.Lease == nil || t.Lease.StartedAt.IsZero() {
			return now.Add(ttl).UTC()
		}
		remaining := time.Duration(t.TimeoutMS)*time.Millisecond - now.Sub(t.Lease.StartedAt)
		if remaining < extension {
			extension = remaining
		}
	}
	if extension < 0 {
		extension = 0
	}
	return now.Add(extension).UTC()
}
