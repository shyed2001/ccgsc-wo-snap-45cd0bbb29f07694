// Path: cmd/pools-watcher/probe.go | WO F16 (cmdc)
// What/why: the real WorkProber. There is no dedicated queue-status REST route
// in the reviewed surface; the developer events stream is the only
// developer-scoped source of queued-work truth, and it replays the committed
// event log on connect. A poll therefore opens the events stream, reads the
// replayed snapshot for a short bounded window and answers whether the net
// queued job count (job.queued minus job.done/job.failed) is positive.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// snapshotBudget bounds one status poll: long enough to read the replay the
// server writes immediately on connect, short enough that a downed stream
// cannot stall the watcher.
const snapshotBudget = 2 * time.Second

// httpProber polls the queue status of one pool.
type httpProber struct {
	base   string
	token  string
	client *http.Client
}

// newHTTPProber answers the real WorkProber for one server and token.
func newHTTPProber(o options, token string) *httpProber {
	return &httpProber{
		base:   o.Server,
		token:  token,
		client: &http.Client{Timeout: snapshotBudget},
	}
}

// HasWork reads a short snapshot of the pool's events stream and answers
// whether any job is currently queued.
func (p *httpProber) HasWork(ctx context.Context, poolID string) (bool, error) {
	prefix := poolPrefix[poolID]
	snapCtx, cancel := context.WithTimeout(ctx, snapshotBudget)
	defer cancel()
	req, err := http.NewRequestWithContext(snapCtx, http.MethodGet, p.base+prefix+"/events", nil)
	if err != nil {
		return false, fmt.Errorf("%s/events: bad request: %w", prefix, err)
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Accept", "text/event-stream")
	res, err := p.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("%s/events: request failed: %w", prefix, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false, fmt.Errorf("%s/events: HTTP %d", prefix, res.StatusCode)
	}
	net := 0
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
					case eventQueued:
						net++
					case eventJobDone, eventJobFailed:
						net--
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
	return net > 0, nil
}
