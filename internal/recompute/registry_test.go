// Path: internal/recompute/registry_test.go | Author: Hermes (MiniMax-M3) for the owner | 2026-11-22
// What/why: WO R17-RECOMPUTE (G-R16-4) row 3 - the red tests of the generic
// reference-recompute registry: the lookup by module SHA-256, the refusal of an
// unknown module, and the refusal of an unusable reference.
package recompute

import (
	"encoding/json"
	"errors"
	"testing"
)

const testVerifierID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// testVerifier is a reference verifier that answers a fixed output.
func testVerifier(input json.RawMessage) (Answer, error) {
	return Answer{Size: 7, Output: []byte("reference output")}, nil
}

// TestRegistryLookupByModuleSHA is the red test of the lookup: a reference is
// found by the module SHA-256 of the job, never by the workload kind.
func TestRegistryLookupByModuleSHA(t *testing.T) {
	reg := New()
	const moduleSHA = "aa11bb22cc33dd44ee55ff6677889900aabbccddeeff00112233445566778899"
	if err := reg.Register(Reference{
		Kind:            "test-kind",
		ModuleSHA256:    moduleSHA,
		ReferenceHashes: map[string]string{"7": "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"},
		Verifier:        testVerifier,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ref, err := reg.Lookup(moduleSHA)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if ref.Kind != "test-kind" || ref.ModuleSHA256 != moduleSHA {
		t.Fatalf("reference = %+v, want the registered one", ref)
	}
	if want, ok := ref.ReferenceHash(7); !ok || want != "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd" {
		t.Fatalf("reference hash = %q ok = %v, want dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", want, ok)
	}
	if _, ok := ref.ReferenceHash(8); ok {
		t.Fatal("a size without a published hash must not answer one")
	}
	if !reg.Has(moduleSHA) {
		t.Fatal("Has must report the registered module")
	}
	if reg.Len() != 1 {
		t.Fatalf("Len = %d, want 1", reg.Len())
	}
}

// TestRegistryUnknownModuleIsNeverRecomputed is the red test of the unknown
// module: the lookup fails with ErrUnknownModule, so the caller never
// recomputes it and never accuses a worker because of it.
func TestRegistryUnknownModuleIsNeverRecomputed(t *testing.T) {
	reg := New()
	if reg.Has("no-such-module") {
		t.Fatal("an unregistered module must not be reported as registered")
	}
	_, err := reg.Lookup("no-such-module")
	if !errors.Is(err, ErrUnknownModule) {
		t.Fatalf("lookup of an unknown module = %v, want ErrUnknownModule", err)
	}
}

// TestRegistryRefusesAnUnusableReference is the red test of the registration
// guard: a reference without a module digest, without a kind or without a
// verifier would silently disable a recompute the operator expects.
func TestRegistryRefusesAnUnusableReference(t *testing.T) {
	reg := New()
	cases := map[string]Reference{
		"no module": {Kind: "k", Verifier: testVerifier},
		"no kind":   {ModuleSHA256: "m", Verifier: testVerifier},
		"no verify": {Kind: "k", ModuleSHA256: "m"},
	}
	for name, ref := range cases {
		if err := reg.Register(ref); !errors.Is(err, ErrInvalid) {
			t.Errorf("Register(%s) = %v, want ErrInvalid", name, err)
		}
	}
	if reg.Len() != 0 {
		t.Fatalf("Len = %d, want an empty registry", reg.Len())
	}
}

// TestRegistryRegisterIsIdempotent pins that a wiring that runs twice (a
// test, a reload) replaces the reference instead of failing.
func TestRegistryRegisterIsIdempotent(t *testing.T) {
	reg := New()
	const moduleSHA = "bb11bb22cc33dd44ee55ff6677889900aabbccddeeff00112233445566778899"
	for i := 0; i < 3; i++ {
		if err := reg.Register(Reference{
			Kind: "test-kind", ModuleSHA256: moduleSHA, VerifierID: testVerifierID, Verifier: testVerifier,
		}); err != nil {
			t.Fatalf("Register %d: %v", i, err)
		}
	}
	if reg.Len() != 1 {
		t.Fatalf("Len = %d, want 1", reg.Len())
	}
}

// TestRegistryLookupReturnsACopy pins that a caller can never mutate a
// registered reference through the map it received.
func TestRegistryLookupReturnsACopy(t *testing.T) {
	reg := New()
	const moduleSHA = "cc11bb22cc33dd44ee55ff6677889900aabbccddeeff00112233445566778899"
	if err := reg.Register(Reference{
		Kind: "test-kind", ModuleSHA256: moduleSHA,
		ReferenceHashes: map[string]string{"7": "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"},
		Verifier:        testVerifier,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ref, err := reg.Lookup(moduleSHA)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	ref.ReferenceHashes["7"] = "tampered"
	again, err := reg.Lookup(moduleSHA)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if again.ReferenceHashes["7"] != "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd" {
		t.Fatal("a caller mutated the registered reference through the returned map")
	}
}

// TestRegistryConcurrentUse pins the registry under the race detector: the
// platform reads it from the hot path while the wiring may still register.
func TestRegistryConcurrentUse(t *testing.T) {
	reg := New()
	const moduleSHA = "dd11bb22cc33dd44ee55ff6677889900aabbccddeeff00112233445566778899"
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			_ = reg.Register(Reference{
				Kind: "test-kind", ModuleSHA256: moduleSHA, VerifierID: testVerifierID, Verifier: testVerifier,
			})
		}
	}()
	for i := 0; i < 100; i++ {
		_, _ = reg.Lookup(moduleSHA)
		_ = reg.Kinds()
		_ = reg.Has(moduleSHA)
	}
	<-done
}
