// Path: internal/pool/engine.go | Author: Codex for Claude Opus 5.5 | 2026-11-17
// What/why: shared atomic job/lease/fencing engine; WWPP-R18.2, R32.2, R32.9.
package pool

import (
	"ccgsc/poolscore/internal/appegress"
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/caps"
	"ccgsc/poolscore/internal/recompute"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/redislease"
	"ccgsc/poolscore/internal/verify"
	"ccgsc/poolscore/web"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Engine struct {
	egressSignerMu sync.Mutex
	egressSigner   *appegress.Signer
	egressReady    atomic.Bool
	Store          store.Store
	Log            *slog.Logger
	Signer         *verify.Signer
	ServerID       string
	TTL            time.Duration
	Now            func() time.Time

	// Trust wiring of the tamper measures (WWPP-R37.1, T4.2).
	mu                sync.RWMutex
	taskKeyRotationMu sync.Mutex
	settingsMu        sync.Mutex
	// poolSettings holds the Director controlled verification settings of each
	// pool (W10C row 3), guarded by mu.
	poolSettings map[string]PoolSettings
	Verifier     *verify.Verifier
	TaskKey      ed25519.PrivateKey
	TaskKeyID    string
	// retiredTaskKeyIDs is the set of task key ids that were current before an
	// admin rotation (W9c row 1). Only these ids may refund a stale-runner
	// report; a random id the server never issued may not.
	retiredTaskKeyIDs      map[string]struct{}
	WebhookAllowPrivateIPs bool
	rerunSecret            []byte
	trust                  TrustConfig
	canaryIDs              []string
	scheduler              *schedulerState
	chain                  ChainConfig
	gpu                    GPUConfig
	anonymousLimits        AnonymousLimits
	// repPersistMu makes the snapshot and its sequence one ordered pair;
	// repPersistSeq orders the persisted reputation writes (AT22-L3-4).
	repPersistMu  sync.Mutex
	repPersistSeq uint64

	// MVPW-D: lease_store switch (pg default; redis for issue/renew/expire fencing)
	leaseStore string
	redis      *redislease.Client

	// R15 AT22-GM2-3 (TD-17): the bounded worker pool of the CPU heavy platform
	// work (the native recompute). The pointer is read on the hot path, so it has
	// its own lock: the engine lock is never taken inside a store transaction
	// (that would invert the lock order of the settings writers).
	workerPoolMu sync.RWMutex
	workerSem    *caps.Semaphore

	// R16-FIX (F01/F02/F03): the durable admin caps record and the knobs of the
	// configuration file. capsMu guards both, and UpdateCapsSettings holds it
	// across the store commit, so a partial write merges atomically and a failing
	// store leaves the cache unchanged.
	capsMu            sync.Mutex
	capsSettings      CapsSettings
	capsSettingsSaved bool
	configuredCaps    caps.Config
	appliedCaps       caps.Caps
	appliedVersion    int

	// WO R17-RECOMPUTE (G-R16-4): the generic reference-recompute registry of
	// the platform. It is set by the wiring outside internal/pool, so the
	// platform never imports workload code (CR-53). recomputeRegistryMu guards
	// the pointer because the wiring may set it after the engine was built.
	recomputeRegistryMu sync.RWMutex
	recomputeRegistry   *recompute.Registry

	// F6: writes are serialized by recomputeWriteMu, while recomputeMu
	// protects only the small in-process cache. PG row locks enforce cross-node
	// durable CAS, and network/DB latency cannot block the worker hot path.
	recomputeWriteMu  sync.Mutex
	recomputeMu       sync.Mutex
	recomputeSettings map[string]RecomputeSettings
	recomputeNotify   recomputeNotifyState

	// WO R18-RECOMPUTE-POLICY (G-R17-1): the knobs of the deferred recompute
	// retry. Zero values resolve to the defaults (three retries, one re-run
	// window per retry); tests shorten them. They are written once at setup,
	// so no lock guards them.
	deferRecomputeRetries int
	deferRecomputeWaitDur time.Duration
}

type reputationObservation struct {
	workerID string
	kind     verify.EventKind
	taskID   string
	detail   string
}

func (e *Engine) observeCommitted(observations []reputationObservation) {
	for _, observation := range observations {
		e.observe(observation.workerID, observation.kind, observation.taskID, observation.detail)
	}
	if len(observations) > 0 {
		e.persistReputation()
	}
}

// New builds an engine with the four tamper measures enabled at their defaults
// (hash check and signed envelope on, re-run at 1 %, canaries and reputation on).
func New(s store.Store, l *slog.Logger, id string, ttl time.Duration) (*Engine, error) {
	if ttl <= 0 || s == nil || l == nil || id == "" {
		return nil, ErrInvalid
	}
	signer, e := verify.NewSigner()
	if e != nil {
		return nil, e
	}
	taskKey, taskKeyID, keyringEntries, retiredKeyIDs, e := loadOrCreateTaskKey(s)
	if e != nil {
		return nil, e
	}
	rerunSecret, e := loadOrCreateRerunSecret(s)
	if e != nil {
		return nil, e
	}
	v := verify.NewVerifier(nil)
	if e = v.Keyring().Restore(keyringEntries); e != nil {
		return nil, e
	}
	engine := &Engine{
		Store:             s,
		Log:               l,
		Signer:            signer,
		ServerID:          id,
		TTL:               ttl,
		Now:               time.Now,
		Verifier:          v,
		TaskKey:           taskKey,
		TaskKeyID:         taskKeyID,
		retiredTaskKeyIDs: retiredTaskKeyIDSet(retiredKeyIDs),
		rerunSecret:       rerunSecret,
		trust:             DefaultTrustConfig(),
		scheduler:         newSchedulerState(),
		chain:             DefaultChainConfig(),
		gpu:               DefaultGPUConfig(),
		anonymousLimits:   DefaultAnonymousLimits(),

		// MVPW-D default
		leaseStore: "pg",
		redis:      nil,
	}

	if err := rebuildSessionIndex(engine); err != nil {
		return nil, err
	}
	// T100 (owner decision 2026-11-21, option A): backfill one per-owner
	// reference for every stored artifact that still carries a first uploader,
	// so an existing database keeps working across the upgrade.
	if err := engine.migrateArtifactOwnerReferences(); err != nil {
		return nil, err
	}
	if err := engine.loadReputation(); err != nil {
		return nil, err
	}
	// The F8 upload scan configuration is durable; a missing record keeps the
	// documented defaults.
	if err := engine.loadUploadScanConfig(); err != nil {
		return nil, err
	}
	// W10C row 3: the per-pool Director settings are durable too.
	if err := engine.loadPoolSettings(); err != nil {
		return nil, err
	}
	// R16-FIX (F01): the admin caps record is durable in the same store.
	if err := engine.loadCapsSettings(); err != nil {
		return nil, err
	}
	// WO R17-RECOMPUTE (G-R16-4) row 2: the per-pool recompute mode is durable
	// too. A missing record keeps the default (off).
	if err := engine.loadRecomputeSettings(); err != nil {
		return nil, err
	}
	return engine, nil
}

// SetRecomputeRegistry sets the generic reference-recompute registry of the
// platform. The wiring that lives outside internal/pool calls it: the platform
// itself never imports workload code (CR-53).
func (e *Engine) SetRecomputeRegistry(reg *recompute.Registry) {
	e.recomputeRegistryMu.Lock()
	e.recomputeRegistry = reg
	e.recomputeRegistryMu.Unlock()
}

// RecomputeRegistry returns the registry of the platform, or nil when no
// reference was registered. An engine without a registry never recomputes.
func (e *Engine) RecomputeRegistry() *recompute.Registry {
	e.recomputeRegistryMu.RLock()
	defer e.recomputeRegistryMu.RUnlock()
	return e.recomputeRegistry
}

type GPUConfig struct {
	Enabled          bool `json:"gpuEnabled"`
	MaxSlotsPerTab   int  `json:"maxGpuSlotsPerTab"`
	OnBatteryAllowed bool `json:"gpuOnBatteryAllowed"`
}

func DefaultGPUConfig() GPUConfig {
	return GPUConfig{Enabled: true, MaxSlotsPerTab: 2}
}

func (e *Engine) SetGPUConfig(c GPUConfig) error {
	if c.MaxSlotsPerTab < 1 || c.MaxSlotsPerTab > 4 {
		return ErrInvalid
	}
	e.mu.Lock()
	e.gpu = c
	e.mu.Unlock()
	return nil
}

func (e *Engine) GPUConfig() GPUConfig {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.gpu
}

// SetLeaseClient is the MVPW-D smallest hook (one call from main.go).
// When lease_store=redis the client provides SET-NX / GET+PEXPIRE fencing for
// issue/renew/expire. Settle + result uniqueness stay in the PG store.
func (e *Engine) SetLeaseClient(mode string, rc *redislease.Client) {
	e.leaseStore = mode
	e.redis = rc
}

// SetWorkerPool sets the number of slots of the bounded worker pool that runs the CPU
// heavy platform work (R15 AT22-GM2-3, TD-17). The pool is a semaphore: at most n tasks
// run at the same time, so the number of busy threads never exceeds the cap. A value of
// zero leaves the work unbounded, which only happens when the caller never sets the
// knob (the server always sets it).
func (e *Engine) SetWorkerPool(n int) {
	e.workerPoolMu.Lock()
	defer e.workerPoolMu.Unlock()
	e.workerSem = caps.NewSemaphore(n)
}

// WorkerPoolSize returns the number of slots of the bounded worker pool (0 = the pool
// was never set).
func (e *Engine) WorkerPoolSize() int {
	e.workerPoolMu.RLock()
	defer e.workerPoolMu.RUnlock()
	return e.workerSem.Cap()
}

// workerPool returns the bounded worker pool of the engine.
func (e *Engine) workerPool() *caps.Semaphore {
	e.workerPoolMu.RLock()
	defer e.workerPoolMu.RUnlock()
	return e.workerSem
}

func (e *Engine) cleanupRedisLeases(refs []redisLeaseRef) {
	for _, ref := range refs {
		if err := e.redis.CompareAndDelete(ref.key, ref.token); err != nil {
			e.Log.Warn("lease.redis.cleanup", "key", ref.key, "error", err)
		}
	}
}

// eventSequenceName is the durable name of the event sequence: the PostgreSQL SEQUENCE
// and the SQLite counter row share it.
const eventSequenceName = "pool_event_seq"

// nextEventSequence hands out the next durable event sequence number. R17-A2 (WO
// R17-SCALE item 3): PostgreSQL answers from a SEQUENCE, so two event writers never
// serialize on one row; the SQLite debug store, its single writer, keeps the
// read-increment-write counter of one row. A sequence number is consumed even when the
// transaction rolls back, so the ids may carry gaps; the SSE route and the gRPC stream
// order by the id and tolerate a gap.
//
// A wrapping transaction always satisfies store.SequenceTx (it forwards the optional
// contract), so an unsupported delegate answers ErrNotImplemented and the counter below
// is used instead.
func nextEventSequence(tx store.Tx, name string) (uint64, error) {
	if sequence, ok := tx.(store.SequenceTx); ok {
		seq, err := sequence.NextSequence(name)
		if !errors.Is(err, store.ErrNotImplemented) {
			return seq, err
		}
	}
	var seq uint64
	err := tx.Get(store.Metrics, name, &seq)
	if err == nil {
		// The counter row already exists; keep counting from it.
	} else if errors.Is(err, store.ErrNotFound) {
		// R18B review rows 6/17: the counter row was renamed from "event_sequence"
		// to "pool_event_seq" without a migration. An upgraded store has no row under
		// the new name, and a counter restarting at 1 would overwrite an existing
		// event record and sort every new event before the stored ones. Seed the new
		// counter from the maximum stored event id instead of starting over.
		seq, err = maxStoredEventID(tx)
		if err != nil {
			return 0, err
		}
	} else {
		return 0, err
	}
	seq++
	if err := tx.Put(store.Metrics, name, seq); err != nil {
		return 0, err
	}
	return seq, nil
}

// maxStoredEventID reads the largest numeric event id of the Events bucket. The ids are
// zero-padded 20-digit sequence values, so the lexicographic maximum equals the numeric
// one; a non-numeric key is skipped.
func maxStoredEventID(tx store.Tx) (uint64, error) {
	rows, err := tx.List(store.Events)
	if err != nil {
		return 0, err
	}
	var max uint64
	for _, row := range rows {
		id, err := strconv.ParseUint(row.Key, 10, 64)
		if err != nil {
			continue
		}
		if id > max {
			max = id
		}
	}
	return max, nil
}

// putEvent allocates the next durable event sequence number and stores the
// event. The zero padded id sorts lexicographically in sequence order, which
// is what the SSE and gRPC replay walk rely on (T108).
func (e *Engine) putEvent(tx store.Tx, v *Event) error {
	seq, err := nextEventSequence(tx, eventSequenceName)
	if err != nil {
		return err
	}
	v.ID = fmt.Sprintf("%020d", seq)
	return tx.Put(store.Events, v.ID, *v)
}

func (e *Engine) event(tx store.Tx, j Job, kind string, r *Result) error {
	v := Event{Type: kind, JobID: j.JobID, At: e.Now().UTC(), Owner: j.Owner, PoolID: j.PoolID, Result: r}
	if r != nil {
		v.TaskID = r.TaskID
	}
	return e.putEvent(tx, &v)
}
func (e *Engine) Validate(q CreateRequest) (CreateRequest, []Warning, error) {
	if len(q.ModuleArtifactIDs) == 0 && q.ModuleSHA256 != "" {
		q.ModuleArtifactIDs = []string{q.ModuleSHA256}
	}
	// R11-G2: a job names at most verify.MaxWorkloadFiles module artifacts, the
	// same cap the signed envelope carries (entry plus companions).
	if len(q.ModuleArtifactIDs) == 0 || len(q.ModuleArtifactIDs) > verify.MaxWorkloadFiles ||
		(len(q.Tasks) == 0) == (q.Split == nil) || len(q.Tasks) > 10000 {
		return q, nil, ErrInvalid
	}
	if err := validateSplitBatch(q); err != nil {
		return q, nil, err
	}
	if q.Needs == "" {
		q.Needs = "cpu"
	}
	if !slices.Contains([]string{"cpu", "gpu", "gpu-preferred"}, q.Needs) {
		return q, nil, ErrInvalid
	}
	if q.Policy == "" {
		q.Policy = "partial"
	}
	if !slices.Contains([]string{"partial", "fail-fast", "threshold"}, q.Policy) || q.Threshold < 0 || q.Threshold > 1 {
		return q, nil, ErrInvalid
	}
	if q.MaxAttempts == 0 {
		q.MaxAttempts = 3
	}
	if q.MaxAttempts < 1 || q.MaxAttempts > 10 {
		return q, nil, ErrInvalid
	}
	if q.Priority == "" {
		q.Priority = "normal"
	}
	if !slices.Contains([]string{"low", "normal", "high"}, q.Priority) {
		return q, nil, ErrUnsupported
	}
	if q.Priority == "high" && !e.QuotaConfig().HighPriority {
		return q, nil, ErrUnsupported
	}
	if q.Webhook != nil && q.Webhook.Validate(e.WebhookAllowPrivateIPs) != nil {
		return q, nil, ErrInvalid
	}
	if err := e.validatePlacement(q); err != nil {
		return q, nil, err
	}
	if err := e.applyOptions(&q); err != nil {
		return q, nil, err
	}
	if q.RerunPercent != nil && (*q.RerunPercent < 0 || *q.RerunPercent > 100) {
		return q, nil, ErrInvalid
	}
	seen := map[string]bool{}
	for _, t := range q.Tasks {
		if t.TaskID == "" || seen[t.TaskID] || len(t.Input) >= 256*1024 || t.TimeoutMS < 0 {
			return q, nil, ErrInvalid
		}
		seen[t.TaskID] = true
	}
	w := []Warning{}
	return q, w, nil
}
func (e *Engine) create(tx store.Tx, p, owner, key, session string, q CreateRequest) (Job, error) {
	requested := q
	q, w, err := e.Validate(q)
	if err != nil {
		return Job{}, err
	}
	rows, err := tx.List(store.Jobs)
	if err != nil {
		return Job{}, err
	}
	jobs := make([]Job, 0, len(rows))
	for _, row := range rows {
		var existing Job
		if err = json.Unmarshal(row.Value, &existing); err != nil {
			return Job{}, err
		}
		jobs = append(jobs, existing)
	}
	b := policyRequestBytes(requested)
	digest := verify.SHA256(b)
	idem := p + ":" + owner + ":" + session + ":" + key
	if key != "" {
		var prior struct{ Hash, JobID string }
		if err = tx.Get(store.Idempotency, idem, &prior); err == nil {
			if prior.Hash != digest {
				return Job{}, ErrConflict
			}
			var j Job
			err = tx.Get(store.Jobs, prior.JobID, &j)
			return j, err
		} else if !errors.Is(err, store.ErrNotFound) {
			return Job{}, err
		}
	}
	q, configVersion, chosenPolicy, err := e.resolvePolicy(tx, p, owner, requested)
	if err != nil {
		return Job{}, err
	}
	q, w, err = e.Validate(q)
	if err != nil {
		return Job{}, err
	}
	if q.AppID != "" {
		var appErr error
		switch p {
		case Process:
			appErr = e.approvedProcessAppTx(tx, p, owner, q.AppID)
		case Thread:
			appErr = e.approvedThreadAppTx(tx, owner, q.AppID)
		default:
			appErr = ErrInvalid
		}
		if appErr != nil {
			return Job{}, appErr
		}
	}
	// D-W10-10: read the entry ABI kind here, where the artifacts are already in
	// hand. The claim path runs inside its own transaction and must not open a
	// second store read (single-connection store), so the kind travels on the Job.
	workloadKind := ""
	for _, id := range q.ModuleArtifactIDs {
		var a Artifact
		if err = tx.Get(store.Artifacts, id, &a); err != nil {
			return Job{}, err
		}
		if !a.Complete {
			return Job{}, ErrConflict
		}
		// The F8 review gate: a module that awaits admin review or that the
		// upload scan rejected may not run as a job module (WWPP-R36.1).
		if err = e.moduleApproved(a); err != nil {
			return Job{}, err
		}
		if strings.EqualFold(a.ContentType, "application/wasm") {
			workloadKind = verify.WorkloadKindWasmABI
		}
	}
	tasks, err := e.expandTasks(tx, q)
	if err != nil {
		return Job{}, err
	}
	if err = e.checkQuotaRecords(owner, jobs, len(tasks), e.Now()); err != nil {
		return Job{}, err
	}
	j := Job{AppID: q.AppID, JobID: e.ServerID + "." + p + "." + auth.ID(), ServerID: e.ServerID, PoolID: p, Owner: owner, State: "queued", CreatedAt: e.Now().UTC(), Needs: q.Needs, Policy: q.Policy, MaxAttempts: q.MaxAttempts, Priority: q.Priority, Warnings: w, Request: q, SessionID: session, ConfigVersion: configVersion, ChosenPolicy: chosenPolicy, Results: []Result{}, ChainMode: q.chainMode, WorkloadKind: workloadKind}
	if err = e.pinPolicy(tx, j); err != nil {
		return Job{}, err
	}
	for _, t := range tasks {
		j.Tasks = append(j.Tasks, Task{TaskSpec: t, State: "queued"})
	}
	j.Progress = progress(j)
	if err = tx.Put(store.Jobs, j.JobID, j); err != nil {
		return j, err
	}
	// T108: the queued event opens the observable lifecycle of the job. It is
	// written inside the create transaction, so a watcher never sees the job
	// before its queued event. The idempotent replay returns above and never
	// re-emits it.
	if err = e.event(tx, j, "job.queued", nil); err != nil {
		return j, err
	}
	if key != "" {
		err = tx.Put(store.Idempotency, idem, struct{ Hash, JobID string }{digest, j.JobID})
	}
	if err == nil {
		e.scheduler.accepted.Add(1)
	}
	return j, err
}
func (e *Engine) Create(ctx context.Context, p, owner, key string, q CreateRequest) (Job, error) {
	var j Job
	err := e.Store.Update(ctx, func(tx store.Tx) error { var er error; j, er = e.create(tx, p, owner, key, "", q); return er })
	if err == nil {
		j = e.refreshRunAudit(ctx, j)
	}
	return j, err
}
func (e *Engine) Get(ctx context.Context, p, owner, id string) (Job, error) {
	var j Job
	err := e.Store.View(ctx, func(r store.Reader) error {
		if er := r.Get(store.Jobs, id, &j); er != nil {
			return er
		}
		if j.PoolID != p || j.Owner != owner {
			return ErrForbidden
		}
		return nil
	})
	if err == nil {
		j = e.refreshRunAudit(ctx, j)
	}
	return j, err
}
func terminal(j Job) bool {
	return slices.Contains([]string{"completed", "failed", "partial", "cancelled"}, j.State)
}
func progress(j Job) Progress {
	p := Progress{Total: len(j.Tasks)}
	for _, t := range j.Tasks {
		switch t.State {
		case "done":
			p.Completed++
		case "failed", "cancelled":
			p.Failed++
		case "queued", StatePendingVerification:
			p.Queued++
		case "leased":
			p.Running++
		}
	}
	return p
}
func (e *Engine) save(tx store.Tx, j *Job, metered ...*[]meteredRuntime) error {
	was := terminal(*j)
	j.Progress = progress(*j)
	if !was {
		p := j.Progress
		fail := p.Failed > 0 && (j.Policy == "fail-fast" || (j.Policy == "threshold" && float64(p.Failed)/float64(p.Total) > j.Request.Threshold))
		if fail {
			j.State = "failed"
			for i := range j.Tasks {
				t := &j.Tasks[i]
				if t.State == "queued" || t.State == "leased" || t.State == StatePendingVerification {
					t.State = "cancelled"
					t.Redundant = nil
					if t.Lease != nil && len(metered) > 0 {
						if err := e.meterLeaseRuntime(tx, *t.Lease, metered[0]); err != nil {
							return err
						}
					}
					t.Lease = nil
				}
			}
			j.Progress = progress(*j)
		} else if p.Completed+p.Failed == p.Total {
			j.State = "completed"
			if p.Failed > 0 {
				j.State = "partial"
			}
		} else if p.Running > 0 || p.Completed > 0 || p.Failed > 0 {
			j.State = "running"
		}
		if terminal(*j) {
			now := e.Now().UTC()
			j.FinishedAt = &now
			if err := e.event(tx, *j, "job.done", nil); err != nil {
				return err
			}
		}
	}
	if err := e.chainSync(tx, j); err != nil {
		return err
	}
	return tx.Put(store.Jobs, j.JobID, j)
}
func (e *Engine) fail(tx store.Tx, j *Job, t *Task, reason string, observations *[]reputationObservation, released *[]redisLeaseRef) error {
	if released != nil && e.redis != nil && t.Lease != nil {
		*released = append(*released, redisLeaseRef{key: "lease:" + j.JobID + ":" + t.Lease.TaskID, token: t.Lease.Token})
	}
	t.Lease = nil
	// The receipt belonged to the lease that just ended. A later claim issues
	// a new one. Clearing it here is what lets the task be re-assigned after
	// the Dew window (G-F12-5); the hold itself is the continue above.
	t.Dew = nil
	if reason == "lease expired" && t.CanaryTaskID != "" {
		t.CanaryID = ""
		t.CanaryTaskID = ""
	}
	// W10C row 1: a lost or failing replica does not doom a task that already
	// holds a result. It keeps waiting for another distinct replica until the
	// verification deadline decides.
	if t.Redundant != nil {
		t.State = StatePendingVerification
		return nil
	}
	// R10 glm: the configured ceiling alone decides exhaustion (maxAttempts 4..10 live).
	if t.Attempts >= j.MaxAttempts {
		if t.Pending != nil {
			first := t.Pending
			t.Pending = nil
			t.State = "done"
			reason := "re-run attempts exhausted, first result accepted without comparison"
			r := Result{JobID: j.JobID, TaskID: t.TaskID, Status: "done", Attempts: first.Attempts, Output: first.Output, SHA256: first.SHA256, At: first.At, WorkerID: first.WorkerID, HashOK: true, Rerun: true, RerunOK: false, Flagged: true, FlagReason: reason}
			j.Results = append(j.Results, r)
			e.scheduler.completed.Add(1)
			if er := e.recordLeaseSettled(tx, *j, nil); er != nil {
				return er
			}
			if observations != nil {
				*observations = append(*observations, reputationObservation{first.WorkerID, verify.EventInconclusive, t.TaskID, reason})
			}
			return e.finishAccepted(tx, j, &r, nil)
		}
		t.State = "failed"
		t.Quarantined = reason != "lease expired"
		r := Result{JobID: j.JobID, TaskID: t.TaskID, Status: "failed", Attempts: t.Attempts, At: e.Now().UTC(), Error: reason}
		j.Results = append(j.Results, r)
		return e.event(tx, *j, "task.failed", &r)
	}

	t.State = "queued"
	return nil
}

type meteredRuntime struct {
	gpu     bool
	seconds int64
}

func (e *Engine) meterLeaseRuntime(r store.Reader, lease Lease, metered *[]meteredRuntime) error {
	if lease.StartedAt.IsZero() {
		return nil
	}
	var worker Worker
	if err := r.Get(store.Workers, lease.WorkerID, &worker); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	seconds := int64(e.Now().Sub(lease.StartedAt) / time.Second)
	if seconds > 0 {
		*metered = append(*metered, meteredRuntime{gpu: worker.GPU, seconds: seconds})
	}
	return nil
}

func (e *Engine) applyMeteredRuntime(metered []meteredRuntime) {
	for _, runtime := range metered {
		e.AddMeteredSeconds(runtime.gpu, runtime.seconds)
	}
}

type taskKeyRecord struct {
	KeyID      string                `json:"keyId"`
	PrivateKey []byte                `json:"privateKey"`
	Keyring    []verify.KeyringEntry `json:"keyring,omitempty"`
	// Retired is the set of key ids that were current before an admin rotation
	// (W9c row 1), persisted so the stale-runner refund rule survives a restart.
	Retired []string `json:"retired,omitempty"`
}

const taskKeyStoreKey = "task_signing_key"
const rerunSecretStoreKey = "rerun_sampling_secret"

func loadOrCreateRerunSecret(s store.Store) ([]byte, error) {
	var secret []byte
	err := s.Update(context.Background(), func(tx store.Tx) error {
		if err := tx.Get(store.Metrics, rerunSecretStoreKey, &secret); err == nil {
			return nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		candidate := make([]byte, 32)
		if _, err := rand.Read(candidate); err != nil {
			return err
		}
		var putErr error
		if atomic, ok := tx.(store.PutIfAbsentTx); ok {
			_, putErr = atomic.PutIfAbsent(store.Metrics, rerunSecretStoreKey, candidate)
		} else {
			putErr = tx.Put(store.Metrics, rerunSecretStoreKey, candidate)
		}
		if putErr != nil {
			return putErr
		}
		return tx.Get(store.Metrics, rerunSecretStoreKey, &secret)
	})
	if err != nil {
		return nil, err
	}
	if len(secret) < 32 {
		return nil, fmt.Errorf("%w: invalid persisted re-run sampling secret", ErrInvalid)
	}
	return append([]byte(nil), secret...), nil
}

func loadOrCreateTaskKey(s store.Store) (ed25519.PrivateKey, string, []verify.KeyringEntry, []string, error) {
	var record taskKeyRecord
	err := s.Update(context.Background(), func(tx store.Tx) error {
		if err := tx.Get(store.Metrics, taskKeyStoreKey, &record); err == nil {
			return nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		keyID := verify.KeyID(key.Public().(ed25519.PublicKey))
		candidate := taskKeyRecord{
			KeyID: keyID, PrivateKey: append([]byte(nil), key...),
			Keyring: []verify.KeyringEntry{{KeyID: keyID, PublicKey: append([]byte(nil), key.Public().(ed25519.PublicKey)...)}},
		}
		if atomic, ok := tx.(store.PutIfAbsentTx); ok {
			_, err = atomic.PutIfAbsent(store.Metrics, taskKeyStoreKey, candidate)
		} else {
			err = tx.Put(store.Metrics, taskKeyStoreKey, candidate)
		}
		if err != nil {
			return err
		}
		return tx.Get(store.Metrics, taskKeyStoreKey, &record)
	})
	if err != nil {
		return nil, "", nil, nil, err
	}
	if len(record.PrivateKey) != ed25519.PrivateKeySize || record.KeyID == "" {
		return nil, "", nil, nil, fmt.Errorf("%w: invalid persisted task signing key", ErrInvalid)
	}
	key := ed25519.PrivateKey(append([]byte(nil), record.PrivateKey...))
	if verify.KeyID(key.Public().(ed25519.PublicKey)) != record.KeyID {
		return nil, "", nil, nil, fmt.Errorf("%w: persisted task signing key id mismatch", ErrInvalid)
	}
	if len(record.Keyring) == 0 {
		record.Keyring = []verify.KeyringEntry{{KeyID: record.KeyID, PublicKey: append([]byte(nil), key.Public().(ed25519.PublicKey)...)}}
	}
	return key, record.KeyID, record.Keyring, record.Retired, nil
}

// failWith ends or requeues a task with a prepared result record. The terminal flag
// marks a conclusive verdict (a re-run mismatch): the task is flagged and not
// retried, because a retry would only repeat the same evidence.
func (e *Engine) failWith(tx store.Tx, j *Job, t *Task, r *Result, terminal bool, released *[]redisLeaseRef) error {
	if released != nil && e.redis != nil && t.Lease != nil {
		*released = append(*released, redisLeaseRef{key: "lease:" + j.JobID + ":" + t.Lease.TaskID, token: t.Lease.Token})
	}
	t.Lease = nil
	// R10 glm: same rule as fail(), the configured ceiling alone decides.
	if terminal || t.Attempts >= j.MaxAttempts {
		t.State = "failed"
		t.Quarantined = true
		j.Results = append(j.Results, *r)
		return e.event(tx, *j, "task.failed", r)
	}
	t.State = "queued"
	return nil
}

// errJobChanged is the sentinel of the claim and sweep walks: the job row was
// written by a concurrent transaction after the lock-free List read, so the
// stale snapshot must not be written back. The walker skips the job entirely
// and the next claim or sweep sees the concurrent state (R18B review row 3).
var errJobChanged = errors.New("job changed concurrently")

// recheckJob re-reads one job row inside the write transaction. The PostgreSQL
// adapter locks the single row with FOR UPDATE, so when the row still equals
// the snapshot the caller owns it until the transaction ends and every
// whole-job write is fenced against a concurrent submit. R18B review row 3:
// List takes no row lock (a FOR UPDATE over the bucket serialized every claim
// behind one table scan), so a walk that read the row through List must
// re-check before it mutates or writes.
func (e *Engine) recheckJob(tx store.Tx, snapshot Job) (bool, error) {
	var fresh Job
	if err := tx.Get(store.Jobs, snapshot.JobID, &fresh); err != nil {
		return false, err
	}
	return reflect.DeepEqual(fresh, snapshot), nil
}

func (e *Engine) expire(tx store.Tx, j *Job, snapshot Job, owned map[string]bool, observations *[]reputationObservation, metered *[]meteredRuntime, released *[]redisLeaseRef) error {
	if terminal(*j) {
		return nil
	}
	// A3a (WO R17): a walk that changes nothing must not rewrite the job. Every
	// Claim and every Sweep visits every job of the pool, and the whole-Job JSON
	// write was unconditional, so a pool with no expiries paid a job rewrite per
	// claim (the R16 A3 whole-Job rewrite cost) for no state change at all.
	changed := false
	// ensureOwned fences the first mutation of this job against a concurrent
	// writer: the row is re-read with the write lock and compared with the
	// snapshot the caller read from List. A concurrent change skips the whole
	// job, so no event, metric or result of this stale snapshot ever commits.
	ensureOwned := func() error {
		if owned[j.JobID] {
			return nil
		}
		ok, err := e.recheckJob(tx, snapshot)
		if err != nil {
			return err
		}
		if !ok {
			return errJobChanged
		}
		owned[j.JobID] = true
		return nil
	}
	for i := range j.Tasks {
		t := &j.Tasks[i]
		// W10C row 1: a redundant verification that never reached quorum ends at
		// its deadline; the first result is accepted and flagged.
		if t.Redundant != nil && !e.Now().Before(t.Redundant.Deadline) {
			if err := ensureOwned(); err != nil {
				return err
			}
			if t.Lease != nil {
				if err := e.meterLeaseRuntime(tx, *t.Lease, metered); err != nil {
					return err
				}
			}
			if err := e.expireRedundant(tx, j, t, observations, released); err != nil {
				return err
			}
			changed = true
			continue
		}
		if t.Lease != nil && !t.Lease.ExpiresAt.After(e.Now()) {
			// G-F12-5: a missed heartbeat does not re-assign a task while its
			// Dew receipt is still inside the window and the run has not ended.
			if t.Dew != nil && t.Dew.Deadline.After(e.Now()) && !dewRunEnded(j) {
				continue
			}
			if err := ensureOwned(); err != nil {
				return err
			}
			if err := e.meterLeaseRuntime(tx, *t.Lease, metered); err != nil {
				return err
			}
			e.Log.Info("lease.expired", "jobId", j.JobID, "taskId", t.TaskID, "generation", t.Generation)
			if err := e.snapshotExpiredLease(tx, j, t); err != nil {
				return err
			}
			if er := e.recordLeaseExpired(tx, *j); er != nil {
				return er
			}
			if er := e.event(tx, *j, "worker.lost", nil); er != nil {
				return er
			}
			// A lost worker is a timeout event of the reputation (R39.1: a genuine
			// timeout lowers the score, a transport error alone does not).
			*observations = append(*observations, reputationObservation{t.Lease.WorkerID, verify.EventTimeout, t.TaskID, "lease expired"})
			if er := e.recordReputationOutcome(tx, *j, verify.EventTimeout); er != nil {
				return er
			}
			if er := e.fail(tx, j, t, "lease expired", observations, released); er != nil {
				return er
			}
			changed = true
			continue
		}
		// A held re-run result must not wait forever: when no second worker picked the
		// task up in time, the first result is accepted (WWPP-R35.1).
		if t.Pending != nil && t.State == "queued" && !e.Now().Before(t.Pending.At.Add(e.reRunWindow())) {
			if err := ensureOwned(); err != nil {
				return err
			}
			first := t.Pending
			t.Pending = nil
			reason := "re-run window expired, first result accepted without comparison"
			r := Result{JobID: j.JobID, TaskID: t.TaskID, Status: "done", Attempts: first.Attempts, Output: first.Output, SHA256: first.SHA256, At: first.At, WorkerID: first.WorkerID, HashOK: true, Rerun: true, RerunOK: false, Flagged: true, FlagReason: reason}
			j.Results = append(j.Results, r)
			t.State = "done"
			t.Lease = nil
			*observations = append(*observations, reputationObservation{first.WorkerID, verify.EventInconclusive, t.TaskID, reason})
			if er := e.recordLeaseSettled(tx, *j, nil); er != nil {
				return er
			}
			if er := e.finishAccepted(tx, j, &r, nil); er != nil {
				return er
			}
			e.Log.Info("rerun.timeout", "jobId", j.JobID, "taskId", t.TaskID, "workerId", first.WorkerID)
			changed = true
		}
		// G-R17-1 (WO R18): a deferred recompute must not wait forever. When its
		// window expired (or the engine restarted and no retry goroutine exists),
		// the held result is accepted without comparison and logged - the same
		// skip fallback the bounded retries end in.
		if t.DeferredRecompute != nil && !e.Now().Before(t.DeferredRecompute.At.Add(e.reRunWindow())) {
			if err := e.expireDeferredRecompute(tx, j, t, observations); err != nil {
				return err
			}
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return e.save(tx, j, metered)
}
func (e *Engine) Cancel(ctx context.Context, p, owner, id string) (Job, bool, error) {
	var j Job
	alreadyTerminal := false
	var metered []meteredRuntime
	err := e.Store.Update(ctx, func(tx store.Tx) error {
		if er := tx.Get(store.Jobs, id, &j); er != nil {
			return er
		}
		if j.Owner != owner || j.PoolID != p {
			return ErrForbidden
		}
		if terminal(j) {
			alreadyTerminal = true
			return nil
		}
		j.State = "cancelled"
		now := e.Now().UTC()
		j.FinishedAt = &now
		for i := range j.Tasks {
			t := &j.Tasks[i]
			if t.State == "queued" || t.State == "leased" || t.State == StatePendingVerification {
				t.State = "cancelled"
				t.Redundant = nil
				if t.Lease != nil {
					if er := e.meterLeaseRuntime(tx, *t.Lease, &metered); er != nil {
						return er
					}
				}
				t.Lease = nil
			}
		}
		if er := e.event(tx, j, "job.done", nil); er != nil {
			return er
		}
		return e.save(tx, &j, &metered)
	})
	if err == nil {
		e.applyMeteredRuntime(metered)
	}
	return j, alreadyTerminal, err
}
func (e *Engine) Claim(ctx context.Context, p, owner, worker string) (Assignment, error) {
	var out Assignment
	found := false
	var observations []reputationObservation
	var metered []meteredRuntime
	var acquired []redisLeaseRef
	var released []redisLeaseRef
	repChanged := false
	err := e.Store.Update(ctx, func(tx store.Tx) error {
		if er := e.expireSessions(tx, &metered); er != nil {
			return er
		}
		var w Worker
		if er := tx.Get(store.Workers, worker, &w); er != nil {
			return er
		}
		if w.Owner != owner || w.PoolID != p {
			return ErrForbidden
		}
		ok, st, changed := e.eligibleWithRecovery(worker)
		repChanged = repChanged || changed
		if !ok {
			return fmt.Errorf("%w: worker reputation %s", ErrForbidden, st)
		}
		if e.Now().Sub(w.Heartbeat) > 2*e.TTL {
			return ErrConflict
		}
		if w.RunnerStale {
			return ErrRunnerStale
		}
		physical, er := e.lockPhysicalAppTab(tx, w)
		if er != nil {
			return er
		}
		rows, er := tx.List(store.Jobs)
		if er != nil {
			return er
		}
		if er = e.refreshPhysicalAppTab(tx, &physical, &w, rows); er != nil {
			return er
		}
		jobs := make([]Job, 0, len(rows))
		snapshots := make(map[string]Job, len(rows))
		owned := make(map[string]bool, len(rows))
		activeCPU, activeGPU, activeVerification := 0, 0, 0
		for _, r := range rows {
			var j Job
			if er = json.Unmarshal(r.Value, &j); er != nil {
				return er
			}
			snapshot := j
			if er = e.expire(tx, &j, snapshot, owned, &observations, &metered, &released); er != nil {
				if errors.Is(er, errJobChanged) {
					// A concurrent transaction changed the job after the lock-free
					// List read; this walk never writes the stale snapshot (R18B
					// review row 3).
					continue
				}
				return er
			}
			snapshots[j.JobID] = snapshot
			_, verification := activeLeaseCounts(j, worker)
			for _, t := range j.Tasks {
				if t.Lease == nil || t.Lease.WorkerID != worker {
					continue
				}
				if t.CanaryID != "" {
					continue
				}
				// W9b row 3: a held re-run is a real lease of the tab. A GPU re-run
				// takes a GPU slot; a CPU re-run stays in the reserved verification
				// lane counted above (activeLeaseCounts).
				if t.Lease.GPU {
					activeGPU++
				} else if t.Pending == nil {
					activeCPU++
				}
			}
			activeVerification += verification
			jobs = append(jobs, j)
		}
		gpuCfg := e.GPUConfig()
		gpuSlots := w.GPUSlots
		if gpuSlots == 0 && w.GPU {
			gpuSlots = 1
		}
		if gpuSlots > gpuCfg.MaxSlotsPerTab {
			gpuSlots = gpuCfg.MaxSlotsPerTab
		}
		if activeCPU >= w.Slots && activeGPU >= gpuSlots && activeVerification >= reservedVerificationSlots {
			return nil
		}
		// Reputation gate: a barred or a banned worker gets no lease (WWPP-R37.1).
		gateOK, gateStatus, gateChanged := e.eligibleWithRecovery(worker)
		repChanged = repChanged || gateChanged
		if !gateOK {
			e.Log.Warn("worker.notEligible", "workerId", worker, "reputation", string(gateStatus))
			return ErrForbidden
		}
		e.scheduleForClaim(jobs)
		for n := range jobs {
			j := &jobs[n]
			if j.PoolID != p || terminal(*j) || j.SessionID != w.SessionID {
				continue
			}
			if !physical.permits(j) {
				continue
			}
			if j.AppID != "" {
				policy, err := appegress.ReadTx(tx, p, j.Owner, j.AppID)
				if err != nil {
					return err
				}
				if len(policy.Endpoints) > 0 && (!appEgressConsented(w, policy) || !e.egressReady.Load()) {
					continue
				}
			}
			leaseGPU := false
			switch j.Needs {
			case "gpu":
				leaseGPU = gpuCfg.Enabled && w.GPU && gpuSlots > activeGPU && gpuAllowedOnWorker(w, gpuCfg)
			case "gpu-preferred":
				leaseGPU = gpuCfg.Enabled && w.GPU && gpuSlots > activeGPU && gpuAllowedOnWorker(w, gpuCfg)
			case "cpu":
				leaseGPU = false
			default:
				continue
			}
			if j.Needs == "gpu" && !leaseGPU {
				continue
			}
			verification := hasQueuedVerificationTask(e, j)
			if !claimAllowed(e.QuotaConfig(), jobs, j, verification) {
				continue
			}
			if verification {
				if activeVerification >= reservedVerificationSlots {
					continue
				}
			} else if leaseGPU && activeGPU >= gpuSlots || !leaseGPU && activeCPU >= w.Slots {
				continue
			}
			available := w.Slots - activeCPU
			if leaseGPU {
				available = gpuSlots - activeGPU
			}
			if verification {
				available = reservedVerificationSlots - activeVerification
			}
			requested := j.Request.BatchSize
			if requested < 1 {
				requested = 1
			}
			if verification {
				requested = 1
			}
			if limit := e.maxBatchSize(w, available, verification); requested > limit {
				requested = limit
			}
			// F10: an app-specific Thread lease is one independent Web Worker
			// slot, never a batch spanning multiple slots.
			if p == Thread && j.AppID != "" {
				requested = 1
			}
			if requested < 1 {
				continue
			}
			originalBatchSize := j.Request.BatchSize
			j.Request.BatchSize = requested
			batchStart := len(acquired)
			if !owned[j.JobID] {
				// The job row was not fenced by the expiry walk above: re-check it
				// with the write lock before the whole-job write of the lease, so a
				// concurrent submit can never be clobbered (R18B review row 3).
				ok, err := e.recheckJob(tx, snapshots[j.JobID])
				if err != nil {
					return err
				}
				if !ok {
					j.Request.BatchSize = originalBatchSize
					continue
				}
				owned[j.JobID] = true
			}
			if p == Process && j.AppID != "" {
				if bindErr := e.bindProcessAppTx(tx, j, &w); bindErr != nil {
					if errors.Is(bindErr, ErrProcessAppFull) || errors.Is(bindErr, ErrProcessAppBound) {
						j.Request.BatchSize = originalBatchSize
						continue
					}
					return bindErr
				}
			}
			threadSlotID := ""
			if p == Thread && j.AppID != "" {
				var bindErr error
				threadSlotID, bindErr = e.bindThreadAppTx(tx, j, w, jobs, physical.busy)
				if bindErr != nil {
					if errors.Is(bindErr, ErrThreadAppFull) || errors.Is(bindErr, ErrThreadAppBound) {
						j.Request.BatchSize = originalBatchSize
						continue
					}
					return bindErr
				}
			}
			out, found, er = e.claimBatch(tx, j, w, leaseGPU, &acquired)
			j.Request.BatchSize = originalBatchSize
			if errors.Is(er, ErrLeaseContended) {
				// GLM R7-2: earlier members of this batch already hold Redis keys, but their
				// store leases are dropped with this job; release them now or they block
				// re-claims until the PX TTL ends.
				if e.redis != nil {
					e.cleanupRedisLeases(acquired[batchStart:])
				}
				acquired = acquired[:batchStart]
				if j.AppID != "" {
					return ErrEmpty
				}
				continue
			}
			if er != nil {
				return er
			}
			if !found && j.AppID != "" {
				// The process-app policy was tentatively modified, but no
				// assignment was issued; abort its transaction rather than
				// consume a tab slot with no work.
				return ErrEmpty
			}
			if found {
				if j.AppID != "" {
					if er := e.bindPhysicalAppTab(tx, &physical, &w, j, threadSlotID); er != nil {
						return er
					}
				}
				if p == Thread && j.AppID != "" {
					out.Lease.WorkerSlotID = threadSlotID
					for ti := range j.Tasks {
						l := j.Tasks[ti].Lease
						if l != nil && l.Token == out.Lease.Token && l.WorkerID == worker {
							l.WorkerSlotID = threadSlotID
							break
						}
					}
				}
				n := int64(1)
				if len(out.Batch) > 0 {
					n = int64(len(out.Batch))
				}
				e.scheduler.leased.Add(n)
				if er := e.recordLeaseIssued(tx, *j, int(n)); er != nil {
					return er
				}
				// T108: one leased event per wire task of the batch, so a watcher
				// sees the queued -> leased -> done progression of every task. The
				// wire lease task id keeps canary substitutions opaque.
				members := out.Batch
				if len(members) == 0 {
					members = []Assignment{out}
				}
				for _, m := range members {
					ev := Event{Type: "task.leased", JobID: j.JobID, TaskID: m.Lease.TaskID, At: e.Now().UTC(), Owner: j.Owner, PoolID: j.PoolID, SessionID: j.SessionID}
					if er := e.putEvent(tx, &ev); er != nil {
						return er
					}
				}
				return e.save(tx, j, &metered)
			}
		}
		return nil
	})
	// W9 F2: a recovery, ban or reseed decided by the gate is durable even when
	// the claim itself is refused and no reputation event follows.
	if err != nil && e.redis != nil {
		e.cleanupRedisLeases(acquired)
	}
	if err == nil && e.redis != nil {
		e.cleanupRedisLeases(released)
	}
	if errors.Is(err, ErrLeaseContended) {
		err = nil
	}
	if err == nil {
		e.applyMeteredRuntime(metered)
		e.observeCommitted(observations)
	}
	// W9d row 2: the explicit persist covers the paths observeCommitted cannot:
	// a failed transaction (its observations were rolled back) and a changed
	// record with no observation at all. A successful observeCommitted already
	// persisted the tracker.
	if repChanged && (err != nil || len(observations) == 0) {
		e.persistReputation()
	}
	if err == nil && !found {
		err = ErrEmpty
	}
	return out, err
}
func (e *Engine) Submit(ctx context.Context, p, owner, worker string, q Submission) (Result, error) {
	if !validGPUResultMetrics(q.GPU) {
		return Result{}, ErrInvalid
	}
	var out Result
	var rejected error
	var observations []reputationObservation
	var metered []meteredRuntime
	var failedVerify *verifyFailureMetric
	var settledRedis []redisLeaseRef
	var deadLetter error
	var deferred *deferredRecomputeRef
	var runAudit bool
	repChanged := false
	// A1 (WO R17): the Ed25519 envelope verify, the dew receipt verify, the output
	// digest and the optional sampled recompute run before the settlement
	// transaction, inside the bounded worker pool. The transaction below keeps its own
	// classification and every cheap re-check; it only consumes a verdict that still
	// matches the task it reads.
	snapshot, snapshotErr := e.snapshotSubmit(ctx, p, owner, worker, q)
	var prep *submitPrepare
	if snapshotErr == nil && snapshot != nil {
		prep = e.prepareSubmit(snapshot, q)
	}
	err := e.Store.Update(ctx, func(tx store.Tx) error {
		if er := e.expireSessions(tx, &metered); er != nil {
			return er
		}
		var w Worker
		if er := tx.Get(store.Workers, worker, &w); er != nil {
			return er
		}
		if w.Owner != owner || w.PoolID != p {
			return ErrForbidden
		}
		ok, st, changed := e.eligibleWithRecovery(worker)
		repChanged = repChanged || changed
		if !ok {
			return fmt.Errorf("%w: worker reputation %s", ErrForbidden, st)
		}
		var j Job
		if er := tx.Get(store.Jobs, q.Lease.JobID, &j); er != nil {
			return er
		}
		if j.PoolID != p {
			return ErrForbidden
		}
		runAudit = j.ChainMode == ChainModePerRun
		var task *Task
		for i := range j.Tasks {
			if j.Tasks[i].TaskID == q.Lease.TaskID || j.Tasks[i].CanaryTaskID == q.Lease.TaskID {
				task = &j.Tasks[i]
				break
			}
		}
		if task == nil {
			return store.ErrNotFound
		}
		l := task.Lease
		late := false
		now := e.Now()
		// G-F12-4: the settled lease may already be gone. A later valid result
		// of the same receipt, inside the window and before the run ends, is
		// not a stale lease. It is recorded below and does not settle again.
		dewDuplicate := task.EnvelopeUsed && q.LeaseReceipt != nil && task.Dew != nil &&
			!dewRunEnded(&j) && !terminal(j) &&
			q.Lease.WorkerID == worker && q.Lease.WorkerID == task.Dew.WorkerID &&
			q.Lease.Generation == task.Dew.Generation &&
			verify.SHA256([]byte(q.Lease.Token)) == task.Dew.LeaseTokenSHA256 &&
			now.Before(task.Dew.Deadline)
		if !dewDuplicate && terminal(j) {
			rejected = ErrConflict
		} else if !dewDuplicate && l != nil && l.WorkerID == worker && q.Lease.WorkerID == worker && l.Generation == q.Lease.Generation && l.Token == q.Lease.Token && l.ExpiresAt.After(now) {
		} else if !dewDuplicate {
			if l != nil && !l.ExpiresAt.After(now) && task.ExpiredLease == nil {
				if er := e.snapshotExpiredLease(tx, &j, task); er != nil {
					return er
				}
				if er := e.recordLeaseExpired(tx, j); er != nil {
					return er
				}
			}
			ev := task.ExpiredLease
			if ev != nil && !ev.Consumed && task.Generation == ev.Lease.Generation &&
				ev.Lease.WorkerID == worker && q.Lease.WorkerID == worker &&
				ev.Lease.Token == q.Lease.Token && q.Lease.Generation == ev.Lease.Generation &&
				ev.ContributorKeyID == owner && (task.Lease == nil || task.Lease.Token == ev.Lease.Token) {
				l = &ev.Lease
				late = true
			} else {
				rejected = ErrConflict
			}
		}
		// The prepared verdict is only usable when the classification above reached the
		// same lease and the task did not move in the meantime.
		if !snapshot.matches(task, late) {
			prep = nil
		}
		if rejected != nil {
			rejected = ErrConflict
			e.Log.Warn("lease.rejected", "jobId", j.JobID, "taskId", task.TaskID, "generation", q.Lease.Generation)
			return tx.Put(store.DeadLetters, auth.ID(), submissionDeadLetter(q, "stale, expired or terminal lease", e.Now()))
		}
		if dewDuplicate {
			recorded, er := e.finishDewDuplicateTx(tx, &j, task, q, worker)
			if er != nil {
				if errors.Is(er, errDewRejected) || errors.Is(er, ErrConflict) {
					deadLetter = er
				}
				return er
			}
			out = recorded
			return nil
		}
		// R11 Part E: an offline or peer-relayed result has to carry the signed
		// receipt of its lease whatever the job's dew option says. The binding check
		// itself runs after the envelope block (below).
		if (q.Offline || q.PeerRelayed) && q.LeaseReceipt == nil {
			deadLetter = dewReject("missing")
			return deadLetter
		}
		cfg := e.TrustConfig()
		if cfg.EnvelopeEnabled || dewEnabled(&j) || q.Offline || q.PeerRelayed || q.LeaseReceipt != nil {
			envStart := time.Now()
			envelopeErr := error(nil)
			if prep != nil && prep.envelopeChecked {
				// The signature, the binding and the replay binding were verified
				// before the transaction; the task was not touched since.
				envelopeErr = prep.envelopeErr
				if envelopeErr == nil {
					envStart = envStart.Add(-prep.envelopeDur)
				}
			} else if late {
				envelopeErr = verify.VerifyTaskEnvelopeAllowExpired(q.TaskEnvelope, e.Verifier.Keyring(), now)
			} else {
				envelopeErr = e.verifyRenewedEnvelope(q.TaskEnvelope, *l)
			}
			if envelopeErr != nil {
				failedVerify = &verifyFailureMetric{j.PoolID, j.JobID, verify.OpEnvelope, time.Since(envStart)}
				return fmt.Errorf("%w: task envelope: %v", ErrConflict, envelopeErr)
			}
			if prep == nil || !prep.envelopeChecked {
				env, err := verify.DecodeTaskEnvelope(q.TaskEnvelope)
				expectedInput := task.Input
				if task.CanaryID != "" {
					c, canaryErr := e.Verifier.Canaries().Get(task.CanaryID)
					if canaryErr != nil {
						failedVerify = &verifyFailureMetric{j.PoolID, j.JobID, verify.OpEnvelope, time.Since(envStart)}
						return fmt.Errorf("%w: canary binding", ErrConflict)
					}
					expectedInput = c.Input
				}
				expectedTaskID := task.TaskID
				if task.CanaryTaskID != "" && q.Lease.TaskID == task.CanaryTaskID {
					expectedTaskID = task.CanaryTaskID
				}
				if err != nil || env.JobID != j.JobID || env.TaskID != expectedTaskID || env.WorkerID != worker || env.LeaseToken != l.Token || env.ArtifactSHA256 != moduleDigest(j.Request.ModuleArtifactIDs) || env.InputSHA256 != verify.SHA256(expectedInput) {
					failedVerify = &verifyFailureMetric{j.PoolID, j.JobID, verify.OpEnvelope, time.Since(envStart)}
					return fmt.Errorf("%w: task envelope binding", ErrConflict)
				}
			}
			digest := verify.SHA256(append(append([]byte(nil), q.TaskEnvelope.Payload...), q.TaskEnvelope.Signature...))
			if task.EnvelopeDigest == "" || task.EnvelopeDigest != digest || task.EnvelopeUsed {
				failedVerify = &verifyFailureMetric{j.PoolID, j.JobID, verify.OpEnvelope, time.Since(envStart)}
				return fmt.Errorf("%w: task envelope replay", ErrConflict)
			}
			if late && (task.ExpiredLease == nil || task.ExpiredLease.EnvelopeDigest != digest) {
				failedVerify = &verifyFailureMetric{j.PoolID, j.JobID, verify.OpEnvelope, time.Since(envStart)}
				return fmt.Errorf("%w: expired task envelope binding", ErrConflict)
			}
			task.EnvelopeUsed = true
			if late {
				task.ExpiredLease.Consumed = true
			}
			if er := e.recordVerifyOutcome(tx, j, verify.OpEnvelope, time.Since(envStart), true, true); er != nil {
				return er
			}
		}
		// R11 Part E addendum: a refused dew receipt leaves evidence. The check runs
		// on the resolved lease, so it can tell "the receipt expired because the
		// active lease outlived it" from a forgery. It runs after the envelope
		// block so the tamper evidence of a forged envelope is recorded first.
		if prep != nil && prep.dewChecked {
			if er := prep.dewErr; er != nil {
				if errors.Is(er, errDewRejected) {
					deadLetter = er
				}
				return er
			}
		} else if er := e.verifyDewSubmission(&j, task, q, l.ExpiresAt); er != nil {
			if errors.Is(er, errDewRejected) {
				deadLetter = er
			}
			return er
		}
		if late && q.Error != "" {
			return fmt.Errorf("%w: late worker error cannot settle a result", ErrConflict)
		}
		if q.Error != "" && isRunnerConfigError(q.Error) {
			// T114 + W9 F1 + W9c row 1 (owner decisions 2026-11-21): the tab's
			// runner does not trust the live task key (stale cached runner.html).
			// That is a page fault, not a task fault, but only for a tab that
			// reports a key id the server really retired on an admin rotation.
			// A current pin, no pin, or an id the server never issued is a lie
			// or a broken runner: a normal failed try with a reputation fail
			// event below. One task refunds at most one stale report
			// (Task.Refunds), so cycling tabs cannot farm tries.
			pinKeyID := runnerPinKeyID(w.RunnerPin)
			currentKeyID := e.currentTaskKeyID()
			if pinKeyID != "" && pinKeyID != currentKeyID {
				// A page that does not trust the live key gets no lease until it
				// reloads, even when the task already spent its one refund.
				if !w.RunnerStale {
					w.RunnerStale = true
					if er := tx.Put(store.Workers, worker, w); er != nil {
						return er
					}
				}
				// W9c row 1 + W9d row 1: only a key id the server really retired
				// before an admin rotation, carried with the sha256 of the runner
				// page the server serves, proves the page is stale. An unknown or
				// random id, or a foreign page hash, is a failed try with a
				// reputation fail event below.
				if task.Refunds == 0 && e.taskKeyRetired(pinKeyID) && runnerPinPageOK(w.RunnerPin) {
					task.Refunds++
					if task.Attempts > 0 {
						task.Attempts--
					}
					e.Log.Warn("worker.runner_stale", "workerId", worker, "jobId", j.JobID, "reason", q.Error, "runnerPin", w.RunnerPin)
					if er := e.fail(tx, &j, task, q.Error, nil, &settledRedis); er != nil {
						return er
					}
					out = Result{JobID: j.JobID, TaskID: task.TaskID, Status: "requeued", Attempts: task.Attempts, At: e.Now().UTC(), Error: q.Error, WorkerID: worker}
					return e.save(tx, &j, &metered)
				}
			}
			e.Log.Warn("worker.runner_stale_rejected", "workerId", worker, "jobId", j.JobID, "reason", q.Error, "runnerPin", w.RunnerPin, "refunds", task.Refunds, "currentKeyId", currentKeyID, "runnerPageOK", runnerPinPageOK(w.RunnerPin))
		}
		if q.Error != "" {
			// A worker error is a reputation event (WWPP-R37.1) and a retry.
			observations = append(observations, reputationObservation{worker, verify.EventFail, task.TaskID, q.Error})
			if er := e.recordReputationOutcome(tx, j, verify.EventFail); er != nil {
				return er
			}
			if er := e.meterLeaseRuntime(tx, *l, &metered); er != nil {
				return er
			}
			if er := e.fail(tx, &j, task, q.Error, &observations, &settledRedis); er != nil {
				return er
			}
			out = Result{JobID: j.JobID, TaskID: task.TaskID, Status: "failed", Attempts: task.Attempts, At: e.Now().UTC(), Error: q.Error, WorkerID: worker}
			return e.save(tx, &j, &metered)
		}
		if len(q.Output) == 0 || len(q.Output) >= 256*1024 || !json.Valid(q.Output) || q.SHA256 == "" {
			return ErrInvalid
		}
		hashStart := time.Now()
		digest := verify.SHA256(q.Output)
		hashDur := time.Since(hashStart)
		// A1 (WO R17): the digest was hashed before the transaction; the bytes are
		// immutable, so the prepared value is the same one.
		if prep != nil && prep.outputDigest != "" {
			digest, hashDur = prep.outputDigest, prep.hashDur
		}
		if !late {
			if er := e.meterLeaseRuntime(tx, *l, &metered); er != nil {
				return er
			}
		}
		// The result hash check (WWPP-R37.1): the declared hash must match the bytes.
		// The measure can be switched off by the admin (R37.1b).
		hashChecked := cfg.HashCheckEnabled
		hashOK := q.SHA256 == digest
		e.Log.Info("tamper.result", "jobId", j.JobID, "taskId", task.TaskID, "valid", hashOK)
		if hashChecked {
			if er := e.recordVerifyOutcome(tx, j, verify.OpHash, hashDur, hashOK, true); er != nil {
				return er
			}
		}
		if !hashOK && hashChecked {
			// A tampered result is a reputation failure of the worker and the task is
			// flagged for review; it is never accepted.
			observations = append(observations, reputationObservation{worker, verify.EventFail, task.TaskID, "result hash mismatch"})
			if er := e.recordReputationOutcome(tx, j, verify.EventFail); er != nil {
				return er
			}
			task.Flagged = true
			task.FlagReason = "result hash mismatch"
			out = Result{JobID: j.JobID, TaskID: task.TaskID, Status: "failed", Attempts: task.Attempts, At: e.Now().UTC(), Error: "result hash mismatch", WorkerID: worker, HashOK: false, Flagged: true, FlagReason: "result hash mismatch"}
			if er := e.event(tx, j, "task.tampered", &out); er != nil {
				return er
			}
			if er := e.failWith(tx, &j, task, &out, false, &settledRedis); er != nil {
				return er
			}
			return e.save(tx, &j, &metered)
		}
		// The module hash check runs on every load of the task (WWPP-R37.1).
		if ok, reason := e.modulesIntact(tx, &j); !ok {
			task.Flagged = true
			task.FlagReason = reason
			out = Result{JobID: j.JobID, TaskID: task.TaskID, Status: "failed", Attempts: task.Attempts, At: e.Now().UTC(), Error: reason, WorkerID: worker, HashOK: false, Flagged: true, FlagReason: reason}
			if er := e.event(tx, j, "task.tampered", &out); er != nil {
				return er
			}
			if er := e.failWith(tx, &j, task, &out, false, &settledRedis); er != nil {
				return er
			}
			return e.save(tx, &j, &metered)
		}
		// The canary check of the returned result (WWPP-R37.1, R39.1).
		canaryState := task.CanaryState
		if canaryState == "" {
			canaryState = "not-run"
		}
		if task.CanaryID != "" {
			cr, er := e.checkCanary(task.CanaryID, q.Output)
			if er != nil {
				return er
			}
			if cr.Pass {
				canaryState = "pass"
			} else {
				canaryState = "fail"
			}
			task.CanaryState = canaryState
			task.CanaryEvidenceID = cr.CanaryID
			task.CanaryWorkerID = worker
			// W9b row 1: the authenticated lease is `l` (the late path rebuilds
			// it from the expired evidence); task.Lease may be nil there.
			task.CanaryLeaseToken = l.Token
			task.CanaryGeneration = l.Generation
			if er := e.recordCanaryOutcome(tx, j, cr.Pass); er != nil {
				return er
			}
			if !cr.Pass {
				e.Log.Warn("tamper.canary", "jobId", j.JobID, "taskId", task.TaskID, "workerId", worker, "canaryId", cr.CanaryID, "reason", cr.Reason)
				task.Flagged = true
				task.FlagReason = "canary failed: " + cr.Reason
				observations = append(observations, reputationObservation{worker, verify.EventCanaryFail, task.TaskID, cr.Reason})
				if er := e.recordReputationOutcome(tx, j, verify.EventCanaryFail); er != nil {
					return er
				}
				task.CanaryID = ""
				task.CanaryTaskID = ""
				out = Result{JobID: j.JobID, TaskID: task.TaskID, Status: "failed", Attempts: task.Attempts, At: e.Now().UTC(), Error: task.FlagReason, WorkerID: worker, HashOK: hashChecked && hashOK, CanaryID: cr.CanaryID, CanaryState: canaryState, CanaryOK: false, Flagged: true, FlagReason: task.FlagReason}
				if er := e.failWith(tx, &j, task, &out, false, &settledRedis); er != nil {
					return er
				}
				return e.save(tx, &j, &metered)
			}
		}
		// R11-G1: a submission that answers a canary IS the canary evidence. It is
		// never judged by the developer measures below: a recompute against a
		// matmul answer accuses an honest worker, and a replica quorum or a re-run
		// comparison would settle the developer task from canary bytes. Captured
		// here, before the canary settle below clears the binding.
		canarySubmission := task.CanaryID != "" || task.CanaryTaskID != ""
		hashVerified := hashChecked && hashOK
		canaryEnabled := cfg.CanaryEnabled
		canaryRan := task.CanaryAttempts > 0 || task.CanaryID != "" || task.CanaryEvidenceID != ""
		canaryEvidenceID := task.CanaryEvidenceID
		if task.CanaryID != "" {
			canaryEvidenceID = task.CanaryID
		}
		canaryBound := canaryRan && canaryState == "pass" &&
			task.CanaryWorkerID == worker && task.CanaryLeaseToken != ""
		trustComplete := hashVerified && (!canaryEnabled || canaryBound)
		if w.Anonymous && !canaryBound {
			trustComplete = false
		}
		out = Result{JobID: j.JobID, TaskID: task.TaskID, Status: "done", Attempts: task.Attempts, Output: q.Output, SHA256: digest, At: e.Now().UTC(), WorkerID: worker, Verified: trustComplete, HashOK: hashVerified, TrustComplete: trustComplete, CanaryID: canaryEvidenceID, CanaryState: canaryState, CanaryOK: canaryState == "pass", CanaryEnabled: canaryEnabled, CanaryRan: canaryRan, Flagged: task.Flagged, FlagReason: task.FlagReason, Late: late}
		if late {
			at := now.UTC()
			out.LateAcceptedAt = &at
		}
		out.GPU = q.GPU
		// W10C row 1: a replica of a redundant task settles the quorum, starts the
		// single tie-breaker, or lets the majority win.
		if task.Redundant != nil {
			settled, er := e.settleRedundantResult(tx, &j, task, w, l, q, out, &observations, &metered, &settledRedis)
			out = settled
			return er
		}
		// W10C row 2: the server recomputes a sampled matmul result with its native
		// kernel. The comparison is against the raw output digest the platform
		// reports, never against worker-supplied verdicts.
		//
		// A1 (WO R17): the native recompute runs BEFORE this transaction, inside the
		// bounded worker pool, so a claim/submit transaction never waits for it. The
		// transaction keeps the settlement rules: the mismatch below is the same one.
		//
		// WO R18 (G-R17-1..3): the verdict now carries the malformed claim and the
		// pinned reference size, and the per-pool policies decide the outcome.
		if !canarySubmission && task.Pending == nil && task.DeferredRecompute == nil && e.recomputeSampled(&j, task) {
			v := e.recomputeVerdictStateFor(prep, &j, task, q.Output)
			if v.ran {
				if v.invalid && e.RecomputeMalformedPolicy(j.PoolID) == RecomputeMalformedReject {
					// G-R17-2 (default): an invalid claim must never mark fraud.
					// Commit the rejection/re-lease, then return ErrInvalid to the
					// submitter *after* the transaction: returning it from this
					// callback would roll back the re-lease and trap the old token.
					if late {
						return ErrInvalid // expired lease is already unusable
					}
					rejected = ErrInvalid
					if e.redis != nil {
						settledRedis = append(settledRedis, redisLeaseRef{key: "lease:" + j.JobID + ":" + l.TaskID, token: l.Token})
					}
					task.State = "queued"
					task.Lease = nil
					task.EnvelopeDigest = ""
					task.EnvelopeUsed = false
					out = Result{JobID: j.JobID, TaskID: task.TaskID, Status: "requeued", Attempts: task.Attempts, At: e.Now().UTC(), Error: "invalid recompute digest", WorkerID: worker}
					if er := e.recordLeaseSettled(tx, j, l); er != nil {
						return er
					}
					if er := e.event(tx, j, "task.recomputeMalformedRejected", &out); er != nil {
						return er
					}
					e.Log.Warn("recompute.malformedRejected", "jobId", j.JobID, "taskId", task.TaskID, "workerId", worker)
					return e.save(tx, &j, &metered)
				}
				if !v.match {
					if !v.pinned && !v.invalid {
						// G-R17-3: an unpinned reference never accuses. The
						// result settles unverified and only 2-of-N redundancy
						// may still judge it.
						out.RecomputeUnverified = true
						e.Log.Warn("recompute.unverified", "jobId", j.JobID, "taskId", task.TaskID, "workerId", worker, "reason", "unpinned reference mismatch; result accepted unverified")
					} else {
						reason := "server recompute mismatch"
						task.Flagged = true
						task.FlagReason = reason
						observations = append(observations, reputationObservation{worker, verify.EventFail, task.TaskID, reason})
						if er := e.recordReputationOutcome(tx, j, verify.EventFail); er != nil {
							return er
						}
						out = Result{JobID: j.JobID, TaskID: task.TaskID, Status: "failed", Attempts: task.Attempts, At: e.Now().UTC(), Error: reason, WorkerID: worker, HashOK: hashChecked && hashOK, Flagged: true, FlagReason: reason}
						e.Log.Warn("recompute.mismatch", "jobId", j.JobID, "taskId", task.TaskID, "workerId", worker)
						if er := e.event(tx, j, "task.recomputeMismatch", &out); er != nil {
							return er
						}
						if er := e.failWith(tx, &j, task, &out, false, &settledRedis); er != nil {
							return er
						}
						return e.save(tx, &j, &metered)
					}
				} else {
					out.RecomputeVerified = true
				}
			} else if e.RecomputeBusyPolicy(j.PoolID) == RecomputeBusyDefer && !late {
				// G-R17-1 (defer): hold the result and retry the recompute in
				// the background when a slot frees; the bounded retries fall
				// back to skip. The goroutine starts only after this
				// transaction committed.
				held, er := e.deferRecomputeResult(tx, &j, task, w, l, q, out, &metered, &settledRedis)
				if er != nil {
					return er
				}
				out = held
				deferred = &deferredRecomputeRef{jobID: j.JobID, taskID: task.TaskID}
				return e.save(tx, &j, &metered)
			}
			// The skip policy (the default): settle the submission untouched and
			// accuse no one - the pre-R18 behaviour.
		}
		// The re-run comparison (WWPP-R35.1): the second result of a sampled task is
		// compared with the first one by exact hash only; the server never runs
		// developer code.
		if task.Pending != nil {
			first := task.Pending
			// The comparison is over the exact submitted bytes: the stored raw message
			// is compacted by the JSON round trip, so the original bytes are kept.
			rerunStart := time.Now()
			match := verify.CompareResults(first.exactBytes(), q.Output) && verify.CompareResultHashes(first.SHA256, digest)
			// A1 (WO R17): the comparison was made before the transaction against the
			// same held first result; only its measured cost is taken from the verdict.
			if prep != nil && prep.rerunFirst != nil && prep.rerunFirst.SHA256 == first.SHA256 {
				match, rerunStart = prep.rerunMatch, rerunStart.Add(-prep.rerunDur)
			}
			if er := e.recordVerifyOutcome(tx, j, verify.OpRerun, time.Since(rerunStart), match, true); er != nil {
				return er
			}
			out.Rerun = true
			out.RerunOK = match
			task.Pending = nil
			if !match {
				// A mismatch is a reputation failure for both workers and the task is
				// flagged for review.
				observations = append(observations,
					reputationObservation{first.WorkerID, verify.EventFail, task.TaskID, "re-run mismatch"},
					reputationObservation{worker, verify.EventFail, task.TaskID, "re-run mismatch"})
				if er := e.recordReputationOutcome(tx, j, verify.EventFail); er != nil {
					return er
				}
				if er := e.recordReputationOutcome(tx, j, verify.EventFail); er != nil {
					return er
				}
				task.Flagged = true
				task.FlagReason = "re-run mismatch"
				task.RerunExclude = append(task.RerunExclude, first.WorkerID, worker)
				task.RerunExcludeOwners = append(task.RerunExcludeOwners, owner)
				out.Status = "failed"
				out.Error = "re-run mismatch"
				out.Flagged = true
				out.FlagReason = "re-run mismatch"
				if er := e.event(tx, j, "task.rerunMismatch", &out); er != nil {
					return er
				}
				e.Log.Warn("rerun.mismatch", "jobId", j.JobID, "taskId", task.TaskID, "firstWorkerId", first.WorkerID, "workerId", worker)
				// A mismatch is conclusive: the task is not retried.
				if er := e.failWith(tx, &j, task, &out, true, &settledRedis); er != nil {
					return er
				}
				return e.save(tx, &j, &metered)
			}
			// The hashes match: the result is accepted.
			observations = append(observations, reputationObservation{worker, verify.EventPass, task.TaskID, ""})
			if er := e.recordReputationOutcome(tx, j, verify.EventPass); er != nil {
				return er
			}
			j.Results = append(j.Results, out)
			task.State = "done"
			if e.redis != nil {
				settledRedis = append(settledRedis, redisLeaseRef{key: "lease:" + j.JobID + ":" + l.TaskID, token: l.Token})
			}
			task.Lease = nil
			e.scheduler.completed.Add(1)
			if er := e.recordLeaseSettled(tx, j, l); er != nil {
				return er
			}
			if er := e.recordGPUResultMetrics(tx, j, q.GPU); er != nil {
				return er
			}
			if er := e.finishAccepted(tx, &j, &out, l); er != nil {
				return er
			}
			e.Log.Info("rerun.matched", "jobId", j.JobID, "taskId", task.TaskID, "workerId", worker)
			return e.save(tx, &j, &metered)
		}
		// R11-G1: the winning submission of a canary clears its binding above, so
		// this block also catches the canary answer itself. It must RETURN: the
		// fall-through let that answer reach the redundant quorum, the recompute
		// and the generic settlement as if it were developer output.
		if task.CanaryID != "" || canarySubmission {
			canaryID := task.CanaryEvidenceID
			if task.CanaryID != "" {
				canaryID = task.CanaryID
			}
			task.CanaryState = "pass"
			task.CanaryEvidenceID = canaryID
			task.CanaryWorkerID = worker
			// W9b row 1: same as the check above, bind the authenticated lease.
			task.CanaryLeaseToken = l.Token
			task.CanaryGeneration = l.Generation
			task.CanaryID = ""
			task.CanaryTaskID = ""
			task.State = "queued"
			if e.redis != nil {
				settledRedis = append(settledRedis, redisLeaseRef{key: "lease:" + j.JobID + ":" + l.TaskID, token: l.Token})
			}
			task.Lease = nil
			task.EnvelopeDigest = ""
			task.EnvelopeUsed = false
			canaryHashOK := hashChecked && hashOK
			out = Result{JobID: j.JobID, TaskID: task.TaskID, Status: "canary", Attempts: task.Attempts, At: e.Now().UTC(), WorkerID: worker, HashOK: canaryHashOK, CanaryID: canaryID, CanaryState: "pass", CanaryOK: true, CanaryEnabled: cfg.CanaryEnabled, CanaryRan: true, FlagReason: "canary passed; developer task requeued"}
			if er := e.event(tx, j, "task.canary", &out); er != nil {
				return er
			}
			return e.save(tx, &j, &metered)
		}
		// W10C row 1: a redundancy-sampled first result is held and the task waits
		// in PENDING_VERIFICATION for a replica on a distinct worker. R11-G1: a
		// canary answer never enters the quorum.
		if !late && !canarySubmission && e.redundantRequired(&j, task, w) {
			held, er := e.holdRedundantResult(tx, &j, task, w, l, q, out, &metered, &settledRedis)
			out = held
			return er
		}
		// The re-run measure (WWPP-R35.1): the sampled task waits for a second worker,
		// then the server compares the exact hashes only.
		if !late && !canarySubmission && e.Verifier.ShouldRerunWithKey(e.rerunSecret, j.JobID+":"+task.TaskID, e.requestedRerunPercent(&j)) {
			out.Rerun = true
			task.FirstSHA256 = digest
			task.RerunExclude = append(task.RerunExclude, worker)
			task.RerunExcludeOwners = append(task.RerunExcludeOwners, owner)
			task.Pending = &PendingResult{WorkerID: worker, SHA256: digest, Output: q.Output, Bytes: base64.StdEncoding.EncodeToString(q.Output), Attempts: task.Attempts, At: e.Now().UTC()}
			// The task is queued again; the lease goes to a different worker.
			task.State = "queued"
			// R10 glm: release the claim fence with the lease. Without this ref the
			// second worker's SET NX fails until the fence TTL and the requeued task
			// waits out its re-run window unclaimable (same rule as the canary path).
			if e.redis != nil {
				settledRedis = append(settledRedis, redisLeaseRef{key: "lease:" + j.JobID + ":" + l.TaskID, token: l.Token})
			}
			task.Lease = nil
			if er := e.event(tx, j, "task.rerun", &out); er != nil {
				return er
			}
			e.Log.Info("rerun.dispatched", "jobId", j.JobID, "taskId", task.TaskID, "workerId", worker)
			return e.save(tx, &j, &metered)
		}
		// A passed task is a reputation event only once it is final. A re-run
		// accepted without comparison is flagged instead of rewarding its worker.
		if canaryState == "pass" || canaryState == "not-run" {
			observations = append(observations, reputationObservation{worker, verify.EventPass, task.TaskID, ""})
			if er := e.recordReputationOutcome(tx, j, verify.EventPass); er != nil {
				return er
			}
		}
		j.Results = append(j.Results, out)
		task.State = "done"
		if e.redis != nil {
			settledRedis = append(settledRedis, redisLeaseRef{key: "lease:" + j.JobID + ":" + l.TaskID, token: l.Token})
		}
		task.Lease = nil
		e.scheduler.completed.Add(1)
		if er := e.recordLeaseSettled(tx, j, l); er != nil {
			return er
		}
		if er := e.recordGPUResultMetrics(tx, j, q.GPU); er != nil {
			return er
		}
		if er := e.finishAccepted(tx, &j, &out, l); er != nil {
			return er
		}
		e.Log.Info("lease.settled", "jobId", j.JobID, "taskId", task.TaskID, "generation", task.Generation)
		return e.save(tx, &j, &metered)
	})
	// W9 F2 + W9d row 2: persist a recovery/ban decision even when the submission
	// is refused; the failed transaction discards the observations, so
	// observeCommitted below would never run.
	if err != nil {
		if repChanged {
			e.persistReputation()
		}
		e.persistVerifyFailure(ctx, failedVerify)
		if deadLetter != nil {
			e.recordDewDeadLetter(ctx, q)
		}
		return out, err
	}
	if e.redis != nil {
		e.cleanupRedisLeases(settledRedis)
	}
	e.applyMeteredRuntime(metered)
	e.observeCommitted(observations)
	// W9 F2: persist a recovery/ban decision when no observation did it.
	if repChanged && len(observations) == 0 {
		e.persistReputation()
	}
	// G-R17-1 (WO R18): the deferred recompute retry starts only after the hold
	// committed; the goroutine re-reads the task and stops when it is settled.
	if deferred != nil {
		go e.settleDeferredRecompute(*deferred)
	}
	// F4: evidence is independent of the committed settlement.
	if runAudit {
		_, _ = e.bestEffortRunAudit(ctx, q.Lease.JobID)
	}
	return out, rejected
}

// Sweep runs both on demand and from main, so retry/quarantine progresses without workers.
func (e *Engine) Sweep(ctx context.Context) error {
	var observations []reputationObservation
	var metered []meteredRuntime
	var releasedRedis []redisLeaseRef
	err := e.Store.Update(ctx, func(tx store.Tx) error {
		if er := e.expireSessions(tx, &metered); er != nil {
			return er
		}
		rows, er := tx.List(store.Jobs)
		if er != nil {
			return er
		}
		owned := make(map[string]bool, len(rows))
		for _, r := range rows {
			var j Job
			if er = json.Unmarshal(r.Value, &j); er != nil {
				return er
			}
			snapshot := j
			if er = e.expire(tx, &j, snapshot, owned, &observations, &metered, &releasedRedis); er != nil {
				if errors.Is(er, errJobChanged) {
					// A concurrent transaction changed the job after the lock-free
					// List read; this sweep never writes the stale snapshot (R18B
					// review row 3).
					continue
				}
				return er
			}
		}
		return e.maybeCheckpoint(tx)
	})
	if err == nil {
		if e.redis != nil {
			e.cleanupRedisLeases(releasedRedis)
		}
		e.applyMeteredRuntime(metered)
		e.observeCommitted(observations)
	}
	return err
}

// isRunnerConfigError reports a worker error caused by the page, not the task
// (T114): the runner's pinned key ring lacks the live task key.
func isRunnerConfigError(reason string) bool {
	return strings.Contains(reason, "unknown_operator_key")
}

// maxRunnerPinBytes bounds the pin a worker may report on register/heartbeat.
const maxRunnerPinBytes = 256

// normalizeRunnerPin validates the reported runner pin of a tab (W9 F1). The
// canonical form is "<runner.html sha256>:<task key id>", both lowercase hex; a
// bare task key id is accepted, because the sha half is evidence only.
func normalizeRunnerPin(pin string) (string, error) {
	pin = strings.TrimSpace(pin)
	if pin == "" || len(pin) > maxRunnerPinBytes {
		return "", ErrInvalid
	}
	sha, keyID := runnerPinParts(pin)
	if sha != "" && (len(sha) != 64 || !lowerHex(sha)) {
		return "", ErrInvalid
	}
	if keyID == "" || len(keyID) > 64 || !lowerHex(keyID) {
		return "", ErrInvalid
	}
	return pin, nil
}

// runnerPinParts splits a reported runner pin into the runner.html sha256 and
// the id of the task key the page trusts.
func runnerPinParts(pin string) (sha, keyID string) {
	pin = strings.TrimSpace(pin)
	if i := strings.LastIndexByte(pin, ':'); i >= 0 {
		return strings.TrimSpace(pin[:i]), strings.TrimSpace(pin[i+1:])
	}
	return "", pin
}

// runnerPinKeyID is the task key id half of a reported pin; an empty pin or an
// empty key half yields "" (a tab that cannot say what it trusts).
func runnerPinKeyID(pin string) string {
	_, keyID := runnerPinParts(pin)
	return keyID
}

// runnerPagePinSHA is the sha256 the SERVED runner page claims for itself: the
// sha half of its data-runner-pin="<sha256>:<keyId>" attribute (D-W10-9), or, on
// a page built before that attribute existed, the hash of the exact inline
// runner script the page executes. The whole-file hash is NOT the contract: the
// page's identity is its inline script (the same exact-body rule as the CSP), so
// comparing against the file hash could never match a truthful runner.
//
// The embed is immutable, so both values are computed once.
var (
	runnerPagePinOnce  sync.Once
	runnerPagePinValue string
)

// runnerPinAttr matches the self-reported pin attribute.
var runnerPinAttr = regexp.MustCompile(`data-runner-pin="([0-9a-f]{64}):([0-9a-f]{16})"`)

// runnerInlineScript matches the exact inline runner body the sandbox executes.
var runnerInlineScript = regexp.MustCompile(`(?s)<script type="module">(.*?)</script>`)

func runnerPagePinSHA() string {
	runnerPagePinOnce.Do(func() {
		data, err := fs.ReadFile(web.Pages, "pages/runner.html")
		if err != nil {
			return
		}
		if m := runnerPinAttr.FindStringSubmatch(string(data)); m != nil {
			runnerPagePinValue = m[1]
			return
		}
		if m := runnerInlineScript.FindStringSubmatch(string(data)); m != nil {
			runnerPagePinValue = verify.SHA256([]byte(m[1]))
		}
	})
	return runnerPagePinValue
}

// runnerPinPageOK reports whether the pin carries the sha of the served runner
// page. A bare key id has no page sha and fails closed.
//
// R10 head row: the comparison is against the page's own pin, never the hash of
// the whole file, which no runner can report.
func runnerPinPageOK(pin string) bool {
	sha, _ := runnerPinParts(pin)
	served := runnerPagePinSHA()
	return served != "" && sha == served
}

func lowerHex(v string) bool {
	for i := 0; i < len(v); i++ {
		if c := v[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// currentTaskKeyID is the server's current runner pin key id: the same value the
// admin key rotate endpoint returns as `keyId` (W9 F1).
func (e *Engine) currentTaskKeyID() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.TaskKeyID
}

// retiredTaskKeyIDSet builds the lookup set of the ids that were current before
// an admin rotation (W9c row 1).
func retiredTaskKeyIDSet(ids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id != "" {
			set[id] = struct{}{}
		}
	}
	return set
}

// taskKeyRetired reports whether keyID was the current task key before an admin
// rotation (W9c row 1). An id the server never issued is not evidence.
func (e *Engine) taskKeyRetired(keyID string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, ok := e.retiredTaskKeyIDs[keyID]
	return ok
}
