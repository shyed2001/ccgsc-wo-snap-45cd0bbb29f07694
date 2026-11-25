// Path: internal/verify/verifier.go | Author: Hermes (MiniMax) for the owner | 2026-11-17
// What/why: one facade that wires the four tamper measures of WWPP-R37.1 (hash check,
// re-run %, signed envelope, canary + reputation) behind one configurable surface;
// every measure is on/off switchable by the admin pages (WWPP-R37.1b).
package verify

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Config holds the on/off switch and the parameters of every tamper measure. The admin
// pages map one JSON blob onto this struct (WWPP-R37.1b: all four measures configurable).
type Config struct {
	HashCheckEnabled  bool             `json:"hashCheckEnabled"`
	RerunEnabled      bool             `json:"rerunEnabled"`
	RerunPercent      float64          `json:"rerunPercent"`    // developer requested rate
	RerunMaxPercent   float64          `json:"rerunMaxPercent"` // admin ceiling
	EnvelopeEnabled   bool             `json:"envelopeEnabled"`
	CanaryEnabled     bool             `json:"canaryEnabled"`
	ReputationEnabled bool             `json:"reputationEnabled"`
	Reputation        ReputationConfig `json:"reputation"`
}

// DefaultConfig returns the default measure configuration: hash check and envelope on,
// re-run at 1 % (developer-led, WWPP-R35.1), canaries and reputation on.
func DefaultConfig() Config {
	return Config{
		HashCheckEnabled:  true,
		RerunEnabled:      true,
		RerunPercent:      1,
		RerunMaxPercent:   100,
		EnvelopeEnabled:   true,
		CanaryEnabled:     true,
		ReputationEnabled: true,
		Reputation:        DefaultReputationConfig(),
	}
}

// VerifyResult is the outcome of one verification of a task result.
type VerifyResult struct {
	HashOK       bool   `json:"hashOk"`
	HashExpected string `json:"hashExpected,omitempty"`
	HashActual   string `json:"hashActual,omitempty"`
	Rerun        bool   `json:"rerun"`
	EnvelopeOK   bool   `json:"envelopeOk"`
	CanaryID     string `json:"canaryId,omitempty"`
	CanaryOK     bool   `json:"canaryOk"`
	Reputation   Status `json:"reputation,omitempty"`
	Banned       bool   `json:"banned"`
	Verdict      string `json:"verdict"` // "accepted", "rejected" or "warn"
	Reason       string `json:"reason,omitempty"`
	DurationNS   int64  `json:"durationNs"`
}

// Verdict values.
const (
	VerdictAccepted   = "accepted"
	VerdictRejected   = "rejected"
	VerdictWarn       = "warn"
	VerdictUnverified = "unverified"
)

// Verifier ties the four tamper measures together. It is safe for concurrent use.
type Verifier struct {
	mu       sync.RWMutex
	cfg      Config
	keyring  *Keyring
	canaries *CanaryRegistry
	rep      *Tracker
	sampler  *RerunSampler
	metrics  *Metrics
	now      func() time.Time
}

// NewVerifier builds a verifier with the default configuration and a fresh key ring.
func NewVerifier(m *Metrics) *Verifier {
	cfg := DefaultConfig()
	return &Verifier{
		cfg:      cfg,
		keyring:  NewKeyring(),
		canaries: NewCanaryRegistry(m),
		rep:      NewReputationWithConfig(cfg.Reputation, m),
		sampler:  NewRerunSampler(m),
		metrics:  m,
		now:      time.Now,
	}
}

// Config returns the active configuration.
func (v *Verifier) Config() Config {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.cfg
}

// SetConfig replaces the configuration and propagates the reputation thresholds.
func (v *Verifier) SetConfig(c Config) {
	v.mu.Lock()
	v.cfg = c
	v.mu.Unlock()
	v.rep.SetConfig(c.Reputation)
}

// Keyring exposes the trusted key ring (key rotation by keyId).
func (v *Verifier) Keyring() *Keyring { return v.keyring }

// Canaries exposes the canary registry.
func (v *Verifier) Canaries() *CanaryRegistry { return v.canaries }

// Reputation exposes the reputation tracker.
func (v *Verifier) Reputation() *Tracker { return v.rep }

// Sampler exposes the re-run sampler.
func (v *Verifier) Sampler() *RerunSampler { return v.sampler }

// Metrics exposes the registry the verifier writes to.
func (v *Verifier) Metrics() *Metrics { return v.metrics }

// SetClock overrides the clock (tests stay deterministic). The clock drives the envelope
// time checks and the duration measurements; a private metrics registry follows it.
func (v *Verifier) SetClock(now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	v.mu.Lock()
	v.now = now
	v.mu.Unlock()
	if v.metrics != nil && v.metrics != defaultMetrics {
		v.metrics.SetClock(now)
	}
}

func (v *Verifier) clock() func() time.Time {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.now
}

// CheckArtifact runs the hash check of an artifact or cached blob.
func (v *Verifier) CheckArtifact(data []byte, expectedSHA string) error {
	if !v.Config().HashCheckEnabled {
		return nil
	}
	return CheckTimed(data, expectedSHA, v.metrics)
}

// ShouldRerun applies the admin ceiling to the developer requested re-run percentage and
// draws the deterministic decision for the task.
func (v *Verifier) ShouldRerun(taskID string, requested float64) bool {
	cfg := v.Config()
	if !cfg.RerunEnabled {
		return false
	}
	pct := cfg.EffectiveRerunPercent(requested)
	return v.sampler.ShouldRerun(taskID, pct)
}

// ShouldRerunWithKey applies the admin ceiling to a keyed sampling decision.
func (v *Verifier) ShouldRerunWithKey(key []byte, taskID string, requested float64) bool {
	cfg := v.Config()
	if !cfg.RerunEnabled {
		return false
	}
	pct := cfg.EffectiveRerunPercent(requested)
	return v.sampler.ShouldRerunWithKey(key, taskID, pct)
}

// SignEnvelope signs a task envelope with the key bound to keyID.
func (v *Verifier) SignEnvelope(t TaskEnvelope, priv ed25519.PrivateKey, keyID string) (SignedTaskEnvelope, error) {
	return SignTaskEnvelope(t, priv, keyID)
}

// VerifyEnvelope verifies a signed task envelope against the key ring.
func (v *Verifier) VerifyEnvelope(s SignedTaskEnvelope) error {
	return VerifyTaskEnvelope(s, v.keyring, v.clock()())
}

// CheckCanary runs a canary check and feeds the reputation tracker when enabled.
func (v *Verifier) CheckCanary(workerID, canaryID string, answer []byte) (CanaryResult, error) {
	cfg := v.Config()
	if !cfg.CanaryEnabled {
		return CanaryResult{CanaryID: canaryID, Pass: true, Reason: "canaries disabled", At: v.clock()().UTC()}, nil
	}
	res := v.canaries.CheckCanary(canaryID, answer)
	if cfg.ReputationEnabled && workerID != "" {
		kind := EventPass
		if !res.Pass {
			kind = EventCanaryFail
		}
		if _, err := v.rep.Observe(workerID, kind, "", res.Reason); err != nil && err != ErrBannedWorker {
			return res, err
		}
	}
	return res, nil
}

// ObserveResult records a task outcome in the reputation tracker.
func (v *Verifier) ObserveResult(workerID string, ok bool, timedOut bool) error {
	if !v.Config().ReputationEnabled || workerID == "" {
		return nil
	}
	kind := EventPass
	switch {
	case timedOut:
		kind = EventTimeout
	case !ok:
		kind = EventFail
	}
	_, err := v.rep.Observe(workerID, kind, "", "")
	if err == ErrBannedWorker {
		return nil
	}
	return err
}

// Verify runs the full measure set for one task result and returns the verdict.
func (v *Verifier) Verify(ctx context.Context, req VerifyRequest) VerifyResult {
	clock := v.clock()
	start := clock()
	cfg := v.Config()
	res := VerifyResult{HashOK: false, EnvelopeOK: true}
	if req.ExpectedSHA == "" {
		res.HashOK = false
		res.Verdict = VerdictUnverified
		res.Reason = "expected artifact hash is required"
	} else {
		res.HashExpected = req.ExpectedSHA
		res.HashActual = SHA256(req.Artifact)
		res.HashOK = res.HashExpected == res.HashActual
		if !res.HashOK && cfg.HashCheckEnabled {
			res.Verdict = VerdictRejected
			res.Reason = "artifact hash mismatch"
			res.DurationNS = clock().Sub(start).Nanoseconds()
			return res
		}
	}
	if len(req.RerunKey) > 0 {
		res.Rerun = v.ShouldRerunWithKey(req.RerunKey, req.TaskID, req.RerunPercent)
	}
	if cfg.EnvelopeEnabled || len(req.Envelope.Payload) != 0 {
		if err := v.VerifyEnvelope(req.Envelope); err != nil {
			res.EnvelopeOK = false
			if cfg.EnvelopeEnabled {
				res.Verdict = VerdictRejected
				res.Reason = "envelope: " + err.Error()
				res.DurationNS = clock().Sub(start).Nanoseconds()
				return res
			}
		}
	}
	if req.CanaryID != "" {
		res.CanaryID = req.CanaryID
		cr, err := v.CheckCanary(req.WorkerID, req.CanaryID, req.CanaryAnswer)
		res.CanaryOK = cr.Pass
		if err != nil {
			res.Reason = "canary: " + err.Error()
		}
		if !cr.Pass && cfg.CanaryEnabled {
			res.Verdict = VerdictRejected
			res.Reason = "canary failed"
			res.DurationNS = clock().Sub(start).Nanoseconds()
			return res
		}
	}
	if req.WorkerID != "" {
		if st, err := v.rep.StatusOf(req.WorkerID); err == nil {
			res.Reputation = st
			res.Banned = st == StatusBan
		}
	}
	switch {
	case res.Verdict != "" && res.Verdict != VerdictUnverified:
	case res.Banned:
		res.Verdict = VerdictRejected
		res.Reason = "worker banned"
	case res.Reputation == StatusBar:
		res.Verdict = VerdictRejected
		res.Reason = "worker reputation bar"
	case res.Reputation == StatusWarn:
		res.Verdict = VerdictWarn
		res.Reason = "worker reputation " + string(res.Reputation)
	case res.Verdict == VerdictUnverified:
	default:
		res.Verdict = VerdictAccepted
	}
	res.DurationNS = clock().Sub(start).Nanoseconds()
	return res
}

// VerifyRequest carries the inputs of one verification.
type VerifyRequest struct {
	TaskID       string
	WorkerID     string
	Artifact     []byte
	ExpectedSHA  string
	RerunPercent float64
	RerunKey     []byte
	Envelope     SignedTaskEnvelope
	CanaryID     string
	CanaryAnswer []byte
}

// Inject satisfies the legacy CanaryInjector seam for the canary registry.
func (r *CanaryRegistry) Inject(ctx context.Context, id string) (json.RawMessage, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return r.CanaryTask(id)
}

// InjectWithEnvelope returns the canary task body together with the signed envelope, so
// a canary travels the same path as a normal assignment (R39.1).
func (v *Verifier) InjectWithEnvelope(ctx context.Context, id, jobID string, priv ed25519.PrivateKey, keyID string) (json.RawMessage, SignedTaskEnvelope, error) {
	body, err := v.canaries.Inject(ctx, id)
	if err != nil {
		return nil, SignedTaskEnvelope{}, err
	}
	c, err := v.canaries.Get(id)
	if err != nil {
		return nil, SignedTaskEnvelope{}, err
	}
	t := NewTaskEnvelope(jobID, c.TaskID, c.ArtifactSHA256, CanaryDigest(c.Input), keyID, v.clock()(), defaultEnvelopeTTL)
	env, err := v.SignEnvelope(t, priv, keyID)
	if err != nil {
		return nil, SignedTaskEnvelope{}, err
	}
	return body, env, nil
}

// RerunPercentOnly reports whether re-run sampling is enabled at all.
func (c Config) RerunPercentOnly() bool { return c.RerunEnabled }

// EffectiveRerunPercent returns the rate that is actually applied after the admin ceiling.
func (c Config) EffectiveRerunPercent(requested float64) float64 {
	pct := requested
	if pct > c.RerunMaxPercent {
		pct = c.RerunMaxPercent
	}
	if pct < 0 {
		pct = 0
	}
	return pct
}

// String renders the configuration for logs.
func (c Config) String() string {
	return fmt.Sprintf("hash=%v rerun=%v(%.2f/%.2f) envelope=%v canary=%v reputation=%v",
		c.HashCheckEnabled, c.RerunEnabled, c.RerunPercent, c.RerunMaxPercent,
		c.EnvelopeEnabled, c.CanaryEnabled, c.ReputationEnabled)
}
