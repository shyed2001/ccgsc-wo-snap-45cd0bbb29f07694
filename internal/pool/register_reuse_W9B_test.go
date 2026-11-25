// Path: internal/pool/register_reuse_W9B_test.go | Author: Claude Opus 5.5 | 2026-11-21
// What/why: W9b row 2 (REVIEW_W9_gemini part 2) - a re-register of the same
// HostID for (owner, pool) reuses the tab (a reload/charger change must not
// mint a second TabID), and the anonymous live-tab limits are checked inside
// the registration transaction so two concurrent joins cannot both pass.
package pool

import (
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/sqlite"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestW9BRegisterReusesHostTabWithinTTL(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	first, err := h.engine.Register(ctx, Thread, "contributor", Worker{HostID: "host-a", Slots: 1})
	if err != nil {
		t.Fatalf("Register first: %v", err)
	}
	second, err := h.engine.Register(ctx, Thread, "contributor", Worker{HostID: "host-a", Slots: 2})
	if err != nil {
		t.Fatalf("re-register of a live host: %v", err)
	}
	if second.TabID != first.TabID {
		t.Fatalf("re-register minted tab %q, want the existing %q", second.TabID, first.TabID)
	}
	if second.Slots != 2 {
		t.Fatalf("reused tab slots = %d, want the new registration's 2", second.Slots)
	}
	if err := h.engine.Store.View(ctx, func(r store.Reader) error {
		rows, er := r.List(store.Workers)
		if er != nil {
			return er
		}
		n := 0
		for _, row := range rows {
			var w Worker
			if er = json.Unmarshal(row.Value, &w); er != nil {
				return er
			}
			if w.Owner == "contributor" && w.PoolID == Thread && w.HostID == "host-a" {
				n++
			}
		}
		if n != 1 {
			t.Errorf("worker rows for the host = %d, want 1", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	other, err := h.engine.Register(ctx, Thread, "contributor", Worker{HostID: "host-b", Slots: 1})
	if err != nil {
		t.Fatalf("Register a different host: %v", err)
	}
	if other.TabID == first.TabID {
		t.Fatal("a different host reused the tab")
	}
	// T114: the reload of the same host is the way out of a stale-runner mark.
	// W9c row 2: the mark is cleared only when the reload reports the current
	// task key id, so the reload carries the live pin.
	if err := h.engine.Store.Update(ctx, func(tx store.Tx) error {
		var w Worker
		if er := tx.Get(store.Workers, second.TabID, &w); er != nil {
			return er
		}
		w.RunnerStale = true
		return tx.Put(store.Workers, second.TabID, w)
	}); err != nil {
		t.Fatal(err)
	}
	reload, err := h.engine.Register(ctx, Thread, "contributor", Worker{HostID: "host-a", Slots: 2, RunnerPin: h.engine.TaskKeyID})
	if err != nil {
		t.Fatalf("Register after the stale mark: %v", err)
	}
	if reload.TabID != first.TabID || reload.RunnerStale {
		t.Fatalf("reload = tab %q stale %v, want the reused tab %q without the stale mark", reload.TabID, reload.RunnerStale, first.TabID)
	}
}

func TestW9BRegisterReloadAtAnonymousIPCap(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.engine.SetAnonymousLimits(AnonymousLimits{MaxLiveTabsPerIP: 1})
	const ip = "203.0.113.7"
	first, err := h.engine.Register(ctx, Thread, "anon-w9b", Worker{HostID: "host-a", Slots: 1, Anonymous: true, ClientIP: ip})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	reload, err := h.engine.Register(ctx, Thread, "anon-w9b", Worker{HostID: "host-a", Slots: 1, Anonymous: true, ClientIP: ip})
	if err != nil {
		t.Fatalf("reload at the IP cap: %v, want the existing tab reused (no new live tab)", err)
	}
	if reload.TabID != first.TabID {
		t.Fatalf("reload minted tab %q, want %q", reload.TabID, first.TabID)
	}
	if _, err := h.engine.Register(ctx, Thread, "anon-w9b", Worker{HostID: "host-b", Slots: 1, Anonymous: true, ClientIP: ip}); !errors.Is(err, ErrAnonymousQuota) {
		t.Fatalf("fresh host at the IP cap = %v, want ErrAnonymousQuota", err)
	}
}

// viewGateStore forces two concurrent registrations to finish their
// Store.View reads before either proceeds to Store.Update, which makes the
// read-check-write race of an out-of-transaction limit check deterministic.
type viewGateStore struct {
	store.Store
	mu    sync.Mutex
	armed bool
	total int
	seen  int
	ch    chan struct{}
}

func newViewGateStore(s store.Store) *viewGateStore {
	return &viewGateStore{Store: s, ch: make(chan struct{})}
}

func (g *viewGateStore) arm(total int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.armed = true
	g.total = total
	g.seen = 0
	g.ch = make(chan struct{})
}

func (g *viewGateStore) disarm() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.armed = false
}

func (g *viewGateStore) View(ctx context.Context, fn func(store.Reader) error) error {
	if err := g.Store.View(ctx, fn); err != nil {
		return err
	}
	g.mu.Lock()
	if !g.armed {
		g.mu.Unlock()
		return nil
	}
	g.seen++
	if g.seen == g.total {
		close(g.ch)
	}
	ch := g.ch
	g.mu.Unlock()
	<-ch
	return nil
}

func TestW9BRegisterAnonymousLimitsInsideTx(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	gate := newViewGateStore(db)
	engine, err := New(gate, slog.New(slog.NewJSONHandler(io.Discard, nil)), "s1", time.Minute)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	engine.SetAnonymousLimits(AnonymousLimits{MaxLiveTabsPerIP: 1})
	gate.arm(2)

	ctx := context.Background()
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, host := range []string{"host-a", "host-b"} {
		host := host
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := engine.Register(ctx, Thread, "anon-w9b", Worker{HostID: host, Slots: 1, Anonymous: true, ClientIP: "203.0.113.7"})
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	gate.disarm()

	ok, quota := 0, 0
	for e := range errs {
		switch {
		case e == nil:
			ok++
		case errors.Is(e, ErrAnonymousQuota):
			quota++
		default:
			t.Fatalf("Register: %v", e)
		}
	}
	if ok != 1 || quota != 1 {
		t.Fatalf("concurrent registrations = %d ok / %d quota, want 1 / 1 for a cap of 1 live tab per IP", ok, quota)
	}
	live, err := engine.CountLiveAnonymousTabs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("live anonymous tabs = %d, want 1", live)
	}
}
