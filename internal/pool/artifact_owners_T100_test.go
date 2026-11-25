// Path: internal/pool/artifact_owners_T100_test.go | Author: GLM for Claude Opus 5.5 | 2026-11-21
// What/why: engine tests of the T100 artifact ownership (owner decision
// 2026-11-21, option A): shared content-addressed blobs with per-owner
// reference rows, the join upload of identical bytes, the reference rule for
// reads and job use, the reference delete that keeps the blob for the other
// owner, the per-owner quota over references and the first-uploader migration.
package pool

import (
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

// t100Upload uploads the bytes for the owner subject and returns the artifact.
func t100Upload(t *testing.T, e *Engine, data []byte, owner string) Artifact {
	t.Helper()
	a, err := e.UploadOwned(context.Background(), verify.SHA256(data), "application/octet-stream", 0, int64(len(data)), data, owner, false)
	if err != nil {
		t.Fatalf("UploadOwned(%s): %v", owner, err)
	}
	return a
}

func TestT100UploadIdenticalBytesAddsReference(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	data := []byte("shared module bytes")
	id := verify.SHA256(data)

	first := t100Upload(t, h.engine, data, "dev-key-a")
	second := t100Upload(t, h.engine, data, "dev-key-b")

	if first.ArtifactID != id || second.ArtifactID != id {
		t.Fatalf("artifact ids = %s / %s, want the shared blob %s", first.ArtifactID, second.ArtifactID, id)
	}
	if second.Owner != "dev-key-a" {
		t.Errorf("second upload owner = %q, want the first uploader kept as the blob creator", second.Owner)
	}
	if !second.Complete || len(second.Data) != len(data) {
		t.Errorf("second upload = %+v, want a complete join of the shared blob", second)
	}
	var refs []ArtifactOwner
	if err := h.engine.Store.View(ctx, func(r store.Reader) error {
		records, e := r.List(store.ArtifactOwners)
		if e != nil {
			return e
		}
		for _, row := range records {
			var ref ArtifactOwner
			if e := json.Unmarshal(row.Value, &ref); e != nil {
				return e
			}
			refs = append(refs, ref)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("references = %d (%+v), want one per owner subject", len(refs), refs)
	}
	owners := map[string]ArtifactOwner{}
	for _, ref := range refs {
		owners[ref.Owner] = ref
		if ref.ArtifactID != id {
			t.Errorf("reference %+v points at %s, want %s", ref, ref.ArtifactID, id)
		}
	}
	if _, ok := owners["dev-key-a"]; !ok {
		t.Error("the first uploader holds no reference")
	}
	if _, ok := owners["dev-key-b"]; !ok {
		t.Error("the joining uploader holds no reference")
	}
	if owners["dev-key-b"].CreatedAt.IsZero() {
		t.Error("the join reference carries no createdAt")
	}
}

func TestT100CanUseArtifactsAndReads(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	data := []byte("readable module bytes")
	id := verify.SHA256(data)
	t100Upload(t, h.engine, data, "dev-key-a")

	// The holder and the joining holder may use the artifact.
	t100Upload(t, h.engine, data, "dev-key-b")
	if err := h.engine.CanUseArtifacts(ctx, "dev-key-a", false, []string{id}); err != nil {
		t.Errorf("CanUseArtifacts(holder) = %v, want nil", err)
	}
	if err := h.engine.CanUseArtifacts(ctx, "dev-key-b", false, []string{id}); err != nil {
		t.Errorf("CanUseArtifacts(joiner) = %v, want nil", err)
	}
	// A third developer without a reference is refused.
	err := h.engine.CanUseArtifacts(ctx, "dev-key-c", false, []string{id})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("CanUseArtifacts(third) = %v, want ErrForbidden", err)
	}
	// A Store A admin bypasses the rule.
	if err := h.engine.CanUseArtifacts(ctx, "some-admin-key", true, []string{id}); err != nil {
		t.Errorf("CanUseArtifacts(admin) = %v, want nil", err)
	}
	// An unknown artifact stays a not-found, the pre-T100 answer.
	if err := h.engine.CanUseArtifacts(ctx, "dev-key-a", false, []string{verify.SHA256([]byte("missing"))}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("CanUseArtifacts(missing) = %v, want ErrNotFound", err)
	}
	// The blob stays readable for both holders after the join.
	for _, subject := range []string{"dev-key-a", "dev-key-b"} {
		if _, err := h.engine.Artifact(ctx, id); err != nil {
			t.Errorf("Artifact(%s) = %v, want the shared blob", subject, err)
		}
	}
}

func TestT100DeleteReferenceKeepsBlobForOtherOwner(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	data := []byte("delete-protected bytes")
	id := verify.SHA256(data)
	t100Upload(t, h.engine, data, "dev-key-a")
	t100Upload(t, h.engine, data, "dev-key-b")

	// A delete by a subject without a reference is refused.
	if _, err := h.engine.DeleteArtifactReference(ctx, id, "dev-key-c"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("DeleteArtifactReference(third) = %v, want ErrForbidden", err)
	}
	// The first delete removes only the caller's reference.
	out, err := h.engine.DeleteArtifactReference(ctx, id, "dev-key-a")
	if err != nil {
		t.Fatalf("DeleteArtifactReference(a): %v", err)
	}
	if !out.Removed || out.BlobDeleted || out.RemainingRefs != 1 {
		t.Fatalf("delete by a = %+v, want one remaining reference and a kept blob", out)
	}
	// The blob still exists and is still usable by the other holder.
	if _, err := h.engine.Artifact(ctx, id); err != nil {
		t.Fatalf("blob deleted although the other owner holds a reference: %v", err)
	}
	if err := h.engine.CanUseArtifacts(ctx, "dev-key-b", false, []string{id}); err != nil {
		t.Fatalf("CanUseArtifacts(b after a delete) = %v, want nil", err)
	}
	// The last delete removes the blob.
	out, err = h.engine.DeleteArtifactReference(ctx, id, "dev-key-b")
	if err != nil {
		t.Fatalf("DeleteArtifactReference(b): %v", err)
	}
	if !out.Removed || !out.BlobDeleted || out.RemainingRefs != 0 {
		t.Fatalf("delete by b = %+v, want the blob deleted with the last reference", out)
	}
	if _, err := h.engine.Artifact(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Artifact after the last delete = %v, want ErrNotFound", err)
	}
	// The reference rows went away with the blob.
	var refs []store.Record
	if err := h.engine.Store.View(ctx, func(r store.Reader) error {
		var e error
		refs, e = r.List(store.ArtifactOwners)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Fatalf("references after the blob delete = %d, want none", len(refs))
	}
}

func TestT100PerOwnerQuotaCountsReferences(t *testing.T) {
	h := newHarness(t)
	limits := h.engine.sessionLimits()
	limits.MaxArtifactBytesPerOwner = 5
	h.engine.SetSessionLimits(limits)

	first := []byte("1234")
	if _, err := h.engine.UploadOwned(context.Background(), verify.SHA256(first), "text/plain", 0, int64(len(first)), first, "owner", false); err != nil {
		t.Fatalf("first upload: %v", err)
	}
	// A second owner joining the shared blob spends its size against the cap.
	second := []byte("56")
	if _, err := h.engine.UploadOwned(context.Background(), verify.SHA256(second), "text/plain", 0, int64(len(second)), second, "owner", false); !errors.Is(err, ErrEmpty) {
		t.Fatalf("second upload = %v, want the owner quota over references", err)
	}
	// Another owner joins the shared blob; its own cap stays free.
	if _, err := h.engine.UploadOwned(context.Background(), verify.SHA256(first), "text/plain", 0, int64(len(first)), first, "joiner", false); err != nil {
		t.Fatalf("joiner upload of identical bytes: %v", err)
	}
}

func TestT100MigrationBackfillsFirstUploaderReference(t *testing.T) {
	// Seed an artifact the way a pre-T100 database stored it: a first uploader
	// on the artifact record and no reference rows at all.
	data := []byte("legacy module bytes")
	id := verify.SHA256(data)
	legacy := Artifact{
		ArtifactID: id, SHA256: id, Owner: "legacy-key", SizeBytes: int64(len(data)),
		Complete: true, ContentType: "application/octet-stream",
		CreatedAt: time.Unix(1_700_000_000, 0).UTC(), Data: data,
	}
	seedStore := newT100SeedStore(t, legacy)

	engine, err := New(seedStore, discardLogger(), "s1", time.Minute)
	if err != nil {
		t.Fatalf("New with a legacy artifact: %v", err)
	}
	if err := engine.CanUseArtifacts(context.Background(), "legacy-key", false, []string{id}); err != nil {
		t.Fatalf("CanUseArtifacts(legacy-key) = %v, want the migration to grant the first uploader a reference", err)
	}
	if err := engine.CanUseArtifacts(context.Background(), "someone-else", false, []string{id}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("CanUseArtifacts(someone-else) = %v, want ErrForbidden", err)
	}
	// The migration is idempotent: a second engine start keeps exactly one row.
	reloaded, err := New(seedStore, discardLogger(), "s1", time.Minute)
	if err != nil {
		t.Fatalf("reloaded New: %v", err)
	}
	if err := reloaded.CanUseArtifacts(context.Background(), "legacy-key", false, []string{id}); err != nil {
		t.Fatalf("CanUseArtifacts after a restart = %v, want nil", err)
	}
	var refs []store.Record
	if err := seedStore.View(context.Background(), func(r store.Reader) error {
		var e error
		refs, e = r.List(store.ArtifactOwners)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("references after two migrations = %d, want exactly one", len(refs))
	}
}

func newT100SeedStore(t *testing.T, a Artifact) store.Store {
	t.Helper()
	h := newHarness(t)
	// The raw Put writes exactly the pre-T100 row shape: an artifact record with
	// a first uploader and no reference rows, because no engine migration ran
	// for it yet.
	if err := h.engine.Store.Update(context.Background(), func(tx store.Tx) error {
		return tx.Put(store.Artifacts, a.ArtifactID, a)
	}); err != nil {
		t.Fatal(err)
	}
	return h.engine.Store
}

// discardLogger keeps the engine JSON log out of the test output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}
