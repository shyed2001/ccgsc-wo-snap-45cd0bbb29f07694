// Path: internal/api/workload_seed_W10.go | W10 platform/workload split.
// What/why: a job's module artifact is the Workload, not the platform. The server
// therefore registers the reference workload at startup as a content-addressed
// artifact (its sha256 is the manifest pin), so a developer can submit a job that
// names it and a contributor can fetch the exact bytes the sandbox will run.
// The seed is idempotent: the second start re-reads the same blob and checks the
// pin. The artifact is ownerless on purpose - it is a shared read-only module, so
// a contributor may fetch it and any developer may submit it as a module.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"

	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/web"
)

// workloadContentType is the artifact content type the upload scan accepts for a
// JavaScript workload module (internal/pool/upload_scan_F8.go).
const workloadContentType = "application/javascript"

// wasmContentType is the content type of a wasm-abi entry (D-W10-10). The job
// creation path reads the ABI kind back from this field, so a .wasm entry must
// never be registered as JavaScript.
const wasmContentType = "application/wasm"

// entryContentType picks the content type of one workload entry by extension.
func entryContentType(entry string) string {
	if strings.EqualFold(filepath.Ext(entry), ".wasm") {
		return wasmContentType
	}
	return workloadContentType
}

// workloadManifest is the on-disk manifest of one workload (design section 2):
// id, version, kind, entry file, the entry sha256 pin and the schemas.
type workloadManifest struct {
	ID        string          `json:"id"`
	Version   int             `json:"version"`
	Kind      string          `json:"kind"`
	Entry     string          `json:"entry"`
	SHA256    string          `json:"sha256"`
	Runtime   string          `json:"runtime"`
	Input     json.RawMessage `json:"input"`
	Output    json.RawMessage `json:"output"`
	DemoInput json.RawMessage `json:"demoInput,omitempty"`
}

// SeedWorkloadArtifacts registers the embedded reference workloads as shared,
// content-addressed artifacts. It is idempotent and runs before the server
// serves its first request, so the matmul job can always name its kernel module.
func (s *Server) SeedWorkloadArtifacts() error {
	if s.Engine == nil {
		return nil
	}
	root, err := web.WorkloadRoot()
	if err != nil {
		return fmt.Errorf("workload tree: %w", err)
	}
	entries, err := fs.ReadDir(root, ".")
	if err != nil {
		return fmt.Errorf("workload tree: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := s.seedWorkload(root, entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

// seedWorkload registers one workload folder: the manifest is read for the pin,
// the entry file is uploaded as the blob and the pin is verified afterwards.
func (s *Server) seedWorkload(root fs.FS, dir string) error {
	raw, err := fs.ReadFile(root, dir+"/manifest.json")
	if err != nil {
		return fmt.Errorf("workload %s manifest: %w", dir, err)
	}
	var manifest workloadManifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("workload %s manifest: %w", dir, err)
	}
	if manifest.Entry == "" || manifest.SHA256 == "" {
		return fmt.Errorf("workload %s manifest: entry and sha256 are required", dir)
	}
	data, err := fs.ReadFile(root, dir+"/"+manifest.Entry)
	if err != nil {
		return fmt.Errorf("workload %s entry: %w", dir, err)
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if digest != manifest.SHA256 {
		return fmt.Errorf("workload %s digest %s does not match the manifest pin %s", dir, digest, manifest.SHA256)
	}
	// A complete blob already registered by any earlier start is reused. The
	// engine answers ErrConflict for a complete blob, so this is the fast path.
	if existing, err := s.Engine.Artifact(context.Background(), digest); err == nil && existing.Complete {
		return nil
	} else if err != nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, pool.ErrConflict) {
		return fmt.Errorf("workload %s lookup: %w", dir, err)
	}
	if _, err = s.Engine.Upload(context.Background(), digest, entryContentType(manifest.Entry), 0, int64(len(data)), data); err != nil {
		return fmt.Errorf("workload %s upload: %w", dir, err)
	}
	if s.Log != nil {
		s.Log.Info("workload.registered", "id", manifest.ID, "kind", manifest.Kind, "artifactId", digest, "sizeBytes", len(data))
	}
	return nil
}

// serveMockArtifact hands the contributor a separate, pinned workload artifact
// and its example input at RUNTIME. CR-53 forbids embedding any workload source
// in pages/js or the generated runner.html. The reference is chosen from
// workload manifests, not hardcoded in the platform JavaScript.
func (s *Server) serveMockArtifact(w http.ResponseWriter, r *http.Request) {
	root, err := web.WorkloadRoot()
	if err != nil {
		http.Error(w, "mock artifact unavailable", http.StatusServiceUnavailable)
		return
	}
	entries, err := fs.ReadDir(root, ".")
	if err != nil {
		http.Error(w, "mock artifact unavailable", http.StatusServiceUnavailable)
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		raw, err := fs.ReadFile(root, entry.Name()+"/manifest.json")
		if err != nil {
			http.Error(w, "mock manifest unavailable", http.StatusInternalServerError)
			return
		}
		var m workloadManifest
		if json.Unmarshal(raw, &m) != nil {
			http.Error(w, "invalid mock manifest", http.StatusInternalServerError)
			return
		}
		if len(m.DemoInput) == 0 {
			continue // only registered reference workloads with demo inputs qualify
		}
		if m.Entry == "" || m.SHA256 == "" || !json.Valid(m.DemoInput) {
			http.Error(w, "invalid mock manifest", http.StatusInternalServerError)
			return
		}
		data, err := fs.ReadFile(root, entry.Name()+"/"+m.Entry)
		if err != nil {
			http.Error(w, "mock artifact unavailable", http.StatusInternalServerError)
			return
		}
		sum := sha256.Sum256(data)
		digest := hex.EncodeToString(sum[:])
		if digest != m.SHA256 {
			http.Error(w, "mock artifact pin mismatch", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id": m.ID, "version": m.Version, "sha256": digest,
			"inputJSON": string(m.DemoInput), "source": string(data),
		})
		return
	}
	http.Error(w, "no demo-capable workload", http.StatusServiceUnavailable)
}
