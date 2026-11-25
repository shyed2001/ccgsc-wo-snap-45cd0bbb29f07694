// Path: internal/matmulref/matmulref_test.go | Author: Hermes (MiniMax-M3) for the owner | 2026-11-22
// What/why: WO R17-RECOMPUTE (G-R16-4) row 3 - the red tests of the matmul
// reference: the native kernel reproduces the published reference hashes, the
// reference registers itself under the module SHA-256 of the committed kernel,
// and the module digest is the one the workload manifest pins.
package matmulref

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"ccgsc/poolscore/internal/recompute"
)

// hashOf is the SHA-256 of bytes as a lowercase hex digest.
func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestMatmulHashesMatchPublishedReferences is the red test of the reference:
// the native kernel reproduces the published digests of the reference workload,
// and it refuses a size above the cap.
func TestMatmulHashesMatchPublishedReferences(t *testing.T) {
	for n, want := range map[int]string{
		160: "1a5a6c39c56eada3b4ca01eaed913972ed5e48d1318884748e1abf3646abc2d2",
		240: "049db7c32a56cc68f03c9bff543f27f7733d1510104782ccc4510e5754cb320c",
	} {
		got, ok := Hash(n)
		if !ok {
			t.Fatalf("Hash(%d) unavailable", n)
		}
		if got != want {
			t.Errorf("Hash(%d) = %s, want %s", n, got, want)
		}
	}
	if _, ok := Output(257); ok {
		t.Error("the reference accepted a size above the cap")
	}
	if _, ok := Output(0); ok {
		t.Error("the reference accepted size 0")
	}
}

// TestMatmulModuleDigestMatchesTheManifestPin is the red test of the module
// pin: the digest the reference answers for is the digest the workload
// manifest pins. The workload layer owns that file; this test only proves the
// two agree, so a rotated kernel is caught here and not in a silent skip.
func TestMatmulModuleDigestMatchesTheManifestPin(t *testing.T) {
	root := filepath.Join("..", "..", "web", "workloads", "matmul")
	manifest, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatalf("read the manifest: %v", err)
	}
	var m struct {
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatalf("parse the manifest: %v", err)
	}
	if m.SHA256 != ModuleSHA256 {
		t.Fatalf("the reference answers for module %s, but the manifest pins %s", ModuleSHA256, m.SHA256)
	}
}

// TestMatmulRegisterIntoTheRegistry is the red test of the wiring: the
// reference registers itself and is then found by the module SHA-256.
func TestMatmulRegisterIntoTheRegistry(t *testing.T) {
	reg := recompute.New()
	if err := Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ref, err := reg.Lookup(ModuleSHA256)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if ref.Kind != Kind {
		t.Fatalf("reference kind = %q, want %q", ref.Kind, Kind)
	}
	// The published hashes travel with the reference, keyed by the size.
	for size, want := range ReferenceHashes {
		n, err := strconv.Atoi(size)
		if err != nil {
			t.Fatalf("the published hash key %q is not a size: %v", size, err)
		}
		got, ok := ref.ReferenceHash(n)
		if !ok {
			t.Fatalf("the reference lost the published hash of size %d", n)
		}
		if got != want {
			t.Errorf("the published hash of size %d = %s, want %s", n, got, want)
		}
	}
}

// TestMatmulVerifierAnswersTheTaskInput is the red test of the verifier: it
// answers a matmul input (bare or in the {"json": ...} envelope) and refuses
// an input of another kind with an error, never with an accusation.
func TestMatmulVerifierAnswersTheTaskInput(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"matmul-f32-gpu","n":160}`,
		`{"json":{"kind":"matmul-f32-gpu","n":160}}`,
	} {
		answer, err := Verifier(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("Verifier(%s): %v", raw, err)
		}
		if answer.Size != 160 {
			t.Fatalf("answer size = %d, want 160", answer.Size)
		}
		want, _ := Hash(160)
		if got := hashOf(answer.Output); got != want {
			t.Fatalf("answer digest = %s, want %s", got, want)
		}
	}
	if _, err := Verifier(json.RawMessage(`{"kind":"other","n":160}`)); !errors.Is(err, recompute.ErrInvalid) {
		t.Fatalf("an input of another kind = %v, want ErrInvalid", err)
	}
	if _, err := Verifier(json.RawMessage(`{"kind":"matmul-f32-gpu","n":1000}`)); !errors.Is(err, recompute.ErrInvalid) {
		t.Fatalf("a size above the cap = %v, want ErrInvalid", err)
	}
}
