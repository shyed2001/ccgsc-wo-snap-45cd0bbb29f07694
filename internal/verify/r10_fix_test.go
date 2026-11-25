// Path: internal/verify/r10_fix_test.go | R10 fix lane (web review of fe646643).
// What/why: the qwen rows against the signed task envelope and its key ring, plus
// the two CHECK rows of the same triage block. Every test is red on the unfixed tree:
//
//	q3 (MINOR, envelope.go): Valid() did not enforce a lowercase 64-hex digest on
//	    ArtifactSHA256/InputSHA256, although the workload files already were.
//	q4 (MINOR, envelope.go): no TTL bound, so a leaked signing path could mint an
//	    envelope valid for any window and every verifier would accept it.
//	q5 (MINOR, envelope.go): Keyring.Add skipped the KeyID(pub) == keyID check
//	    that Restore already performs, so mismatched keys could be published.
//	q6 (CHECK, canonical.go): no HTML escaping of < > & -- parity with JS
//	    JSON.stringify. Green today; kept as a regression pin.
//	q7 (CHECK, envelope.go): VerifyTaskEnvelopeAllowExpired with a retired key,
//	    reachable from the W9 refund path. Green today; kept as a regression pin.
package verify

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"
)

func r10Key(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv, KeyID(pub)
}

// TestR10EnvelopeDigestsMustBeLowercaseHex is the q3 row.
func TestR10EnvelopeDigestsMustBeLowercaseHex(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	tests := []struct {
		name     string
		artifact string
		input    string
		wantErr  bool
	}{
		{"valid pair", SHA256([]byte("a")), SHA256([]byte("b")), false},
		{"short artifact", "aa", SHA256([]byte("b")), true},
		{"short input", SHA256([]byte("a")), "bb", true},
		{"uppercase artifact", strings.ToUpper(SHA256([]byte("a"))), SHA256([]byte("b")), true},
		{"uppercase input", SHA256([]byte("a")), strings.ToUpper(SHA256([]byte("b"))), true},
		{"non-hex artifact", strings.Repeat("z", 64), SHA256([]byte("b")), true},
		{"non-hex input", SHA256([]byte("a")), strings.Repeat("z", 64), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := TaskEnvelope{Version: EnvelopeVersion, JobID: "j", TaskID: "t", ArtifactSHA256: tc.artifact, InputSHA256: tc.input, KeyID: "k", IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix()}
			err := env.Valid()
			if tc.wantErr && err == nil {
				t.Fatalf("Valid accepted digest pair %q/%q", tc.artifact, tc.input)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Valid refused a correct pair: %v", err)
			}
		})
	}
}

// TestR10EnvelopeTTLIsBounded is the q4 row.
func TestR10EnvelopeTTLIsBounded(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	digest := SHA256([]byte("a"))
	tests := []struct {
		name    string
		ttl     time.Duration
		wantErr bool
	}{
		{"one second", time.Second, false},
		{"default ttl", defaultEnvelopeTTL, false},
		{"twenty-three hours", 23 * time.Hour, false},
		{"the cap", maxEnvelopeTTL, false},
		{"one second over the cap", maxEnvelopeTTL + time.Second, true},
		{"a week", 7 * 24 * time.Hour, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := TaskEnvelope{Version: EnvelopeVersion, JobID: "j", TaskID: "t", ArtifactSHA256: digest, InputSHA256: digest, KeyID: "k",
				IssuedAt: now.Unix(), ExpiresAt: now.Add(tc.ttl).Unix()}
			err := env.Valid()
			if tc.wantErr && err == nil {
				t.Fatalf("Valid accepted an envelope of %v", tc.ttl)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Valid refused an envelope of %v: %v", tc.ttl, err)
			}
		})
	}
}

// TestR10KeyringAddEnforcesDerivedKeyID is the q5 row.
func TestR10KeyringAddEnforcesDerivedKeyID(t *testing.T) {
	pub, _, keyID := r10Key(t)
	_, _, otherID := r10Key(t)
	ring := NewKeyring()
	if err := ring.Add(keyID, pub); err != nil {
		t.Fatalf("Add with the derived id: %v", err)
	}
	if err := ring.Add(otherID, pub); !errors.Is(err, ErrKeyIDMismatch) {
		t.Fatalf("Add with a foreign id = %v, want ErrKeyIDMismatch", err)
	}
	if got := ring.Len(); got != 1 {
		t.Fatalf("Len = %d, want only the derived entry", got)
	}
}

// TestR10CanonicalJSONKeepsHTMLCharsLiteral is the q6 CHECK row.
func TestR10CanonicalJSONKeepsHTMLCharsLiteral(t *testing.T) {
	got, err := CanonicalJSON(map[string]string{"a": "<&>"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":"<&>"}` {
		t.Fatalf("CanonicalJSON = %s, want no HTML escaping (JS JSON.stringify parity)", got)
	}
}

// TestR10AllowExpiredSurvivesRetiredKey is the q7 CHECK row: the W9 stale-runner
// refund verifies an envelope whose lease already expired, against a key ring that
// retired the signing key at the rotation. The envelope must verify on the retired
// key as long as the caller passes a clock before the retirement.
func TestR10AllowExpiredSurvivesRetiredKey(t *testing.T) {
	pub, priv, keyID := r10Key(t)
	ring := NewKeyring()
	if err := ring.Add(keyID, pub); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	signed, err := SignTaskEnvelope(NewTaskEnvelope("job", "task", SHA256(nil), SHA256(nil), keyID, now, time.Minute), priv, keyID)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTaskEnvelope(signed, ring, now.Add(time.Minute+time.Second)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired envelope strict err = %v, want ErrExpired", err)
	}
	if err := VerifyTaskEnvelopeAllowExpired(signed, ring, now.Add(30*time.Minute)); err != nil {
		t.Fatalf("allow-expired verification after expiry: %v", err)
	}
	// Retire the key at the rotation moment; the refund verifies at the lease
	// expiry, which is before the retirement, so the old key still verifies.
	rotated := now.Add(2 * time.Minute)
	if err := ring.Retire(keyID, rotated); err != nil {
		t.Fatal(err)
	}
	if err := VerifyTaskEnvelopeAllowExpired(signed, ring, rotated.Add(-time.Second)); err != nil {
		t.Fatalf("allow-expired on the retired key before retirement: %v", err)
	}
	if err := VerifyTaskEnvelopeAllowExpired(signed, ring, rotated); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("allow-expired at the retirement boundary = %v, want ErrUnknownKey", err)
	}
}
