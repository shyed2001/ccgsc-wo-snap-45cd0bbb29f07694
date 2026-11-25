// Path: internal/api/workload_seed_r10_test.go | R10 fix lane (web review of fe646643).
// What/why: the qwen MAJOR row against workload_seed_W10.go, checked for
// reachability. The row claims that an Upload ErrConflict aborts startup although
// the artifact is complete, so the seed should re-lookup on ErrConflict and treat
// a complete artifact as success.
//
// The tests below show the claimed state cannot occur. The seed looks the blob up
// first and returns as soon as the row is complete, so Upload is not even called
// for a complete artifact; and every conflict Upload can answer comes from a row
// that a re-lookup does not report complete either:
//
//  1. A registered complete blob is returned by Engine.Artifact, so the seed's
//     fast path returns before Upload (no conflict can abort it).
//  2. A direct re-upload of the same pinned bytes is a replay, not ErrConflict.
//  3. The remaining conflict state is a partial row (an interrupted first
//     upload). It is reported incomplete by Artifact and stays incomplete after
//     a re-lookup, so "complete = success" cannot resolve it and aborting is the
//     fail-closed behaviour the row itself allows.
//
// Row verdict: NOT-REAL. The tests are kept as the proof.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"testing"

	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/web"
)

// TestR10SeedCompleteArtifactNeverReachesUpload proves claims 1 and 2.
func TestR10SeedCompleteArtifactNeverReachesUpload(t *testing.T) {
	s, engine, _ := workloadSeedFixture(t)
	ctx := context.Background()
	data, manifest := embeddedWorkload(t)
	if _, err := engine.Upload(ctx, manifest.SHA256, workloadContentType, 0, int64(len(data)), data); err != nil {
		t.Fatalf("first upload: %v", err)
	}
	found, err := engine.Artifact(ctx, manifest.SHA256)
	if err != nil || !found.Complete {
		t.Fatalf("lookup of the registered blob = %+v / %v", found, err)
	}
	// A raced or repeated upload of the same bytes is a replay, not a conflict.
	if _, err = engine.Upload(ctx, manifest.SHA256, workloadContentType, 0, int64(len(data)), data); err != nil {
		t.Fatalf("identical re-upload err = %v, want nil (replay path)", err)
	}
	// The seed is green because its fast path handles the complete blob.
	for i := 0; i < 2; i++ {
		if err := s.SeedWorkloadArtifacts(); err != nil {
			t.Fatalf("seed run %d after a complete registration: %v", i+1, err)
		}
	}
	if got, err := engine.Artifact(ctx, manifest.SHA256); err != nil || !got.Complete {
		t.Fatalf("artifact after the seed = %+v / %v", got, err)
	}
}

// TestR10SeedConflictStateIsNeverComplete proves claim 3: the partial row that
// does make the seed's Upload answer ErrConflict is also reported incomplete, so a
// re-lookup could not turn it into the success the row asks for.
func TestR10SeedConflictStateIsNeverComplete(t *testing.T) {
	_, engine, _ := workloadSeedFixture(t)
	ctx := context.Background()
	data, manifest := embeddedWorkload(t)
	// An interrupted first upload leaves a prefix behind.
	prefix := int64(len(data) / 2)
	if _, err := engine.Upload(ctx, manifest.SHA256, workloadContentType, 0, int64(len(data)), data[:prefix]); err != nil {
		t.Fatalf("partial upload: %v", err)
	}
	if _, err := engine.Artifact(ctx, manifest.SHA256); !errors.Is(err, pool.ErrConflict) {
		t.Fatalf("lookup of the partial row err = %v, want ErrConflict (not complete)", err)
	}
	// The restarted seed then re-uploads the whole blob from offset 0, which is a
	// conflict: the row already holds a prefix. Nothing marks the row complete, so
	// the row's remedy ("re-lookup; complete = success") cannot apply.
	if _, err := engine.Upload(ctx, manifest.SHA256, workloadContentType, 0, int64(len(data)), data); !errors.Is(err, pool.ErrConflict) {
		t.Fatalf("full re-upload over a partial row err = %v, want ErrConflict", err)
	}
	if got, err := engine.Artifact(ctx, manifest.SHA256); !errors.Is(err, pool.ErrConflict) || got.Complete {
		t.Fatalf("re-lookup after the conflict = %+v / %v, want still incomplete", got, err)
	}
}

// TestR10SeedRegistersEveryWorkload pins that every folder in the embed ends up as
// a registered artifact, which is what the startup path needs.
func TestR10SeedRegistersEveryWorkload(t *testing.T) {
	s, engine, _ := workloadSeedFixture(t)
	if err := s.SeedWorkloadArtifacts(); err != nil {
		t.Fatal(err)
	}
	root, err := web.WorkloadRoot()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(root, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		raw, err := fs.ReadFile(root, entry.Name()+"/manifest.json")
		if err != nil {
			t.Fatalf("workload %s manifest: %v", entry.Name(), err)
		}
		var manifest workloadManifest
		if err = json.Unmarshal(raw, &manifest); err != nil {
			t.Fatalf("workload %s manifest: %v", entry.Name(), err)
		}
		var a pool.Artifact
		if err := engine.Store.View(context.Background(), func(r store.Reader) error {
			return r.Get(store.Artifacts, manifest.SHA256, &a)
		}); err != nil {
			t.Fatalf("workload %s not registered: %v", entry.Name(), err)
		}
		if !a.Complete {
			t.Fatalf("workload %s is not complete: %+v", entry.Name(), a)
		}
	}
}
