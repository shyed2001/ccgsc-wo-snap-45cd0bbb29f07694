// Path: internal/api/runner_pin_r10_test.go | R10 fix lane, owner row D-W10-9.
// What/why: the Go half of the runner self-report row. The row asks this lane to
// confirm that the register and heartbeat handlers read runnerPin into
// Worker.RunnerPin and that the W9C/W9D refund compares it, and to add an
// API-level test that a register carrying an old pin takes the refund path.
//
// Carriers: internal/worker/worker.go (register body -> pool.Worker.RunnerPin,
// heartbeat body -> Engine.HeartbeatWithPin). Comparison: Engine.Submit, which
// reads runnerPinKeyID(w.RunnerPin) and requires both taskKeyRetired(pinKeyID)
// and runnerPinPageOK(w.RunnerPin) before it refunds the task attempt.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"regexp"
	"testing"
	"time"

	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/verify"
	"ccgsc/poolscore/web"
)

// r10RunnerPin builds the canonical pin of the served runner page and a task key.
// D-W10-9: the page sha is the page's own pin (its data-runner-pin attribute, or
// the exact inline script hash), never the hash of the whole file.
func r10RunnerPin(t *testing.T, keyID string) string {
	t.Helper()
	page, err := fs.ReadFile(web.Pages, "pages/runner.html")
	if err != nil {
		t.Fatalf("embedded runner page: %v", err)
	}
	body := regexp.MustCompile(`(?s)<script type="module">(.*?)</script>`).FindStringSubmatch(string(page))
	if body == nil {
		t.Fatal("runner.html carries no inline runner script")
	}
	return verify.SHA256([]byte(body[1])) + ":" + keyID
}

// TestR10RunnerPinWireContract is the row's register/heartbeat confirmation.
func TestR10RunnerPinWireContract(t *testing.T) {
	f := newW7Anonymous(t)
	const remote = "198.51.100.130:4242"
	tok := f.issue(t, remote)
	pin := r10RunnerPin(t, f.engine.TaskKeyID)

	var registered pool.Worker
	if err := json.Unmarshal(f.call(t, http.MethodPost, "/thread-pool/v1/workers/register", tok.AccessToken,
		map[string]any{"hostId": "pinned", "slots": 1, "runnerPin": pin}, http.StatusCreated, remote), &registered); err != nil {
		t.Fatal(err)
	}
	if registered.RunnerPin != pin {
		t.Fatalf("registered pin = %q, want %q", registered.RunnerPin, pin)
	}
	var stored pool.Worker
	if err := f.db.View(context.Background(), func(r store.Reader) error { return r.Get(store.Workers, registered.TabID, &stored) }); err != nil {
		t.Fatal(err)
	}
	if stored.RunnerPin != pin {
		t.Fatalf("stored pin = %q, want %q", stored.RunnerPin, pin)
	}

	// A pin-less heartbeat keeps the stored evidence.
	var beat pool.Worker
	if err := json.Unmarshal(f.call(t, http.MethodPost, "/thread-pool/v1/workers/"+registered.TabID+"/heartbeat", tok.AccessToken,
		map[string]any{}, http.StatusOK, remote), &beat); err != nil {
		t.Fatal(err)
	}
	if beat.RunnerPin != pin {
		t.Fatalf("pin after a pin-less heartbeat = %q, want %q", beat.RunnerPin, pin)
	}
	// A pinned heartbeat replaces it, which is how a reload re-reports the page.
	other := r10RunnerPin(t, "deadbeefdeadbeef")
	if err := json.Unmarshal(f.call(t, http.MethodPost, "/thread-pool/v1/workers/"+registered.TabID+"/heartbeat", tok.AccessToken,
		map[string]any{"runnerPin": other}, http.StatusOK, remote), &beat); err != nil {
		t.Fatal(err)
	}
	if beat.RunnerPin != other {
		t.Fatalf("pin after a pinned heartbeat = %q, want %q", beat.RunnerPin, other)
	}
	// A malformed pin is refused rather than stored.
	f.call(t, http.MethodPost, "/thread-pool/v1/workers/"+registered.TabID+"/heartbeat", tok.AccessToken,
		map[string]any{"runnerPin": "not-a-pin"}, http.StatusBadRequest, remote)
}

// r10Module uploads one module artifact for the developer bearer and returns its
// id. The T100 owner reference is keyed by the key id of the caller.
func r10Module(t *testing.T, f *w7Anonymous, bearer string) string {
	t.Helper()
	body := []byte("module fixture for the runner-pin row")
	id := verify.SHA256(body)
	owner := w7Claims(t, bearer).KeyID
	if _, err := f.engine.UploadOwned(context.Background(), id, "application/javascript", 0, int64(len(body)), body, owner, false); err != nil {
		t.Fatalf("module upload: %v", err)
	}
	return id
}

// TestR10RunnerPinOldPinTakesTheRefundPath is the API-level refund test the row
// asks for: a tab whose pin carries the task key id the server retired on a real
// admin rotation reports unknown_operator_key, and the submit refunds the attempt.
func TestR10RunnerPinOldPinTakesTheRefundPath(t *testing.T) {
	f := newW7Anonymous(t)
	const remote = "198.51.100.131:4242"
	ctx := context.Background()
	oldKeyID := f.engine.TaskKeyID
	if err := f.engine.RotateTaskKey(ctx, time.Minute); err != nil {
		t.Fatalf("RotateTaskKey: %v", err)
	}
	pin := r10RunnerPin(t, oldKeyID)

	_, devBearer := f.credentials(t, "developer")
	var job pool.Job
	if err := json.Unmarshal(f.call(t, http.MethodPost, "/thread-pool/v1/jobs", devBearer,
		pool.CreateRequest{ModuleArtifactIDs: []string{r10Module(t, f, devBearer)},
			Tasks: []pool.TaskSpec{{TaskID: "pin-refund", Input: json.RawMessage(`{"n":1}`)}}},
		http.StatusCreated, ""), &job); err != nil {
		t.Fatal(err)
	}

	_, contribBearer := f.credentials(t, "contributor")
	var tab pool.Worker
	if err := json.Unmarshal(f.call(t, http.MethodPost, "/thread-pool/v1/workers/register", contribBearer,
		map[string]any{"hostId": "old-pin", "slots": 1, "runnerPin": pin}, http.StatusCreated, remote), &tab); err != nil {
		t.Fatal(err)
	}
	if tab.RunnerPin != pin {
		t.Fatalf("registered pin = %q, want the old %q", tab.RunnerPin, pin)
	}
	var a pool.Assignment
	if err := json.Unmarshal(f.call(t, http.MethodPost, "/thread-pool/v1/workers/"+tab.TabID+"/leases", contribBearer,
		nil, http.StatusOK, remote), &a); err != nil {
		t.Fatal(err)
	}
	if a.Lease.JobID != job.JobID {
		t.Fatalf("leased job %s, want %s", a.Lease.JobID, job.JobID)
	}

	var res pool.Result
	if err := json.Unmarshal(f.call(t, http.MethodPost, "/thread-pool/v1/workers/"+tab.TabID+"/results", contribBearer,
		pool.Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Error: "verify failed: unknown_operator_key"},
		http.StatusOK, remote), &res); err != nil {
		t.Fatal(err)
	}
	if res.Status != "requeued" || res.Attempts != 0 {
		t.Fatalf("result = %+v, want requeued with the attempt refunded", res)
	}
	var stored pool.Worker
	if err := f.db.View(ctx, func(r store.Reader) error { return r.Get(store.Workers, tab.TabID, &stored) }); err != nil {
		t.Fatal(err)
	}
	if !stored.RunnerStale {
		t.Fatal("the refunded tab was not marked stale")
	}
	// A stale tab gets no further lease until it reloads.
	if _, err := f.engine.Claim(ctx, pool.Thread, stored.Owner, tab.TabID); !errors.Is(err, pool.ErrRunnerStale) {
		t.Fatalf("claim of the stale tab err = %v, want ErrRunnerStale", err)
	}
}

// TestR10RunnerPinCurrentPinDoesNotRefund is the control: the same report from a
// tab that trusts the live key is a plain failed try, not a page fault.
func TestR10RunnerPinCurrentPinDoesNotRefund(t *testing.T) {
	f := newW7Anonymous(t)
	const remote = "198.51.100.132:4242"
	ctx := context.Background()

	_, devBearer := f.credentials(t, "developer")
	var job pool.Job
	if err := json.Unmarshal(f.call(t, http.MethodPost, "/thread-pool/v1/jobs", devBearer,
		pool.CreateRequest{ModuleArtifactIDs: []string{r10Module(t, f, devBearer)},
			Tasks: []pool.TaskSpec{{TaskID: "pin-live", Input: json.RawMessage(`{"n":1}`)}}},
		http.StatusCreated, ""), &job); err != nil {
		t.Fatal(err)
	}
	_, contribBearer := f.credentials(t, "contributor")
	var tab pool.Worker
	if err := json.Unmarshal(f.call(t, http.MethodPost, "/thread-pool/v1/workers/register", contribBearer,
		map[string]any{"hostId": "live-pin", "slots": 1, "runnerPin": r10RunnerPin(t, f.engine.TaskKeyID)}, http.StatusCreated, remote), &tab); err != nil {
		t.Fatal(err)
	}
	var a pool.Assignment
	if err := json.Unmarshal(f.call(t, http.MethodPost, "/thread-pool/v1/workers/"+tab.TabID+"/leases", contribBearer,
		nil, http.StatusOK, remote), &a); err != nil {
		t.Fatal(err)
	}
	var res pool.Result
	if err := json.Unmarshal(f.call(t, http.MethodPost, "/thread-pool/v1/workers/"+tab.TabID+"/results", contribBearer,
		pool.Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Error: "verify failed: unknown_operator_key"},
		http.StatusOK, remote), &res); err != nil {
		t.Fatal(err)
	}
	if res.Status == "requeued" || res.Attempts != a.Lease.Attempt {
		t.Fatalf("result = %+v, want a plain failed try (no refund)", res)
	}
	var stored pool.Worker
	if err := f.db.View(ctx, func(r store.Reader) error { return r.Get(store.Workers, tab.TabID, &stored) }); err != nil {
		t.Fatal(err)
	}
	if stored.RunnerStale {
		t.Fatal("a tab that trusts the live key was marked stale")
	}
}
