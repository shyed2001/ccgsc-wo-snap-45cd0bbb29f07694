// Path: internal/pool/contributor_counters_F15.go
// What/why: F15 contributor counters. Accepted tasks and compute milliseconds are
// pool_metrics samples, not a ledger. A signed-in account is one counter across
// devices. An anonymous contributor is one counter per browser install id, the
// host id the contribute page keeps. The result that settles a task takes the
// task credit. A later valid duplicate inside the dew window takes compute only.
// F15-01/04: a credit is decided by one atomic create of a durable marker, never
// by a read of the samples followed by a write of one more sample.
package pool

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
)

const (
	contributorAcceptedName = "contributor_accepted_tasks"
	contributorComputeName  = "contributor_compute_ms"
	contributorSourceSep    = "\x1f"
	// contributorCreditBucket holds the durable marker of one credit. The
	// marker is the decision; the metric sample is only its visible result.
	contributorCreditBucket = store.Idempotency
	// contributorCreditPrefix marks the accepted-task credit of one task.
	contributorCreditPrefix = "credit/"
	// contributorComputePrefix marks the compute credit of one lease
	// generation of one task.
	contributorComputePrefix = "compute/"
)

// ContributorCounter is the accepted-task count and the compute milliseconds
// of one contributor, or of every contributor when the read is the monitor total.
type ContributorCounter struct {
	AcceptedTasks float64 `json:"acceptedTasks"`
	ComputeMS     float64 `json:"computeMs"`
}

// ContributorAccountKey is the counter of one signed-in account. Devices of
// that account share it.
func ContributorAccountKey(owner string) string {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return ""
	}
	return "account:" + owner
}

// ContributorInstallKey is the counter of one anonymous browser install. The
// id is the host id stored by the contribute page, not an id from the runner.
func ContributorInstallKey(hostID string) string {
	hostID = strings.TrimSpace(hostID)
	if hostID == "" {
		return ""
	}
	return "install:" + hostID
}

func contributorCounterKey(w Worker) string {
	if w.Anonymous || auth.IsAnonymousID(w.Owner) {
		return ContributorInstallKey(w.HostID)
	}
	return ContributorAccountKey(w.Owner)
}

func contributorSource(publicKey, taskID, kind string) string {
	return publicKey + contributorSourceSep + taskID + contributorSourceSep + kind
}

func contributorPublicKey(source string) string {
	if i := strings.IndexByte(source, contributorSourceSep[0]); i >= 0 {
		return source[:i]
	}
	return source
}

// contributorCreditKey is the durable marker of the accepted-task credit of one
// task of one job. The key carries no contributor: the result that settles a
// task takes the task credit, whoever contributed it.
func contributorCreditKey(jobID, taskID string) string {
	return contributorCreditPrefix + jobID + "/" + taskID
}

// contributorComputeKey is the durable marker of the compute credit of one
// lease generation of one task of one job.
func contributorComputeKey(jobID, taskID string, generation uint64) string {
	return contributorComputePrefix + jobID + "/" + taskID + "/" + strconv.FormatUint(generation, 10)
}

// contributorCreditMarker is the durable record of one decided credit. It is
// the decision itself; the metric sample is only its visible result.
type contributorCreditMarker struct {
	Key string    `json:"key"`
	At  time.Time `json:"at"`
}

func contributorComputeMS(now time.Time, lease *Lease) (float64, bool) {
	if lease == nil || lease.StartedAt.IsZero() {
		return 0, false
	}
	end := now
	if !lease.ExpiresAt.IsZero() && end.After(lease.ExpiresAt) {
		end = lease.ExpiresAt
	}
	ms := float64(end.Sub(lease.StartedAt).Nanoseconds()) / 1e6
	if ms < 0 {
		ms = 0
	}
	return ms, true
}

func taskAlreadyCredited(samples []store.MetricSample, taskID string) bool {
	for _, sample := range samples {
		if sample.Name == contributorAcceptedName && strings.Contains(sample.Source, contributorSourceSep+taskID+contributorSourceSep+"task") {
			return true
		}
	}
	return false
}

// claimContributorCredit decides one credit with one atomic create. The marker
// is inserted only when it is absent, so exactly one of the racing transactions
// wins the credit and the others write nothing. F15-01/04: the decision never
// depends on a read of the samples, which two transactions could both miss.
func (e *Engine) claimContributorCredit(tx store.Tx, key string) (bool, error) {
	if tx == nil || key == "" {
		return false, nil
	}
	atomic, ok := tx.(store.PutIfAbsentTx)
	if !ok {
		// A transaction without the optional atomic create cannot decide a
		// credit safely. It must not fall back to the old read-then-write.
		return false, store.ErrNotImplemented
	}
	inserted, err := atomic.PutIfAbsent(contributorCreditBucket, key, contributorCreditMarker{Key: key, At: e.Now().UTC()})
	if err != nil {
		return false, err
	}
	return inserted, nil
}

// creditContributor writes one contributor's task credit and, when the lease
// measured it, that lease's compute milliseconds. A repeated lease writes nothing.
func (e *Engine) creditContributor(tx store.Tx, j *Job, w Worker, taskID string, lease *Lease, taskCredit bool) error {
	if e == nil || j == nil || taskID == "" {
		return nil
	}
	key := contributorCounterKey(w)
	if key == "" {
		return nil
	}
	if taskCredit {
		// The task credit is decided by the atomic create of the task marker.
		claimed, err := e.claimContributorCredit(tx, contributorCreditKey(j.JobID, taskID))
		if err != nil {
			return err
		}
		if claimed {
			if err := e.recordMetric(tx, *j, contributorAcceptedName, 1, "count", contributorSource(key, taskID, "task")); err != nil {
				return err
			}
		}
	}
	ms, ok := contributorComputeMS(e.Now(), lease)
	if !ok || lease.Token == "" {
		return nil
	}
	if lease.WorkerID != "" && lease.WorkerID != w.TabID {
		return nil
	}
	// The compute credit is decided the same way, per lease generation of the
	// task. One generation of one task is credited once.
	claimed, err := e.claimContributorCredit(tx, contributorComputeKey(j.JobID, taskID, lease.Generation))
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	return e.recordMetric(tx, *j, contributorComputeName, ms, "ms", contributorSource(key, taskID, "lease:"+lease.Token))
}

func (e *Engine) workerByID(tx store.Tx, id string) (Worker, bool, error) {
	var w Worker
	if id == "" {
		return w, false, nil
	}
	if err := tx.Get(store.Workers, id, &w); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return w, false, nil
		}
		return w, false, err
	}
	return w, true, nil
}

// creditSettled records the task credit of the accepted result and the compute
// time of the lease that is settling it.
func (e *Engine) creditSettled(tx store.Tx, j *Job, r *Result, lease *Lease) error {
	if r == nil || r.Status != "done" {
		return nil
	}
	w, ok, err := e.workerByID(tx, r.WorkerID)
	if err != nil || !ok {
		return err
	}
	var taskLease *Lease
	if lease != nil && (lease.WorkerID == "" || lease.WorkerID == w.TabID) {
		taskLease = lease
	}
	if err := e.creditContributor(tx, j, w, r.TaskID, taskLease, true); err != nil {
		return err
	}
	if lease == nil || lease.WorkerID == "" || lease.WorkerID == w.TabID {
		return nil
	}
	other, ok, err := e.workerByID(tx, lease.WorkerID)
	if err != nil || !ok {
		return err
	}
	return e.creditContributor(tx, j, other, r.TaskID, lease, false)
}

// finishAccepted credits the contributor and emits task.done. Settlement paths
// call it instead of emitting the event directly.
func (e *Engine) finishAccepted(tx store.Tx, j *Job, r *Result, lease *Lease) error {
	if err := e.creditSettled(tx, j, r, lease); err != nil {
		return err
	}
	return e.event(tx, *j, "task.done", r)
}

// creditDewDuplicate credits compute time for a valid copy of an already
// settled result while the submission's dew receipt is still inside its window.
// It does not credit another accepted task and it does not change the result.
func (e *Engine) creditDewDuplicate(tx store.Tx, j *Job, task *Task, w Worker, q Submission) error {
	if j == nil || task == nil || task.State != "done" || q.Error != "" || q.LeaseReceipt == nil {
		return nil
	}
	if !dewEnabled(j) {
		return nil
	}
	if q.Lease.WorkerID != w.TabID || q.Lease.Token == "" || q.LeaseReceipt.ReceiptID != q.Lease.Token {
		return nil
	}
	covers := false
	for _, id := range q.LeaseReceipt.TaskIDs {
		if id == task.TaskID {
			covers = true
			break
		}
	}
	if !covers {
		return nil
	}
	submitted := strings.TrimSpace(q.SHA256)
	if submitted == "" && len(q.Output) > 0 {
		submitted = verify.SHA256(q.Output)
	}
	matched := false
	for _, result := range j.Results {
		if result.TaskID != task.TaskID || result.Status != "done" || result.SHA256 == "" {
			continue
		}
		if verify.CompareResultHashes(result.SHA256, submitted) {
			matched = true
			break
		}
	}
	if !matched {
		return nil
	}
	if err := verify.VerifyLeaseReceipt(*q.LeaseReceipt, e.Verifier.Keyring(), e.Now()); err != nil {
		return nil
	}
	return e.creditContributor(tx, j, w, task.TaskID, &q.Lease, false)
}

// ContributorCounters sums the durable samples. An empty public key is the
// monitor total. A public key is one account or one browser install.
func (e *Engine) ContributorCounters(ctx context.Context, publicKey string) (ContributorCounter, error) {
	if e == nil || e.Store == nil {
		return ContributorCounter{}, nil
	}
	samples, err := store.QueryMetricSamples(ctx, e.Store, store.MetricFilter{})
	if err != nil {
		return ContributorCounter{}, err
	}
	return sumContributorCounters(samples, strings.TrimSpace(publicKey)), nil
}

func sumContributorCounters(samples []store.MetricSample, publicKey string) ContributorCounter {
	var out ContributorCounter
	for _, sample := range samples {
		key := contributorPublicKey(sample.Source)
		if publicKey != "" {
			if key != publicKey {
				continue
			}
		} else if !strings.HasPrefix(key, "account:") && !strings.HasPrefix(key, "install:") {
			continue
		}
		switch sample.Name {
		case contributorAcceptedName:
			out.AcceptedTasks += sample.Value
		case contributorComputeName:
			out.ComputeMS += sample.Value
		}
	}
	return out
}

// OwnsAnonymousInstall reports whether this anonymous subject registered the
// browser install id. The id is the contribute page's host id.
func (e *Engine) OwnsAnonymousInstall(ctx context.Context, owner, install string) (bool, error) {
	if e == nil || e.Store == nil || !auth.IsAnonymousID(owner) || strings.TrimSpace(install) == "" {
		return false, nil
	}
	install = strings.TrimSpace(install)
	var owns bool
	err := e.Store.View(ctx, func(r store.Reader) error {
		rows, err := r.List(store.Workers)
		if err != nil {
			return err
		}
		for _, row := range rows {
			var w Worker
			if err := json.Unmarshal(row.Value, &w); err != nil {
				return err
			}
			if w.Owner == owner && w.HostID == install && (w.Anonymous || auth.IsAnonymousID(w.Owner)) {
				owns = true
				return nil
			}
		}
		return nil
	})
	return owns, err
}
