// Path: internal/verify/reputation_recover_W9_test.go | Author: Claude Opus 5.5 | 2026-11-21
// What/why: W9 F2 - progressive quiet recovery: 15 min, 1 h, 4 h, the fourth
// bar is a 24 h ban and after it the worker is reseeded; the recovery count and
// ban-until survive a snapshot/restore, and only Unban lifts a ban inside 24 h.
package verify

import (
	"testing"
	"time"
)

func TestW9F2ProgressiveRecoveryTimeline(t *testing.T) {
	t0 := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	clock := t0
	r := NewReputation(nil)
	r.now = func() time.Time { return clock }
	const seed = 0.55
	barAt := func(at time.Time) {
		clock = at
		r.Seed("w1", 0.4)
		r.workers["w1"].LastEvent = at
	}
	recoveries := func() int { return r.workers["w1"].Recoveries }

	// First bar: 15 minutes of quiet.
	barAt(t0)
	clock = t0.Add(15*time.Minute - time.Second)
	if st := r.Recover("w1", 15*time.Minute, seed); st != StatusBar {
		t.Fatalf("first bar 1s early = %s, want bar", st)
	}
	clock = t0.Add(15 * time.Minute)
	if st := r.Recover("w1", 15*time.Minute, seed); st == StatusBar || st == StatusBan {
		t.Fatalf("first recovery = %s, want recovered", st)
	}
	if recoveries() != 1 {
		t.Fatalf("recoveries = %d, want 1", recoveries())
	}

	// Second bar: 1 hour (15 min x 4).
	t1 := clock
	barAt(t1)
	clock = t1.Add(time.Hour - time.Second)
	if st := r.Recover("w1", 15*time.Minute, seed); st != StatusBar {
		t.Fatalf("second bar 1s early = %s, want bar", st)
	}
	clock = t1.Add(time.Hour)
	if st := r.Recover("w1", 15*time.Minute, seed); st == StatusBar || st == StatusBan {
		t.Fatalf("second recovery = %s, want recovered", st)
	}
	if recoveries() != 2 {
		t.Fatalf("recoveries = %d, want 2", recoveries())
	}

	// Third bar: 4 hours (15 min x 16).
	t2 := clock
	barAt(t2)
	clock = t2.Add(4*time.Hour - time.Second)
	if st := r.Recover("w1", 15*time.Minute, seed); st != StatusBar {
		t.Fatalf("third bar 1s early = %s, want bar", st)
	}
	clock = t2.Add(4 * time.Hour)
	if st := r.Recover("w1", 15*time.Minute, seed); st == StatusBar || st == StatusBan {
		t.Fatalf("third recovery = %s, want recovered", st)
	}
	if recoveries() != 3 {
		t.Fatalf("recoveries = %d, want 3", recoveries())
	}

	// Fourth bar: a 24 h ban, not another quiet recovery.
	t3 := clock
	barAt(t3)
	banUntil := t3.Add(24 * time.Hour)
	if st := r.Recover("w1", 15*time.Minute, seed); st != StatusBan {
		t.Fatalf("fourth bar = %s, want ban", st)
	}
	w, err := r.Worker("w1")
	if err != nil {
		t.Fatalf("Worker: %v", err)
	}
	if !w.Banned || !w.BanUntil.Equal(banUntil) {
		t.Fatalf("ban state = banned %v until %v, want until %v", w.Banned, w.BanUntil, banUntil)
	}

	// The recovery count and ban-until survive a snapshot/restore.
	restored := NewReputation(nil)
	restored.now = func() time.Time { return clock }
	restored.Restore(r.Snapshot())
	w, err = restored.Worker("w1")
	if err != nil {
		t.Fatalf("restored Worker: %v", err)
	}
	if w.Recoveries != 3 || !w.BanUntil.Equal(banUntil) {
		t.Fatalf("restored state = recoveries %d until %v, want 3 until %v", w.Recoveries, w.BanUntil, banUntil)
	}

	// Inside the ban nothing lifts it but an owner/admin unbar.
	clock = banUntil.Add(-time.Second)
	if st := restored.Recover("w1", 15*time.Minute, seed); st != StatusBan {
		t.Fatalf("1s before ban end = %s, want ban", st)
	}

	// After 24 h the worker is reseeded at the seed score.
	clock = banUntil
	if st := restored.Recover("w1", 15*time.Minute, seed); st == StatusBan || st == StatusBar {
		t.Fatalf("after ban = %s, want reseeded", st)
	}
	w, err = restored.Worker("w1")
	if err != nil {
		t.Fatalf("reseeded Worker: %v", err)
	}
	if w.Banned || !w.BanUntil.IsZero() || w.Recoveries != 0 {
		t.Fatalf("reseed state = banned %v until %v recoveries %d, want a clean reseed", w.Banned, w.BanUntil, w.Recoveries)
	}
	if w.Score != seed {
		t.Fatalf("reseeded score = %v, want %v", w.Score, seed)
	}
}

func TestW9F2OwnerUnbanLiftsBanInsideWindow(t *testing.T) {
	clock := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	r := NewReputation(nil)
	r.now = func() time.Time { return clock }
	r.Seed("w2", 0.4)
	r.workers["w2"].Recoveries = 3
	if st := r.Recover("w2", 15*time.Minute, 0.55); st != StatusBan {
		t.Fatalf("fourth bar = %s, want ban", st)
	}
	clock = clock.Add(time.Hour)
	w, err := r.Unban("w2")
	if err != nil {
		t.Fatalf("Unban: %v", err)
	}
	if w.Banned || w.Status == StatusBan || !w.BanUntil.IsZero() || w.Recoveries != 0 {
		t.Fatalf("unbar state = %+v, want ban lifted and escalation reset", w)
	}
}
