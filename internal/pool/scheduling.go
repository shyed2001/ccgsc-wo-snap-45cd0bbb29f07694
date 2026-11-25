package pool

import (
	"ccgsc/poolscore/internal/verify"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var ErrQuotaExceeded = errors.New("developer quota exceeded")

const reservedVerificationSlots = 1

// QuotaConfig controls admission and priority policy for each developer.
// A zero limit means unlimited.
type QuotaConfig struct {
	MaxRunningJobs    int
	MaxQueuedTasks    int
	RequestsPerMinute int
	RetryAfter        time.Duration
	HighPriority      bool
}

func DefaultQuotaConfig() QuotaConfig {
	return QuotaConfig{RetryAfter: time.Minute, HighPriority: true}
}

type QuotaError struct {
	RetryAfter time.Duration
	Reason     string
}

func (e *QuotaError) Error() string {
	if e.Reason != "" {
		return e.Reason
	}
	return ErrQuotaExceeded.Error()
}

func (e *QuotaError) Unwrap() error { return ErrQuotaExceeded }

type MeteringSnapshot struct {
	AcceptedJobs   int64 `json:"acceptedJobs"`
	RejectedJobs   int64 `json:"rejectedJobs"`
	LeasedTasks    int64 `json:"leasedTasks"`
	CompletedTasks int64 `json:"completedTasks"`
	CPUSeconds     int64 `json:"cpuSeconds"`
	GPUSeconds     int64 `json:"gpuSeconds"`
}

type schedulerState struct {
	mu        sync.Mutex
	quota     QuotaConfig
	requests  map[string][]time.Time
	accepted  atomic.Int64
	rejected  atomic.Int64
	leased    atomic.Int64
	completed atomic.Int64
	cpu       atomic.Int64
	gpu       atomic.Int64
}

func newSchedulerState() *schedulerState {
	return &schedulerState{quota: DefaultQuotaConfig(), requests: make(map[string][]time.Time)}
}

func (e *Engine) SetQuotaConfig(c QuotaConfig) {
	if c.RetryAfter <= 0 {
		c.RetryAfter = time.Minute
	}
	e.scheduler.mu.Lock()
	e.scheduler.quota = c
	e.scheduler.mu.Unlock()
}

func (e *Engine) QuotaConfig() QuotaConfig {
	e.scheduler.mu.Lock()
	defer e.scheduler.mu.Unlock()
	return e.scheduler.quota
}

func (e *Engine) Metering() MeteringSnapshot {
	return MeteringSnapshot{
		AcceptedJobs:   e.scheduler.accepted.Load(),
		RejectedJobs:   e.scheduler.rejected.Load(),
		LeasedTasks:    e.scheduler.leased.Load(),
		CompletedTasks: e.scheduler.completed.Load(),
		CPUSeconds:     e.scheduler.cpu.Load(),
		GPUSeconds:     e.scheduler.gpu.Load(),
	}
}

func (e *Engine) AddMeteredSeconds(gpu bool, seconds int64) {
	if seconds < 0 {
		return
	}
	if gpu {
		e.scheduler.gpu.Add(seconds)
	} else {
		e.scheduler.cpu.Add(seconds)
	}
}

func (e *Engine) checkQuotaRecords(owner string, jobs []Job, requestedTasks int, now time.Time) error {
	e.scheduler.mu.Lock()
	defer e.scheduler.mu.Unlock()
	q := e.scheduler.quota
	running, queued := 0, 0
	for _, j := range jobs {
		if j.Owner != owner || terminal(j) {
			continue
		}
		if j.State == "running" {
			running++
		}
		for _, t := range j.Tasks {
			if t.State == "queued" {
				queued++
			}
		}
	}
	if q.MaxRunningJobs > 0 && running >= q.MaxRunningJobs {
		e.scheduler.rejected.Add(1)
		return &QuotaError{RetryAfter: q.RetryAfter, Reason: "running job quota exceeded"}
	}
	if q.MaxQueuedTasks > 0 && queued+requestedTasks > q.MaxQueuedTasks {
		e.scheduler.rejected.Add(1)
		return &QuotaError{RetryAfter: q.RetryAfter, Reason: "queued task quota exceeded"}
	}
	if q.RequestsPerMinute > 0 {
		window := now.Add(-time.Minute)
		recent := e.scheduler.requests[owner]
		n := 0
		for _, at := range recent {
			if at.After(window) {
				recent[n] = at
				n++
			}
		}
		recent = recent[:n]
		e.scheduler.requests[owner] = recent
		if len(recent) >= q.RequestsPerMinute {
			retry := q.RetryAfter
			if len(recent) > 0 && (retry <= 0 || recent[0].Add(time.Minute).Sub(now) > retry) {
				retry = recent[0].Add(time.Minute).Sub(now)
			}
			e.scheduler.rejected.Add(1)
			return &QuotaError{RetryAfter: retry, Reason: "request rate quota exceeded"}
		}
	}
	e.scheduler.requests[owner] = append(e.scheduler.requests[owner], now)
	return nil
}

func scheduleJobs(jobs []Job, now time.Time) {
	shares := make(map[string]int)
	for _, j := range jobs {
		for _, t := range j.Tasks {
			if t.State == "leased" {
				shares[j.Owner]++
			}
		}
	}
	sort.SliceStable(jobs, func(i, j int) bool {
		a, b := jobs[i], jobs[j]
		ap, bp := priorityRank(a.Priority), priorityRank(b.Priority)
		if ap != bp {
			return ap > bp
		}
		if shares[a.Owner] != shares[b.Owner] {
			return shares[a.Owner] < shares[b.Owner]
		}
		aa := now.Sub(a.CreatedAt)
		ba := now.Sub(b.CreatedAt)
		return aa > ba
	})
}

// claimableTask reports whether a task may receive a lease. A redundant
// verification task stays claimable while its next replica is still missing
// (W10C row 1).
func claimableTask(t *Task) bool {
	if t.State == "queued" {
		return true
	}
	return t.State == StatePendingVerification && t.Redundant != nil && len(t.Redundant.Held) < t.Redundant.Target
}

func verificationTask(e *Engine, j *Job, t *Task) bool {
	if t.Pending != nil || t.CanaryID != "" || t.Redundant != nil {
		return true
	}
	if t.Attempts != 0 || e == nil {
		return false
	}
	cfg := e.TrustConfig()
	return cfg.CanaryEnabled && cfg.CanaryRate > 0 && len(e.canaryList()) > 0 &&
		verify.ShouldRerunWithKey(e.rerunSecret, j.JobID+":"+t.TaskID, cfg.CanaryRate)
}

func hasQueuedVerificationTask(e *Engine, j *Job) bool {
	for i := range j.Tasks {
		t := &j.Tasks[i]
		if claimableTask(t) && verificationTask(e, j, t) {
			return true
		}
	}
	return false
}

func prioritizeVerificationTasks(e *Engine, j *Job) {
	queued := make([]Task, 0, len(j.Tasks))
	normal := make([]Task, 0, len(j.Tasks))
	for _, t := range j.Tasks {
		if claimableTask(&t) && verificationTask(e, j, &t) {
			queued = append(queued, t)
		} else {
			normal = append(normal, t)
		}
	}
	copy(j.Tasks, append(queued, normal...))
}

func runningJobsForOwner(jobs []Job, owner string) int {
	n := 0
	for _, j := range jobs {
		if j.Owner != owner || terminal(j) {
			continue
		}
		for _, t := range j.Tasks {
			if t.State == "leased" {
				n++
				break
			}
		}
	}
	return n
}

func claimAllowed(q QuotaConfig, jobs []Job, j *Job, verification bool) bool {
	if verification || q.MaxRunningJobs <= 0 || j.State == "running" {
		return true
	}
	return runningJobsForOwner(jobs, j.Owner) < q.MaxRunningJobs
}

func (e *Engine) scheduleForClaim(jobs []Job) {
	now := time.Now()
	if e.Now != nil {
		now = e.Now()
	}
	scheduleJobs(jobs, now)
	sort.SliceStable(jobs, func(i, j int) bool {
		vi := hasQueuedVerificationTask(e, &jobs[i])
		vj := hasQueuedVerificationTask(e, &jobs[j])
		return vi && !vj
	})
	for i := range jobs {
		prioritizeVerificationTasks(e, &jobs[i])
	}
}

func priorityRank(priority string) int {
	switch priority {
	case "high":
		return 3
	case "normal":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}
