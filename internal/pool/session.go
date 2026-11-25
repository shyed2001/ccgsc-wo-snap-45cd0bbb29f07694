// Path: internal/pool/session.go | Author: Codex for Claude Opus 5.5 | 2026-11-17
// What/why: explicit process-pool reservations and batch submission; WWPP-R32.2, R39.1.
package pool

import (
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

const sessionDrainGrace = 2 * time.Minute

var sessionIndex = struct {
	sync.Mutex
	ids  map[*Engine]map[string]struct{}
	jobs map[*Engine]map[string]map[string]struct{}
}{ids: map[*Engine]map[string]struct{}{}, jobs: map[*Engine]map[string]map[string]struct{}{}}

func indexSession(e *Engine, id string) {
	sessionIndex.Lock()
	defer sessionIndex.Unlock()
	if sessionIndex.ids[e] == nil {
		sessionIndex.ids[e] = map[string]struct{}{}
	}
	sessionIndex.ids[e][id] = struct{}{}
}

func indexJob(e *Engine, sessionID, jobID string) {
	sessionIndex.Lock()
	defer sessionIndex.Unlock()
	if sessionIndex.jobs[e] == nil {
		sessionIndex.jobs[e] = map[string]map[string]struct{}{}
	}
	if sessionIndex.jobs[e][sessionID] == nil {
		sessionIndex.jobs[e][sessionID] = map[string]struct{}{}
	}
	sessionIndex.jobs[e][sessionID][jobID] = struct{}{}
}

func unindexSession(e *Engine, id string) {
	sessionIndex.Lock()
	defer sessionIndex.Unlock()
	delete(sessionIndex.ids[e], id)
	delete(sessionIndex.jobs[e], id)
}

func indexedSessions(e *Engine) []string {
	sessionIndex.Lock()
	defer sessionIndex.Unlock()
	out := make([]string, 0, len(sessionIndex.ids[e]))
	for id := range sessionIndex.ids[e] {
		out = append(out, id)
	}
	return out
}

func indexedJobs(e *Engine, sessionID string) []string {
	sessionIndex.Lock()
	defer sessionIndex.Unlock()
	out := make([]string, 0, len(sessionIndex.jobs[e][sessionID]))
	for id := range sessionIndex.jobs[e][sessionID] {
		out = append(out, id)
	}
	return out
}

func rebuildSessionIndex(e *Engine) error {
	return e.Store.View(context.Background(), func(r store.Reader) error {
		sessionRows, err := r.List(store.Sessions)
		if err != nil {
			return err
		}
		jobRows, err := r.List(store.Jobs)
		if err != nil {
			return err
		}
		ids := make(map[string]struct{}, len(sessionRows))
		jobs := make(map[string]map[string]struct{})
		for _, row := range sessionRows {
			var s Session
			if err := json.Unmarshal(row.Value, &s); err != nil {
				return err
			}
			ids[s.SessionID] = struct{}{}
		}
		for _, row := range jobRows {
			var j Job
			if err := json.Unmarshal(row.Value, &j); err != nil {
				return err
			}
			if j.SessionID == "" {
				continue
			}
			if jobs[j.SessionID] == nil {
				jobs[j.SessionID] = map[string]struct{}{}
			}
			jobs[j.SessionID][j.JobID] = struct{}{}
		}
		sessionIndex.Lock()
		defer sessionIndex.Unlock()
		sessionIndex.ids[e] = ids
		sessionIndex.jobs[e] = jobs
		return nil
	})
}

func (e *Engine) sessionEvent(tx store.Tx, s Session, kind string) error {
	seq, err := nextEventSequence(tx, eventSequenceName)
	if err != nil {
		return err
	}
	return tx.Put(store.Events, fmt.Sprintf("%020d", seq), Event{ID: fmt.Sprintf("%020d", seq), Type: kind, At: e.Now().UTC(), Owner: s.Owner, PoolID: Process, SessionID: s.SessionID})
}

func (e *Engine) OpenSession(ctx context.Context, owner, key string, q SessionRequest) (Session, error) {
	var s Session
	if q.HostCount == 0 {
		q.HostCount = q.TabCount
	}
	if q.TTLSeconds == 0 {
		q.TTLSeconds = 900
	}
	if q.Needs == "" {
		q.Needs = "cpu"
	}
	if q.TabCount < 1 || q.TabCount > 64 || q.HostCount < 1 || q.HostCount > q.TabCount || q.TTLSeconds < 30 || q.TTLSeconds > 86400 {
		return s, ErrInvalid
	}
	if q.Needs != "cpu" && q.Needs != "gpu" && q.Needs != "gpu-preferred" {
		return s, ErrInvalid
	}
	if _, err := decodePin(q.Pin); err != nil {
		return s, err
	}
	if q.WarmInitializerArtifactID != "" && !isArtifactID(q.WarmInitializerArtifactID) {
		return s, ErrInvalid
	}
	var metered []meteredRuntime
	err := e.Store.Update(ctx, func(tx store.Tx) error {
		if er := e.expireSessions(tx, &metered); er != nil {
			return er
		}
		b, er := json.Marshal(q)
		if er != nil {
			return er
		}
		digest := verify.SHA256(b)
		idem := "session:" + owner + ":" + key
		if key != "" {
			var old struct{ Hash, ID string }
			if er := tx.Get(store.Idempotency, idem, &old); er == nil {
				if old.Hash != digest {
					return ErrConflict
				}
				return tx.Get(store.Sessions, old.ID, &s)
			} else if !errors.Is(er, store.ErrNotFound) {
				return er
			}
		}
		limits := e.sessionLimits()
		ownerSessions, ownerTabs := 0, 0
		for _, sessionID := range indexedSessions(e) {
			var existing Session
			if er = tx.Get(store.Sessions, sessionID, &existing); er != nil {
				if errors.Is(er, store.ErrNotFound) {
					continue
				}
				return er
			}
			if existing.Owner == owner && (existing.State == "open" || existing.State == "draining") {
				ownerSessions++
				ownerTabs += len(existing.Tabs)
			}
		}
		if ownerSessions >= limits.MaxSessionsPerOwner || ownerTabs+q.TabCount > limits.MaxTabsPerOwner {
			return ErrEmpty
		}
		if q.WarmInitializerArtifactID != "" {
			var a Artifact
			if er := tx.Get(store.Artifacts, q.WarmInitializerArtifactID, &a); er != nil {
				return er
			}
			if !a.Complete {
				return ErrConflict
			}
			if er = e.moduleApproved(a); er != nil {
				return er
			}
		}
		for _, id := range q.ModuleArtifactIDs {
			var a Artifact
			if er := tx.Get(store.Artifacts, id, &a); er != nil {
				return er
			}
			if !a.Complete {
				return ErrConflict
			}
			// The F8 review gate applies to the session modules too.
			if er = e.moduleApproved(a); er != nil {
				return er
			}
		}
		rows, er := tx.List(store.Workers)
		if er != nil {
			return er
		}
		eligible := []Worker{}
		byHost := map[string]bool{}
		chosen := map[string]bool{}
		tabs := []Worker{}
		jobs, er := tx.List(store.Jobs)
		if er != nil {
			return er
		}
		busy := map[string]bool{}
		gpuCfg := e.GPUConfig()
		for _, r := range jobs {
			var j Job
			if er = json.Unmarshal(r.Value, &j); er != nil {
				return er
			}
			if terminal(j) {
				continue
			}
			for _, t := range j.Tasks {
				if t.State == "leased" && t.Lease != nil && t.Lease.ExpiresAt.After(e.Now()) {
					busy[t.Lease.WorkerID] = true
				}
			}
		}
		for _, r := range rows {
			var w Worker
			if er = json.Unmarshal(r.Value, &w); er != nil {
				return er
			}
			gpuEligible := gpuCfg.Enabled && w.GPU && gpuAllowedOnWorker(w, gpuCfg)
			if w.PoolID == Process && w.SessionID == "" && !busy[w.TabID] && e.Now().Sub(w.Heartbeat) <= 2*e.TTL &&
				(q.Needs != "gpu" || gpuEligible) && e.pinAllows(q.Pin, w) {
				eligible = append(eligible, w)
			}
		}
		// Prefer hosts with most available tabs, so an arbitrary listing order cannot
		// reject a satisfiable tabCount/hostCount reservation.
		counts := map[string]int{}
		for _, w := range eligible {
			counts[w.HostID]++
		}
		sort.SliceStable(eligible, func(i, j int) bool {
			if q.Needs == "gpu-preferred" && eligible[i].GPU != eligible[j].GPU {
				return eligible[i].GPU
			}
			return counts[eligible[i].HostID] > counts[eligible[j].HostID]
		})
		for _, w := range eligible {
			if len(byHost) < q.HostCount && !byHost[w.HostID] {
				tabs = append(tabs, w)
				byHost[w.HostID] = true
				chosen[w.TabID] = true
			}
		}
		for _, w := range eligible {
			if len(tabs) < q.TabCount && byHost[w.HostID] && !chosen[w.TabID] {
				tabs = append(tabs, w)
				chosen[w.TabID] = true
			}
		}
		if len(tabs) != q.TabCount || len(byHost) != q.HostCount {
			return ErrEmpty
		}
		for _, id := range q.ModuleArtifactIDs {
			var a Artifact
			if er = tx.Get(store.Artifacts, id, &a); er != nil {
				return er
			}
			if !a.Complete {
				return ErrConflict
			}
		}
		warnings := []Warning{}
		if len(q.ModuleArtifactIDs) == 0 {
			warnings = append(warnings, Warning{Code: "session-module-required", Message: "moduleArtifactIds must be supplied before session tasks can be submitted", Severity: "warning"})
		}
		warmState := "not-requested"
		if q.WarmInitializerArtifactID != "" {
			warmState = "warm"
		}
		s = Session{SessionID: e.ServerID + ".pp.session." + auth.ID(), Owner: owner, State: "open", TabCount: q.TabCount, HostCount: q.HostCount, CreatedAt: e.Now().UTC(), ExpiresAt: e.Now().Add(time.Duration(q.TTLSeconds) * time.Second).UTC(), Tabs: tabs, WarmState: warmState, Pin: q.Pin, WarmInitializerArtifactID: q.WarmInitializerArtifactID, Request: q, Warnings: warnings}
		for i := range s.Tabs {
			s.Tabs[i].SessionID = s.SessionID
			if er = tx.Put(store.Workers, s.Tabs[i].TabID, s.Tabs[i]); er != nil {
				return er
			}
		}
		if er = tx.Put(store.Sessions, s.SessionID, s); er != nil {
			return er
		}
		indexSession(e, s.SessionID)
		if er = e.sessionEvent(tx, s, "session.opened"); er != nil {
			return er
		}
		if key != "" {
			return tx.Put(store.Idempotency, idem, struct{ Hash, ID string }{digest, s.SessionID})
		}
		return nil
	})
	if err == nil {
		e.applyMeteredRuntime(metered)
	}
	return s, err
}
func (e *Engine) GetSession(ctx context.Context, owner, id string) (Session, error) {
	var s Session
	var metered []meteredRuntime
	err := e.Store.Update(ctx, func(tx store.Tx) error {
		if er := e.expireSessions(tx, &metered); er != nil {
			return er
		}
		if er := tx.Get(store.Sessions, id, &s); er != nil {
			return er
		}
		if s.Owner != owner {
			return ErrForbidden
		}
		for _, jobID := range indexedJobs(e, id) {
			var j Job
			if er := tx.Get(store.Jobs, jobID, &j); er != nil {
				if errors.Is(er, store.ErrNotFound) {
					continue
				}
				return er
			}
			p := progress(j)
			s.Progress.Total += p.Total
			s.Progress.Completed += p.Completed
			s.Progress.Failed += p.Failed
			s.Progress.Queued += p.Queued
			s.Progress.Running += p.Running
		}
		return nil
	})
	if err == nil {
		e.applyMeteredRuntime(metered)
	}
	return s, err
}
func (e *Engine) SessionTasks(ctx context.Context, owner, id, key string, q CreateRequest) (Job, error) {
	var j Job
	var metered []meteredRuntime
	err := e.Store.Update(ctx, func(tx store.Tx) error {
		if er := e.expireSessions(tx, &metered); er != nil {
			return er
		}
		var s Session
		if er := tx.Get(store.Sessions, id, &s); er != nil {
			return er
		}
		if s.Owner != owner {
			return ErrForbidden
		}
		if s.State != "open" || !s.ExpiresAt.After(e.Now()) {
			return ErrConflict
		}
		if len(s.Request.ModuleArtifactIDs) == 0 {
			return ErrInvalid
		}
		q.ModuleArtifactIDs = s.Request.ModuleArtifactIDs
		q.Needs = s.Request.Needs
		if len(q.Pin) == 0 {
			q.Pin = s.Pin
		}
		if pin, er := decodePin(q.Pin); er != nil || (pin.SessionID != "" && pin.SessionID != id) {
			return ErrInvalid
		}
		var er error
		j, er = e.create(tx, Process, owner, key, id, q)
		if er == nil {
			indexJob(e, id, j.JobID)
		}
		return er
	})
	if err == nil {
		e.applyMeteredRuntime(metered)
		j = e.refreshRunAudit(ctx, j)
	}
	return j, err
}
func (e *Engine) release(tx store.Tx, s *Session, state string, metered ...*[]meteredRuntime) error {
	s.State = state
	for _, w := range s.Tabs {
		var current Worker
		if er := tx.Get(store.Workers, w.TabID, &current); er != nil {
			if errors.Is(er, store.ErrNotFound) {
				continue
			}
			return er
		}
		if current.SessionID == s.SessionID {
			current.SessionID = ""
			if er := tx.Put(store.Workers, current.TabID, current); er != nil {
				return er
			}
		}
	}
	rows, er := tx.List(store.Jobs)
	if er != nil {
		return er
	}
	for _, r := range rows {
		var j Job
		if er = json.Unmarshal(r.Value, &j); er != nil {
			return er
		}
		if j.SessionID != s.SessionID || terminal(j) {
			continue
		}
		j.State = "cancelled"
		now := e.Now().UTC()
		j.FinishedAt = &now
		for i := range j.Tasks {
			t := &j.Tasks[i]
			if t.State == "queued" || t.State == "leased" || t.Pending != nil {
				t.State = "cancelled"
				if t.Lease != nil && len(metered) > 0 {
					if er := e.meterLeaseRuntime(tx, *t.Lease, metered[0]); er != nil {
						return er
					}
				}
				t.Lease = nil
				t.Pending = nil
			}
		}
		if er = e.event(tx, j, "job.done", nil); er != nil {
			return er
		}
		if er = e.save(tx, &j, metered...); er != nil {
			return er
		}
	}
	s.Tabs = append([]Worker(nil), s.Tabs...)
	for i := range s.Tabs {
		s.Tabs[i].SessionID = ""
	}
	if er = e.sessionEvent(tx, *s, "session."+state); er != nil {
		return er
	}
	if err := tx.Put(store.Sessions, s.SessionID, s); err != nil {
		return err
	}
	unindexSession(e, s.SessionID)
	return nil
}
func (e *Engine) CloseSession(ctx context.Context, owner, id string, drain bool) (Session, error) {
	var s Session
	var metered []meteredRuntime
	err := e.Store.Update(ctx, func(tx store.Tx) error {
		if er := tx.Get(store.Sessions, id, &s); er != nil {
			return er
		}
		if s.Owner != owner {
			return ErrForbidden
		}
		if s.State == "closed" || s.State == "expired" {
			return nil
		}
		if drain {
			s.State = "draining"
			until := e.Now().Add(sessionDrainGrace).UTC()
			s.DrainUntil = &until
			if er := e.sessionEvent(tx, s, "session.draining"); er != nil {
				return er
			}
			return tx.Put(store.Sessions, id, s)
		}
		return e.release(tx, &s, "closed", &metered)
	})
	if err == nil {
		e.applyMeteredRuntime(metered)
	}
	return s, err
}
func (e *Engine) expireSessions(tx store.Tx, metered ...*[]meteredRuntime) error {
	for _, id := range indexedSessions(e) {
		var s Session
		if er := tx.Get(store.Sessions, id, &s); er != nil {
			if errors.Is(er, store.ErrNotFound) {
				continue
			}
			return er
		}
		if s.State != "open" && s.State != "draining" {
			continue
		}
		stale := false
		for _, tab := range s.Tabs {
			heartbeat := tab.Heartbeat
			var current Worker
			if er := tx.Get(store.Workers, tab.TabID, &current); er == nil {
				if current.SessionID == s.SessionID {
					heartbeat = current.Heartbeat
				}
			} else if !errors.Is(er, store.ErrNotFound) {
				return er
			}
			if e.Now().Sub(heartbeat) > 2*e.TTL {
				stale = true
				break
			}
		}
		if stale && s.State == "open" {
			until := e.Now().Add(sessionDrainGrace).UTC()
			s.State = "draining"
			s.DrainUntil = &until
			s.Warnings = append(s.Warnings, Warning{Code: "stale-session-tab", Message: "a reserved tab stopped heartbeating", Severity: "warning"})
			if er := tx.Put(store.Sessions, s.SessionID, s); er != nil {
				return er
			}
			if er := e.sessionEvent(tx, s, "session.draining"); er != nil {
				return er
			}
		}
		if !s.ExpiresAt.After(e.Now()) && s.State == "open" {
			active := false
			for _, jobID := range indexedJobs(e, s.SessionID) {
				var j Job
				if er := tx.Get(store.Jobs, jobID, &j); er == nil && !terminal(j) {
					active = true
					break
				}
			}
			if !active {
				if er := e.release(tx, &s, "expired", metered...); er != nil {
					return er
				}
				continue
			}
			until := e.Now().Add(sessionDrainGrace).UTC()
			s.State = "draining"
			s.DrainUntil = &until
			s.Warnings = append(s.Warnings, Warning{Code: "session-tab-expired", Message: "session expiry entered drain grace", Severity: "warning"})
			if er := tx.Put(store.Sessions, s.SessionID, s); er != nil {
				return er
			}
			if er := e.sessionEvent(tx, s, "session.draining"); er != nil {
				return er
			}
			continue
		}
		if s.State == "draining" && (s.DrainUntil == nil || !s.DrainUntil.After(e.Now())) {
			if er := e.release(tx, &s, "expired", metered...); er != nil {
				return er
			}
			continue
		}
		if s.State == "draining" {
			active := false
			for _, jobID := range indexedJobs(e, s.SessionID) {
				var j Job
				if er := tx.Get(store.Jobs, jobID, &j); er == nil && !terminal(j) {
					active = true
					break
				}
			}
			if !active {
				if er := e.release(tx, &s, "closed", metered...); er != nil {
					return er
				}
			}
		}
	}
	return nil
}

func (e *Engine) RenewSession(ctx context.Context, owner, id string, ttlSeconds int) (Session, error) {
	var s Session
	err := e.Store.Update(ctx, func(tx store.Tx) error {
		if er := tx.Get(store.Sessions, id, &s); er != nil {
			return er
		}
		if s.Owner != owner {
			return ErrForbidden
		}
		if s.State != "open" && (s.State != "draining" || s.DrainUntil == nil || !s.DrainUntil.After(e.Now())) {
			return ErrConflict
		}
		if ttlSeconds == 0 {
			ttlSeconds = int(e.TTL / time.Second)
		}
		if ttlSeconds < 30 || ttlSeconds > 86400 {
			return ErrInvalid
		}
		s.State = "open"
		s.DrainUntil = nil
		s.ExpiresAt = e.Now().Add(time.Duration(ttlSeconds) * time.Second).UTC()
		if er := tx.Put(store.Sessions, id, s); er != nil {
			return er
		}
		return e.sessionEvent(tx, s, "session.renewed")
	})
	return s, err
}
