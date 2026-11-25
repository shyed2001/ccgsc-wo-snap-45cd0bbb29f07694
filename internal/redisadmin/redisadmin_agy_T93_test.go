// Path: internal/redisadmin/redisadmin_agy_T93_test.go | Author: GLM (z.ai) | 2026-11-21
// T93 acceptance (agy review, fixed in the T101 batch):
//   - the 1 MiB per-reply budget also covers '+' simple strings, so a nested
//     array of them cannot allocate ~4 MiB under a fake 1 KiB charge;
//   - Configure stores the config and applies it under the same applyMu, so
//     Status never pairs a new Desired with a stale State and two Configures
//     cannot invert their applies;
//   - an ErrUnsupported freeRAM failure maps to exactly one state, never to
//     the transient "unavailable".
package redisadmin

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestReplyBudgetCoversSimpleStrings proves the '+' fix: simple strings are
// charged to the reply budget like bulk strings.
func TestReplyBudgetCoversSimpleStrings(t *testing.T) {
	// A plain simple string reply is still accepted.
	if v, err := readReply("+OK\r\n"); err != nil || v != "OK" {
		t.Fatalf("+OK read back as %v (%v)", v, err)
	}
	// Small simple strings inside one array keep working.
	v, err := readReply("*2\r\n+abc\r\n+def\r\n")
	if err != nil {
		t.Fatalf("small array rejected: %v", err)
	}
	items, ok := v.([]any)
	if !ok || len(items) != 2 || items[0] != "abc" || items[1] != "def" {
		t.Fatalf("array read back as %T %v", v, v)
	}
	// 32 arrays of 32 simple strings of 4093 bytes allocate ~4 MiB in one
	// reply while charging only 32 + 1024 = 1056 bytes to the budget without
	// the '+' case. Every line still fits the 4096 byte ReadSlice buffer.
	if _, err := readReply(nestedArrayOfSimpleStrings(32, 32, 4093)); err == nil {
		t.Fatal("a nested array of simple strings above the cap was accepted")
	}
	// The same shape far below the cap is still accepted.
	v, err = readReply(nestedArrayOfSimpleStrings(2, 2, 64))
	if err != nil {
		t.Fatalf("a small nested array of simple strings was rejected: %v", err)
	}
	if items, ok = v.([]any); !ok || len(items) != 2 {
		t.Fatalf("nested array read back as %T", v)
	}
}

// nestedArrayOfSimpleStrings builds a RESP array of arrays of '+' simple
// strings, each body size bytes long.
func nestedArrayOfSimpleStrings(outer, inner, size int) string {
	var b strings.Builder
	b.WriteString("*" + strconv.Itoa(outer) + "\r\n")
	for i := 0; i < outer; i++ {
		b.WriteString("*" + strconv.Itoa(inner) + "\r\n")
		for j := 0; j < inner; j++ {
			b.WriteString("+" + strings.Repeat("x", size) + "\r\n")
		}
	}
	return b.String()
}

// applyReply answers the commands of an apply session: AUTH and CONFIG SET
// get +OK, everything else is refused so the session ends.
func applyReply(cmd Command) (string, bool) {
	if len(cmd) == 0 {
		return "", false
	}
	switch cmd[0] {
	case "AUTH", "CONFIG":
		return "+OK\r\n", true
	}
	return "", false
}

// TestConfigureOrderingAndPendingState pins the T93 ordering fix: while the
// second apply is stalled on the network, Status must show the new Desired
// with the "pending" state (not the previous apply's "applied"), and a third
// Configure must wait for the second apply instead of overtaking it.
func TestConfigureOrderingAndPendingState(t *testing.T) {
	r := New("", "test-user", "test-password")
	r.cfg = DefaultConfig()

	var mu sync.Mutex
	dials := 0
	stalled := make(chan struct{})
	release := make(chan struct{})
	r.dial = func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		mu.Lock()
		dials++
		n := dials
		mu.Unlock()
		go func() {
			defer server.Close()
			_ = server.SetDeadline(time.Now().Add(10 * time.Second))
			if n == 2 {
				// The second apply: stall before reading its first command
				// (so the AUTH write or reply blocks), then serve normally
				// once released.
				close(stalled)
				<-release
			}
			reader := bufio.NewReader(server)
			for {
				v, err := readRESP(reader, 0)
				if err != nil {
					return
				}
				items, ok := v.([]any)
				if !ok || len(items) == 0 {
					return
				}
				cmd := Command{}
				for _, a := range items {
					s, ok := a.(string)
					if !ok {
						return
					}
					cmd = append(cmd, s)
				}
				reply, ok := applyReply(cmd)
				if !ok {
					return
				}
				if _, err := io.WriteString(server, reply); err != nil {
					return
				}
			}
		}()
		return client, nil
	}

	// The first apply completes and leaves state "applied".
	if err := r.Apply(context.Background()); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if s := r.Status(context.Background()); s.State != "applied" {
		t.Fatalf("state after the first apply = %q, want applied", s.State)
	}

	cfg2 := Config{"fixed", 2, 10, 5, "aof_everysec"}
	done2 := make(chan error, 1)
	go func() { done2 <- r.Configure(context.Background(), cfg2) }()
	select {
	case <-stalled:
	case <-time.After(2 * time.Second):
		t.Fatal("the second Configure never reached the server")
	}

	// While the second apply is stalled, Status must pair the new Desired
	// with "pending".
	s := r.Status(context.Background())
	if s.State != "pending" {
		t.Fatalf("stalled Configure state = %q, want pending", s.State)
	}
	if s.Desired != cfg2 {
		t.Fatalf("stalled Configure desired = %+v, want %+v", s.Desired, cfg2)
	}

	// A third Configure must not run its apply while the second holds applyMu.
	cfg3 := Config{"fixed", 3, 10, 5, "aof_everysec"}
	done3 := make(chan error, 1)
	go func() { done3 <- r.Configure(context.Background(), cfg3) }()
	select {
	case err := <-done3:
		t.Fatalf("the third Configure did not wait for the second apply: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-done2:
		if err != nil {
			t.Fatalf("second configure: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second Configure never finished")
	}
	select {
	case err := <-done3:
		if err != nil {
			t.Fatalf("third configure: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the third Configure never finished")
	}
	if got := r.Config(); got != cfg3 {
		t.Fatalf("final config = %+v, want %+v", got, cfg3)
	}
	if s := r.Status(context.Background()); s.State != "applied" || s.Desired != cfg3 {
		t.Fatalf("final status = %+v, want applied with %+v", s, cfg3)
	}
}

// TestFreeRAMFailureMapsExactlyOneState pins the state decision of the T93
// flicker fix.
func TestFreeRAMFailureMapsExactlyOneState(t *testing.T) {
	state, message := freeRAMFailure(ErrUnsupported)
	if state != "unsupported" || message == "" {
		t.Fatalf("ErrUnsupported maps to %q / %q", state, message)
	}
	state, _ = freeRAMFailure(errors.New("read /proc/meminfo: no such file"))
	if state != "unavailable" {
		t.Fatalf("a read failure maps to %q, want unavailable", state)
	}
}

// TestApplyUnsupportedEndsInUnsupportedState proves the apply does not dial
// and ends in exactly the "unsupported" state.
func TestApplyUnsupportedEndsInUnsupportedState(t *testing.T) {
	r := New("", "test-user", "test-password")
	r.cfg = Config{"dynamic", 1, 20, 5, "aof_everysec"}
	dialed := false
	r.dial = func(context.Context, string, string) (net.Conn, error) {
		dialed = true
		return nil, errors.New("must not dial")
	}
	r.freeRAM = func() (uint64, error) { return 0, ErrUnsupported }
	if err := r.Apply(context.Background()); err == nil {
		t.Fatal("the freeRAM error was swallowed")
	}
	s := r.Status(context.Background())
	if s.State != "unsupported" {
		t.Fatalf("final state = %q, want unsupported", s.State)
	}
	if dialed {
		t.Fatal("the apply dialed despite the freeRAM failure")
	}
}

// TestStatusNeverObservesUnavailableForUnsupportedOS hammers Status while
// Apply reports an unsupported OS: the transient "unavailable" state of the
// old two-step setState must never be observable.
func TestStatusNeverObservesUnavailableForUnsupportedOS(t *testing.T) {
	r := New("", "test-user", "test-password")
	r.cfg = Config{"dynamic", 1, 20, 5, "aof_everysec"}
	r.dial = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("must not dial")
	}
	r.freeRAM = func() (uint64, error) { return 0, ErrUnsupported }
	stop := make(chan struct{})
	var mu sync.Mutex
	seen := map[string]bool{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			// Record first, then check stop: the applies below finish in
			// microseconds, and the final state must still be sampled.
			s := r.Status(context.Background())
			mu.Lock()
			seen[s.State] = true
			mu.Unlock()
			select {
			case <-stop:
				return
			default:
			}
			runtime.Gosched()
		}
	}()
	appliesDone := make(chan struct{})
	go func() {
		defer close(appliesDone)
		for i := 0; i < 300; i++ {
			_ = r.Apply(context.Background())
		}
	}()
	<-appliesDone
	// Give the observer a moment to drain the final state before stopping it.
	time.Sleep(50 * time.Millisecond)
	close(stop)
	<-done
	mu.Lock()
	defer mu.Unlock()
	for state := range seen {
		if state == "unavailable" {
			t.Fatal("a concurrent Status observed the transient unavailable state")
		}
	}
	if !seen["unsupported"] {
		t.Fatalf("the observer never saw the unsupported state: %v", seen)
	}
}

// TestConfigureDisabledRunnerNeverDials pins that a disabled applier stores
// the config but neither dials nor leaves the disabled state.
func TestConfigureDisabledRunnerNeverDials(t *testing.T) {
	r := New("", "", "")
	dialed := false
	r.dial = func(context.Context, string, string) (net.Conn, error) {
		dialed = true
		return nil, errors.New("must not dial")
	}
	if err := r.Configure(context.Background(), DefaultConfig()); err != nil {
		t.Fatalf("configure on a disabled runner: %v", err)
	}
	if dialed {
		t.Fatal("a disabled runner dialed on Configure")
	}
	if s := r.Status(context.Background()); s.State != "disabled" {
		t.Fatalf("state = %q, want disabled", s.State)
	}
}
