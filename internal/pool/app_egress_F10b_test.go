package pool

import (
	"ccgsc/poolscore/internal/appegress"
	"ccgsc/poolscore/internal/store/pg"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestF10bConcurrentPGPhysicalTab(t *testing.T) {
	h := newPGEngineHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := pg.Open(h.dsn)
	pgEngineOK(t, "second open", err)
	defer db.Close()
	second, err := New(db, h.e.Log, "second", time.Minute)
	pgEngineOK(t, "second engine", err)
	second.Now = func() time.Time { return h.now }
	second.Verifier.SetClock(func() time.Time { return h.now })
	pgEngineOK(t, "thread approval", h.e.ApproveThreadApp(ctx, "developer", "AppAlpha", 2))
	pgEngineOK(t, "process approval", h.e.ApproveProcessApp(ctx, "developer", "AppBeta", 1))
	module := h.module(t)
	for _, x := range []struct{ p, app string }{{Thread, "AppAlpha"}, {Process, "AppBeta"}} {
		_, err = h.e.Create(ctx, x.p, "developer", "", CreateRequest{AppID: x.app, ModuleArtifactIDs: []string{module}, Tasks: []TaskSpec{{TaskID: "task", Input: json.RawMessage("{}")}}})
		pgEngineOK(t, "create", err)
	}
	a, err := h.e.Register(ctx, Thread, "contributor", Worker{HostID: "physical", BrowserTabID: "tab", Slots: 2})
	pgEngineOK(t, "thread register", err)
	b, err := second.Register(ctx, Process, "contributor", Worker{HostID: "physical", BrowserTabID: "tab", Slots: 1})
	pgEngineOK(t, "process register", err)
	start := make(chan struct{})
	result := make(chan error, 2)
	go func() { <-start; _, err := h.e.Claim(ctx, Thread, "contributor", a.TabID); result <- err }()
	go func() { <-start; _, err := second.Claim(ctx, Process, "contributor", b.TabID); result <- err }()
	close(start)
	x, y := <-result, <-result
	if !(x == nil && errors.Is(y, ErrEmpty) || y == nil && errors.Is(x, ErrEmpty)) {
		t.Fatalf("want exactly one claim and one empty: %v / %v", x, y)
	}
}
func TestF10bEgressConsentAndLease(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	e := h.engine
	e.SetAppEgressReady(true)
	policy := appegress.Policy{Endpoints: []string{"https://api.example.com", "wss://socket.example.com"}}
	if err := e.ApproveAppEgress(ctx, Thread, "developer", "AppAlpha", 1, &policy); err != nil {
		t.Fatal(err)
	}
	policy, err := e.AppEgressPolicy(ctx, Thread, "developer", "AppAlpha")
	if err != nil {
		t.Fatal(err)
	}
	f10ThreadJob(t, h, "AppAlpha", "job", 1)
	w, err := e.Register(ctx, Thread, "contributor", Worker{HostID: "host", BrowserTabID: "tab", Slots: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Claim(ctx, Thread, "contributor", w.TabID); !errors.Is(err, ErrEmpty) {
		t.Fatalf("unconsented claim: %v", err)
	}
	w, err = e.Register(ctx, Thread, "contributor", Worker{HostID: "host", BrowserTabID: "tab", Slots: 1, EgressConsent: true, EgressConsentIDs: []string{policy.ConsentID}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.Claim(ctx, Thread, "contributor", w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := e.IssueAppEgress(ctx, Thread, "contributor", w.TabID, a.Lease)
	if err != nil || grant == nil {
		t.Fatalf("grant: %v", err)
	}
	signer, err := e.EgressSigner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c, err := signer.Verify(grant.Token, "worker", h.now)
	if err != nil || c.Owner != "developer" || c.Audience != "AppAlpha" || c.Subject != w.TabID || c.Lease == a.Lease.Token || c.ExpiresAt > a.Lease.ExpiresAt.Unix() {
		t.Fatalf("bad lease claims: %+v %v", c, err)
	}
	if _, err = e.RunnerAppPolicy(ctx, grant.Token); err == nil {
		t.Fatal("worker token admitted to runner")
	}
	p, err := e.RunnerAppPolicy(ctx, grant.RunnerGrant)
	if err != nil || p.ConsentID != policy.ConsentID {
		t.Fatalf("runner grant: %+v %v", p, err)
	}
	bad := a.Lease
	bad.Generation++
	if _, err = e.IssueAppEgress(ctx, Thread, "contributor", w.TabID, bad); err == nil {
		t.Fatal("wrong generation admitted")
	}
	changed := appegress.Policy{Endpoints: []string{"https://other.example.com"}}
	if err = e.ApproveAppEgress(ctx, Thread, "developer", "AppAlpha", 1, &changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed live policy: %v", err)
	}
	h.now = a.Lease.ExpiresAt.Add(time.Second)
	if _, err = e.RunnerAppPolicy(ctx, grant.RunnerGrant); err == nil {
		t.Fatal("expired runner grant admitted")
	}
}
