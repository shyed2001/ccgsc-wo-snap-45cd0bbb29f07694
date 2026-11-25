// Path: cmd/pools-browser-smoke/main.go | WO_SMOKE (D-W10-12, G-MVP box 2 prep)
// What/why: the browser smoke harness of pools-core. It starts a real
// headless Chrome against a pools-core server and proves the browser side
// works: the contributor page loads without a CSP violation, the runner page
// serves a CSP whose hash matches the inline script, the module Blob Worker
// and the nested kernel worker start, a matmul job settles with the published
// reference hash, and the task.done event of the job arrives on the SSE
// stream.
//
// Every check is printed as PASS or FAIL with a reason. The program exits 1
// when any check failed. Every process the harness starts is stopped on exit,
// also on Ctrl-C: the local servers are killed and their temp dir is removed,
// and the Chrome process dies with its context.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// check is one PASS/FAIL line of the run.
type check struct {
	// Name is the short name of the check.
	Name string
	// Pass reports whether the check passed.
	Pass bool
	// Reason explains the verdict in one line.
	Reason string
	// Skipped marks a check that could not run here, with the reason.
	Skipped bool
}

// report is the whole run.
type report struct {
	Checks []check
}

// pass records a passed check.
func (r *report) pass(name, reason string) {
	r.Checks = append(r.Checks, check{Name: name, Pass: true, Reason: reason})
}

// fail records a failed check.
func (r *report) fail(name, reason string) {
	r.Checks = append(r.Checks, check{Name: name, Pass: false, Reason: reason})
}

// failed answers the number of failed checks.
func (r *report) failed() int {
	n := 0
	for _, c := range r.Checks {
		if !c.Pass && !c.Skipped {
			n++
		}
	}
	return n
}

// print writes the report to stdout.
func (r *report) print() {
	for _, c := range r.Checks {
		status := "PASS"
		switch {
		case c.Skipped:
			status = "SKIP"
		case !c.Pass:
			status = "FAIL"
		}
		fmt.Printf("%-4s  %-24s  %s\n", status, c.Name, c.Reason)
	}
	passed := 0
	for _, c := range r.Checks {
		if c.Pass {
			passed++
		}
	}
	fmt.Printf("%-4s  %d/%d checks passed\n", verdict(r.failed() == 0), passed, len(r.Checks))
}

// verdict answers the summary word.
func verdict(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

func main() {
	o, err := parseFlags("pools-browser-smoke", os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "pools-browser-smoke:", err)
		os.Exit(2)
	}
	// A Ctrl-C ends the run and unwinds every deferred stop, so no server
	// and no Chrome process survives the harness.
	ctx, cancel := interruptContext(context.Background())
	defer cancel()

	runCtx, runCancel := context.WithTimeout(ctx, o.Timeout)
	defer runCancel()

	rep := &report{Checks: []check{}}
	if err := run(runCtx, o, rep); err != nil {
		fmt.Fprintln(os.Stderr, "pools-browser-smoke:", err)
		rep.print()
		os.Exit(1)
	}
	rep.print()
	if rep.failed() > 0 {
		os.Exit(1)
	}
}

// run executes the whole harness.
func run(ctx context.Context, o options, rep *report) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	// The local servers are started first, so every later check runs against
	// a known base URL.
	base := o.Base
	adminKey := o.AdminKey
	runOrigin := ""
	if o.StartLocal {
		srv, err := startLocalServer(ctx, root)
		if err != nil {
			return err
		}
		// The servers die with the harness, also on Ctrl-C: the deferred
		// stop runs on every exit path.
		defer srv.Stop()
		base = srv.Base
		runOrigin = srv.RunOrigin
		if adminKey == "" {
			adminKey = srv.AdminKey
		}
		fmt.Printf("[SMOKE] local pools-core main instance on %s, run-origin instance on %s\n", srv.Base, srv.RunOrigin)
	}
	fmt.Printf("[SMOKE] base=%s pools=%s timeout=%s\n", base, strings.Join(o.Pools, ","), o.Timeout)

	refs, err := referenceHashes(filepath.Join(root, "web", "workloads", "matmul", "manifest.json"))
	if err != nil {
		return err
	}
	// The sizes are the ones the workload manifest publishes a reference
	// hash for: 160 and 240 today.
	matmulSizes := referenceSizes(refs)
	if len(matmulSizes) == 0 {
		return fmt.Errorf("the workload manifest publishes no reference hash to check")
	}
	fmt.Printf("[SMOKE] reference hashes: %s\n", strings.Join(sortedHashes(refs), " "))

	// The sandbox page is served by the run origin when the deployment has
	// one. Without a run origin the page loads the runner from its own
	// tree, and the sandbox page is then served under /pools/.
	if runOrigin == "" {
		runOrigin, err = runnerOrigin(ctx, base)
		if err != nil {
			fmt.Printf("[SMOKE] the runner-config route could not be read: %v\n", err)
		}
	}
	runnerURL := strings.TrimRight(base, "/") + "/pools/runner.html"
	if runOrigin != "" {
		runnerURL = strings.TrimRight(runOrigin, "/") + "/runner.html"
	}
	fmt.Printf("[SMOKE] runner page: %s\n", runnerURL)

	// The browser runs for every browser check. It is closed on every exit
	// path, so no Chrome process survives the harness.
	chrome, err := chromePath(o.Chrome)
	if err != nil {
		return err
	}
	fmt.Printf("[SMOKE] chrome=%s\n", chrome)
	b, err := startBrowser(ctx, chrome)
	if err != nil {
		return err
	}
	defer b.Close()

	c := newClient(base)

	// Check (a): the contributor page loads with no CSP violation in the
	// console. The console and the log entries of the browser are collected
	// from the first navigation on, so no message is lost.
	pageURL := fmt.Sprintf("%s/pools/contribute.html?pools=%s", strings.TrimRight(base, "/"), strings.Join(o.Pools, ","))
	if err := b.navigate(ctx, pageURL); err != nil {
		return err
	}
	if err := b.waitDocument(ctx, 20*time.Second); err != nil {
		return err
	}
	// The page writes its state after the registration, so the check waits
	// for the log to show that the page is live.
	_, _ = b.waitForLogLine(ctx, "Registered with the pool server", 30*time.Second)
	handshake, handshakeSeen := b.waitForLogLine(ctx, "Runner handshake READY", 20*time.Second)
	// A short settle, so a late console message of the page is collected too.
	time.Sleep(2 * time.Second)
	violations := b.cspViolations()
	if len(violations) == 0 {
		rep.pass("contribute-page-csp", fmt.Sprintf("%s loaded with no CSP violation in the console", pageURL))
	} else {
		rep.fail("contribute-page-csp", fmt.Sprintf("%d CSP violation(s) in the console: %s",
			len(violations), strings.Join(violations, " | ")))
	}

	// Check (b): the runner page serves the CSP header/meta, and the inline
	// script hash of the CSP matches the body the browser hashes.
	body, header, err := fetchPage(ctx, runnerURL)
	if err != nil {
		rep.fail("runner-page-csp", fmt.Sprintf("the runner page could not be fetched: %v", err))
	} else {
		verdict := checkCSPHash(body)
		switch {
		case !verdict.Present:
			rep.fail("runner-page-csp", verdict.Detail)
		case verdict.Match:
			rep.pass("runner-page-csp", fmt.Sprintf(
				"the runner page pins the sha256 the browser computes over the inline script (sha256-%s); the response CSP header is %q",
				verdict.Pinned, header.Get("Content-Security-Policy")))
		default:
			rep.fail("runner-page-csp", verdict.Detail)
		}
	}

	// Check (c): the module Blob Worker and the nested kernel worker start.
	// The state is read from the log text the page already writes: the
	// runner reports the readiness of its workers over the MessagePort and
	// the page logs it.
	reportWorkerState(rep, b, handshake, handshakeSeen)

	// Checks (d) and (e): the REST job with the published reference hash and
	// the SSE task.done event of the job, for every checked pool.
	keys, err := prepareWorkerTokens(ctx, c, o, adminKey)
	if err != nil {
		rep.fail("developer-key-flow", err.Error())
		return nil
	}
	rep.pass("developer-key-flow", fmt.Sprintf(
		"the developer key flow of cmd/pools-e2e created the developer and contributor keys of the pools %s",
		strings.Join(o.Pools, ",")))

	artifactID, err := uploadModule(ctx, c, o, keys.developer)
	if err != nil {
		rep.fail("module-upload", err.Error())
		return nil
	}
	rep.pass("module-upload", fmt.Sprintf("the matmul kernel was uploaded as the module artifact %s", artifactID[:12]))

	// The builtin worker settles the job. It is the worker of cmd/pools-e2e,
	// so the settled hash is the reference hash of the workload.
	//
	// The work order asks for the reference hash of size 160 AND of size
	// 240, so every checked pool runs one job per published size.
	for _, pool := range o.Pools {
		prefix := poolPrefix[pool]
		// The token maps are keyed by the pool id, because the run loop
		// walks the pool ids. They used to be keyed by the route prefix, so
		// the lookup answered an empty string and the job was submitted
		// without a bearer: the server answered 401.
		token := keys.developer[pool]
		// The worker routes are contributor routes: they need the
		// contributor token, not the developer token of the job.
		workerToken := keys.contributor[pool]
		// The SSE stream is opened BEFORE the job is submitted, so the
		// events of the job arrive live.
		watcher := newSSEWatcher()
		watchCtx, watchCancel := context.WithCancel(ctx)
		go c.watch(watchCtx, prefix, token, watcher)

		for _, size := range matmulSizes {
			job, _, err := c.createJob(ctx, prefix, token, artifactID, size,
				fmt.Sprintf("matmul-%d", size),
				fmt.Sprintf("browser-smoke-%s-%d-%d", pool, size, time.Now().UnixNano()))
			if err != nil {
				rep.fail("matmul-job-"+pool, fmt.Sprintf("the n=%d job could not be submitted: %v", size, err))
				continue
			}
			// The worker pulls the lease and settles the result. It runs as
			// a contributor, so it uses the contributor token.
			worker := &builtinWorker{client: c, prefix: prefix, token: workerToken, size: size}
			workerCtx, workerCancel := context.WithCancel(ctx)
			workerErr := make(chan error, 1)
			go func() { workerErr <- worker.runBuiltinWorker(workerCtx, 30*time.Second) }()
			state, err := c.awaitJob(ctx, prefix, token, job.JobID, 45*time.Second)
			workerCancel()
			workerErrValue := <-workerErr
			if err != nil {
				rep.fail("matmul-job-"+pool, fmt.Sprintf("the n=%d job %s did not settle: %v (the worker reported: %v)",
					size, job.JobID, err, workerErrValue))
				continue
			}
			if state != "completed" {
				rep.fail("matmul-job-"+pool, fmt.Sprintf("the n=%d job %s settled in state %q, not completed",
					size, job.JobID, state))
				continue
			}
			items, err := c.results(ctx, prefix, token, job.JobID)
			if err != nil {
				rep.fail("matmul-job-"+pool, fmt.Sprintf("the results of the n=%d job %s could not be read: %v",
					size, job.JobID, err))
				continue
			}
			got := ""
			for _, item := range items {
				if item.Status != "done" {
					continue
				}
				if sum, ok := outputHash(item); ok {
					got = sum
					break
				}
			}
			want, err := referenceHash(refs, size)
			if err != nil {
				rep.fail("matmul-job-"+pool, err.Error())
				continue
			}
			if got == "" {
				rep.fail("matmul-job-"+pool, fmt.Sprintf("the n=%d job %s settled without a done result carrying a hash",
					size, job.JobID))
				continue
			}
			if got != want {
				rep.fail("matmul-job-"+pool, fmt.Sprintf("the n=%d result hash %s does not match the published reference %s",
					size, got, want))
				continue
			}
			rep.pass("matmul-job-"+pool, fmt.Sprintf(
				"the job %s completed and the result hash %s matches the published reference (n=%d)",
				job.JobID, got, size))

			// Check (e): the task.done event of the job on the SSE stream.
			ev, ok := watcher.awaitEvent(job.JobID, "task.done", 20*time.Second)
			if ok {
				rep.pass("sse-task-done-"+pool, fmt.Sprintf(
					"the SSE stream delivered task.done (event %s) for the n=%d job %s", ev.ID, size, job.JobID))
				continue
			}
			seen := []string{}
			for _, e := range watcher.collected() {
				seen = append(seen, e.Type)
			}
			rep.fail("sse-task-done-"+pool, fmt.Sprintf(
				"no task.done event for the n=%d job %s arrived on the SSE stream (the stream delivered: %s)",
				size, job.JobID, strings.Join(seen, ",")))
		}
		watchCancel()
	}
	return nil
}

// reportWorkerState records the verdict of the worker check (c).
//
// The runner reports the readiness of its module Blob Worker and of the
// nested kernel worker over the MessagePort, and the page logs the readiness
// of the runner itself. The harness reads that line, so no test hook is added
// to the platform code.
func reportWorkerState(rep *report, b *browser, handshake string, handshakeSeen bool) {
	st, err := b.readPageState(context.Background())
	if err != nil {
		rep.fail("sandbox-workers", "the page state could not be read: "+err.Error())
		return
	}
	joined := strings.Join(st.LogLines, "\n")
	switch {
	case handshakeSeen:
		rep.pass("sandbox-workers", fmt.Sprintf(
			"the runner iframe started and the sandbox Workers are live: %s", handshake))
	case strings.Contains(joined, "bootstrap refused"):
		rep.fail("sandbox-workers", "the runner refused the bootstrap: the module Blob Worker did not start")
	case strings.Contains(joined, "Runner frame is not the pinned runner.html"):
		rep.fail("sandbox-workers", "the runner frame was not the pinned runner.html: the handshake was refused")
	default:
		rep.fail("sandbox-workers", fmt.Sprintf(
			"the page never reported a ready runner: the module Blob Worker and the nested kernel worker did not start (the page log carries: %s)",
			strings.Join(st.LogLines, " / ")))
	}
}

// poolKeys are the tokens of one pool: the developer token owns the job and
// the contributor token serves the worker routes.
type poolKeys struct {
	developer   map[string]string
	contributor map[string]string
}

// prepareWorkerTokens walks the developer key flow of cmd/pools-e2e: the
// admin key is exchanged for a token, a developer key and a contributor key
// are created with it, and both keys are exchanged for the tokens of every
// checked pool.
//
// The maps are keyed by the POOL ID, not by the route prefix, because the run
// loop walks the pool ids of the checked set.
func prepareWorkerTokens(ctx context.Context, c *client, o options, adminKey string) (poolKeys, error) {
	if strings.TrimSpace(adminKey) == "" {
		return poolKeys{}, fmt.Errorf("no admin API key: pass -admin-key, or start the local server with -start-local")
	}
	prefixes := o.prefixes()
	adminTokens := map[string]string{}
	for _, p := range prefixes {
		token, err := c.exchange(ctx, p, adminKey)
		if err != nil {
			return poolKeys{}, fmt.Errorf("the admin key could not be exchanged on %s: %w", p, err)
		}
		adminTokens[p] = token
	}
	// The developer key owns the job and the artifact; the contributor key
	// serves the worker routes. Both are created on the first pool and
	// exchanged on every pool, exactly like the e2e binary does.
	devKey, err := c.createKey(ctx, prefixes[0], adminTokens[prefixes[0]],
		"browser-smoke-developer-"+fmt.Sprint(time.Now().UnixNano()),
		[]string{"developer"}, []string{"artifacts:write", "jobs:write", "jobs:read"})
	if err != nil {
		return poolKeys{}, fmt.Errorf("the developer key could not be created: %w", err)
	}
	contribKey, err := c.createKey(ctx, prefixes[0], adminTokens[prefixes[0]],
		"browser-smoke-contributor-"+fmt.Sprint(time.Now().UnixNano()),
		[]string{"contributor"}, []string{"worker"})
	if err != nil {
		return poolKeys{}, fmt.Errorf("the contributor key could not be created: %w", err)
	}
	out := poolKeys{developer: map[string]string{}, contributor: map[string]string{}}
	for i, pool := range o.Pools {
		token, err := c.exchange(ctx, prefixes[i], devKey.raw)
		if err != nil {
			return poolKeys{}, fmt.Errorf("the developer key could not be exchanged on %s: %w", pool, err)
		}
		out.developer[pool] = token
		contribToken, err := c.exchange(ctx, prefixes[i], contribKey.raw)
		if err != nil {
			return poolKeys{}, fmt.Errorf("the contributor key could not be exchanged on %s: %w", pool, err)
		}
		out.contributor[pool] = contribToken
	}
	return out, nil
}

// uploadModule uploads the reference matmul kernel as the module artifact of
// the job. The bytes are the shipped workload file, so the artifact the job
// names is the same bytes the workload tree pins.
func uploadModule(ctx context.Context, c *client, o options, tokens map[string]string) (string, error) {
	root, err := repoRoot()
	if err != nil {
		return "", err
	}
	kernelPath, _, err := workloadPaths(root)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(kernelPath)
	if err != nil {
		return "", fmt.Errorf("read the reference kernel: %w", err)
	}
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:])
	// The token map is keyed by the pool id.
	prefix := o.prefixes()[0]
	status, err := c.uploadArtifact(ctx, prefix, tokens[o.Pools[0]], data, id)
	if err != nil {
		return "", fmt.Errorf("upload the module artifact: %w", err)
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return "", fmt.Errorf("upload the module artifact: HTTP %d", status)
	}
	return id, nil
}
