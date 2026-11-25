// Path: internal/verify/reputation_r10_test.go | R10 fix lane (web review of fe646643).
// What/why: the glm MINOR row against the ban reseed of the reputation tracker.
//
// The row names CanaryFails, but the reachable defect is one step earlier: a ban
// raised by the canary threshold (Observe, EventCanaryFail -> w.Banned = true)
// records no BanUntil, and Recover treats a zero BanUntil as a ban that never
// lapses. Such a worker is banned forever: no quiet recovery, no reseed, and only
// an explicit admin Unban lifts it. A canary ban must get the same 24 h window as
// the fourth-bar ban, so the reseed path is reachable for it too.
package verify

import (
	"testing"
	"time"
)

// TestR10CanaryBanLapsesAndReseeds is the glm MINOR row.
func TestR10CanaryBanLapsesAndReseeds(t *testing.T) {
	clock := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	r := NewReputation(nil)
	r.now = func() time.Time { return clock }

	threshold := r.Config().BanCanaryFails
	if threshold < 1 {
		t.Fatalf("BanCanaryFails = %d, want a positive threshold", threshold)
	}
	for i := 0; i < threshold; i++ {
		if _, err := r.Observe("tab", EventCanaryFail, "", "canary"); err != nil && err != ErrBannedWorker {
			t.Fatalf("canary failure %d: %v", i, err)
		}
	}
	w, err := r.Worker("tab")
	if err != nil {
		t.Fatalf("Worker: %v", err)
	}
	if !w.Banned {
		t.Fatalf("%d canary failures did not ban the worker: %+v", threshold, w)
	}
	if w.BanUntil.IsZero() {
		t.Fatalf("the canary ban records no BanUntil, so it can never lapse: %+v", w)
	}

	// After the window the worker is reseeded like a fourth-bar ban.
	clock = w.BanUntil.Add(time.Second)
	if st := r.Recover("tab", time.Minute, DefaultReputationConfig().StartScore); st == StatusBan {
		t.Fatalf("recover after the canary ban window = %q, want a reseeded worker", st)
	}
	after, err := r.Worker("tab")
	if err != nil {
		t.Fatalf("Worker: %v", err)
	}
	if after.Banned || after.CanaryFails != 0 {
		t.Fatalf("reseed state = banned %v canaryFails %d, want a clean reseed", after.Banned, after.CanaryFails)
	}
	// One further failure must not re-ban a reseeded worker.
	if _, err := r.Observe("tab", EventCanaryFail, "", "canary"); err != nil {
		t.Fatalf("first canary failure after the reseed: %v", err)
	}
	if st, err := r.StatusOf("tab"); err != nil || st == StatusBan {
		t.Fatalf("status after one canary failure following the reseed = %q (%v), want no immediate re-ban", st, err)
	}
}
