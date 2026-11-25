// Path: internal/redisadmin/redisadmin_T93_test.go | Author: hermes MiniMax-M3 | 2026-11-20
// T93 acceptance: the runner mutex is never held across network I/O, and one
// reply can never allocate more than maxReplyBytes in total.
package redisadmin

import (
	"bufio"
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestStatusNotBlockedWhileApplyStuck proves that a stuck Apply does not block
// Status. The fake server answers the Status connection normally and never
// answers the Apply connection, so the Apply sits in its AUTH command until
// its context is cancelled. Status must still answer within the 2 s deadline.
func TestStatusNotBlockedWhileApplyStuck(t *testing.T) {
	r := New("unused", "test-user", "test-password")
	r.cfg = DefaultConfig()

	var mu sync.Mutex
	dials := 0
	applyDialed := make(chan struct{})
	release := make(chan struct{})
	defer close(release)

	// The first dial is the Apply, the second is the Status. The Apply
	// connection never gets a reply; the Status connection is scripted.
	r.dial = func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		mu.Lock()
		dials++
		n := dials
		mu.Unlock()
		go func() {
			defer server.Close()
			_ = server.SetDeadline(time.Now().Add(10 * time.Second))
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
				if n == 1 {
					// The Apply connection: stall forever until the
					// test releases it.
					close(applyDialed)
					<-release
					return
				}
				reply, ok := statusReply(cmd)
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

	applyCtx, cancelApply := context.WithCancel(context.Background())
	defer cancelApply()
	applyDone := make(chan error, 1)
	go func() { applyDone <- r.Apply(applyCtx) }()

	select {
	case <-applyDialed:
	case <-time.After(2 * time.Second):
		t.Fatal("the Apply never reached the server")
	}
	// Give the Apply time to block on its unanswered AUTH command.
	time.Sleep(50 * time.Millisecond)

	statusDone := make(chan Status, 1)
	go func() { statusDone <- r.Status(context.Background()) }()

	select {
	case s := <-statusDone:
		if s.Current == nil {
			t.Fatalf("status=%+v", s)
		}
		if s.Current.MaxMemoryBytes != 2*GiB || s.Current.Persistence != "aof_everysec" {
			t.Fatalf("status current=%+v", s.Current)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Status was blocked by the stuck Apply")
	}

	// The Apply is still stuck; it only ends when its context is cancelled.
	select {
	case err := <-applyDone:
		t.Fatalf("the Apply finished early: %v", err)
	default:
	}
	cancelApply()
	select {
	case <-applyDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the Apply did not end after the context was cancelled")
	}
}

// statusReply answers one read-only observer command.
func statusReply(cmd Command) (string, bool) {
	if len(cmd) == 0 {
		return "", false
	}
	switch cmd[0] {
	case "AUTH":
		return "+OK\r\n", true
	case "INFO":
		if len(cmd) > 1 && cmd[1] == "memory" {
			return bulk("maxmemory:2147483648\r\nmaxmemory_policy:noeviction\r\n"), true
		}
		return bulk("aof_enabled:1\r\n"), true
	case "CONFIG":
		if len(cmd) > 2 && cmd[2] == "appendfsync" {
			return pair("appendfsync", "everysec"), true
		}
		return pair("appendfsync", "everysec"), true
	}
	return "", false
}

// TestReplyAllocationCap proves that one reply never allocates more than
// maxReplyBytes in total: the sum of the bulk lengths and the array element
// counts of the whole reply.
func TestReplyAllocationCap(t *testing.T) {
	// A single bulk of exactly the cap is still accepted.
	if v, err := readReply(bulkOfSize(maxReplyBytes)); err != nil {
		t.Fatalf("bulk of %d bytes rejected: %v", maxReplyBytes, err)
	} else if s, ok := v.(string); !ok || len(s) != maxReplyBytes {
		t.Fatalf("bulk of %d bytes read back as %T", maxReplyBytes, v)
	}
	// One byte more is refused before the allocation.
	if _, err := readReply(bulkOfSize(maxReplyBytes + 1)); err == nil {
		t.Fatal("a bulk above the cap was accepted")
	}
	// An array header that claims more elements than the cap allows is refused.
	if _, err := readReply("*1048577\r\n"); err == nil {
		t.Fatal("an array above the cap was accepted")
	}
	// An array of 32 bulk strings of 64 KiB is 2 MiB in total: refused.
	if _, err := readReply(arrayOfBulks(32, 64*1024)); err == nil {
		t.Fatal("an array of 2 MiB in total was accepted")
	}
	// The same shape below the cap is still accepted.
	v, err := readReply(arrayOfBulks(32, 1024))
	if err != nil {
		t.Fatalf("a normal array was rejected: %v", err)
	}
	items, ok := v.([]any)
	if !ok || len(items) != 32 {
		t.Fatalf("array read back as %T", v)
	}
}

// bulkOfSize is a RESP bulk string of n bytes.
func bulkOfSize(n int) string {
	return "$" + strconv.Itoa(n) + "\r\n" + strings.Repeat("x", n) + "\r\n"
}

// arrayOfBulks is a RESP array of count bulk strings of size bytes each.
func arrayOfBulks(count, size int) string {
	var b strings.Builder
	b.WriteString("*" + strconv.Itoa(count) + "\r\n")
	for i := 0; i < count; i++ {
		b.WriteString(bulkOfSize(size))
	}
	return b.String()
}

// readReply serves reply from a fake server and parses it with readRESP.
func readReply(reply string) (any, error) {
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		_ = server.SetDeadline(time.Now().Add(10 * time.Second))
		_, _ = io.WriteString(server, reply)
		// Drain until the reader closes the connection.
		_, _ = io.Copy(io.Discard, server)
	}()
	defer func() {
		_ = client.Close()
		<-done
	}()
	return readRESP(bufio.NewReader(client), 0)
}
