package pool

import (
	"errors"
	"testing"
	"time"
)

func TestScheduleJobsUsesPriorityAndFairShare(t *testing.T) {
	now := time.Now()
	jobs := []Job{
		{Owner: "busy", Priority: "normal", CreatedAt: now.Add(-time.Hour), Tasks: []Task{{State: "leased"}, {State: "queued"}}},
		{Owner: "new", Priority: "normal", CreatedAt: now.Add(-time.Minute), Tasks: []Task{{State: "queued"}}},
		{Owner: "low", Priority: "low", CreatedAt: now.Add(-2 * time.Hour), Tasks: []Task{{State: "queued"}}},
		{Owner: "high", Priority: "high", CreatedAt: now.Add(-time.Minute), Tasks: []Task{{State: "queued"}}},
	}
	scheduleJobs(jobs, now)
	if jobs[0].Owner != "high" || jobs[1].Owner != "new" || jobs[2].Owner != "busy" || jobs[3].Owner != "low" {
		t.Fatalf("unexpected schedule order: %v, %v, %v, %v", jobs[0].Owner, jobs[1].Owner, jobs[2].Owner, jobs[3].Owner)
	}
}

func TestCheckQuotaRecordsReturnsRetryAfter(t *testing.T) {
	e := &Engine{scheduler: newSchedulerState()}
	e.SetQuotaConfig(QuotaConfig{MaxQueuedTasks: 2, RetryAfter: 17 * time.Second})
	err := e.checkQuotaRecords("dev", []Job{{Owner: "dev", Tasks: []Task{{State: "queued"}}}}, 2, time.Now())
	var quota *QuotaError
	if !errors.As(err, &quota) || quota.RetryAfter != 17*time.Second {
		t.Fatalf("quota error = %v, want Retry-After 17s", err)
	}
}

func TestPriorityValidation(t *testing.T) {
	e := &Engine{scheduler: newSchedulerState()}
	for _, priority := range []string{"low", "normal", "high"} {
		q, _, err := e.Validate(CreateRequest{ModuleArtifactIDs: []string{"a"}, Tasks: []TaskSpec{{TaskID: "t"}}, Priority: priority})
		if err != nil || q.Priority != priority {
			t.Fatalf("priority %q: q=%+v err=%v", priority, q, err)
		}
	}
}

func TestClaimQuotaCountsOnlyJobsAlreadyRunning(t *testing.T) {
	jobs := []Job{
		{Owner: "dev", State: "running", Tasks: []Task{{State: "leased"}}},
		{Owner: "dev", State: "queued", Tasks: []Task{{State: "queued"}}},
	}
	if claimAllowed(QuotaConfig{MaxRunningJobs: 1}, jobs, &jobs[1], false) {
		t.Fatal("queued developer job bypassed running-job quota")
	}
	if !claimAllowed(QuotaConfig{MaxRunningJobs: 1}, jobs, &jobs[1], true) {
		t.Fatal("verification job was blocked by developer running-job quota")
	}
}

func TestVerificationJobsAndTasksArePrioritized(t *testing.T) {
	e := &Engine{scheduler: newSchedulerState()}
	now := time.Now()
	jobs := []Job{
		{Owner: "normal", Priority: "high", CreatedAt: now, Tasks: []Task{{State: "queued"}}},
		{Owner: "verify", Priority: "low", CreatedAt: now, Tasks: []Task{{State: "queued", Pending: &PendingResult{}}}},
	}
	e.scheduleForClaim(jobs)
	if jobs[0].Owner != "verify" {
		t.Fatalf("verification job was not scheduled first: %s", jobs[0].Owner)
	}
	if jobs[0].Tasks[0].Pending == nil {
		t.Fatal("verification task was not kept at the front of its lane")
	}
}
