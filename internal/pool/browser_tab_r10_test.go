// Path: internal/pool/browser_tab_r10_test.go | R10 fix lane (web review of fe646643).
// What&why: the chatgpt MAJOR row against Register's re-register match. The reuse
// key was (owner, pool, HostID). Two browser tabs of the SAME developer share
// localStorage, so they report the same HostID: the second tab's register then
// matched the first tab's record and overwrote it. One tab silently disappeared
// from the pool and both tabs claimed one Worker record.
//
// The fix adds the browser tab id (sessionStorage per tab) to the register body
// and to the reuse match; an empty id keeps the old rule, so a client that does
// not send it behaves exactly as before.
package pool

import (
	"context"
	"encoding/json"
	"testing"

	"ccgsc/poolscore/internal/store"
)

// countOwnerTabs counts the worker records of one owner in this pool.
func countOwnerTabs(t *testing.T, h *harness, owner string) int {
	t.Helper()
	n := 0
	if err := h.engine.Store.View(context.Background(), func(r store.Reader) error {
		rows, err := r.List(store.Workers)
		if err != nil {
			return err
		}
		for _, row := range rows {
			var w Worker
			if err := json.Unmarshal(row.Value, &w); err != nil {
				return err
			}
			if w.Owner == owner && w.PoolID == Thread {
				n++
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestR10TwoBrowserTabsOfOneOwnerAreTwoWorkers is the chatgpt MAJOR row.
func TestR10TwoBrowserTabsOfOneOwnerAreTwoWorkers(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_, err := h.engine.Register(ctx, Thread, "developer", Worker{HostID: "shared-host", BrowserTabID: "tab-1", Slots: 1})
	if err != nil {
		t.Fatalf("first tab: %v", err)
	}
	second, err := h.engine.Register(ctx, Thread, "developer", Worker{HostID: "shared-host", BrowserTabID: "tab-2", Slots: 1})
	if err != nil {
		t.Fatalf("second tab: %v", err)
	}
	if second.BrowserTabID != "tab-2" {
		t.Fatalf("stored browser tab id = %q, want tab-2", second.BrowserTabID)
	}
	if seen := countOwnerTabs(t, h, "developer"); seen != 2 {
		t.Fatalf("worker records of the developer = %d, want 2 (two browser tabs)", seen)
	}
}

// TestR10SameBrowserTabReloadReusesItsWorker pins the reuse the fix must keep: the
// same browser tab registering again (a reload) is the same Worker.
func TestR10SameBrowserTabReloadReusesItsWorker(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	first, err := h.engine.Register(ctx, Thread, "developer", Worker{HostID: "shared-host", BrowserTabID: "tab-1", Slots: 1})
	if err != nil {
		t.Fatalf("first register: %v", err)
	}
	again, err := h.engine.Register(ctx, Thread, "developer", Worker{HostID: "shared-host", BrowserTabID: "tab-1", Slots: 2})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if again.TabID != first.TabID {
		t.Fatalf("reload minted a new tab %q, want the reused %q", again.TabID, first.TabID)
	}
	if got := countOwnerTabs(t, h, "developer"); got != 1 {
		t.Fatalf("worker records = %d, want 1", got)
	}
}

// TestR10PinlessLegacyRegisterKeepsTheOldRule pins the compatibility rule: a body
// without the browser tab id still matches by (owner, pool, HostID).
func TestR10PinlessLegacyRegisterKeepsTheOldRule(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	first, err := h.engine.Register(ctx, Thread, "developer", Worker{HostID: "shared-host", Slots: 1})
	if err != nil {
		t.Fatalf("first register: %v", err)
	}
	again, err := h.engine.Register(ctx, Thread, "developer", Worker{HostID: "shared-host", Slots: 2})
	if err != nil {
		t.Fatalf("legacy re-register: %v", err)
	}
	if again.TabID != first.TabID {
		t.Fatalf("legacy re-register minted a new tab %q, want the reused %q", again.TabID, first.TabID)
	}
}
