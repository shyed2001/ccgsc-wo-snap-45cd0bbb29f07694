// Path: internal/store/redislease/client_redact_T101_test.go | Author: GLM (z.ai) | 2026-11-21
// T101 (cmdc leak sweep): a Redis address may carry userinfo credentials, and
// net.Dial errors print the address they were given. Every entry point must
// therefore reject or strip userinfo before dialing, and the String/GoString
// renderings of Options and Client must never carry the password.
package redislease

import (
	"fmt"
	"strings"
	"testing"

	"ccgsc/poolscore/internal/redact"
)

func TestNormalizeAddrRules(t *testing.T) {
	ok := map[string]string{
		"127.0.0.1:6379":           "127.0.0.1:6379",
		" redis://127.0.0.1:6379 ": "127.0.0.1:6379",
		"redis://db.internal:6380": "db.internal:6380",
		"tcp://127.0.0.1:6379":     "127.0.0.1:6379",
	}
	for in, want := range ok {
		got, err := normalizeAddr(in)
		if err != nil || got != want {
			t.Fatalf("normalizeAddr(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{
		"",
		"redis://:secret@127.0.0.1:6379",
		"redis://default:secret@127.0.0.1:6379",
		"redis://user@127.0.0.1:6379",
		"default:secret@127.0.0.1:6379",
		":secret@127.0.0.1:6379",
		"redis://",
		"ftp://127.0.0.1:6379",
	}
	for _, in := range bad {
		if _, err := normalizeAddr(in); err == nil {
			t.Fatalf("normalizeAddr(%q) accepted an invalid address", in)
		}
	}
}

func TestNormalizeAddrErrorNeverEchoesCredential(t *testing.T) {
	for _, in := range []string{
		"redis://default:top-secret-pw@127.0.0.1:6379",
		":top-secret-pw@127.0.0.1:6379",
	} {
		_, err := normalizeAddr(in)
		if err == nil {
			t.Fatalf("normalizeAddr(%q) accepted userinfo", in)
		}
		if strings.Contains(err.Error(), "top-secret-pw") || strings.Contains(err.Error(), "@") {
			t.Fatalf("normalizeAddr error echoes the credential: %q", err.Error())
		}
	}
}

func TestDialAndConnectRejectUserinfoWithoutEcho(t *testing.T) {
	for _, addr := range []string{"redis://:top-secret-pw@127.0.0.1:1", ":top-secret-pw@127.0.0.1:1"} {
		if _, err := DialWithOptions(addr, defaultCommandTimeout, Options{}); err == nil {
			t.Fatalf("DialWithOptions(%q) accepted userinfo", addr)
		} else if strings.Contains(err.Error(), "top-secret-pw") {
			t.Fatalf("DialWithOptions error echoes the credential: %q", err.Error())
		}
	}
	if _, err := Connect("redis", "redis://:top-secret-pw@127.0.0.1:1", Options{}); err == nil {
		t.Fatal("Connect accepted userinfo in the address")
	} else if strings.Contains(err.Error(), "top-secret-pw") {
		t.Fatalf("Connect error echoes the credential: %q", err.Error())
	}
}

func TestDialRedisURLStripsScheme(t *testing.T) {
	addr, stop, _ := newFakeRESP(t)
	defer stop()
	c, err := DialWithOptions("redis://"+addr, defaultCommandTimeout, Options{})
	if err != nil {
		t.Fatalf("DialWithOptions(redis://...) failed: %v", err)
	}
	defer c.Close()
	if c.addr != addr {
		t.Fatalf("dial address = %q, want the stripped host %q", c.addr, addr)
	}
}

func TestOptionsStringersRedactPassword(t *testing.T) {
	o := Options{Username: "pools", Password: "correct-horse", DB: 2, Prefix: "tenant9:"}
	renderings := []string{fmt.Sprintf("%v", o), fmt.Sprintf("%+v", o), fmt.Sprintf("%#v", o), o.String(), o.GoString()}
	for _, r := range renderings {
		if strings.Contains(r, "correct-horse") {
			t.Fatalf("Options rendering %q leaks the password", r)
		}
		if !strings.Contains(r, redact.Marker) {
			t.Fatalf("Options rendering %q has no redaction marker", r)
		}
		if !strings.Contains(r, "pools") || !strings.Contains(r, "tenant9:") {
			t.Fatalf("Options rendering %q lost its diagnosis fields", r)
		}
	}
	empty := Options{}
	if s := empty.String(); strings.Contains(s, redact.Marker) {
		t.Fatalf("empty Options rendering shows a redaction marker: %q", s)
	}
}

func TestClientStringersRedactPassword(t *testing.T) {
	addr, stop, _ := startFakeRESP(t, fakeRESPConfig{username: "pools", password: "correct-horse"})
	defer stop()
	c, err := DialWithOptions(addr, defaultCommandTimeout, Options{Username: "pools", Password: "correct-horse", DB: 0})
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer c.Close()
	renderings := []string{fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), c.String(), c.GoString()}
	for _, r := range renderings {
		if strings.Contains(r, "correct-horse") {
			t.Fatalf("Client rendering %q leaks the password", r)
		}
		if !strings.Contains(r, redact.Marker) {
			t.Fatalf("Client rendering %q has no redaction marker", r)
		}
		if !strings.Contains(r, addr) {
			t.Fatalf("Client rendering %q lost the address", r)
		}
	}
}
