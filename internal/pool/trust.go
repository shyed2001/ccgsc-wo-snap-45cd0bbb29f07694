// Path: internal/pool/trust.go | Author: Hermes (MiniMax) for the owner | 2026-11-17
// What/why: wiring of the tamper measures of WWPP-R37.1 into the pool engine: the
// signed task envelope over canonical JSON with a key ring, the artifact and result
// hash checks, the reputation events, the re-run sample with hash-only comparison and
// the canary injection; WWPP-R35.1, R37.1, R37.1b, R39.1 (WO T4.2).
package pool

import (
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// DefaultCanaryRate is the share of the tasks that carry an injected canary (R37.1).
const DefaultCanaryRate = 1.0

// TrustConfig holds every switch of the tamper measures that the pool applies.
//
// The four measures themselves (hash check, re-run %, signed envelope, canary plus
// reputation) come from verify.Config: R37.1b puts their on/off switches and their
// parameters on the admin pages, so the pool only reads them. CanaryRate is the pool
// side injection rate of the known-answer canary, because verify.Config carries only
// its on/off switch.
type TrustConfig struct {
	verify.Config
	CanaryRate float64 `json:"canaryRate"`
}

// DefaultTrustConfig returns the defaults of WWPP-R37.1.
func DefaultTrustConfig() TrustConfig {
	return TrustConfig{Config: verify.DefaultConfig(), CanaryRate: DefaultCanaryRate}
}

// TrustConfig returns the active trust configuration of the engine.
func (e *Engine) TrustConfig() TrustConfig {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.trust
}

// SetTrustConfig applies the configuration: the four measure switches and the
// reputation thresholds go to the verifier, the canary rate stays pool side.
func (e *Engine) SetTrustConfig(c TrustConfig) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.trust = c
	e.Verifier.SetConfig(c.Config)
}

// SetVerifyConfig applies only the verify.Config switches of the four measures.
func (e *Engine) SetVerifyConfig(c verify.Config) {
	e.mu.Lock()
	defer e.mu.Unlock()
	cur := e.trust
	cur.Config = c
	e.trust = cur
	e.Verifier.SetConfig(c)
}

// JobRerunPercent returns the effective rate used for a job.
func (e *Engine) JobRerunPercent(j Job) float64 {
	return e.TrustConfig().EffectiveRerunPercent(e.requestedRerunPercent(&j))
}

// TrustStats returns the light metrics of the tamper measures (R37.2).
func (e *Engine) TrustStats() verify.StatsSnapshot { return verify.Stats() }

// VerificationState reports whether configured tamper measures are currently
// able to run, rather than silently presenting an unverified pool as healthy.
func (e *Engine) VerificationState() string {
	cfg := e.TrustConfig()
	if cfg.CanaryEnabled && len(e.canaryList()) == 0 {
		return "degraded: no canary fixture registered"
	}
	e.mu.RLock()
	keyID := e.TaskKeyID
	e.mu.RUnlock()
	if cfg.EnvelopeEnabled && keyID == "" {
		return "degraded: no task signing key"
	}
	return "ok"
}

// TaskPublicKey returns the trusted public key of the task envelope, so an admin
// page or a worker bootstrap can provision it out of band (the embedded key of a
// received envelope is never trusted).
func (e *Engine) TaskPublicKey() ed25519.PublicKey {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.TaskKey.Public().(ed25519.PublicKey)
}

// RotateTaskKey publishes a successor envelope key while retaining the current
// key for the requested overlap window.
func (e *Engine) RotateTaskKey(ctx context.Context, overlap time.Duration) error {
	if overlap < 0 {
		return ErrInvalid
	}
	e.taskKeyRotationMu.Lock()
	defer e.taskKeyRotationMu.Unlock()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	keyID := verify.KeyID(key.Public().(ed25519.PublicKey))
	if err := e.Verifier.Keyring().Add(keyID, key.Public().(ed25519.PublicKey)); err != nil {
		return err
	}
	e.mu.Lock()
	oldID := e.TaskKeyID
	e.TaskKey = key
	e.TaskKeyID = keyID
	if oldID != "" {
		if e.retiredTaskKeyIDs == nil {
			e.retiredTaskKeyIDs = make(map[string]struct{})
		}
		e.retiredTaskKeyIDs[oldID] = struct{}{}
	}
	retired := make([]string, 0, len(e.retiredTaskKeyIDs))
	for id := range e.retiredTaskKeyIDs {
		retired = append(retired, id)
	}
	e.mu.Unlock()
	sort.Strings(retired)
	if oldID != "" && overlap > 0 {
		if err := e.Verifier.Keyring().Retire(oldID, e.Now().UTC().Add(overlap)); err != nil {
			return err
		}
	} else if oldID != "" {
		if err := e.Verifier.Keyring().Revoke(oldID); err != nil {
			return err
		}
	}
	record := taskKeyRecord{
		KeyID:      keyID,
		PrivateKey: append([]byte(nil), key...),
		Keyring:    e.Verifier.Keyring().Snapshot(),
		Retired:    retired,
	}
	return e.Store.Update(ctx, func(tx store.Tx) error {
		return tx.Put(store.Metrics, taskKeyStoreKey, record)
	})
}

// VerifyTaskEnvelope verifies a signed task envelope against the key ring of the
// engine. Unknown, revoked and mismatched key ids fail closed (R39.1).
func (e *Engine) VerifyTaskEnvelope(s verify.SignedTaskEnvelope) error {
	return verify.VerifyTaskEnvelope(s, e.Verifier.Keyring(), e.Now())
}

// RegisterCanary registers a known-answer canary fixture and remembers its id, so
// the scheduler can inject it (the expected answer never leaves the server).
func (e *Engine) RegisterCanary(c verify.Canary) error {
	if err := e.Verifier.Canaries().Register(c); err != nil {
		return err
	}
	e.rememberCanary(c.ID)
	return nil
}

// RegisterCanaryWithAnswer registers a canary and derives the expected hash from the
// known answer.
func (e *Engine) RegisterCanaryWithAnswer(id, taskID string, input, answer []byte) (verify.Canary, error) {
	c, err := e.Verifier.Canaries().RegisterWithAnswer(id, taskID, input, answer)
	if err != nil {
		return verify.Canary{}, err
	}
	e.rememberCanary(id)
	return c, nil
}

// RotateCanary replaces the known answer of a canary and keeps the rotation counter.
func (e *Engine) RotateCanary(id string, answer []byte) (verify.Canary, error) {
	return e.Verifier.Canaries().Rotate(id, answer)
}

// rememberCanary keeps the injected canary ids sorted and unique.
func (e *Engine) rememberCanary(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, v := range e.canaryIDs {
		if v == id {
			return
		}
	}
	e.canaryIDs = append(e.canaryIDs, id)
	sort.Strings(e.canaryIDs)
}

// canaryList returns the registered canary ids.
func (e *Engine) canaryList() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]string(nil), e.canaryIDs...)
}

// moduleDigest returns the artifact hash that a task envelope binds to. A single
// module artifact is addressed by its own content hash; several module artifacts are
// bound by the digest of their sorted ids.
func moduleDigest(ids []string) string {
	if len(ids) == 0 {
		return verify.SHA256(nil)
	}
	if len(ids) == 1 {
		return ids[0]
	}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	return verify.SHA256([]byte(strings.Join(sorted, "\n")))
}

// signTaskEnvelope signs the canonical task envelope of one lease: job id, task id,
// artifact hash, input hash, time bounds and key id, over canonical JSON with a
// domain separator (WWPP-R37.1, R39.1). The entry ABI kind travels on the Job
// because it is read at job creation, inside the transaction that already held
// the artifact records.
func (e *Engine) signTaskEnvelope(j *Job, t *Task, l Lease, input []byte) (verify.SignedTaskEnvelope, error) {
	now := e.Now()
	ttl := l.ExpiresAt.Sub(now)
	if ttl < time.Second {
		ttl = e.TTL
	}
	if ttl < time.Second {
		ttl = time.Second
	}
	taskID := t.TaskID
	if t.CanaryTaskID != "" && l.TaskID == t.CanaryTaskID {
		taskID = t.CanaryTaskID
	}
	e.mu.RLock()
	key, keyID := e.TaskKey, e.TaskKeyID
	e.mu.RUnlock()
	ids := j.Request.ModuleArtifactIDs
	// R11-G2: never sign an envelope whose file list the schema would refuse.
	if len(ids) > verify.MaxWorkloadFiles {
		return verify.SignedTaskEnvelope{}, fmt.Errorf("%w: %d workload files, at most %d are allowed", ErrInvalid, len(ids), verify.MaxWorkloadFiles)
	}
	env := verify.NewTaskEnvelope(j.JobID, taskID, moduleDigest(ids), verify.SHA256(input), keyID, now, ttl)
	env.LeaseToken = l.Token
	env.WorkerID = l.WorkerID
	// D-W10-6: the Director selected isolation mode is copied into the signed
	// envelope, so a tab executes only the isolation the pool owner chose.
	env.IsolationMode = string(e.PoolSettings(j.PoolID).IsolationMode)
	// D-W10-3: sign the content-addressed workload identity/version and EACH
	// artifact's SHA-256 with the existing task operator key. For now the paths
	// are stable positions in the job's artifact list; the manifest remains a
	// build aid, not a runtime trust anchor. Multi-file execution remains gated
	// by the contributor until a verified dependency loader is implemented.
	if len(ids) > 0 {
		files := make([]verify.WorkloadFile, 0, len(ids))
		for i, id := range ids {
			files = append(files, verify.WorkloadFile{Path: fmt.Sprintf("artifact-%03d", i), SHA256: id})
		}
		// D-W10-10: the ABI kind is signed with the rest of the descriptor, so a
		// job cannot present a js-module under the byte ABI or the other way
		// round. It was read from the artifact record at job creation.
		env.Workload = &verify.WorkloadDescriptor{ID: moduleDigest(ids), Version: 1, Kind: j.WorkloadKind, Files: files}
	}
	return verify.SignTaskEnvelope(env, key, keyID)
}

func (e *Engine) loadReputation() error {
	var snapshot verify.ReputationSnapshot
	err := e.Store.View(context.Background(), func(r store.Reader) error {
		return r.Get(store.Metrics, "reputation", &snapshot)
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load reputation: %w", err)
	}
	e.Verifier.Reputation().Restore(snapshot)
	e.repPersistSeq = snapshot.Sequence
	return nil
}

// persistReputation writes the whole in-memory tracker to the store. The
// snapshot and its sequence are taken under one lock, so a persistence that
// starts later always carries a higher sequence. The write is skipped when the
// store already holds a newer sequence: without that guard an older snapshot
// whose transaction commits late can overwrite a newer one (AT22-L3-4).
func (e *Engine) persistReputation() {
	e.repPersistMu.Lock()
	e.repPersistSeq++
	snapshot := e.Verifier.Reputation().Snapshot()
	snapshot.Sequence = e.repPersistSeq
	e.repPersistMu.Unlock()
	if err := e.Store.Update(context.Background(), func(tx store.Tx) error {
		var current verify.ReputationSnapshot
		if err := tx.Get(store.Metrics, "reputation", &current); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if current.Sequence > snapshot.Sequence {
			return nil
		}
		return tx.Put(store.Metrics, "reputation", snapshot)
	}); err != nil {
		e.Log.Warn("reputation.persist", "error", err)
	}
}

func (e *Engine) verifyRenewedEnvelope(s verify.SignedTaskEnvelope, lease Lease) error {
	err := e.VerifyTaskEnvelope(s)
	if !errors.Is(err, verify.ErrExpired) {
		return err
	}
	env, decodeErr := verify.DecodeTaskEnvelope(s)
	if decodeErr != nil || !lease.ExpiresAt.After(time.Unix(env.ExpiresAt, 0).UTC()) {
		return err
	}
	return verify.VerifyTaskEnvelope(s, e.Verifier.Keyring(), time.Unix(env.ExpiresAt-1, 0).UTC())
}

// pickCanary draws the canary that rides with a task, at the configured rate. The
// injection decision remains stable for a task, while fixture selection is
// unpredictable so a worker cannot prepare for a known fixture sequence.
func (e *Engine) pickCanary(j *Job, taskID string) *verify.Canary {
	cfg := e.TrustConfig()
	if !cfg.CanaryEnabled || cfg.CanaryRate <= 0 {
		return nil
	}
	ids := e.canaryList()
	if len(ids) == 0 {
		return nil
	}
	key := j.JobID + ":" + taskID
	if !verify.ShouldRerunWithKey(e.rerunSecret, key, cfg.CanaryRate) {
		return nil
	}
	id := ids[int(verify.RerunBucketWithKey(e.rerunSecret, key))%len(ids)]
	c, err := e.Verifier.Canaries().Get(id)
	if err != nil {
		return nil
	}
	return &c
}

// checkCanary runs the canary check of a returned task result. The expected answer
// stays server side; the comparison is a hash compare only. A failed canary is a
// reputation event of the worker (WWPP-R37.1, R39.1).
func (e *Engine) checkCanary(taskCanaryID string, answer []byte) (verify.CanaryResult, error) {
	if !e.TrustConfig().CanaryEnabled {
		return verify.CanaryResult{CanaryID: taskCanaryID, Pass: true, Reason: "canaries disabled", At: e.Now().UTC()}, nil
	}
	return e.Verifier.Canaries().CheckCanary(taskCanaryID, answer), nil
}

// observe records one reputation event of a worker. The measure can be switched off
// by the admin (R37.1b); a banned worker stays banned and only further canary
// failures are recorded.
func (e *Engine) observe(workerID string, kind verify.EventKind, taskID, detail string) {
	if workerID == "" {
		return
	}
	if !e.TrustConfig().ReputationEnabled {
		return
	}
	if _, err := e.Verifier.Reputation().Observe(workerID, kind, taskID, detail); err != nil && !errors.Is(err, verify.ErrBannedWorker) {
		e.Log.Warn("reputation.observe", "workerId", workerID, "kind", string(kind), "error", err)
	}
}

// eligible reports whether a worker may still receive work. A barred or a banned
// worker is excluded from every lease; a warned worker still works, but the standing
// travels with the assignment (WWPP-R37.1 warn/bar/ban).
//
// W9d row 3: a recovery, ban or reseed decided here is persisted immediately, so a
// caller outside Claim/Submit cannot lose it by discarding the changed flag. It must
// not be called from inside a store transaction (persistReputation opens its own).
func (e *Engine) eligible(workerID string) (bool, verify.Status) {
	ok, st, changed := e.eligibleWithRecovery(workerID)
	if changed {
		e.persistReputation()
	}
	return ok, st
}

// eligibleWithRecovery is eligible plus whether the recovery ladder changed the
// stored record (W9 F2), so a caller persists the recovery count and ban-until
// after the transaction commits.
func (e *Engine) eligibleWithRecovery(workerID string) (bool, verify.Status, bool) {
	rep := e.Verifier.Reputation()
	st, err := rep.StatusOf(workerID)
	if err != nil {
		// An unknown worker has no evidence against it.
		return true, "", false
	}
	changed := false
	if st == verify.StatusBar || st == verify.StatusBan {
		// T115 option 3 + W9 F2: a bar recovers over the progressive quiet
		// ladder (15 min, 1 h, 4 h), the fourth bar is a 24 h ban, and after the
		// ban the worker is reseeded. A canary-failure ban (no ban-until) stays
		// until an owner/admin unban.
		before, _ := rep.Worker(workerID)
		st = rep.Recover(workerID, anonymousRecoverQuiet, anonymousSeedScore(rep.Config().BarBelow))
		after, _ := rep.Worker(workerID)
		changed = before.Score != after.Score || before.Status != after.Status ||
			before.Recoveries != after.Recoveries || before.Banned != after.Banned ||
			!before.BanUntil.Equal(after.BanUntil)
	}
	switch st {
	case verify.StatusBar, verify.StatusBan:
		return false, st, changed
	}
	return true, st, changed
}

// excluded reports whether a worker must not take a task: a held re-run goes to a
// different worker than the one that produced the first result (WWPP-R35.1).
func excluded(t *Task, worker, owner string) bool {
	for _, id := range t.RerunExclude {
		if id == worker {
			return true
		}
	}
	for _, id := range t.RerunExcludeOwners {
		if id == owner {
			return true
		}
	}
	return false
}

// requestedRerunPercent returns the re-run rate of a job: the rate the developer
// requested, otherwise the configured default (R35.1: default 1 %, developer 0-100 %).
func (e *Engine) requestedRerunPercent(j *Job) float64 {
	if j.Request.RerunPercent != nil {
		return *j.Request.RerunPercent
	}
	return e.TrustConfig().RerunPercent
}

// reRunWindow is how long a held re-run waits for a second worker before the first
// result is accepted. A re-run never blocks a job forever.
func (e *Engine) reRunWindow() time.Duration {
	w := 2 * e.TTL
	if w < time.Minute {
		w = time.Minute
	}
	return w
}

// modulesIntact runs the hash check of the module artifacts of a job (WWPP-R37.1: the
// hash check runs on every load). It returns the reason when an artifact is tampered.
// The measure can be switched off by the admin (R37.1b).
func (e *Engine) modulesIntact(tx store.Tx, j *Job) (bool, string) {
	if !e.TrustConfig().HashCheckEnabled {
		return true, ""
	}
	for _, id := range j.Request.ModuleArtifactIDs {
		var a Artifact
		if er := tx.Get(store.Artifacts, id, &a); er != nil {
			return false, "module artifact missing"
		}
		if !a.Complete {
			return false, "module artifact incomplete"
		}
		if er := verify.CheckTimed(a.Data, id, nil); er != nil {
			e.Log.Warn("tamper.artifact.result", "jobId", j.JobID, "artifactId", id, "valid", false)
			return false, "module artifact hash mismatch"
		}
	}
	return true, ""
}
