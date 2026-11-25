package pool

import (
	"context"
	"errors"
	"testing"
	"time"

	"ccgsc/poolscore/internal/verify"
)

func TestFISS2LeaseCanBeRenewedRepeatedly(t *testing.T) {
	h := newHarness(t)
	h.seed(t, 1, nil)
	w := h.worker(t, 1)
	a, err := h.engine.Claim(context.Background(), Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(20 * time.Second)
	if _, err := h.engine.Heartbeat(context.Background(), Thread, w.Owner, w.TabID, &a.Lease); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(20 * time.Second)
	if _, err := h.engine.Heartbeat(context.Background(), Thread, w.Owner, w.TabID, &a.Lease); err != nil {
		t.Fatalf("second renewal: %v", err)
	}
	got, err := h.engine.Get(context.Background(), Thread, "developer", a.Lease.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Tasks[0].Lease.ExpiresAt.Equal(h.now.Add(h.engine.TTL)) {
		t.Fatalf("expiry = %s, want %s", got.Tasks[0].Lease.ExpiresAt, h.now.Add(h.engine.TTL))
	}
}

func TestFISS2RenewalConflictKeepsHeartbeat(t *testing.T) {
	h := newHarness(t)
	h.seed(t, 1, nil)
	w := h.worker(t, 1)
	a, err := h.engine.Claim(context.Background(), Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(10 * time.Second)
	forged := a.Lease
	forged.Generation++
	if _, err := h.engine.Heartbeat(context.Background(), Thread, w.Owner, w.TabID, &forged); !errors.Is(err, ErrConflict) {
		t.Fatalf("renewal error = %v, want conflict", err)
	}
	got, err := h.engine.Get(context.Background(), Thread, "developer", a.Lease.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tasks[0].Lease == nil {
		t.Fatal("lease was lost after renewal conflict")
	}
}

func TestFISS2LateResultIsNotMeteredTwice(t *testing.T) {
	h := newHarness(t)
	h.seed(t, 1, nil)
	w := h.worker(t, 1)
	a, err := h.engine.Claim(context.Background(), Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	h.now = a.Lease.ExpiresAt.Add(10 * time.Second)
	if _, err := h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, lateSubmission(a, `{"late":true}`)); err != nil {
		t.Fatal(err)
	}
	if got := h.engine.Metering().CPUSeconds; got != 0 {
		t.Fatalf("CPU seconds = %d, want 0 for late result", got)
	}
}

func TestFISS2NilRenewalLeaseDoesNotPanic(t *testing.T) {
	if got := renewedLeaseExpiry(&Task{TaskSpec: TaskSpec{TimeoutMS: 1000}}, time.Unix(100, 0), time.Second); got.IsZero() {
		t.Fatal("nil lease returned zero expiry")
	}
}

func TestFISS2OwnerQuotaAppliesWhenOwnerAttaches(t *testing.T) {
	e, done := newSessionR2Engine(t)
	defer done()
	e.SetSessionLimits(SessionLimits{MaxArtifactBytesPerOwner: 5})
	data := []byte("123456")
	id := verify.SHA256(data)
	if _, err := e.UploadOwned(context.Background(), id, "text/plain", 0, int64(len(data)), data[:3], "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.UploadOwned(context.Background(), id, "text/plain", 3, int64(len(data)), data[3:], "owner", false); !errors.Is(err, ErrEmpty) {
		t.Fatalf("owner attachment error = %v, want quota error", err)
	}
}

func TestFISS2CancelMetersActiveLease(t *testing.T) {
	h := newHarness(t)
	id := uploadF1(t, h.engine, []byte("module"))
	j, err := h.engine.Create(context.Background(), Thread, "developer", "", CreateRequest{
		ModuleArtifactIDs: []string{id},
		Tasks:             []TaskSpec{{TaskID: "cancel"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	w, err := h.engine.Register(context.Background(), Thread, "gpu-worker", Worker{HostID: "gpu-host", Slots: 1, GPU: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.engine.Claim(context.Background(), Thread, "gpu-worker", w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(3 * time.Second)
	if _, _, err := h.engine.Cancel(context.Background(), Thread, "developer", j.JobID); err != nil {
		t.Fatal(err)
	}
	if got := h.engine.Metering().GPUSeconds; got != 3 {
		t.Fatalf("GPU seconds = %d, want 3", got)
	}
}

func TestFISS2SessionIndexPrunedOnRelease(t *testing.T) {
	e, done := newSessionR2Engine(t)
	defer done()
	id := uploadF1(t, e, []byte("module"))
	w, err := e.Register(context.Background(), Process, "worker-owner", Worker{HostID: "host", Slots: 1})
	if err != nil {
		t.Fatal(err)
	}
	s, err := e.OpenSession(context.Background(), "developer", "", SessionRequest{TabCount: 1, ModuleArtifactIDs: []string{id}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.CloseSession(context.Background(), "developer", s.SessionID, false); err != nil {
		t.Fatal(err)
	}
	sessionIndex.Lock()
	_, indexed := sessionIndex.ids[e][s.SessionID]
	sessionIndex.Unlock()
	if indexed {
		t.Fatalf("session %s remains indexed after release (worker %s)", s.SessionID, w.TabID)
	}
}
