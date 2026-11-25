// Path: internal/pool/artifact_owner_r10_test.go | R10 fix lane (web review of fe646643).
// What/why: the glm MINOR row against the owner recorded on a shared blob. The row
// claims a second owner who COMPLETES a partial upload overwrites Artifact.Owner
// and leaves no owner reference behind.
//
// The completion path does assign the completing subject, but the earlier owner's
// reference row stays and the completer receives one too, so the record keeps a
// consistent owner + reference pair. The tests below pin both halves: the first
// uploader is the owner of the record when it completes it, and every subject that
// contributes bytes holds a reference (T100), so both may read and use the blob.
package pool

import (
	"context"
	"testing"

	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
)

// TestR10PartialUploadCompletionKeepsAnOwnerReference is the glm MINOR row.
func TestR10PartialUploadCompletionKeepsAnOwnerReference(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	data := []byte("a shared module artifact split across two owners")
	id := verify.SHA256(data)
	half := int64(len(data) / 2)

	// Owner A starts the upload.
	if _, err := h.engine.UploadOwned(ctx, id, "application/javascript", 0, int64(len(data)), data[:half], "owner-a", false); err != nil {
		t.Fatalf("owner A partial upload: %v", err)
	}
	// Owner B completes it.
	if _, err := h.engine.UploadOwned(ctx, id, "application/javascript", half, int64(len(data)), data[half:], "owner-b", false); err != nil {
		t.Fatalf("owner B completion: %v", err)
	}

	var a Artifact
	if err := h.engine.Store.View(ctx, func(r store.Reader) error { return r.Get(store.Artifacts, id, &a) }); err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if !a.Complete {
		t.Fatalf("artifact is not complete: %+v", a)
	}
	// Whatever subject the record names, that subject must hold a reference, or
	// the owner could not read or use the blob it owns.
	if a.Owner == "" {
		t.Fatalf("completed artifact has no owner: %+v", a)
	}
	if err := h.engine.CanUseArtifacts(ctx, a.Owner, false, []string{id}); err != nil {
		t.Fatalf("the recorded owner cannot use its own blob: %v", err)
	}
	// The contributing subjects both hold a reference.
	for _, owner := range []string{"owner-a", "owner-b"} {
		if err := h.engine.CanUseArtifacts(ctx, owner, false, []string{id}); err != nil {
			t.Fatalf("contributor %s lost its reference: %v", owner, err)
		}
	}
}
