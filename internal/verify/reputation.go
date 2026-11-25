// Path: internal/verify/reputation.go | Author: Hermes (MiniMax) for the owner | 2026-11-17
// What/why: per-worker reputation with warn / bar / ban thresholds and evidence;
// WWPP-R37.1 (canary + reputation), R39.1 (explicit reputation algorithm).
package verify

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MaxEvidence bounds recent per-worker details; lifetime totals remain available.
const MaxEvidence = 100

// copyWorker prevents returned evidence from aliasing the bounded live buffer.
func copyWorker(w *WorkerReputation) WorkerReputation {
	out := *w
	out.Evidence = append([]Event(nil), w.Evidence...)
	return out
}

// EventKind is one kind of reputation event.
type EventKind string

// Reputation event kinds.
const (
	EventPass         EventKind = "pass"
	EventFail         EventKind = "fail"
	EventCanaryFail   EventKind = "canary_fail"
	EventTimeout      EventKind = "timeout"
	EventInconclusive EventKind = "inconclusive"
)

// Errors of the reputation API.
var (
	ErrUnknownWorker = errors.New("verify: unknown worker")
	ErrBannedWorker  = errors.New("verify: worker is banned")
)

// Status is the reputation standing of a worker.
type Status string

// Reputation statuses.
const (
	StatusGood Status = "good"
	StatusWarn Status = "warn"
	StatusBar  Status = "bar"
	StatusBan  Status = "ban"
)

// ReputationConfig holds the thresholds and weights of the reputation algorithm.
// Defaults (WWPP-R37.1): warn below 0.8, bar below 0.5, ban after 3 canary failures.
type ReputationConfig struct {
	WarnBelow        float64 `json:"warnBelow"`
	BarBelow         float64 `json:"barBelow"`
	BanCanaryFails   int     `json:"banCanaryFails"`
	PassWeight       float64 `json:"passWeight"`
	FailWeight       float64 `json:"failWeight"`
	TimeoutWeight    float64 `json:"timeoutWeight"`
	CanaryFailWeight float64 `json:"canaryFailWeight"`
	StartScore       float64 `json:"startScore"`
}

// DefaultReputationConfig returns the defaults of WWPP-R37.1.
func DefaultReputationConfig() ReputationConfig {
	return ReputationConfig{
		WarnBelow:        0.8,
		BarBelow:         0.5,
		BanCanaryFails:   3,
		PassWeight:       0.0,
		FailWeight:       0.1,
		TimeoutWeight:    0.05,
		CanaryFailWeight: 0.25,
		StartScore:       1.0,
	}
}

// Event is one recorded reputation observation.
type Event struct {
	Kind   EventKind `json:"kind"`
	At     time.Time `json:"at"`
	TaskID string    `json:"taskId,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

// WorkerReputation is the reputation state of one worker.
type WorkerReputation struct {
	WorkerID    string    `json:"workerId"`
	Score       float64   `json:"score"`
	Status      Status    `json:"status"`
	Passes      int       `json:"passes"`
	Fails       int       `json:"fails"`
	CanaryFails int       `json:"canaryFails"`
	Timeouts    int       `json:"timeouts"`
	TotalEvents int       `json:"totalEvents"`
	Banned      bool      `json:"banned"`
	BannedAt    time.Time `json:"bannedAt,omitempty"`
	FirstSeen   time.Time `json:"firstSeen"`
	LastEvent   time.Time `json:"lastEvent"`
	// Progressive recovery (W9 F2): the quiet recoveries already granted and the
	// end of the 24 h ban of the fourth bar. Both are persisted with the record.
	Recoveries int       `json:"recoveries,omitempty"`
	BanUntil   time.Time `json:"banUntil,omitempty"`
	Evidence   []Event   `json:"evidence,omitempty"`
}

// Tracker is an in-memory reputation tracker with Snapshot and Restore.
// The legacy `Reputation` interface in verify.go stays the seam other packages use.
type Tracker struct {
	mu      sync.RWMutex
	cfg     ReputationConfig
	workers map[string]*WorkerReputation
	metrics *Metrics
	now     func() time.Time
}

// NewReputation returns a tracker with the default configuration.
func NewReputation(m *Metrics) *Tracker {
	return NewReputationWithConfig(DefaultReputationConfig(), m)
}

// NewReputationWithConfig returns a tracker with an explicit configuration.
func NewReputationWithConfig(cfg ReputationConfig, m *Metrics) *Tracker {
	if cfg.StartScore == 0 {
		cfg.StartScore = 1.0
	}
	return &Tracker{cfg: cfg, workers: make(map[string]*WorkerReputation), metrics: m, now: time.Now}
}

// Config returns the active configuration.
func (r *Tracker) Config() ReputationConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg
}

// SetConfig replaces the thresholds (admin pages tune every measure, WWPP-R37.1b).
func (r *Tracker) SetConfig(cfg ReputationConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cfg.StartScore == 0 {
		cfg.StartScore = 1.0
	}
	r.cfg = cfg
}

// Observe records one event for a worker and returns the new state. Transport errors
// alone never lower the score (R39.1): use EventTimeout for genuine timeouts only.
func (r *Tracker) Observe(workerID string, kind EventKind, taskID, detail string) (WorkerReputation, error) {
	start := startOn(r.metrics)
	r.mu.Lock()
	w, ok := r.workers[workerID]
	if !ok {
		w = &WorkerReputation{WorkerID: workerID, Score: r.cfg.StartScore, FirstSeen: r.now().UTC()}
		r.workers[workerID] = w
	}
	if w.Banned && kind != EventCanaryFail {
		out := copyWorker(w)
		r.mu.Unlock()
		recordOn(r.metrics, OpReputation, start, false)
		return out, ErrBannedWorker
	}
	ev := Event{Kind: kind, At: r.now().UTC(), TaskID: taskID, Detail: detail}
	w.TotalEvents++
	w.LastEvent = ev.At
	if len(w.Evidence) == MaxEvidence {
		copy(w.Evidence, w.Evidence[1:])
		w.Evidence[len(w.Evidence)-1] = ev
	} else {
		w.Evidence = append(w.Evidence, ev)
	}
	switch kind {
	case EventPass:
		w.Passes++
		w.Score += r.cfg.PassWeight
	case EventFail:
		w.Fails++
		w.Score -= r.cfg.FailWeight
	case EventTimeout:
		w.Timeouts++
		w.Score -= r.cfg.TimeoutWeight
	case EventCanaryFail:
		w.CanaryFails++
		w.Score -= r.cfg.CanaryFailWeight
		if r.cfg.BanCanaryFails > 0 && w.CanaryFails >= r.cfg.BanCanaryFails {
			w.Banned = true
			w.BannedAt = ev.At
			// R10 glm: without a window this ban can never lapse - Recover treats a
			// zero BanUntil as permanent - so the canary threshold bans for the same
			// 24 h as the fourth bar and then reseeds like it.
			if w.BanUntil.IsZero() {
				w.BanUntil = ev.At.Add(RecoverBanDuration).UTC()
			}
		}
	case EventInconclusive:
		// An unavailable comparison is evidence-neutral: it is not a worker failure.
	default:
		out := copyWorker(w)
		r.mu.Unlock()
		recordOn(r.metrics, OpReputation, start, false)
		return out, fmt.Errorf("verify: unknown reputation event %q", kind)
	}
	if w.Score < 0 {
		w.Score = 0
	}
	if w.Score > 1 {
		w.Score = 1
	}
	w.Status = r.statusLocked(w)
	out := copyWorker(w)
	r.mu.Unlock()
	recordOn(r.metrics, OpReputation, start, true)
	return out, nil
}

// statusLocked derives the standing of a worker (caller holds the lock).
func (r *Tracker) statusLocked(w *WorkerReputation) Status {
	if w.Banned {
		return StatusBan
	}
	switch {
	case w.Score < r.cfg.BarBelow:
		return StatusBar
	case w.Score < r.cfg.WarnBelow:
		return StatusWarn
	default:
		return StatusGood
	}
}

// Seed initializes or updates the reputation score of a worker.
func (r *Tracker) Seed(workerID string, score float64) WorkerReputation {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.workers[workerID]
	if !ok {
		w = &WorkerReputation{WorkerID: workerID, FirstSeen: r.now().UTC()}
		r.workers[workerID] = w
	}
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	w.Score = score
	w.Status = r.statusLocked(w)
	return copyWorker(w)
}

// Score returns the score of a worker.
func (r *Tracker) Score(workerID string) (float64, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	w, ok := r.workers[workerID]
	if !ok {
		return 0, ErrUnknownWorker
	}
	return w.Score, nil
}

// StatusOf returns the standing of a worker.
func (r *Tracker) StatusOf(workerID string) (Status, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	w, ok := r.workers[workerID]
	if !ok {
		return "", ErrUnknownWorker
	}
	return w.Status, nil
}

// Eligible reports whether a worker may still receive work (not banned).
func (r *Tracker) Eligible(workerID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	w, ok := r.workers[workerID]
	return ok && !w.Banned
}

// Worker returns a copy of the state of one worker.
func (r *Tracker) Worker(workerID string) (WorkerReputation, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	w, ok := r.workers[workerID]
	if !ok {
		return WorkerReputation{}, ErrUnknownWorker
	}
	return copyWorker(w), nil
}

// Len returns the number of tracked workers.
func (r *Tracker) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.workers)
}

// Workers returns the states of all tracked workers, sorted by worker id.
func (r *Tracker) Workers() []WorkerReputation {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]WorkerReputation, 0, len(r.workers))
	for _, w := range r.workers {
		out = append(out, copyWorker(w))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WorkerID < out[j].WorkerID })
	return out
}

// ReputationSnapshot is the serialisable state of the whole tracker.
type ReputationSnapshot struct {
	// Sequence orders persisted snapshots: a persistence that starts later
	// carries a higher value, so a snapshot whose transaction commits late
	// cannot overwrite a newer one (AT22-L3-4). Zero means an unstamped
	// snapshot, which any stamped write may replace.
	Sequence uint64             `json:"sequence,omitempty"`
	Config   ReputationConfig   `json:"config"`
	Workers  []WorkerReputation `json:"workers"`
}

// Snapshot returns the full state for persistence (pure in-memory now, snapshot/restore
// ready for the durable store work order).
func (r *Tracker) Snapshot() ReputationSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := ReputationSnapshot{Config: r.cfg}
	for _, w := range r.workers {
		out.Workers = append(out.Workers, copyWorker(w))
	}
	sort.Slice(out.Workers, func(i, j int) bool { return out.Workers[i].WorkerID < out.Workers[j].WorkerID })
	return out
}

// Restore replaces the state with a snapshot.
func (r *Tracker) Restore(s ReputationSnapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.Config.StartScore == 0 {
		s.Config.StartScore = 1.0
	}
	r.cfg = s.Config
	r.workers = make(map[string]*WorkerReputation, len(s.Workers))
	for i := range s.Workers {
		w := copyWorker(&s.Workers[i])
		if len(w.Evidence) > MaxEvidence {
			w.Evidence = append([]Event(nil), w.Evidence[len(w.Evidence)-MaxEvidence:]...)
		}
		if w.Score < 0 {
			w.Score = 0
		}
		if w.Score > 1 {
			w.Score = 1
		}
		if w.Status == "" {
			w.Status = r.statusLocked(&w)
		}
		r.workers[w.WorkerID] = &w
	}
}

// Unban clears the ban of a worker (admin review restores eligibility, R39.1).
func (r *Tracker) Unban(workerID string) (WorkerReputation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.workers[workerID]
	if !ok {
		return WorkerReputation{}, ErrUnknownWorker
	}
	w.Banned = false
	w.BannedAt = time.Time{}
	w.BanUntil = time.Time{}
	w.Recoveries = 0
	w.CanaryFails = 0
	w.Score = max(r.cfg.StartScore, r.cfg.BarBelow)
	w.Status = r.statusLocked(w)
	return copyWorker(w), nil
}

// ReputationService adapts the tracker to the legacy Reputation seam: a context aware
// pass/fail observation of a worker. The interface stays untouched for other packages.
type ReputationService struct {
	tracker *Tracker
}

// NewReputationService wraps a tracker in the legacy interface.
func NewReputationService(t *Tracker) *ReputationService {
	if t == nil {
		t = NewReputation(nil)
	}
	return &ReputationService{tracker: t}
}

// Tracker returns the wrapped tracker.
func (s *ReputationService) Tracker() *Tracker { return s.tracker }

// Observe records a pass (ok true) or a fail (ok false) for a worker.
func (s *ReputationService) Observe(ctx context.Context, workerID string, ok bool) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	kind := EventPass
	if !ok {
		kind = EventFail
	}
	_, err := s.tracker.Observe(workerID, kind, "", "")
	return err
}

// Score returns the reputation score of a worker.
func (s *ReputationService) Score(ctx context.Context, workerID string) (float64, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
	return s.tracker.Score(workerID)
}

// Progressive recovery of a barred worker (W9 F2, owner decision 2026-11-21):
// the quiet time quadruples per granted recovery, so a base of 15 min gives
// 15 min, 1 h and 4 h; the fourth bar is a 24 h ban instead, and after it the
// worker is reseeded at the seed score. A canary-failure ban carries no
// ban-until and is lifted only by Unban (owner/admin).
const (
	recoverQuietScale  = 4
	maxQuietRecoveries = 3
	RecoverBanDuration = 24 * time.Hour
)

// Recover applies the progressive recovery ladder to a barred or banned worker.
// quiet is the first step (15 min for anonymous tabs); every granted recovery
// multiplies the next quiet time by four. It returns the status after the call.
func (r *Tracker) Recover(workerID string, quiet time.Duration, score float64) Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.workers[workerID]
	if !ok {
		return ""
	}
	now := r.now()
	if w.Banned {
		if w.BanUntil.IsZero() || now.Before(w.BanUntil) {
			w.Status = StatusBan
			return w.Status
		}
		// The 24 h ban is over: reseed the worker, do not merely unban it.
		w.Banned = false
		w.BannedAt = time.Time{}
		w.BanUntil = time.Time{}
		w.Recoveries = 0
		// R10 glm: a reseed also clears the canary failure count, or the next
		// failure re-bans the worker at once and the reseed was no reseed.
		w.CanaryFails = 0
		w.Score = clampScore(score)
		w.Status = r.statusLocked(w)
		return w.Status
	}
	if r.statusLocked(w) == StatusBar {
		if w.Recoveries >= maxQuietRecoveries {
			// The fourth bar is not quiet-recoverable: a ban for 24 h.
			w.Banned = true
			w.BannedAt = now.UTC()
			w.BanUntil = now.Add(RecoverBanDuration).UTC()
		} else {
			needed := quiet
			for i := 0; i < w.Recoveries; i++ {
				needed *= recoverQuietScale
			}
			if now.Sub(w.LastEvent) >= needed {
				w.Recoveries++
				w.Score = clampScore(score)
			}
		}
	}
	w.Status = r.statusLocked(w)
	return w.Status
}

// clampScore keeps a score inside the unit interval.
func clampScore(score float64) float64 {
	return max(0, min(1, score))
}
