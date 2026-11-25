// Path: internal/api/stale_tab_cap_W7_test.go | Author: cmdc for Claude Opus 5.5 | 2026-11-21
// What/why: W7 fix of the cmdc MEDIUM row - a stale token at the live-tab cap.
//
// A tab that slept past exp+grace is not renewable, so the join route falls into the
// fresh-identity path, which is cap-checked. Its own old registration can be the record
// that fills the cap, and then the tab is locked out for the whole engine TTL although
// the bearer proves which registration it is. The fix releases only the dead records
// of the proven subject before the cap is checked.
package api

import (
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/pool"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// TestW7StaleTokenAtCapReplacesOwnDeadTab is the cmdc MEDIUM row.
func TestW7StaleTokenAtCapReplacesOwnDeadTab(t *testing.T) {
	f := newW7Anonymous(t)
	// One live tab per client IP: the tightest cap the bug locked a tab out of.
	f.engine.SetAnonymousLimits(pool.AnonymousLimits{MaxLiveTabsPerIP: 1, MaxTotalTabs: 100})
	const remote = "198.51.100.120:4242"

	first := f.issue(t, remote)
	sub := w7Claims(t, first.AccessToken).Subject
	var worker pool.Worker
	if err := json.Unmarshal(f.call(t, http.MethodPost, "/thread-pool/v1/workers/register", first.AccessToken, map[string]any{"hostId": "slept", "slots": 1}, http.StatusCreated, remote), &worker); err != nil {
		t.Fatal(err)
	}
	if !worker.Anonymous || worker.Owner != sub {
		t.Fatalf("bad anonymous worker: %+v", worker)
	}

	// The tab sleeps past exp+grace. Both clocks move, because the auth side decides
	// that the bearer is stale while the engine side decides that the tab is.
	clock := func() time.Time { return time.Now().Add(1800 * time.Second) }
	f.auth.Now = clock
	f.engine.Now = clock

	// The old behaviour answered 429 here: the fresh-identity path counted the dead
	// registration and the cap refused the very tab that owns it.
	next := f.issueWithBearer(t, first.AccessToken, remote)
	nextClaims := w7Claims(t, next.AccessToken)
	if nextClaims.Subject == sub {
		t.Fatal("a token past its renewal window must not renew the same subject")
	}
	// The replacement identity must take the slot its predecessor held.
	var replacement pool.Worker
	if err := json.Unmarshal(f.call(t, http.MethodPost, "/thread-pool/v1/workers/register", next.AccessToken, map[string]any{"hostId": "slept", "slots": 1}, http.StatusCreated, remote), &replacement); err != nil {
		t.Fatal(err)
	}
	if replacement.TabID == "" || replacement.Owner != nextClaims.Subject {
		t.Fatalf("replacement registration was not honoured: %+v", replacement)
	}
	if live, err := f.engine.CountLiveAnonymousTabs(context.Background()); err != nil || live != 1 {
		t.Fatalf("live anonymous tabs = %d (err %v), want exactly the replacement", live, err)
	}
}

// TestW7StaleTokenEvictionNeverTouchesLiveTabs bounds the fix: only dead records of
// the proven subject are released, so a bearer cannot displace a working tab.
//
// The dangerous shape is a subject with a live tab and an old bearer of its own: the
// tab is kept alive by a newer token of the same subject, while a copied old one is
// past its renewal window. Presenting it must not release the working tab.
func TestW7StaleTokenEvictionNeverTouchesLiveTabs(t *testing.T) {
	f := newW7Anonymous(t)
	const remote = "198.51.100.121:4242"

	old := f.issue(t, remote)
	sub := w7Claims(t, old.AccessToken).Subject
	var worker pool.Worker
	if err := json.Unmarshal(f.call(t, http.MethodPost, "/thread-pool/v1/workers/register", old.AccessToken, map[string]any{"hostId": "awake", "slots": 1}, http.StatusCreated, remote), &worker); err != nil {
		t.Fatal(err)
	}

	// Later, the same subject holds a fresh token and the tab heartbeats with it.
	clock := func() time.Time { return time.Now().Add(1800 * time.Second) }
	f.auth.Now = clock
	f.engine.Now = clock
	fresh, err := f.auth.IssueAnonymous(sub, auth.ContributorWorkerScopes)
	if err != nil {
		t.Fatal(err)
	}
	f.call(t, http.MethodPost, "/thread-pool/v1/workers/"+worker.TabID+"/heartbeat", fresh.AccessToken, map[string]any{}, http.StatusOK, remote)

	// The old bearer is stale, the tab is live: nothing may be released.
	if _, err = f.auth.AnonymousStaleClaims(old.AccessToken); err != nil {
		t.Fatalf("the kept bearer is not stale: %v", err)
	}
	evicted, err := f.engine.EvictStaleAnonymousTabs(context.Background(), sub)
	if err != nil {
		t.Fatal(err)
	}
	if evicted != 0 {
		t.Fatalf("evicted %d live tabs of the subject, want 0", evicted)
	}
	if live, err := f.engine.CountLiveAnonymousTabs(context.Background()); err != nil || live != 1 {
		t.Fatalf("live tabs = %d (err %v), want the untouched tab", live, err)
	}

	// Once the same tab is dead as well, the next stale bearer may release it.
	// The engine TTL is one minute, so more than two TTLs of silence is dead.
	f.engine.Now = func() time.Time { return time.Now().Add(1800*time.Second + 3*f.engine.TTL) }
	if evicted, err = f.engine.EvictStaleAnonymousTabs(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	if evicted != 1 {
		t.Fatalf("evicted %d dead tabs, want 1", evicted)
	}
}
