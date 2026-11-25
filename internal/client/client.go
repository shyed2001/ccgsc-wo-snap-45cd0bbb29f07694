// Path: internal/client/client.go | Author: Copilot | Date: 2026-11-20
// What/why: Shared REST and gRPC client for the pools-client CLI, including
// authenticated jobs, event watching, and the CPU worker loop.
//
// T102 review fixes (2026-11-20):
//   - The API key and every bearer token stay off the wire unless the
//     connection is TLS or the host is loopback; Authorization never follows
//     a redirect to another host (or an https->http downgrade).
//   - The token cache is guarded by a mutex (two worker goroutines share the
//     client) and refreshes itself before the server-side expiry.
//   - The worker computes exactly the leased task payload, not a CLI flag,
//     and keeps its lease alive with timely heartbeats and renewals.
//   - Watch reconnects with backoff and jitter, honours Last-Event-ID,
//     refreshes the token on reauth, and stops cleanly on Ctrl+C.
//
// T101 fix (2026-11-21): REST error bodies are redacted against the API key
// and the bearer token before they become error text, so a server that
// echoes a request back on an error page cannot leak credentials through the
// CLI's error output.
package client

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"ccgsc/poolscore/internal/grpcapi"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/redact"
	"ccgsc/poolscore/internal/verify"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

type Pool string

const (
	Thread  Pool = "thread"
	Process Pool = "process"
)

func (p Pool) prefix() string { return "/" + string(p) + "-pool/v1" }

const (
	// heartbeatInterval is the idle heartbeat cadence. The server reaps a tab
	// whose heartbeat is older than twice the lease TTL (60s by default), so
	// 5s keeps the tab registered with a wide margin.
	heartbeatInterval = 5 * time.Second
	// watchBackoffBase and watchBackoffMax bound the reconnect backoff of a
	// watch stream; jitter spreads the reconnects out.
	watchBackoffBase = 500 * time.Millisecond
	watchBackoffMax  = 8 * time.Second
	// minHeartbeatInterval floors the heartbeat cadence for tiny leases.
	minHeartbeatInterval = 200 * time.Millisecond
)

// tokenEntry is one cached bearer token with the point at which the client
// wants to refresh it (before the server-side expiry).
type tokenEntry struct {
	value      string
	validUntil time.Time // zero = the server reported no expiry
}

func (e tokenEntry) fresh(now time.Time) bool {
	return e.value != "" && (e.validUntil.IsZero() || now.Before(e.validUntil))
}

// tokenEntryFrom derives the cache entry from the server response. The refresh
// margin is a fifth of the lifetime (45s at the default 15min), so a token is
// always refreshed before it lapses (T102 MED).
func tokenEntryFrom(value string, expiresAt time.Time, expiresIn time.Duration) tokenEntry {
	entry := tokenEntry{value: value}
	ttl := expiresIn
	if !expiresAt.IsZero() {
		remaining := time.Until(expiresAt)
		if ttl <= 0 || remaining < ttl {
			ttl = remaining
		}
	}
	if ttl > 0 {
		margin := ttl / 5
		if margin > 45*time.Second {
			margin = 45 * time.Second
		}
		entry.validUntil = time.Now().Add(ttl - margin)
	}
	return entry
}

type Client struct {
	base       string
	key        string
	http       *http.Client
	streamHTTP *http.Client // no overall deadline: SSE ends by context, not timeout
	grpc       *grpc.ClientConn
	thread     grpcapi.ThreadPoolClient
	process    grpcapi.ProcessPoolClient

	tokenMu sync.Mutex           // guards the token cache (T102 HIGH: shared by goroutines)
	tokens  map[Pool]tokenEntry  // cached bearer token per pool
	fetchMu map[Pool]*sync.Mutex // one exchange at a time per pool
}

// isLoopbackHost reports whether host is loopback: a 127.0.0.0/8 or ::1
// literal, or the localhost name. Plain http is accepted only for these.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// redirectPolicy lets same-host redirects through and strips the Authorization
// header everywhere else (T102 HIGH): a redirect to another host, or a fall
// back to plain http, must never receive the bearer token. A redirect that
// would still carry a request body to another host is refused, so an API key
// in a body can never follow one either.
func redirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if len(via) == 0 {
		return nil
	}
	prev := via[len(via)-1]
	crossHost := !strings.EqualFold(req.URL.Host, prev.URL.Host)
	downgrade := prev.URL.Scheme == "https" && req.URL.Scheme != "https"
	if crossHost {
		req.Header.Del("Authorization")
		if req.Body != nil {
			return fmt.Errorf("refusing to send the request body to another host on redirect (%s -> %s)", prev.URL.Host, req.URL.Host)
		}
	}
	if downgrade {
		req.Header.Del("Authorization")
	}
	return nil
}

// New builds a client. The base URL must be https unless the host is loopback
// (127.0.0.0/8, ::1, localhost): the API key and every bearer token would
// otherwise cross the network in cleartext (T102 HIGH).
func New(base, key string, useGRPC bool) (*Client, error) {
	if strings.TrimSpace(base) == "" || strings.TrimSpace(key) == "" {
		return nil, errors.New("base URL and API key are required")
	}
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("invalid base URL: need an http(s) URL with a host")
	}
	if u.Scheme != "https" && !isLoopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("refusing plain %s to non-loopback host %q: the API key and bearer tokens would be sent in cleartext; use https or a loopback host (127.0.0.1, ::1, localhost)", u.Scheme, u.Host)
	}
	c := &Client{
		base:       strings.TrimRight(base, "/"),
		key:        key,
		http:       &http.Client{Timeout: 2 * time.Minute, CheckRedirect: redirectPolicy},
		streamHTTP: &http.Client{CheckRedirect: redirectPolicy},
		tokens:     make(map[Pool]tokenEntry),
		fetchMu:    make(map[Pool]*sync.Mutex),
	}
	if !useGRPC {
		return c, nil
	}
	var creds credentials.TransportCredentials
	if u.Scheme == "https" {
		creds = credentials.NewTLS(&tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12})
	} else {
		creds = insecure.NewCredentials()
	}
	conn, err := grpc.NewClient(u.Host, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("connect gRPC: %w", err)
	}
	c.grpc = conn
	c.thread, c.process = grpcapi.NewThreadPoolClient(conn), grpcapi.NewProcessPoolClient(conn)
	return c, nil
}

func (c *Client) Close() error {
	if c.grpc != nil {
		return c.grpc.Close()
	}
	return nil
}

// token returns a cached bearer token for the pool or exchanges the API key
// for a new one. The cache is guarded by a mutex (T102 HIGH: two worker
// goroutines run against the same client), and each pool exchanges under its
// own lock so concurrent cold starts do not duplicate requests.
func (c *Client) token(ctx context.Context, p Pool) (string, error) {
	now := time.Now()
	c.tokenMu.Lock()
	entry := c.tokens[p]
	mu, ok := c.fetchMu[p]
	if !ok {
		mu = &sync.Mutex{}
		c.fetchMu[p] = mu
	}
	c.tokenMu.Unlock()
	if entry.fresh(now) {
		return entry.value, nil
	}
	mu.Lock()
	defer mu.Unlock()
	// Double-check: another goroutine may have refreshed while we waited.
	c.tokenMu.Lock()
	entry = c.tokens[p]
	c.tokenMu.Unlock()
	if entry.fresh(time.Now()) {
		return entry.value, nil
	}
	value, entry, err := c.exchange(ctx, p)
	if err != nil {
		return "", err
	}
	c.tokenMu.Lock()
	c.tokens[p] = entry
	c.tokenMu.Unlock()
	return value, nil
}

// invalidateToken drops the cached token of the pool so the next call
// exchanges again. Used on reauth frames and 401 responses.
func (c *Client) invalidateToken(p Pool) {
	c.tokenMu.Lock()
	delete(c.tokens, p)
	c.tokenMu.Unlock()
}

// exchange fetches a fresh bearer token over REST or gRPC.
func (c *Client) exchange(ctx context.Context, p Pool) (string, tokenEntry, error) {
	if c.grpc == nil {
		raw, err := c.rest(ctx, http.MethodPost, p.prefix()+"/auth/token", "", map[string]any{"apiKey": c.key})
		if err != nil {
			return "", tokenEntry{}, err
		}
		var out struct {
			AccessToken string    `json:"accessToken"`
			ExpiresIn   int       `json:"expiresIn"`
			ExpiresAt   time.Time `json:"expiresAt"`
		}
		if err := json.Unmarshal(raw, &out); err != nil || out.AccessToken == "" {
			return "", tokenEntry{}, errors.New("invalid token response")
		}
		entry := tokenEntryFrom(out.AccessToken, out.ExpiresAt, time.Duration(out.ExpiresIn)*time.Second)
		return out.AccessToken, entry, nil
	}
	var out *grpcapi.TokenResponse
	var err error
	req := &grpcapi.TokenRequest{ApiKey: c.key}
	if p == Thread {
		out, err = c.thread.ExchangeToken(ctx, req)
	} else {
		out, err = c.process.ExchangeToken(ctx, req)
	}
	if err != nil {
		return "", tokenEntry{}, err
	}
	if out.GetAccessToken() == "" {
		return "", tokenEntry{}, errors.New("empty gRPC token response")
	}
	var ttl time.Duration
	if out.GetExpiresIn() > 0 {
		ttl = time.Duration(out.GetExpiresIn()) * time.Second
	}
	var expiresAt time.Time
	if ts := out.GetExpiresAt(); ts != nil {
		expiresAt = ts.AsTime()
	}
	entry := tokenEntryFrom(out.GetAccessToken(), expiresAt, ttl)
	return out.GetAccessToken(), entry, nil
}

// httpError carries the HTTP status of a failed REST call so callers can tell
// permanent rejections from transient ones.
type httpError struct {
	path   string
	status int
	body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("%s: HTTP %d: %s", e.path, e.status, strings.TrimSpace(e.body))
}

// statusOf returns the HTTP status of a REST error, or 0 for transport errors.
func statusOf(err error) int {
	var he *httpError
	if errors.As(err, &he) {
		return he.status
	}
	return 0
}

// problemText reads the additive message/hint of a problem body (WO
// R17-ENVELOPE) and falls back to detail for a server that predates them. A
// body that is not the envelope is passed through unchanged.
func problemText(raw []byte) string {
	var problem struct {
		Message string `json:"message"`
		Hint    string `json:"hint"`
		Detail  string `json:"detail"`
	}
	if err := json.Unmarshal(raw, &problem); err != nil {
		return string(raw)
	}
	text := problem.Message
	if text == "" {
		text = problem.Detail
	}
	if text == "" {
		return string(raw)
	}
	if problem.Hint != "" {
		text += " (" + problem.Hint + ")"
	}
	return text
}

func (c *Client) Capabilities(ctx context.Context, p Pool) (any, error) {
	token, err := c.token(ctx, p)
	if err != nil {
		return nil, err
	}
	if c.grpc == nil {
		return c.rest(ctx, http.MethodGet, p.prefix()+"/capabilities", token, nil)
	}
	ctx = grpcapi.WithBearer(ctx, token)
	if p == Thread {
		return c.thread.GetCapabilities(ctx, &grpcapi.GetCapabilitiesRequest{})
	}
	return c.process.GetCapabilities(ctx, &grpcapi.GetCapabilitiesRequest{})
}

func (c *Client) Submit(ctx context.Context, p Pool, size int) (string, error) {
	if size <= 0 {
		return "", errors.New("size must be positive")
	}
	token, err := c.token(ctx, p)
	if err != nil {
		return "", err
	}
	artifact := []byte("pools-client artifact")
	artifactID := verify.SHA256(artifact)
	if c.grpc == nil {
		if _, err := c.restWithHeaders(ctx, http.MethodPut, p.prefix()+"/artifacts", token, artifact, map[string]string{
			"Content-Type": "application/octet-stream", "X-Artifact-Sha256": artifactID,
			"Upload-Offset": "0", "Upload-Length": fmt.Sprint(len(artifact)),
		}); err != nil {
			return "", err
		}
		zero := 0.0
		body := pool.CreateRequest{ModuleArtifactIDs: []string{artifactID}, Tasks: []pool.TaskSpec{{TaskID: "matmul", Input: json.RawMessage(fmt.Sprintf(`{"json":{"kind":"matmul-f32-gpu","n":%d}}`, size))}}, BatchSize: 1, Needs: "cpu", RerunPercent: &zero}
		raw, err := c.rest(ctx, http.MethodPost, p.prefix()+"/jobs", token, body)
		if err != nil {
			return "", err
		}
		var job pool.Job
		if err := json.Unmarshal(raw, &job); err != nil {
			return "", err
		}
		return job.JobID, nil
	}
	gctx := grpcapi.WithBearer(ctx, token)
	req := &grpcapi.ArtifactUploadRequest{Sha256: artifactID, UploadLength: int64(len(artifact)), Chunk: artifact, ContentType: "application/octet-stream"}
	if p == Thread {
		if _, err = c.thread.UploadArtifact(gctx, req); err != nil {
			return "", err
		}
	} else if _, err = c.process.UploadArtifact(gctx, req); err != nil {
		return "", err
	}
	input, _ := structpb.NewStruct(map[string]any{"kind": "matmul-f32-gpu", "n": float64(size)})
	needs, batch, rerun := "cpu", int64(1), float64(0)
	reqJob := &grpcapi.CreateJobRequest{ModuleArtifactIds: []string{artifactID}, Tasks: []*grpcapi.TaskSpec{{TaskId: "matmul", Input: &grpcapi.TaskInput{Source: &grpcapi.TaskInput_Json{Json: input}}}}, BatchSize: &batch, Needs: &needs, RerunPercent: &rerun, IdempotencyKey: "pools-client-" + fmt.Sprint(time.Now().UnixNano())}
	if p == Thread {
		job, err := c.thread.CreateJob(gctx, reqJob)
		if err != nil {
			return "", err
		}
		return job.GetJobId(), nil
	}
	job, err := c.process.CreateJob(gctx, reqJob)
	if err != nil {
		return "", err
	}
	return job.GetJobId(), nil
}

func (c *Client) Status(ctx context.Context, p Pool, jobID string) (any, error) {
	token, err := c.token(ctx, p)
	if err != nil {
		return nil, err
	}
	if c.grpc == nil {
		return c.rest(ctx, http.MethodGet, p.prefix()+"/jobs/"+url.PathEscape(jobID), token, nil)
	}
	gctx := grpcapi.WithBearer(ctx, token)
	req := &grpcapi.GetJobRequest{JobId: jobID}
	if p == Thread {
		return c.thread.GetJob(gctx, req)
	}
	return c.process.GetJob(gctx, req)
}

func (c *Client) Results(ctx context.Context, p Pool, jobID string) (any, error) {
	token, err := c.token(ctx, p)
	if err != nil {
		return nil, err
	}
	if c.grpc == nil {
		return c.rest(ctx, http.MethodGet, p.prefix()+"/jobs/"+url.PathEscape(jobID)+"/results", token, nil)
	}
	gctx := grpcapi.WithBearer(ctx, token)
	req := &grpcapi.GetResultsBulkRequest{JobId: jobID}
	if p == Thread {
		return c.thread.GetResultsBulk(gctx, req)
	}
	return c.process.GetResultsBulk(gctx, req)
}

// watchTerminalTypes are the event types that end a job watch. Task events
// never end the watch: a batched or re-run job emits several of them (T102
// MED: the watch used to stop at the first task.done).
var watchTerminalTypes = map[string]bool{"job.done": true, "job.failed": true}

// Watch streams the events of one job until the job reaches a terminal state
// or the context is cancelled (Ctrl+C returns nil). Both transports reconnect
// with backoff and jitter and resume from the last seen event id (T102 MED).
func (c *Client) Watch(ctx context.Context, p Pool, jobID string, emit func(any) error) error {
	if c.grpc != nil {
		return c.watchGRPC(ctx, p, jobID, emit)
	}
	return c.watchSSE(ctx, p, jobID, emit)
}

// watchSSE runs the reconnect loop of the REST event stream.
func (c *Client) watchSSE(ctx context.Context, p Pool, jobID string, emit func(any) error) error {
	lastEventID := ""
	attempt := 0
	authFailures := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		token, err := c.token(ctx, p)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var he *httpError
			if errors.As(err, &he) && (he.status == http.StatusUnauthorized || he.status == http.StatusForbidden) {
				return fmt.Errorf("events: token exchange rejected: %w", err)
			}
			if !sleepCtx(ctx, backoffDelay(attempt, watchBackoffBase, watchBackoffMax)) {
				return nil
			}
			attempt++
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+p.prefix()+"/events?jobIds="+url.QueryEscape(jobID), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if lastEventID != "" {
			req.Header.Set("Last-Event-ID", lastEventID)
		}
		res, err := c.streamHTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if !sleepCtx(ctx, backoffDelay(attempt, watchBackoffBase, watchBackoffMax)) {
				return nil
			}
			attempt++
			continue
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			res.Body.Close()
			switch {
			case res.StatusCode == http.StatusUnauthorized:
				// One refresh-and-retry; a revoked key gives up instead of
				// hammering the exchange endpoint forever.
				authFailures++
				if authFailures >= 3 {
					return fmt.Errorf("events: HTTP 401 with a fresh token")
				}
				c.invalidateToken(p)
			case res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500:
				// retryable
			default:
				// A bad job id or a revoked key cannot recover by retrying.
				return fmt.Errorf("events: HTTP %d", res.StatusCode)
			}
			if !sleepCtx(ctx, backoffDelay(attempt, watchBackoffBase, watchBackoffMax)) {
				return nil
			}
			attempt++
			continue
		}
		attempt = 0
		authFailures = 0
		stop, err := c.readSSE(ctx, p, res.Body, &lastEventID, emit)
		res.Body.Close()
		if err != nil {
			return err
		}
		if stop || ctx.Err() != nil {
			return nil
		}
		// The stream ended without a terminal event (reauth, idle close,
		// queue overflow): reconnect and resume from the last event id.
		if !sleepCtx(ctx, backoffDelay(attempt, watchBackoffBase, watchBackoffMax)) {
			return nil
		}
		attempt++
	}
}

// readSSE consumes one SSE connection, tracking the event id and refreshing
// the token on the reauth frame. It reports stop=true when the watch saw a
// terminal job event.
func (c *Client) readSSE(ctx context.Context, p Pool, body io.Reader, lastEventID *string, emit func(any) error) (stop bool, err error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var event string
	var data []string
	// dispatch finishes one frame (a blank line ends it).
	dispatch := func() (terminal bool, derr error) {
		payload := strings.Join(data, "\n")
		kind := event
		event, data = "", nil
		if kind == "reauth" {
			// The server ends the stream before the token expires: drop the
			// cache so the reconnect carries a fresh bearer token (T102 MED).
			c.invalidateToken(p)
			return false, nil
		}
		if payload == "" {
			return false, nil
		}
		var v map[string]any
		if json.Unmarshal([]byte(payload), &v) != nil {
			return false, nil
		}
		if err := emit(v); err != nil {
			return false, err
		}
		return watchTerminalTypes[kind], nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			terminal, derr := dispatch()
			if derr != nil {
				return false, derr
			}
			if terminal {
				return true, nil
			}
		case strings.HasPrefix(line, ":"):
			// comment / keepalive
		case strings.HasPrefix(line, "id:"):
			if id := strings.TrimSpace(strings.TrimPrefix(line, "id:")); id != "" {
				*lastEventID = id
			}
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return false, nil // Ctrl+C while reading: clean stop
		}
		return false, err
	}
	return false, nil // EOF without terminal event: reconnect
}

// watchGRPC runs the reconnect loop of the gRPC event stream.
func (c *Client) watchGRPC(ctx context.Context, p Pool, jobID string, emit func(any) error) error {
	lastEventID := ""
	attempt := 0
	authFailures := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		token, err := c.token(ctx, p)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if status.Code(err) == codes.Unauthenticated {
				return fmt.Errorf("events: token exchange rejected: %w", err)
			}
			if !sleepCtx(ctx, backoffDelay(attempt, watchBackoffBase, watchBackoffMax)) {
				return nil
			}
			attempt++
			continue
		}
		gctx := grpcapi.WithBearer(ctx, token)
		req := &grpcapi.EventStreamRequest{JobIds: []string{jobID}, LastEventId: lastEventID}
		var stream interface {
			Recv() (*grpcapi.EventMessage, error)
		}
		if p == Thread {
			stream, err = c.thread.StreamEvents(gctx, req)
		} else {
			stream, err = c.process.StreamEvents(gctx, req)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if status.Code(err) == codes.Unauthenticated {
				authFailures++
				if authFailures >= 3 {
					return fmt.Errorf("events: stream rejected with a fresh token: %w", err)
				}
				c.invalidateToken(p)
			}
			if !sleepCtx(ctx, backoffDelay(attempt, watchBackoffBase, watchBackoffMax)) {
				return nil
			}
			attempt++
			continue
		}
		attempt = 0
		authFailures = 0
		stop, err := c.recvEvents(ctx, p, stream, &lastEventID, emit)
		if err != nil {
			return err
		}
		if stop || ctx.Err() != nil {
			return nil
		}
		if !sleepCtx(ctx, backoffDelay(attempt, watchBackoffBase, watchBackoffMax)) {
			return nil
		}
		attempt++
	}
}

// recvEvents drains one gRPC event stream. It reports stop=true when the
// watch saw a terminal job event.
func (c *Client) recvEvents(ctx context.Context, p Pool, stream interface {
	Recv() (*grpcapi.EventMessage, error)
}, lastEventID *string, emit func(any) error) (bool, error) {
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return false, nil // server ended the stream: reconnect and resume
		}
		if err != nil {
			if ctx.Err() != nil {
				return false, nil // Ctrl+C: clean stop
			}
			if status.Code(err) == codes.Unauthenticated {
				c.invalidateToken(p)
			}
			return false, nil // any stream error reconnects with backoff
		}
		if event.GetType() == "reauth" {
			c.invalidateToken(p)
			continue
		}
		if id := event.GetEventId(); id != "" {
			*lastEventID = id
		}
		if err := emit(event); err != nil {
			return false, err
		}
		if watchTerminalTypes[event.GetType()] {
			return true, nil
		}
	}
}

// backoffDelay grows exponentially from base to max and adds jitter in
// [d/2, d), so reconnects spread out instead of stampeding.
func backoffDelay(attempt int, base, max time.Duration) time.Duration {
	d := base
	for i := 0; i < attempt && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	return d/2 + time.Duration(rand.Float64()*float64(d/2))
}

// sleepCtx waits for d or until the context is done; it reports whether the
// full wait completed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// leaseSet tracks the leases the worker currently holds, so the heartbeat loop
// can renew them before they expire (T102 HIGH: an expired lease turns the
// result into tamper evidence on the server).
type leaseSet struct {
	mu     sync.Mutex
	leases []pool.Lease
	minTTL time.Duration
}

func sameLease(a, b pool.Lease) bool {
	return a.JobID == b.JobID && a.TaskID == b.TaskID && a.Generation == b.Generation && a.Token == b.Token
}

func (l *leaseSet) add(lease pool.Lease) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, held := range l.leases {
		if sameLease(held, lease) {
			return
		}
	}
	l.leases = append(l.leases, lease)
	if ttl := lease.ExpiresAt.Sub(lease.StartedAt); ttl > 0 && (l.minTTL == 0 || ttl < l.minTTL) {
		l.minTTL = ttl
	}
}

func (l *leaseSet) drop(lease pool.Lease) {
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.leases[:0]
	for _, held := range l.leases {
		if !sameLease(held, lease) {
			kept = append(kept, held)
		}
	}
	l.leases = kept
}

func (l *leaseSet) snapshot() []pool.Lease {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]pool.Lease(nil), l.leases...)
}

// interval is the heartbeat cadence: at most heartbeatInterval apart, and
// never further than a third of the shortest granted lease, so every held
// lease is renewed long before it expires.
func (l *leaseSet) interval() time.Duration {
	l.mu.Lock()
	minTTL := l.minTTL
	l.mu.Unlock()
	interval := heartbeatInterval
	if minTTL > 0 {
		if want := minTTL / 3; want < interval {
			interval = want
		}
	}
	if interval < minHeartbeatInterval {
		interval = minHeartbeatInterval
	}
	return interval
}

// Worker registers a contributor tab and runs leased tasks until the context
// is cancelled (Ctrl+C returns nil). The computation always follows the leased
// task payload (T102 HIGH): the server, not a CLI flag, defines the work.
func (c *Client) Worker(ctx context.Context, p Pool, slots int) error {
	if c.grpc != nil {
		return errors.New("worker operations use REST; omit -grpc for worker")
	}
	if slots < 1 {
		return errors.New("slots must be positive")
	}
	token, err := c.token(ctx, p)
	if err != nil {
		return err
	}
	raw, err := c.rest(ctx, http.MethodPost, p.prefix()+"/workers/register", token, map[string]any{"hostId": "pools-client", "slots": slots})
	if err != nil {
		return err
	}
	var worker pool.Worker
	if err := json.Unmarshal(raw, &worker); err != nil {
		return err
	}
	if _, err := c.rest(ctx, http.MethodPost, p.prefix()+"/workers/"+url.PathEscape(worker.TabID)+"/heartbeat", token, map[string]any{}); err != nil {
		return err
	}
	// Heartbeat loop: keeps the tab alive and renews every held lease on time
	// (T102 HIGH). It stops when the worker returns, so no goroutine leaks.
	leases := &leaseSet{}
	hbCtx, hbStop := context.WithCancel(ctx)
	defer hbStop()
	go c.heartbeatLoop(hbCtx, p, worker.TabID, leases)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		// A fresh token per round: the cache refreshes before the server-side
		// expiry, so a long-running worker never submits with a lapsed token.
		token, err := c.token(ctx, p)
		if err != nil {
			return err
		}
		raw, err := c.rest(ctx, http.MethodPost, p.prefix()+"/workers/"+url.PathEscape(worker.TabID)+"/leases", token, nil)
		if errors.Is(err, errNoContent) {
			if !sleepCtx(ctx, 25*time.Millisecond) {
				return nil
			}
			continue
		}
		if err != nil {
			return err
		}
		var assignment pool.Assignment
		if err := json.Unmarshal(raw, &assignment); err != nil {
			return err
		}
		items := assignment.Batch
		if len(items) == 0 {
			items = []pool.Assignment{assignment}
		}
		for _, item := range items {
			leases.add(item.Lease)
			err := c.runLease(ctx, p, worker.TabID, token, item)
			leases.drop(item.Lease)
			if err != nil {
				return err
			}
		}
	}
}

// runLease computes and submits one leased task. An assigned task the worker
// cannot run is reported as an error result, so the server can retry or fail
// it instead of receiving bogus output.
func (c *Client) runLease(ctx context.Context, p Pool, tabID, token string, item pool.Assignment) error {
	n, err := assignedMatmulSize(item)
	var sub pool.Submission
	if err != nil {
		sub = pool.Submission{Lease: item.Lease, TaskEnvelope: item.TaskEnvelope, LeaseReceipt: item.LeaseReceipt, Error: err.Error()}
	} else {
		output := matmulOutput(n)
		sub = pool.Submission{Lease: item.Lease, TaskEnvelope: item.TaskEnvelope, LeaseReceipt: item.LeaseReceipt, Output: output, SHA256: shaJSON(output)}
	}
	_, err = c.rest(ctx, http.MethodPost, p.prefix()+"/workers/"+url.PathEscape(tabID)+"/results", token, sub)
	return err
}

// assignedMatmulSize reads the matmul size out of the leased task payload.
// The input arrives as the wire envelope {"json":{"kind":...,"n":...}} (REST
// and gRPC store the same shape); the bare {"kind":...,"n":...} form is
// accepted as well.
func assignedMatmulSize(item pool.Assignment) (int, error) {
	input := []byte(item.InputJSON)
	if item.Task.Input != nil {
		input = item.Task.Input
	}
	if len(bytes.TrimSpace(input)) == 0 {
		return 0, errors.New("assigned task has no input")
	}
	var wire struct {
		Json *struct {
			Kind string   `json:"kind"`
			N    *float64 `json:"n"`
		} `json:"json"`
		Kind string   `json:"kind"`
		N    *float64 `json:"n"`
	}
	if err := json.Unmarshal(input, &wire); err != nil {
		return 0, fmt.Errorf("assigned task input: %w", err)
	}
	kind, size := wire.Kind, wire.N
	if wire.Json != nil {
		kind, size = wire.Json.Kind, wire.Json.N
	}
	if kind != "matmul-f32-gpu" {
		return 0, fmt.Errorf("unsupported assigned task kind %q", kind)
	}
	if size == nil || *size < 1 || *size != math.Trunc(*size) || *size > math.MaxInt32 {
		return 0, errors.New("assigned task has no valid matmul size")
	}
	return int(*size), nil
}

// heartbeatLoop keeps the contributor tab alive and renews held leases. The
// server reaps a tab whose heartbeat is older than twice the lease TTL, and
// an expired lease turns a late result into tamper evidence, so both run on
// time (T102 HIGH). Idle ticks send a plain keep-alive; a tick with held
// leases sends one heartbeat per lease, which also refreshes the tab.
// The loop wakes on a short quantum and compares against the current lease
// cadence, so a newly granted short lease is renewed in time even though the
// loop started before the claim.
func (c *Client) heartbeatLoop(ctx context.Context, p Pool, tabID string, leases *leaseSet) {
	path := p.prefix() + "/workers/" + url.PathEscape(tabID) + "/heartbeat"
	last := time.Now()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if now.Sub(last) < leases.interval() {
				continue
			}
			last = now
		}
		token, err := c.token(ctx, p)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue // transient exchange failure: retry on the next tick
		}
		held := leases.snapshot()
		if len(held) == 0 {
			if _, err := c.rest(ctx, http.MethodPost, path, token, map[string]any{}); err != nil && ctx.Err() == nil {
				if s := statusOf(err); s == http.StatusNotFound || s == http.StatusForbidden {
					return // the tab is gone server-side; the claim loop will see it too
				}
			}
			continue
		}
		for _, lease := range held {
			if ctx.Err() != nil {
				return
			}
			_, err := c.rest(ctx, http.MethodPost, path, token, map[string]any{"lease": lease})
			if err != nil && ctx.Err() == nil {
				if s := statusOf(err); s == 0 || s == http.StatusRequestTimeout || s == http.StatusTooManyRequests || s >= 500 {
					continue // transient: keep the lease, retry next tick
				}
				// 4xx: fenced, expired, unknown or forbidden — the server
				// will reject a result for this lease anyway, so stop
				// renewing it and let the submit decide the outcome.
				leases.drop(lease)
			}
		}
	}
}

var errNoContent = errors.New("no content")

func (c *Client) rest(ctx context.Context, method, path, token string, body any) ([]byte, error) {
	var r io.Reader
	if body != nil {
		var raw []byte
		switch v := body.(type) {
		case []byte:
			raw = v
		default:
			var err error
			raw, err = json.Marshal(v)
			if err != nil {
				return nil, err
			}
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if readErr != nil {
		return nil, readErr
	}
	if res.StatusCode == http.StatusNoContent {
		return nil, errNoContent
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		// T101 (cmdc leak sweep): a server that echoes the request body or
		// headers back on an error page must not smuggle the API key or the
		// bearer token into the CLI's error output.
		return nil, &httpError{path: path, status: res.StatusCode, body: redact.Text(problemText(raw), c.key, token)}
	}
	return raw, nil
}

func (c *Client) restWithHeaders(ctx context.Context, method, path, token string, body []byte, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if readErr != nil {
		return nil, readErr
	}
	if res.StatusCode == http.StatusNoContent {
		return nil, errNoContent
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, &httpError{path: path, status: res.StatusCode, body: redact.Text(problemText(raw), c.key, token)}
	}
	return raw, nil
}

func matmulOutput(n int) []byte {
	values := make([]float32, n*n)
	for row := 0; row < n; row++ {
		for col := 0; col < n; col++ {
			var sum float32
			for k := 0; k < n; k++ {
				sum += float32((row*n+k)%7-3) * float32((k*n+col)%5-2)
			}
			values[row*n+col] = sum
		}
	}
	raw := make([]byte, len(values)*4)
	for i, v := range values {
		u := math.Float32bits(v)
		raw[i*4], raw[i*4+1], raw[i*4+2], raw[i*4+3] = byte(u), byte(u>>8), byte(u>>16), byte(u>>24)
	}
	sum := sha256.Sum256(raw)
	return mustJSON(map[string]any{"n": n, "sha256": hex.EncodeToString(sum[:])})
}

func shaJSON(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func mustJSON(v any) []byte   { b, _ := json.Marshal(v); return b }
