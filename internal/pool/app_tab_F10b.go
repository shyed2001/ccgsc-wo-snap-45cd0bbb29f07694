package pool

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"ccgsc/poolscore/internal/appegress"
	"ccgsc/poolscore/internal/processapp"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/threadapp"
	"ccgsc/poolscore/internal/verify"
)

// F10b: this shared base fence is independent of the Thread and Process
// quota registries. Claim and lease renewal serialize on the physical tab
// before examining the job rows. The current server-assigned worker ID alone
// cannot represent a tab registered with both pools.
type physicalAppTab struct {
	id           string
	fence        appegress.TabFence
	legacyActive bool
	conflict     bool
	active       bool
	busy         map[string]bool
}

func (e *Engine) lockPhysicalAppTab(tx store.Tx, w Worker) (physicalAppTab, error) {
	if w.BrowserTabID == "" {
		return physicalAppTab{}, nil
	}
	id, err := appegress.TabIdentity(w.HostID, w.BrowserTabID)
	if err != nil {
		return physicalAppTab{}, ErrInvalid
	}
	f, err := appegress.LockTabTx(tx, id)
	return physicalAppTab{id: id, fence: f}, err
}
func (e *Engine) refreshPhysicalAppTab(tx store.Tx, t *physicalAppTab, w *Worker, rows []store.Record) error {
	if t.id == "" {
		return nil
	}
	now := e.Now().UTC()
	workers, err := tx.List(store.Workers)
	if err != nil {
		return err
	}
	ids := map[string]bool{w.TabID: true}
	for _, row := range workers {
		var other Worker
		if err = json.Unmarshal(row.Value, &other); err != nil {
			return err
		}
		if other.HostID == w.HostID && other.BrowserTabID == w.BrowserTabID {
			ids[other.TabID] = true
		}
	}
	active := false
	live := map[string]bool{}
	t.busy = map[string]bool{}
	for _, row := range rows {
		var j Job
		if err = json.Unmarshal(row.Value, &j); err != nil {
			return err
		}
		for _, task := range j.Tasks {
			l := task.Lease
			leaseLive := l != nil && ids[l.WorkerID] && l.ExpiresAt.After(now)
			// G-F12-3: the tab stays on its app while a Dew result is still
			// outstanding. A missed heartbeat must not start the idle timer
			// before the receipt expires or the result is submitted.
			dewHeld := !task.EnvelopeUsed && task.Dew != nil && ids[task.Dew.WorkerID] &&
				task.Dew.Deadline.After(now) && !dewRunEnded(&j)
			if !leaseLive && !dewHeld {
				continue
			}
			if j.AppID == "" {
				if leaseLive {
					t.legacyActive = true
				}
				continue
			}
			if !t.fence.Permits(j.PoolID, j.Owner, j.AppID) {
				t.conflict = true
				continue
			}
			t.fence.Bind(j.PoolID, j.Owner, j.AppID, "")
			if leaseLive && j.PoolID == Thread && l.WorkerSlotID != "" {
				t.busy[l.WorkerSlotID] = true
				for i := 0; i < 64; i++ {
					slot, _ := threadapp.SlotIdentity(w.HostID, w.BrowserTabID, i)
					if verify.SHA256([]byte(slot)) == l.WorkerSlotID {
						t.fence.Bind(j.PoolID, j.Owner, j.AppID, slot)
						live[slot] = true
						break
					}
				}
			}
			active = true
		}
	}
	if t.conflict || (t.legacyActive && active) {
		t.conflict = true
		return nil
	}
	t.active = active
	// F10b-02: the fence retains slot reservations whose leases ended while
	// other slots of the same tab stayed busy; renewing them would keep W quota
	// reserved with no workload behind it. Live slots keep their binding and the
	// idle release below still frees every remaining slot.
	if active && len(t.fence.ThreadSlots) > 0 {
		kept := t.fence.ThreadSlots[:0]
		for _, slot := range t.fence.ThreadSlots {
			if live[slot] {
				kept = append(kept, slot)
			}
		}
		t.fence.ThreadSlots = kept
	}
	if t.fence.AppID != "" {
		if active {
			t.fence.IdleSince = time.Time{}
		} else if t.fence.IdleSince.IsZero() {
			t.fence.IdleSince = now
		}
		policy, err := appegress.ReadReleaseTx(tx, t.fence.Pool)
		if err != nil {
			return err
		}
		if policy.CanRelease(t.fence, active, now) {
			if err = e.releasePhysicalAppQuota(tx, t.id, t.fence, now); err != nil {
				return err
			}
			t.fence = appegress.TabFence{}
		}
	}
	if t.fence.AppID == "" || t.fence.Pool != w.PoolID {
		w.AppID = ""
		w.AppOwner = ""
		w.AppExpiresAt = time.Time{}
	} else {
		w.AppID = t.fence.AppID
		w.AppOwner = t.fence.Owner
		w.AppExpiresAt = now.Add(e.processAppTTL())
	}
	if err = tx.Put(store.Workers, w.TabID, *w); err != nil {
		return err
	}
	return appegress.SaveTabTx(tx, t.id, t.fence)
}
func (e *Engine) releasePhysicalAppQuota(tx store.Tx, id string, f appegress.TabFence, now time.Time) error {
	if f.AppID == "" {
		return nil
	}
	if f.Pool == Process {
		err := processapp.ReleaseTx(tx, f.Owner, f.AppID, id, now)
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, processapp.ErrBound) {
			return nil
		}
		return err
	}
	if f.Pool == Thread {
		for _, slot := range f.ThreadSlots {
			err := threadapp.ReleaseTx(tx, f.Owner, f.AppID, slot, now)
			if err != nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, threadapp.ErrBound) {
				return err
			}
		}
		return nil
	}
	return ErrInvalid
}
func (t physicalAppTab) permits(j *Job) bool {
	if t.conflict {
		return false
	}
	if j.AppID != "" && (t.id == "" || t.legacyActive) {
		return false
	}
	return t.fence.Permits(j.PoolID, j.Owner, j.AppID)
}
func (e *Engine) bindPhysicalAppTab(tx store.Tx, t *physicalAppTab, w *Worker, j *Job, publicSlot string) error {
	if j.AppID == "" {
		return nil
	}
	if t.id == "" || !t.permits(j) {
		return ErrConflict
	}
	slot := ""
	if publicSlot != "" {
		for i := 0; i < w.Slots; i++ {
			v, _ := threadapp.SlotIdentity(w.HostID, w.BrowserTabID, i)
			if verify.SHA256([]byte(v)) == publicSlot {
				slot = v
				break
			}
		}
		if slot == "" {
			return ErrInvalid
		}
	}
	t.fence.Bind(j.PoolID, j.Owner, j.AppID, slot)
	w.AppID = j.AppID
	w.AppOwner = j.Owner
	w.AppExpiresAt = e.Now().UTC().Add(e.processAppTTL())
	if err := tx.Put(store.Workers, w.TabID, *w); err != nil {
		return err
	}
	return appegress.SaveTabTx(tx, t.id, t.fence)
}
func (e *Engine) ReadTabRelease(ctx context.Context, p string) (appegress.ReleasePolicy, error) {
	var v appegress.ReleasePolicy
	err := e.Store.View(ctx, func(r store.Reader) error { var er error; v, er = appegress.ReadReleaseTx(r, p); return er })
	return v, err
}
func (e *Engine) WriteTabRelease(ctx context.Context, p string, v appegress.ReleasePolicy) (appegress.ReleasePolicy, error) {
	var out appegress.ReleasePolicy
	err := e.Store.Update(ctx, func(tx store.Tx) error { var er error; out, er = appegress.WriteReleaseTx(tx, p, v); return er })
	if errors.Is(err, appegress.ErrInvalid) {
		err = ErrInvalid
	}
	return out, err
}

func (e *Engine) renewPhysicalAppQuota(tx store.Tx, t physicalAppTab, w Worker) error {
	if t.fence.AppID == "" {
		return nil
	}
	now := e.Now().UTC()
	ttl := e.processAppTTL()
	if t.fence.Pool == Process {
		_, err := processapp.BindTx(tx, t.fence.Owner, t.fence.AppID, t.id, now, ttl)
		return err
	}
	if t.fence.Pool == Thread {
		for _, slot := range t.fence.ThreadSlots {
			if _, err := threadapp.BindTx(tx, t.fence.Owner, t.fence.AppID, slot, now, ttl); err != nil {
				return err
			}
		}
		return nil
	}
	return ErrInvalid
}
