package threadapp

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ccgsc/poolscore/internal/store/sqlite"
)

func newF10DB(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "thread_app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
func TestF10KQuotaAndSameTabIdempotence(t *testing.T) {
	db := newF10DB(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 24, 1, 0, 0, 0, time.UTC)
	if err := Approve(ctx, db, "developer-a", "AppAlpha", 1, now); err != nil {
		t.Fatal(err)
	}
	tab1, _ := SlotIdentity("host-01", "tab-01", 0)
	tab2, _ := SlotIdentity("host-01", "tab-02", 0)
	b, err := Bind(ctx, db, "developer-a", "AppAlpha", tab1, now, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := Bind(ctx, db, "developer-a", "AppAlpha", tab1, now.Add(time.Minute), 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if b2.Generation != b.Generation {
		t.Errorf("duplicate claim incremented generation: %d vs %d", b2.Generation, b.Generation)
	}
	if _, err := Bind(ctx, db, "developer-a", "AppAlpha", tab2, now, 15*time.Minute); !errors.Is(err, ErrFull) {
		t.Errorf("W=1 permitted second tab: %v", err)
	}
	p, err := Read(ctx, db, "developer-a", "AppAlpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Active) != 1 {
		t.Errorf("active tabs = %d, want 1", len(p.Active))
	}
}
func TestF10CrossAppTabExclusiveUntilReleaseOrExpiry(t *testing.T) {
	db := newF10DB(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 24, 1, 0, 0, 0, time.UTC)
	for _, app := range []string{"AppAlpha", "AppBeta"} {
		if err := Approve(ctx, db, "owner", app, 2, now); err != nil {
			t.Fatal(err)
		}
	}
	tab, _ := SlotIdentity("host", "browser-tab", 0)
	if _, err := Bind(ctx, db, "owner", "AppAlpha", tab, now, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := Bind(ctx, db, "owner", "AppBeta", tab, now, 5*time.Minute); !errors.Is(err, ErrBound) {
		t.Errorf("tab switched app while active: %v", err)
	}
	if err := Release(ctx, db, "owner", "AppAlpha", tab, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := Bind(ctx, db, "owner", "AppBeta", tab, now.Add(2*time.Minute), 5*time.Minute); err != nil {
		t.Fatalf("release did not free tab: %v", err)
	}
}
func TestF10ExpiredTabPermitsReplacement(t *testing.T) {
	db := newF10DB(t)
	ctx := context.Background()
	now := time.Now()
	if err := Approve(ctx, db, "owner", "AppAlpha", 1, now); err != nil {
		t.Fatal(err)
	}
	a, _ := SlotIdentity("h", "one", 0)
	b, _ := SlotIdentity("h", "two", 0)
	if _, err := Bind(ctx, db, "owner", "AppAlpha", a, now, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := Bind(ctx, db, "owner", "AppAlpha", b, now.Add(3*time.Second), 2*time.Second); err != nil {
		t.Errorf("expired tab did not release K: %v", err)
	}
}
func TestF10ConcurrentK1Atomic(t *testing.T) {
	db := newF10DB(t)
	ctx := context.Background()
	now := time.Now()
	if err := Approve(ctx, db, "owner", "AppAlpha", 1, now); err != nil {
		t.Fatal(err)
	}
	var okN, fullN atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, name := range []string{"tab-a", "tab-b"} {
		name := name
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tab, _ := SlotIdentity("host", name, 0)
			_, err := Bind(ctx, db, "owner", "AppAlpha", tab, now, 5*time.Minute)
			switch {
			case err == nil:
				okN.Add(1)
			case errors.Is(err, ErrFull):
				fullN.Add(1)
			default:
				t.Errorf("unexpected concurrent result: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if okN.Load() != 1 || fullN.Load() != 1 {
		t.Errorf("concurrent W=1 results ok=%d full=%d, want 1+1", okN.Load(), fullN.Load())
	}
}
func TestF10UnapprovedOrForgedOwnerRejected(t *testing.T) {
	db := newF10DB(t)
	ctx := context.Background()
	now := time.Now()
	if _, err := Bind(ctx, db, "owner", "AppAlpha", "host:tab", now, time.Minute); !errors.Is(err, ErrNotApproved) {
		t.Errorf("unapproved app allowed: %v", err)
	}
	if err := Approve(ctx, db, "owner", "AppAlpha", 1, now); err != nil {
		t.Fatal(err)
	}
	if _, err := Bind(ctx, db, "attacker", "AppAlpha", "host:tab", now, time.Minute); !errors.Is(err, ErrNotApproved) {
		t.Errorf("forged owner allowed: %v", err)
	}
	for _, name := range []string{"../escape", "", "other space", "$bad"} {
		if err := Approve(ctx, db, "owner", name, 1, now); !errors.Is(err, ErrInvalid) {
			t.Errorf("unsafe appId %q allowed: %v", name, err)
		}
	}
}

func TestF10TwoSlotsOnSameBrowserTabCountAsTwoWebWorkers(t *testing.T) {
	db := newF10DB(t)
	ctx := context.Background()
	now := time.Now()
	if err := Approve(ctx, db, "owner", "AppAlpha", 2, now); err != nil {
		t.Fatal(err)
	}
	slot0, _ := SlotIdentity("host-A", "tab-A", 0)
	slot1, _ := SlotIdentity("host-A", "tab-A", 1)
	slot2, _ := SlotIdentity("host-A", "tab-A", 2)
	if _, err := Bind(ctx, db, "owner", "AppAlpha", slot0, now, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := Bind(ctx, db, "owner", "AppAlpha", slot1, now, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := Bind(ctx, db, "owner", "AppAlpha", slot2, now, 2*time.Minute); !errors.Is(err, ErrFull) {
		t.Fatalf("W=2 allowed third WebWorker slot in same tab: %v", err)
	}
	p, err := Read(ctx, db, "owner", "AppAlpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Active) != 2 {
		t.Fatalf("expected 2 worker slots, got %d", len(p.Active))
	}
}
