package pool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"ccgsc/poolscore/internal/appegress"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/pg"
)

// F10c: real two-engine PostgreSQL claims on one physical tab. Each engine owns
// its own pgx pool, so the two transactions run on distinct connections and the
// durable tab fence (one row, read FOR UPDATE inside the write transaction,
// internal/store/pg/pg.go getQuery) is the only thing that can serialize them.
func f10cSecondEngine(t *testing.T, h *pgEngineHarness) *Engine {
	t.Helper()
	db, err := pg.Open(h.dsn)
	pgEngineOK(t, "second open", err)
	t.Cleanup(func() { _ = db.Close() })
	e, err := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)), "second", time.Minute)
	pgEngineOK(t, "second engine", err)
	e.Now = func() time.Time { return h.now }
	e.Verifier.SetClock(func() time.Time { return h.now })
	e.SetAppEgressReady(true)
	return e
}

func f10cAppJob(t *testing.T, e *Engine, module, pool, owner, app string) {
	t.Helper()
	_, err := e.Create(context.Background(), pool, owner, "", CreateRequest{AppID: app, ModuleArtifactIDs: []string{module}, Tasks: []TaskSpec{
		{TaskID: "one", Input: json.RawMessage("{}")}, {TaskID: "two", Input: json.RawMessage("{}")}}})
	pgEngineOK(t, "create "+app, err)
}

// f10cApprove approves an app with an egress policy so that a worker can be
// steered to exactly one app by its consent list.
func f10cApprove(t *testing.T, e *Engine, pool, owner, app string, quota int, host string) string {
	t.Helper()
	ctx := context.Background()
	pgEngineOK(t, "approve "+app, e.ApproveAppEgress(ctx, pool, owner, app, quota, &appegress.Policy{Endpoints: []string{"https://" + host}}))
	p, err := e.AppEgressPolicy(ctx, pool, owner, app)
	pgEngineOK(t, "policy "+app, err)
	return p.ConsentID
}

func f10cActive(t *testing.T, e *Engine, pool, owner, app string, now time.Time) int {
	t.Helper()
	ctx := context.Background()
	n := 0
	if pool == Thread {
		p, err := e.GetThreadApp(ctx, owner, app)
		pgEngineOK(t, "thread registry", err)
		for _, s := range p.Active {
			if s.ExpiresAt.After(now) {
				n++
			}
		}
		return n
	}
	p, err := e.GetProcessApp(ctx, owner, app)
	pgEngineOK(t, "process registry", err)
	for _, s := range p.Active {
		if s.ExpiresAt.After(now) {
			n++
		}
	}
	return n
}

type f10cOutcome struct {
	app string
	err error
}

// Two engines, same (host, tab) in the SAME pool, two different approved apps.
// The two contributors are distinct owners so each has its own worker row;
// consent steers each worker to exactly one app and forces the conflict.
func TestF10cPGSamePoolSameTabDifferentApps(t *testing.T) {
	h := newPGEngineHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	second := f10cSecondEngine(t, h)
	h.e.SetAppEgressReady(true)
	module := h.module(t)
	alpha := f10cApprove(t, h.e, Thread, "dev-alpha", "AppAlpha", 2, "alpha.example.com")
	beta := f10cApprove(t, h.e, Thread, "dev-beta", "AppBeta", 2, "beta.example.com")
	f10cAppJob(t, h.e, module, Thread, "dev-alpha", "AppAlpha")
	f10cAppJob(t, h.e, module, Thread, "dev-beta", "AppBeta")
	for round := 0; round < 6; round++ {
		host, tab := "host-"+fmt.Sprint(round), "tab"
		a, err := h.e.Register(ctx, Thread, "contributor-a", Worker{HostID: host, BrowserTabID: tab, Slots: 2, EgressConsent: true, EgressConsentIDs: []string{alpha}})
		pgEngineOK(t, "register a", err)
		b, err := second.Register(ctx, Thread, "contributor-b", Worker{HostID: host, BrowserTabID: tab, Slots: 2, EgressConsent: true, EgressConsentIDs: []string{beta}})
		pgEngineOK(t, "register b", err)
		// A heartbeat commits the (empty) fence row first, so the race below
		// is on an EXISTING row and only the row lock can serialize it.
		_, err = h.e.Heartbeat(ctx, Thread, "contributor-a", a.TabID, nil)
		pgEngineOK(t, "heartbeat a", err)
		start := make(chan struct{})
		var wg sync.WaitGroup
		out := make([]f10cOutcome, 2)
		run := func(i int, e *Engine, owner, id, app string) {
			defer wg.Done()
			<-start
			_, out[i].err = e.Claim(ctx, Thread, owner, id)
			out[i].app = app
		}
		wg.Add(2)
		go run(0, h.e, "contributor-a", a.TabID, "AppAlpha")
		go run(1, second, "contributor-b", b.TabID, "AppBeta")
		close(start)
		wg.Wait()
		wins, empties := 0, 0
		winner := ""
		for _, o := range out {
			switch {
			case o.err == nil:
				wins++
				winner = o.app
			case errors.Is(o.err, ErrEmpty):
				empties++
			default:
				t.Fatalf("round %d: unexpected claim error %v", round, o.err)
			}
		}
		if wins != 1 || empties != 1 {
			t.Fatalf("round %d: want one winner and one empty, got %+v", round, out)
		}
		f := f10cFenceDSN(t, h.e, host, tab)
		if f.AppID != winner || (winner == "AppAlpha" && f.Owner != "dev-alpha") || (winner == "AppBeta" && f.Owner != "dev-beta") || f.Pool != Thread {
			t.Fatalf("round %d: fence %+v does not match winner %s", round, f, winner)
		}
		if len(f.ThreadSlots) != 1 {
			t.Fatalf("round %d: fence slots %v", round, f.ThreadSlots)
		}
		loser := map[string][2]string{"AppAlpha": {"dev-beta", "AppBeta"}, "AppBeta": {"dev-alpha", "AppAlpha"}}[winner]
		if n := f10cActive(t, h.e, Thread, loser[0], loser[1], h.now); n != 0 {
			t.Fatalf("round %d: loser %s holds %d W slots on the shared tab", round, loser[1], n)
		}
		// Later rounds reuse the apps; let this round's leases and bindings lapse.
		h.now = h.now.Add(10 * time.Minute)
	}
}

// f10cFenceDSN reads the durable fence through the engine's PG store.
func f10cFenceDSN(t *testing.T, e *Engine, host, tab string) appegress.TabFence {
	t.Helper()
	id, err := appegress.TabIdentity(host, tab)
	pgEngineOK(t, "identity", err)
	var f appegress.TabFence
	pgEngineOK(t, "read fence", e.Store.View(context.Background(), func(r store.Reader) error {
		var er error
		f, er = appegress.ReadTabTx(r, id)
		return er
	}))
	return f
}

// Thread versus Process on one physical tab, two engines, same owner: only one
// durable app fence and no conflicting W/K quota binding.
func TestF10cPGCrossPoolSameTabOneFenceNoConflictingQuota(t *testing.T) {
	h := newPGEngineHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	second := f10cSecondEngine(t, h)
	h.e.SetAppEgressReady(true)
	module := h.module(t)
	pgEngineOK(t, "thread approval", h.e.ApproveThreadApp(ctx, "dev-alpha", "AppAlpha", 2))
	pgEngineOK(t, "process approval", h.e.ApproveProcessApp(ctx, "dev-beta", "AppBeta", 1))
	f10cAppJob(t, h.e, module, Thread, "dev-alpha", "AppAlpha")
	f10cAppJob(t, h.e, module, Process, "dev-beta", "AppBeta")
	for round := 0; round < 8; round++ {
		host, tab := "physical", fmt.Sprintf("tab-%d", round)
		a, err := h.e.Register(ctx, Thread, "contributor", Worker{HostID: host, BrowserTabID: tab, Slots: 2})
		pgEngineOK(t, "thread register", err)
		b, err := second.Register(ctx, Process, "contributor", Worker{HostID: host, BrowserTabID: tab, Slots: 1})
		pgEngineOK(t, "process register", err)
		_, err = h.e.Heartbeat(ctx, Thread, "contributor", a.TabID, nil)
		pgEngineOK(t, "heartbeat", err)
		start := make(chan struct{})
		var wg sync.WaitGroup
		out := make([]f10cOutcome, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, out[0].err = h.e.Claim(ctx, Thread, "contributor", a.TabID)
			out[0].app = "AppAlpha"
		}()
		go func() {
			defer wg.Done()
			<-start
			_, out[1].err = second.Claim(ctx, Process, "contributor", b.TabID)
			out[1].app = "AppBeta"
		}()
		close(start)
		wg.Wait()
		wins, winner := 0, ""
		for _, o := range out {
			if o.err == nil {
				wins++
				winner = o.app
			} else if !errors.Is(o.err, ErrEmpty) {
				t.Fatalf("round %d: unexpected error %v", round, o.err)
			}
		}
		if wins != 1 {
			t.Fatalf("round %d: want exactly one claim, got %+v", round, out)
		}
		f := f10cFenceDSN(t, h.e, host, tab)
		wantPool, wantOwner := Thread, "dev-alpha"
		if winner == "AppBeta" {
			wantPool, wantOwner = Process, "dev-beta"
		}
		if f.AppID != winner || f.Pool != wantPool || f.Owner != wantOwner {
			t.Fatalf("round %d: fence %+v, winner %s", round, f, winner)
		}
		// Reset the winner's quota between rounds by releasing the tab.
		h.now = h.now.Add(10 * time.Minute)
		if _, err = h.e.Heartbeat(ctx, Thread, "contributor", a.TabID, nil); err != nil {
			t.Fatal(err)
		}
		if _, err = second.Heartbeat(ctx, Process, "contributor", b.TabID, nil); err != nil {
			t.Fatal(err)
		}
	}
}
