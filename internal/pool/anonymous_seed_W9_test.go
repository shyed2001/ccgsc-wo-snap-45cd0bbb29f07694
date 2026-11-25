// Path: internal/pool/anonymous_seed_W9_test.go | Author: Claude Opus 5.5 | 2026-11-21
// What/why: W9 F3 - an anonymous tab is seeded only when the tracker does not
// know it; a re-register keeps the old score, status and recovery count.
package pool

import (
	"ccgsc/poolscore/internal/verify"
	"context"
	"testing"
)

func TestW9F3ReRegisterKeepsReputation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const owner = "anon-w9f3"
	rep := h.engine.Verifier.Reputation()
	first, err := h.engine.Register(ctx, Thread, owner, Worker{HostID: "host", Slots: 1})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !first.Anonymous {
		t.Fatal("anon- owner must register an anonymous tab")
	}
	seeded, err := rep.Worker(first.TabID)
	if err != nil {
		t.Fatalf("seeded record: %v", err)
	}
	if want := anonymousSeedScore(rep.Config().BarBelow); seeded.Score != want {
		t.Fatalf("seeded score = %v, want %v", seeded.Score, want)
	}

	// Dig the tab into the bar band, grant one quiet recovery, then dig it back
	// down: the re-register must keep score, status and recovery count.
	rep.Seed(first.TabID, 0.4)
	if st := rep.Recover(first.TabID, anonymousRecoverQuiet, anonymousSeedScore(rep.Config().BarBelow)); st == verify.StatusBar {
		t.Fatal("quiet recovery did not lift the bar")
	}
	rep.Seed(first.TabID, 0.42)
	before, err := rep.Worker(first.TabID)
	if err != nil {
		t.Fatalf("before record: %v", err)
	}
	if before.Recoveries != 1 || before.Status != verify.StatusBar {
		t.Fatalf("before state = recoveries %d status %s, want 1 and bar", before.Recoveries, before.Status)
	}

	// Past 2*TTL the same host re-registers and reuses its tab id.
	h.now = h.now.Add(3 * h.engine.TTL)
	second, err := h.engine.Register(ctx, Thread, owner, Worker{HostID: "host", Slots: 1})
	if err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if second.TabID != first.TabID {
		t.Fatalf("re-register tab = %s, want the reused %s", second.TabID, first.TabID)
	}
	after, err := rep.Worker(first.TabID)
	if err != nil {
		t.Fatalf("after record: %v", err)
	}
	if after.Score != before.Score {
		t.Fatalf("score after re-register = %v, want %v kept", after.Score, before.Score)
	}
	if after.Status != before.Status {
		t.Fatalf("status after re-register = %s, want %s kept", after.Status, before.Status)
	}
	if after.Recoveries != before.Recoveries {
		t.Fatalf("recoveries after re-register = %d, want %d kept", after.Recoveries, before.Recoveries)
	}
}
