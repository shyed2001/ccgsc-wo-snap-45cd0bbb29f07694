// Path: internal/pool/reputation_ban_W9_test.go | Author: Claude Opus 5.5 | 2026-11-21
// What/why: W9 F2 - the 24 h ban of the fourth bar and its ban-until are part of
// the persisted reputation record, so a restart does not lift the ban.
package pool

import (
	"context"
	"errors"
	"testing"
	"time"

	"ccgsc/poolscore/internal/verify"
)

func TestW9F2FourthBarBanPersistsAcrossRestart(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	w := h.worker(t, 1)
	rep := h.engine.Verifier.Reputation()
	seed := anonymousSeedScore(rep.Config().BarBelow)

	// Three quiet recoveries, then a fourth bar.
	for i := 0; i < 3; i++ {
		rep.Seed(w.TabID, 0.4)
		if st := rep.Recover(w.TabID, anonymousRecoverQuiet, seed); st == verify.StatusBar || st == verify.StatusBan {
			t.Fatalf("recovery %d = %s, want recovered", i+1, st)
		}
	}
	rep.Seed(w.TabID, 0.4)
	if _, err := h.engine.Claim(ctx, Thread, "contributor", w.TabID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("claim at the fourth bar err = %v, want ErrForbidden", err)
	}
	banned, err := rep.Worker(w.TabID)
	if err != nil {
		t.Fatalf("banned record: %v", err)
	}
	if !banned.Banned || banned.BanUntil.IsZero() || banned.Recoveries != 3 {
		t.Fatalf("fourth bar state = %+v, want banned until a set time after 3 recoveries", banned)
	}

	// A restart reads the persisted record back: the ban survives.
	restarted, err := New(h.engine.Store, h.engine.Log, "s2", h.engine.TTL)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	restarted.Now = func() time.Time { return h.now }
	after, err := restarted.Verifier.Reputation().Worker(w.TabID)
	if err != nil {
		t.Fatalf("restored record: %v", err)
	}
	if !after.Banned || !after.BanUntil.Equal(banned.BanUntil) || after.Recoveries != banned.Recoveries {
		t.Fatalf("restored state = %+v, want the ban persisted", after)
	}
	if _, err := restarted.Claim(ctx, Thread, "contributor", w.TabID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("claim after restart err = %v, want ErrForbidden", err)
	}
}
