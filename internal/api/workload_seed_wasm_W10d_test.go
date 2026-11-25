// Path: internal/api/workload_seed_wasm_W10d_test.go | W10d D-W10-10.
// What/why: the wasm-abi example is registered like every other workload, but a
// .wasm entry must be stored as application/wasm - that field is how job
// creation learns the ABI kind to sign. A .wasm registered as JavaScript would
// make the signed kind unreachable, so it is asserted here rather than assumed.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store/sqlite"
	"ccgsc/poolscore/internal/verify"
	"ccgsc/poolscore/web"
)

// discardLogger keeps the pool's artifact-load chatter out of the test output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestW10dWasmWorkloadIsSeededAsWasm(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "wasm-seed.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	log := discardLogger()
	engine, err := pool.New(db, log, "wasm-seed", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Engine: engine, Log: log, Config: config.Default()}
	if err := s.SeedWorkloadArtifacts(); err != nil {
		t.Fatalf("seed: %v", err)
	}

	root, err := web.WorkloadRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := fs.ReadFile(root, "matmul_wasm/manifest.json")
	if err != nil {
		t.Fatalf("the wasm workload must be embedded: %v", err)
	}
	var manifest workloadManifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Kind != verify.WorkloadKindWasmABI {
		t.Fatalf("manifest kind = %q, want %q", manifest.Kind, verify.WorkloadKindWasmABI)
	}
	entry, err := fs.ReadFile(root, "matmul_wasm/"+manifest.Entry)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(entry, []byte{0x00, 0x61, 0x73, 0x6d}) {
		t.Fatal("the entry is not a WebAssembly module")
	}

	stored, err := engine.Artifact(context.Background(), manifest.SHA256)
	if err != nil {
		t.Fatalf("artifact %s not registered: %v", manifest.SHA256, err)
	}
	if stored.ContentType != wasmContentType {
		t.Fatalf("content type = %q, want %q", stored.ContentType, wasmContentType)
	}
	if !bytes.Equal(stored.Data, entry) {
		t.Fatal("the registered bytes are not the manifest entry")
	}
}

func TestW10dEntryContentType(t *testing.T) {
	cases := map[string]string{
		"matmul.c":           workloadContentType,
		"matmul_scalar.wasm": wasmContentType,
		"MATMUL.WASM":        wasmContentType,
		"table.bin":          workloadContentType,
		"entry.js":           workloadContentType,
	}
	for entry, want := range cases {
		if got := entryContentType(entry); got != want {
			t.Errorf("entryContentType(%q) = %q, want %q", entry, got, want)
		}
	}
}
