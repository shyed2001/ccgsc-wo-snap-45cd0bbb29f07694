// Path: cmd/pools-watcher/watcher.go | WO F16 (cmdc)
// What/why: the contributor-browser watcher state machine. It watches the pool
// work stream (SSE) and launches headless Chrome contributor tabs when a pool
// has queued work, up to a cap. When SSE drops it falls back to polling the
// queue status and keeps trying to reconnect, backing off up to a cap. When
// every watched pool is idle for the configured duration the tabs are closed,
// and every launched tab is closed on exit.
//
// The three external dependencies (EventSource, WorkProber, BrowserLauncher)
// are interfaces so the whole state machine is testable with fakes.
package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// Work event types the watcher reacts to. The developer events stream emits
// job.queued when a job enters the queue and job.done/job.failed when a job
// leaves it (internal/api/api.go eventTx and docs/R3E_RUNTIME_CONTRACT.md).
const (
	eventQueued    = "job.queued"
	eventJobDone   = "job.done"
	eventJobFailed = "job.failed"
)

// idleCheckEvery is how often the idle reaper checks whether the tabs have
// been idle long enough to close. It is small enough for the short idle
// durations used in tests and negligible for a production idle of 60s.
const idleCheckEvery = 100 * time.Millisecond

// errStreamClosed marks a dropped SSE stream whose events channel was closed.
var errStreamClosed = errors.New("the work stream closed")

// WorkEvent is one work notification for a pool.
type WorkEvent struct {
	PoolID string
	Type   string
	JobID  string
}

// EventStream is one open subscription to a pool work stream.
type EventStream struct {
	// Events delivers work events; it is closed when the stream drops.
	Events <-chan WorkEvent
	// Errors reports the reason the stream dropped.
	Errors <-chan error
}

// EventSource opens live pool work streams (the SSE transport).
type EventSource interface {
	// Subscribe opens the stream for one pool. It answers an error when the
	// stream cannot open.
	Subscribe(ctx context.Context, poolID string) (*EventStream, error)
}

// WorkProber polls the queue status of one pool while SSE is down.
type WorkProber interface {
	// HasWork reports whether the pool has queued work.
	HasWork(ctx context.Context, poolID string) (bool, error)
}

// TabHandle is one open contributor tab.
type TabHandle interface{}

// BrowserLauncher opens and closes headless Chrome contributor tabs.
type BrowserLauncher interface {
	// Launch opens one contributor tab for the given page URL.
	Launch(ctx context.Context, url string) (TabHandle, error)
	// Close closes one tab, killing its Chrome process.
	Close(h TabHandle)
}

// watcher is the whole state machine.
type watcher struct {
	cfg     options
	source  EventSource
	prober  WorkProber
	browser BrowserLauncher

	// ctx is the run context; it is set by Run before any goroutine starts and
	// is only read afterwards, so it never races.
	ctx context.Context

	mu        sync.Mutex
	jobs      map[string]string // job id -> pool id that owns the queued job
	busy      map[string]bool   // pool id -> has queued work
	tabs      []TabHandle
	idleSince time.Time
}

// newWatcher answers a watcher for one run.
func newWatcher(o options, source EventSource, prober WorkProber, browser BrowserLauncher) *watcher {
	return &watcher{
		cfg:     o,
		source:  source,
		prober:  prober,
		browser: browser,
		jobs:    map[string]string{},
		busy:    map[string]bool{},
	}
}

// Run watches every configured pool until ctx is cancelled, then closes every
// launched tab. It blocks until the run ends.
func (w *watcher) Run(ctx context.Context) {
	w.ctx = ctx
	var wg sync.WaitGroup
	for _, pool := range w.cfg.Pools {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			w.watchPool(ctx, p)
		}(pool)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.reapIdle(ctx)
	}()
	wg.Wait()
	w.closeAll()
}

// watchPool drives one pool: consume the SSE stream, and whenever it drops
// fall back to polling the queue status while retrying the stream with
// backoff (capped at maxBackoff).
func (w *watcher) watchPool(ctx context.Context, pool string) {
	backoff := w.cfg.Poll
	for {
		if ctx.Err() != nil {
			return
		}
		stream, err := w.source.Subscribe(ctx, pool)
		if err == nil {
			err = w.consumeSSE(ctx, pool, stream)
			if ctx.Err() != nil {
				return
			}
			// The stream dropped; reset the backoff for the poll fallback.
			backoff = w.cfg.Poll
		}
		w.pollOnce(ctx, pool)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// consumeSSE consumes one open stream until it drops or the context ends.
func (w *watcher) consumeSSE(ctx context.Context, pool string, stream *EventStream) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-stream.Events:
			if !ok {
				return errStreamClosed
			}
			w.applyEvent(pool, ev)
		case err, ok := <-stream.Errors:
			if !ok {
				// A closed error channel has no more errors; stop selecting it.
				stream.Errors = nil
				continue
			}
			if err != nil {
				return err
			}
		}
	}
}

// pollOnce reads the queue status once and applies it. A poll error keeps the
// last known state instead of ending the watch.
func (w *watcher) pollOnce(ctx context.Context, pool string) {
	busy, err := w.prober.HasWork(ctx, pool)
	if err != nil {
		return
	}
	w.mu.Lock()
	w.setBusyLocked(pool, busy)
	w.mu.Unlock()
}

// applyEvent applies one work event to the pool state.
func (w *watcher) applyEvent(pool string, ev WorkEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch ev.Type {
	case eventQueued:
		if ev.JobID != "" {
			w.jobs[ev.JobID] = pool
		}
	case eventJobDone, eventJobFailed:
		if ev.JobID != "" {
			delete(w.jobs, ev.JobID)
		}
	}
	w.setBusyLocked(pool, w.poolBusyLocked(pool))
}

// setBusyLocked reconciles the tab set after a busy/idle change. It must be
// called with w.mu held.
func (w *watcher) setBusyLocked(pool string, busy bool) {
	if busy {
		w.busy[pool] = true
		w.idleSince = time.Time{}
		w.ensureTabsLocked()
		return
	}
	delete(w.busy, pool)
	if len(w.busy) == 0 && w.idleSince.IsZero() {
		w.idleSince = time.Now()
	}
}

// poolBusyLocked reports whether any queued job is owned by the pool. It must
// be called with w.mu held.
func (w *watcher) poolBusyLocked(pool string) bool {
	for _, p := range w.jobs {
		if p == pool {
			return true
		}
	}
	return false
}

// ensureTabsLocked launches tabs until the cap is reached. It must be called
// with w.mu held.
func (w *watcher) ensureTabsLocked() {
	for len(w.tabs) < w.cfg.MaxTabs {
		h, err := w.browser.Launch(w.ctx, w.tabURL())
		if err != nil {
			return
		}
		w.tabs = append(w.tabs, h)
	}
}

// tabURL is the contribute page URL of the watched pools.
func (w *watcher) tabURL() string {
	return strings.TrimRight(w.cfg.Server, "/") + "/pools/contribute.html?pools=" + strings.Join(w.cfg.Pools, ",")
}

// reapIdle closes the tabs once every watched pool has been idle for the
// configured duration.
func (w *watcher) reapIdle(ctx context.Context) {
	ticker := time.NewTicker(idleCheckEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.mu.Lock()
			if len(w.busy) == 0 && !w.idleSince.IsZero() && time.Since(w.idleSince) >= w.cfg.Idle {
				w.closeTabsLocked()
				w.idleSince = time.Time{}
			}
			w.mu.Unlock()
		}
	}
}

// closeTabsLocked closes every launched tab. It must be called with w.mu held.
func (w *watcher) closeTabsLocked() {
	for _, h := range w.tabs {
		w.browser.Close(h)
	}
	w.tabs = nil
}

// closeAll closes every launched tab on exit. Run calls it after all watch and
// reap goroutines have returned, so it does not need the lock.
func (w *watcher) closeAll() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closeTabsLocked()
}
