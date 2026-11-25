package pool

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"ccgsc/poolscore/internal/store"
	"ccgsc/poolscore/internal/store/pg"
)

func TestF15PostgreSQLConcurrentCreditIsExactlyOnce(t *testing.T) {
	h := newPGEngineHarness(t)
	secondStore, err := pg.Open(h.dsn)
	if err != nil {
		t.Fatalf("open second PostgreSQL engine: %v", err)
	}
	t.Cleanup(func() { _ = secondStore.Close() })
	second, err := New(secondStore, slog.New(slog.NewTextHandler(io.Discard, nil)), "cli1-second", time.Minute)
	if err != nil {
		t.Fatalf("second Engine.New: %v", err)
	}
	second.Now = func() time.Time { return h.now }
	second.Verifier.SetClock(func() time.Time { return h.now })

	j := &Job{JobID: "f15-concurrent-job"}
	w := Worker{Owner: "alice", TabID: "tab-alice"}
	lease := &Lease{
		JobID: j.JobID, TaskID: "task-one", WorkerID: w.TabID, Token: "lease-one",
		Generation: 1, StartedAt: h.now.Add(-2 * time.Second), ExpiresAt: h.now.Add(time.Second),
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, engine := range []*Engine{h.e, second} {
		wg.Add(1)
		go func(engine *Engine) {
			defer wg.Done()
			<-start
			errs <- engine.Store.Update(context.Background(), func(tx store.Tx) error {
				return engine.creditContributor(tx, j, w, lease.TaskID, lease, true)
			})
		}(engine)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent credit: %v", err)
		}
	}

	got, err := h.e.ContributorCounters(context.Background(), ContributorAccountKey(w.Owner))
	if err != nil {
		t.Fatalf("read counters: %v", err)
	}
	if got.AcceptedTasks != 1 || got.ComputeMS != 2000 {
		t.Fatalf("counters = %+v, want exactly one task and 2000 ms", got)
	}
}
