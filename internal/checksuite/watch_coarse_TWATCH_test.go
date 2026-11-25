package checksuite

import (
	"testing"
	"time"
)

// T-WATCH: Windows can return the same monotonic timestamp for distinct
// operations. Equal wall-clock observations do not reverse causal event order.
func TestTWatchCoarseClockEqualQueuedAndWatchTimestamp(t *testing.T) {
	at := time.Now()
	job := "fresh-job-after-submit"
	events := []eventRecord{
		{ID: "0001", Type: "job.queued", JobID: job, Arrived: at},
		{ID: "0002", Type: "task.leased", JobID: job, Arrived: at.Add(time.Millisecond)},
		{ID: "0003", Type: "task.done", JobID: job, Arrived: at.Add(2 * time.Millisecond)},
		{ID: "0004", Type: "job.done", JobID: job, Arrived: at.Add(3 * time.Millisecond)},
	}
	result := watchOneCheck("thread", checkSSEWatch, "SSE", events, 1, job, at)
	if result.Status != StatusPass {
		t.Fatalf("causally ordered events sharing coarse watch timestamp must pass: %s", result.Detail)
	}

	// An event timestamp strictly before watch setup remains invalid.
	early := append([]eventRecord(nil), events...)
	early[0].Arrived = at.Add(-time.Millisecond)
	result = watchOneCheck("thread", checkSSEWatch, "SSE", early, 1, job, at)
	if result.Status != StatusFail {
		t.Fatalf("pre-watch queue event must fail, got %s", result.Status)
	}
}
