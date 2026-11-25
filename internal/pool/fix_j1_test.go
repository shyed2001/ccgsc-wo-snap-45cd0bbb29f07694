package pool

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFIXJ1SplitTasksConsumeQueuedQuota(t *testing.T) {
	h := newHarness(t)
	h.engine.SetQuotaConfig(QuotaConfig{MaxQueuedTasks: 10, RetryAfter: 17 * time.Second})
	module := uploadF1(t, h.engine, []byte("module"))
	input := uploadF1(t, h.engine, []byte("0123456789a"))

	_, err := h.engine.Create(context.Background(), Thread, "developer", "", CreateRequest{
		ModuleArtifactIDs: []string{module},
		Split:             &SplitSpec{ArtifactID: input, Count: 11},
	})
	var quota *QuotaError
	if !errors.As(err, &quota) || quota.RetryAfter != 17*time.Second {
		t.Fatalf("error = %v, want queued-task quota with Retry-After 17s", err)
	}
}
