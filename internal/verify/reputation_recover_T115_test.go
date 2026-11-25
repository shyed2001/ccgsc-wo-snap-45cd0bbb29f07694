// Path: internal/verify/reputation_recover_T115_test.go | Author: Claude Opus 5.5 | 2026-11-21
// What/why: T115 option 3 - a bar decays after a quiet period, a ban never does.
package verify

import (
	"testing"
	"time"
)

func TestT115RecoverLiftsBarAfterQuietNeverBan(t *testing.T) {
	clock := time.Date(2026, 11, 21, 5, 0, 0, 0, time.UTC)
	r := NewReputation(nil)
	r.now = func() time.Time { return clock }
	r.Seed("w1", 0.4)
	r.workers["w1"].LastEvent = clock
	if st := r.Recover("w1", 15*time.Minute, 0.55); st != StatusBar {
		t.Fatalf("inside the quiet period status = %s, want bar", st)
	}
	clock = clock.Add(16 * time.Minute)
	if st := r.Recover("w1", 15*time.Minute, 0.55); st == StatusBar || st == StatusBan {
		t.Fatalf("after the quiet period status = %s, want recovered", st)
	}
	r.Seed("w2", 0.1)
	r.workers["w2"].Banned = true
	if st := r.Recover("w2", 0, 0.55); st != StatusBan {
		t.Fatalf("banned worker status = %s, want ban", st)
	}
}
