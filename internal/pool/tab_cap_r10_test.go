// Path: internal/pool/tab_cap_r10_test.go | R10 fix lane (web review of fe646643).
// What/why: the glm "not sure" row against the per-owner tab cap in Register. The
// cap counted every worker record of the owner, including records whose heartbeat
// is long dead. The only reaper (EvictStaleAnonymousTabs) is anonymous-only, so a
// stored-key subject that closed its tabs could never register again: the dead
// records held its slots forever.
package pool

import (
	"context"
	"testing"
	"time"
)

// TestR10NonAnonymousCapIgnoresDeadTabs is the glm row.
func TestR10NonAnonymousCapIgnoresDeadTabs(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.engine.SetSessionLimits(SessionLimits{MaxWorkersPerOwner: 1, MaxTabsPerOwner: 8, MaxSlotsPerWorker: 8})

	// One live tab of the developer, then let it die.
	first, err := h.engine.Register(ctx, Thread, "developer", Worker{HostID: "old-host", Slots: 1})
	if err != nil {
		t.Fatalf("first register: %v", err)
	}
	if first.Anonymous {
		t.Fatal("fixture is not a stored-key tab")
	}
	h.now = h.now.Add(3 * h.engine.TTL)

	// A new browser of the same subject must get the slot the dead tab held.
	second, err := h.engine.Register(ctx, Thread, "developer", Worker{HostID: "new-host", Slots: 1})
	if err != nil {
		t.Fatalf("re-register after the old tab died: %v", err)
	}
	if second.TabID == first.TabID {
		t.Fatal("the dead tab was reused instead of released")
	}
	if second.TabID == "" {
		t.Fatal("no tab id")
	}
	// And the live cap still bounds the owner: with one dead and one live record,
	// a third tab is refused.
	if _, err = h.engine.Register(ctx, Thread, "developer", Worker{HostID: "third-host", Slots: 1}); err == nil {
		t.Fatal("a third live tab was accepted over the cap of one")
	}
	_ = time.Second
}
