// Path: tests/mvpw_d_test.go
// MVPW-D: redis lease fencing + PG/sqlite settle uniqueness (one accepted result per task).
package tests

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store/redislease"
	"ccgsc/poolscore/internal/verify"
)

type mvpwDEntry struct {
	value   string
	expires time.Time
}

func startMVPWDFakeRESP(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	store := &sync.Map{}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go mvpwDHandle(conn, store)
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

func mvpwDHandle(conn net.Conn, store *sync.Map) {
	defer conn.Close()
	rd := bufio.NewReader(conn)
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "*") {
			_, _ = conn.Write([]byte("-ERR bad array\r\n"))
			continue
		}
		n, _ := strconv.Atoi(strings.TrimPrefix(line, "*"))
		args := make([]string, 0, n)
		for i := 0; i < n; i++ {
			l, _ := rd.ReadString('\n')
			l = strings.TrimSpace(l)
			if !strings.HasPrefix(l, "$") {
				break
			}
			lnum, _ := strconv.Atoi(strings.TrimPrefix(l, "$"))
			buf := make([]byte, lnum+2)
			_, _ = rd.Read(buf)
			args = append(args, string(buf[:lnum]))
		}
		if len(args) == 0 {
			continue
		}
		cmd := strings.ToUpper(args[0])
		now := time.Now()
		switch cmd {
		case "SET":
			if len(args) < 3 {
				_, _ = conn.Write([]byte("-ERR\r\n"))
				continue
			}
			key, val := args[1], args[2]
			nx, px := false, 0
			for j := 3; j < len(args); j++ {
				u := strings.ToUpper(args[j])
				if u == "NX" {
					nx = true
				}
				if u == "PX" && j+1 < len(args) {
					px, _ = strconv.Atoi(args[j+1])
				}
			}
			shouldSet := true
			if nx {
				if v, ok := store.Load(key); ok {
					e := v.(mvpwDEntry)
					if e.expires.After(now) {
						shouldSet = false
					}
				}
			}
			if shouldSet {
				store.Store(key, mvpwDEntry{val, now.Add(time.Duration(px) * time.Millisecond)})
				_, _ = conn.Write([]byte("+OK\r\n"))
			} else {
				_, _ = conn.Write([]byte("$-1\r\n"))
			}
		case "GET":
			if len(args) < 2 {
				_, _ = conn.Write([]byte("$-1\r\n"))
				continue
			}
			if v, ok := store.Load(args[1]); ok {
				e := v.(mvpwDEntry)
				if e.expires.After(now) {
					_, _ = conn.Write([]byte(fmt.Sprintf("$%d\r\n%s\r\n", len(e.value), e.value)))
					continue
				}
			}
			_, _ = conn.Write([]byte("$-1\r\n"))
		case "PEXPIRE":
			if len(args) < 3 {
				_, _ = conn.Write([]byte(":0\r\n"))
				continue
			}
			ms, _ := strconv.Atoi(args[2])
			if v, ok := store.Load(args[1]); ok {
				e := v.(mvpwDEntry)
				if e.expires.After(now) {
					store.Store(args[1], mvpwDEntry{e.value, now.Add(time.Duration(ms) * time.Millisecond)})
					_, _ = conn.Write([]byte(":1\r\n"))
					continue
				}
			}
			_, _ = conn.Write([]byte(":0\r\n"))
		case "DEL":
			if len(args) < 2 {
				_, _ = conn.Write([]byte(":0\r\n"))
				continue
			}
			if _, ok := store.Load(args[1]); ok {
				store.Delete(args[1])
				_, _ = conn.Write([]byte(":1\r\n"))
			} else {
				_, _ = conn.Write([]byte(":0\r\n"))
			}
		default:
			_, _ = conn.Write([]byte("-ERR unknown\r\n"))
		}
	}
}

func TestMVPWD_RedisLeaseSettleStillUniqueInStore(t *testing.T) {
	f := setup(t)
	addr, stop := startMVPWDFakeRESP(t)
	defer stop()
	rc, err := redislease.Dial(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = rc.Close() })
	f.engine.SetLeaseClient("redis", rc)

	ctx := context.Background()
	j, w := seed(t, f, 1)
	a, err := f.engine.Claim(ctx, pool.Thread, "contributor", w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	sub := pool.Submission{Lease: a.Lease, TaskEnvelope: a.TaskEnvelope, LeaseReceipt: a.LeaseReceipt, Output: json.RawMessage(`{}`), SHA256: verify.SHA256([]byte(`{}`))}
	if _, err = f.engine.Submit(ctx, pool.Thread, "contributor", w.TabID, sub); err != nil {
		t.Fatalf("first settle: %v", err)
	}
	_, err = f.engine.Submit(ctx, pool.Thread, "contributor", w.TabID, sub)
	if err == nil || !errors.Is(err, pool.ErrConflict) {
		t.Fatalf("second settle want ErrConflict, got %v", err)
	}
	got, err := f.engine.Get(ctx, pool.Thread, "developer", j.JobID)
	if err != nil || len(got.Results) != 1 {
		t.Fatalf("settle uniqueness: err=%v results=%d", err, len(got.Results))
	}
}
