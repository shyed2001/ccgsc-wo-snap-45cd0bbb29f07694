// Path: internal/matmulref/matmulref.go | Author: Hermes (MiniMax-M3) for the owner | 2026-11-22
// What/why: WO R17-RECOMPUTE (G-R16-4) row 1 - the matmul reference recompute.
//
// This package is the WORKLOAD side of the reference recompute and it lives
// OUTSIDE internal/pool on purpose: the platform (internal/pool) may never
// import or read workload code (owner hard rule CR-53). The matmul reference
// owns
//
//   - the native kernel that reproduces the bytes of the reference matmul
//     workload,
//   - the module SHA-256 the kernel was written for,
//   - the published reference digests of the workload sizes.
//
// It registers itself into the platform registry; the platform only looks the
// reference up by module SHA-256.
//
// The module digest below is the pin of the committed reference kernel
// (web/workloads/matmul/matmul_kernel.js, LF line endings, as the workload
// manifest pins it). The workload layer is the owner of that file; this
// package only records the digest the reference answers for.
package matmulref

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"

	"ccgsc/poolscore/internal/recompute"
)

const (
	// Kind is the input kind of the reference matmul workload.
	Kind = "matmul-f32-gpu"
	// ModuleSHA256 is the module artifact digest of the reference matmul
	// kernel. A job whose module artifact has another digest is never
	// recomputed by this reference, even when the workload kind matches.
	ModuleSHA256 = "ef4efbb653948565ccf86b524e37fb89b2b8f7b5185fcb112fbf64f1b74b74e1"
	// VerifierVersion changes when native kernel or input semantics change.
	VerifierVersion = "ccgsc.matmulref.native.v1"
	// MaxN caps the native recompute at 256^3 (W10C row 2).
	MaxN = 256
)

// ReferenceHashes are the published digests of the reference matmul workload.
// A recompute that disagrees with them is a server defect, not worker fraud.
var ReferenceHashes = map[string]string{
	"160": "1a5a6c39c56eada3b4ca01eaed913972ed5e48d1318884748e1abf3646abc2d2",
	"240": "049db7c32a56cc68f03c9bff543f27f7733d1510104782ccc4510e5754cb320c",
}

// Size returns the dimension of a matmul task input. The runner accepts either
// the bare workload input or the {"json": ...} envelope, so both are read.
func Size(input json.RawMessage) (int, bool) {
	raw := input
	var wrapped struct {
		JSON json.RawMessage `json:"json"`
	}
	if json.Unmarshal(input, &wrapped) == nil && len(wrapped.JSON) > 0 {
		raw = wrapped.JSON
	}
	var shape struct {
		Kind string `json:"kind"`
		N    int    `json:"n"`
	}
	if json.Unmarshal(raw, &shape) != nil || shape.Kind != Kind {
		return 0, false
	}
	if shape.N < 1 || shape.N > MaxN {
		return 0, false
	}
	return shape.N, true
}

// Output reproduces the little-endian f32 output bytes of the reference matmul
// workload: a[i] = i%7-3, b[i] = i%5-2, C = A*B row major. Every operand,
// product and partial sum is an exactly representable integer, so the float32
// result is identical to the workload's Math.fround accumulation.
func Output(n int) ([]byte, bool) {
	if n < 1 || n > MaxN {
		return nil, false
	}
	a := make([]float32, n*n)
	b := make([]float32, n*n)
	for i := 0; i < n*n; i++ {
		a[i] = float32(i%7 - 3)
		b[i] = float32(i%5 - 2)
	}
	c := make([]float32, n*n)
	for row := 0; row < n; row++ {
		for col := 0; col < n; col++ {
			var sum float32
			for k := 0; k < n; k++ {
				sum = float32(sum + float32(a[row*n+k]*b[k*n+col]))
			}
			c[row*n+col] = sum
		}
	}
	out := make([]byte, n*n*4)
	for i, value := range c {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(value))
	}
	return out, true
}

// Hash returns the SHA-256 of the native matmul output bytes.
func Hash(n int) (string, bool) {
	out, ok := Output(n)
	if !ok {
		return "", false
	}
	sum := sha256.Sum256(out)
	return hex.EncodeToString(sum[:]), true
}

// Verifier is the registry verifier of the matmul reference: it turns a task
// input into the reference answer. An input that is not a matmul input of the
// reference is an error, never an accusation of a worker.
func Verifier(input json.RawMessage) (recompute.Answer, error) {
	n, ok := Size(input)
	if !ok {
		return recompute.Answer{}, fmt.Errorf("%w: the input is not a %s input", recompute.ErrInvalid, Kind)
	}
	out, ok := Output(n)
	if !ok {
		return recompute.Answer{}, fmt.Errorf("%w: the matmul size %d is out of range", recompute.ErrInvalid, n)
	}
	return recompute.Answer{Size: n, Output: out}, nil
}

// Reference is the registry entry of the matmul reference.
func Reference() recompute.Reference {
	return recompute.Reference{
		Kind:            Kind,
		ModuleSHA256:    ModuleSHA256,
		ReferenceHashes: ReferenceHashes,
		VerifierID:      fmt.Sprintf("%x", sha256.Sum256([]byte(VerifierVersion+":"+ModuleSHA256))),
		Verifier:        Verifier,
	}
}

// Register adds the matmul reference to a registry. It is idempotent.
func Register(reg *recompute.Registry) error {
	if reg == nil {
		return fmt.Errorf("%w: no registry", recompute.ErrInvalid)
	}
	return reg.Register(Reference())
}
