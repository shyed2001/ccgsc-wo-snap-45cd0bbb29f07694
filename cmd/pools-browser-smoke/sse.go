// Path: cmd/pools-browser-smoke/sse.go | WO_SMOKE (D-W10-12, G-MVP box 2 prep)
// What/why: the SSE watcher of the harness. It opens the job events stream of
// one pool and records every event, so the run can assert that the task.done
// event of the submitted job arrived on the live stream.
//
// The watcher connects BEFORE the job is submitted, so the events of the job
// arrive live and are not a replay of an older snapshot.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// sseEvent is one observed event of a stream.
type sseEvent struct {
	ID      string
	Type    string
	JobID   string
	TaskID  string
	Arrived time.Time
}

// sseWatcher collects the events of one stream.
type sseWatcher struct {
	mu     sync.Mutex
	events []sseEvent
	err    error
	done   chan struct{}
}

// newSSEWatcher answers a watcher that is not connected yet.
func newSSEWatcher() *sseWatcher {
	return &sseWatcher{done: make(chan struct{})}
}

// watch opens the event stream of one pool and collects the events until the
// context ends. It answers the terminal error of the stream.
func (c *client) watch(ctx context.Context, prefix, token string, w *sseWatcher) {
	defer close(w.done)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+prefix+"/events", nil)
	if err != nil {
		w.setErr(err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	res, err := c.stream.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			w.setErr(fmt.Errorf("%s/events: request failed: %w", prefix, err))
		}
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		w.setErr(fmt.Errorf("%s/events: HTTP %d", prefix, res.StatusCode))
		return
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	var id, typ string
	var data strings.Builder
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if id != "" || data.Len() > 0 {
				ev := sseEvent{ID: id, Type: typ, Arrived: time.Now()}
				var payload struct {
					JobID  string `json:"jobId"`
					TaskID string `json:"taskId"`
				}
				if json.Unmarshal([]byte(data.String()), &payload) == nil {
					ev.JobID = payload.JobID
					ev.TaskID = payload.TaskID
				}
				w.add(ev)
			}
			id, typ = "", ""
			data.Reset()
		case strings.HasPrefix(line, ":"):
			// keep-alive comment
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
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		w.setErr(fmt.Errorf("%s/events: stream failed: %w", prefix, err))
	}
}

// add records one event.
func (w *sseWatcher) add(ev sseEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, ev)
}

// setErr records the terminal error of the stream.
func (w *sseWatcher) setErr(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err == nil {
		w.err = err
	}
}

// terminalError answers the error the stream ended with, if any.
func (w *sseWatcher) terminalError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// collected answers the events seen so far, in arrival order.
func (w *sseWatcher) collected() []sseEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]sseEvent, len(w.events))
	copy(out, w.events)
	return out
}

// eventsFor filters the events of one job.
func (w *sseWatcher) eventsFor(jobID string) []sseEvent {
	out := []sseEvent{}
	for _, ev := range w.collected() {
		if ev.JobID == jobID {
			out = append(out, ev)
		}
	}
	return out
}

// awaitEvent waits until an event of the given type arrived for the job, or
// the budget passes. It answers the matching event and whether it arrived.
func (w *sseWatcher) awaitEvent(jobID, eventType string, budget time.Duration) (sseEvent, bool) {
	deadline := time.Now().Add(budget)
	for {
		for _, ev := range w.eventsFor(jobID) {
			if ev.Type == eventType {
				return ev, true
			}
		}
		if time.Now().After(deadline) {
			return sseEvent{}, false
		}
		time.Sleep(25 * time.Millisecond)
	}
}
