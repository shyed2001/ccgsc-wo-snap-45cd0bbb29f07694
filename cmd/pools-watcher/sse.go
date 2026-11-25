// Path: cmd/pools-watcher/sse.go | WO F16 (cmdc)
// What/why: the real SSE EventSource. It opens the developer events stream of
// one pool (GET {pool-prefix}/events, docs/R3E_RUNTIME_CONTRACT.md section 19)
// and forwards the job.queued/job.done/job.failed frames as WorkEvents. The
// token is set on the Authorization header and never printed.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// sseSource is the real SSE transport of the watcher.
type sseSource struct {
	base  string
	token string
	// streamClient has no overall timeout: the stream ends by its context, not
	// by a timeout.
	streamClient *http.Client
}

// newSSESource answers the real EventSource for one server and token.
func newSSESource(o options, token string) *sseSource {
	return &sseSource{
		base:         o.Server,
		token:        token,
		streamClient: &http.Client{},
	}
}

// rawEvent is the Event JSON the stream carries (internal/pool/model.go).
type rawEvent struct {
	Type  string `json:"type"`
	JobID string `json:"jobId"`
}

// Subscribe opens the events stream of one pool and answers the event/error
// channels. The body is parsed by readStream until the stream ends.
func (s *sseSource) Subscribe(ctx context.Context, poolID string) (*EventStream, error) {
	prefix := poolPrefix[poolID]
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+prefix+"/events", nil)
	if err != nil {
		return nil, fmt.Errorf("%s/events: bad request: %w", prefix, err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "text/event-stream")
	res, err := s.streamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s/events: request failed: %w", prefix, err)
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("%s/events: HTTP %d", prefix, res.StatusCode)
	}
	events := make(chan WorkEvent, 64)
	errs := make(chan error, 1)
	go s.readStream(ctx, res, poolID, events, errs)
	return &EventStream{Events: events, Errors: errs}, nil
}

// readStream parses the SSE frames and forwards the work events. It closes the
// events channel when the stream ends and reports a non-context error on errs.
func (s *sseSource) readStream(ctx context.Context, res *http.Response, poolID string, events chan<- WorkEvent, errs chan<- error) {
	defer res.Body.Close()
	defer close(events)
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	var data strings.Builder
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if data.Len() > 0 {
				var ev rawEvent
				if json.Unmarshal([]byte(data.String()), &ev) == nil {
					switch ev.Type {
					case eventQueued, eventJobDone, eventJobFailed:
						select {
						case events <- WorkEvent{PoolID: poolID, Type: ev.Type, JobID: ev.JobID}:
						case <-ctx.Done():
							return
						}
					}
				}
			}
			data.Reset()
		case strings.HasPrefix(line, ":"):
			// keep-alive comment
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(line, "data:"))
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		select {
		case errs <- err:
		default:
		}
	}
}
