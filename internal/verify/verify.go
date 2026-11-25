// Path: internal/verify/verify.go | Author: Codex for Claude Opus 5.5 | 2026-11-17
// What/why: hash and signed-envelope primitives plus verification seams; WWPP-R35.1, R37.1.
// Extended 2026-11-17 by Hermes (MiniMax): re-run sampling, canaries, reputation, metrics.
package verify

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// SHA256 returns the hex encoded SHA-256 digest of b.
func SHA256(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// SHA256Bytes returns the raw SHA-256 digest of b.
func SHA256Bytes(b []byte) [32]byte { return sha256.Sum256(b) }

// Check compares the SHA-256 digest of b with the expected hex digest. It is the hash
// check that runs on every artifact load, including IndexedDB cache hits (WWPP-R37.1).
func Check(b []byte, want string) error {
	if SHA256(b) != want {
		return errors.New("artifact hash mismatch")
	}
	return nil
}

// CheckTimed is Check plus a metrics observation of the duration and the outcome.
func CheckTimed(b []byte, want string, m *Metrics) error {
	start := startOn(m)
	err := Check(b, want)
	recordOn(m, OpHash, start, err == nil)
	return err
}

// Envelope is the legacy wire envelope: payload, raw signature, embedded public key and
// the key id. Kept for compatibility with internal/pool and tests/core_test.go.
type Envelope struct {
	Payload   json.RawMessage `json:"payload"`
	Signature []byte          `json:"signature"`
	PublicKey []byte          `json:"publicKey"`
	KeyID     string          `json:"keyId"`
}

// Signer holds an Ed25519 key pair and signs arbitrary values.
type Signer struct {
	private ed25519.PrivateKey
	Public  ed25519.PublicKey
}

// NewSigner generates a fresh Ed25519 key pair.
func NewSigner() (*Signer, error) {
	pub, priv, e := ed25519.GenerateKey(rand.Reader)
	return &Signer{priv, pub}, e
}

// NewSignerFromKey rebuilds a signer from a stored private key.
func NewSignerFromKey(priv ed25519.PrivateKey) (*Signer, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, errors.New("verify: bad private key size")
	}
	return &Signer{priv, priv.Public().(ed25519.PublicKey)}, nil
}

// Sign marshals v as JSON and signs the bytes.
func (s *Signer) Sign(v any) (Envelope, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return Envelope{}, e
	}
	return Envelope{b, ed25519.Sign(s.private, b), s.Public, SHA256(s.Public)[:16]}, nil
}

// SignTimed is Sign plus a metrics observation of the duration and the outcome.
func (s *Signer) SignTimed(v any, m *Metrics) (Envelope, error) {
	start := startOn(m)
	e, err := s.Sign(v)
	recordOn(m, OpEnvelope, start, err == nil)
	return e, err
}

// VerifyEnvelope requires an independently trusted public key; never trust the embedded key.
func VerifyEnvelope(e Envelope, trusted ed25519.PublicKey) bool {
	return len(trusted) == ed25519.PublicKeySize && ed25519.Verify(trusted, e.Payload, e.Signature)
}

// VerifyEnvelopeTimed is VerifyEnvelope plus a metrics observation.
func VerifyEnvelopeTimed(e Envelope, trusted ed25519.PublicKey, m *Metrics) bool {
	start := startOn(m)
	ok := VerifyEnvelope(e, trusted)
	recordOn(m, OpEnvelope, start, ok)
	return ok
}

// Sampler draws the re-run decision for a task.
type Sampler interface {
	ShouldRerun(taskID string, percent float64) bool
}

// HashSampler is the stateless, deterministic sampler of task id hashes.
type HashSampler struct{}

// ShouldRerun reports whether the task is inside the re-run percentage.
func (HashSampler) ShouldRerun(id string, p float64) bool { return ShouldRerun(id, p) }

// CanaryInjector injects a scheduler owned canary task.
type CanaryInjector interface {
	Inject(context.Context, string) (json.RawMessage, error)
}

// Reputation is the seam other packages use to observe worker outcomes.
type Reputation interface {
	Observe(context.Context, string, bool) error
	Score(context.Context, string) (float64, error)
}
