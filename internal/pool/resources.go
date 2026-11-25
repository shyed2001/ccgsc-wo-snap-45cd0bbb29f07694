// Path: internal/pool/resources.go | Author: Codex for Claude Opus 5.5 | 2026-11-17
// What/why: immutable chunked artifacts, tab registration and lease renewals; WWPP-R32.7, R37.1.
package pool

import (
	"bytes"
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func gpuAllowedOnWorker(w Worker, cfg GPUConfig) bool {
	if cfg.OnBatteryAllowed {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(w.DeviceClass)) {
	case "phone", "mobile", "laptop":
		return w.Charging
	default:
		return true
	}
}

const MaxArtifactBytes = 32 * 1024 * 1024
const MaxContentTypeBytes = 128

func validContentType(v string) bool {
	if v == "" {
		return true
	}
	if len(v) > MaxContentTypeBytes {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "application/octet-stream", "application/json", "application/wasm", "application/zip", "text/plain",
		"application/javascript", "text/javascript", "application/ecmascript":
		return true
	default:
		return false
	}
}

func (e *Engine) Upload(ctx context.Context, id, contentType string, offset, total int64, chunk []byte) (Artifact, error) {
	return e.UploadOwned(ctx, id, contentType, offset, total, chunk, "", false)
}

// UploadOwned uploads a chunk and runs the F8 upload scan when the artifact completes.
// The owner and the owner-code flag come from the authenticated caller: Store A
// owner/admin/director code is auto-approved by the review gate (R13.1).
// T100 (owner decision 2026-11-21, option A): the blob stays stored once per
// sha256 and the upload of identical bytes by another owner subject joins that
// blob by adding a per-owner reference row instead of being refused. The first
// uploader stays the Owner of the artifact record. The per-owner byte quota
// counts the artifacts the subject holds a reference to, so every holder
// spends the shared blob against its own cap. A failed scan is reported after
// the transaction commits, so the stored artifact keeps its scan report as the
// evidence of the admin review queue.
func (e *Engine) UploadOwned(ctx context.Context, id, contentType string, offset, total int64, chunk []byte, owner string, ownerCode bool) (Artifact, error) {
	var a Artifact
	var scanErr error
	h, err := hex.DecodeString(id)
	if err != nil || len(h) != 32 || strings.ToLower(id) != id || !validContentType(contentType) || offset < 0 || total < 0 || total > MaxArtifactBytes || int64(len(chunk)) > total || offset > total-int64(len(chunk)) {
		return a, ErrInvalid
	}
	err = e.Store.Update(ctx, func(tx store.Tx) error {
		er := tx.Get(store.Artifacts, id, &a)
		if errors.Is(er, store.ErrNotFound) {
			if offset != 0 {
				return ErrConflict
			}
			rows, listErr := tx.List(store.Artifacts)
			if listErr != nil {
				return listErr
			}
			used := int64(0)
			for _, row := range rows {
				var existing Artifact
				if jsonErr := json.Unmarshal(row.Value, &existing); jsonErr != nil {
					return jsonErr
				}
				used += existing.SizeBytes
			}
			if used+total > e.sessionLimits().MaxArtifactBytesTotal {
				return ErrEmpty
			}
			if owner != "" {
				// T100: the per-owner quota counts the shared blobs this subject
				// already holds a reference to.
				referenced, refErr := ownerReferencedBytes(tx, owner)
				if refErr != nil {
					return refErr
				}
				if referenced+total > e.sessionLimits().MaxArtifactBytesPerOwner {
					return ErrEmpty
				}
			}
			a = Artifact{ArtifactID: id, SHA256: id, Owner: owner, SizeBytes: total, ContentType: contentType, CreatedAt: e.Now().UTC(), Data: []byte{}}
			if owner != "" {
				if err := addArtifactReference(tx, id, owner, e.Now().UTC()); err != nil {
					return err
				}
			}
		} else if er != nil {
			return er
		} else if owner != "" {
			// T100: the artifact already exists, so this upload joins the shared
			// blob. The quota is charged once for the new reference, before any
			// byte of the join is accepted; a holder of the reference pays
			// nothing again.
			var ref ArtifactOwner
			refErr := tx.Get(store.ArtifactOwners, artifactOwnerKey(id, owner), &ref)
			if errors.Is(refErr, store.ErrNotFound) {
				referenced, listErr := ownerReferencedBytes(tx, owner)
				if listErr != nil {
					return listErr
				}
				if referenced+a.SizeBytes > e.sessionLimits().MaxArtifactBytesPerOwner {
					return ErrEmpty
				}
				// R10 glm: a subject that JOINS an existing blob holds a reference
				// from that moment, exactly like the first uploader. Without the row
				// a contributor that completed another subject's partial upload owned
				// a record it could not read or use (T100).
				if err := addArtifactReference(tx, id, owner, e.Now().UTC()); err != nil {
					return err
				}
			} else if refErr != nil {
				return refErr
			}
		}
		if a.SizeBytes != total {
			return ErrConflict
		}
		end := offset + int64(len(chunk))
		if offset < a.NextOffset || a.Complete {
			if end <= a.NextOffset && bytes.Equal(a.Data[offset:end], chunk) {
				// An idempotent replay of a rejected artifact stays rejected.
				if a.ReviewState == ReviewRejected {
					scanErr = fmt.Errorf("%w: %s", ErrInvalid, a.ReviewReason)
				}
				// T100: the replay of the complete object by a new owner subject
				// is the join that adds that subject's reference.
				if owner != "" && end == total {
					if err := addArtifactReference(tx, id, owner, e.Now().UTC()); err != nil {
						return err
					}
				}
				return nil
			}
			return ErrConflict
		}
		if offset != a.NextOffset {
			return ErrConflict
		}
		a.Data = append(a.Data, chunk...)
		a.NextOffset = end
		a.ReceivedBytes = end
		if end == total {
			er = verify.Check(a.Data, id)
			e.Log.Info("tamper.artifact", "artifactId", id, "valid", er == nil)
			if er != nil {
				return ErrInvalid
			}
			a.Complete = true
			// The F8 upload scan runs on the complete bytes. The artifact is
			// stored even when the scan fails, so the admin review queue keeps
			// the report as its evidence.
			scanErr = e.applyUploadScan(&a)
			if ownerCode {
				now := e.Now().UTC()
				a.ReviewedAt = &now
				if scanErr == nil {
					a.ReviewState = ReviewApproved
					a.ReviewReason = "owner code auto-approved (R13.1)"
				}
			}
			// R10 glm: the FIRST uploader stays the owner of the record (the same
			// T100 rule the join path documents). A subject that completes another
			// subject's partial upload holds a reference, it does not take the blob.
			if owner != "" && a.Owner == "" {
				a.Owner = owner
			}
			return tx.Put(store.Artifacts, id, a)
		}
		return tx.Put(store.Artifacts, id, a)
	})
	if err != nil {
		a = Artifact{}
		return a, err
	}
	if scanErr != nil {
		a.Data = nil
		return a, scanErr
	}
	return a, nil
}
func (e *Engine) Artifact(ctx context.Context, id string) (Artifact, error) {
	var a Artifact
	err := e.Store.View(ctx, func(r store.Reader) error { return r.Get(store.Artifacts, id, &a) })
	if err == nil && !a.Complete {
		err = ErrConflict
	}
	if err == nil {
		err = verify.Check(a.Data, id)
		e.Log.Info("tamper.artifact.load", "artifactId", id, "valid", err == nil)
	}
	if err != nil {
		a = Artifact{}
	}
	return a, err
}

// AnonymousMaxTabsPerSubject caps the tabs one anonymous subject registers per
// pool (T110). A browser tab registers once per pool; 2 leaves room for a reload
// that registers before the old tab is reaped.
const AnonymousMaxTabsPerSubject = 2

// maxBrowserTabIDBytes bounds the client-reported browser tab id (R10).
const maxBrowserTabIDBytes = 128

func (e *Engine) Register(ctx context.Context, p, owner string, w Worker) (Worker, error) {
	if len(w.EgressConsentIDs) > 256 {
		return Worker{}, ErrInvalid
	}
	for _, id := range w.EgressConsentIDs {
		if len(id) != 64 {
			return Worker{}, ErrInvalid
		}
		for _, ch := range id {
			if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
				return Worker{}, ErrInvalid
			}
		}
	}
	w.EgressConsent = w.EgressConsent && len(w.EgressConsentIDs) > 0
	limits := e.sessionLimits()
	if w.HostID == "" || w.Slots < 1 || w.Slots > limits.MaxSlotsPerWorker {
		return w, ErrInvalid
	}
	if w.GPUSlots < 0 || w.GPUSlots > 2 || (!w.GPU && (w.GPUSlots != 0 || len(w.GPUs) != 0)) {
		return w, ErrInvalid
	}
	if w.GPU && w.GPUSlots == 0 {
		w.GPUSlots = 1
	}
	if len(w.GPUs) > 2 {
		return w, ErrInvalid
	}
	for _, gpu := range w.GPUs {
		if len(gpu.Vendor) > 64 || len(gpu.Architecture) > 64 {
			return w, ErrInvalid
		}
	}
	if w.RunnerPin != "" {
		pin, er := normalizeRunnerPin(w.RunnerPin)
		if er != nil {
			return w, er
		}
		w.RunnerPin = pin
	}
	// R10: the browser tab id is a short client-chosen opaque id, bounded like the
	// runner pin so a register body cannot grow the worker record at will.
	if len(w.BrowserTabID) > maxBrowserTabIDBytes {
		return w, ErrInvalid
	}
	// F9: app affinity is SERVER-owned state, never accepted from a
	// contributor-controlled register request. Reuse may restore it below.
	w.AppID = ""
	w.AppOwner = ""
	w.AppExpiresAt = time.Time{}
	w.PoolID = p
	w.Owner = owner
	w.Heartbeat = e.Now().UTC()
	if w.Anonymous || auth.IsAnonymousID(owner) {
		w.Anonymous = true
	}
	err := e.Store.Update(ctx, func(tx store.Tx) error {
		rows, er := tx.List(store.Workers)
		if er != nil {
			return er
		}
		now := e.Now()
		count := 0
		liveAnonymous := 0
		var reused *Worker
		for _, row := range rows {
			var existing Worker
			if er = json.Unmarshal(row.Value, &existing); er != nil {
				return er
			}
			if existing.Owner != owner || existing.PoolID != p {
				continue
			}
			// GLM R10: the per-owner cap bounds LIVE tabs. A dead record of a
			// stored-key subject is never reaped (EvictStaleAnonymousTabs is
			// anonymous-only), so counting it would lock the subject out for good.
			if existing.Anonymous {
				count++
			} else if e.liveTab(existing, now) {
				count++
			}
			if e.liveAnonymousTab(existing, now) {
				liveAnonymous++
			}
			// W9b row 2: a re-register of the same host for (owner, pool) is a
			// reload or a charger change of the same tab, not a second tab. The
			// newest match wins when old duplicates already exist.
			//
			// R10 chatgpt: two browser tabs of one owner share localStorage, so
			// their HostID is equal and a host-only match overwrote the first tab.
			// A client that reports a browser tab id matches only a record of the
			// same tab; a client that does not report one keeps the old rule.
			if existing.HostID != w.HostID {
				continue
			}
			if w.BrowserTabID != "" && existing.BrowserTabID != w.BrowserTabID {
				continue
			}
			if reused == nil || existing.Heartbeat.After(reused.Heartbeat) {
				match := existing
				reused = &match
			}
		}
		if reused != nil {
			w.TabID = reused.TabID
			w.SessionID = reused.SessionID
			// F9: preserving the process-app binding on re-register is
			// mandatory. A browser refresh cannot reset its exclusive tab.
			w.AppID = reused.AppID
			w.AppOwner = reused.AppOwner
			w.AppExpiresAt = reused.AppExpiresAt
			// W9c row 2: the stale-runner mark is cleared only by a page that
			// reports the current task key id. A retired or unknown pin keeps
			// the tab stale; a pin-less re-register proves nothing either.
			pinKeyID := runnerPinKeyID(w.RunnerPin)
			currentKeyID := e.currentTaskKeyID()
			switch {
			case pinKeyID != "" && pinKeyID == currentKeyID:
				w.RunnerStale = false
			case pinKeyID != "":
				w.RunnerStale = true
			default:
				w.RunnerStale = reused.RunnerStale
			}
			// W9c row 3: a revived tab must pass the same live anonymous caps as
			// a fresh one, with its own record excluded so it cannot block itself.
			if w.Anonymous && !w.RunnerStale {
				if er := e.checkAnonymousLimitsExcept(tx, w.ClientIP, w.TabID); er != nil {
					return er
				}
				maxTabs := limits.MaxWorkersPerOwner
				if maxTabs > AnonymousMaxTabsPerSubject {
					maxTabs = AnonymousMaxTabsPerSubject
				}
				live := liveAnonymous
				if e.liveAnonymousTab(*reused, now) {
					live--
				}
				if live >= maxTabs {
					return ErrEmpty
				}
			}
			if w.SessionID != "" {
				var s Session
				if er := tx.Get(store.Sessions, w.SessionID, &s); er == nil {
					for i := range s.Tabs {
						if s.Tabs[i].TabID == w.TabID {
							s.Tabs[i] = w
							break
						}
					}
					if er := tx.Put(store.Sessions, s.SessionID, s); er != nil {
						return er
					}
				} else if !errors.Is(er, store.ErrNotFound) {
					return er
				}
			}
			return tx.Put(store.Workers, w.TabID, w)
		}
		if w.Anonymous {
			// W9b row 2: the live anonymous caps are checked in this
			// transaction, so two concurrent joins cannot both pass.
			if er := e.checkAnonymousLimits(tx, w.ClientIP); er != nil {
				return er
			}
		}
		maxTabs := limits.MaxWorkersPerOwner
		// T110: one anonymous token may not register many tabs (for example the
		// same bearer copied to N IPs). A re-register of a known host returns above.
		if w.Anonymous {
			if maxTabs > AnonymousMaxTabsPerSubject {
				maxTabs = AnonymousMaxTabsPerSubject
			}
			// W9 F4: a stale-runner tab holds no live slot, so it must not hold the
			// subject slot either; same live rule as the IP/global caps of T110.
			count = liveAnonymous
		}
		if count >= maxTabs {
			return ErrEmpty
		}
		w.TabID = auth.ID()
		w.SessionID = ""
		return tx.Put(store.Workers, w.TabID, w)
	})
	if err == nil && w.Anonymous {
		e.seedAnonymousReputation(w.TabID)
	}
	return w, err
}
func (e *Engine) Heartbeat(ctx context.Context, p, owner, id string, renew *Lease) (Worker, error) {
	return e.heartbeat(ctx, p, owner, id, "", renew)
}

// HeartbeatWithPin refreshes a tab and records the runner pin it reports (W9
// F1). An empty pin keeps the stored one, so a client that does not send it
// never erases evidence.
func (e *Engine) HeartbeatWithPin(ctx context.Context, p, owner, id, pin string, renew *Lease) (Worker, error) {
	if pin != "" {
		normalized, err := normalizeRunnerPin(pin)
		if err != nil {
			return Worker{}, err
		}
		pin = normalized
	}
	return e.heartbeat(ctx, p, owner, id, pin, renew)
}

func (e *Engine) heartbeat(ctx context.Context, p, owner, id, pin string, renew *Lease) (Worker, error) {
	var w Worker
	var renewErr error
	var metered []meteredRuntime
	err := e.Store.Update(ctx, func(tx store.Tx) error {
		if er := tx.Get(store.Workers, id, &w); er != nil {
			return er
		}
		if w.Owner != owner || w.PoolID != p {
			return ErrForbidden
		}
		if pin != "" {
			w.RunnerPin = pin
		}
		w.Heartbeat = e.Now().UTC()
		physical, er := e.lockPhysicalAppTab(tx, w)
		if er != nil {
			return er
		}
		if physical.id != "" {
			rows, er := tx.List(store.Jobs)
			if er != nil {
				return er
			}
			if er = e.refreshPhysicalAppTab(tx, &physical, &w, rows); er != nil {
				return er
			}
			if physical.conflict && renew != nil {
				return ErrConflict
			}
			if physical.active && physical.fence.Pool == p {
				if er = e.renewPhysicalAppQuota(tx, physical, w); er != nil {
					return er
				}
			}
		}
		if w.SessionID != "" {
			var s Session
			if er := tx.Get(store.Sessions, w.SessionID, &s); er == nil {
				for i := range s.Tabs {
					if s.Tabs[i].TabID == w.TabID {
						s.Tabs[i].Heartbeat = w.Heartbeat
						break
					}
				}
				if er := tx.Put(store.Sessions, s.SessionID, s); er != nil {
					return er
				}
			} else if !errors.Is(er, store.ErrNotFound) {
				return er
			}
		}
		if er := tx.Put(store.Workers, id, w); er != nil {
			return er
		}
		if er := e.expireSessions(tx, &metered); er != nil {
			return er
		}
		// Re-read because expiry can release a tab.
		if er := tx.Get(store.Workers, id, &w); er != nil {
			return er
		}
		if renew != nil {
			var j Job
			if er := tx.Get(store.Jobs, renew.JobID, &j); er != nil {
				return er
			}
			if j.PoolID != p || terminal(j) {
				renewErr = ErrConflict
			} else {
				found := false
				for i := range j.Tasks {
					t := &j.Tasks[i]
					l := t.Lease
					if t.State == "leased" && l != nil && l.TaskID == renew.TaskID && l.WorkerID == id && l.Token == renew.Token && l.Generation == renew.Generation && !renew.ExpiresAt.After(l.ExpiresAt) && l.ExpiresAt.After(e.Now()) {
						if er := e.renewBatch(&j, *l); er != nil {
							renewErr = er
						} else {
							found = true
						}
						e.Log.Info("lease.renewed", "jobId", j.JobID, "taskId", t.TaskID, "generation", l.Generation)
						break
					}
				}
				if !found {
					renewErr = ErrConflict
				} else if er := e.save(tx, &j, &metered); er != nil {
					renewErr = er
				}
			}
		}
		w.Heartbeat = e.Now().UTC()
		if w.SessionID != "" {
			var s Session
			if er := tx.Get(store.Sessions, w.SessionID, &s); er == nil {
				for i := range s.Tabs {
					if s.Tabs[i].TabID == w.TabID {
						s.Tabs[i].Heartbeat = w.Heartbeat
						break
					}
				}
				if er := tx.Put(store.Sessions, s.SessionID, s); er != nil {
					return er
				}
			} else if !errors.Is(er, store.ErrNotFound) {
				return er
			}
		}
		if er := tx.Put(store.Workers, id, w); er != nil {
			return er
		}
		if renewErr != nil && !errors.Is(renewErr, ErrConflict) {
			return renewErr
		}
		return nil
	})
	if err == nil {
		e.applyMeteredRuntime(metered)
		if renewErr != nil {
			return w, renewErr
		}
	}
	return w, err
}
