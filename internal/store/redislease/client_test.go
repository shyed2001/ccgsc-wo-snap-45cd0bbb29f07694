// Path: internal/store/redislease/client_test.go
// MVPW-D tests: write tests first (per rules). Fake in-process RESP2 server (no real redis).
// Covers: issue (SET NX), double-issue refused, renew (GET+PEXPIRE), expiry, switch at startup via config.
// Round 8 (R42.7): the fake gains ACL AUTH, SELECT and a per-command log, and new tests cover
// AUTH-before-any-command, SELECT-after-AUTH, wrong-password startup failure, prefix on
// SET/GET/PEXPIRE/DEL and AUTH+SELECT repeated on reconnect.
package redislease

import (
	"bufio"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeEntry struct {
	value   string
	expires time.Time
}

// fakeRESPServer is the in-process RESP2 fake. When a password is configured,
// every command before a successful AUTH is refused with -NOAUTH, exactly like
// a real Redis with an ACL user (R42.7). The command log records each command
// with its key, but never records a credential value.
type fakeRESPServer struct {
	store    *sync.Map // wire key -> fakeEntry
	username string
	password string

	mu  sync.Mutex
	log []string
}

func (s *fakeRESPServer) record(line string) {
	s.mu.Lock()
	s.log = append(s.log, line)
	s.mu.Unlock()
}

func (s *fakeRESPServer) commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.log...)
}

// authMatches validates an AUTH argument list without storing any credential.
// Two arguments are the Redis 6 ACL form AUTH <user> <pass>; one argument is
// the legacy AUTH <pass> form, accepted only when no explicit username exists.
func (s *fakeRESPServer) authMatches(args []string) bool {
	if s.password == "" {
		return false
	}
	if len(args) == 2 {
		return s.username != "" && args[0] == s.username && args[1] == s.password
	}
	if len(args) == 1 {
		return s.username == "" && args[0] == s.password
	}
	return false
}

type fakeRESPConfig struct {
	username string
	password string
}

func startFakeRESP(t *testing.T, cfg fakeRESPConfig) (string, func(), *fakeRESPServer) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &fakeRESPServer{
		store:    &sync.Map{}, // wire key -> fakeEntry
		username: cfg.username,
		password: cfg.password,
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.handleConn(conn)
		}
	}()
	stop := func() {
		_ = ln.Close()
	}
	return ln.Addr().String(), stop, srv
}

// newFakeRESP keeps the original no-auth helper signature for the MVPW-D tests.
func newFakeRESP(t *testing.T) (string, func(), *sync.Map) {
	addr, stop, srv := startFakeRESP(t, fakeRESPConfig{})
	return addr, stop, srv.store
}

func (s *fakeRESPServer) handleConn(conn net.Conn) {
	defer conn.Close()
	rd := bufio.NewReader(conn)
	authed := s.password == ""
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
		case "AUTH":
			if s.authMatches(args[1:]) {
				authed = true
				s.record("AUTH") // credential values are never recorded
				_, _ = conn.Write([]byte("+OK\r\n"))
			} else {
				s.record("AUTH rejected")
				_, _ = conn.Write([]byte("-WRONGPASS invalid username-password pair or user is disabled.\r\n"))
			}
		case "SELECT":
			if !authed {
				_, _ = conn.Write([]byte("-NOAUTH Authentication required.\r\n"))
				continue
			}
			db := 0
			if len(args) > 1 {
				db, _ = strconv.Atoi(args[1])
			}
			s.record("SELECT " + strconv.Itoa(db))
			_, _ = conn.Write([]byte("+OK\r\n"))
		case "SET":
			if !authed {
				_, _ = conn.Write([]byte("-NOAUTH Authentication required.\r\n"))
				continue
			}
			if len(args) < 3 {
				_, _ = conn.Write([]byte("-ERR\r\n"))
				continue
			}
			key, val := args[1], args[2]
			nx := false
			px := 0
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
				if v, ok := s.store.Load(key); ok {
					e := v.(fakeEntry)
					if e.expires.After(now) {
						shouldSet = false
					}
				}
			}
			if shouldSet {
				ex := now.Add(time.Duration(px) * time.Millisecond)
				s.store.Store(key, fakeEntry{val, ex})
				s.record("SET " + key)
				_, _ = conn.Write([]byte("+OK\r\n"))
			} else {
				_, _ = conn.Write([]byte("$-1\r\n"))
			}
		case "GET":
			if !authed {
				_, _ = conn.Write([]byte("-NOAUTH Authentication required.\r\n"))
				continue
			}
			if len(args) < 2 {
				_, _ = conn.Write([]byte("$-1\r\n"))
				continue
			}
			key := args[1]
			s.record("GET " + key)
			if v, ok := s.store.Load(key); ok {
				e := v.(fakeEntry)
				if e.expires.After(now) {
					_, _ = conn.Write([]byte(fmt.Sprintf("$%d\r\n%s\r\n", len(e.value), e.value)))
					continue
				}
			}
			_, _ = conn.Write([]byte("$-1\r\n"))
		case "PEXPIRE":
			if !authed {
				_, _ = conn.Write([]byte("-NOAUTH Authentication required.\r\n"))
				continue
			}
			if len(args) < 3 {
				_, _ = conn.Write([]byte(":0\r\n"))
				continue
			}
			key := args[1]
			ms, _ := strconv.Atoi(args[2])
			s.record("PEXPIRE " + key)
			if v, ok := s.store.Load(key); ok {
				e := v.(fakeEntry)
				if e.expires.After(now) {
					ex := now.Add(time.Duration(ms) * time.Millisecond)
					s.store.Store(key, fakeEntry{e.value, ex})
					_, _ = conn.Write([]byte(":1\r\n"))
					continue
				}
			}
			_, _ = conn.Write([]byte(":0\r\n"))
		case "DEL":
			if !authed {
				_, _ = conn.Write([]byte("-NOAUTH Authentication required.\r\n"))
				continue
			}
			if len(args) < 2 {
				_, _ = conn.Write([]byte(":0\r\n"))
				continue
			}
			key := args[1]
			s.record("DEL " + key)
			if _, ok := s.store.Load(key); ok {
				s.store.Delete(key)
				_, _ = conn.Write([]byte(":1\r\n"))
			} else {
				_, _ = conn.Write([]byte(":0\r\n"))
			}
		default:
			_, _ = conn.Write([]byte("-ERR unknown\r\n"))
		}
	}
}

func TestRedisLease_IssueAndDoubleRefused(t *testing.T) {
	addr, stop, _ := newFakeRESP(t)
	defer stop()

	c, err := Dial(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	key := "lease:job1:task42"
	tok := "tok-abc-123"

	ok, err := c.SetNX(key, tok, 2000)
	if err != nil || !ok {
		t.Fatalf("first issue failed: ok=%v err=%v", ok, err)
	}

	// double issue refused
	ok2, err := c.SetNX(key, "tok-other", 2000)
	if err != nil || ok2 {
		t.Fatalf("double-issue should refuse: ok2=%v err=%v", ok2, err)
	}

	// get should return the token
	got, err := c.Get(key)
	if err != nil || got != tok {
		t.Fatalf("get after issue: got=%q err=%v", got, err)
	}
}

func TestRedisLease_RenewAndExpiry(t *testing.T) {
	addr, stop, _ := newFakeRESP(t)
	defer stop()

	c, err := Dial(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	key := "lease:job2:task99"
	tok := "tok-renew-456"

	ok, _ := c.SetNX(key, tok, 300)
	if !ok {
		t.Fatal("initial set failed")
	}

	// wait a bit, still valid
	time.Sleep(100 * time.Millisecond)
	got, _ := c.Get(key)
	if got != tok {
		t.Fatalf("still valid get failed: %q", got)
	}

	// renew
	if got, _ := c.Get(key); got == tok {
		if err := c.PExpire(key, 1500); err != nil {
			t.Fatalf("pexpire: %v", err)
		}
	}

	// bad renew should not (but we don't enforce in fake beyond)
	// now check expiry by short original (but we renewed)
	time.Sleep(400 * time.Millisecond)
	got, _ = c.Get(key)
	if got == "" {
		t.Fatal("after renew should still be there")
	}

	// now let it expire by waiting
	time.Sleep(1200 * time.Millisecond)
	got, _ = c.Get(key)
	if got != "" {
		t.Fatalf("expected expiry, got %q", got)
	}
}

func TestRedisLease_SettleStillUniqueInStore(t *testing.T) {
	// This exercises that even with redis lease, the settle (result) uniqueness
	// is enforced by the PG/sqlite store side (one accepted result per task).
	// Here we just verify the lease client itself does not affect settle path.
	addr, stop, _ := newFakeRESP(t)
	defer stop()
	c, _ := Dial(addr)
	defer c.Close()

	// simulate lease issue for a task
	key := "lease:job-settle:task1"
	tok := "tok-settle"
	c.SetNX(key, tok, 1000)

	// "settle" would check token in PG job lease + write result under tx unique
	// redis only guarded the issue/renew; here we just confirm client works alongside
	got, _ := c.Get(key)
	if got != tok {
		t.Fatal("lease token not held for settle")
	}
	// del on settle (sim)
	c.Del(key)
	if g, _ := c.Get(key); g != "" {
		t.Fatal("del did not clear for settle path")
	}
}

func TestConnectIfRedis_SwitchAtStartup(t *testing.T) {
	c, err := ConnectIfRedis("pg", "127.0.0.1:1")
	if err != nil || c != nil {
		t.Fatalf("pg must skip dial: client=%v err=%v", c, err)
	}
	if _, err = ConnectIfRedis("redis", ""); err == nil {
		t.Fatal("redis without addr must fail")
	}
	if _, err = ConnectIfRedis("memory", "127.0.0.1:1"); err == nil {
		t.Fatal("invalid mode must fail")
	}
	addr, stop, _ := newFakeRESP(t)
	defer stop()
	rc, err := ConnectIfRedis("redis", addr)
	if err != nil || rc == nil {
		t.Fatalf("redis startup dial: client=%v err=%v", rc, err)
	}
	defer rc.Close()
	ok, err := rc.SetNX("lease:startup:t1", "tok-startup", 800)
	if err != nil || !ok {
		t.Fatalf("issue after redis switch: ok=%v err=%v", ok, err)
	}
}

// --- Round 8 (R42.7): AUTH, SELECT, prefix, reconnect -------------------------

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func countExact(log []string, entry string) int {
	n := 0
	for _, e := range log {
		if e == entry {
			n++
		}
	}
	return n
}

func lastIndex(log []string, entry string) int {
	for i := len(log) - 1; i >= 0; i-- {
		if log[i] == entry {
			return i
		}
	}
	return -1
}

func TestRedisLease_AUTHThenSELECTBeforeAnyLeaseCommand(t *testing.T) {
	addr, stop, srv := startFakeRESP(t, fakeRESPConfig{username: "pools", password: "correct-horse"})
	defer stop()

	c, err := DialWithOptions(addr, defaultCommandTimeout, Options{Username: "pools", Password: "correct-horse", DB: 2})
	if err != nil {
		t.Fatalf("dial with auth: %v", err)
	}
	defer c.Close()

	// The handshake ran inside DialWithOptions: AUTH first, then SELECT,
	// and no other command may precede them.
	log := srv.commands()
	if len(log) < 2 || log[0] != "AUTH" || log[1] != "SELECT 2" {
		t.Fatalf("handshake = %v, want [AUTH, SELECT 2, ...]", log)
	}

	key := "lease:job1:task42"
	ok, err := c.SetNX(key, "tok-abc-123", 2000)
	if err != nil || !ok {
		t.Fatalf("first issue failed: ok=%v err=%v", ok, err)
	}
	if got, err := c.Get(key); err != nil || got != "tok-abc-123" {
		t.Fatalf("get: got=%q err=%v", got, err)
	}
	if err := c.PExpire(key, 1500); err != nil {
		t.Fatalf("pexpire: %v", err)
	}

	// The wire keys carry the configured prefix; the logical key never leaks.
	if _, ok := srv.store.Load("pools:lease:job1:task42"); !ok {
		t.Fatal("wire key missing the pools: prefix")
	}
	if _, ok := srv.store.Load("lease:job1:task42"); ok {
		t.Fatal("unprefixed logical key leaked to the shared server")
	}
	if err := c.Del(key); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.store.Load("pools:lease:job1:task42"); ok {
		t.Fatal("DEL did not clear the prefixed wire key")
	}

	want := []string{
		"AUTH", "SELECT 2",
		"SET pools:lease:job1:task42",
		"GET pools:lease:job1:task42",
		"PEXPIRE pools:lease:job1:task42",
		"DEL pools:lease:job1:task42",
	}
	if got := srv.commands(); !sameStrings(got, want) {
		t.Fatalf("command log = %v, want %v", got, want)
	}
}

func TestRedisLease_WrongPasswordFailsAtStartup(t *testing.T) {
	addr, stop, _ := startFakeRESP(t, fakeRESPConfig{username: "pools", password: "correct-horse"})
	defer stop()

	c, err := DialWithOptions(addr, defaultCommandTimeout, Options{Username: "pools", Password: "wrong-staple"})
	if err == nil {
		_ = c.Close()
		t.Fatal("dial with a wrong password must fail at startup")
	}
	if c != nil {
		t.Fatal("a failed dial must not return a client")
	}
	if !strings.Contains(err.Error(), "AUTH") {
		t.Fatalf("startup error must name AUTH: %v", err)
	}
	// The clear startup error never echoes a credential value.
	if strings.Contains(err.Error(), "wrong-staple") || strings.Contains(err.Error(), "correct-horse") {
		t.Fatalf("startup error must not contain credentials: %v", err)
	}
}

func TestRedisLease_LegacyPasswordOnlyAUTHAndNoSELECTForDB0(t *testing.T) {
	addr, stop, srv := startFakeRESP(t, fakeRESPConfig{password: "legacy-pass"})
	defer stop()

	c, err := DialWithOptions(addr, defaultCommandTimeout, Options{Password: "legacy-pass"})
	if err != nil {
		t.Fatalf("dial with password only: %v", err)
	}
	defer c.Close()

	log := srv.commands()
	if len(log) < 1 || log[0] != "AUTH" {
		t.Fatalf("first command = %v, want AUTH", log)
	}
	if ok, err := c.SetNX("lease:legacy:t1", "tok", 1000); err != nil || !ok {
		t.Fatalf("issue after legacy AUTH: ok=%v err=%v", ok, err)
	}
	// DB 0 is the default DB: no SELECT is sent for it.
	for _, entry := range srv.commands() {
		if strings.HasPrefix(entry, "SELECT") {
			t.Fatalf("SELECT sent for DB 0: %v", srv.commands())
		}
	}
}

func TestRedisLease_ReconnectRepeatsAUTHAndSELECT(t *testing.T) {
	addr, stop, srv := startFakeRESP(t, fakeRESPConfig{username: "pools", password: "correct-horse"})
	defer stop()

	c, err := DialWithOptions(addr, defaultCommandTimeout, Options{Username: "pools", Password: "correct-horse", DB: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if ok, err := c.SetNX("lease:reconn:t1", "tok-r", 8000); err != nil || !ok {
		t.Fatalf("issue: ok=%v err=%v", ok, err)
	}
	if got := countExact(srv.commands(), "AUTH"); got != 1 {
		t.Fatalf("AUTH count before reconnect = %d, want 1", got)
	}

	// Simulate a dropped connection: the next command must reconnect and
	// repeat AUTH + SELECT before it may run.
	c.mu.Lock()
	c.discardConn()
	c.mu.Unlock()

	got, err := c.Get("lease:reconn:t1")
	if err != nil {
		t.Fatalf("get after reconnect: %v", err)
	}
	if got != "tok-r" {
		t.Fatalf("token after reconnect = %q, want tok-r", got)
	}
	log := srv.commands()
	if got := countExact(log, "AUTH"); got != 2 {
		t.Fatalf("AUTH count after reconnect = %d, want 2 (log %v)", got, log)
	}
	lastAUTH := lastIndex(log, "AUTH")
	if lastAUTH+2 >= len(log) || log[lastAUTH+1] != "SELECT 1" || log[lastAUTH+2] != "GET pools:lease:reconn:t1" {
		t.Fatalf("reconnect handshake order = %v, want [AUTH, SELECT 1, GET pools:...]", log[lastAUTH:])
	}
}

func TestRedisLease_DefaultPrefixIsPools(t *testing.T) {
	addr, stop, srv := startFakeRESP(t, fakeRESPConfig{})
	defer stop()

	c, err := Dial(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	if ok, err := c.SetNX("lease:def:t1", "tok", 1000); err != nil || !ok {
		t.Fatalf("issue: ok=%v err=%v", ok, err)
	}
	if _, ok := srv.store.Load("pools:lease:def:t1"); !ok {
		t.Fatal("Dial without options must apply the default pools: prefix (R42.7)")
	}
	if _, ok := srv.store.Load("lease:def:t1"); ok {
		t.Fatal("unprefixed key leaked to the shared server")
	}
}

func TestConnectWithSafeUseOptions(t *testing.T) {
	if c, err := Connect("pg", "127.0.0.1:1", Options{Username: "u", Password: "p"}); err != nil || c != nil {
		t.Fatalf("pg must skip dial: client=%v err=%v", c, err)
	}
	if _, err := Connect("redis", "", Options{}); err == nil {
		t.Fatal("redis without addr must fail")
	}
	if _, err := Connect("memory", "127.0.0.1:1", Options{}); err == nil {
		t.Fatal("invalid mode must fail")
	}

	addr, stop, srv := startFakeRESP(t, fakeRESPConfig{username: "pools", password: "correct-horse"})
	defer stop()
	rc, err := Connect("redis", addr, Options{Username: "pools", Password: "correct-horse", DB: 5, Prefix: "tenant9:"})
	if err != nil || rc == nil {
		t.Fatalf("redis startup dial: client=%v err=%v", rc, err)
	}
	defer rc.Close()

	if ok, err := rc.SetNX("lease:startup:t1", "tok-startup", 800); err != nil || !ok {
		t.Fatalf("issue: ok=%v err=%v", ok, err)
	}
	if _, ok := srv.store.Load("tenant9:lease:startup:t1"); !ok {
		t.Fatal("wire key missing the configured prefix")
	}
	if _, ok := srv.store.Load("lease:startup:t1"); ok {
		t.Fatal("unprefixed key leaked to the shared server")
	}
}
