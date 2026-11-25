package recompute

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func n10OtherVerifier(_ json.RawMessage) (Answer, error) {
	return Answer{Size: 7, Output: []byte("other code")}, nil
}

// N10 owner binding: 64-hex digest, case-insensitive input, lowercase storage.
func TestN10RegistryCanonicalizesModuleAndReferenceDigests(t *testing.T) {
	module := strings.Repeat("AB", 32)
	pin := strings.Repeat("CD", 32)
	r := New()
	if err := r.Register(Reference{Kind: "demo", ModuleSHA256: module,
		ReferenceHashes: map[string]string{"7": pin}, VerifierID: testVerifierID, Verifier: testVerifier}); err != nil {
		t.Fatal(err)
	}
	expectedModule := strings.ToLower(module)
	got, err := r.Lookup(expectedModule)
	if err != nil {
		t.Fatalf("lowercase lookup: %v", err)
	}
	if got.ModuleSHA256 != expectedModule {
		t.Fatalf("module = %q", got.ModuleSHA256)
	}
	value, ok := got.ReferenceHash(7)
	if !ok || value != strings.ToLower(pin) {
		t.Fatalf("pin=%q found=%v", value, ok)
	}
	if !r.Has(module) || !r.Has(expectedModule) {
		t.Fatal("mixed-case Has failed")
	}
	if _, err := r.Lookup(module); err != nil {
		t.Fatalf("uppercase lookup: %v", err)
	}
	if r.Len() != 1 {
		t.Fatalf("Len=%d", r.Len())
	}
}

func TestN10RegistryRejectsInvalidDigestShapes(t *testing.T) {
	good := strings.Repeat("a", 64)
	bad := []string{"", strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("g", 64), " ab" + strings.Repeat("a", 61)}
	for _, digest := range bad {
		t.Run("module_"+digest, func(t *testing.T) {
			reg := New()
			err := reg.Register(Reference{Kind: "demo", ModuleSHA256: digest, Verifier: testVerifier})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("module=%q error=%v, want invalid", digest, err)
			}
			if reg.Len() != 0 {
				t.Fatal("invalid registration changed registry")
			}
		})
		t.Run("pin_"+digest, func(t *testing.T) {
			reg := New()
			err := reg.Register(Reference{Kind: "demo", ModuleSHA256: good,
				ReferenceHashes: map[string]string{"7": digest}, Verifier: testVerifier})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("reference pin=%q error=%v, want invalid", digest, err)
			}
			if reg.Len() != 0 {
				t.Fatal("invalid pin changed registry")
			}
		})
	}
}

func TestN10RegistryIdenticalReuploadIsIdempotentButDifferentCodeIsRefused(t *testing.T) {
	good := strings.Repeat("a", 64)
	pin := strings.Repeat("b", 64)
	r := New()
	original := Reference{Kind: "demo", ModuleSHA256: good,
		ReferenceHashes: map[string]string{"7": pin}, VerifierID: testVerifierID, Verifier: testVerifier}
	if err := r.Register(original); err != nil {
		t.Fatal(err)
	}
	repeated := original.Clone()
	repeated.ModuleSHA256 = strings.ToUpper(good)
	repeated.ReferenceHashes["7"] = strings.ToUpper(pin)
	if err := r.Register(repeated); err != nil {
		t.Fatalf("identical uppercase replay error: %v", err)
	}
	for _, different := range []Reference{
		{Kind: "different", ModuleSHA256: good, ReferenceHashes: map[string]string{"7": pin}, VerifierID: testVerifierID, Verifier: testVerifier},
		{Kind: "demo", ModuleSHA256: good, ReferenceHashes: map[string]string{"7": strings.Repeat("c", 64)}, VerifierID: testVerifierID, Verifier: testVerifier},
		{Kind: "demo", ModuleSHA256: good, ReferenceHashes: map[string]string{"7": pin}, VerifierID: strings.Repeat("b", 64), Verifier: n10OtherVerifier},
	} {
		err := r.Register(different)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("different implementation accepted: %+v err=%v", different, err)
		}
	}
	after, err := r.Lookup(good)
	if err != nil {
		t.Fatal(err)
	}
	if after.Kind != "demo" || after.ReferenceHashes["7"] != pin || r.Len() != 1 {
		t.Fatalf("rejected replacement changed canonical original: %+v len=%d", after, r.Len())
	}
}
