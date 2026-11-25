package appegress

import (
	"context"
	"path/filepath"
	"testing"

	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/sqlite"
)

// F10b-15/F10b-18: pool identifiers, modes and idle bounds are validated at the
// store boundary; the durable default is idle-timeout at 30 seconds.
func TestF10cReleasePolicyValidationAndDefaults(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "release.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	var first ReleasePolicy
	if err = db.View(ctx, func(r store.Reader) error {
		var er error
		first, er = ReadReleaseTx(r, "tp")
		return er
	}); err != nil {
		t.Fatal(err)
	}
	if first != DefaultReleasePolicy() || first.Version != 0 {
		t.Fatalf("default release policy: %+v", first)
	}
	for _, tc := range []struct {
		name string
		pool string
		p    ReleasePolicy
	}{
		{"invalid pool", "bogus", ReleasePolicy{Mode: IdleTimeout, IdleSeconds: 30}},
		{"invalid mode", "tp", ReleasePolicy{Mode: "right-away", IdleSeconds: 30}},
		{"empty mode", "tp", ReleasePolicy{}},
		{"zero idle", "tp", ReleasePolicy{Mode: IdleTimeout, IdleSeconds: 0}},
		{"idle too long", "pp", ReleasePolicy{Mode: IdleTimeout, IdleSeconds: 3601}},
	} {
		if err = db.Update(ctx, func(tx store.Tx) error {
			_, er := WriteReleaseTx(tx, tc.pool, tc.p)
			return er
		}); err == nil {
			t.Errorf("%s accepted", tc.name)
		}
	}
	for _, seconds := range []int{1, 3600} {
		if err = db.Update(ctx, func(tx store.Tx) error {
			_, er := WriteReleaseTx(tx, "tp", ReleasePolicy{Mode: IdleTimeout, IdleSeconds: seconds})
			return er
		}); err != nil {
			t.Fatalf("idle %d refused: %v", seconds, err)
		}
	}
	var stored ReleasePolicy
	if err = db.View(ctx, func(r store.Reader) error {
		var er error
		stored, er = ReadReleaseTx(r, "tp")
		return er
	}); err != nil {
		t.Fatal(err)
	}
	if stored.Mode != IdleTimeout || stored.IdleSeconds != 3600 || stored.Version != 2 {
		t.Fatalf("stored release policy: %+v", stored)
	}
	if err = db.Update(ctx, func(tx store.Tx) error {
		_, er := WriteReleaseTx(tx, "tp", ReleasePolicy{Mode: Immediate, IdleSeconds: 30})
		return er
	}); err != nil {
		t.Fatal(err)
	}
	var final ReleasePolicy
	if err = db.View(ctx, func(r store.Reader) error {
		var er error
		final, er = ReadReleaseTx(r, "tp")
		return er
	}); err != nil {
		t.Fatal(err)
	}
	if final.Mode != Immediate || final.IdleSeconds != 30 || final.Version != 3 {
		t.Fatalf("final release policy: %+v", final)
	}
}
