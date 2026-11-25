// Path: internal/pool/caps_settings_R16.go | Author: Codex for Claude Opus 5.5 | 2026-11-22
// What/why: R16-FIX (ChatGPT review F01/F02/F03) - the admin caps write of the
// server (G-R15-3) must be durable, merged and atomic.
//
// The rules, in the order a write applies them:
//   - the record is durable in the same store the pool settings use
//     (store.Metrics, key "caps_settings"), so a restart keeps the admin values,
//   - the precedence is saved admin value > configuration file > default; a
//     partial write merges onto that base, so it never resets an unnamed knob,
//   - the record is versioned like the pool settings (R11-D2): a stale
//     expectedVersion is refused with ErrSettingsConflict,
//   - read, merge, validate, version check, the record and its audit row all
//     happen in one locked transaction, and the cache changes only after the
//     commit, so a failing store changes nothing.
package pool

import (
	"context"
	"errors"
	"fmt"
	"time"

	"ccgsc/poolscore/internal/caps"
	"ccgsc/poolscore/internal/store"
)

// capsSettingsKey is the durable key of the admin caps record.
const capsSettingsKey = "caps_settings"

// capsPendingRestart are the fields that need a restart of the process: the
// bounded worker pool is built when the engine starts and the connection cap is
// set when the store opens.
var capsPendingRestart = []string{"workerPool", "dbMaxOpenConns"}

// CapsSettings is the durable admin record of the four caps knobs. The values
// have the meaning of caps.Config: zero is auto, never unlimited.
type CapsSettings struct {
	Version int `json:"version"`
	// MaxProcs, MemLimitMB, WorkerPool and DBMaxOpenConns follow caps.Config:
	// zero is auto. MaxProcs clamps at NumCPU-1; the other three must not be
	// negative.
	MaxProcs       int `json:"maxProcs"`
	MemLimitMB     int `json:"memLimitMB"`
	WorkerPool     int `json:"workerPool"`
	DBMaxOpenConns int `json:"dbMaxOpenConns"`
}

// Config returns the knobs of the record.
func (s CapsSettings) Config() caps.Config {
	return caps.Config{
		MaxProcs:       s.MaxProcs,
		MemLimitMB:     s.MemLimitMB,
		WorkerPool:     s.WorkerPool,
		DBMaxOpenConns: s.DBMaxOpenConns,
	}
}

// capsSettingsFromConfig builds a record from the knobs.
func capsSettingsFromConfig(c caps.Config) CapsSettings {
	return CapsSettings{
		MaxProcs:       c.MaxProcs,
		MemLimitMB:     c.MemLimitMB,
		WorkerPool:     c.WorkerPool,
		DBMaxOpenConns: c.DBMaxOpenConns,
	}
}

// validate refuses a record the caps package cannot resolve.
func (s CapsSettings) validate() error {
	if _, err := caps.Resolve(s.Config()); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	return nil
}

// CapsSettingsDelta is a partial caps write: a nil field keeps the value of the
// record. It mirrors SettingsDelta (R11-D1), so a caller can never build a full
// record from a snapshot taken outside capsMu and clobber a concurrent write.
type CapsSettingsDelta struct {
	MaxProcs       *int
	MemLimitMB     *int
	WorkerPool     *int
	DBMaxOpenConns *int
}

// applyOn merges the named fields onto the record.
func (d CapsSettingsDelta) applyOn(s CapsSettings) CapsSettings {
	if d.MaxProcs != nil {
		s.MaxProcs = *d.MaxProcs
	}
	if d.MemLimitMB != nil {
		s.MemLimitMB = *d.MemLimitMB
	}
	if d.WorkerPool != nil {
		s.WorkerPool = *d.WorkerPool
	}
	if d.DBMaxOpenConns != nil {
		s.DBMaxOpenConns = *d.DBMaxOpenConns
	}
	return s
}

// SavedCapsSettings returns the durable admin record and whether one exists.
func (e *Engine) SavedCapsSettings() (CapsSettings, bool) {
	e.capsMu.Lock()
	defer e.capsMu.Unlock()
	return e.capsSettings, e.capsSettingsSaved
}

// ConfiguredCaps returns the four knobs of the configuration file.
func (e *Engine) ConfiguredCaps() caps.Config {
	e.capsMu.Lock()
	defer e.capsMu.Unlock()
	return e.configuredCaps
}

// SetConfiguredCaps records the four knobs of the configuration file. The start
// of the server calls it before it applies the effective caps; it never persists.
func (e *Engine) SetConfiguredCaps(c caps.Config) {
	e.capsMu.Lock()
	e.configuredCaps = c
	e.capsMu.Unlock()
}

// capsBase resolves the knobs a partial write merges onto: the saved admin
// record when there is one, the configuration file otherwise. The caller holds
// capsMu.
func (e *Engine) capsBase() caps.Config {
	if e.capsSettingsSaved {
		return e.capsSettings.Config()
	}
	return e.configuredCaps
}

// EffectiveCaps resolves the effective caps of the server: the saved admin
// record wins over the configuration file, the zero value of every knob stays
// auto. It is the single resolution the admin read view and the start of the
// server share.
func (e *Engine) EffectiveCaps() (caps.Caps, error) {
	e.capsMu.Lock()
	knobs := e.capsBase()
	e.capsMu.Unlock()
	return caps.Resolve(knobs)
}

// CapsSource names where the effective caps came from: "admin" when a saved
// record exists, "config" otherwise.
func (e *Engine) CapsSource() string {
	e.capsMu.Lock()
	defer e.capsMu.Unlock()
	if e.capsSettingsSaved {
		return "admin"
	}
	return "config"
}

// CommitCapsSettings merges a delta onto the base (the saved record, else the
// configuration), validates, checks the expected version and writes the record
// and its audit row in ONE transaction. The cache changes only after that
// transaction committed, so a failing store leaves the effective caps exactly as
// they were (R16 F03). The whole sequence runs under capsMu, so two concurrent
// partial writes keep each other's field (R11-D1). A stale expectedVersion is
// refused with ErrSettingsConflict before the merged record is validated, so a
// stale invalid write answers 409 rather than 400 (R11-D2). A nil
// expectedVersion writes unconditionally.
//
// The caller applies the returned record to the runtime after this returns,
// never before (R16 F03).
func (e *Engine) CommitCapsSettings(ctx context.Context, delta CapsSettingsDelta, expectedVersion *int, actor string) (CapsSettings, error) {
	e.capsMu.Lock()
	defer e.capsMu.Unlock()
	if expectedVersion != nil && *expectedVersion != e.capsSettings.Version {
		return CapsSettings{}, ErrSettingsConflict
	}
	next := delta.applyOn(capsSettingsFromConfig(e.capsBase()))
	if err := next.validate(); err != nil {
		return CapsSettings{}, err
	}
	next.Version = e.capsSettings.Version + 1
	effective, err := caps.Resolve(next.Config())
	if err != nil {
		return CapsSettings{}, fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	key := "caps-change:" + e.ServerID + ":" + time.Now().UTC().Format(time.RFC3339Nano)
	event := map[string]any{
		"type":           "caps.change",
		"actor":          actor,
		"maxProcs":       effective.MaxProcs,
		"memLimitMB":     effective.MemLimitMB,
		"workerPool":     effective.WorkerPool,
		"dbMaxOpenConns": effective.DBMaxOpenConns,
		"softLimit":      effective.SoftLimit,
		"version":        next.Version,
		"pendingRestart": capsPendingRestart,
		"timestamp":      time.Now().UTC(),
	}
	if err := e.Store.Update(ctx, func(tx store.Tx) error {
		if err := tx.Put(store.Metrics, capsSettingsKey, next); err != nil {
			return err
		}
		return tx.Put(store.Audit, key, event)
	}); err != nil {
		return CapsSettings{}, err
	}
	e.capsSettings = next
	e.capsSettingsSaved = true
	return next, nil
}

// SetAppliedCaps records the effective caps the runtime carries after a write,
// so the admin view never reports a stored value the process did not apply.
func (e *Engine) SetAppliedCaps(c caps.Caps, version int) {
	e.capsMu.Lock()
	e.appliedCaps = c
	e.appliedVersion = version
	e.capsMu.Unlock()
}

// AppliedCaps returns the effective caps the runtime carries and the version of
// the record they came from.
func (e *Engine) AppliedCaps() (caps.Caps, int) {
	e.capsMu.Lock()
	defer e.capsMu.Unlock()
	return e.appliedCaps, e.appliedVersion
}

// loadCapsSettings restores the durable admin record. A missing record keeps the
// configuration file; an unreadable or invalid record fails startup closed, the
// same rule the pool settings follow.
func (e *Engine) loadCapsSettings() error {
	var s CapsSettings
	err := e.Store.View(context.Background(), func(r store.Reader) error {
		return r.Get(store.Metrics, capsSettingsKey, &s)
	})
	if errors.Is(err, store.ErrNotFound) {
		e.capsMu.Lock()
		e.capsSettings = CapsSettings{}
		e.capsSettingsSaved = false
		e.capsMu.Unlock()
		return nil
	}
	if err != nil {
		return err
	}
	if err = s.validate(); err != nil {
		return fmt.Errorf("%w: stored caps settings", err)
	}
	e.capsMu.Lock()
	e.capsSettings = s
	e.capsSettingsSaved = true
	e.capsMu.Unlock()
	return nil
}
