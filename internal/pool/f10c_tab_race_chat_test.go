package pool

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"ccgsc/poolscore/internal/appegress"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
)

// (f10cSecondEngine lives in app_tab_F10c_pg_test.go; this file reuses it.)

func TestF10CPhysicalTabTwoEngineDifferentAppsPG(t *testing.T) {
	h := newPGEngineHarness(t)
	e2 := f10cSecondEngine(t, h)
	ctx := context.Background()
	const host = "f10c-same-host"
	const tab = "f10c-same-physical-tab"
	if err := h.e.ApproveThreadApp(ctx, "developer", "AppAlpha", 2); err != nil {
		t.Fatal(err)
	}
	if err := e2.ApproveProcessApp(ctx, "developer", "AppBeta", 1); err != nil {
		t.Fatal(err)
	}
	module := h.module(t)
	for _, item := range []struct {
		e           *Engine
		p, app, key string
	}{
		{h.e, Thread, "AppAlpha", "f10c-thread"}, {e2, Process, "AppBeta", "f10c-process"},
	} {
		_, err := item.e.Create(ctx, item.p, "developer", item.key, CreateRequest{
			AppID: item.app, ModuleArtifactIDs: []string{module},
			Tasks: []TaskSpec{{TaskID: "f10c-" + item.key, Input: json.RawMessage(`{"json":{"n":1}}`)}},
		})
		if err != nil {
			t.Fatalf("create %s: %v", item.p, err)
		}
	}
	tw, err := h.e.Register(ctx, Thread, "contributor", Worker{HostID: host, BrowserTabID: tab, Slots: 2})
	if err != nil {
		t.Fatal(err)
	}
	pw, err := e2.Register(ctx, Process, "contributor", Worker{HostID: host, BrowserTabID: tab, Slots: 1})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		pool string
		a    Assignment
		err  error
	}
	outcomes := make(chan result, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, c := range []struct {
		e    *Engine
		p, w string
	}{{h.e, Thread, tw.TabID}, {e2, Process, pw.TabID}} {
		wg.Add(1)
		go func(c struct {
			e    *Engine
			p, w string
		}) {
			defer wg.Done()
			<-start
			a, err := c.e.Claim(ctx, c.p, "contributor", c.w)
			outcomes <- result{c.p, a, err}
		}(c)
	}
	close(start)
	wg.Wait()
	close(outcomes)
	wins := 0
	var winner result
	for o := range outcomes {
		if o.err == nil {
			wins++
			winner = o
			continue
		}
		if !errors.Is(o.err, ErrEmpty) && !errors.Is(o.err, ErrConflict) {
			t.Fatalf("unexpected losing claim: %s %v", o.pool, o.err)
		}
	}
	if wins != 1 {
		t.Fatalf("physical tab was claimed %d times by conflicting apps, want 1", wins)
	}
	id, err := appegress.TabIdentity(host, tab)
	if err != nil {
		t.Fatal(err)
	}
	var fence appegress.TabFence
	err = h.e.Store.View(ctx, func(r store.Reader) error {
		return r.Get("app_physical_tabs", verify.SHA256([]byte(id)), &fence)
	})
	if err != nil {
		t.Fatal(err)
	}
	wantApp := "AppAlpha"
	if winner.pool == Process {
		wantApp = "AppBeta"
	}
	if !fence.Permits(winner.pool, "developer", wantApp) || fence.AppID != wantApp {
		t.Fatalf("fence=%+v, winner=%s/%s", fence, winner.pool, wantApp)
	}
	// A subsequent claim from the losing pool cannot rebind the same physical tab.
	if winner.pool == Thread {
		if _, err = e2.Claim(ctx, Process, "contributor", pw.TabID); !errors.Is(err, ErrEmpty) {
			t.Fatalf("loser rebound physical tab: %v", err)
		}
	} else {
		if _, err = h.e.Claim(ctx, Thread, "contributor", tw.TabID); !errors.Is(err, ErrEmpty) {
			t.Fatalf("loser rebound physical tab: %v", err)
		}
	}
}

// Test the actual PG fence primitive against two distinct same-pool apps.
// This exercises the two-instance unique-insert + row-lock path independent of
// scheduler job choice, which cannot be forced to target a particular app.
func TestF10CPhysicalTabFenceTwoAppSamePoolPG(t *testing.T) {
	h := newPGEngineHarness(t)
	e2 := f10cSecondEngine(t, h)
	ctx := context.Background()
	id, err := appegress.TabIdentity("f10c-fence-host", "f10c-fence-tab")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	start := make(chan struct{})
	for _, u := range []struct {
		e   *Engine
		app string
	}{{h.e, "AppAlpha"}, {e2, "AppBeta"}} {
		go func(u struct {
			e   *Engine
			app string
		}) {
			<-start
			done <- u.e.Store.Update(ctx, func(tx store.Tx) error {
				f, err := appegress.LockTabTx(tx, id)
				if err != nil {
					return err
				}
				if !f.Permits(Thread, "developer", u.app) {
					return ErrConflict
				}
				f.Bind(Thread, "developer", u.app, "")
				return appegress.SaveTabTx(tx, id, f)
			})
		}(u)
	}
	close(start)
	a, b := <-done, <-done
	wins := 0
	if a == nil {
		wins++
	} else if !errors.Is(a, ErrConflict) {
		t.Fatalf("first=%v", a)
	}
	if b == nil {
		wins++
	} else if !errors.Is(b, ErrConflict) {
		t.Fatalf("second=%v", b)
	}
	if wins != 1 {
		t.Fatalf("fence accepted %d conflicting app bindings, want 1", wins)
	}
	var fence appegress.TabFence
	if err = h.e.Store.View(ctx, func(r store.Reader) error {
		var er error
		fence, er = appegress.ReadTabTx(r, id)
		return er
	}); err != nil {
		t.Fatal(err)
	}
	if fence.AppID != "AppAlpha" && fence.AppID != "AppBeta" {
		t.Fatalf("unexpected final fence: %+v", fence)
	}
	if !fence.Permits(Thread, "developer", fence.AppID) {
		t.Fatalf("final fence does not permit its own app: %+v", fence)
	}
}
