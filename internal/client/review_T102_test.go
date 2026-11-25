// Path: internal/client/review_T102_test.go | Author: Copilot | Date: 2026-11-20
// What/why: Regression coverage for the T102 ChatGPT review findings: plain
// http credential exposure and cross-host Authorization forwarding, the
// unsynchronized token cache, the CLI-size worker bug, the missing
// heartbeat/lease renewal, and the early-ending watch streams. All servers
// are in-process (httptest / h2c); no internet access is needed.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"ccgsc/poolscore/internal/grpcapi"
	"ccgsc/poolscore/internal/pool"
)

// Finding 1 (HIGH), part a: the API key and bearer tokens must never cross a
// plain http connection to a non-loopback host.
func TestNewRefusesPlainHTTPOffLoopback(t *testing.T) {
	for _, base := range []string{"http://203.0.113.5:8091", "http://pools.example.com:8091"} {
		if _, err := New(base, "k", false); err == nil {
			t.Errorf("New(%q, REST) accepted plain http off loopback", base)
		}
		if _, err := New(base, "k", true); err == nil {
			t.Errorf("New(%q, gRPC) accepted plain http off loopback", base)
		}
	}
	for _, base := range []string{"http://localhost:8091", "http://127.0.0.1:8091", "http://[::1]:8091", "https://pools.example.com", "https://127.0.0.1:8091"} {
		if _, err := New(base, "k", false); err != nil {
			t.Errorf("New(%q): %v", base, err)
		}
	}
	if _, err := New("ftp://127.0.0.1:8091", "k", false); err == nil {
		t.Error("non-http(s) scheme accepted")
	}
	if _, err := New("http://127.0.0.1:8091", "   ", false); err == nil {
		t.Error("empty API key accepted")
	}
}

// Finding 1 (HIGH), part b: Authorization must never follow a redirect to
// another host, but a same-host redirect keeps it.
func TestAuthorizationNeverFollowsCrossHostRedirect(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	inner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen["inner"+r.URL.Path] = r.Header.Get("Authorization")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "{}")
	}))
	defer inner.Close()
	outer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/auth/token"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"accessToken":"tok-1","tokenType":"Bearer","expiresIn":900}`)
		case strings.HasSuffix(r.URL.Path, "/capabilities"):
			// httptest servers differ by port, so inner.URL is another host.
			location := inner.URL + "/landing"
			if r.URL.Query().Get("same") == "1" {
				location = "/landing" // relative: same host as the request
			}
			w.Header().Set("Location", location)
			w.WriteHeader(http.StatusFound)
		default:
			mu.Lock()
			seen["outer"+r.URL.Path] = r.Header.Get("Authorization")
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "{}")
		}
	}))
	defer outer.Close()

	c, err := New(outer.URL, "key-1", false)
	if err != nil {
		t.Fatal(err)
	}
	// Cross-host redirect: the bearer token must not travel to the other host.
	if _, err := c.Capabilities(context.Background(), Thread); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	leaked := seen["inner/landing"]
	mu.Unlock()
	if leaked != "" {
		t.Fatalf("Authorization leaked across a redirect to another host: %q", leaked)
	}
	// Same-host redirect: the bearer token still applies.
	if _, err := c.rest(context.Background(), http.MethodGet, "/thread-pool/v1/capabilities?same=1", "tok-1", nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	same := seen["outer/landing"]
	mu.Unlock()
	if same != "Bearer tok-1" {
		t.Fatalf("Authorization dropped on a same-host redirect: %q", same)
	}
}

// Finding 2 (HIGH): the token cache is shared by goroutines (the "both" pools
// worker runs one per pool). The race detector proves the mutex; the counter
// proves concurrent cold starts exchange exactly once per pool.
func TestTokenCacheIsRaceFreeAndSingleFlight(t *testing.T) {
	newFake := func(t *testing.T) (*Client, func() (int, int)) {
		var mu sync.Mutex
		thread, process := 0, 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/auth/token") {
				mu.Lock()
				if strings.HasPrefix(r.URL.Path, "/thread-pool") {
					thread++
				} else {
					process++
				}
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"accessToken":"tok","tokenType":"Bearer","expiresIn":900}`)
				return
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(srv.Close)
		c, err := New(srv.URL, "key-1", false)
		if err != nil {
			t.Fatal(err)
		}
		return c, func() (int, int) {
			mu.Lock()
			defer mu.Unlock()
			return thread, process
		}
	}

	t.Run("cached tokens do not race and cold start exchanges once per pool", func(t *testing.T) {
		c, counts := newFake(t)
		var wg sync.WaitGroup
		for i := 0; i < 32; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				p := Thread
				if i%2 == 1 {
					p = Process
				}
				if tok, err := c.token(context.Background(), p); err != nil || tok == "" {
					t.Errorf("token(%s): %q, %v", p, tok, err)
				}
			}(i)
		}
		wg.Wait()
		thread, process := counts()
		if thread != 1 || process != 1 {
			t.Fatalf("token exchanges: thread=%d process=%d, want 1 each", thread, process)
		}
	})

	t.Run("concurrent invalidation and refresh stay race free", func(t *testing.T) {
		c, _ := newFake(t)
		var wg sync.WaitGroup
		for i := 0; i < 24; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				p := Thread
				if i%2 == 1 {
					p = Process
				}
				if i%3 == 0 {
					c.invalidateToken(p)
				}
				if tok, err := c.token(context.Background(), p); err != nil || tok == "" {
					t.Errorf("token(%s): %q, %v", p, tok, err)
				}
			}(i)
		}
		wg.Wait()
	})
}

// Finding 3 (HIGH): the worker computes from the leased task payload. One
// worker runs two jobs submitted with different sizes; every result must
// carry the n the server assigned, not a CLI flag.
func TestWorkerComputesAssignedTaskSize(t *testing.T) {
	h := newClientHarness(t, false)
	dev, err := New(h.rest.URL, h.developer, false)
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	worker, err := New(h.rest.URL, h.contributor, false)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	jobA, err := dev.Submit(ctx, Thread, 8)
	if err != nil {
		t.Fatalf("submit A: %v", err)
	}
	jobB, err := dev.Submit(ctx, Thread, 12)
	if err != nil {
		t.Fatalf("submit B: %v", err)
	}
	wctx, wcancel := context.WithCancel(context.Background())
	defer wcancel()
	go func() { _ = worker.Worker(wctx, Thread, 1) }()
	waitForCompleted(t, dev, Thread, jobA)
	waitForCompleted(t, dev, Thread, jobB)

	for _, tc := range []struct {
		id   string
		want int
	}{{jobA, 8}, {jobB, 12}} {
		raw, err := dev.Results(context.Background(), Thread, tc.id)
		if err != nil {
			t.Fatalf("results %s: %v", tc.id, err)
		}
		var page struct {
			Items []struct {
				Status string          `json:"status"`
				Output json.RawMessage `json:"output"`
			} `json:"items"`
		}
		if err := json.Unmarshal(raw.([]byte), &page); err != nil {
			t.Fatalf("results %s: %v", tc.id, err)
		}
		if len(page.Items) == 0 {
			t.Fatalf("no results for job %s", tc.id)
		}
		for _, item := range page.Items {
			if item.Status != "done" {
				t.Fatalf("result status %q for job %s", item.Status, tc.id)
			}
			// The REST view wraps a plain object output as {"json": {...}}.
			var out struct {
				Json struct {
					N int `json:"n"`
				} `json:"json"`
			}
			if err := json.Unmarshal(item.Output, &out); err != nil {
				t.Fatalf("result output: %v", err)
			}
			if out.Json.N != tc.want {
				t.Fatalf("result n = %d, want the assigned size %d", out.Json.N, tc.want)
			}
		}
	}
}

// Finding 4 (HIGH): the worker keeps heartbeating and renews its lease before
// it expires. The fake server grants a 1s lease and holds the results call
// until a renewal heartbeat arrives, so the renewal must happen on time.
func TestWorkerHeartbeatsAndRenewsLeaseOnTime(t *testing.T) {
	var mu sync.Mutex
	plainHeartbeats, renewals := 0, 0
	var renewalAge time.Duration
	var resultBody []byte
	renewObserved := make(chan struct{})
	var once sync.Once
	// submitDone closes when the worker claims again after its result: the
	// submit round trip is then complete and the scenario can end (AT22-L3-4).
	submitDone := make(chan struct{})
	var submitOnce sync.Once

	grant := time.Now().UTC() // the lease the server will hand out
	assignment, err := json.Marshal(pool.Assignment{
		Lease: pool.Lease{
			JobID: "job-1", TaskID: "matmul", WorkerID: "w1", Token: "lt-1",
			Generation: 1, Attempt: 1,
			StartedAt: grant, ExpiresAt: grant.Add(time.Second),
		},
		Task:              pool.TaskSpec{TaskID: "matmul", Input: json.RawMessage(`{"json":{"kind":"matmul-f32-gpu","n":10}}`)},
		ModuleArtifactIDs: []string{"art-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	claims := 0

	mux := http.NewServeMux()
	mux.HandleFunc("POST /thread-pool/v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"accessToken":"tok-1","tokenType":"Bearer","expiresIn":900}`)
	})
	mux.HandleFunc("POST /thread-pool/v1/workers/register", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"tabId":"w1","hostId":"pools-client","slots":1}`)
	})
	mux.HandleFunc("POST /thread-pool/v1/workers/w1/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var q struct {
			Lease *pool.Lease `json:"lease"`
		}
		_ = json.Unmarshal(body, &q)
		mu.Lock()
		if q.Lease != nil && q.Lease.Token == "lt-1" {
			renewals++
			if renewalAge == 0 {
				renewalAge = time.Since(grant)
			}
			mu.Unlock()
			once.Do(func() { close(renewObserved) })
			return
		}
		plainHeartbeats++
		mu.Unlock()
		fmt.Fprint(w, `{"tabId":"w1"}`)
	})
	mux.HandleFunc("POST /thread-pool/v1/workers/w1/leases", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		claims++
		n := claims
		mu.Unlock()
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.Write(assignment)
			return
		}
		if n == 2 {
			// The worker only claims again after its submit returned, so the
			// leased task is complete and the test can stop the loop.
			submitOnce.Do(func() { close(submitDone) })
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /thread-pool/v1/workers/w1/results", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		select {
		case <-renewObserved:
		case <-time.After(5 * time.Second):
			http.Error(w, "lease was never renewed in time", http.StatusInternalServerError)
			return
		}
		mu.Lock()
		resultBody = body
		mu.Unlock()
		fmt.Fprint(w, `{"taskId":"matmul","status":"done"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := New(srv.URL, "key-1", false)
	if err != nil {
		t.Fatal(err)
	}
	// AT22-L3-4: the worker loop only ends through its context, so the test
	// stops it once the scenario is complete. The fixed 4 s deadline used to
	// cut the work short under parallel CPU load ("context deadline exceeded"):
	// under load the deadline landed inside a request instead of the idle poll
	// sleep. The run is now bounded by observed progress — the submit ends
	// only after the held results call saw a renewal — and the deadline below
	// is a safety net. The renewal assertions are unchanged.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	workerErr := make(chan error, 1)
	go func() { workerErr <- c.Worker(ctx, Thread, 1) }()
	select {
	case <-submitDone:
	case <-time.After(20 * time.Second):
		t.Fatal("worker never completed the leased task")
	}
	cancel()
	select {
	case err := <-workerErr:
		// A cancelled poll is a clean stop of the loop; any other worker
		// error (a rejected submit, a failed heartbeat round) still fails.
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("worker: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not stop after cancel")
	}
	mu.Lock()
	gotRenewals, gotPlain, body, age := renewals, plainHeartbeats, resultBody, renewalAge
	mu.Unlock()
	if gotRenewals < 1 {
		t.Fatal("lease was never renewed")
	}
	if age >= time.Second {
		t.Fatalf("renewal arrived %v after grant, past the 1s lease expiry", age)
	}
	if gotPlain < 1 {
		t.Fatal("worker sent no plain keep-alive heartbeat while idle")
	}
	var sub pool.Submission
	if err := json.Unmarshal(body, &sub); err != nil {
		t.Fatalf("submission: %v", err)
	}
	var out struct {
		N int `json:"n"`
	}
	if err := json.Unmarshal(sub.Output, &out); err != nil {
		t.Fatalf("submission output: %v", err)
	}
	if out.N != 10 {
		t.Fatalf("output n = %d, want the assigned 10", out.N)
	}
	if sub.SHA256 != shaJSON(sub.Output) {
		t.Fatal("submission sha256 does not match its output bytes")
	}
}

// Finding 5 (MED), REST: the watch reconnects with backoff, resumes from the
// Last-Event-ID, refreshes the token after the reauth frame, keeps watching
// past task.done, and ends at the terminal job event.
func TestWatchSSEReconnectsResumesAndRefreshesToken(t *testing.T) {
	var mu sync.Mutex
	conns, tokens := 0, 0
	auths := map[int]string{}
	lastIDs := map[int]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/auth/token"):
			mu.Lock()
			tokens++
			n := tokens
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"accessToken":"tok-%d","tokenType":"Bearer","expiresIn":900}`, n)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/events"):
			mu.Lock()
			conns++
			n := conns
			auths[n] = r.Header.Get("Authorization")
			lastIDs[n] = r.Header.Get("Last-Event-ID")
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			f := w.(http.Flusher)
			if n == 1 {
				// A task event, then the reauth frame, then the server closes
				// the stream: an early-ending watch stops here for good.
				fmt.Fprint(w, "id: evt-1\nevent: task.done\ndata: {\"id\":\"evt-1\",\"type\":\"task.done\",\"jobId\":\"j1\"}\n\n")
				f.Flush()
				fmt.Fprint(w, "event: reauth\ndata: {\"reason\":\"token_expiring\",\"action\":\"refresh_and_reconnect\"}\n\n")
				f.Flush()
				return
			}
			fmt.Fprint(w, "id: evt-2\nevent: job.done\ndata: {\"id\":\"evt-2\",\"type\":\"job.done\",\"jobId\":\"j1\"}\n\n")
			f.Flush()
			return
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL, "key-1", false)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	var emitMu sync.Mutex
	err = c.Watch(context.Background(), Thread, "j1", func(v any) error {
		m, _ := v.(map[string]any)
		typ, _ := m["type"].(string)
		emitMu.Lock()
		seen = append(seen, typ)
		emitMu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	mu.Lock()
	gotConns, auth2, resume := conns, auths[2], lastIDs[2]
	mu.Unlock()
	emitMu.Lock()
	defer emitMu.Unlock()
	if gotConns != 2 {
		t.Fatalf("watch made %d connections, want a reconnect (2)", gotConns)
	}
	if auth2 != "Bearer tok-2" {
		t.Fatalf("reconnect carried %q, want a refreshed token", auth2)
	}
	if resume != "evt-1" {
		t.Fatalf("reconnect carried Last-Event-ID %q, want evt-1", resume)
	}
	joined := strings.Join(seen, ",")
	if !strings.Contains(joined, "task.done") || !strings.Contains(joined, "job.done") {
		t.Fatalf("watch saw [%s]; it must run past task.done to job.done", joined)
	}
}

// Finding 5 (MED), REST: Ctrl+C stops the watch cleanly and leaks no
// goroutine from the client or its stream connection.
func TestWatchStopsCleanlyOnCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/auth/token") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"accessToken":"tok-1","tokenType":"Bearer","expiresIn":900}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		f.Flush()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
					return
				}
				f.Flush()
			}
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL, "key-1", false)
	if err != nil {
		t.Fatal(err)
	}
	baseline := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- c.Watch(ctx, Thread, "j1", func(any) error { return nil })
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancelled watch returned %v, want a clean nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not stop on Ctrl+C")
	}
	time.Sleep(400 * time.Millisecond)
	if n := runtime.NumGoroutine(); n > baseline+4 {
		t.Fatalf("goroutine leak: %d before, %d after the cancelled watch", baseline, n)
	}
}

// Finding 5 (MED), gRPC: the stream runs past task.done to the terminal job
// event instead of ending early, against the real h2c gRPC server.
func TestWatchGRPCStreamRunsToJobDone(t *testing.T) {
	h := newClientHarness(t, true)
	dev, err := New(h.grpc.URL, h.developer, true)
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	worker, err := New(h.rest.URL, h.contributor, false)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	jobID, err := dev.Submit(ctx, Thread, 8)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	var mu sync.Mutex
	var seen []string
	wctx, wcancel := context.WithCancel(context.Background())
	defer wcancel()
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- dev.Watch(wctx, Thread, jobID, func(v any) error {
			if m, ok := v.(*grpcapi.EventMessage); ok {
				mu.Lock()
				seen = append(seen, m.GetType())
				mu.Unlock()
			}
			return nil
		})
	}()
	kctx, kcancel := context.WithCancel(context.Background())
	defer kcancel()
	go func() { _ = worker.Worker(kctx, Thread, 1) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		joined := strings.Join(seen, ",")
		mu.Unlock()
		if strings.Contains(joined, "job.done") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("watch never saw job.done; saw [%s]", joined)
		}
		time.Sleep(20 * time.Millisecond)
	}
	wcancel()
	select {
	case err := <-watchDone:
		if err != nil {
			t.Fatalf("watch: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not return after the terminal event")
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(seen, ",")
	if !strings.Contains(joined, "task.done") || !strings.Contains(joined, "job.done") {
		t.Fatalf("watch saw [%s]; it must run past task.done to job.done", joined)
	}
}
