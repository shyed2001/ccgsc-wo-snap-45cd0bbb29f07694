// Path: cmd/pools-watcher/watcher_test.go | WO F16 (cmdc)
// What/why: the state-machine tests of the watcher. They run against fakes for
// EventSource, WorkProber and BrowserLauncher, so no Chrome and no server are
// needed: SSE work launches a tab, idle closes it, an SSE drop falls back to
// polling and recovers, the tab cap is respected and exit closes every child.
package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeEventSource is a deterministic EventSource. Subscribe answers an error
// while subErr is set (the SSE outage) and otherwise installs a fresh stream
// the test can emit into or drop.
type fakeEventSource struct {
	mu       sync.Mutex
	subErr   error
	streams  map[string]*fakeStream
	subCalls int
	okCalls  int
}

type fakeStream struct {
	events chan WorkEvent
	errs   chan error
}

func newFakeEventSource() *fakeEventSource {
	return &fakeEventSource{streams: map[string]*fakeStream{}}
}

func (f *fakeEventSource) Subscribe(_ context.Context, poolID string) (*EventStream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subCalls++
	if f.subErr != nil {
		return nil, f.subErr
	}
	f.okCalls++
	s := &fakeStream{events: make(chan WorkEvent, 64), errs: make(chan error, 1)}
	f.streams[poolID] = s
	return &EventStream{Events: s.events, Errors: s.errs}, nil
}

func (f *fakeEventSource) setErr(err error) {
	f.mu.Lock()
	f.subErr = err
	f.mu.Unlock()
}

func (f *fakeEventSource) emit(poolID string, ev WorkEvent) {
	f.mu.Lock()
	s := f.streams[poolID]
	f.mu.Unlock()
	if s != nil {
		s.events <- ev
	}
}

func (f *fakeEventSource) drop(poolID string) {
	f.mu.Lock()
	s := f.streams[poolID]
	f.streams[poolID] = nil
	f.mu.Unlock()
	if s != nil {
		close(s.events)
	}
}

func (f *fakeEventSource) okCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.okCalls
}

// fakeProber is a deterministic WorkProber.
type fakeProber struct {
	mu     sync.Mutex
	result map[string]bool
	err    error
	calls  int
}

func newFakeProber() *fakeProber {
	return &fakeProber{result: map[string]bool{}}
}

func (f *fakeProber) HasWork(_ context.Context, poolID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	return f.result[poolID], nil
}

func (f *fakeProber) set(poolID string, busy bool) {
	f.mu.Lock()
	f.result[poolID] = busy
	f.mu.Unlock()
}

func (f *fakeProber) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeBrowser is a deterministic BrowserLauncher.
type fakeBrowser struct {
	mu       sync.Mutex
	next     int
	launched []int
	closed   []int
}

type fakeHandle struct{ id int }

func newFakeBrowser() *fakeBrowser { return &fakeBrowser{} }

func (f *fakeBrowser) Launch(_ context.Context, _ string) (TabHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	id := f.next
	f.launched = append(f.launched, id)
	return fakeHandle{id: id}, nil
}

func (f *fakeBrowser) Close(h TabHandle) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := h.(fakeHandle).id
	f.closed = append(f.closed, id)
}

func (f *fakeBrowser) launchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.launched)
}

func (f *fakeBrowser) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.closed)
}

// startWatcher runs a watcher with the given fakes and answers a cancel and a
// done channel that closes when Run returns.
func startWatcher(o options, src *fakeEventSource, prober *fakeProber, browser *fakeBrowser) (context.CancelFunc, chan struct{}) {
	w := newWatcher(o, src, prober, browser)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return cancel, done
}

// waitFor polls the condition until it is true or the budget passes.
func waitFor(t *testing.T, budget time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", budget)
}

func TestSSEWorkLaunchesTab(t *testing.T) {
	src := newFakeEventSource()
	prober := newFakeProber()
	browser := newFakeBrowser()
	o := options{Server: "http://127.0.0.1:1", Pools: []string{poolThread}, MaxTabs: 1, Idle: time.Hour, Poll: 5 * time.Millisecond}
	cancel, done := startWatcher(o, src, prober, browser)
	defer cancel()

	waitFor(t, time.Second, func() bool { return src.okCount() >= 1 })
	src.emit(poolThread, WorkEvent{Type: eventQueued, JobID: "j1"})

	waitFor(t, time.Second, func() bool { return browser.launchCount() >= 1 })

	cancel()
	<-done
}

func TestIdleClosesTabs(t *testing.T) {
	src := newFakeEventSource()
	prober := newFakeProber()
	browser := newFakeBrowser()
	o := options{Server: "http://127.0.0.1:1", Pools: []string{poolThread}, MaxTabs: 1, Idle: 40 * time.Millisecond, Poll: 5 * time.Millisecond}
	cancel, done := startWatcher(o, src, prober, browser)
	defer cancel()

	waitFor(t, time.Second, func() bool { return src.okCount() >= 1 })
	src.emit(poolThread, WorkEvent{Type: eventQueued, JobID: "j1"})
	waitFor(t, time.Second, func() bool { return browser.launchCount() >= 1 })

	src.emit(poolThread, WorkEvent{Type: eventJobDone, JobID: "j1"})
	waitFor(t, time.Second, func() bool { return browser.closeCount() >= 1 })

	cancel()
	<-done
}

func TestSSEDropPollRecovery(t *testing.T) {
	src := newFakeEventSource()
	prober := newFakeProber()
	prober.set(poolThread, true) // the pool has queued work while SSE is down
	browser := newFakeBrowser()
	o := options{Server: "http://127.0.0.1:1", Pools: []string{poolThread}, MaxTabs: 1, Idle: 40 * time.Millisecond, Poll: 5 * time.Millisecond}
	cancel, done := startWatcher(o, src, prober, browser)
	defer cancel()

	waitFor(t, time.Second, func() bool { return src.okCount() >= 1 })
	src.emit(poolThread, WorkEvent{Type: eventQueued, JobID: "j1"})
	waitFor(t, time.Second, func() bool { return browser.launchCount() >= 1 })

	// Drop the stream and make every reconnect attempt fail: the watcher must
	// fall back to polling the queue status.
	src.drop(poolThread)
	src.setErr(errors.New("sse down"))
	waitFor(t, time.Second, func() bool { return prober.count() >= 2 })

	// Recover the stream; the watcher must stop polling once it reconnects.
	okBefore := src.okCount()
	src.setErr(nil)
	waitFor(t, time.Second, func() bool { return src.okCount() > okBefore })
	calls := prober.count()
	time.Sleep(80 * time.Millisecond)
	if got := prober.count(); got != calls {
		t.Fatalf("the watcher kept polling after SSE recovered: %d -> %d", calls, got)
	}

	// After recovery the SSE stream drives the state again: done -> idle close.
	src.emit(poolThread, WorkEvent{Type: eventJobDone, JobID: "j1"})
	waitFor(t, time.Second, func() bool { return browser.closeCount() >= 1 })

	cancel()
	<-done
}

func TestMaxTabsRespected(t *testing.T) {
	src := newFakeEventSource()
	prober := newFakeProber()
	browser := newFakeBrowser()
	o := options{Server: "http://127.0.0.1:1", Pools: []string{poolThread}, MaxTabs: 2, Idle: time.Hour, Poll: 5 * time.Millisecond}
	cancel, done := startWatcher(o, src, prober, browser)
	defer cancel()

	waitFor(t, time.Second, func() bool { return src.okCount() >= 1 })
	src.emit(poolThread, WorkEvent{Type: eventQueued, JobID: "j1"})
	src.emit(poolThread, WorkEvent{Type: eventQueued, JobID: "j2"})
	src.emit(poolThread, WorkEvent{Type: eventQueued, JobID: "j3"})

	waitFor(t, time.Second, func() bool { return browser.launchCount() >= 2 })
	time.Sleep(80 * time.Millisecond)
	if got := browser.launchCount(); got != 2 {
		t.Fatalf("launched %d tabs, want exactly %d (the cap)", got, 2)
	}

	cancel()
	<-done
}

func TestExitClosesAllTabs(t *testing.T) {
	src := newFakeEventSource()
	prober := newFakeProber()
	browser := newFakeBrowser()
	o := options{Server: "http://127.0.0.1:1", Pools: []string{poolThread}, MaxTabs: 3, Idle: time.Hour, Poll: 5 * time.Millisecond}
	cancel, done := startWatcher(o, src, prober, browser)
	defer cancel()

	waitFor(t, time.Second, func() bool { return src.okCount() >= 1 })
	src.emit(poolThread, WorkEvent{Type: eventQueued, JobID: "j1"})
	waitFor(t, time.Second, func() bool { return browser.launchCount() >= 3 })

	cancel()
	<-done

	if got, want := browser.closeCount(), browser.launchCount(); got != want {
		t.Errorf("exit closed %d tabs, want %d (every launched tab)", got, want)
	}
}
