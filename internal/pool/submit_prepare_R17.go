// Path: internal/pool/submit_prepare_R17.go | Author: CommandCode for the owner | 2026-11-22
// What/why: WO R17-SCALE item 2 (A1 + G-R16-2). The expensive part of a submission -
// the Ed25519 envelope verify, the dew receipt verify, the SHA-256 of the output and the
// optional sampled recompute - runs BEFORE the settlement transaction, inside a slot of
// the existing bounded worker pool (workerSem). The settlement transaction keeps its own
// classification and every cheap re-check (task generation, lease token, EnvelopeUsed,
// terminal job, the expired-lease late rules, the canary bypass and the dew dead
// letters); it only stops recomputing the work for which it already holds a matching
// verdict, so no claim/submit transaction waits for a verify or a recompute.
package pool

import (
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
	"context"
	"fmt"
	"time"
)

// submitSnapshot is the read-only state a submission is prepared against: the job and
// the task exactly as the settlement transaction reads them, and the lease the
// submission resolves to. The snapshot only decides which expensive work is worth
// running; the transaction remains the only writer and the only refuser.
type submitSnapshot struct {
	// presented is the lease the caller submits.
	presented Lease
	// job and task are the read-only copies the verdict was judged against.
	job  Job
	task *Task
	// taskID, gen and token identify the task state the verdict was judged against.
	taskID string
	gen    uint64
	token  string
	// lease is the resolved lease, nil when the cheap rules already refuse the
	// submission (terminal job, stale or expired lease).
	lease *Lease
	// late is true when the submission answers an expired lease whose evidence binds
	// it (the F9 late-result rules).
	late bool
	// canaryID is the canary riding with the task, empty when there is none.
	canaryID string
}

// matches reports whether the snapshot still describes the task the settlement
// transaction reads. A task that moved invalidates the verdict.
func (s *submitSnapshot) matches(task *Task, late bool) bool {
	if s == nil || s.lease == nil || task == nil {
		return false
	}
	if s.late != late {
		return false
	}
	if task.TaskID != s.taskID || task.Generation != s.gen || leaseTokenOf(task) != s.token {
		return false
	}
	return !task.EnvelopeUsed
}

// leaseTokenOf is the token of the lease the task currently holds; empty when it holds
// none.
func leaseTokenOf(t *Task) string {
	if t == nil || t.Lease == nil {
		return ""
	}
	return t.Lease.Token
}

// resolveSubmissionTask returns the task a submission answers: a developer task, or the
// canary task riding with it.
func resolveSubmissionTask(j *Job, taskID string) *Task {
	for i := range j.Tasks {
		if j.Tasks[i].TaskID == taskID || j.Tasks[i].CanaryTaskID == taskID {
			return &j.Tasks[i]
		}
	}
	return nil
}

// leaseLive reports whether the submission presents exactly the lease the task holds.
func leaseLive(task *Task, worker string, q Submission, now time.Time) bool {
	l := task.Lease
	return l != nil && l.WorkerID == worker && q.Lease.WorkerID == worker &&
		l.Generation == q.Lease.Generation && l.Token == q.Lease.Token && l.ExpiresAt.After(now)
}

// leaseLate is the expired-lease late rule: the recorded snapshot of the expired lease
// must be the lease the submission presents, it must not be consumed yet, it must still
// be the current generation, and the active lease (when any) must not have been
// reissued under a different token.
func leaseLate(task *Task, owner, worker string, q Submission) bool {
	ev := task.ExpiredLease
	return ev != nil && !ev.Consumed && task.Generation == ev.Lease.Generation &&
		ev.Lease.WorkerID == worker && q.Lease.WorkerID == worker &&
		ev.Lease.Token == q.Lease.Token && q.Lease.Generation == ev.Lease.Generation &&
		ev.ContributorKeyID == owner && (task.Lease == nil || task.Lease.Token == ev.Lease.Token)
}

// snapshotSubmit reads the state a submission is prepared against, without a write
// transaction. The caller ignores a snapshot error: the settlement transaction then
// produces the authoritative error and the expensive work simply stays inline.
func (e *Engine) snapshotSubmit(ctx context.Context, p, owner, worker string, q Submission) (*submitSnapshot, error) {
	s := &submitSnapshot{presented: q.Lease}
	err := e.Store.View(ctx, func(r store.Reader) error {
		var w Worker
		if err := r.Get(store.Workers, worker, &w); err != nil {
			return err
		}
		if w.Owner != owner || w.PoolID != p {
			return ErrForbidden
		}
		var j Job
		if err := r.Get(store.Jobs, q.Lease.JobID, &j); err != nil {
			return err
		}
		if j.PoolID != p {
			return ErrForbidden
		}
		task := resolveSubmissionTask(&j, q.Lease.TaskID)
		if task == nil {
			return store.ErrNotFound
		}
		s.job, s.task = j, task
		s.taskID, s.gen, s.token, s.canaryID = task.TaskID, task.Generation, leaseTokenOf(task), task.CanaryID
		if terminal(j) {
			return nil
		}
		now := e.Now()
		if leaseLive(task, worker, q, now) {
			l := *task.Lease
			s.lease = &l
			return nil
		}
		if leaseLate(task, owner, worker, q) {
			l := task.ExpiredLease.Lease
			s.lease = &l
			s.late = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// submitPrepare is the verdict computed outside the settlement transaction. Every field
// is a value the transaction used to recompute; none of them decides anything by itself.
type submitPrepare struct {
	// envelopeErr is the refusal of the envelope measure, nil when it passed or when
	// the measure does not apply to the job.
	envelopeErr error
	// envelopeChecked reports that the measure applied.
	envelopeChecked bool
	// envelopeDur is the measured verification cost of the envelope.
	envelopeDur time.Duration

	// dewErr is the refusal of the dew receipt check, nil when it passed.
	dewChecked bool
	dewErr     error

	// outputDigest is the SHA-256 of the exact submitted bytes and hashDur its cost.
	outputDigest string
	hashDur      time.Duration

	// recomputeSampled records that the sample recompute applies to the task;
	// recomputeMatch and recomputeRan are its verdict. recomputeInvalid marks a
	// malformed claimed digest (G-R17-2) and recomputePinned a self-checked
	// reference size (G-R17-3).
	recomputeSampled bool
	recomputeMatch   bool
	recomputeRan     bool
	recomputeInvalid bool
	recomputePinned  bool

	// rerunFirst is the held first result of a re-run comparison and rerunMatch the
	// exact comparison verdict; rerunFirst is nil when the task waits for no re-run.
	rerunFirst *PendingResult
	rerunMatch bool
	rerunDur   time.Duration
}

// prepareSubmit runs the expensive, store-free checks of a submission against the
// snapshot. The key verifications run in a slot of the bounded worker pool, so their
// number never exceeds the cap; the recompute keeps its own non-blocking slot exactly as
// before, so a busy pool skips the comparison instead of queueing a submission.
func (e *Engine) prepareSubmit(s *submitSnapshot, q Submission) *submitPrepare {
	p := &submitPrepare{}
	if s.lease == nil {
		return p
	}
	j, task, l := &s.job, s.task, s.lease

	cfg := e.TrustConfig()
	if cfg.EnvelopeEnabled || dewEnabled(j) || q.Offline || q.PeerRelayed || q.LeaseReceipt != nil {
		p.envelopeChecked = true
		envStart := time.Now()
		p.envelopeErr = e.holdVerifySlot(func() error {
			var envelopeErr error
			if s.late {
				envelopeErr = verify.VerifyTaskEnvelopeAllowExpired(q.TaskEnvelope, e.Verifier.Keyring(), e.Now())
			} else {
				envelopeErr = e.verifyRenewedEnvelope(q.TaskEnvelope, *l)
			}
			if envelopeErr != nil {
				return fmt.Errorf("%w: task envelope: %v", ErrConflict, envelopeErr)
			}
			env, err := verify.DecodeTaskEnvelope(q.TaskEnvelope)
			expectedInput := task.Input
			if task.CanaryID != "" {
				c, canaryErr := e.Verifier.Canaries().Get(task.CanaryID)
				if canaryErr != nil {
					return fmt.Errorf("%w: canary binding", ErrConflict)
				}
				expectedInput = c.Input
			}
			expectedTaskID := task.TaskID
			if task.CanaryTaskID != "" && q.Lease.TaskID == task.CanaryTaskID {
				expectedTaskID = task.CanaryTaskID
			}
			if err != nil || env.JobID != j.JobID || env.TaskID != expectedTaskID || env.WorkerID != l.WorkerID || env.LeaseToken != l.Token || env.ArtifactSHA256 != moduleDigest(j.Request.ModuleArtifactIDs) || env.InputSHA256 != verify.SHA256(expectedInput) {
				return fmt.Errorf("%w: task envelope binding", ErrConflict)
			}
			return nil
		})
		p.envelopeDur = time.Since(envStart)
	}
	// R11 Part E addendum: a refused dew receipt leaves evidence. The check runs on the
	// resolved lease, so it can tell "the receipt expired because the active lease
	// outlived it" from a forgery, and it runs after the envelope block so the tamper
	// evidence of a forged envelope is recorded first.
	p.dewChecked = true
	p.dewErr = e.holdVerifySlot(func() error {
		return e.verifyDewSubmission(j, task, q, l.ExpiresAt)
	})

	hashStart := time.Now()
	p.outputDigest = verify.SHA256(q.Output)
	p.hashDur = time.Since(hashStart)

	// R11-G1: a submission that answers a canary IS the canary evidence. It is never
	// judged by the developer measures below.
	canarySubmission := task.CanaryID != "" || task.CanaryTaskID != ""
	if !canarySubmission && task.Pending == nil && e.recomputeSampled(j, task) {
		p.recomputeSampled = true
		v := e.recomputeVerdictFull(j, task, q.Output)
		p.recomputeMatch, p.recomputeRan = v.match, v.ran
		p.recomputeInvalid, p.recomputePinned = v.invalid, v.pinned
	}
	// The re-run comparison (WWPP-R35.1): the second result of a sampled task is
	// compared with the first one by exact hash only; the server never runs developer
	// code.
	if task.Pending != nil {
		rerunStart := time.Now()
		p.rerunFirst = task.Pending
		p.rerunMatch = verify.CompareResults(p.rerunFirst.exactBytes(), q.Output) && verify.CompareResultHashes(p.rerunFirst.SHA256, p.outputDigest)
		p.rerunDur = time.Since(rerunStart)
	}
	return p
}

// holdVerifySlot runs one key verification in a slot of the bounded worker pool, so the
// concurrent number of Ed25519 verifications never exceeds the cap. The slot is released
// before the caller continues, so the non-blocking recompute slot below is never starved
// by the verification of the same submission.
func (e *Engine) holdVerifySlot(f func() error) error {
	sem := e.workerPool()
	if sem.Cap() == 0 {
		return f()
	}
	if err := sem.Acquire(context.Background()); err != nil {
		return err
	}
	defer sem.Release()
	return f()
}

// recomputeVerdictFor only consumes a reference verdict prepared before
// Store.Update. A missing, stale, or capacity-skipped verdict is unavailable;
// never invoke a CPU-heavy reference verifier inside the write transaction.
// A skipped comparison never accuses a contributor (R17-SCALE A1).
func (e *Engine) recomputeVerdictFor(prep *submitPrepare, j *Job, task *Task, output []byte) (match, ran bool) {
	v := e.recomputeVerdictStateFor(prep, j, task, output)
	return v.match, v.ran
}

// recomputeVerdictStateFor consumes the full prepared verdict of a submission.
// The same A1 rule applies: only a verdict prepared before the transaction is
// ever consumed, so the CPU-heavy verifier never runs inside it.
func (e *Engine) recomputeVerdictStateFor(prep *submitPrepare, j *Job, task *Task, output []byte) recomputeVerdictOutcome {
	if prep != nil && prep.recomputeSampled && prep.recomputeRan {
		return recomputeVerdictOutcome{
			match:   prep.recomputeMatch,
			ran:     true,
			invalid: prep.recomputeInvalid,
			pinned:  prep.recomputePinned,
		}
	}
	return recomputeVerdictOutcome{}
}
