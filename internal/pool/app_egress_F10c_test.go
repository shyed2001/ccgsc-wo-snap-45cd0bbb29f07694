package pool

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"ccgsc/poolscore/internal/appegress"
	"ccgsc/poolscore/internal/store"
)

func f10cEgressSetup(t *testing.T) (*harness, context.Context, Worker, Assignment) {
	t.Helper()
	h := newHarness(t)
	ctx := context.Background()
	e := h.engine
	e.SetAppEgressReady(true)
	policy := appegress.Policy{Endpoints: []string{"https://api.example.com"}}
	if err := e.ApproveAppEgress(ctx, Thread, "developer", "AppAlpha", 1, &policy); err != nil {
		t.Fatal(err)
	}
	policy, err := e.AppEgressPolicy(ctx, Thread, "developer", "AppAlpha")
	if err != nil {
		t.Fatal(err)
	}
	f10ThreadJob(t, h, "AppAlpha", "job", 1)
	w, err := e.Register(ctx, Thread, "contributor", Worker{HostID: "host", BrowserTabID: "tab", Slots: 1, EgressConsent: true, EgressConsentIDs: []string{policy.ConsentID}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.Claim(ctx, Thread, "contributor", w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	return h, ctx, w, a
}

// F10b-11: issuance reads the durable fence itself, not only the worker-row
// mirror written at claim/heartbeat time.
func TestF10cGrantRequiresDurableTabFence(t *testing.T) {
	h, ctx, w, a := f10cEgressSetup(t)
	e := h.engine
	if _, err := e.IssueAppEgress(ctx, Thread, "contributor", w.TabID, a.Lease); err != nil {
		t.Fatalf("control grant: %v", err)
	}
	id, err := appegress.TabIdentity(w.HostID, w.BrowserTabID)
	if err != nil {
		t.Fatal(err)
	}
	var orig appegress.TabFence
	if err = e.Store.View(ctx, func(r store.Reader) error {
		var er error
		orig, er = appegress.ReadTabTx(r, id)
		return er
	}); err != nil {
		t.Fatal(err)
	}
	if orig.AppID != "AppAlpha" {
		t.Fatalf("control fence: %+v", orig)
	}
	rebound := orig
	rebound.AppID = "AppBeta"
	if err = e.Store.Update(ctx, func(tx store.Tx) error { return appegress.SaveTabTx(tx, id, rebound) }); err != nil {
		t.Fatal(err)
	}
	if _, err = e.IssueAppEgress(ctx, Thread, "contributor", w.TabID, a.Lease); !errors.Is(err, ErrForbidden) {
		t.Fatalf("grant issued for a rebound tab: %v", err)
	}
	if err = e.Store.Update(ctx, func(tx store.Tx) error { return appegress.SaveTabTx(tx, id, orig) }); err != nil {
		t.Fatal(err)
	}
	if _, err = e.IssueAppEgress(ctx, Thread, "contributor", w.TabID, a.Lease); err != nil {
		t.Fatalf("grant after fence restore: %v", err)
	}
}

// F10b-12: the worker credential is re-issued against the renewed lease, so a
// long-running task never keeps an expired bearer.
func TestF10cEgressTokenRenewedWithLease(t *testing.T) {
	h, ctx, w, a := f10cEgressSetup(t)
	e := h.engine
	first, err := e.IssueAppEgress(ctx, Thread, "contributor", w.TabID, a.Lease)
	if err != nil || first == nil {
		t.Fatalf("first grant: %v", err)
	}
	h.now = h.now.Add(30 * time.Second)
	if _, err = e.Heartbeat(ctx, Thread, "contributor", w.TabID, &a.Lease); err != nil {
		t.Fatalf("lease renewal: %v", err)
	}
	second, err := e.IssueAppEgress(ctx, Thread, "contributor", w.TabID, a.Lease)
	if err != nil || second == nil {
		t.Fatalf("renewed grant: %v", err)
	}
	if second.ExpiresAt < first.ExpiresAt || second.Token == first.Token {
		t.Fatalf("renewal did not refresh the credential: %d -> %d", first.ExpiresAt, second.ExpiresAt)
	}
}

// F10b-17: two engines racing different egress approvals must serialize. The
// version is the discriminator: if the read-modify-write were unlocked, both
// writers would read version 1 and the final policy would carry version 2.
func TestF10cPGConcurrentEgressPolicyWritesSerialize(t *testing.T) {
	h := newPGEngineHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	second := f10cSecondEngine(t, h)
	pgEngineOK(t, "initial approval", h.e.ApproveAppEgress(ctx, Thread, "developer", "AppAlpha", 2, &appegress.Policy{Endpoints: []string{"https://alpha.example.com"}}))
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = h.e.ApproveAppEgress(ctx, Thread, "developer", "AppAlpha", 2, &appegress.Policy{Endpoints: []string{"https://one.example.com"}})
	}()
	go func() {
		defer wg.Done()
		errs[1] = second.ApproveAppEgress(ctx, Thread, "developer", "AppAlpha", 2, &appegress.Policy{Endpoints: []string{"https://two.example.com"}})
	}()
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent approval %d: %v", i, err)
		}
	}
	p, err := h.e.AppEgressPolicy(ctx, Thread, "developer", "AppAlpha")
	pgEngineOK(t, "read policy", err)
	one := slices.Equal(p.Endpoints, []string{"https://one.example.com"})
	two := slices.Equal(p.Endpoints, []string{"https://two.example.com"})
	if p.Version != 3 || !(one || two) {
		t.Fatalf("lost or torn update: version %d endpoints %v", p.Version, p.Endpoints)
	}
}

// F10b-18: the same serialization for release policies; version 3 proves the
// second writer observed the first writer's committed row.
func TestF10cPGConcurrentReleasePolicyWritesSerialize(t *testing.T) {
	h := newPGEngineHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	second := f10cSecondEngine(t, h)
	if _, err := h.e.WriteTabRelease(ctx, "tp", appegress.ReleasePolicy{Mode: appegress.IdleTimeout, IdleSeconds: 30}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = h.e.WriteTabRelease(ctx, "tp", appegress.ReleasePolicy{Mode: appegress.IdleTimeout, IdleSeconds: 45})
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = second.WriteTabRelease(ctx, "tp", appegress.ReleasePolicy{Mode: appegress.Immediate, IdleSeconds: 30})
	}()
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent release write %d: %v", i, err)
		}
	}
	final, err := h.e.ReadTabRelease(ctx, "tp")
	if err != nil {
		t.Fatal(err)
	}
	if final.Version != 3 {
		t.Fatalf("release policy updates were lost: %+v", final)
	}
	if (final.Mode != appegress.IdleTimeout || final.IdleSeconds != 45) &&
		(final.Mode != appegress.Immediate || final.IdleSeconds != 30) {
		t.Fatalf("final release policy is torn: %+v", final)
	}
}
