// Path: internal/pool/contributor_counters_F15_test.go
// What/why: F15 per-contributor counters. Accepted tasks and compute milliseconds
// are durable pool_metrics samples. A signed-in account shares one counter across
// devices. An anonymous browser shares one counter per install id kept by the
// contribute page. The first valid result that settles a task takes the task
// credit; a later valid duplicate inside the dew window takes compute time only.
package pool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"ccgsc/poolscore/internal/verify"
)

func (h *harness) f15Job(t *testing.T, n int, options json.RawMessage) Job {
	t.Helper()
	zero := 0.0
	ctx := context.Background()
	data := []byte("module bytes")
	id := verify.SHA256(data)
	if _, err := h.engine.Upload(ctx, id, "", 0, int64(len(data)), data); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	q := CreateRequest{ModuleArtifactIDs: []string{id}, RerunPercent: &zero, Options: options}
	for i := 0; i < n; i++ {
		q.Tasks = append(q.Tasks, TaskSpec{TaskID: "task-" + string(rune('a'+i)), Input: json.RawMessage(`{"json":{"n":1}}`)})
	}
	j, err := h.engine.Create(ctx, Thread, "developer", "", q)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cfg := h.engine.TrustConfig()
	cfg.CanaryEnabled = false
	h.engine.SetTrustConfig(cfg)
	return j
}

func (h *harness) f15Settle(t *testing.T, w Worker, output string, compute time.Duration) Result {
	t.Helper()
	a, err := h.engine.Claim(context.Background(), Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatalf("Claim(%s): %v", w.Owner, err)
	}
	if compute > 0 {
		h.now = h.now.Add(compute)
	}
	got, err := h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, lateSubmission(a, output))
	if err != nil {
		t.Fatalf("Submit(%s): %v", w.Owner, err)
	}
	if got.Status != "done" {
		t.Fatalf("Submit(%s) status = %q, want done", w.Owner, got.Status)
	}
	return got
}

func (h *harness) f15Counters(t *testing.T, publicKey string) ContributorCounter {
	t.Helper()
	got, err := h.engine.ContributorCounters(context.Background(), publicKey)
	if err != nil {
		t.Fatalf("ContributorCounters(%q): %v", publicKey, err)
	}
	return got
}

func TestF15SettlementCreditsTaskAndCompute(t *testing.T) {
	h := newHarness(t)
	h.f15Job(t, 1, nil)
	alice := h.workerAt(t, "alice", "host-a", "")
	h.f15Settle(t, alice, `{"ok":1}`, 1500*time.Millisecond)

	got := h.f15Counters(t, ContributorAccountKey("alice"))
	if got.AcceptedTasks != 1 {
		t.Fatalf("accepted tasks = %v, want 1", got.AcceptedTasks)
	}
	if got.ComputeMS < 1500 || got.ComputeMS > 1501 {
		t.Fatalf("compute ms = %v, want 1500", got.ComputeMS)
	}
	// The device install id is not a second counter for a signed-in account.
	if install := h.f15Counters(t, ContributorInstallKey("host-a")); install.AcceptedTasks != 0 || install.ComputeMS != 0 {
		t.Fatalf("install counter = %+v, want empty for a signed-in account", install)
	}
}

func TestF15SignedInAccountSharesCountersAcrossDevices(t *testing.T) {
	h := newHarness(t)
	h.f15Job(t, 2, nil)
	phone := h.workerAt(t, "alice", "host-phone", "")
	laptop := h.workerAt(t, "alice", "host-laptop", "")
	h.f15Settle(t, phone, `{"ok":1}`, 1000*time.Millisecond)
	h.f15Settle(t, laptop, `{"ok":2}`, 2500*time.Millisecond)

	got := h.f15Counters(t, ContributorAccountKey("alice"))
	if got.AcceptedTasks != 2 {
		t.Fatalf("accepted tasks = %v, want 2 across devices", got.AcceptedTasks)
	}
	if got.ComputeMS < 3500 || got.ComputeMS > 3501 {
		t.Fatalf("compute ms = %v, want 3500 across devices", got.ComputeMS)
	}
	totals := h.f15Counters(t, "")
	if totals.AcceptedTasks != 2 || totals.ComputeMS != got.ComputeMS {
		t.Fatalf("totals = %+v, want the one account", totals)
	}
}

func TestF15AnonymousCountersFollowBrowserInstall(t *testing.T) {
	h := newHarness(t)
	h.f15Job(t, 3, nil)
	// Two anonymous sessions on one browser install share the install counter.
	tab1 := h.workerAt(t, "anon-one", "host-shared", "203.0.113.1")
	tab2 := h.workerAt(t, "anon-two", "host-shared", "203.0.113.2")
	other := h.workerAt(t, "anon-one", "host-other", "203.0.113.1")
	if !tab1.Anonymous || !tab2.Anonymous || !other.Anonymous {
		t.Fatalf("anonymous flags = %v %v %v, want all true", tab1.Anonymous, tab2.Anonymous, other.Anonymous)
	}
	h.f15Settle(t, tab1, `{"ok":1}`, 1000*time.Millisecond)
	h.f15Settle(t, tab2, `{"ok":2}`, 2000*time.Millisecond)
	h.f15Settle(t, other, `{"ok":3}`, 4000*time.Millisecond)

	shared := h.f15Counters(t, ContributorInstallKey("host-shared"))
	if shared.AcceptedTasks != 2 {
		t.Fatalf("shared install accepted = %v, want 2", shared.AcceptedTasks)
	}
	if shared.ComputeMS < 3000 || shared.ComputeMS > 3001 {
		t.Fatalf("shared install compute = %v, want 3000", shared.ComputeMS)
	}
	separate := h.f15Counters(t, ContributorInstallKey("host-other"))
	if separate.AcceptedTasks != 1 || separate.ComputeMS < 4000 || separate.ComputeMS > 4001 {
		t.Fatalf("other install = %+v, want 1 task and 4000 ms", separate)
	}
	// The anonymous subject is not the counter. Two sessions of one subject
	// stay split by the install id the contribute page kept.
	if bySubject := h.f15Counters(t, ContributorAccountKey("anon-one")); bySubject.AcceptedTasks != 0 {
		t.Fatalf("anonymous account key = %+v, want no account counter", bySubject)
	}
}

func TestF15DewDuplicateCreditsComputeOnly(t *testing.T) {
	h := newHarness(t)
	h.f15Job(t, 2, nil)
	alice := h.workerAt(t, "alice", "host-a", "")

	a, err := h.engine.Claim(context.Background(), Thread, alice.Owner, alice.TabID)
	if err != nil {
		t.Fatalf("Claim alice: %v", err)
	}
	if a.LeaseReceipt == nil {
		t.Fatal("claim carried no dew receipt")
	}
	// The original holder's duplicate is submitted late inside its receipt window.
	h.now = h.now.Add(time.Second)
	first := lateSubmission(a, `{"ok":true}`)
	if got, err := h.engine.Submit(context.Background(), Thread, alice.Owner, alice.TabID, first); err != nil || got.Status != "done" {
		t.Fatalf("first submit = %+v, %v, want done", got, err)
	}
	h.now = a.Lease.ExpiresAt.Add(time.Second)
	replay := lateSubmission(a, `{"ok":"late"}`)
	if got, err := h.engine.Submit(context.Background(), Thread, alice.Owner, alice.TabID, replay); err != nil || got.Status != "recorded" {
		t.Fatalf("duplicate submit = %+v, %v, want recorded", got, err)
	}

	aliceCount := h.f15Counters(t, ContributorAccountKey("alice"))
	if aliceCount.AcceptedTasks != 1 {
		t.Fatalf("settler accepted = %v, want 1", aliceCount.AcceptedTasks)
	}
	if aliceCount.ComputeMS <= 0 {
		t.Fatalf("duplicate compute = %v, want the lease compute time", aliceCount.ComputeMS)
	}
	totals := h.f15Counters(t, "")
	if totals.AcceptedTasks != 1 {
		t.Fatalf("totals accepted = %v, want 1", totals.AcceptedTasks)
	}
	if totals.ComputeMS != aliceCount.ComputeMS {
		t.Fatalf("totals compute = %v, want %v", totals.ComputeMS, aliceCount.ComputeMS)
	}

	// The same lease is not compute-credited twice.
	if _, err = h.engine.Submit(context.Background(), Thread, alice.Owner, alice.TabID, replay); !errors.Is(err, ErrConflict) {
		t.Fatalf("second duplicate = %v, want ErrConflict", err)
	}
	again := h.f15Counters(t, ContributorAccountKey("alice"))
	if again.ComputeMS != aliceCount.ComputeMS || again.AcceptedTasks != aliceCount.AcceptedTasks {
		t.Fatalf("replay counters = %+v, want unchanged %+v", again, aliceCount)
	}
}

func TestF15DewDuplicateOutsideWindowOrMismatchCreditsNothing(t *testing.T) {
	h := newHarness(t)
	h.f15Job(t, 1, nil)
	alice := h.workerAt(t, "alice", "host-a", "")

	a, err := h.engine.Claim(context.Background(), Thread, alice.Owner, alice.TabID)
	if err != nil {
		t.Fatalf("Claim alice: %v", err)
	}
	// G-F12-5: Bob can claim only after the first Dew receipt expires.
	h.now = time.Unix(a.LeaseReceipt.Deadline, 0).Add(time.Second)
	if err = h.engine.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	bob := h.workerAt(t, "bob", "host-b", "")
	h.f15Settle(t, bob, `{"ok":true}`, time.Second)
	bobCount := h.f15Counters(t, ContributorAccountKey("bob"))

	// A different payload is not a valid duplicate.
	if _, err = h.engine.Submit(context.Background(), Thread, alice.Owner, alice.TabID, lateSubmission(a, `{"no":true}`)); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched duplicate = %v, want ErrConflict", err)
	}
	if got := h.f15Counters(t, ContributorAccountKey("alice")); got.AcceptedTasks != 0 || got.ComputeMS != 0 {
		t.Fatalf("mismatch credit = %+v, want none", got)
	}

	// Past the receipt deadline the matching bytes are outside the dew window.
	h.now = time.Unix(a.LeaseReceipt.Deadline, 0).Add(time.Second)
	if _, err = h.engine.Submit(context.Background(), Thread, alice.Owner, alice.TabID, lateSubmission(a, `{"ok":true}`)); !errors.Is(err, ErrConflict) {
		t.Fatalf("late duplicate = %v, want ErrConflict", err)
	}
	if got := h.f15Counters(t, ContributorAccountKey("alice")); got.AcceptedTasks != 0 || got.ComputeMS != 0 {
		t.Fatalf("outside-window credit = %+v, want none", got)
	}
	if got := h.f15Counters(t, ""); got.AcceptedTasks != 1 || got.ComputeMS != bobCount.ComputeMS {
		t.Fatalf("totals = %+v, want only the settler %+v", got, bobCount)
	}
}

func TestF15DewOffDuplicateDoesNotCreditCompute(t *testing.T) {
	h := newHarness(t)
	h.f15Job(t, 1, json.RawMessage(`{"dew":{"enabled":false}}`))
	alice := h.workerAt(t, "alice", "host-a", "")
	bob := h.workerAt(t, "bob", "host-b", "")
	a, err := h.engine.Claim(context.Background(), Thread, alice.Owner, alice.TabID)
	if err != nil {
		t.Fatalf("Claim alice: %v", err)
	}
	h.now = a.Lease.ExpiresAt.Add(time.Second)
	if err = h.engine.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	h.f15Settle(t, bob, `{"ok":true}`, time.Second)
	if _, err = h.engine.Submit(context.Background(), Thread, alice.Owner, alice.TabID, lateSubmission(a, `{"ok":true}`)); !errors.Is(err, ErrConflict) {
		t.Fatalf("dew-off duplicate = %v, want ErrConflict", err)
	}
	if got := h.f15Counters(t, ContributorAccountKey("alice")); got.AcceptedTasks != 0 || got.ComputeMS != 0 {
		t.Fatalf("dew-off credit = %+v, want none", got)
	}
	if got := h.f15Counters(t, ContributorAccountKey("bob")); got.AcceptedTasks != 1 {
		t.Fatalf("settler accepted = %v, want 1", got.AcceptedTasks)
	}
}

func TestF15FailedResultDoesNotCredit(t *testing.T) {
	h := newHarness(t)
	h.f15Job(t, 1, nil)
	alice := h.workerAt(t, "alice", "host-a", "")
	a, err := h.engine.Claim(context.Background(), Thread, alice.Owner, alice.TabID)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	q := lateSubmission(a, `{"bad":true}`)
	q.Error = "worker failed"
	if _, err = h.engine.Submit(context.Background(), Thread, alice.Owner, alice.TabID, q); err != nil {
		t.Fatalf("failed submit: %v", err)
	}
	if got := h.f15Counters(t, ""); got.AcceptedTasks != 0 || got.ComputeMS != 0 {
		t.Fatalf("failed credit = %+v, want none", got)
	}
}

func TestF15ComputeCreditStopsAtLeaseExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	lease := &Lease{
		StartedAt: now.Add(-10 * time.Second),
		ExpiresAt: now.Add(-2 * time.Second),
	}
	got, ok := contributorComputeMS(now, lease)
	if !ok || got != 8000 {
		t.Fatalf("compute ms = %v, measured = %v; want 8000 ms", got, ok)
	}
}
