package pool

import (
	"ccgsc/poolscore/internal/store"
	"context"
	"errors"
	"testing"
	"time"

	"ccgsc/poolscore/internal/verify"
)

func TestFIXQ2HeartbeatRefreshesSessionTab(t *testing.T) {
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
	now := e.Now().Add(3 * e.TTL)
	e.Now = func() time.Time { return now }
	if _, err := e.Heartbeat(context.Background(), Process, w.Owner, w.TabID, nil); err != nil {
		t.Fatal(err)
	}
	got, err := e.GetSession(context.Background(), "developer", s.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "open" || !got.Tabs[0].Heartbeat.Equal(now) {
		t.Fatalf("session after heartbeat = %+v", got)
	}
}

func TestFIXQ2SessionIndexRebuiltOnRestart(t *testing.T) {
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
	reloaded, err := New(e.Store, e.Log, "reloaded", e.TTL)
	if err != nil {
		t.Fatal(err)
	}
	now := e.Now().Add(3 * e.TTL)
	reloaded.Now = func() time.Time { return now }
	if _, err := reloaded.GetSession(context.Background(), "developer", s.SessionID); err != nil {
		t.Fatal(err)
	}
	got, err := reloaded.GetSession(context.Background(), "developer", s.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if (got.State != "draining" && got.State != "closed") || got.Tabs[0].TabID != w.TabID {
		t.Fatalf("reloaded session = %+v", got)
	}
}

func TestFIXQ2ArtifactOwnerLimit(t *testing.T) {
	e, done := newSessionR2Engine(t)
	defer done()
	e.SetSessionLimits(SessionLimits{MaxArtifactBytesPerOwner: 5})
	first := []byte("1234")
	if _, err := e.UploadOwned(context.Background(), verify.SHA256(first), "text/plain", 0, int64(len(first)), first, "owner", false); err != nil {
		t.Fatal(err)
	}
	second := []byte("56")
	if _, err := e.UploadOwned(context.Background(), verify.SHA256(second), "text/plain", 0, int64(len(second)), second, "owner", false); !errors.Is(err, ErrEmpty) {
		t.Fatalf("second upload error = %v, want owner quota", err)
	}
}

func TestFIXQ2RenewalHonorsTaskTimeout(t *testing.T) {
	h := newHarness(t)
	id := uploadF1(t, h.engine, []byte("module"))
	j, err := h.engine.Create(context.Background(), Thread, "developer", "", CreateRequest{
		ModuleArtifactIDs: []string{id},
		Tasks:             []TaskSpec{{TaskID: "timeout", TimeoutMS: 45_000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	w := h.worker(t, 1)
	a, err := h.engine.Claim(context.Background(), Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(20 * time.Second)
	if _, err := h.engine.Heartbeat(context.Background(), Thread, w.Owner, w.TabID, &a.Lease); err != nil {
		t.Fatal(err)
	}
	got, err := h.engine.Get(context.Background(), Thread, "developer", j.JobID)
	if err != nil {
		t.Fatal(err)
	}
	want := h.now.Add(25 * time.Second)
	if !got.Tasks[0].Lease.ExpiresAt.Equal(want) {
		t.Fatalf("renewed expiry = %s, want %s", got.Tasks[0].Lease.ExpiresAt, want)
	}
}

func TestFIXQ2MeteringRecordsLeaseRuntime(t *testing.T) {
	h := newHarness(t)
	id := uploadF1(t, h.engine, []byte("module"))
	_, err := h.engine.Create(context.Background(), Thread, "developer", "", CreateRequest{
		ModuleArtifactIDs: []string{id},
		Tasks:             []TaskSpec{{TaskID: "meter"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	w, err := h.engine.Register(context.Background(), Thread, "gpu-worker", Worker{HostID: "gpu-host", Slots: 1, GPU: true})
	if err != nil {
		t.Fatal(err)
	}
	a, err := h.engine.Claim(context.Background(), Thread, w.Owner, w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(3 * time.Second)
	output := []byte(`{"ok":true}`)
	if _, err := h.engine.Submit(context.Background(), Thread, w.Owner, w.TabID, Submission{
		Lease:        a.Lease,
		TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt,
		Output: output,
		SHA256: verify.SHA256(output),
	}); err != nil {
		t.Fatal(err)
	}
	if got := h.engine.Metering().GPUSeconds; got != 3 {
		t.Fatalf("GPU seconds = %d, want 3", got)
	}
}

func TestFIXR2MeteringDoesNotSurviveRolledBackTransaction(t *testing.T) {
	h := newHarness(t)
	w, err := h.engine.Register(context.Background(), Thread, "gpu-worker", Worker{HostID: "gpu-host", Slots: 1, GPU: true})
	if err != nil {
		t.Fatal(err)
	}
	lease := Lease{WorkerID: w.TabID, StartedAt: h.now.Add(-4 * time.Second)}
	var metered []meteredRuntime
	err = h.engine.Store.Update(context.Background(), func(tx store.Tx) error {
		if err := h.engine.meterLeaseRuntime(tx, lease, &metered); err != nil {
			return err
		}
		return errors.New("force rollback")
	})
	if err == nil {
		t.Fatal("rollback transaction unexpectedly committed")
	}
	if len(metered) != 1 || metered[0].seconds != 4 {
		t.Fatalf("pending metering = %+v, want one 4-second measurement", metered)
	}
	if got := h.engine.Metering().GPUSeconds; got != 0 {
		t.Fatalf("GPU seconds = %d, want 0 after rollback", got)
	}
}
