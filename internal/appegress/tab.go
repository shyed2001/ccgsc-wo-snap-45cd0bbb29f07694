package appegress

import (
	"errors"
	"fmt"
	"time"

	"ccgsc/poolscore/internal/store"
)

const tabBucket = "app_physical_tabs"
const releaseBucket = "app_tab_release_policy"

type TabFence struct {
	Owner       string    `json:"owner,omitempty"`
	AppID       string    `json:"appId,omitempty"`
	Pool        string    `json:"pool,omitempty"`
	IdleSince   time.Time `json:"idleSince,omitempty"`
	ThreadSlots []string  `json:"threadSlots,omitempty"`
}

func TabIdentity(host, tab string) (string, error) {
	if len(host) < 1 || len(host) > 256 || len(tab) < 1 || len(tab) > 128 {
		return "", ErrInvalid
	}
	return fmt.Sprintf("%d:%s:%s", len(host), host, tab), nil
}

// LockTabTx uses one durable row, independent of either pool's registry.
// Existing rows are read with the PG transaction's FOR UPDATE semantics.
// The unique conditional insertion serializes a previously unseen physical tab.
func LockTabTx(tx store.Tx, id string) (TabFence, error) {
	var fence TabFence
	key := hash(id)
	err := tx.Get(tabBucket, key, &fence)
	if err == nil {
		return fence, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return fence, err
	}
	atomic, ok := tx.(store.PutIfAbsentTx)
	if !ok {
		return fence, store.ErrNotImplemented
	}
	if _, err = atomic.PutIfAbsent(tabBucket, key, TabFence{}); err != nil {
		return fence, err
	}
	err = tx.Get(tabBucket, key, &fence)
	return fence, err
}

// ReadTabTx reads the fence without creating or locking it. A missing row is an
// unbound tab.
func ReadTabTx(r store.Reader, id string) (TabFence, error) {
	var f TabFence
	err := r.Get(tabBucket, hash(id), &f)
	if errors.Is(err, store.ErrNotFound) {
		return TabFence{}, nil
	}
	return f, err
}
func SaveTabTx(tx store.Tx, id string, f TabFence) error { return tx.Put(tabBucket, hash(id), f) }
func (f TabFence) Permits(pool, owner, app string) bool {
	return f.AppID == "" || (f.Pool == pool && f.Owner == owner && f.AppID == app)
}
func (f *TabFence) Bind(pool, owner, app, slot string) {
	if app == "" {
		return
	}
	f.Pool = pool
	f.Owner = owner
	f.AppID = app
	f.IdleSince = time.Time{}
	if slot != "" {
		for _, v := range f.ThreadSlots {
			if v == slot {
				return
			}
		}
		f.ThreadSlots = append(f.ThreadSlots, slot)
	}
}

type ReleasePolicy struct {
	Mode        string `json:"mode"`
	IdleSeconds int    `json:"idleSeconds"`
	Version     uint64 `json:"version"`
}

func DefaultReleasePolicy() ReleasePolicy { return ReleasePolicy{Mode: IdleTimeout, IdleSeconds: 30} }
func ReadReleaseTx(r store.Reader, pool string) (ReleasePolicy, error) {
	if pool != "tp" && pool != "pp" {
		return ReleasePolicy{}, ErrInvalid
	}
	var p ReleasePolicy
	err := r.Get(releaseBucket, pool, &p)
	if errors.Is(err, store.ErrNotFound) {
		return DefaultReleasePolicy(), nil
	}
	if err != nil {
		return p, err
	}
	if !validRelease(p) {
		return p, ErrInvalid
	}
	return p, nil
}
func validRelease(p ReleasePolicy) bool {
	return (p.Mode == IdleTimeout || p.Mode == Immediate) && p.IdleSeconds >= 1 && p.IdleSeconds <= 3600
}
func WriteReleaseTx(tx store.Tx, pool string, p ReleasePolicy) (ReleasePolicy, error) {
	if !validRelease(p) {
		return ReleasePolicy{}, ErrInvalid
	}
	old, err := ReadReleaseTx(tx, pool)
	if err != nil {
		return ReleasePolicy{}, err
	}
	p.Version = old.Version + 1
	if old.Version == 0 {
		atomic, ok := tx.(store.PutIfAbsentTx)
		if !ok {
			return ReleasePolicy{}, store.ErrNotImplemented
		}
		inserted, err := atomic.PutIfAbsent(releaseBucket, pool, p)
		if err != nil {
			return ReleasePolicy{}, err
		}
		if !inserted {
			return ReleasePolicy{}, ErrInvalid
		}
	} else if err = tx.Put(releaseBucket, pool, p); err != nil {
		return ReleasePolicy{}, err
	}
	return p, nil
}
func (p ReleasePolicy) CanRelease(f TabFence, active bool, now time.Time) bool {
	if active || f.AppID == "" {
		return false
	}
	return p.Mode == Immediate || (!f.IdleSince.IsZero() && !now.Before(f.IdleSince.Add(time.Duration(p.IdleSeconds)*time.Second)))
}
