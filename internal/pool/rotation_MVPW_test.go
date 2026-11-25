package pool

import (
	"context"
	"sync"
	"testing"
	"time"

	"ccgsc/poolscore/internal/verify"
)

func TestMVPWConcurrentRotationAndSigning(t *testing.T) {
	h := newHarness(t)
	job := &Job{JobID: "job"}
	task := &Task{TaskSpec: TaskSpec{TaskID: "task"}}
	lease := Lease{TaskID: "task", WorkerID: "worker", Token: "token", ExpiresAt: h.now.Add(time.Minute)}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			if err := h.engine.RotateTaskKey(context.Background(), time.Hour); err != nil {
				errs <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			signed, err := h.engine.signTaskEnvelope(job, task, lease, []byte(`{}`))
			if err == nil {
				err = verify.VerifyTaskEnvelope(signed, h.engine.Verifier.Keyring(), h.now)
			}
			if err != nil {
				errs <- err
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
