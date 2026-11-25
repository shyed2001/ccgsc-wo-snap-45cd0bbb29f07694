// Path: internal/pool/rerun_fence_r10_test.go | R10 fix lane (web review of fe646643).
// What/why: the glm MAJOR row against the re-run dispatch. With lease_store=redis
// the claim fence lives in Redis ("lease:<jobId>:<taskId>", SET NX PX). The
// dispatch of a sampled re-run cleared task.Lease and requeued the task without
// releasing that fence, so a second worker's claim still lost SET NX and the
// re-run could only wait out the lease TTL - or the whole re-run window - with
// the task looking queued but unclaimable.
//
// The fake below is a minimal RESP2 server for the commands the client uses.
package pool

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ccgsc/poolscore/internal/store/redislease"
	"ccgsc/poolscore/internal/verify"
)

// r10FakeRedis is a RESP2 stand-in that stores SET NX values with their expiry.
type r10FakeRedis struct {
	mu    sync.Mutex
	store map[string]r10FakeEntry
}

type r10FakeEntry struct {
	value   string
	expires time.Time
}

func r10StartFake(t *testing.T) (*r10FakeRedis, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s := &r10FakeRedis{store: map[string]r10FakeEntry{}}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s, ln.Addr().String()
}

func (s *r10FakeRedis) serve(conn net.Conn) {
	defer conn.Close()
	rd := bufio.NewReader(conn)
	for {
		args, err := r10ReadCommand(rd)
		if err != nil {
			return
		}
		switch strings.ToUpper(args[0]) {
		case "SET":
			key, val := args[1], args[2]
			nx := false
			px := 0
			for i := 3; i < len(args); i++ {
				switch strings.ToUpper(args[i]) {
				case "NX":
					nx = true
				case "PX":
					px, _ = strconv.Atoi(args[i+1])
				}
			}
			s.mu.Lock()
			cur, ok := s.store[key]
			live := ok && cur.expires.After(time.Now())
			if nx && live {
				s.mu.Unlock()
				_, _ = conn.Write([]byte("$-1\r\n"))
				continue
			}
			s.store[key] = r10FakeEntry{val, time.Now().Add(time.Duration(px) * time.Millisecond)}
			s.mu.Unlock()
			_, _ = conn.Write([]byte("+OK\r\n"))
		case "GET":
			s.mu.Lock()
			e, ok := s.store[args[1]]
			s.mu.Unlock()
			if !ok || !e.expires.After(time.Now()) {
				_, _ = conn.Write([]byte("$-1\r\n"))
				continue
			}
			_, _ = conn.Write([]byte(fmt.Sprintf("$%d\r\n%s\r\n", len(e.value), e.value)))
		case "DEL":
			s.mu.Lock()
			delete(s.store, args[1])
			s.mu.Unlock()
			_, _ = conn.Write([]byte(":1\r\n"))
		case "PEXPIRE":
			s.mu.Lock()
			if e, ok := s.store[args[1]]; ok {
				ms, _ := strconv.Atoi(args[2])
				e.expires = time.Now().Add(time.Duration(ms) * time.Millisecond)
				s.store[args[1]] = e
			}
			s.mu.Unlock()
			_, _ = conn.Write([]byte(":1\r\n"))
		case "AUTH", "SELECT":
			_, _ = conn.Write([]byte("+OK\r\n"))
		default:
			_, _ = conn.Write([]byte("-ERR unsupported\r\n"))
		}
	}
}

func r10ReadCommand(rd *bufio.Reader) ([]string, error) {
	line, err := rd.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, fmt.Errorf("empty")
	}
	n, err := strconv.Atoi(strings.TrimPrefix(line, "*"))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		hdr, err := rd.ReadString('\n')
		if err != nil {
			return nil, err
		}
		ln, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(hdr), "$"))
		if err != nil {
			return nil, err
		}
		buf := make([]byte, ln+2)
		if _, err := readFull(rd, buf); err != nil {
			return nil, err
		}
		out = append(out, string(buf[:ln]))
	}
	return out, nil
}

func readFull(rd *bufio.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := rd.Read(buf[n:])
		if err != nil {
			return n, err
		}
		n += m
	}
	return n, nil
}

// r10RedisEngine builds a harness whose engine uses the fake redis lease store.
func r10RedisEngine(t *testing.T) (*harness, *r10FakeRedis, string) {
	t.Helper()
	h := newHarness(t)
	fake, addr := r10StartFake(t)
	client, err := redislease.Dial(addr)
	if err != nil {
		t.Fatalf("redislease.Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	h.engine.SetLeaseClient("redis", client)
	return h, fake, addr
}

// TestR10RerunDispatchReleasesTheClaimFence is the glm MAJOR row.
//
// A 100 % re-run sample requeues the task for a second worker. The claim fence of
// the first lease must be released with it, or the next claim answers
// ErrLeaseContended and the re-run cannot be verified.
func TestR10RerunDispatchReleasesTheClaimFence(t *testing.T) {
	h, fake, _ := r10RedisEngine(t)
	ctx := context.Background()
	hundred := 100.0
	j := h.seed(t, 1, &hundred)

	first := h.worker(t, 1)
	a, err := h.engine.Claim(ctx, Thread, "contributor", first.TabID)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	out := []byte(`{"answer":1}`)
	res, err := h.engine.Submit(ctx, Thread, "contributor", first.TabID, Submission{
		Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: out, SHA256: verify.SHA256(out),
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !res.Rerun {
		t.Fatalf("the sampled task was not dispatched for a re-run: %+v", res)
	}
	// The client applies the configured wire prefix exactly once.
	fence := redislease.DefaultPrefix + "lease:" + j.JobID + ":" + a.Lease.TaskID
	fake.mu.Lock()
	entry, held := fake.store[fence]
	fake.mu.Unlock()
	if held {
		t.Fatalf("the re-run dispatch kept the claim fence %s (token %q)", fence, entry.value)
	}

	second := h.worker(t, 1)
	// The dispatch excludes the first worker, so the second claim may legitimately
	// find no eligible task; the fence is what must not block it.
	if _, err := h.engine.Claim(ctx, Thread, "contributor", second.TabID); err != nil && !errors.Is(err, ErrEmpty) {
		t.Fatalf("the second worker's claim failed on the fence, not on eligibility: %v", err)
	}
}
