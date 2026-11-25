// Path: internal/verify/envelope.go | Author: Hermes (MiniMax) for the owner | 2026-11-17
// What/why: signed task envelopes (Ed25519 over canonical JSON) plus a key ring with
// keyId rotation, overlap and revocation; WWPP-R37.1, R39.1 (signature/key protocol).
package verify

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Domain separator for task envelopes (R39.1: domain separated, versioned envelopes).
const (
	EnvelopeDomain     = "ccgsc.pools.task-envelope.v1"
	EnvelopeVersion    = 1
	EnvelopeSignature  = "ed25519"
	keyIDLength        = 16
	defaultEnvelopeTTL = 15 * time.Minute
	// maxEnvelopeTTL bounds ExpiresAt-IssuedAt. It tracks the largest lease a pool
	// may be configured with (internal/config Config.LeaseSeconds <= 86400 s), so a
	// leaked signing path cannot mint an envelope that outlives any legal lease
	// while every legal lease still signs (R10 q4).
	maxEnvelopeTTL  = 24 * time.Hour
	maxEnvelopeSkew = 2 * time.Minute
)

// MaxWorkloadFiles is the most files one workload may carry (R11-G2, D-W10-8):
// the entry module plus its data/.wasm companions. The client, the signer and
// job validation all enforce this one number.
const MaxWorkloadFiles = 8

// Hostile-workload isolation modes (D-W10-6). The mode is a Director setting and
// travels inside the signed task envelope, so a tab executes only the isolation
// the operator selected for the pool.
const (
	IsolationSeparateLock = "separate+lock"
	IsolationLockOnly     = "lock-only"
)

// The two platform workload kinds of D-W10-10. A kind is ABI, never a workload
// identity: every language that targets wasm32 uses WorkloadKindWasmABI, and
// WorkloadKindJSModule is the W10c ES-module contract (kept because WebGPU
// cannot be reached from WASM).
const (
	WorkloadKindJSModule = "js-module"
	WorkloadKindWasmABI  = "wasm-abi"
)

// Errors returned by the envelope and key ring API.
var (
	ErrUnknownKey    = errors.New("verify: unknown key id")
	ErrRevokedKey    = errors.New("verify: revoked key id")
	ErrBadSignature  = errors.New("verify: bad signature")
	ErrExpired       = errors.New("verify: envelope expired")
	ErrNotYetValid   = errors.New("verify: envelope not yet valid")
	ErrInvalidEnv    = errors.New("verify: invalid envelope")
	ErrKeyIDMismatch = errors.New("verify: key id mismatch")
	ErrNotCanonical  = errors.New("verify: payload is not canonical JSON")
)

// WorkloadFile is one file in the server-signed workload descriptor (D-W10-3).
// Manifest.json is a BUILD AID only; no separate manifest signing key is used.
type WorkloadFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// WorkloadDescriptor is authenticated by the same task operator signature.
//
// Kind is the platform ABI the entry module is held to (D-W10-10). It is part
// of the SIGNED payload, because the ABI decides how the bytes are executed:
// a job must not be able to present a js-module under the byte ABI or the other
// way round. An empty Kind means "js-module", the W10c default, so an envelope
// signed before this field existed stays valid and byte-identical.
type WorkloadDescriptor struct {
	ID      string         `json:"id"`
	Version int            `json:"version"`
	Kind    string         `json:"kind,omitempty"`
	Files   []WorkloadFile `json:"files"`
}

// TaskEnvelope is the signed payload of a task assignment (WWPP-R37.1).
//
// Field order in the struct is irrelevant: the signed bytes are canonical JSON with
// sorted keys, so the signature covers exactly one byte sequence.
type TaskEnvelope struct {
	Version        int                 `json:"version"`
	JobID          string              `json:"jobId"`
	TaskID         string              `json:"taskId"`
	ArtifactSHA256 string              `json:"artifactSha256"`
	InputSHA256    string              `json:"inputSha256"`
	Workload       *WorkloadDescriptor `json:"workload,omitempty"`
	LeaseToken     string              `json:"leaseToken,omitempty"`
	WorkerID       string              `json:"workerId,omitempty"`
	IssuedAt       int64               `json:"issuedAt"` // unix seconds
	ExpiresAt      int64               `json:"expiresAt"`
	KeyID          string              `json:"keyId"`
	// IsolationMode is the Director selected hostile-workload isolation of the
	// pool (D-W10-6: separate+lock or lock-only), covered by the same signature.
	IsolationMode string `json:"isolationMode,omitempty"`
}

// Valid reports whether the envelope has the minimum fields required for signing.
func (t TaskEnvelope) Valid() error {
	if t.JobID == "" || t.TaskID == "" {
		return fmt.Errorf("%w: jobId and taskId are required", ErrInvalidEnv)
	}
	if t.ArtifactSHA256 == "" || t.InputSHA256 == "" {
		return fmt.Errorf("%w: artifactSha256 and inputSha256 are required", ErrInvalidEnv)
	}
	// R10 q3: the two digests are the binding of the signature to the artifact and
	// to the input, so they must be the same lowercase 64-hex shape the workload
	// file pins already are. An arbitrary string would sign and verify just as well
	// while binding nothing.
	if !lowerHexDigest(t.ArtifactSHA256) || !lowerHexDigest(t.InputSHA256) {
		return fmt.Errorf("%w: artifactSha256 and inputSha256 must be lowercase 64-hex", ErrInvalidEnv)
	}
	if t.KeyID == "" {
		return fmt.Errorf("%w: keyId is required", ErrInvalidEnv)
	}
	if t.ExpiresAt <= t.IssuedAt {
		return fmt.Errorf("%w: expiresAt must be after issuedAt", ErrInvalidEnv)
	}
	// R10 q4: an envelope is a lease credential, so its window is bounded by the
	// longest lease a pool may be configured with.
	if t.ExpiresAt-t.IssuedAt > int64(maxEnvelopeTTL/time.Second) {
		return fmt.Errorf("%w: expiresAt-issuedAt exceeds the maximum envelope ttl", ErrInvalidEnv)
	}
	if t.IsolationMode != "" && t.IsolationMode != IsolationSeparateLock && t.IsolationMode != IsolationLockOnly {
		return fmt.Errorf("%w: unknown isolationMode %q", ErrInvalidEnv, t.IsolationMode)
	}
	if t.Workload != nil {
		w := t.Workload
		if w.ID == "" || w.Version < 1 || len(w.Files) == 0 {
			return fmt.Errorf("%w: invalid workload identity or empty file list", ErrInvalidEnv)
		}
		// R11-G2: a workload is at most MaxWorkloadFiles files (entry plus
		// companions). The cap is part of the signed schema, so an over-cap
		// descriptor can never be signed and can never verify.
		if len(w.Files) > MaxWorkloadFiles {
			return fmt.Errorf("%w: workload has %d files, at most %d are allowed", ErrInvalidEnv, len(w.Files), MaxWorkloadFiles)
		}
		// D-W10-10: the ABI kind is signed. Only the two platform kinds exist;
		// an unknown spelling must never fall back to a runnable default.
		if w.Kind != "" && w.Kind != WorkloadKindJSModule && w.Kind != WorkloadKindWasmABI {
			return fmt.Errorf("%w: unknown workload kind %q", ErrInvalidEnv, w.Kind)
		}
		seen := make(map[string]struct{}, len(w.Files))
		for _, f := range w.Files {
			if f.Path == "" || len(f.SHA256) != 64 || strings.ToLower(f.SHA256) != f.SHA256 {
				return fmt.Errorf("%w: invalid workload file metadata", ErrInvalidEnv)
			}
			if _, err := hex.DecodeString(f.SHA256); err != nil {
				return fmt.Errorf("%w: invalid workload file digest", ErrInvalidEnv)
			}
			if _, duplicate := seen[f.Path]; duplicate {
				return fmt.Errorf("%w: duplicate workload file path", ErrInvalidEnv)
			}
			seen[f.Path] = struct{}{}
		}
	}
	return nil
}

// CanonicalBytes returns the exact bytes that are signed: a domain separator followed by
// the canonical JSON encoding of the envelope.
func (t TaskEnvelope) CanonicalBytes() ([]byte, error) {
	p, err := t.canonicalPayload()
	if err != nil {
		return nil, err
	}
	return append([]byte(EnvelopeDomain+"\x00"), p...), nil
}

// canonicalPayload returns the canonical JSON encoding of the envelope (sorted keys,
// no spaces). These exact bytes are the payload of a signed envelope.
func (t TaskEnvelope) canonicalPayload() ([]byte, error) {
	if t.Version == 0 {
		t.Version = EnvelopeVersion
	}
	return CanonicalJSON(t)
}

// SignedTaskEnvelope carries the canonical payload, the raw signature and the key id used.
// The signature covers the domain separator plus the canonical payload bytes.
type SignedTaskEnvelope struct {
	Payload   json.RawMessage `json:"payload"`
	Signature []byte          `json:"signature"`
	KeyID     string          `json:"keyId"`
	Algorithm string          `json:"algorithm"`
}

// SignTaskEnvelope signs the envelope with the private key bound to keyID.
func SignTaskEnvelope(t TaskEnvelope, priv ed25519.PrivateKey, keyID string) (SignedTaskEnvelope, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return SignedTaskEnvelope{}, fmt.Errorf("%w: bad private key size %d", ErrInvalidEnv, len(priv))
	}
	if t.Version == 0 {
		t.Version = EnvelopeVersion
	}
	t.KeyID = keyID
	if err := t.Valid(); err != nil {
		return SignedTaskEnvelope{}, err
	}
	payload, err := CanonicalJSON(t)
	if err != nil {
		return SignedTaskEnvelope{}, err
	}
	signing, err := t.CanonicalBytes()
	if err != nil {
		return SignedTaskEnvelope{}, err
	}
	return SignedTaskEnvelope{
		Payload:   json.RawMessage(payload),
		Signature: ed25519.Sign(priv, signing),
		KeyID:     keyID,
		Algorithm: EnvelopeSignature,
	}, nil
}

// VerifyTaskEnvelope verifies the signature and the time bounds of a signed envelope
// against a key ring. Unknown, revoked and mismatched key ids fail closed (R39.1).
// The clock is supplied by the caller so tests stay deterministic.
func VerifyTaskEnvelope(s SignedTaskEnvelope, ring *Keyring, now time.Time) error {
	return verifyTaskEnvelope(s, ring, now, false)
}

// VerifyTaskEnvelopeAllowExpired verifies the signature, canonical binding, key
// status and issuance time while allowing an already-expired envelope. Callers
// must separately prove that the expired lease is the recorded eligible lease.
func VerifyTaskEnvelopeAllowExpired(s SignedTaskEnvelope, ring *Keyring, now time.Time) error {
	return verifyTaskEnvelope(s, ring, now, true)
}

func verifyTaskEnvelope(s SignedTaskEnvelope, ring *Keyring, now time.Time, allowExpired bool) error {
	if s.Algorithm != "" && s.Algorithm != EnvelopeSignature {
		return fmt.Errorf("%w: unsupported algorithm %q", ErrInvalidEnv, s.Algorithm)
	}
	if len(s.Payload) == 0 || len(s.Signature) != ed25519.SignatureSize {
		return fmt.Errorf("%w: missing payload or signature", ErrInvalidEnv)
	}
	pub, err := ring.PublicKeyAt(s.KeyID, now)
	if err != nil {
		return err
	}
	var t TaskEnvelope
	if err := json.Unmarshal(s.Payload, &t); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidEnv, err)
	}
	if t.KeyID != s.KeyID {
		return fmt.Errorf("%w: payload keyId %q != envelope keyId %q", ErrKeyIDMismatch, t.KeyID, s.KeyID)
	}
	if err := t.Valid(); err != nil {
		return err
	}
	if t.Version != 0 && t.Version != EnvelopeVersion {
		return fmt.Errorf("%w: unsupported version %d", ErrInvalidEnv, t.Version)
	}
	// The payload must be exactly the canonical encoding, otherwise one signed envelope
	// could be re-encoded into a second valid byte sequence.
	canonical, err := t.canonicalPayload()
	if err != nil {
		return err
	}
	if !jsonBytesEqual(canonical, s.Payload) {
		return fmt.Errorf("%w: payload is not canonical", ErrNotCanonical)
	}
	signing, err := t.CanonicalBytes()
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, signing, s.Signature) {
		return ErrBadSignature
	}
	if t.ExpiresAt <= t.IssuedAt {
		return fmt.Errorf("%w: expiresAt must be after issuedAt", ErrInvalidEnv)
	}
	issued := time.Unix(t.IssuedAt, 0).UTC()
	expires := time.Unix(t.ExpiresAt, 0).UTC()
	if now.Before(issued.Add(-maxEnvelopeSkew)) {
		return fmt.Errorf("%w: issued at %s", ErrNotYetValid, issued.Format(time.RFC3339))
	}
	if now.After(expires) && !allowExpired {
		return fmt.Errorf("%w: expired at %s", ErrExpired, expires.Format(time.RFC3339))
	}
	return nil
}

// DecodeTaskEnvelope decodes the canonical payload of a signed envelope.
func DecodeTaskEnvelope(s SignedTaskEnvelope) (TaskEnvelope, error) {
	var t TaskEnvelope
	if err := json.Unmarshal(s.Payload, &t); err != nil {
		return TaskEnvelope{}, fmt.Errorf("%w: %v", ErrInvalidEnv, err)
	}
	return t, nil
}

// NewTaskEnvelope builds an envelope with sensible time bounds (now .. now+ttl).
func NewTaskEnvelope(jobID, taskID, artifactSHA256, inputSHA256, keyID string, now time.Time, ttl time.Duration) TaskEnvelope {
	if ttl < time.Second {
		ttl = time.Second
	}
	return TaskEnvelope{
		Version:        EnvelopeVersion,
		JobID:          jobID,
		TaskID:         taskID,
		ArtifactSHA256: artifactSHA256,
		InputSHA256:    inputSHA256,
		IssuedAt:       now.UTC().Unix(),
		ExpiresAt:      now.UTC().Add(ttl).Unix(),
		KeyID:          keyID,
	}
}

// Keyring holds the trusted public keys by key id and supports rotation with overlap,
// revocation and expiry of the retired key (R39.1).
type Keyring struct {
	mu   sync.RWMutex
	keys map[string]keyEntry
}

// KeyringEntry is the durable form of one trusted public key and its rotation
// state.
type KeyringEntry struct {
	KeyID     string    `json:"keyId"`
	PublicKey []byte    `json:"publicKey"`
	Revoked   bool      `json:"revoked,omitempty"`
	Retire    time.Time `json:"retire,omitempty"`
}

type keyEntry struct {
	pub     ed25519.PublicKey
	revoked bool
	retire  time.Time // zero = never retires
}

// NewKeyring returns an empty key ring.
func NewKeyring() *Keyring { return &Keyring{keys: make(map[string]keyEntry)} }

// KeyID derives the default key id of a public key (first 16 hex chars of its SHA-256).
func KeyID(pub ed25519.PublicKey) string { return SHA256(pub)[:keyIDLength] }

// Add registers a public key under keyID. Re-adding the same key id replaces the key,
// which is how a rotation publishes the successor key.
//
// R10 q5: the id must be the id of that key. Restore already enforced the rule, so
// Add was the one way to publish a public key under a foreign id -- and then every
// envelope signed by it carries a key id that identifies another key.
func (k *Keyring) Add(keyID string, pub ed25519.PublicKey) error {
	if keyID == "" {
		return fmt.Errorf("%w: empty key id", ErrInvalidEnv)
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: bad public key size %d", ErrInvalidEnv, len(pub))
	}
	if KeyID(pub) != keyID {
		return fmt.Errorf("%w: public key does not match key id %q", ErrKeyIDMismatch, keyID)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.keys == nil {
		k.keys = make(map[string]keyEntry)
	}
	k.keys[keyID] = keyEntry{pub: append(ed25519.PublicKey(nil), pub...)}
	return nil
}

// Retire keeps the key valid until at (overlap window) and then removes it.
func (k *Keyring) Retire(keyID string, at time.Time) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	e, ok := k.keys[keyID]
	if !ok {
		return ErrUnknownKey
	}
	e.retire = at.UTC()
	k.keys[keyID] = e
	return nil
}

// Revoke removes a key id immediately; it fails closed on every later verification.
func (k *Keyring) Revoke(keyID string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	e, ok := k.keys[keyID]
	if !ok {
		return ErrUnknownKey
	}
	e.revoked = true
	e.retire = time.Time{}
	k.keys[keyID] = e
	return nil
}

// PublicKey returns the trusted public key of keyID, honouring revocation and retirement.
func (k *Keyring) PublicKey(keyID string) (ed25519.PublicKey, error) {
	return k.PublicKeyAt(keyID, time.Now())
}

// PublicKeyAt applies retirement using the verification clock.
func (k *Keyring) PublicKeyAt(keyID string, now time.Time) (ed25519.PublicKey, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	e, ok := k.keys[keyID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownKey, keyID)
	}
	if e.revoked {
		return nil, fmt.Errorf("%w: %s", ErrRevokedKey, keyID)
	}
	if !e.retire.IsZero() && !now.Before(e.retire) {
		return nil, fmt.Errorf("%w: %s retired", ErrUnknownKey, keyID)
	}
	return append(ed25519.PublicKey(nil), e.pub...), nil
}

// KeyIDs lists the known key ids in sorted order.
func (k *Keyring) KeyIDs() []string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	out := make([]string, 0, len(k.keys))
	for id := range k.keys {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Len returns the number of known key ids.
func (k *Keyring) Len() int {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return len(k.keys)
}

// Snapshot returns all keyring entries, including retired and revoked keys, so
// rotation state can survive a process restart.
func (k *Keyring) Snapshot() []KeyringEntry {
	k.mu.RLock()
	defer k.mu.RUnlock()
	out := make([]KeyringEntry, 0, len(k.keys))
	for id, entry := range k.keys {
		out = append(out, KeyringEntry{
			KeyID:     id,
			PublicKey: append([]byte(nil), entry.pub...),
			Revoked:   entry.revoked,
			Retire:    entry.retire,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].KeyID < out[j].KeyID })
	return out
}

// Restore replaces the keyring with durable entries.
func (k *Keyring) Restore(entries []KeyringEntry) error {
	keys := make(map[string]keyEntry, len(entries))
	for _, entry := range entries {
		if entry.KeyID == "" || len(entry.PublicKey) != ed25519.PublicKeySize {
			return fmt.Errorf("%w: invalid persisted keyring entry", ErrInvalidEnv)
		}
		pub := ed25519.PublicKey(append([]byte(nil), entry.PublicKey...))
		if KeyID(pub) != entry.KeyID {
			return fmt.Errorf("%w: persisted keyring key id mismatch", ErrInvalidEnv)
		}
		keys[entry.KeyID] = keyEntry{pub: pub, revoked: entry.Revoked, retire: entry.Retire.UTC()}
	}
	k.mu.Lock()
	k.keys = keys
	k.mu.Unlock()
	return nil
}

// EncodeSignature returns the base64 form of a raw signature (wire friendly).
func EncodeSignature(sig []byte) string { return base64.StdEncoding.EncodeToString(sig) }

// DecodeSignature parses a base64 signature.
func DecodeSignature(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(strings.TrimSpace(s))
}

// jsonBytesEqual compares two byte slices without allocating.
func jsonBytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// lowerHexDigest reports whether v is a lowercase 64-character hex digest.
func lowerHexDigest(v string) bool {
	if len(v) != 2*sha256.Size || strings.ToLower(v) != v {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}
