// Path: internal/redisadmin/redisadmin_test.go | Author: Codex | 2026-11-20
// T91 acceptance: deterministic plans, strict config, disabled/no-dial and RESP safety.
package redisadmin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPlan(t *testing.T) {
	for _, tc := range []struct {
		name, mode     string
		fixed, percent int
		free, want     uint64
	}{
		{"fixed1", "fixed", 1, 10, 0, GiB}, {"fixed4", "fixed", 4, 10, 0, 4 * GiB},
		{"dynamic10lower", "dynamic", 1, 10, 2 * GiB, GiB},
		{"dynamic10middle", "dynamic", 1, 10, 20 * GiB, 2 * GiB},
		{"dynamic40middle", "dynamic", 1, 40, 5 * GiB, 2 * GiB},
		{"dynamic40upper", "dynamic", 1, 40, 20 * GiB, 4 * GiB},
		{"dynamicZero", "dynamic", 1, 10, 0, GiB},
		{"dynamicOverflow", "dynamic", 1, 40, ^uint64(0), 4 * GiB},
	} {
		for _, persistence := range []string{"aof_everysec", "snapshot"} {
			t.Run(tc.name+"/"+persistence, func(t *testing.T) {
				c := Config{tc.mode, tc.fixed, tc.percent, 5, persistence}
				aof := "yes"
				if persistence == "snapshot" {
					aof = "no"
				}
				want := []Command{{"CONFIG", "SET", "maxmemory-policy", "noeviction"},
					{"CONFIG", "SET", "maxmemory", strconv.FormatUint(tc.want, 10)},
					{"CONFIG", "SET", "appendfsync", "everysec"}, {"CONFIG", "SET", "appendonly", aof}}
				if got := Plan(c, tc.free); !reflect.DeepEqual(got, want) {
					t.Fatalf("plan=%v want=%v", got, want)
				}
			})
		}
	}
	c := DefaultConfig()
	c.FixedGB = 5
	if Plan(c, GiB) != nil {
		t.Fatal("invalid config emitted commands")
	}
}

func TestDecodeAdminConfig(t *testing.T) {
	for _, page := range []string{"thread-pool-admin", "process-pool-admin"} {
		c, found, err := DecodeAdminConfig(page, []byte(`{}`))
		if err != nil || found || c != DefaultConfig() {
			t.Fatalf("defaults: %v %v %v", c, found, err)
		}
		for _, raw := range []string{
			`{"redis.memory.mode":"elastic"}`, `{"redis.memory.mode":null}`,
			`{"redis.memory.fixed_gb":0}`, `{"redis.memory.fixed_gb":5}`, `{"redis.memory.fixed_gb":1.5}`, `{"redis.memory.fixed_gb":"2"}`,
			`{"redis.memory.dynamic_percent":9}`, `{"redis.memory.dynamic_percent":41}`, `{"redis.memory.dynamic_percent":10.1}`,
			`{"redis.memory.interval_min":0}`, `{"redis.memory.interval_min":61}`,
			`{"redis.persistence.mode":"off"}`, `{"redis.persistence.mode":true}`,
			`{"redis.admin.password":"forbidden"}`, `null`, `[]`,
		} {
			if ValidateAdminConfig(page, []byte(raw)) == nil {
				t.Fatalf("accepted %s on %s", raw, page)
			}
		}
		for _, raw := range []string{`{"redis.memory.fixed_gb":1}`, `{"redis.memory.fixed_gb":4}`, `{"redis.memory.dynamic_percent":40}`, `{"redis.memory.interval_min":60}`, `{"redis.persistence.mode":"snapshot"}`} {
			if err := ValidateAdminConfig(page, []byte(raw)); err != nil {
				t.Fatalf("valid %s: %v", raw, err)
			}
		}
	}
	if ValidateAdminConfig("monitor", []byte(`{"redis.memory.fixed_gb":2}`)) == nil {
		t.Fatal("wrong page accepted")
	}
}

func TestDisabledNeverDials(t *testing.T) {
	for _, creds := range [][2]string{{"", ""}, {"admin", ""}, {"", "placeholder"}} {
		r := New("unused", creds[0], creds[1])
		r.dial = func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("disabled applier dialed")
			return nil, nil
		}
		r.freeRAM = func() (uint64, error) { t.Fatal("disabled applier read RAM"); return 0, nil }
		c := DefaultConfig()
		c.Mode = "dynamic"
		if err := r.Configure(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		if err := r.Apply(context.Background()); err != nil {
			t.Fatal(err)
		}
		if r.Interval() != 0 || r.Enabled() {
			t.Fatal("disabled scheduling")
		}
	}
}

type exchange struct {
	command Command
	reply   string
}

// net.Pipe has no sockets or external services. Commands are checked on the wire.
func script(t *testing.T, r *Runner, exchanges []exchange) {
	t.Helper()
	r.dial = func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer server.Close()
			_ = server.SetDeadline(time.Now().Add(3 * time.Second))
			reader := bufio.NewReader(server)
			for _, x := range exchanges {
				v, err := readRESP(reader, 0)
				if err != nil {
					t.Errorf("read command: %v", err)
					return
				}
				args, ok := v.([]any)
				if !ok {
					t.Errorf("command not array")
					return
				}
				got := Command{}
				for _, a := range args {
					got = append(got, a.(string))
				}
				if !reflect.DeepEqual(got, x.command) {
					t.Errorf("unexpected command name or arguments (credentials omitted)")
					return
				}
				if _, err = io.WriteString(server, x.reply); err != nil {
					t.Errorf("reply: %v", err)
					return
				}
			}
			// Ensure the caller closes instead of sending an unlisted mutation.
			if _, err := reader.ReadByte(); err == nil {
				t.Error("unexpected extra command")
			}
		}()
		t.Cleanup(func() { _ = client.Close(); <-done })
		return client, nil
	}
}

func bulk(s string) string    { return fmt.Sprintf("$%d\r\n%s\r\n", len(s), s) }
func pair(k, v string) string { return "*2\r\n" + bulk(k) + bulk(v) }

func TestRunnerApplyAndSnapshotGuard(t *testing.T) {
	for _, mode := range []string{"aof_everysec", "snapshot"} {
		t.Run(mode, func(t *testing.T) {
			r := New("unused", "test-user", "test-password")
			c := DefaultConfig()
			c.Persistence = mode
			xs := []exchange{{Command{"AUTH", "test-user", "test-password"}, "+OK\r\n"}}
			if mode == "snapshot" {
				xs = append(xs, exchange{Command{"CONFIG", "GET", "save"}, pair("save", "900 1 300 10")})
			}
			for _, cmd := range Plan(c, 0) {
				xs = append(xs, exchange{cmd, "+OK\r\n"})
			}
			script(t, r, xs)
			if err := r.Configure(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			if r.state != "applied" {
				t.Fatal(r.state)
			}
		})
	}
	t.Run("no snapshot rules", func(t *testing.T) {
		r := New("unused", "test-user", "test-password")
		script(t, r, []exchange{{Command{"AUTH", "test-user", "test-password"}, "+OK\r\n"}, {Command{"CONFIG", "GET", "save"}, pair("save", "")}})
		c := DefaultConfig()
		c.Persistence = "snapshot"
		if err := r.Configure(context.Background(), c); err == nil || !strings.Contains(err.Error(), "save rules") {
			t.Fatal("empty save rules accepted")
		}
	})
}

func TestRunnerDynamicAndFailures(t *testing.T) {
	r := New("unused", "test-user", "test-password")
	c := DefaultConfig()
	c.Mode = "dynamic"
	c.IntervalMin = 7
	r.freeRAM = func() (uint64, error) { return 20 * GiB, nil }
	xs := []exchange{{Command{"AUTH", "test-user", "test-password"}, "+OK\r\n"}}
	for _, cmd := range Plan(c, 20*GiB) {
		xs = append(xs, exchange{cmd, "+OK\r\n"})
	}
	script(t, r, xs)
	if err := r.Configure(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if r.Interval() != 7*time.Minute {
		t.Fatal("interval not adopted")
	}
	for _, failure := range []error{ErrUnsupported, errors.New("RAM read failed")} {
		r.freeRAM = func() (uint64, error) { return 0, failure }
		r.dial = func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("unsupported dynamic mode dialed")
			return nil, nil
		}
		if err := r.Apply(context.Background()); err == nil {
			t.Fatal("RAM failure ignored")
		}
	}
	if runtime.GOOS != "linux" {
		if _, err := FreeRAMBytes(); !errors.Is(err, ErrUnsupported) {
			t.Fatal(err)
		}
	}
}

func TestNoCredentialsInErrorsOrJSON(t *testing.T) {
	r := New("unused", "test-user", "test-password")
	script(t, r, []exchange{{Command{"AUTH", "test-user", "test-password"}, "-ERR test-password test-user\r\n"}})
	err := r.Apply(context.Background())
	if err == nil || strings.Contains(err.Error(), "test-") || strings.Contains(r.message, "test-") {
		t.Fatal("peer error leaked")
	}
	b, _ := json.Marshal(r)
	if string(b) != "{}" {
		t.Fatal("connection fields serialized")
	}
}

func TestObserverReadOnlyAndActualValues(t *testing.T) {
	r := New("unused", "", "")
	r.readUser = "reader"
	r.readPassword = "test-password"
	script(t, r, []exchange{
		{Command{"AUTH", "reader", "test-password"}, "+OK\r\n"},
		{Command{"INFO", "memory"}, bulk("maxmemory:2147483648\r\nmaxmemory_policy:noeviction\r\n")},
		{Command{"INFO", "persistence"}, bulk("aof_enabled:1\r\n")},
		{Command{"CONFIG", "GET", "appendfsync"}, pair("appendfsync", "everysec")},
	})
	s := r.Status(context.Background())
	if s.Enabled || s.State != "disabled" || s.Current == nil || s.Current.MaxMemoryBytes != 2*GiB || s.Current.Persistence != "aof_everysec" {
		t.Fatalf("status=%+v", s)
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), "test-password") {
		t.Fatal("status leaked credentials")
	}
}

func TestMemAvailable(t *testing.T) {
	n, err := parseMemAvailable(strings.NewReader("MemFree: 4 kB\nMemAvailable: 12345 kB\n"))
	if err != nil || n != 12345*1024 {
		t.Fatalf("%d %v", n, err)
	}
	for _, s := range []string{"MemFree: 123 kB", "MemAvailable: -1 kB", "MemAvailable: 2 MB", "MemAvailable: 18446744073709551615 kB"} {
		if _, err := parseMemAvailable(strings.NewReader(s)); err == nil {
			t.Fatal("invalid meminfo accepted")
		}
	}
}
