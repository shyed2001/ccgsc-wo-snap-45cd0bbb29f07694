// File: pools-core/internal/pool/settings_W10C.go (go)
// W10C-SERVER row 3: per-pool verification and isolation settings selectable on
// the Director panel (WWPP-R3.4 keep all verification modes, D-W10-6, D-W10-7).
package pool

import (
	"context"
	"errors"
	"fmt"
	"math"

	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
)

// VerifyMode selects which result verification measures a pool runs.
type VerifyMode string

// Verification modes (WWPP-R3.4: every mode stays selectable).
const (
	VerifyModeOff       VerifyMode = "off"
	VerifyModeSample    VerifyMode = "sample"
	VerifyModeRedundant VerifyMode = "redundant"
	VerifyModeBoth      VerifyMode = "both"
)

// IsolationMode is the hostile-workload isolation of a pool (D-W10-6).
type IsolationMode string

// Isolation modes: a separate kernel worker plus locked globals, or locked
// globals inside the single sandbox worker.
const (
	IsolationSeparateLock IsolationMode = verify.IsolationSeparateLock
	IsolationLockOnly     IsolationMode = verify.IsolationLockOnly
)

// DefaultRedundantRate is the share of tasks that get a redundant replica.
const DefaultRedundantRate = 0.10

// DefaultSampleRate is the share of matmul tasks the server recomputes. The
// server recompute is OPTIONAL (owner decision 2026-11-21, Part D of R11): the
// shipped default is 0, so recompute is off until the Director sets a sampling
// verifyMode AND a sample rate above 0. The developer-led re-run measure keeps
// its own default and is not affected by this value.
const DefaultSampleRate = 0

// ErrSettingsConflict reports a stale expectedVersion on a pool settings write.
var ErrSettingsConflict = errors.New("pool settings version conflict")

// PoolSettings is the Director controlled verification configuration of one pool.
type PoolSettings struct {
	Version       int           `json:"version"`
	VerifyMode    VerifyMode    `json:"verifyMode"`
	RedundantRate float64       `json:"redundantRate"`
	SampleRate    float64       `json:"sampleRate"`
	IsolationMode IsolationMode `json:"isolationMode"`
	// W10D row 1 (D-W10-11): the contributor controls of this pool.
	ContributorControls ContributorControls `json:"contributorControls"`
}

// normalized materializes the D-W10-11 field. A record written before the field
// existed, or a zero value built in code, keeps the documented default (off).
func (s PoolSettings) normalized() PoolSettings {
	if s.ContributorControls == "" {
		s.ContributorControls = ContributorControlsDefault
	}
	return s
}

// DefaultPoolSettings returns the documented defaults. The mode stays "sample"
// so the existing developer-led re-run behaviour is unchanged until the Director
// selects redundant, both or off. Contributor controls stay off, so a contributor
// page keeps its pre-D-W10-11 behaviour until the Director selects a mode.
func DefaultPoolSettings() PoolSettings {
	return PoolSettings{
		VerifyMode:          VerifyModeSample,
		RedundantRate:       DefaultRedundantRate,
		SampleRate:          DefaultSampleRate,
		IsolationMode:       IsolationSeparateLock,
		ContributorControls: ContributorControlsDefault,
	}
}

func (s PoolSettings) validate() error {
	switch s.VerifyMode {
	case VerifyModeOff, VerifyModeSample, VerifyModeRedundant, VerifyModeBoth:
	default:
		return fmt.Errorf("%w: unknown verifyMode %q", ErrInvalid, s.VerifyMode)
	}
	if math.IsNaN(s.RedundantRate) || math.IsInf(s.RedundantRate, 0) || s.RedundantRate < 0 || s.RedundantRate > 1 {
		return fmt.Errorf("%w: redundantRate must be in [0,1]", ErrInvalid)
	}
	if math.IsNaN(s.SampleRate) || math.IsInf(s.SampleRate, 0) || s.SampleRate < 0 || s.SampleRate > 1 {
		return fmt.Errorf("%w: sampleRate must be in [0,1]", ErrInvalid)
	}
	switch s.IsolationMode {
	case IsolationSeparateLock, IsolationLockOnly:
	default:
		return fmt.Errorf("%w: unknown isolationMode %q", ErrInvalid, s.IsolationMode)
	}
	// W10D row 1 (D-W10-11): an unset field is the default; every other value
	// must be one the contributor page knows. The HTTP boundary rejects an
	// explicitly empty value, so only a pre-W10D record or a zero value gets here.
	switch s.ContributorControls {
	case "", ContributorControlsOff, ContributorControlsPauseResume, ContributorControlsStartStopOptIn:
	default:
		return fmt.Errorf("%w: unknown contributorControls %q", ErrInvalid, s.ContributorControls)
	}
	return nil
}

func poolSettingsKey(p string) string { return "pool_settings:" + p }

// PoolSettings returns the active settings of one pool.
func (e *Engine) PoolSettings(p string) PoolSettings {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if s, ok := e.poolSettings[p]; ok {
		return s.normalized()
	}
	return DefaultPoolSettings()
}

// SettingsDelta is a partial write of one pool's settings: a nil field keeps the
// stored value. It is the only write form (R11-D1), so a caller can never build
// a full record from a snapshot taken outside settingsMu and clobber a
// concurrent write of another field.
type SettingsDelta struct {
	VerifyMode          *VerifyMode
	RedundantRate       *float64
	SampleRate          *float64
	IsolationMode       *IsolationMode
	ContributorControls *ContributorControls
}

// applyOn merges the named fields onto the stored record. An unnamed field
// keeps its stored value, including the W10D contributor controls.
func (d SettingsDelta) applyOn(s PoolSettings) PoolSettings {
	if d.VerifyMode != nil {
		s.VerifyMode = *d.VerifyMode
	}
	if d.RedundantRate != nil {
		s.RedundantRate = *d.RedundantRate
	}
	if d.SampleRate != nil {
		s.SampleRate = *d.SampleRate
	}
	if d.IsolationMode != nil {
		s.IsolationMode = *d.IsolationMode
	}
	if d.ContributorControls != nil {
		s.ContributorControls = *d.ContributorControls
	}
	return s
}

// UpdatePoolSettings merges a delta onto the stored settings of one pool and
// writes the result. Read, merge, validate, version check, persist and cache all
// happen under settingsMu, so two concurrent partial writes keep each other's
// field (R11-D1). A stale expectedVersion is refused with ErrSettingsConflict
// before the merged record is validated, so a stale invalid write answers 409
// rather than 400 (R11-D2). A nil expectedVersion writes unconditionally.
func (e *Engine) UpdatePoolSettings(ctx context.Context, p string, delta SettingsDelta, expectedVersion *int) (PoolSettings, error) {
	if p != Thread && p != Process {
		return PoolSettings{}, ErrInvalid
	}
	e.settingsMu.Lock()
	defer e.settingsMu.Unlock()
	current := e.PoolSettings(p)
	if expectedVersion != nil && *expectedVersion != current.Version {
		return PoolSettings{}, ErrSettingsConflict
	}
	next := delta.applyOn(current).normalized()
	if err := next.validate(); err != nil {
		return PoolSettings{}, err
	}
	next.Version = current.Version + 1
	if err := e.Store.Update(ctx, func(tx store.Tx) error {
		return tx.Put(store.Metrics, poolSettingsKey(p), next)
	}); err != nil {
		return PoolSettings{}, err
	}
	e.mu.Lock()
	if e.poolSettings == nil {
		e.poolSettings = make(map[string]PoolSettings)
	}
	e.poolSettings[p] = next
	e.mu.Unlock()
	return next, nil
}

// SetPoolSettings stores the next settings of one pool. A stale expectedVersion
// is refused with ErrSettingsConflict, exactly like the existing versioned admin
// settings endpoints; a nil expectedVersion writes unconditionally. It is a
// whole-record write: callers that merge a partial body use UpdatePoolSettings.
func (e *Engine) SetPoolSettings(ctx context.Context, p string, next PoolSettings, expectedVersion *int) (PoolSettings, error) {
	if p != Thread && p != Process {
		return PoolSettings{}, ErrInvalid
	}
	next = next.normalized()
	e.settingsMu.Lock()
	defer e.settingsMu.Unlock()
	current := e.PoolSettings(p)
	if expectedVersion != nil && *expectedVersion != current.Version {
		return PoolSettings{}, ErrSettingsConflict
	}
	if err := next.validate(); err != nil {
		return PoolSettings{}, err
	}
	next.Version = current.Version + 1
	if err := e.Store.Update(ctx, func(tx store.Tx) error {
		return tx.Put(store.Metrics, poolSettingsKey(p), next)
	}); err != nil {
		return PoolSettings{}, err
	}
	e.mu.Lock()
	if e.poolSettings == nil {
		e.poolSettings = make(map[string]PoolSettings)
	}
	e.poolSettings[p] = next
	e.mu.Unlock()
	return next, nil
}

// loadPoolSettings restores the durable settings of both pools. A missing record
// keeps the defaults; an unreadable or invalid record fails startup closed.
func (e *Engine) loadPoolSettings() error {
	loaded := make(map[string]PoolSettings, 2)
	for _, p := range []string{Thread, Process} {
		var s PoolSettings
		err := e.Store.View(context.Background(), func(r store.Reader) error {
			return r.Get(store.Metrics, poolSettingsKey(p), &s)
		})
		if errors.Is(err, store.ErrNotFound) {
			s = DefaultPoolSettings()
		} else if err != nil {
			return err
		}
		s = s.normalized()
		if err = s.validate(); err != nil {
			return fmt.Errorf("%w: stored pool settings for %s", ErrInvalid, p)
		}
		loaded[p] = s
	}
	e.mu.Lock()
	e.poolSettings = loaded
	e.mu.Unlock()
	return nil
}
