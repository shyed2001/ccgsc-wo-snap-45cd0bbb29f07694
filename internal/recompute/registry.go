// Path: internal/recompute/registry.go | Author: Hermes (MiniMax-M3) for the owner | 2026-11-22
// What/why: WO R17-RECOMPUTE (G-R16-4) row 1 - the GENERIC reference-recompute
// registry of the platform.
//
// The registry is the platform half of the reference recompute. It knows how to
// find a reference implementation by the module SHA-256 of a job, and it never
// reads a workload file: the platform may not import or read workload code
// (owner hard rule CR-53). The workload layer (or the wiring outside
// internal/pool) registers its own reference; the platform only looks it up.
//
// The contract of one registered reference:
//
//   - Kind            the workload kind the reference answers for,
//   - ModuleSHA256    the module artifact digest the reference is written for,
//   - ReferenceHashes the published reference digests, keyed by the size the
//     reference publishes,
//   - VerifierID      stable digest identifying the trusted, versioned verifier
//   - Verifier        the function that turns a task input into the reference
//     output.
//
// A reference that is not registered is never recomputed: an unknown module is
// always settled on its own evidence. The registry is safe for concurrent use.
package recompute

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"sync"
)

// ErrUnknownModule reports a lookup that found no registered reference. The
// caller must treat it as "never recomputed", never as a failure of a worker.
var ErrUnknownModule = errors.New("no reference recompute is registered for this module")

// ErrInvalid reports a reference the registry cannot use.
var ErrInvalid = errors.New("invalid reference recompute")

// Verifier turns a task input into the reference answer.
//
// The answer carries the size the reference computed for and the reference
// output bytes. The platform compares the digest of those bytes with the digest
// the worker reported; it never parses the workload kind of the input itself.
type Verifier func(input json.RawMessage) (Answer, error)

// Answer is what a reference answers for one task input.
type Answer struct {
	// Size is the size the reference computed for. It is the key of the
	// published reference hashes, so the platform can run the self-check of
	// the reference without knowing the workload kind.
	Size int `json:"size"`
	// Output is the reference output bytes of the task.
	Output []byte `json:"output"`
}

// Reference is one registered reference recompute.
//
// The struct is the WO contract plus explicit versioned verifier identity (R3C).
type Reference struct {
	// Kind is the workload kind the reference answers for.
	Kind string `json:"kind"`
	// ModuleSHA256 is the module artifact digest the reference is written for.
	// A job whose module artifact has a different digest is never recomputed
	// by this reference, even when the workload kind matches.
	ModuleSHA256 string `json:"moduleSHA256"`
	// ReferenceHashes are the published reference digests, keyed by the size
	// the reference publishes (the decimal size as a string). A size without an
	// entry is still recomputed; the self-check simply has no published value
	// to compare against.
	ReferenceHashes map[string]string `json:"referenceHashes,omitempty"`
	// VerifierID is the trusted registering implementation's stable digest of
	// its versioned verifier definition. A new implementation or captured
	// configuration must use a new identity. A first legacy registration
	// without an ID is allowed, but cannot be replayed as proven identical.
	VerifierID string `json:"verifierId,omitempty"`
	// Verifier produces the reference answer of a task input.
	Verifier Verifier `json:"-"`
}

// Clone returns a copy of the reference with its own hash map, so a caller
// can never mutate a registered reference through the map it received.
func (r Reference) Clone() Reference {
	out := r
	if r.ReferenceHashes != nil {
		out.ReferenceHashes = make(map[string]string, len(r.ReferenceHashes))
		for k, v := range r.ReferenceHashes {
			out.ReferenceHashes[k] = v
		}
	}
	return out
}

// ReferenceHash returns the published digest of one size of the reference.
func (r Reference) ReferenceHash(size int) (string, bool) {
	want, ok := r.ReferenceHashes[strconv.Itoa(size)]
	return want, ok
}

// Registry holds the registered references of the platform.
//
// The zero Registry is not usable: build one with New.
type Registry struct {
	mu         sync.RWMutex
	references map[string]Reference
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{references: make(map[string]Reference)}
}

// Register adds a reference to the registry.
//
// The module SHA-256 is the canonical lowercase key. Re-registration with
// identical kind, normalized published pins and explicit verifier ID is idempotent;
// different definitions under the same module digest are refused. The owner
// must publish a new digest/version rather than silently replacing code.
//
// A reference without a module digest, without a kind or without a verifier is
// refused: an unusable reference would silently disable a recompute that the
// operator expects.
func canonicalSHA256(value string) (string, error) {
	if len(value) != 64 {
		return "", fmt.Errorf("%w: expected exactly 64 hexadecimal SHA-256 characters", ErrInvalid)
	}
	bytes, err := hex.DecodeString(value)
	if err != nil || len(bytes) != 32 {
		return "", fmt.Errorf("%w: malformed SHA-256 hexadecimal digest", ErrInvalid)
	}
	return hex.EncodeToString(bytes), nil
}

func (r *Registry) Register(ref Reference) error {
	var err error
	ref = ref.Clone()
	ref.ModuleSHA256, err = canonicalSHA256(ref.ModuleSHA256)
	if err != nil {
		return fmt.Errorf("%w: invalid module SHA-256: %v", ErrInvalid, err)
	}
	if ref.Kind == "" {
		return fmt.Errorf("%w: the reference has no kind", ErrInvalid)
	}
	if ref.Verifier == nil {
		return fmt.Errorf("%w: the reference has no verifier", ErrInvalid)
	}
	if ref.VerifierID != "" {
		ref.VerifierID, err = canonicalSHA256(ref.VerifierID)
		if err != nil {
			return fmt.Errorf("%w: invalid verifier identity: %v", ErrInvalid, err)
		}
	}
	for size, want := range ref.ReferenceHashes {
		canonical, er := canonicalSHA256(want)
		if er != nil {
			return fmt.Errorf("%w: invalid reference pin for size %q: %v", ErrInvalid, size, er)
		}
		ref.ReferenceHashes[size] = canonical
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.references == nil {
		r.references = make(map[string]Reference)
	}
	if existing, ok := r.references[ref.ModuleSHA256]; ok {
		// Function entry pointers are not identities: closures and bound methods
		// can share a code pointer while capturing different values. An explicit
		// versioned digest is the trusted wiring contract, not runtime introspection.
		if ref.VerifierID != "" && existing.VerifierID == ref.VerifierID &&
			existing.Kind == ref.Kind && maps.Equal(existing.ReferenceHashes, ref.ReferenceHashes) {
			return nil
		}
		return fmt.Errorf("%w: conflicting reference registration for module %s; use a new module digest", ErrInvalid, ref.ModuleSHA256)
	}
	r.references[ref.ModuleSHA256] = ref
	return nil
}

// Lookup returns the reference registered for a module digest.
//
// An unknown module answers ErrUnknownModule: the caller must never recompute
// it and never accuse a worker because of it.
func (r *Registry) Lookup(moduleSHA256 string) (Reference, error) {
	moduleSHA256 = strings.ToLower(moduleSHA256)
	r.mu.RLock()
	defer r.mu.RUnlock()
	ref, ok := r.references[moduleSHA256]
	if !ok {
		return Reference{}, fmt.Errorf("%w: %s", ErrUnknownModule, moduleSHA256)
	}
	return ref.Clone(), nil
}

// Has reports whether a module digest has a registered reference.
func (r *Registry) Has(moduleSHA256 string) bool {
	moduleSHA256 = strings.ToLower(moduleSHA256)
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.references[moduleSHA256]
	return ok
}

// Kinds returns the workload kinds of the registered references, one per
// reference. It is a read-only view for the admin surface.
func (r *Registry) Kinds() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	kinds := make([]string, 0, len(r.references))
	for _, ref := range r.references {
		kinds = append(kinds, ref.Kind)
	}
	return kinds
}

// Len returns the number of registered references.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.references)
}
