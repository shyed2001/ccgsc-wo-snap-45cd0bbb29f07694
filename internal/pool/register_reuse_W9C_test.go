// Path: internal/pool/register_reuse_W9C_test.go | Author: Claude Opus 5.5 | 2026-11-21
// What/why: W9c rows 2 and 3 (REVIEW_W9_deepseek, MAJOR) - a same-HostID
// re-register may clear the runner-stale mark only when it reports the current
// task key id, and a revived tab must pass the live anonymous caps (IP, global,
// subject) with itself excluded.
package pool

import (
	"ccgsc/poolscore/internal/store"
	"context"
	"errors"
	"testing"
	"time"
)

func w9cMarkStale(t *testing.T, h *harness, tabID string) {
	t.Helper()
	if err := h.engine.Store.Update(context.Background(), func(tx store.Tx) error {
		var w Worker
		if err := tx.Get(store.Workers, tabID, &w); err != nil {
			return err
		}
		w.RunnerStale = true
		return tx.Put(store.Workers, tabID, w)
	}); err != nil {
		t.Fatalf("mark stale: %v", err)
	}
}

// TestW9CReuseOldPinKeepsRunnerStale proves the row 2 defect: the old reuse path
// cleared RunnerStale unconditionally, so an unknown (or retired) pin was enough
// to lift the lease ban without ever running the current runner.
func TestW9CReuseOldPinKeepsRunnerStale(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	first, err := h.engine.Register(ctx, Thread, "contributor", Worker{HostID: "host-a", Slots: 1})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	w9cMarkStale(t, h, first.TabID)

	// A pin the server never issued: not the current key, so the mark stays.
	reload, err := h.engine.Register(ctx, Thread, "contributor", Worker{HostID: "host-a", Slots: 1, RunnerPin: "deadbeefdeadbeef"})
	if err != nil {
		t.Fatalf("re-register with an unknown pin: %v", err)
	}
	if reload.TabID != first.TabID {
		t.Fatalf("re-register minted tab %q, want the reused %q", reload.TabID, first.TabID)
	}
	if !reload.RunnerStale || !w9Worker(t, h, first.TabID).RunnerStale {
		t.Fatal("an unknown pin reload cleared the runner-stale mark")
	}

	// No pin at all is no proof either: the mark stays.
	reload, err = h.engine.Register(ctx, Thread, "contributor", Worker{HostID: "host-a", Slots: 1})
	if err != nil {
		t.Fatalf("re-register without a pin: %v", err)
	}
	if !reload.RunnerStale || !w9Worker(t, h, first.TabID).RunnerStale {
		t.Fatal("a pin-less reload cleared the runner-stale mark")
	}

	// Reporting the live key id is the proof the page reloaded.
	reload, err = h.engine.Register(ctx, Thread, "contributor", Worker{HostID: "host-a", Slots: 1, RunnerPin: h.engine.TaskKeyID})
	if err != nil {
		t.Fatalf("re-register with the current pin: %v", err)
	}
	if reload.RunnerStale || w9Worker(t, h, first.TabID).RunnerStale {
		t.Fatal("a reload reporting the current key id must clear the runner-stale mark")
	}
}

// TestW9CReuseReviveRunsAnonymousIPCap proves the per-IP half of row 3: reviving
// an expired same-host tab must not push the IP over its live tab cap.
func TestW9CReuseReviveRunsAnonymousIPCap(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.engine.SetAnonymousLimits(AnonymousLimits{MaxLiveTabsPerIP: 1})
	const ip = "203.0.113.9"
	const owner = "anon-w9c-ip"
	if _, err := h.engine.Register(ctx, Thread, owner, Worker{HostID: "host-a", Slots: 1, Anonymous: true, ClientIP: ip}); err != nil {
		t.Fatalf("Register first: %v", err)
	}
	h.now = h.now.Add(3 * time.Minute)
	if _, err := h.engine.Register(ctx, Thread, owner, Worker{HostID: "host-b", Slots: 1, Anonymous: true, ClientIP: ip}); err != nil {
		t.Fatalf("Register second after expiry: %v", err)
	}
	_, err := h.engine.Register(ctx, Thread, owner, Worker{HostID: "host-a", Slots: 1, Anonymous: true, ClientIP: ip, RunnerPin: h.engine.TaskKeyID})
	if !errors.Is(err, ErrAnonymousQuota) {
		t.Fatalf("revive at the IP cap err = %v, want ErrAnonymousQuota", err)
	}
}

// TestW9CReuseReviveRunsAnonymousGlobalCap proves the global half of row 3.
func TestW9CReuseReviveRunsAnonymousGlobalCap(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.engine.SetAnonymousLimits(AnonymousLimits{MaxTotalTabs: 1})
	const owner = "anon-w9c-global"
	if _, err := h.engine.Register(ctx, Thread, owner, Worker{HostID: "host-a", Slots: 1, Anonymous: true, ClientIP: "198.51.100.1"}); err != nil {
		t.Fatalf("Register first: %v", err)
	}
	h.now = h.now.Add(3 * time.Minute)
	if _, err := h.engine.Register(ctx, Thread, owner, Worker{HostID: "host-b", Slots: 1, Anonymous: true, ClientIP: "198.51.100.2"}); err != nil {
		t.Fatalf("Register second after expiry: %v", err)
	}
	_, err := h.engine.Register(ctx, Thread, owner, Worker{HostID: "host-a", Slots: 1, Anonymous: true, ClientIP: "198.51.100.1", RunnerPin: h.engine.TaskKeyID})
	if !errors.Is(err, ErrAnonymousQuota) {
		t.Fatalf("revive at the global cap err = %v, want ErrAnonymousQuota", err)
	}
}

// TestW9CReuseReviveRunsAnonymousSubjectCap proves the subject half of row 3: the
// revived tab itself is excluded from the count, but the remaining live tabs of
// the subject still cap it.
func TestW9CReuseReviveRunsAnonymousSubjectCap(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const owner = "anon-w9c-subject"
	if _, err := h.engine.Register(ctx, Thread, owner, Worker{HostID: "host-a", Slots: 1, Anonymous: true}); err != nil {
		t.Fatalf("Register host-a: %v", err)
	}
	if _, err := h.engine.Register(ctx, Thread, owner, Worker{HostID: "host-b", Slots: 1, Anonymous: true}); err != nil {
		t.Fatalf("Register host-b: %v", err)
	}
	h.now = h.now.Add(3 * time.Minute)
	if _, err := h.engine.Register(ctx, Thread, owner, Worker{HostID: "host-c", Slots: 1, Anonymous: true}); err != nil {
		t.Fatalf("Register host-c after expiry: %v", err)
	}
	if _, err := h.engine.Register(ctx, Thread, owner, Worker{HostID: "host-d", Slots: 1, Anonymous: true}); err != nil {
		t.Fatalf("Register host-d after expiry: %v", err)
	}
	_, err := h.engine.Register(ctx, Thread, owner, Worker{HostID: "host-a", Slots: 1, Anonymous: true, RunnerPin: h.engine.TaskKeyID})
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("revive at the subject cap err = %v, want ErrEmpty", err)
	}
}
