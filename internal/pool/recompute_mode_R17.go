// Path: internal/pool/recompute_mode_R17.go | Author: Hermes (MiniMax-M3) for the owner | 2026-11-22
// What/why: WO R17-RECOMPUTE (G-R16-4) row 2 - the per-pool recompute mode.
//
// The Director chooses per pool whether the server runs the reference recompute
// at all:
//
//   - "registry" - the server recomputes a sampled result through the generic
//     reference registry (the current behaviour, now driven by the registry),
//   - "off"      - the server never recomputes; only the 2-of-N redundancy and
//     the canaries judge a result.
//
// The default is "off" for a new install: the shipped DefaultSampleRate is 0,
// so the recompute never ran until the Director selected a mode AND a sample
// rate above 0. The sample-rate setting itself stays as it is.
//
// The record is durable in the same store the caps settings use (store.Metrics)
// and every write leaves an audit row with the actor, exactly like the caps
// write: the record and the audit row are ONE transaction, the version check
// runs before the merge, and a stale expectedVersion answers ErrSettingsConflict.
package pool

import (
	"context"
	"errors"
	"fmt"

	"ccgsc/poolscore/internal/store"
)

// RecomputeMode selects what the server does with a sampled result.
type RecomputeMode string

// The recompute modes of a pool.
const (
	// RecomputeModeRegistry runs the reference recompute through the registry.
	RecomputeModeRegistry RecomputeMode = "registry"
	// RecomputeModeOff never recomputes: only the 2-of-N redundancy and the
	// canaries judge a result.
	RecomputeModeOff RecomputeMode = "off"
)

// DefaultRecomputeMode is the mode of a pool that never chose one. It is
// "off": a new install must not recompute before the Director asked for it.
const DefaultRecomputeMode = RecomputeModeOff

// RecomputeModes returns the modes the admin panel offers.
func RecomputeModes() []string {
	return []string{string(RecomputeModeRegistry), string(RecomputeModeOff)}
}

// ParseRecomputeMode resolves the wire value of a recompute mode. An unknown
// value is refused: it is never silently the default.
func ParseRecomputeMode(v string) (RecomputeMode, error) {
	switch RecomputeMode(v) {
	case RecomputeModeRegistry, RecomputeModeOff:
		return RecomputeMode(v), nil
	}
	return "", fmt.Errorf("%w: unknown recomputeMode %q", ErrInvalid, v)
}

// RecomputeBusyPolicy selects what a pool does when the sampled recompute
// cannot produce a verdict (verifier busy, prepared verdict stale, or a
// reference that cannot answer) - WO R18, G-R17-1.
type RecomputeBusyPolicy string

// The busy policies of a pool.
const (
	// RecomputeBusySkip settles the submission untouched and accuses no one.
	// It is the pre-R18 behaviour and the default.
	RecomputeBusySkip RecomputeBusyPolicy = "skip"
	// RecomputeBusyDefer keeps the task pending and retries the recompute in
	// the background when a slot frees. The retries are bounded; the bound
	// falls back to skip and logs.
	RecomputeBusyDefer RecomputeBusyPolicy = "defer"
)

// DefaultRecomputeBusyPolicy keeps today's behaviour for a pool that never
// chose one.
const DefaultRecomputeBusyPolicy = RecomputeBusySkip

// RecomputeBusyPolicies returns the busy policies the admin panel offers.
func RecomputeBusyPolicies() []string {
	return []string{string(RecomputeBusySkip), string(RecomputeBusyDefer)}
}

// ParseRecomputeBusyPolicy resolves the wire value of a busy policy. An
// unknown value is refused: it is never silently the default.
func ParseRecomputeBusyPolicy(v string) (RecomputeBusyPolicy, error) {
	switch RecomputeBusyPolicy(v) {
	case RecomputeBusySkip, RecomputeBusyDefer:
		return RecomputeBusyPolicy(v), nil
	}
	return "", fmt.Errorf("%w: unknown recompute busyPolicy %q", ErrInvalid, v)
}

// RecomputeMalformedPolicy selects what a pool does when a sampled submission
// carries a malformed claimed digest - WO R18, G-R17-2.
type RecomputeMalformedPolicy string

// The malformed policies of a pool.
const (
	// RecomputeMalformedReject refuses the submission as invalid and re-leases
	// the task without a fraud mark. It is the default.
	RecomputeMalformedReject RecomputeMalformedPolicy = "reject"
	// RecomputeMalformedFraud keeps the pre-R18 behaviour: the malformed claim
	// is a recompute mismatch with a fraud mark.
	RecomputeMalformedFraud RecomputeMalformedPolicy = "fraud"
)

// DefaultRecomputeMalformedPolicy is the R18 default: a malformed claim is
// rejected, never accused (the owner changed the pre-R18 fraud default).
const DefaultRecomputeMalformedPolicy = RecomputeMalformedReject

// RecomputeMalformedPolicies returns the malformed policies the admin panel
// offers.
func RecomputeMalformedPolicies() []string {
	return []string{string(RecomputeMalformedReject), string(RecomputeMalformedFraud)}
}

// ParseRecomputeMalformedPolicy resolves the wire value of a malformed
// policy. An unknown value is refused: it is never silently the default.
func ParseRecomputeMalformedPolicy(v string) (RecomputeMalformedPolicy, error) {
	switch RecomputeMalformedPolicy(v) {
	case RecomputeMalformedReject, RecomputeMalformedFraud:
		return RecomputeMalformedPolicy(v), nil
	}
	return "", fmt.Errorf("%w: unknown recompute malformedPolicy %q", ErrInvalid, v)
}

// recomputeSettingsKey is the durable key of the recompute record of one pool.
func recomputeSettingsKey(p string) string { return "recompute_settings:" + p }

// RecomputeSettings is the durable admin record of the recompute mode of one
// pool. It is versioned like the pool settings and the caps record.
type RecomputeSettings struct {
	Version int `json:"version"`
	// Mode is the recompute mode of the pool.
	Mode RecomputeMode `json:"mode"`
	// BusyPolicy is the G-R17-1 policy of an unavailable sampled verdict.
	// Pre-R18 records carry no value; they load the default (skip).
	BusyPolicy RecomputeBusyPolicy `json:"busyPolicy,omitempty"`
	// MalformedPolicy is the G-R17-2 policy of a malformed claimed digest.
	// Pre-R18 records carry no value; they load the default (reject).
	MalformedPolicy RecomputeMalformedPolicy `json:"malformedPolicy,omitempty"`
}

// normalized fills the policy fields of a record that predate R18 with the
// documented defaults, so a legacy record validates and behaves like today.
func (s RecomputeSettings) normalized() RecomputeSettings {
	if s.BusyPolicy == "" {
		s.BusyPolicy = DefaultRecomputeBusyPolicy
	}
	if s.MalformedPolicy == "" {
		s.MalformedPolicy = DefaultRecomputeMalformedPolicy
	}
	return s
}

// RecomputeSettingsDelta is a partial recompute write: a nil field keeps the
// stored value.
type RecomputeSettingsDelta struct {
	Mode            *RecomputeMode
	BusyPolicy      *RecomputeBusyPolicy
	MalformedPolicy *RecomputeMalformedPolicy
}

// applyOn merges the named fields onto the stored record and fills the
// defaults (the off mode and the policy defaults), so a record the platform
// can run always validates - also for a pool that never chose a mode yet.
func (d RecomputeSettingsDelta) applyOn(s RecomputeSettings) RecomputeSettings {
	if d.Mode != nil {
		s.Mode = *d.Mode
	}
	if s.Mode == "" {
		s.Mode = DefaultRecomputeMode
	}
	if d.BusyPolicy != nil {
		s.BusyPolicy = *d.BusyPolicy
	}
	if d.MalformedPolicy != nil {
		s.MalformedPolicy = *d.MalformedPolicy
	}
	return s.normalized()
}

// validate refuses a record the platform cannot run.
func (s RecomputeSettings) validate() error {
	switch s.Mode {
	case RecomputeModeRegistry, RecomputeModeOff:
	default:
		return fmt.Errorf("%w: unknown recomputeMode %q", ErrInvalid, s.Mode)
	}
	switch s.BusyPolicy {
	case RecomputeBusySkip, RecomputeBusyDefer:
	default:
		return fmt.Errorf("%w: unknown recompute busyPolicy %q", ErrInvalid, s.BusyPolicy)
	}
	switch s.MalformedPolicy {
	case RecomputeMalformedReject, RecomputeMalformedFraud:
	default:
		return fmt.Errorf("%w: unknown recompute malformedPolicy %q", ErrInvalid, s.MalformedPolicy)
	}
	return nil
}

// SavedRecomputeSettings returns the durable record of one pool and whether
// one exists.
func (e *Engine) SavedRecomputeSettings(p string) (RecomputeSettings, bool) {
	e.recomputeMu.Lock()
	defer e.recomputeMu.Unlock()
	s, ok := e.recomputeSettings[p]
	return s, ok
}

// RecomputeMode returns the effective recompute mode of one pool: the saved
// admin value when there is one, the default otherwise.
func (e *Engine) RecomputeMode(p string) RecomputeMode {
	e.recomputeMu.Lock()
	defer e.recomputeMu.Unlock()
	if s, ok := e.recomputeSettings[p]; ok {
		return s.Mode
	}
	return DefaultRecomputeMode
}

// RecomputeBusyPolicy returns the effective busy policy of one pool (G-R17-1):
// the saved admin value when there is one, the default (skip) otherwise.
func (e *Engine) RecomputeBusyPolicy(p string) RecomputeBusyPolicy {
	e.recomputeMu.Lock()
	defer e.recomputeMu.Unlock()
	if s, ok := e.recomputeSettings[p]; ok {
		return s.BusyPolicy
	}
	return DefaultRecomputeBusyPolicy
}

// RecomputeMalformedPolicy returns the effective malformed policy of one pool
// (G-R17-2): the saved admin value when there is one, the default (reject)
// otherwise.
func (e *Engine) RecomputeMalformedPolicy(p string) RecomputeMalformedPolicy {
	e.recomputeMu.Lock()
	defer e.recomputeMu.Unlock()
	if s, ok := e.recomputeSettings[p]; ok {
		return s.MalformedPolicy
	}
	return DefaultRecomputeMalformedPolicy
}

// RecomputeSettingsAuditKey is the exact key of the committed audit record.
// The admin API logs this key so operators can retrieve the event it references.
func (e *Engine) RecomputeSettingsAuditKey(p string, version int) string {
	return "recompute-change:" + e.ServerID + ":" + p + ":" + fmt.Sprint(version)
}

// CommitRecomputeSettings merges a delta onto the stored record of one pool,
// validates, checks the expected version and writes the record and its audit
// row in ONE transaction. The cache changes only after that transaction
// committed, so a failing store changes nothing.
//
// A stale expectedVersion is refused with ErrSettingsConflict before the merged
// record is validated, so a stale invalid write answers 409 rather than 400
// (R11-D2). A nil expectedVersion writes unconditionally.
func (e *Engine) CommitRecomputeSettings(ctx context.Context, p string, delta RecomputeSettingsDelta, expectedVersion *int, actor string) (RecomputeSettings, error) {
	if p != Thread && p != Process {
		return RecomputeSettings{}, ErrInvalid
	}
	e.recomputeWriteMu.Lock()
	writeLocked := true
	defer func() {
		if writeLocked {
			e.recomputeWriteMu.Unlock()
		}
	}()

	var next RecomputeSettings
	err := e.Store.Update(ctx, func(tx store.Tx) error {
		// A04: read the durable version under the write transaction, never the
		// per-process cache. PostgreSQL Get takes a row lock, so after another
		// coordinator commits we see its new version before deciding.
		var current RecomputeSettings
		lookup := tx.Get(store.Metrics, recomputeSettingsKey(p), &current)
		switch {
		case errors.Is(lookup, store.ErrNotFound):
			// No row yet. The insert below MUST be conditional: two first
			// writers cannot lock a nonexistent row.
		case lookup != nil:
			return lookup
		default:
			current = current.normalized()
			if er := current.validate(); er != nil {
				return er
			}
		}
		if expectedVersion != nil && *expectedVersion != current.Version {
			return ErrSettingsConflict
		}
		next = delta.applyOn(current)
		if er := next.validate(); er != nil {
			return er
		}
		next.Version = current.Version + 1
		if errors.Is(lookup, store.ErrNotFound) {
			atomic, ok := tx.(store.PutIfAbsentTx)
			if !ok {
				// Never silently replace a first writer through a wrapper
				// without the atomic store capability.
				return store.ErrNotImplemented
			}
			inserted, er := atomic.PutIfAbsent(store.Metrics, recomputeSettingsKey(p), next)
			if er != nil {
				return er
			}
			if !inserted {
				return ErrSettingsConflict
			}
		} else if er := tx.Put(store.Metrics, recomputeSettingsKey(p), next); er != nil {
			return er
		}
		event := map[string]any{
			"type":            "recompute.change",
			"actor":           actor,
			"poolId":          p,
			"mode":            next.Mode,
			"busyPolicy":      next.BusyPolicy,
			"malformedPolicy": next.MalformedPolicy,
			"version":         next.Version,
		}
		return tx.Put(store.Audit, e.RecomputeSettingsAuditKey(p, next.Version), event)
	})
	if err != nil {
		if errors.Is(err, ErrSettingsConflict) {
			// Refresh a stale local admin cache after a rejected competing
			// write, so the next GET can expose the current durable version.
			var latest RecomputeSettings
			if er := e.Store.View(ctx, func(r store.Reader) error {
				return r.Get(store.Metrics, recomputeSettingsKey(p), &latest)
			}); er == nil {
				latest = latest.normalized()
				if latest.validate() == nil {
					e.recomputeMu.Lock()
					if current, ok := e.recomputeSettings[p]; !ok || current.Version < latest.Version {
						if e.recomputeSettings == nil {
							e.recomputeSettings = make(map[string]RecomputeSettings)
						}
						e.recomputeSettings[p] = latest
					}
					e.recomputeMu.Unlock()
				}
			}
		}
		return RecomputeSettings{}, err
	}
	e.recomputeMu.Lock()
	if current, ok := e.recomputeSettings[p]; !ok || current.Version < next.Version {
		if e.recomputeSettings == nil {
			e.recomputeSettings = make(map[string]RecomputeSettings)
		}
		e.recomputeSettings[p] = next
	}
	e.recomputeMu.Unlock()
	e.recomputeWriteMu.Unlock()
	writeLocked = false
	// N11 multi-server only: publish AFTER durable commit. Notification
	// failure does not reverse a successful settings+audit transaction;
	// the listener periodically reconciles from the source of truth.
	if er := e.publishRecomputeVersion(ctx, p, next.Version); er != nil {
		e.Log.Warn("recompute.notifyPublishFailed", "pool", p, "error", er)
	}
	return next, nil
}

// loadRecomputeSettings restores the durable admin record of both pools. A
// missing record keeps the default (off); an unreadable or invalid record
// fails startup closed, the same rule the pool settings and the caps record
// follow.
func (e *Engine) loadRecomputeSettings() error {
	loaded := make(map[string]RecomputeSettings, 2)
	for _, p := range []string{Thread, Process} {
		var s RecomputeSettings
		err := e.Store.View(context.Background(), func(r store.Reader) error {
			return r.Get(store.Metrics, recomputeSettingsKey(p), &s)
		})
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		// A pre-R18 record carries no policy fields: fill the documented
		// defaults before validating, so a legacy record never fails the
		// startup and behaves like today.
		s = s.normalized()
		if err = s.validate(); err != nil {
			return fmt.Errorf("%w: stored recompute settings for %s", err, p)
		}
		loaded[p] = s
	}
	e.recomputeMu.Lock()
	e.recomputeSettings = loaded
	e.recomputeMu.Unlock()
	return nil
}
