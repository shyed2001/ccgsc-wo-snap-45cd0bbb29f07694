// Path: internal/pool/artifact_owners_T100.go | Author: GLM for Claude Opus 5.5 | 2026-11-21
// What/why: T100 artifact ownership, owner decision 2026-11-21 option A: the
// content-addressed blob stays stored once per sha256 and every owner subject
// holds a reference row. A second developer uploading identical bytes joins the
// shared blob instead of being refused, reads and job use are allowed for every
// holder (or for a Store A admin), and a delete removes only the caller's
// reference while the blob is deleted when no reference remains.
package pool

import (
	"ccgsc/poolscore/internal/store"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// artifactOwnerKey is the record key of one (artifact, owner) reference.
// The artifact id leads, so the blob-centric flows (delete, count) can filter
// the bucket by their prefix.
func artifactOwnerKey(artifactID, owner string) string {
	return artifactID + ":" + owner
}

// addArtifactReference records that owner holds a reference to the artifact.
// The first reference wins: a repeated upload by the same subject keeps the
// original createdAt instead of rewriting the row.
func addArtifactReference(tx store.Tx, artifactID, owner string, at time.Time) error {
	var ref ArtifactOwner
	err := tx.Get(store.ArtifactOwners, artifactOwnerKey(artifactID, owner), &ref)
	if err == nil {
		return nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return tx.Put(store.ArtifactOwners, artifactOwnerKey(artifactID, owner), ArtifactOwner{
		ArtifactID: artifactID,
		Owner:      owner,
		CreatedAt:  at.UTC(),
	})
}

// ownerReferencedBytes sums the sizes of the artifacts that owner holds a
// reference to. It is the per-owner quota basis of the shared blobs (T100):
// the blob is counted once for every holder, so two owners sharing one blob
// each spend its size against their own cap. A reference whose artifact row
// was already removed contributes nothing.
func ownerReferencedBytes(tx store.Tx, owner string) (int64, error) {
	rows, err := tx.List(store.ArtifactOwners)
	if err != nil {
		return 0, err
	}
	var used int64
	for _, row := range rows {
		var ref ArtifactOwner
		if err := json.Unmarshal(row.Value, &ref); err != nil {
			return 0, err
		}
		if ref.Owner != owner {
			continue
		}
		var a Artifact
		if err := tx.Get(store.Artifacts, ref.ArtifactID, &a); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return 0, err
		}
		used += a.SizeBytes
	}
	return used, nil
}

// artifactReferenceCount counts the references that point at one artifact.
func artifactReferenceCount(tx store.Tx, artifactID string) (int64, error) {
	rows, err := tx.List(store.ArtifactOwners)
	if err != nil {
		return 0, err
	}
	var count int64
	for _, row := range rows {
		var ref ArtifactOwner
		if err := json.Unmarshal(row.Value, &ref); err != nil {
			return 0, err
		}
		if ref.ArtifactID == artifactID {
			count++
		}
	}
	return count, nil
}

// migrateArtifactOwnerReferences backfills the T100 reference rows for
// artifacts that were stored before the owner decision of 2026-11-21: the
// first uploader recorded on the artifact becomes that subject's first
// reference. It runs once per engine start and is idempotent, so an existing
// database keeps working across the upgrade on both the SQLite and the
// PostgreSQL store. Artifacts without an owner subject stay ownerless, exactly
// as the internal engine uploads created them.
func (e *Engine) migrateArtifactOwnerReferences() error {
	return e.Store.Update(context.Background(), func(tx store.Tx) error {
		rows, err := tx.List(store.Artifacts)
		if err != nil {
			return err
		}
		for _, row := range rows {
			var a Artifact
			if err := json.Unmarshal(row.Value, &a); err != nil {
				return err
			}
			if a.Owner == "" || a.ArtifactID == "" {
				continue
			}
			var ref ArtifactOwner
			err := tx.Get(store.ArtifactOwners, artifactOwnerKey(a.ArtifactID, a.Owner), &ref)
			if err == nil {
				continue
			}
			if !errors.Is(err, store.ErrNotFound) {
				return err
			}
			if err := addArtifactReference(tx, a.ArtifactID, a.Owner, e.Now().UTC()); err != nil {
				return err
			}
		}
		return nil
	})
}

// CanUseArtifacts reports whether the caller may read or use every listed
// artifact (T100). Store A admins and directors bypass the rule; a developer
// must hold a reference for each artifact. A missing artifact answers
// store.ErrNotFound, the same answer the surfaces gave before T100.
func (e *Engine) CanUseArtifacts(ctx context.Context, subject string, admin bool, ids []string) error {
	if admin {
		return nil
	}
	return e.Store.View(ctx, func(r store.Reader) error {
		for _, id := range ids {
			var a Artifact
			if err := r.Get(store.Artifacts, id, &a); err != nil {
				return err
			}
			if subject == "" {
				return fmt.Errorf("%w: artifact %s requires an owner reference (T100)", ErrForbidden, id)
			}
			var ref ArtifactOwner
			if err := r.Get(store.ArtifactOwners, artifactOwnerKey(id, subject), &ref); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return fmt.Errorf("%w: artifact %s requires an owner reference (T100)", ErrForbidden, id)
				}
				return err
			}
		}
		return nil
	})
}

// DeleteArtifactResult reports the outcome of one reference delete (T100).
type DeleteArtifactResult struct {
	ArtifactID    string `json:"artifactId"`
	SHA256        string `json:"sha256"`
	Removed       bool   `json:"removed"`
	BlobDeleted   bool   `json:"blobDeleted"`
	RemainingRefs int64  `json:"remainingReferences"`
}

// DeleteArtifactReference removes only the caller's reference (T100). The blob
// is deleted when no reference remains; a reference held by another owner
// keeps the blob readable and usable. A caller without a reference is refused,
// so a repeated delete cannot remove somebody else's share.
func (e *Engine) DeleteArtifactReference(ctx context.Context, id, owner string) (DeleteArtifactResult, error) {
	var out DeleteArtifactResult
	var a Artifact
	err := e.Store.Update(ctx, func(tx store.Tx) error {
		if err := tx.Get(store.Artifacts, id, &a); err != nil {
			return err
		}
		out = DeleteArtifactResult{ArtifactID: a.ArtifactID, SHA256: a.SHA256}
		key := artifactOwnerKey(id, owner)
		var ref ArtifactOwner
		err := tx.Get(store.ArtifactOwners, key, &ref)
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: no artifact reference to delete (T100)", ErrForbidden)
		}
		if err != nil {
			return err
		}
		deleter, ok := tx.(store.DeleteTx)
		if !ok {
			return fmt.Errorf("%w: this store cannot delete artifact references (T100)", ErrUnsupported)
		}
		if err := deleter.Delete(store.ArtifactOwners, key); err != nil {
			return err
		}
		out.Removed = true
		remaining, err := artifactReferenceCount(tx, id)
		if err != nil {
			return err
		}
		out.RemainingRefs = remaining
		if remaining == 0 {
			if err := deleter.Delete(store.Artifacts, id); err != nil {
				return err
			}
			out.BlobDeleted = true
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	if out.Removed {
		e.Log.Info("artifact.reference_deleted", "artifactId", id, "owner", owner, "remainingReferences", out.RemainingRefs, "blobDeleted", out.BlobDeleted)
	}
	return out, nil
}
