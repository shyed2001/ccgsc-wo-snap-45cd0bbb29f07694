package pool

// F9 owner-approved logical Process application pools. The per-app quota is
// stored transactionally; Thread Pool has no dependency on this module.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"ccgsc/poolscore/internal/appegress"
	"ccgsc/poolscore/internal/processapp"
	"ccgsc/poolscore/internal/store"
)

var (
	ErrProcessAppFull  = processapp.ErrFull
	ErrProcessAppBound = processapp.ErrBound
)

func (e *Engine) approvedProcessAppTx(tx store.Tx, p, owner, appID string) error {
	if p != Process {
		return ErrInvalid
	}
	if err := processapp.ApprovedTx(tx, owner, appID); err != nil {
		if errors.Is(err, processapp.ErrNotApproved) {
			return fmt.Errorf("%w: process app not approved", ErrForbidden)
		}
		if errors.Is(err, processapp.ErrInvalid) {
			return ErrInvalid
		}
		return err
	}
	return nil
}
func (e *Engine) processAppTTL() time.Duration {
	ttl := 2 * e.TTL
	if ttl < 2*time.Minute {
		ttl = 2 * time.Minute
	}
	if ttl > 24*time.Hour {
		ttl = 24 * time.Hour
	}
	return ttl
}
func (e *Engine) bindProcessAppTx(tx store.Tx, j *Job, w *Worker) error {
	if j.PoolID != Process || j.AppID == "" {
		return nil
	}
	if w.AppID != "" && (w.AppID != j.AppID || w.AppOwner != j.Owner) && w.AppExpiresAt.After(e.Now()) {
		return processapp.ErrBound
	}
	identity, err := processapp.TabIdentity(w.HostID, w.BrowserTabID)
	if err != nil {
		return ErrInvalid
	}
	b, err := processapp.BindTx(tx, j.Owner, j.AppID, identity, e.Now().UTC(), e.processAppTTL())
	if errors.Is(err, processapp.ErrNotApproved) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	w.AppOwner = j.Owner
	w.AppID = j.AppID
	w.AppExpiresAt = b.ExpiresAt
	return nil
}
func (e *Engine) renewProcessAppTx(tx store.Tx, w *Worker) error {
	if w.PoolID != Process || w.AppID == "" {
		return nil
	}
	identity, err := processapp.TabIdentity(w.HostID, w.BrowserTabID)
	if err != nil {
		return ErrInvalid
	}
	b, err := processapp.BindTx(tx, w.AppOwner, w.AppID, identity, e.Now().UTC(), e.processAppTTL())
	if errors.Is(err, processapp.ErrNotApproved) || errors.Is(err, processapp.ErrBound) || errors.Is(err, processapp.ErrFull) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	w.AppExpiresAt = b.ExpiresAt
	return nil
}

// ApproveProcessApp is admin-facing. Route authorization MUST be performed by
// the caller; client-controlled owner/app strings never approve themselves.
func (e *Engine) ApproveProcessApp(ctx context.Context, owner, appID string, k int) error {
	return processapp.Approve(ctx, e.Store, owner, appID, k, e.Now().UTC())
}
func (e *Engine) GetProcessApp(ctx context.Context, owner, appID string) (processapp.Policy, error) {
	return processapp.Read(ctx, e.Store, owner, appID)
}

// ReleaseProcessApp releases affinity only when no unexpired leased task
// exists for the tab; it cannot be used to evade a per-app active-work quota.
func (e *Engine) ReleaseProcessApp(ctx context.Context, p, workerOwner, workerID string) error {
	if p != Process {
		return ErrInvalid
	}
	return e.Store.Update(ctx, func(tx store.Tx) error {
		var w Worker
		if err := tx.Get(store.Workers, workerID, &w); err != nil {
			return err
		}
		if w.PoolID != p || w.Owner != workerOwner {
			return ErrForbidden
		}
		physical, err := e.lockPhysicalAppTab(tx, w)
		if err != nil {
			return err
		}
		rows, err := tx.List(store.Jobs)
		if err != nil {
			return err
		}
		if err = e.refreshPhysicalAppTab(tx, &physical, &w, rows); err != nil {
			return err
		}
		if physical.conflict || physical.active {
			return ErrConflict
		}
		if w.AppID == "" {
			return nil
		}
		for _, row := range rows {
			var j Job
			if err := json.Unmarshal(row.Value, &j); err != nil {
				return err
			}
			for _, task := range j.Tasks {
				if task.Lease != nil && task.Lease.WorkerID == workerID && task.Lease.ExpiresAt.After(e.Now()) {
					return ErrConflict
				}
			}
		}
		id, err := processapp.TabIdentity(w.HostID, w.BrowserTabID)
		if err != nil {
			return ErrInvalid
		}
		if err := processapp.ReleaseTx(tx, w.AppOwner, w.AppID, id, e.Now().UTC()); err != nil {
			return err
		}
		w.AppID = ""
		w.AppOwner = ""
		w.AppExpiresAt = time.Time{}
		if physical.id != "" {
			if err := appegress.SaveTabTx(tx, physical.id, appegress.TabFence{}); err != nil {
				return err
			}
		}
		return tx.Put(store.Workers, workerID, w)
	})
}
