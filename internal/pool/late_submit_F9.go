package pool

import (
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
	"context"
	"time"
)

func (e *Engine) snapshotExpiredLease(tx store.Tx, _ *Job, task *Task) error {
	if task == nil || task.Lease == nil || task.Lease.ExpiresAt.After(e.Now()) {
		return nil
	}
	var worker Worker
	if err := tx.Get(store.Workers, task.Lease.WorkerID, &worker); err != nil {
		return err
	}
	if task.ExpiredLease != nil && task.ExpiredLease.Lease.Generation == task.Lease.Generation {
		return nil
	}
	task.ExpiredLease = &LateLeaseEvidence{
		Lease:            *task.Lease,
		ContributorKeyID: worker.Owner,
		EnvelopeDigest:   task.EnvelopeDigest,
		ExpiredAt:        e.Now().UTC(),
	}
	return nil
}

func VerifyExpiredLeaseEnvelope(signed verify.SignedTaskEnvelope, keyring *verify.Keyring, evidence LateLeaseEvidence, now time.Time) error {
	digest := verify.SHA256(append(append([]byte(nil), signed.Payload...), signed.Signature...))
	if digest != evidence.EnvelopeDigest {
		return ErrConflict
	}
	return verify.VerifyTaskEnvelopeAllowExpired(signed, keyring, now)
}

type ArtifactPeer struct {
	PeerID         string
	ArtifactSHA256 string
	ExpiresAt      time.Time
}

type ArtifactSourceResolver interface {
	FindSources(ctx context.Context, poolID, artifactSHA256, requesterTabID string) ([]ArtifactPeer, error)
}

type SignedDewReceipt struct {
	ReceiptID   string
	Lease       Lease
	InputHashes []string
	IssuedAt    time.Time
	Deadline    time.Time
	KeyID       string
	Signature   []byte
}

type DewReceiptVerifier interface {
	VerifyReceipt(ctx context.Context, receipt SignedDewReceipt) error
}
