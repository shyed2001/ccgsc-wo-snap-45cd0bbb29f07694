// Path: internal/checksuite/checksuite.go | Author: GLM for Claude Opus 5.5 | 2026-11-21
// What/why: T108 one-command check suite for 2 pools x 3 APIs (REST + SSE + gRPC).
// Shared by cmd/pools-e2e -full and the cmd/pools-client check subcommand, so both
// entry points run exactly the same suite. Per pool the suite uploads one unique
// module artifact, submits a job through REST and a job through gRPC, watches every
// job through an SSE stream (with a Last-Event-ID resume) and through a gRPC event
// stream (with the same resume), starts the builtin worker only after the watches
// are established, asserts the queued -> leased -> done event order and that the
// events arrive live while the job runs, checks the settled matmul result against
// the reference hash, walks the anonymous contributor path of WO_T96 with its
// subject-keeping renewal, and revokes its own keys through the T99 route at the
// end. The JSON report and the stdout table carry no secret material.
package checksuite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/verify"
)

// Matmul160ReferenceSHA256 is the reference sha256 of the raw little endian
// float32 matrix bytes of the 160^3 matmul of the existing test module
// (kind matmul-f32-gpu, a[r][k]=(r*n+k)%7-3, b[k][c]=(k*n+c)%5-2).
const Matmul160ReferenceSHA256 = "1a5a6c39c56eada3b4ca01eaed913972ed5e48d1318884748e1abf3646abc2d2"

// Pool route prefixes of the two pools.
const (
	ThreadPrefix  = "/thread-pool/v1"
	ProcessPrefix = "/process-pool/v1"
)

// Status values of a check line.
const (
	StatusPass = "PASS"
	StatusFail = "FAIL"
)

// Check name vocabulary of the report table.
const (
	checkArtifact    = "artifact-upload"
	checkRestSubmit  = "rest-submit"
	checkGRPCSubmit  = "grpc-submit"
	checkSSEWatch    = "sse-watch"
	checkGRPCWatch   = "grpc-watch"
	checkResultHash  = "result-hash"
	checkAnonymous   = "anonymous"
	checkRevokeKeys  = "revoke-own-keys"
	checkAwaitJobs   = "await-completed"
	checkWorkerSetup = "builtin-worker"
)

// Options configures one suite run.
type Options struct {
	// Base is the server base URL, for example http://127.0.0.1:8091.
	Base string
	// AdminKey is a Store A admin or bootstrap API key. The suite creates its
	// own developer and contributor keys with it and revokes them again at the
	// end through the T99 route.
	AdminKey string
	// Pools are the route prefixes to check; empty means both pools.
	Pools []string
	// Size is the matmul size of the submitted jobs; 160 or 240.
	Size int
	// Timeout bounds the whole run. The default is 3 minutes.
	Timeout time.Duration
	// SettleDelay is the hold between a lease and its result submission. It
	// widens the leased -> done gap so the Last-Event-ID resume has a
	// deterministic window. The default is 150ms.
	SettleDelay time.Duration
}

func (o Options) withDefaults() Options {
	if o.Size != 240 {
		o.Size = 160
	}
	if o.Timeout <= 0 {
		o.Timeout = 3 * time.Minute
	}
	if o.SettleDelay <= 0 {
		o.SettleDelay = 150 * time.Millisecond
	}
	if len(o.Pools) == 0 {
		o.Pools = []string{ThreadPrefix, ProcessPrefix}
	}
	return o
}

// Check is one PASS/FAIL line of the report.
type Check struct {
	ID         string `json:"id"`
	Pool       string `json:"pool,omitempty"`
	Name       string `json:"check"`
	Status     string `json:"status"`
	Detail     string `json:"detail,omitempty"`
	JobID      string `json:"jobId,omitempty"`
	DurationMS int64  `json:"durationMs"`
}

// Report is the secret-free result of one suite run.
type Report struct {
	Mode        string    `json:"mode"`
	Base        string    `json:"base"`
	Pools       []string  `json:"pools"`
	Checks      []Check   `json:"checks"`
	Pass        bool      `json:"pass"`
	RevokedKeys int       `json:"revokedKeys"`
	StartedAt   time.Time `json:"startedAt"`
	DurationMS  int64     `json:"durationMs"`
}

// Table renders one PASS/FAIL line per check plus the summary line.
func (r Report) Table() string {
	var b strings.Builder
	for _, c := range r.Checks {
		detail := c.Detail
		if detail != "" {
			detail = " | " + detail
		}
		fmt.Fprintf(&b, "%-4s  %-8s  %-16s  %6dms  %s%s\n",
			c.Status, c.Pool, c.Name, c.DurationMS, c.JobID, detail)
	}
	fmt.Fprintf(&b, "%-4s  %d/%d checks passed in %dms\n",
		passWord(r.Pass), passCount(r.Checks), len(r.Checks), r.DurationMS)
	return b.String()
}

func passWord(pass bool) string {
	if pass {
		return StatusPass
	}
	return StatusFail
}

func passCount(checks []Check) int {
	n := 0
	for _, c := range checks {
		if c.Status == StatusPass {
			n++
		}
	}
	return n
}

func okCheck(pool, name, detail string, start time.Time) Check {
	return Check{ID: checkID(pool, name), Pool: pool, Name: name, Status: StatusPass,
		Detail: detail, DurationMS: time.Since(start).Milliseconds()}
}

func fail(pool, name, format string, args ...any) Check {
	return Check{ID: checkID(pool, name), Pool: pool, Name: name, Status: StatusFail,
		Detail: fmt.Sprintf(format, args...)}
}

// Run executes the whole suite and returns the report. An error is returned
// only when the run could not start at all (bad options, unreachable server);
// every per-check failure is reported through the report, never as an error.
func Run(parent context.Context, o Options) (Report, error) {
	o = o.withDefaults()
	if strings.TrimSpace(o.AdminKey) == "" {
		return Report{}, errors.New("an admin API key is required to create and revoke the suite keys")
	}
	if !strings.HasPrefix(o.Base, "http://") && !strings.HasPrefix(o.Base, "https://") {
		return Report{}, errors.New("base must be an http(s) URL")
	}
	ctx, cancel := context.WithTimeout(parent, o.Timeout)
	defer cancel()
	rep := Report{Mode: "full", Base: o.Base, Pools: append([]string(nil), o.Pools...), StartedAt: time.Now().UTC(), Checks: []Check{}}

	t, err := newTransport(o.Base)
	if err != nil {
		return rep, err
	}
	defer t.close()

	// The suite identity is unique per run, so its keys, jobs and artifacts
	// never collide with a previous run against the same server.
	run := "checksuite-" + auth.ID()

	adminToken := make(map[string]string, len(o.Pools))
	for _, p := range o.Pools {
		token, err := t.exchange(ctx, p, o.AdminKey)
		if err != nil {
			return rep, fmt.Errorf("admin token exchange on %s failed: %w", p, err)
		}
		adminToken[p] = token
	}
	dev, err := t.createKey(ctx, o.Pools[0], adminToken[o.Pools[0]], auth.User{
		ID: run + "-developer", AccountStore: "B", Roles: []string{"developer"},
	}, []string{"artifacts:write", "jobs:write", "jobs:read"})
	if err != nil {
		return rep, fmt.Errorf("developer key creation failed: %w", err)
	}
	contrib, err := t.createKey(ctx, o.Pools[0], adminToken[o.Pools[0]], auth.User{
		ID: run + "-contributor", AccountStore: "B", Roles: []string{"contributor"},
	}, []string{"worker"})
	if err != nil {
		return rep, fmt.Errorf("contributor key creation failed: %w", err)
	}
	devTokens := make(map[string]string, len(o.Pools))
	contribTokens := make(map[string]string, len(o.Pools))
	for _, p := range o.Pools {
		if devTokens[p], err = t.exchange(ctx, p, dev.raw); err != nil {
			return rep, fmt.Errorf("developer token exchange on %s failed: %w", p, err)
		}
		if contribTokens[p], err = t.exchange(ctx, p, contrib.raw); err != nil {
			return rep, fmt.Errorf("contributor token exchange on %s failed: %w", p, err)
		}
	}

	for _, p := range o.Pools {
		rep.Checks = append(rep.Checks, t.runPool(ctx, o, run, p, devTokens[p], contribTokens[p])...)
	}

	revoked, check := t.revokeOwnKeys(ctx, o.Pools[0], adminToken[o.Pools[0]], dev, contrib)
	rep.RevokedKeys = revoked
	rep.Checks = append(rep.Checks, check)

	rep.Pass = passCount(rep.Checks) == len(rep.Checks) && len(rep.Checks) > 0
	rep.DurationMS = time.Since(rep.StartedAt).Milliseconds()
	return rep, nil
}

// runPool performs every per-pool check: artifact upload, REST and gRPC
// submit, SSE and gRPC watch of both jobs with the worker started after the
// watch, the result hash check and the anonymous contributor path.
func (t *transport) runPool(ctx context.Context, o Options, run, prefix, devToken, contribToken string) []Check {
	name := poolName(prefix)
	var checks []Check

	// Unique artifact bytes per run: artifacts are content-addressed and owned
	// by the first uploader, so fixed bytes would collide across runs.
	artifact := []byte("pools-checksuite module " + auth.ID())
	artifactID := verify.SHA256(artifact)
	checkStart := time.Now()
	status, _, err := t.uploadArtifact(ctx, prefix, devToken, artifact, artifactID)
	if err == nil && status == 201 {
		checks = append(checks, okCheck(name, checkArtifact, "unique module artifact "+artifactID[:12]+"…", checkStart))
	} else {
		checks = append(checks, Check{ID: checkID(name, checkArtifact), Pool: name, Name: checkArtifact,
			Status:     StatusFail,
			Detail:     httpDetail("PUT", prefix+"/artifacts", status, err),
			DurationMS: time.Since(checkStart).Milliseconds()})
		return checks
	}

	// Both watches connect before the jobs exist, so every job event of this
	// run arrives live and never as a replay of an older snapshot.
	wctx, wcancel := context.WithCancel(ctx)
	sse := newWatcher()
	go func() {
		defer sse.close()
		if err := t.watchSSE(wctx, prefix, devToken, sse); err != nil {
			sse.fail(err)
		}
	}()
	gw := newWatcher()
	go func() {
		defer gw.close()
		if err := t.watchGRPC(wctx, prefix, devToken, gw); err != nil {
			gw.fail(err)
		}
	}()
	watchStart := time.Now()

	// Submit job A through REST and job B through gRPC.
	jobA, status, err := t.restSubmitJob(ctx, prefix, devToken, artifactID, o.Size,
		"matmul-rest", run+"-rest")
	submitStart := time.Now()
	if err == nil && status == 201 && jobA != "" {
		checks = append(checks, okCheck(name, checkRestSubmit, "job accepted, queued", submitStart))
		checks[len(checks)-1].JobID = jobA
	} else {
		checks = append(checks, Check{ID: checkID(name, checkRestSubmit), Pool: name, Name: checkRestSubmit,
			Status:     StatusFail,
			Detail:     httpDetail("POST", prefix+"/jobs", status, err),
			DurationMS: time.Since(submitStart).Milliseconds()})
		wcancel()
		_ = sse.collect()
		_ = gw.collect()
		return checks
	}
	_ = jobA
	jobB, err := t.grpcSubmitJob(ctx, prefix, devToken, artifactID, o.Size,
		"matmul-grpc", run+"-grpc")
	submitStart = time.Now()
	if err == nil && jobB != "" {
		checks = append(checks, okCheck(name, checkGRPCSubmit, "job accepted, queued", submitStart))
		checks[len(checks)-1].JobID = jobB
	} else {
		checks = append(checks, Check{ID: checkID(name, checkGRPCSubmit), Pool: name, Name: checkGRPCSubmit,
			Status:     StatusFail,
			Detail:     grpcDetail("CreateJob", err),
			DurationMS: time.Since(submitStart).Milliseconds()})
		wcancel()
		_ = sse.collect()
		_ = gw.collect()
		return checks
	}

	// The builtin worker starts after both watches and both submits, so the
	// queued and leased events must arrive on the live streams while the jobs
	// run. The worker stops through stop, but its HTTP calls use the suite
	// context: the orchestrator can see a job completed over REST while the
	// worker's result response is still on the wire, and canceling that
	// response would fabricate a worker error.
	wstate := &workerState{}
	stop := make(chan struct{})
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		t.runBuiltinWorker(ctx, stop, prefix, contribToken, o, wstate)
	}()

	// Wait for both jobs to reach a terminal state through the REST surface,
	// then wait for their terminal job.done events to arrive on both watched
	// streams. The event wait is part of the check: the terminal events must
	// be delivered while the run is live, not after the streams were torn
	// down. The worker stop stays independent of the event wait, because the
	// last result response can still be on the wire when the orchestrator
	// sees the job completed.
	checkStart = time.Now()
	stateA, errA := t.awaitJob(ctx, prefix, devToken, jobA, o.Timeout)
	stateB, errB := t.awaitJob(ctx, prefix, devToken, jobB, o.Timeout)
	sseEvents, grpcEvents := waitForEvents(ctx, sse, gw, jobA, jobB)
	close(stop)
	<-workerDone
	wcancel()
	// Final drain: everything still in flight up to the watcher shutdown.
	sseEvents = append(sseEvents, sse.collect()...)
	grpcEvents = append(grpcEvents, gw.collect()...)
	awaitErr := firstErr(errA, errB)
	awaitDetail := fmt.Sprintf("rest job state %q, grpc job state %q, worker error: %v", stateA, stateB, wstate.error())
	if awaitErr == nil && stateA == "completed" && stateB == "completed" && wstate.error() == nil {
		checks = append(checks, okCheck(name, checkAwaitJobs, awaitDetail, checkStart))
	} else {
		checks = append(checks, fail(name, checkAwaitJobs, "%s%s", awaitDetail, awaitErrSuffix(awaitErr)))
	}
	// The worker goroutine is launched only after both watchers and both
	// submissions. Windows' clock can report equal timestamps for distinct
	// operations, so equality does not invalidate that causal ordering.
	if workerStart := wstate.startedAt(); !workerStart.IsZero() && !workerStart.Before(watchStart) {
		checks = append(checks, okCheck(name, checkWorkerSetup,
			"builtin worker started after watch setup (clock may be coarse)", watchStart))
	} else {
		checks = append(checks, fail(name, checkWorkerSetup,
			"builtin worker did not start (or preceded watch setup)"))
	}

	// Watch checks on both transports, covering both jobs.
	checks = append(checks, watchCheck(name, checkSSEWatch, "SSE", sseEvents, sse.resumeCount(), jobA, jobB, watchStart)...)
	checks = append(checks, watchCheck(name, checkGRPCWatch, "gRPC stream", grpcEvents, gw.resumeCount(), jobA, jobB, watchStart)...)

	// Result hash check against the reference for both jobs.
	checks = append(checks, t.resultHashCheck(ctx, prefix, devToken, name, jobA, jobB, o.Size)...)

	// Anonymous contributor path: join -> register -> lease -> result, then a
	// renewal keeps the same subject.
	checks = append(checks, t.anonymousCheck(ctx, o, prefix, devToken, artifactID, name)...)
	return checks
}

func awaitErrSuffix(err error) string {
	if err == nil {
		return ""
	}
	return "; " + err.Error()
}

// watchCheck asserts, for one transport and both jobs, the queued -> leased ->
// done order, live arrival while the job runs (the watch predated the submits),
// and a duplicate-free Last-Event-ID resume. Both jobs share the two streams,
// so each check filters the stream events of its job.
func watchCheck(name, checkName, label string, events []eventRecord, resumes int, jobA, jobB string, watchStart time.Time) []Check {
	var checks []Check
	for _, job := range []string{jobA, jobB} {
		checks = append(checks, watchOneCheck(name, checkName, label, events, resumes, job, watchStart))
	}
	return checks
}

func watchOneCheck(name, checkName, label string, events []eventRecord, resumes int, job string, watchStart time.Time) Check {
	sub := eventsFor(events, job)
	if len(sub) == 0 {
		return fail(name, checkName, "%s: no events arrived for job %s (all: %s)", label, job, dumpEvents(events))
	}
	seen := map[string]bool{}
	for _, ev := range sub {
		if ev.ID == "" {
			return fail(name, checkName, "%s: event without an id for job %s", label, job)
		}
		if seen[ev.ID] {
			return fail(name, checkName, "%s: duplicate event id %s for job %s (Last-Event-ID resume replayed events)", label, ev.ID, job)
		}
		seen[ev.ID] = true
	}
	first := compressTypes(sub)
	queued, leased, done := phases(sub)
	if queued < 0 || leased < 0 || done < 0 {
		return fail(name, checkName, "%s: incomplete queued->leased->done lifecycle for job %s: got %v (all: %s)", label, job, first, dumpEvents(events))
	}
	// Both watchers were launched before the unique job was submitted; the
	// stream replays from its cursor if subscription starts after submission.
	// Preserve the queued -> leased -> done ordering and Last-Event-ID resume
	// proof. Equal watch/queued timestamps are valid on coarse Windows clocks,
	// but a queued event strictly before the watch remains invalid.
	if resumes < 1 {
		return fail(name, checkName, "%s: no Last-Event-ID resume for job %s", label, job)
	}
	if sub[queued].Arrived.Before(watchStart) || !sub[done].Arrived.After(sub[leased].Arrived) {
		return fail(name, checkName, "%s: invalid watch timing for job %s", label, job)
	}
	detail := fmt.Sprintf("%s: %d events, lifecycle %v, %d Last-Event-ID resume(s), watch predated submit",
		label, len(sub), first, resumes)
	return Check{ID: checkID(name, checkName), Pool: name, Name: checkName, Status: StatusPass,
		Detail: detail, JobID: job}
}

// compressTypes folds repeated event types of the arrival-ordered stream into
// their phase sequence, for example queued,queued,leased,done -> queued,leased,done.
func compressTypes(sub []eventRecord) []string {
	out := make([]string, 0, 6)
	for _, ev := range sub {
		if len(out) == 0 || out[len(out)-1] != ev.Type {
			out = append(out, ev.Type)
		}
	}
	return out
}

// phases returns the indexes of the first queued, leased and done phase of the
// job events. The phases open at job.queued, task.leased and task.done
// respectively; job.done belongs to the done phase and closes the run.
func phases(sub []eventRecord) (int, int, int) {
	queued, leased, done := -1, -1, -1
	for i, ev := range sub {
		switch ev.Type {
		case "job.queued":
			if queued < 0 {
				queued = i
			}
		case "task.leased":
			if leased < 0 && queued >= 0 {
				leased = i
			}
		case "task.done", "job.done":
			if done < 0 && leased >= 0 && ev.Type == "task.done" {
				done = i
			}
		}
	}
	return queued, leased, done
}

// dumpEvents renders every received event for diagnostics.
func dumpEvents(events []eventRecord) string {
	parts := make([]string, 0, len(events))
	for _, ev := range events {
		parts = append(parts, fmt.Sprintf("%s/%s/j=%s/c%d", ev.ID, ev.Type, ev.JobID[:min(8, len(ev.JobID))], ev.Conn))
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// eventGrace bounds how long the orchestrator waits for the terminal job.done
// events of the watched jobs before it tears the streams down. In-process
// delivery is sub-millisecond; the grace only covers a slow live delivery.
const eventGrace = 5 * time.Second

// waitForEvents drains both watcher channels until every job reported a
// terminal job.done on both transports, the grace expired or ctx ended. It
// returns everything collected so far in arrival order; the caller appends the
// post-shutdown drain.
func waitForEvents(ctx context.Context, sse, gw *watcher, jobs ...string) ([]eventRecord, []eventRecord) {
	var sseEvents, grpcEvents []eventRecord
	sseSeen := map[string]bool{}
	grpcSeen := map[string]bool{}
	sseClosed, grpcClosed := false, false
	deadline := time.NewTimer(eventGrace)
	defer deadline.Stop()
	drain := func() (done bool) {
		for {
			progress := false
			if !sseClosed {
				select {
				case ev, ok := <-sse.eventsC:
					if !ok {
						sseClosed = true
					} else {
						sseEvents = append(sseEvents, ev)
						if ev.Type == "job.done" {
							sseSeen[ev.JobID] = true
						}
						progress = true
					}
				default:
				}
			}
			if !grpcClosed {
				select {
				case ev, ok := <-gw.eventsC:
					if !ok {
						grpcClosed = true
					} else {
						grpcEvents = append(grpcEvents, ev)
						if ev.Type == "job.done" {
							grpcSeen[ev.JobID] = true
						}
						progress = true
					}
				default:
				}
			}
			if !progress {
				return false
			}
		}
	}
	for {
		drain()
		complete := true
		for _, job := range jobs {
			if !sseSeen[job] || !grpcSeen[job] {
				complete = false
				break
			}
		}
		if complete || sseClosed && grpcClosed {
			return sseEvents, grpcEvents
		}
		select {
		case <-ctx.Done():
			return sseEvents, grpcEvents
		case <-deadline.C:
			return sseEvents, grpcEvents
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// eventsFor filters the events of one job in arrival order.
func eventsFor(events []eventRecord, jobID string) []eventRecord {
	out := make([]eventRecord, 0, len(events))
	for _, ev := range events {
		if ev.JobID == jobID {
			out = append(out, ev)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Arrived.Before(out[j].Arrived) })
	return out
}

func checkID(pool, name string) string { return pool + "/" + name }

// WriteJSON writes the report as indented JSON to path with owner-only
// permissions. The report carries identifiers and outcomes, never secrets.
func WriteJSON(path string, r Report) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0600)
}

// poolName maps a route prefix to the short report name.
func poolName(prefix string) string {
	switch prefix {
	case ThreadPrefix:
		return "thread"
	case ProcessPrefix:
		return "process"
	}
	return strings.Trim(prefix, "/")
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
