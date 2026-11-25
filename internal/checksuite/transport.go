// Path: internal/checksuite/transport.go | Author: GLM for Claude Opus 5.5 | 2026-11-21
// What/why: REST, SSE and gRPC transport of the T108 check suite: one HTTP
// client for commands, one without an overall deadline for the SSE streams, one
// h2c gRPC connection for the gRPC surface, and the two event watchers that
// exercise the Last-Event-ID resume. Error strings carry paths and status
// codes only, never keys or tokens.
package checksuite

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/grpcapi"
	"ccgsc/poolscore/internal/pool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
)

// reconnectPause bounds the reconnect loop of a dropped stream.
const reconnectPause = 50 * time.Millisecond

// transport owns every wire connection of one suite run.
type transport struct {
	base    string
	rest    *http.Client
	stream  *http.Client
	conn    *grpc.ClientConn
	thread  grpcapi.ThreadPoolClient
	process grpcapi.ProcessPoolClient
}

func newTransport(base string) (*transport, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid base URL %q", base)
	}
	conn, err := grpc.NewClient(u.Host, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("gRPC connect: %w", err)
	}
	return &transport{
		base:    strings.TrimRight(base, "/"),
		rest:    &http.Client{Timeout: 2 * time.Minute},
		stream:  &http.Client{}, // SSE ends by context, never by a timeout
		conn:    conn,
		thread:  grpcapi.NewThreadPoolClient(conn),
		process: grpcapi.NewProcessPoolClient(conn),
	}, nil
}

func (t *transport) close() error {
	if t.conn != nil {
		return t.conn.Close()
	}
	return nil
}

// do performs one REST call and answers the status together with the body, so
// callers can assert 201/204/200/401 exactly. Transport failures surface as
// errors; every HTTP status is returned, never turned into an error.
func (t *transport) do(ctx context.Context, method, path, token string, body []byte, headers map[string]string) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.base+path, reader)
	if err != nil {
		return 0, nil, fmt.Errorf("%s: bad request: %w", path, err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := t.rest.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s: request failed: %w", path, err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	return res.StatusCode, data, nil
}

func (t *transport) exchange(ctx context.Context, prefix, key string) (string, error) {
	status, body, err := t.do(ctx, http.MethodPost, prefix+"/auth/token", "", mustJSON(struct {
		APIKey string `json:"apiKey"`
	}{key}), nil)
	if err != nil {
		return "", err
	}
	if status != 200 {
		return "", httpError(prefix+"/auth/token", status, body)
	}
	var tok auth.Token
	if json.Unmarshal(body, &tok) != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("%s/auth/token: invalid token response", prefix)
	}
	return tok.AccessToken, nil
}

type createdKey struct {
	raw   string
	keyID string
}

func (t *transport) createKey(ctx context.Context, prefix, admin string, user auth.User, scopes []string) (createdKey, error) {
	status, body, err := t.do(ctx, http.MethodPost, prefix+"/auth/keys", admin, mustJSON(struct {
		User   auth.User `json:"user"`
		Scopes []string  `json:"scopes"`
	}{user, scopes}), nil)
	if err != nil {
		return createdKey{}, err
	}
	if status != 201 {
		return createdKey{}, httpError(prefix+"/auth/keys", status, body)
	}
	var v struct {
		APIKey string `json:"apiKey"`
		KeyID  string `json:"keyId"`
	}
	if json.Unmarshal(body, &v) != nil || v.APIKey == "" || v.KeyID == "" {
		return createdKey{}, fmt.Errorf("%s/auth/keys: invalid key response", prefix)
	}
	return createdKey{raw: v.APIKey, keyID: v.KeyID}, nil
}

func (t *transport) uploadArtifact(ctx context.Context, prefix, token string, data []byte, sha string) (int, []byte, error) {
	return t.do(ctx, http.MethodPut, prefix+"/artifacts", token, data, map[string]string{
		"Content-Type":      "application/octet-stream",
		"X-Artifact-Sha256": sha,
		"Upload-Offset":     "0",
		"Upload-Length":     fmt.Sprint(len(data)),
	})
}

func (t *transport) restSubmitJob(ctx context.Context, prefix, token, artifactID string, size int, taskID, idem string) (string, int, error) {
	zero := 0.0
	body := pool.CreateRequest{
		ModuleArtifactIDs: []string{artifactID},
		Tasks:             []pool.TaskSpec{{TaskID: taskID, Input: json.RawMessage(fmt.Sprintf(`{"json":{"kind":"matmul-f32-gpu","n":%d}}`, size))}},
		BatchSize:         1,
		Needs:             "cpu",
		RerunPercent:      &zero,
	}
	status, raw, err := t.do(ctx, http.MethodPost, prefix+"/jobs", token, mustJSON(body), map[string]string{
		"Idempotency-Key": idem + "-" + auth.ID(),
	})
	if err != nil || status != 201 {
		return "", status, firstErr(err, httpError(prefix+"/jobs", status, raw))
	}
	var job pool.Job
	if json.Unmarshal(raw, &job) != nil || job.JobID == "" {
		return "", status, fmt.Errorf("%s/jobs: invalid job response", prefix)
	}
	return job.JobID, status, nil
}

func (t *transport) grpcSubmitJob(ctx context.Context, prefix, token, artifactID string, size int, taskID, idem string) (string, error) {
	gctx := grpcapi.WithBearer(ctx, token)
	input, err := structpb.NewStruct(map[string]any{"kind": "matmul-f32-gpu", "n": float64(size)})
	if err != nil {
		return "", err
	}
	batch, needs, rerun := int64(1), "cpu", float64(0)
	req := &grpcapi.CreateJobRequest{
		ModuleArtifactIds: []string{artifactID},
		Tasks:             []*grpcapi.TaskSpec{{TaskId: taskID, Input: &grpcapi.TaskInput{Source: &grpcapi.TaskInput_Json{Json: input}}}},
		BatchSize:         &batch,
		Needs:             &needs,
		RerunPercent:      &rerun,
		IdempotencyKey:    idem + "-" + auth.ID(),
	}
	var job *grpcapi.Job
	if prefix == ThreadPrefix {
		job, err = t.thread.CreateJob(gctx, req)
	} else {
		job, err = t.process.CreateJob(gctx, req)
	}
	if err != nil {
		return "", fmt.Errorf("%s CreateJob: %w", prefix, err)
	}
	if job.GetJobId() == "" {
		return "", fmt.Errorf("%s CreateJob: response without a job id", prefix)
	}
	return job.GetJobId(), nil
}

func (t *transport) getJob(ctx context.Context, prefix, token, jobID string) (pool.Job, error) {
	status, raw, err := t.do(ctx, http.MethodGet, prefix+"/jobs/"+url.PathEscape(jobID), token, nil, nil)
	if err != nil {
		return pool.Job{}, err
	}
	if status != 200 {
		return pool.Job{}, httpError(prefix+"/jobs/{id}", status, raw)
	}
	var job pool.Job
	if json.Unmarshal(raw, &job) != nil {
		return pool.Job{}, fmt.Errorf("%s/jobs/{id}: invalid job response", prefix)
	}
	return job, nil
}

func (t *transport) getResults(ctx context.Context, prefix, token, jobID string) ([]pool.Result, error) {
	status, raw, err := t.do(ctx, http.MethodGet, prefix+"/jobs/"+url.PathEscape(jobID)+"/results", token, nil, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, httpError(prefix+"/jobs/{id}/results", status, raw)
	}
	var page struct {
		Items []pool.Result `json:"items"`
	}
	if json.Unmarshal(raw, &page) != nil {
		return nil, fmt.Errorf("%s/jobs/{id}/results: invalid results response", prefix)
	}
	return page.Items, nil
}

// awaitJob polls the REST job view until the job is terminal or the deadline
// passes. The engine settles jobs within milliseconds in-process, so the poll
// interval stays tiny.
func (t *transport) awaitJob(ctx context.Context, prefix, token, jobID string, budget time.Duration) (string, error) {
	deadline := time.Now().Add(budget)
	var last pool.Job
	for {
		job, err := t.getJob(ctx, prefix, token, jobID)
		if err == nil {
			last = job
			switch job.State {
			case "completed", "failed", "partial", "cancelled":
				return job.State, nil
			}
		} else if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if time.Now().After(deadline) {
			return last.State, fmt.Errorf("job %s did not complete in time (last state %q)", jobID, last.State)
		}
		select {
		case <-ctx.Done():
			return last.State, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// ---------------------------------------------------------------- watchers

// eventRecord is one observed event with its arrival time and connection.
type eventRecord struct {
	ID      string
	Type    string
	JobID   string
	TaskID  string
	Arrived time.Time
	Conn    int
}

// watcher collects the events of one stream. close releases the collector;
// fail records the terminal watcher error for the report.
type watcher struct {
	eventsC chan eventRecord
	resumes atomic.Int32
	mu      sync.Mutex
	err     error
}

func newWatcher() *watcher {
	return &watcher{eventsC: make(chan eventRecord, 256)}
}

// close is deferred by the watcher goroutine: it releases collect.
func (w *watcher) close() { close(w.eventsC) }

// fail records the terminal error of the watcher goroutine.
func (w *watcher) fail(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.err = err
}

// terminalError reports the error the watcher ended with, if any.
func (w *watcher) terminalError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

func (w *watcher) resumeCount() int { return int(w.resumes.Load()) }

// collect blocks until the watcher goroutine closed the channel and answers
// every event in arrival order.
func (w *watcher) collect() []eventRecord {
	out := make([]eventRecord, 0, 32)
	for ev := range w.eventsC {
		out = append(out, ev)
	}
	return out
}

// emit records one event and advances the resume cursor. The one-shot
// Last-Event-ID resume fires on the first event of the watch: the caller then
// closes the connection and reconnects with the id, so the resumed stream must
// not replay anything. emit reports whether the resume fired.
func (w *watcher) emit(rec eventRecord, last *string, resumeArmed *bool) bool {
	*last = rec.ID
	w.eventsC <- rec
	if *resumeArmed {
		return false
	}
	*resumeArmed = true
	w.resumes.Add(1)
	return true
}

// watchSSE streams GET {prefix}/events with a one-shot Last-Event-ID resume.
// The watcher runs until ctx is cancelled; transient stream errors reconnect.
func (t *transport) watchSSE(ctx context.Context, prefix, token string, w *watcher) error {
	last := ""
	resumeArmed := false
	conn := 0
	for ctx.Err() == nil {
		conn++
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.base+prefix+"/events", nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "text/event-stream")
		if last != "" {
			req.Header.Set("Last-Event-ID", last)
		}
		res, err := t.stream.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if !sleepCtx(ctx, reconnectPause) {
				return nil
			}
			continue
		}
		resumed := false
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		var id, typ string
		var data strings.Builder
		flush := func() bool {
			defer func() {
				id, typ = "", ""
				data.Reset()
			}()
			if id == "" && data.Len() == 0 {
				return false
			}
			rec := eventRecord{ID: id, Type: typ, Arrived: time.Now(), Conn: conn}
			var payload struct {
				JobID  string `json:"jobId"`
				TaskID string `json:"taskId"`
			}
			if json.Unmarshal([]byte(data.String()), &payload) == nil {
				rec.JobID, rec.TaskID = payload.JobID, payload.TaskID
			}
			if w.emit(rec, &last, &resumeArmed) {
				resumed = true
			}
			return resumed
		}
	readLoop:
		for !resumed {
			if !sc.Scan() {
				break
			}
			line := sc.Text()
			switch {
			case line == "":
				if flush() {
					break readLoop // resume fired: reconnect with Last-Event-ID
				}
			case strings.HasPrefix(line, ":"):
				// comment or keep-alive
			case strings.HasPrefix(line, "id:"):
				id = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			case strings.HasPrefix(line, "event:"):
				typ = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(strings.TrimPrefix(line, "data:"))
			}
		}
		_ = res.Body.Close()
		if ctx.Err() != nil {
			return nil
		}
		if !sleepCtx(ctx, reconnectPause) {
			return nil
		}
	}
	return nil
}

// watchGRPC streams StreamEvents with a one-shot last_event_id resume, the
// gRPC twin of the SSE watcher.
func (t *transport) watchGRPC(ctx context.Context, prefix, token string, w *watcher) error {
	last := ""
	resumeArmed := false
	conn := 0
	for ctx.Err() == nil {
		conn++
		sctx, scancel := context.WithCancel(ctx)
		req := &grpcapi.EventStreamRequest{LastEventId: last}
		var stream interface {
			Recv() (*grpcapi.EventMessage, error)
		}
		var err error
		if prefix == ThreadPrefix {
			stream, err = t.thread.StreamEvents(grpcapi.WithBearer(sctx, token), req)
		} else {
			stream, err = t.process.StreamEvents(grpcapi.WithBearer(sctx, token), req)
		}
		if err != nil {
			scancel()
			if ctx.Err() != nil {
				return nil
			}
			if !sleepCtx(ctx, reconnectPause) {
				return nil
			}
			continue
		}
	recvLoop:
		for {
			msg, rerr := stream.Recv()
			if rerr != nil {
				break recvLoop // stream closed or failed: reconnect
			}
			if msg.GetType() == "reauth" {
				break recvLoop // terminal stream event: reconnect
			}
			rec := eventRecord{
				ID: msg.GetEventId(), Type: msg.GetType(),
				JobID: msg.GetJobId(), TaskID: msg.GetTaskId(),
				Arrived: time.Now(), Conn: conn,
			}
			if w.emit(rec, &last, &resumeArmed) {
				break recvLoop // resume fired: reconnect with last_event_id
			}
		}
		scancel()
		_ = stream
		if ctx.Err() != nil {
			return nil
		}
		if !sleepCtx(ctx, reconnectPause) {
			return nil
		}
	}
	return nil
}

// sleepCtx waits for d or until ctx is done; it reports whether the full wait
// completed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// httpError renders a non-2xx status with the RFC 9457 code when present.
// Problem bodies carry codes and trace ids, never key material, so the string
// is safe for the report.
func httpError(path string, status int, body []byte) error {
	code := problemCode(body)
	if code != "" {
		return fmt.Errorf("%s: HTTP %d (%s)", path, status, code)
	}
	return fmt.Errorf("%s: HTTP %d", path, status)
}

func problemCode(body []byte) string {
	var p struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(body, &p) != nil {
		return ""
	}
	return p.Code
}

func httpDetail(method, path string, status int, err error) string {
	detail := fmt.Sprintf("%s %s -> HTTP %d", method, path, status)
	if err != nil {
		detail += ": " + err.Error()
	}
	return detail
}

func grpcDetail(op string, err error) string {
	if err != nil {
		return "gRPC " + op + ": " + err.Error()
	}
	return "gRPC " + op
}
