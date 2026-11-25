// MVPW-F, Codex, 2026-11-19: issue receipts for opt-in offline batches; preserve
// existing late-result fencing and require original evidence for peer relays.
// R11 Part E (owner decision 2026-11-21): dew defaults ON. A job that names no
// dew option, or an empty one, gets enabled receipts; only an explicit
// {"dew":{"enabled":false}} turns the measure off. Part E addendum (DeepSeek
// R14 review, 2026-11-22): the flag is a *bool (absent = ON), a parse error
// fails closed, an active lease never fails on receipt expiry, a lease claimed
// before the change is accepted until its TTL, and a rejection leaves a dead
// letter.
// F12 (owner grill 2026-11-24, WO section 16, BINDING - supersedes the 24 h
// design): receipts are PER TASK and run-capped (G-F12-2); the window is
// min(Dew window, run end) with a 10 minute default, an admin range of 1-30
// minutes and a per-app developer override within the admin max (G-F12-1); the
// task is re-assigned only at receipt expiry (G-F12-5); the first valid result
// settles and a later valid result earns compute-time credit only while EVERY
// result is recorded for the developer (G-F12-4).
package pool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
)

// DefaultDewReceiptWindowSeconds is the receipt window of a job that names no
// dew window and of a pool whose admin set nothing (F12 owner decision
// 2026-11-24, G-F12-1: default 10 minutes; the retired default was 24 h).
const DefaultDewReceiptWindowSeconds int64 = 600

// errDewRejected marks a refused submission as a dew finding, so the caller can
// record it in the dead-letter store after the rolled-back transaction.
var errDewRejected = errors.New("dew receipt rejected")

// dewOptions is the parsed dew option of one job. Enabled is a pointer on
// purpose: a job that names no dew object, an empty one, or {"enabled":null}
// keeps the default ON, and only an explicit false turns the measure off.
type dewOptions struct {
	Enabled              *bool `json:"enabled"`
	ReceiptWindowSeconds int64 `json:"receiptWindowSeconds"`
	// present reports that the job carried a dew object at all.
	present bool
	// windowSet reports that the job named receiptWindowSeconds itself, so the
	// value LOWERS the admin/app window (G-F12-1: each layer can only
	// shorten) instead of being the default answer.
	windowSet bool
}

// on reports the effective switch: absent is ON, explicit true is ON, explicit
// false is OFF.
func (d dewOptions) on() bool {
	return d.Enabled == nil || *d.Enabled
}

// dewOff is the explicit false a failed parse answers. The default is ON, so the
// only safe value on an unreadable option is a real "off", both as the returned
// struct and as the error.
var dewOff = false

// parseDewOptions reads the dew object of a job. A job with no option at all, an
// empty object or a null dew object answers the default ON with the default
// window. A malformed option is ErrInvalid and answers an explicit OFF, so an
// unreadable option can never enable the measure by accident. An explicit
// window outside the owner range (1-30 minutes, G-F12-1) is a malformed option:
// it fails closed instead of widening the window.
func parseDewOptions(raw json.RawMessage) (dewOptions, error) {
	var fields struct {
		Dew json.RawMessage `json:"dew"`
	}
	if len(raw) == 0 {
		return dewOptions{ReceiptWindowSeconds: DefaultDewReceiptWindowSeconds}, nil
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return dewOptions{Enabled: &dewOff}, ErrInvalid
	}
	if len(fields.Dew) == 0 || string(fields.Dew) == "null" {
		return dewOptions{ReceiptWindowSeconds: DefaultDewReceiptWindowSeconds}, nil
	}
	var wire struct {
		Enabled              *bool  `json:"enabled"`
		ReceiptWindowSeconds *int64 `json:"receiptWindowSeconds"`
	}
	dec := json.NewDecoder(bytes.NewReader(fields.Dew))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return dewOptions{Enabled: &dewOff}, ErrInvalid
	}
	d := dewOptions{Enabled: wire.Enabled, present: true, windowSet: wire.ReceiptWindowSeconds != nil}
	if wire.ReceiptWindowSeconds != nil {
		d.ReceiptWindowSeconds = *wire.ReceiptWindowSeconds
	}
	if d.ReceiptWindowSeconds == 0 {
		d.ReceiptWindowSeconds = DefaultDewReceiptWindowSeconds
	}
	if d.ReceiptWindowSeconds < DewMinWindowSeconds || d.ReceiptWindowSeconds > verify.MaxTaskReceiptWindowSeconds {
		return dewOptions{Enabled: &dewOff}, ErrInvalid
	}
	return d, nil
}

// dewEnabled answers the envelope requirement of engine.go and batch_F1.go. The
// measure is on unless the job explicitly turns it off; an unreadable option is
// off, never on.
func dewEnabled(j *Job) bool {
	if j == nil {
		return false
	}
	d, err := parseDewOptions(j.Request.Options)
	return err == nil && d.on()
}

// attachDewReceipt issues ONE signed receipt PER LEASED TASK (F12, G-F12-2).
// It is skipped only when the job carries a dew object AND that object
// explicitly disables the measure; a job with no dew option now gets a receipt
// like any other. The window is the admin pool window, lowered by the app
// override and by the job's own option - each layer can only shorten
// (G-F12-1) - read from the durable policy at issue time. The claim path marks
// the receipted tasks (Task.DewReceipt), so a later submission is held to the
// receipt it was issued.
// rd is the claim transaction. Opening Store.View from inside Store.Update is
// forbidden: the receipt window and the contributor owner are read on the same
// transaction that writes Task.Dew.
func (e *Engine) attachDewReceipt(rd store.Reader, j *Job, members []Assignment) error {
	d, err := parseDewOptions(j.Request.Options)
	if err != nil {
		return err
	}
	if d.present && !d.on() {
		return nil
	}
	if len(members) == 0 {
		return nil
	}
	if rd == nil {
		return fmt.Errorf("%w: dew receipt has no store reader", ErrInvalid)
	}
	jobRequested := int64(0)
	if d.windowSet {
		jobRequested = d.ReceiptWindowSeconds
	}
	window, err := effectiveDewWindowTx(rd, j, jobRequested)
	if err != nil {
		return err
	}
	// Enrichment only: the submit path authenticates the contributor, so a
	// missing worker row must never fail the claim (F5: logged, never
	// silent). An anonymous install leaves ContributorOwner empty.
	var contributor string
	var worker Worker
	if er := rd.Get(store.Workers, members[0].Lease.WorkerID, &worker); er != nil {
		e.Log.Warn("dew.contributorLookupFailed", "workerId", members[0].Lease.WorkerID, "error", er)
	} else {
		contributor = worker.Owner
	}
	now := e.Now().UTC()
	for i := range members {
		a := &members[i]
		tokenHash := verify.SHA256([]byte(a.Lease.Token))
		r := verify.LeaseReceipt{
			Version:          verify.TaskReceiptVersion,
			ReceiptID:        verify.TaskReceiptID(j.JobID, a.Lease.TaskID, a.Lease.Generation, tokenHash),
			PoolID:           j.PoolID,
			JobID:            j.JobID,
			TaskID:           a.Lease.TaskID,
			AppID:            j.AppID,
			DeveloperOwner:   j.Owner,
			ContributorOwner: contributor,
			WorkerID:         a.Lease.WorkerID,
			Generation:       a.Lease.Generation,
			LeaseTokenSHA256: tokenHash,
			EnvelopeSHA256:   dewEnvelopeDigest(a.TaskEnvelope),
			TaskIDs:          []string{a.Lease.TaskID},
			InputHashes:      []string{verify.SHA256(a.Task.Input)},
			IssuedAt:         now.Unix(),
			Deadline:         now.Unix() + window,
			WindowSeconds:    window,
		}
		e.mu.RLock()
		signed, err := verify.SignLeaseReceipt(r, e.TaskKey, e.TaskKeyID)
		e.mu.RUnlock()
		if err != nil {
			return err
		}
		a.LeaseReceipt = &signed
	}
	return nil
}

// Receipts never replace the original signed envelope or server-side fencing.
// Peer transport is outside this slice: a relayed submission must authenticate
// as the original contributor and retain that contributor's worker/lease identity.
//
// leaseExpires is the end of the lease the submission presents. Part E addendum
// (still true under F12): an active lease must not fail because its receipt
// window closed, so a receipt that is expired while the lease is still live
// proves nothing and is accepted. A lease claimed before dew default ON carries
// no receipt at all (task.DewReceipt is false) and is accepted until its own
// TTL instead of being rejected.
// F12: the receipt is per task, so a v2 receipt is additionally bound to the
// derived receipt id, pool, job, task, worker, generation, lease token hash and
// the exact envelope presented at claim time; and every receipt of a run dies
// when the run ends or is cancelled (G-F12-2), whatever the signed deadline
// says - the run end cannot be signed in advance.
func (e *Engine) verifyDewSubmission(j *Job, task *Task, q Submission, leaseExpires time.Time) error {
	d, err := parseDewOptions(j.Request.Options)
	perJobOn := err == nil && d.on()
	required := perJobOn || q.Offline || q.PeerRelayed
	if !required && q.LeaseReceipt == nil {
		return nil
	}
	if q.LeaseReceipt == nil {
		if !task.DewReceipt && !q.Offline && !q.PeerRelayed {
			return nil
		}
		return dewReject("missing")
	}
	if dewRunEnded(j) {
		return dewReject("run ended")
	}
	r := *q.LeaseReceipt
	if err := verify.VerifyLeaseReceipt(r, e.Verifier.Keyring(), e.Now()); err != nil {
		if !(errors.Is(err, verify.ErrExpired) && leaseExpires.After(e.Now())) {
			return dewReject(err.Error())
		}
	}
	if err := verify.VerifyTaskEnvelopeAllowExpired(q.TaskEnvelope, e.Verifier.Keyring(), e.Now()); err != nil {
		return dewReject(err.Error())
	}
	env, err := verify.DecodeTaskEnvelope(q.TaskEnvelope)
	if err != nil {
		return dewReject("envelope invalid")
	}
	if r.Version == verify.TaskReceiptVersion {
		tokenHash := verify.SHA256([]byte(q.Lease.Token))
		if r.ReceiptID != verify.TaskReceiptID(j.JobID, q.Lease.TaskID, q.Lease.Generation, tokenHash) ||
			r.PoolID != j.PoolID || r.JobID != j.JobID ||
			r.TaskID != q.Lease.TaskID || r.WorkerID != q.Lease.WorkerID ||
			r.Generation != q.Lease.Generation ||
			r.LeaseTokenSHA256 != tokenHash ||
			r.EnvelopeSHA256 != dewEnvelopeDigest(q.TaskEnvelope) ||
			env.LeaseToken != q.Lease.Token {
			return dewReject("original envelope/lease binding")
		}
	} else if r.ReceiptID != q.Lease.Token || env.LeaseToken != r.ReceiptID {
		return dewReject("original envelope/lease binding")
	}
	if env.WorkerID != q.Lease.WorkerID || env.JobID != j.JobID ||
		env.TaskID != q.Lease.TaskID || env.ArtifactSHA256 != moduleDigest(j.Request.ModuleArtifactIDs) {
		return dewReject("original envelope/lease binding")
	}
	for i, id := range r.TaskIDs {
		if id == env.TaskID && r.InputHashes[i] == env.InputSHA256 {
			return nil
		}
	}
	return dewReject("task/input binding")
}

// dewReject builds a refusal that both wraps ErrConflict and identifies itself as
// a dew finding, so the submit path can record it as a dead letter.
func dewReject(reason string) error {
	return fmt.Errorf("%w: dew receipt: %w: %s", ErrConflict, errDewRejected, reason)
}

// recordDewDeadLetter writes a refused dew submission to the dead-letter store.
// The submission transaction is already rolled back, so this runs on its own and
// a failure is logged rather than masking the refusal.
func (e *Engine) recordDewDeadLetter(ctx context.Context, q Submission) {
	err := e.Store.Update(ctx, func(tx store.Tx) error {
		return tx.Put(store.DeadLetters, auth.ID(), submissionDeadLetter(q, "dew receipt rejected", e.Now()))
	})
	if err != nil {
		e.Log.Warn("dew.deadLetterFailed", "jobId", q.Lease.JobID, "taskId", q.Lease.TaskID, "error", err)
	}
}

// Keep refusal evidence without persisting reusable lease tokens, signed
// envelopes/receipts, or arbitrary developer output/error strings. Reason must
// be a server-owned category: verifier errors can contain an untrusted key ID.
// Hashes allow correlation with the original evidence without copying it.
func submissionDeadLetter(q Submission, reason string, now time.Time) any {
	var submissionHash string
	if raw, err := json.Marshal(q); err == nil {
		submissionHash = verify.SHA256(raw)
	}
	return struct {
		Reason           string
		JobID            string
		TaskID           string
		WorkerID         string
		Generation       uint64
		LeaseTokenSHA256 string
		OutputSHA256     string
		SubmissionSHA256 string
		At               time.Time
	}{reason, q.Lease.JobID, q.Lease.TaskID, q.Lease.WorkerID,
		q.Lease.Generation, verify.SHA256([]byte(q.Lease.Token)),
		verify.SHA256(q.Output), submissionHash, now.UTC()}
}
