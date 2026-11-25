// Package threadapp implements durable, developer-owned logical THREAD pools.
// Each thread slot is one individual browser Web Worker. This package contains
// NO dependency on Process Pool module and shares only the base store.
package threadapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	"ccgsc/poolscore/internal/appegress"
	"ccgsc/poolscore/internal/store"
	"slices"
)

var (
	ErrInvalid     = errors.New("invalid thread application policy")
	ErrNotApproved = errors.New("thread application not approved")
	ErrFull        = errors.New("per-app tab quota reached")
	ErrBound       = errors.New("browser tab bound to another app")
)

const maxWorkers = 64

var appName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

type Slot struct {
	ExpiresAt  time.Time `json:"expiresAt"`
	Generation uint64    `json:"generation"`
}
type Policy struct {
	AppID          string          `json:"appId"`
	DeveloperOwner string          `json:"developerOwner"`
	MaxWorkers     int             `json:"maxWorkers"`
	Active         map[string]Slot `json:"activeTabs,omitempty"`
	Version        uint64          `json:"version"`
}
type SlotBinding struct {
	Owner      string    `json:"owner"`
	AppID      string    `json:"appId"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Generation uint64    `json:"generation"`
}

const bucket = "thread_app_pools"
const slotBucket = "thread_app_slots"

func hashKey(raw string) string          { h := sha256.Sum256([]byte(raw)); return hex.EncodeToString(h[:]) }
func appKey(owner, appID string) string  { return "app:" + hashKey(owner) + "." + appID }
func slotKey(slotIdentity string) string { return "slot:" + hashKey(slotIdentity) }
func Validate(owner, appID string) error {
	if owner == "" || len(owner) > 256 || !appName.MatchString(appID) {
		return ErrInvalid
	}
	return nil
}

// Approve changes or creates a policy, to be called only by an admin-authenticated
// API. The registry itself does not create permissions for callers.
func Approve(ctx context.Context, s store.Store, owner, appID string, k int, now time.Time) error {
	if err := Validate(owner, appID); err != nil {
		return err
	}
	if k < 1 || k > maxWorkers {
		return ErrInvalid
	}
	return s.Update(ctx, func(tx store.Tx) error { return ApproveTx(tx, owner, appID, k, now, nil) })
}

func ApproveTx(tx store.Tx, owner, appID string, k int, now time.Time, egress *appegress.Policy) error {
	if err := Validate(owner, appID); err != nil {
		return err
	}
	if k < 1 || k > maxWorkers {
		return ErrInvalid
	}

	key := appKey(owner, appID)
	p := Policy{}
	err := tx.Get(bucket, key, &p)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err == nil && (p.DeveloperOwner != owner || p.AppID != appID) {
		return ErrInvalid
	}
	if err != nil {
		p = Policy{AppID: appID, DeveloperOwner: owner, Active: map[string]Slot{}}
	}
	active := 0
	for id, slot := range p.Active {
		if !slot.ExpiresAt.After(now) {
			delete(p.Active, id)
		} else {
			active++
		}
	}
	if egress != nil {
		poolID := "tp"
		old, err := appegress.ReadTx(tx, poolID, owner, appID)
		if err != nil {
			return err
		}
		normalized, err := appegress.Normalize(*egress)
		if err != nil {
			return ErrInvalid
		}
		if !slices.Equal(old.Endpoints, normalized.Endpoints) || old.TokenServiceURL != normalized.TokenServiceURL {
			if active > 0 {
				return ErrBound
			}
			if _, err = appegress.WriteTx(tx, poolID, owner, appID, normalized); err != nil {
				return err
			}
		}
	}
	if active > k {
		return ErrFull
	}
	p.MaxWorkers = k
	p.Version++
	return tx.Put(bucket, key, p)

}

// ApprovedTx checks the durable admin-approved app policy in the calling
// transaction. Its caller must separately verify developer identity.
func ApprovedTx(tx store.Tx, owner, appID string) error {
	if err := Validate(owner, appID); err != nil {
		return err
	}
	var p Policy
	if err := tx.Get(bucket, appKey(owner, appID), &p); errors.Is(err, store.ErrNotFound) {
		return ErrNotApproved
	} else if err != nil {
		return err
	}
	if p.DeveloperOwner != owner || p.AppID != appID || p.MaxWorkers < 1 || p.MaxWorkers > maxWorkers {
		return ErrNotApproved
	}
	return nil
}

// BindTx atomically admits one physical tab to an approved thread app. A
// single locked policy row serializes K-slot admission across PostgreSQL
// coordinators; a distinct global tab-binding row enforces cross-app exclusivity.
// Caller must own an authenticated worker and call this inside Store.Update.
func BindTx(tx store.Tx, owner, appID, slotIdentity string, now time.Time, ttl time.Duration) (SlotBinding, error) {
	if err := Validate(owner, appID); err != nil {
		return SlotBinding{}, err
	}
	if len(slotIdentity) < 3 || len(slotIdentity) > 512 || ttl < time.Second || ttl > 24*time.Hour {
		return SlotBinding{}, ErrInvalid
	}
	key := appKey(owner, appID)
	var p Policy
	if err := tx.Get(bucket, key, &p); errors.Is(err, store.ErrNotFound) {
		return SlotBinding{}, ErrNotApproved
	} else if err != nil {
		return SlotBinding{}, err
	}
	if p.DeveloperOwner != owner || p.AppID != appID || p.MaxWorkers < 1 || p.MaxWorkers > maxWorkers {
		return SlotBinding{}, ErrNotApproved
	}
	if p.Active == nil {
		p.Active = make(map[string]Slot)
	}
	slotID := hashKey(slotIdentity)
	for id, slot := range p.Active {
		if !slot.ExpiresAt.After(now) {
			delete(p.Active, id)
		}
	}
	tbKey := slotKey(slotIdentity)
	var existing SlotBinding
	err := tx.Get(slotBucket, tbKey, &existing)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return SlotBinding{}, err
	}
	if err == nil && existing.ExpiresAt.After(now) && (existing.Owner != owner || existing.AppID != appID) {
		return SlotBinding{}, ErrBound
	}
	slot, known := p.Active[slotID]
	if !known && len(p.Active) >= p.MaxWorkers {
		return SlotBinding{}, ErrFull
	}
	generation := uint64(1)
	if known && slot.Generation > 0 {
		generation = slot.Generation
	}
	if err == nil && existing.Generation >= generation {
		generation = existing.Generation
	}
	if !known || !slot.ExpiresAt.After(now) {
		generation++
	}
	if generation == 0 {
		generation = 1
	}
	expires := now.Add(ttl)
	binding := SlotBinding{Owner: owner, AppID: appID, ExpiresAt: expires, Generation: generation}
	if errors.Is(err, store.ErrNotFound) {
		insertion, ok := tx.(store.PutIfAbsentTx)
		if !ok {
			return SlotBinding{}, store.ErrNotImplemented
		}
		inserted, err := insertion.PutIfAbsent(slotBucket, tbKey, binding)
		if err != nil {
			return SlotBinding{}, err
		}
		if !inserted {
			return SlotBinding{}, ErrBound
		}
	} else if err := tx.Put(slotBucket, tbKey, binding); err != nil {
		return SlotBinding{}, err
	}
	p.Active[slotID] = Slot{ExpiresAt: expires, Generation: generation}
	p.Version++
	if err := tx.Put(bucket, key, p); err != nil {
		return SlotBinding{}, err
	}
	return binding, nil
}
func Bind(ctx context.Context, s store.Store, owner, appID, slotIdentity string, now time.Time, ttl time.Duration) (SlotBinding, error) {
	var b SlotBinding
	err := s.Update(ctx, func(tx store.Tx) error {
		var e error
		b, e = BindTx(tx, owner, appID, slotIdentity, now, ttl)
		return e
	})
	return b, err
}

// ReleaseTx drops the app slot, retaining an expired tab row for idempotent
// retry. The caller must authenticate the worker that owns slotIdentity.
func ReleaseTx(tx store.Tx, owner, appID, slotIdentity string, now time.Time) error {
	if err := Validate(owner, appID); err != nil {
		return err
	}
	var p Policy
	if err := tx.Get(bucket, appKey(owner, appID), &p); err != nil {
		return err
	}
	var tb SlotBinding
	if err := tx.Get(slotBucket, slotKey(slotIdentity), &tb); err != nil {
		return err
	}
	if tb.AppID != appID || tb.Owner != owner {
		return ErrBound
	}
	delete(p.Active, hashKey(slotIdentity))
	tb.ExpiresAt = now
	p.Version++
	if err := tx.Put(slotBucket, slotKey(slotIdentity), tb); err != nil {
		return err
	}
	return tx.Put(bucket, appKey(owner, appID), p)
}
func Release(ctx context.Context, s store.Store, owner, appID, tab string, now time.Time) error {
	return s.Update(ctx, func(tx store.Tx) error { return ReleaseTx(tx, owner, appID, tab, now) })
}
func Read(ctx context.Context, s store.Store, owner, appID string) (Policy, error) {
	if err := Validate(owner, appID); err != nil {
		return Policy{}, err
	}
	var p Policy
	err := s.View(ctx, func(r store.Reader) error { return r.Get(bucket, appKey(owner, appID), &p) })
	if errors.Is(err, store.ErrNotFound) {
		err = ErrNotApproved
	}
	if err == nil && (p.DeveloperOwner != owner || p.AppID != appID) {
		err = ErrNotApproved
	}
	return p, err
}

// SlotIdentity composes the actual browser host+tab identifiers. A tab's
// server-assigned worker ID must not be used to evade quotas on re-register.
func SlotIdentity(hostID, browserTabID string, workerIndex int) (string, error) {
	if len(hostID) < 1 || len(hostID) > 256 || len(browserTabID) < 1 || len(browserTabID) > 128 {
		return "", fmt.Errorf("%w: host/tab id required", ErrInvalid)
	}
	if workerIndex < 0 || workerIndex >= 64 {
		return "", ErrInvalid
	}
	return fmt.Sprintf("%d:%s:%s:worker-%d", len(hostID), hostID, browserTabID, workerIndex), nil
}
