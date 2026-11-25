// Package processapp implements durable, developer-owned logical PROCESS pools.
// Each process slot is one whole contributor browser tab. This package contains
// NO dependency on the future Thread Pool module and shares only the base store.
package processapp

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
	ErrInvalid     = errors.New("invalid process application policy")
	ErrNotApproved = errors.New("process application not approved")
	ErrFull        = errors.New("per-app tab quota reached")
	ErrBound       = errors.New("browser tab bound to another app")
)

const maxTabs = 64

var appName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

type Slot struct {
	ExpiresAt  time.Time `json:"expiresAt"`
	Generation uint64    `json:"generation"`
}
type Policy struct {
	AppID          string          `json:"appId"`
	DeveloperOwner string          `json:"developerOwner"`
	MaxTabs        int             `json:"maxTabs"`
	Active         map[string]Slot `json:"activeTabs,omitempty"`
	Version        uint64          `json:"version"`
}
type TabBinding struct {
	Owner      string    `json:"owner"`
	AppID      string    `json:"appId"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Generation uint64    `json:"generation"`
}

const bucket = "process_app_pools"
const tabBucket = "process_app_tabs"

func hashKey(raw string) string         { h := sha256.Sum256([]byte(raw)); return hex.EncodeToString(h[:]) }
func appKey(owner, appID string) string { return "app:" + hashKey(owner) + "." + appID }
func tabKey(tabIdentity string) string  { return "tab:" + hashKey(tabIdentity) }
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
	if k < 1 || k > maxTabs {
		return ErrInvalid
	}
	return s.Update(ctx, func(tx store.Tx) error { return ApproveTx(tx, owner, appID, k, now, nil) })
}

func ApproveTx(tx store.Tx, owner, appID string, k int, now time.Time, egress *appegress.Policy) error {
	if err := Validate(owner, appID); err != nil {
		return err
	}
	if k < 1 || k > maxTabs {
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
		poolID := "pp"
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
	p.MaxTabs = k
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
	if p.DeveloperOwner != owner || p.AppID != appID || p.MaxTabs < 1 || p.MaxTabs > maxTabs {
		return ErrNotApproved
	}
	return nil
}

// BindTx atomically admits one physical tab to an approved process app. A
// single locked policy row serializes K-slot admission across PostgreSQL
// coordinators; a distinct global tab-binding row enforces cross-app exclusivity.
// Caller must own an authenticated worker and call this inside Store.Update.
func BindTx(tx store.Tx, owner, appID, tabIdentity string, now time.Time, ttl time.Duration) (TabBinding, error) {
	if err := Validate(owner, appID); err != nil {
		return TabBinding{}, err
	}
	if len(tabIdentity) < 3 || len(tabIdentity) > 512 || ttl < time.Second || ttl > 24*time.Hour {
		return TabBinding{}, ErrInvalid
	}
	key := appKey(owner, appID)
	var p Policy
	if err := tx.Get(bucket, key, &p); errors.Is(err, store.ErrNotFound) {
		return TabBinding{}, ErrNotApproved
	} else if err != nil {
		return TabBinding{}, err
	}
	if p.DeveloperOwner != owner || p.AppID != appID || p.MaxTabs < 1 || p.MaxTabs > maxTabs {
		return TabBinding{}, ErrNotApproved
	}
	if p.Active == nil {
		p.Active = make(map[string]Slot)
	}
	tabID := hashKey(tabIdentity)
	for id, slot := range p.Active {
		if !slot.ExpiresAt.After(now) {
			delete(p.Active, id)
		}
	}
	tbKey := tabKey(tabIdentity)
	var existing TabBinding
	err := tx.Get(tabBucket, tbKey, &existing)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return TabBinding{}, err
	}
	if err == nil && existing.ExpiresAt.After(now) && (existing.Owner != owner || existing.AppID != appID) {
		return TabBinding{}, ErrBound
	}
	slot, known := p.Active[tabID]
	if !known && len(p.Active) >= p.MaxTabs {
		return TabBinding{}, ErrFull
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
	binding := TabBinding{Owner: owner, AppID: appID, ExpiresAt: expires, Generation: generation}
	if errors.Is(err, store.ErrNotFound) {
		insertion, ok := tx.(store.PutIfAbsentTx)
		if !ok {
			return TabBinding{}, store.ErrNotImplemented
		}
		inserted, err := insertion.PutIfAbsent(tabBucket, tbKey, binding)
		if err != nil {
			return TabBinding{}, err
		}
		if !inserted {
			return TabBinding{}, ErrBound
		}
	} else if err := tx.Put(tabBucket, tbKey, binding); err != nil {
		return TabBinding{}, err
	}
	p.Active[tabID] = Slot{ExpiresAt: expires, Generation: generation}
	p.Version++
	if err := tx.Put(bucket, key, p); err != nil {
		return TabBinding{}, err
	}
	return binding, nil
}
func Bind(ctx context.Context, s store.Store, owner, appID, tabIdentity string, now time.Time, ttl time.Duration) (TabBinding, error) {
	var b TabBinding
	err := s.Update(ctx, func(tx store.Tx) error { var e error; b, e = BindTx(tx, owner, appID, tabIdentity, now, ttl); return e })
	return b, err
}

// ReleaseTx drops the app slot, retaining an expired tab row for idempotent
// retry. The caller must authenticate the worker that owns tabIdentity.
func ReleaseTx(tx store.Tx, owner, appID, tabIdentity string, now time.Time) error {
	if err := Validate(owner, appID); err != nil {
		return err
	}
	var p Policy
	if err := tx.Get(bucket, appKey(owner, appID), &p); err != nil {
		return err
	}
	var tb TabBinding
	if err := tx.Get(tabBucket, tabKey(tabIdentity), &tb); err != nil {
		return err
	}
	if tb.AppID != appID || tb.Owner != owner {
		return ErrBound
	}
	delete(p.Active, hashKey(tabIdentity))
	tb.ExpiresAt = now
	p.Version++
	if err := tx.Put(tabBucket, tabKey(tabIdentity), tb); err != nil {
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

// TabIdentity composes the actual browser host+tab identifiers. A tab's
// server-assigned worker ID must not be used to evade quotas on re-register.
func TabIdentity(hostID, browserTabID string) (string, error) {
	if len(hostID) < 1 || len(hostID) > 256 || len(browserTabID) < 1 || len(browserTabID) > 128 {
		return "", fmt.Errorf("%w: host/tab id required", ErrInvalid)
	}
	return fmt.Sprintf("%d:%s:%s", len(hostID), hostID, browserTabID), nil
}
