package recompute

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

//go:noinline
func r3cCapturedVerifier(value string) Verifier {
	return func(_ json.RawMessage) (Answer, error) { return Answer{Size: 1, Output: []byte(value)}, nil }
}

// Without an explicit identity, a second registration cannot prove
// equivalence. The same closure code pointer can capture different data.
func TestR3CUnidentifiedClosureConflict(t *testing.T) {
	reg := New()
	module := strings.Repeat("a", 64)
	first := Reference{Kind: "closure", ModuleSHA256: module, Verifier: r3cCapturedVerifier("first")}
	second := Reference{Kind: "closure", ModuleSHA256: module, Verifier: r3cCapturedVerifier("second")}
	if err := reg.Register(first); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(second); !errors.Is(err, ErrInvalid) {
		t.Fatalf("conflicting captured verifier accepted: %v", err)
	}
	got, err := reg.Lookup(module)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := got.Verifier(nil)
	if err != nil || string(answer.Output) != "first" {
		t.Fatalf("original verifier changed: %+v %v", answer, err)
	}
}

func TestR3CRegistrationOwnsInputPins(t *testing.T) {
	reg := New()
	module := strings.Repeat("a", 64)
	upper := strings.Repeat("B", 64)
	pins := map[string]string{"1": upper}
	ref := Reference{Kind: "closure", ModuleSHA256: module, ReferenceHashes: pins, Verifier: r3cCapturedVerifier("first")}
	if err := reg.Register(ref); err != nil {
		t.Fatal(err)
	}
	if pins["1"] != upper {
		t.Fatal("Register normalized caller-owned map")
	}
	pins["1"] = strings.Repeat("c", 64)
	got, err := reg.Lookup(module)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReferenceHashes["1"] != strings.ToLower(upper) {
		t.Fatal("caller mutation changed registered pin")
	}
}

func TestR3CExplicitVersionedIdentity(t *testing.T) {
	reg := New()
	module := strings.Repeat("a", 64)
	first := Reference{Kind: "closure", ModuleSHA256: module, VerifierID: strings.Repeat("B", 64), Verifier: r3cCapturedVerifier("first")}
	if err := reg.Register(first); err != nil {
		t.Fatal(err)
	}
	same := first.Clone()
	same.VerifierID = strings.ToLower(first.VerifierID)
	if err := reg.Register(same); err != nil {
		t.Fatalf("same explicit ID should be idempotent: %v", err)
	}
	different := same.Clone()
	different.VerifierID = strings.Repeat("c", 64)
	different.Verifier = r3cCapturedVerifier("second")
	if err := reg.Register(different); !errors.Is(err, ErrInvalid) {
		t.Fatalf("different versioned ID accepted: %v", err)
	}
	got, err := reg.Lookup(module)
	if err != nil {
		t.Fatal(err)
	}
	if got.VerifierID != same.VerifierID {
		t.Fatalf("stored verifier ID=%q", got.VerifierID)
	}
	answer, err := got.Verifier(nil)
	if err != nil || string(answer.Output) != "first" {
		t.Fatalf("original verifier changed: %+v %v", answer, err)
	}
	invalid := first.Clone()
	invalid.ModuleSHA256 = strings.Repeat("d", 64)
	invalid.VerifierID = "not-a-digest"
	if err := reg.Register(invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid ID accepted: %v", err)
	}
}
