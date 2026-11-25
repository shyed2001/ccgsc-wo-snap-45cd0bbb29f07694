package pool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
)

func f9ProcessJob(t *testing.T, h *harness, app string, taskPrefix string, n int) Job {
	t.Helper()
	ctx := context.Background()
	content := []byte("f9 test module bytes")
	id := verify.SHA256(content)
	if _, err := h.engine.Upload(ctx, id, "", 0, int64(len(content)), content); err != nil {
		t.Fatal(err)
	}
	q := CreateRequest{AppID: app, ModuleArtifactIDs: []string{id}, Tasks: []TaskSpec{}}
	for i := 0; i < n; i++ {
		q.Tasks = append(q.Tasks, TaskSpec{TaskID: taskPrefix + string(rune('a'+i)), Input: json.RawMessage(`{"json":{"n":1}}`)})
	}
	job, err := h.engine.Create(ctx, Process, "developer", taskPrefix, q)
	if err != nil {
		t.Fatalf("create Process app=%q: %v", app, err)
	}
	return job
}
func f9Worker(t *testing.T, h *harness, host, tab string) Worker {
	t.Helper()
	w, err := h.engine.Register(context.Background(), Process, "contributor", Worker{HostID: host, BrowserTabID: tab, Slots: 1})
	if err != nil {
		t.Fatal(err)
	}
	return w
}
func TestF9ProcessAppKTabQuotaBindingAndLegacy(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.engine.ApproveProcessApp(ctx, "developer", "AppAlpha", 1); err != nil {
		t.Fatal(err)
	}
	appJob := f9ProcessJob(t, h, "AppAlpha", "app-", 3)
	if appJob.AppID != "AppAlpha" {
		t.Fatalf("appId dropped during create: %+v", appJob.Request)
	}
	t.Logf("created app id %s", appJob.AppID)
	w1 := f9Worker(t, h, "host", "tab-1")
	w2 := f9Worker(t, h, "host", "tab-2")
	if _, err := h.engine.Claim(ctx, Process, "contributor", w1.TabID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Claim(ctx, Process, "contributor", w2.TabID); !errors.Is(err, ErrEmpty) {
		t.Errorf("quota K=1 admitted second tab, err=%v", err)
	}
	var stored Worker
	if err := h.engine.Store.View(ctx, func(r store.Reader) error { return r.Get(store.Workers, w1.TabID, &stored) }); err != nil {
		t.Fatal(err)
	}
	if stored.AppID != "AppAlpha" || stored.AppOwner != "developer" {
		t.Errorf("bound tab missing affinity: %+v", stored)
	}
	// Re-registering the same physical tab cannot mint a new quota identity.
	reload, err := h.engine.Register(ctx, Process, "contributor", Worker{HostID: "host", BrowserTabID: "tab-1", Slots: 1})
	if err != nil {
		t.Fatal(err)
	}
	if reload.TabID != w1.TabID || reload.AppID != "AppAlpha" {
		t.Errorf("re-register dropped affinity: %+v", reload)
	}
	// A legacy process job remains claimable by an unbound physical tab.
	f9ProcessJob(t, h, "", "legacy-", 1)
	if _, err := h.engine.Claim(ctx, Process, "contributor", w2.TabID); err != nil {
		t.Fatalf("legacy process job blocked by app pool: %v", err)
	}
}
func TestF9UnapprovedAppCreateFailsClosed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	data := []byte("module bytes")
	id := verify.SHA256(data)
	if _, err := h.engine.Upload(ctx, id, "", 0, int64(len(data)), data); err != nil {
		t.Fatal(err)
	}
	q := CreateRequest{AppID: "UnauthorizedApp", ModuleArtifactIDs: []string{id}, Tasks: []TaskSpec{{TaskID: "t", Input: json.RawMessage(`{"json":{"n":2}}`)}}}
	if _, err := h.engine.Create(ctx, Process, "developer", "", q); !errors.Is(err, ErrForbidden) {
		t.Errorf("unapproved app create err=%v", err)
	}
	if err := h.engine.ApproveProcessApp(ctx, "developer", "AppAlpha", 1); err != nil {
		t.Fatal(err)
	}
	q.AppID = "AppAlpha"
	if _, err := h.engine.Create(ctx, Process, "wrongDeveloper", "", q); !errors.Is(err, ErrForbidden) {
		t.Errorf("cross-owner app spoofing err=%v", err)
	}
	// F10 introduces a *separate* Thread app registry. An approval in the
	// Process registry does not authorize the same app ID in Thread.
	if _, err := h.engine.Create(ctx, Thread, "developer", "", q); !errors.Is(err, ErrForbidden) {
		t.Errorf("process-only approval also authorized Thread app: %v", err)
	}
}
func TestF9ProcessAppReleaseCannotDropActiveLease(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.engine.ApproveProcessApp(ctx, "developer", "AppAlpha", 1); err != nil {
		t.Fatal(err)
	}
	f9ProcessJob(t, h, "AppAlpha", "app-", 2)
	w := f9Worker(t, h, "host", "tab")
	if _, err := h.engine.Claim(ctx, Process, "contributor", w.TabID); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.ReleaseProcessApp(ctx, Process, "contributor", w.TabID); !errors.Is(err, ErrConflict) {
		t.Errorf("active lease released: %v", err)
	}
	// G-F12-3/5: the app stays bound until the Dew receipt expires, not at the
	// 1 minute heartbeat lease. 11 minutes is past the 10 minute default.
	h.now = h.now.Add(11 * time.Minute)
	if err := h.engine.ReleaseProcessApp(ctx, Process, "contributor", w.TabID); err != nil {
		t.Fatalf("after expiry release failed: %v", err)
	}
}

func TestF9ContributorCannotSetServerOwnedAppAffinity(t *testing.T) {
	h := newHarness(t)
	now := h.now.Add(8 * time.Hour)
	w, err := h.engine.Register(context.Background(), Process, "contributor", Worker{HostID: "h", BrowserTabID: "t", Slots: 1, AppID: "AppAlpha", AppOwner: "developer", AppExpiresAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if w.AppID != "" || w.AppOwner != "" || !w.AppExpiresAt.IsZero() {
		t.Fatalf("user-controlled app affinity persisted: %+v", w)
	}
}
