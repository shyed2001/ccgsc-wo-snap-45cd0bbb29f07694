package pool

// F10 Thread application slots are independent of Process Pool tabs.
// The shared Engine delegates W per-worker quotas to internal/threadapp;
// it NEVER imports the Process app module here.
import (
	"context"
	"errors"
	"fmt"

	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/threadapp"
	"ccgsc/poolscore/internal/verify"
)

var (
	ErrThreadAppFull  = threadapp.ErrFull
	ErrThreadAppBound = threadapp.ErrBound
)

func (e *Engine) approvedThreadAppTx(tx store.Tx, owner, appID string) error {
	if err := threadapp.ApprovedTx(tx, owner, appID); err != nil {
		if errors.Is(err, threadapp.ErrNotApproved) {
			return fmt.Errorf("%w: Thread app not approved", ErrForbidden)
		}
		if errors.Is(err, threadapp.ErrInvalid) {
			return ErrInvalid
		}
		return err
	}
	return nil
}
func (e *Engine) ApproveThreadApp(ctx context.Context, owner, appID string, w int) error {
	return threadapp.Approve(ctx, e.Store, owner, appID, w, e.Now().UTC())
}
func (e *Engine) GetThreadApp(ctx context.Context, owner, appID string) (threadapp.Policy, error) {
	return threadapp.Read(ctx, e.Store, owner, appID)
}

// bindThreadAppTx chooses an individual Web Worker *slot* within a tab.
// F10b permits slots only of the physical tab's current app. The common
// base fence is acquired by Claim before entering either quota registry.
func (e *Engine) bindThreadAppTx(tx store.Tx, j *Job, w Worker, jobs []Job, physicalBusy map[string]bool) (string, error) {
	if j.PoolID != Thread || j.AppID == "" {
		return "", ErrInvalid
	}
	if w.BrowserTabID == "" || w.HostID == "" {
		return "", ErrInvalid
	}
	now := e.Now().UTC()
	busy := make(map[string]bool)
	for id := range physicalBusy {
		busy[id] = true
	}
	oldUnmarked := 0
	for _, prev := range jobs {
		for _, task := range prev.Tasks {
			l := task.Lease
			if l == nil || l.WorkerID != w.TabID || !l.ExpiresAt.After(now) {
				continue
			}
			if l.WorkerSlotID != "" {
				busy[l.WorkerSlotID] = true
			} else {
				oldUnmarked++
			}
		}
	}
	for i := oldUnmarked; i < w.Slots; i++ {
		identity, err := threadapp.SlotIdentity(w.HostID, w.BrowserTabID, i)
		if err != nil {
			return "", ErrInvalid
		}
		publicSlotID := verify.SHA256([]byte(identity))
		if busy[publicSlotID] {
			continue
		}
		_, err = threadapp.BindTx(tx, j.Owner, j.AppID, identity, now, e.processAppTTL())
		if errors.Is(err, threadapp.ErrBound) {
			continue
		} // another app owns this slot
		if err != nil {
			return "", err
		}
		return publicSlotID, nil
	}
	return "", ErrThreadAppFull
}
