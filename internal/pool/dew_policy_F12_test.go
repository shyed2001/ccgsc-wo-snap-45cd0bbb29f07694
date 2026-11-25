package pool

// F12 policy tests (owner decisions 2026-11-24, WO section 16, G-F12-1): the
// admin window is 1-30 minutes with a 10 minute default, the per-app override
// and the job option can only shorten, and every write is version-checked so
// two servers cannot lose an update. Approval of the app is an engine-API
// concern (dewAppApproved), so these tests drive the Tx layer directly.

import (
	"context"
	"errors"
	"testing"

	"ccgsc/poolscore/internal/store"
)

func TestF12DewPolicyDefaultIsTenMinutes(t *testing.T) {
	h := newHarness(t)
	pol, err := h.engine.ReadDewPolicy(context.Background(), Thread)
	if err != nil {
		t.Fatal(err)
	}
	if pol.WindowSeconds != DefaultDewReceiptWindowSeconds {
		t.Fatalf("default window = %d, want %d", pol.WindowSeconds, DefaultDewReceiptWindowSeconds)
	}
}

func TestF12DewPolicyAdminRange(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, bad := range []int64{0, 59, 1801, 86400} {
		if _, err := h.engine.WriteDewPolicy(ctx, Thread, bad, nil); !errors.Is(err, ErrInvalid) {
			t.Fatalf("window %d accepted: %v", bad, err)
		}
	}
	if _, err := h.engine.WriteDewPolicy(ctx, "nope", 600, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid pool accepted: %v", err)
	}
	pol, err := h.engine.WriteDewPolicy(ctx, Thread, 300, nil)
	if err != nil || pol.Version != 1 || pol.WindowSeconds != 300 {
		t.Fatalf("first write = %+v, %v", pol, err)
	}
	stale := uint64(0)
	if _, err := h.engine.WriteDewPolicy(ctx, Thread, 900, &stale); !errors.Is(err, ErrDewPolicyConflict) {
		t.Fatalf("stale version accepted: %v", err)
	}
	pol, err = h.engine.WriteDewPolicy(ctx, Thread, 900, &pol.Version)
	if err != nil || pol.Version != 2 || pol.WindowSeconds != 900 {
		t.Fatalf("second write = %+v, %v", pol, err)
	}
	got, err := h.engine.ReadDewPolicy(ctx, Thread)
	if err != nil || got.WindowSeconds != 900 || got.Version != 2 {
		t.Fatalf("read back = %+v, %v", got, err)
	}
}

func TestF12DewPolicyDamagedRowFallsBackToDefault(t *testing.T) {
	h := newHarness(t)
	err := h.engine.Store.Update(context.Background(), func(tx store.Tx) error {
		return tx.Put(store.DewPolicy, dewPolicyKey(Thread), DewPolicy{WindowSeconds: 99999, Version: 7})
	})
	if err != nil {
		t.Fatal(err)
	}
	pol, err := h.engine.ReadDewPolicy(context.Background(), Thread)
	if err != nil {
		t.Fatal(err)
	}
	if pol.WindowSeconds != DefaultDewReceiptWindowSeconds {
		t.Fatalf("damaged row widened the window: %d", pol.WindowSeconds)
	}
}

func TestF12DewAppOverrideOnlyShortens(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.engine.WriteDewPolicy(ctx, Thread, 1800, nil); err != nil {
		t.Fatal(err)
	}
	// Above the admin maximum is refused at write time...
	err := h.engine.Store.Update(ctx, func(tx store.Tx) error {
		_, err := WriteDewAppTx(tx, Thread, "developer", "app-1", 2400, nil)
		return err
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("app window above the admin maximum accepted: %v", err)
	}
	// ...and a valid override is honoured at issue time.
	err = h.engine.Store.Update(ctx, func(tx store.Tx) error {
		_, err := WriteDewAppTx(tx, Thread, "developer", "app-1", 1200, nil)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	j := &Job{PoolID: Thread, Owner: "developer", AppID: "app-1"}
	var w int64
	err = h.engine.Store.View(ctx, func(rd store.Reader) (er error) {
		w, er = effectiveDewWindowTx(rd, j, 0)
		return er
	})
	if err != nil {
		t.Fatal(err)
	}
	if w != 1200 {
		t.Fatalf("effective window with app override = %d, want 1200", w)
	}
	// A job may lower the window further, never raise it.
	err = h.engine.Store.View(ctx, func(rd store.Reader) (er error) {
		if w, er = effectiveDewWindowTx(rd, j, 300); er != nil {
			return er
		}
		if w != 300 {
			t.Errorf("effective window with job option = %d, want 300", w)
		}
		if w, er = effectiveDewWindowTx(rd, j, 2400); er != nil {
			return er
		}
		if w != 1200 {
			t.Errorf("job option raised the window: %d", w)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Clearing the override restores the admin window.
	err = h.engine.Store.Update(ctx, func(tx store.Tx) error {
		_, err := WriteDewAppTx(tx, Thread, "developer", "app-1", 0, nil)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	err = h.engine.Store.View(ctx, func(rd store.Reader) (er error) {
		w, er = effectiveDewWindowTx(rd, j, 0)
		return er
	})
	if err != nil {
		t.Fatal(err)
	}
	if w != 1800 {
		t.Fatalf("effective window after clear = %d, want 1800", w)
	}
}

func TestF12EffectiveWindowCapsJobAtAdminDefault(t *testing.T) {
	h := newHarness(t)
	// The admin never wrote a policy: the 600 s default caps a job that asks
	// for more, and a job without its own window gets the default (the
	// retired draft treated the default as an explicit request and silently
	// capped every admin window above 600 s).
	j := &Job{PoolID: Thread, Owner: "developer"}
	var w int64
	err := h.engine.Store.View(context.Background(), func(rd store.Reader) (er error) {
		if w, er = effectiveDewWindowTx(rd, j, 0); er != nil {
			return er
		}
		if w != 600 {
			t.Errorf("default effective window = %d, want 600", w)
		}
		if w, er = effectiveDewWindowTx(rd, j, 1800); er != nil {
			return er
		}
		if w != 600 {
			t.Errorf("job option of 1800 s raised the default: %d", w)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
