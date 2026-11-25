// Path: internal/pool/anonymous_tabs_W9_test.go | Author: Claude Opus 5.5 | 2026-11-21
// What/why: W9 F4 - a stale-runner tab holds no live slot, so it must not count
// toward AnonymousMaxTabsPerSubject either.
package pool

import (
	"ccgsc/poolscore/internal/store"
	"context"
	"errors"
	"testing"
)

func TestW9F4StaleTabFreesSubjectSlot(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const owner = "anon-w9f4"
	first, err := h.engine.Register(ctx, Thread, owner, Worker{HostID: "host-a", Slots: 1})
	if err != nil {
		t.Fatalf("Register first: %v", err)
	}
	if _, err := h.engine.Register(ctx, Thread, owner, Worker{HostID: "host-b", Slots: 1}); err != nil {
		t.Fatalf("Register second: %v", err)
	}
	if _, err := h.engine.Register(ctx, Thread, owner, Worker{HostID: "host-c", Slots: 1}); !errors.Is(err, ErrEmpty) {
		t.Fatalf("third tab err = %v, want ErrEmpty at the subject cap", err)
	}

	// The first tab is a stale runner: it computes nothing and holds no slot.
	if err := h.engine.Store.Update(ctx, func(tx store.Tx) error {
		var w Worker
		if er := tx.Get(store.Workers, first.TabID, &w); er != nil {
			return er
		}
		w.RunnerStale = true
		return tx.Put(store.Workers, first.TabID, w)
	}); err != nil {
		t.Fatalf("mark stale: %v", err)
	}
	third, err := h.engine.Register(ctx, Thread, owner, Worker{HostID: "host-c", Slots: 1})
	if err != nil {
		t.Fatalf("register after the stale tab: %v", err)
	}
	if third.TabID == first.TabID {
		t.Fatal("the stale tab must not be reused as the fresh tab")
	}

	// The two live tabs still fill the subject cap.
	if _, err := h.engine.Register(ctx, Thread, owner, Worker{HostID: "host-d", Slots: 1}); !errors.Is(err, ErrEmpty) {
		t.Fatalf("fourth tab err = %v, want ErrEmpty with two live tabs", err)
	}
}
