// Path: internal/verify/envelope_workload_W10d_test.go | W10d D-W10-10.
// What/why: the ABI kind of a workload is part of the SIGNED payload, so a job
// cannot present a js-module under the byte ABI or the other way round. The
// kind must round-trip through the canonical encoding, an unknown spelling must
// be refused by schema validation, and an envelope signed without a kind must
// stay byte-identical to the W10c form (no silent field is added).
package verify

import (
	"strings"
	"testing"
	"time"
)

// workloadEnvelope builds a signed envelope carrying one workload descriptor.
func workloadEnvelope(t *testing.T, kind string) (SignedTaskEnvelope, *Keyring, string) {
	t.Helper()
	pub, priv, keyID := newTestKey(t)
	ring := NewKeyring()
	if err := ring.Add(keyID, pub); err != nil {
		t.Fatalf("ring.Add: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	env := NewTaskEnvelope("job-w", "task-w", SHA256([]byte("wasm")), SHA256([]byte("input")), keyID, now, 10*time.Minute)
	env.Workload = &WorkloadDescriptor{
		ID:      "matmul",
		Version: 1,
		Kind:    kind,
		Files:   []WorkloadFile{{Path: "matmul_scalar.wasm", SHA256: SHA256([]byte("kernel"))}},
	}
	if err := env.Valid(); err != nil {
		t.Fatalf("Valid: %v", err)
	}
	signed, err := SignTaskEnvelope(env, priv, keyID)
	if err != nil {
		t.Fatalf("SignTaskEnvelope: %v", err)
	}
	if err := VerifyTaskEnvelope(signed, ring, now); err != nil {
		t.Fatalf("VerifyTaskEnvelope: %v", err)
	}
	return signed, ring, keyID
}

func TestWorkloadKindIsSigned(t *testing.T) {
	signed, _, _ := workloadEnvelope(t, WorkloadKindWasmABI)
	if !strings.Contains(string(signed.Payload), `"kind":"`+WorkloadKindWasmABI+`"`) {
		t.Fatalf("payload does not carry the signed kind: %s", signed.Payload)
	}
	dec, err := DecodeTaskEnvelope(signed)
	if err != nil {
		t.Fatalf("DecodeTaskEnvelope: %v", err)
	}
	if dec.Workload == nil || dec.Workload.Kind != WorkloadKindWasmABI {
		t.Fatalf("decoded workload kind = %+v, want %q", dec.Workload, WorkloadKindWasmABI)
	}
}

// A tampered kind is a different canonical payload, so the signature no longer
// covers it. This is the property that makes the field part of the trust root.
func TestWorkloadKindTamperBreaksSignature(t *testing.T) {
	signed, ring, keyID := workloadEnvelope(t, WorkloadKindWasmABI)
	tampered := SignedTaskEnvelope{
		Payload: []byte(strings.Replace(string(signed.Payload),
			`"kind":"`+WorkloadKindWasmABI+`"`, `"kind":"`+WorkloadKindJSModule+`"`, 1)),
		Signature: signed.Signature,
		KeyID:     keyID,
		Algorithm: signed.Algorithm,
	}
	// Either the schema or the canonical binding must reject it; what may never
	// happen is that the swapped ABI verifies.
	if err := VerifyTaskEnvelope(tampered, ring, time.Unix(1_700_000_001, 0).UTC()); err == nil {
		t.Fatal("a swapped workload kind verified")
	}
}

func TestWorkloadKindSchema(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	build := func(kind string) TaskEnvelope {
		env := NewTaskEnvelope("job-w", "task-w", SHA256([]byte("wasm")), SHA256([]byte("input")), "0123456789abcdef", now, time.Minute)
		env.Workload = &WorkloadDescriptor{
			ID: "matmul", Version: 1, Kind: kind,
			Files: []WorkloadFile{{Path: "k.wasm", SHA256: SHA256([]byte("kernel"))}},
		}
		return env
	}
	// An empty kind is the W10c form and stays valid.
	for _, kind := range []string{"", WorkloadKindJSModule, WorkloadKindWasmABI} {
		if err := build(kind).Valid(); err != nil {
			t.Fatalf("kind %q must be valid: %v", kind, err)
		}
	}
	// An unknown spelling is a schema failure, never a runnable default.
	for _, kind := range []string{"wasm", "WASM-ABI", "elf", "js-module "} {
		if err := build(kind).Valid(); err == nil {
			t.Fatalf("kind %q was accepted", kind)
		}
	}
}

// A kindless descriptor must not grow a "kind" field in the canonical bytes:
// existing leases are signed without one and must keep verifying byte for byte.
func TestKindlessWorkloadPayloadUnchanged(t *testing.T) {
	signed, _, _ := workloadEnvelope(t, "")
	if strings.Contains(string(signed.Payload), `"kind"`) {
		t.Fatalf("a kindless descriptor emitted a kind field: %s", signed.Payload)
	}
}
