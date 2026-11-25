package pool

// F12 (owner decisions 2026-11-24, WO section 16): run-capped receipts and the
// duplicate-result ledger. G-F12-2: every receipt of a run dies when the run
// ends or is cancelled, so verification re-checks the run state at submit time
// instead of trusting only the signed deadline. G-F12-4: the first valid result
// settles the task; a later valid result inside the receipt window gets
// compute-time credit only, and ALL results are recorded for the developer.
// The platform never compares results (G-F3): the ledger is append-only
// evidence, comparison is the developer's job.

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
)

// dewRunEnded reports that the run of one job is over: its receipts are dead
// (G-F12-2, G-F12-6) and no further result may settle or earn credit. The
// owner named three terminal states: completed, cancelled, or failed.
func dewRunEnded(j *Job) bool {
	return j != nil && (j.State == "completed" || j.State == "cancelled" || j.State == "failed")
}

// dewEnvelopeDigest is the SHA-256 over the exact signed envelope bytes,
// payload followed by signature: the same digest the F9 late-lease evidence
// keeps, so both fences bind the identical envelope.
func dewEnvelopeDigest(signed verify.SignedTaskEnvelope) string {
	return verify.SHA256(append(append([]byte(nil), signed.Payload...), signed.Signature...))
}

// DewResultRow is one recorded submission (G-F12-4). It keeps no bearer
// secret: the lease token appears only as a SHA-256 and the output only as
// its digest, so the developer correlates rows with the artifacts it holds.
type DewResultRow struct {
	PoolID           string    `json:"poolId"`
	JobID            string    `json:"jobId"`
	TaskID           string    `json:"taskId"`
	WorkerID         string    `json:"workerId"`
	Generation       uint64    `json:"generation"`
	LeaseTokenSHA256 string    `json:"leaseTokenSha256"`
	OutputSHA256     string    `json:"outputSha256"`
	ComputeMS        int64     `json:"computeMs,omitempty"`
	At               time.Time `json:"at"`
}

func dewResultKey(row DewResultRow) string {
	return strings.Join([]string{
		row.PoolID, row.JobID, row.TaskID,
		strconv.FormatUint(row.Generation, 10), row.LeaseTokenSHA256, row.OutputSHA256,
	}, "\x00")
}

// recordDewResultTx appends one submission to the DewResults ledger. The
// byte-identical (lease, output) pair is a replay and stays ErrConflict; a
// different output of the same lease inside the window is a duplicate result:
// recorded, never settled again (G-F12-4).
func recordDewResultTx(tx store.Tx, row DewResultRow) error {
	key := dewResultKey(row)
	var old DewResultRow
	if err := tx.Get(store.DewResults, key, &old); err == nil {
		return ErrConflict
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return tx.Put(store.DewResults, key, row)
}

// dewComputeCreditTx grants the idempotent compute-time credit of one recorded
// duplicate result (G-F12-4: later valid results get compute credit only, the
// task stays settled). The claimed ComputeMS only ever lowers the credit: it is
// bounded by the span the receipt actually covers (issue -> min(now, deadline)),
// so a contributor cannot claim more than its window allows.
func dewComputeCreditTx(tx store.Tx, r verify.LeaseReceipt, row DewResultRow, computeMS int64, now time.Time) (int64, error) {
	key := "compute\x00" + dewResultKey(row)
	var old DewResultRow
	if err := tx.Get(store.DewCredits, key, &old); err == nil {
		return old.ComputeMS, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return 0, err
	}
	elapsed := now.Unix() - r.IssuedAt
	if elapsed < 0 {
		elapsed = 0
	}
	if elapsed > r.WindowSeconds {
		elapsed = r.WindowSeconds
	}
	bounded := elapsed * 1000
	if computeMS < 0 {
		bounded = 0
	} else if computeMS < bounded {
		bounded = computeMS
	}
	row.ComputeMS = bounded
	return bounded, tx.Put(store.DewCredits, key, row)
}

// submitDewDuplicateTx is the F12 duplicate branch of Submit (G-F12-4). It
// runs inside the submission transaction when the task is already settled
// (its envelope is used), the submission still carries its per-task receipt,
// and the receipt window is open: the result is recorded for the developer and
// earns compute-time credit only - the task, its credit and its recorded
// result stay untouched. Byte-identical replays, submissions outside the
// window and results after the run ended stay ErrConflict.
func (e *Engine) submitDewDuplicateTx(tx store.Tx, j *Job, task *Task, q Submission) (int64, error) {
	if task == nil || !task.EnvelopeUsed {
		return 0, ErrInvalid
	}
	r := q.LeaseReceipt
	if r == nil {
		return 0, ErrInvalid
	}
	now := e.Now().UTC()
	if now.Before(time.Unix(r.IssuedAt, 0)) || !now.Before(time.Unix(r.Deadline, 0)) {
		return 0, ErrConflict
	}
	if dewRunEnded(j) {
		return 0, ErrConflict
	}
	row := DewResultRow{
		PoolID:           j.PoolID,
		JobID:            j.JobID,
		TaskID:           q.Lease.TaskID,
		WorkerID:         q.Lease.WorkerID,
		Generation:       q.Lease.Generation,
		LeaseTokenSHA256: verify.SHA256([]byte(q.Lease.Token)),
		OutputSHA256:     verify.SHA256(q.Output),
		At:               now,
	}
	if err := recordDewResultTx(tx, row); err != nil {
		return 0, err
	}
	return dewComputeCreditTx(tx, *r, row, q.ComputeMS, now)
}

// finishDewDuplicateTx is the Submit hook for a later result of an already
// settled Dew task (G-F12-4). The receipt is checked again, a byte-identical
// output stays a conflict, and a different output inside the window is
// recorded with compute-time credit only. The task and its settled result are
// not written again.
func (e *Engine) finishDewDuplicateTx(tx store.Tx, j *Job, task *Task, q Submission, worker string) (Result, error) {
	if err := e.verifyDewSubmission(j, task, q, e.Now().Add(time.Second)); err != nil {
		return Result{}, err
	}
	outHash := verify.SHA256(q.Output)
	for _, r := range j.Results {
		if r.TaskID == task.TaskID && r.SHA256 != "" && r.SHA256 == outHash {
			return Result{}, dewReject("replay")
		}
	}
	if _, err := e.submitDewDuplicateTx(tx, j, task, q); err != nil {
		return Result{}, err
	}
	w, ok, err := e.workerByID(tx, worker)
	if err != nil {
		return Result{}, err
	}
	if ok {
		// Dew keeps its per-result ledger, while F15 counts this lease's
		// compute time once through the atomic contributor marker.
		if err := e.creditContributor(tx, j, w, task.TaskID, &q.Lease, false); err != nil {
			return Result{}, err
		}
	}
	return Result{
		JobID: j.JobID, TaskID: task.TaskID, Status: "recorded",
		SHA256: outHash, At: e.Now().UTC(), WorkerID: worker, Attempts: task.Attempts,
	}, nil
}

// ListDewResults returns the recorded results of one job for its developer
// (G-F12-4: all results are recorded; the platform does not compare them).
// Ownership is checked through the job read; HTTP authorization stays with the
// API layer.
func (e *Engine) ListDewResults(ctx context.Context, p, owner, jobID string) ([]DewResultRow, error) {
	if _, err := e.Get(ctx, p, owner, jobID); err != nil {
		return nil, err
	}
	rows := []DewResultRow{}
	err := e.Store.View(ctx, func(rd store.Reader) error {
		recs, err := rd.List(store.DewResults)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			var row DewResultRow
			if err := json.Unmarshal(rec.Value, &row); err != nil {
				return err
			}
			if row.PoolID == p && row.JobID == jobID {
				rows = append(rows, row)
			}
		}
		return nil
	})
	return rows, err
}
