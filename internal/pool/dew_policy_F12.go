package pool

// F12 (owner decision 2026-11-24, WO section 16 G-F12-1): the Dew receipt
// window is NOT 24 h. The admin sets it per pool in 1..30 minutes (default 10
// minutes), a developer may lower it for one approved app (never above the
// current admin value), and one job may lower it again with its own dew
// option. Each layer can only shorten. Everything is stored durably and read
// again at every receipt issue, so two server instances always issue the same
// window and a later admin decrease still wins at the next claim.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"ccgsc/poolscore/internal/processapp"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/threadapp"
	"ccgsc/poolscore/internal/verify"
)

const (
	// DewMinWindowSeconds is the smallest admin/app Dew window: 1 minute.
	DewMinWindowSeconds int64 = 60
	// DewMaxWindowSeconds is the largest admin Dew window: 30 minutes.
	DewMaxWindowSeconds int64 = verify.MaxTaskReceiptWindowSeconds
)

// ErrDewPolicyConflict reports a stale expectedVersion on a Dew policy write.
var ErrDewPolicyConflict = errors.New("dew policy version conflict")

// DewPolicy is the admin Dew window of one pool. WindowSeconds is both the
// default window and the maximum any app or job may ask for.
type DewPolicy struct {
	WindowSeconds int64  `json:"windowSeconds"`
	Version       uint64 `json:"version"`
}

// DewAppPolicy is the optional developer override of one approved app.
type DewAppPolicy struct {
	PoolID string `json:"poolId"`
	Owner  string `json:"owner"`
	AppID  string `json:"appId"`
	// WindowSeconds is the requested window; 0 means no override. It is never
	// used above the admin window of the moment (capped at issue time).
	WindowSeconds int64  `json:"windowSeconds"`
	Version       uint64 `json:"version"`
}

// DefaultDewPolicy is the owner default: 10 minutes.
func DefaultDewPolicy() DewPolicy {
	return DewPolicy{WindowSeconds: DefaultDewReceiptWindowSeconds}
}

func validDewWindow(s int64) bool { return s >= DewMinWindowSeconds && s <= DewMaxWindowSeconds }

func dewPolicyKey(p string) string { return "pool:" + p }

func dewAppPolicyKey(p, owner, app string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s", p, owner, app)))
	return "app:" + hex.EncodeToString(sum[:])
}

func validDewPool(p string) bool { return p == Thread || p == Process }

// ReadDewPolicyTx returns the stored admin policy or the default.
func ReadDewPolicyTx(r store.Reader, p string) (DewPolicy, error) {
	if !validDewPool(p) {
		return DewPolicy{}, ErrInvalid
	}
	var v DewPolicy
	err := r.Get(store.DewPolicy, dewPolicyKey(p), &v)
	if errors.Is(err, store.ErrNotFound) {
		return DefaultDewPolicy(), nil
	}
	if err != nil {
		return DewPolicy{}, err
	}
	if !validDewWindow(v.WindowSeconds) {
		// A damaged row never widens the owner window: fall back to the default.
		return DefaultDewPolicy(), nil
	}
	return v, nil
}

// WriteDewPolicyTx replaces the admin policy. expected, when not nil, is the
// version the caller read: a mismatch is ErrDewPolicyConflict, so two servers
// cannot lose one another's update. The first write of a missing row is a
// plain insert: racing first writers both compute version 1 and the store
// keeps one of the two valid windows; every later write is version-checked.
func WriteDewPolicyTx(tx store.Tx, p string, windowSeconds int64, expected *uint64) (DewPolicy, error) {
	if !validDewPool(p) || !validDewWindow(windowSeconds) {
		return DewPolicy{}, ErrInvalid
	}
	var old DewPolicy
	err := tx.Get(store.DewPolicy, dewPolicyKey(p), &old)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return DewPolicy{}, err
	}
	if errors.Is(err, store.ErrNotFound) {
		old = DewPolicy{}
	}
	if expected != nil && *expected != old.Version {
		return DewPolicy{}, ErrDewPolicyConflict
	}
	v := DewPolicy{WindowSeconds: windowSeconds, Version: old.Version + 1}
	return v, tx.Put(store.DewPolicy, dewPolicyKey(p), v)
}

// ReadDewAppTx returns the app override, or a zero override when none exists.
func ReadDewAppTx(r store.Reader, p, owner, app string) (DewAppPolicy, error) {
	if !validDewPool(p) || strings.TrimSpace(owner) == "" || strings.TrimSpace(app) == "" {
		return DewAppPolicy{}, ErrInvalid
	}
	var v DewAppPolicy
	err := r.Get(store.DewPolicy, dewAppPolicyKey(p, owner, app), &v)
	if errors.Is(err, store.ErrNotFound) {
		return DewAppPolicy{PoolID: p, Owner: owner, AppID: app}, nil
	}
	return v, err
}

// WriteDewAppTx stores the override of one app. windowSeconds 0 clears it. A
// value above the CURRENT admin window is refused here and is capped again
// whenever a receipt is issued, so a later admin decrease still wins.
func WriteDewAppTx(tx store.Tx, p, owner, app string, windowSeconds int64, expected *uint64) (DewAppPolicy, error) {
	if !validDewPool(p) || strings.TrimSpace(owner) == "" || strings.TrimSpace(app) == "" {
		return DewAppPolicy{}, ErrInvalid
	}
	if windowSeconds != 0 && !validDewWindow(windowSeconds) {
		return DewAppPolicy{}, ErrInvalid
	}
	admin, err := ReadDewPolicyTx(tx, p)
	if err != nil {
		return DewAppPolicy{}, err
	}
	if windowSeconds > admin.WindowSeconds {
		return DewAppPolicy{}, fmt.Errorf("%w: app window %d s exceeds the admin maximum %d s", ErrInvalid, windowSeconds, admin.WindowSeconds)
	}
	key := dewAppPolicyKey(p, owner, app)
	var old DewAppPolicy
	err = tx.Get(store.DewPolicy, key, &old)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return DewAppPolicy{}, err
	}
	if errors.Is(err, store.ErrNotFound) {
		old = DewAppPolicy{}
	}
	if expected != nil && *expected != old.Version {
		return DewAppPolicy{}, ErrDewPolicyConflict
	}
	v := DewAppPolicy{PoolID: p, Owner: owner, AppID: app, WindowSeconds: windowSeconds, Version: old.Version + 1}
	return v, tx.Put(store.DewPolicy, key, v)
}

// effectiveDewWindowTx answers the window of one receipt: the admin value, or
// the lower app override, or the lower explicit job request. It never exceeds
// the admin value of the moment and never leaves 1..30 minutes. jobRequested
// is 0 unless the job named receiptWindowSeconds itself - the 600 s default
// of an absent option must never silently cap an admin window above it.
func effectiveDewWindowTx(r store.Reader, j *Job, jobRequested int64) (int64, error) {
	admin, err := ReadDewPolicyTx(r, j.PoolID)
	if err != nil {
		return 0, err
	}
	window := admin.WindowSeconds
	if j.AppID != "" {
		app, err := ReadDewAppTx(r, j.PoolID, j.Owner, j.AppID)
		if err != nil {
			return 0, err
		}
		if app.WindowSeconds >= DewMinWindowSeconds && app.WindowSeconds < window {
			window = app.WindowSeconds
		}
	}
	if jobRequested >= DewMinWindowSeconds && jobRequested < window {
		window = jobRequested
	}
	return window, nil
}

// ReadDewPolicy is the engine accessor of the admin Dew window.
func (e *Engine) ReadDewPolicy(ctx context.Context, p string) (DewPolicy, error) {
	var v DewPolicy
	err := e.Store.View(ctx, func(r store.Reader) error {
		var er error
		v, er = ReadDewPolicyTx(r, p)
		return er
	})
	return v, err
}

// WriteDewPolicy stores the admin Dew window. The caller (admin API) is the only
// authorization point; the window is validated to 1..30 minutes here.
func (e *Engine) WriteDewPolicy(ctx context.Context, p string, windowSeconds int64, expected *uint64) (DewPolicy, error) {
	var out DewPolicy
	err := e.Store.Update(ctx, func(tx store.Tx) error {
		var er error
		out, er = WriteDewPolicyTx(tx, p, windowSeconds, expected)
		return er
	})
	return out, err
}

func (e *Engine) dewAppApproved(ctx context.Context, p, owner, app string) error {
	var err error
	switch p {
	case Thread:
		_, err = threadapp.Read(ctx, e.Store, owner, app)
	case Process:
		_, err = processapp.Read(ctx, e.Store, owner, app)
	default:
		return ErrInvalid
	}
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, threadapp.ErrNotApproved) || errors.Is(err, processapp.ErrNotApproved) {
		return ErrForbidden
	}
	return err
}

// ReadDewApp returns the override of one approved app and the admin maximum.
func (e *Engine) ReadDewApp(ctx context.Context, p, owner, app string) (DewAppPolicy, DewPolicy, error) {
	if err := e.dewAppApproved(ctx, p, owner, app); err != nil {
		return DewAppPolicy{}, DewPolicy{}, err
	}
	var v DewAppPolicy
	var admin DewPolicy
	err := e.Store.View(ctx, func(r store.Reader) error {
		var er error
		if admin, er = ReadDewPolicyTx(r, p); er != nil {
			return er
		}
		v, er = ReadDewAppTx(r, p, owner, app)
		return er
	})
	return v, admin, err
}

// WriteDewApp stores the per-app override. The caller authorizes: the developer
// who owns the app, or an admin. The app has to be an approved app of that owner.
func (e *Engine) WriteDewApp(ctx context.Context, p, owner, app string, windowSeconds int64, expected *uint64) (DewAppPolicy, error) {
	if err := e.dewAppApproved(ctx, p, owner, app); err != nil {
		return DewAppPolicy{}, err
	}
	var out DewAppPolicy
	err := e.Store.Update(ctx, func(tx store.Tx) error {
		var er error
		out, er = WriteDewAppTx(tx, p, owner, app, windowSeconds, expected)
		return er
	})
	return out, err
}
