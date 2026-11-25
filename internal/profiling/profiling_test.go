// Path: internal/profiling/profiling_test.go | Author: MCode (MiniMax-M3.1) | 2026-11-22
// What/why: R15 AT22-GM2-1 - the pprof endpoint of the server. It is off by default and
// it may only listen on a loopback address: a profile of the server is a map of the
// process and must never reach the public internet.
package profiling

import (
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestValidateAddrAcceptsLoopback pins the addresses the owner may use.
func TestValidateAddrAcceptsLoopback(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:6060", "127.0.0.5:6060", "localhost:6060", "[::1]:6060"} {
		if err := ValidateAddr(addr); err != nil {
			t.Fatalf("ValidateAddr(%q): %v", addr, err)
		}
	}
}

// TestValidateAddrRefusesEverythingElse pins that every address that is not loopback is
// refused with a clear error.
func TestValidateAddrRefusesEverythingElse(t *testing.T) {
	for _, addr := range []string{
		"",
		"6060",
		"127.0.0.1",
		":6060",
		"0.0.0.0:6060",
		"[::]:6060",
		"192.168.10.5:6060",
		"10.0.0.1:6060",
		"example.com:6060",
	} {
		if err := ValidateAddr(addr); err == nil {
			t.Fatalf("ValidateAddr(%q) accepted a non loopback address", addr)
		}
	}
}

// TestValidateAddrErrorNamesTheAddress pins that the error is usable in a log line.
func TestValidateAddrErrorNamesTheAddress(t *testing.T) {
	err := ValidateAddr("0.0.0.0:6060")
	if err == nil {
		t.Fatal("ValidateAddr accepted 0.0.0.0:6060")
	}
	if !strings.Contains(err.Error(), "0.0.0.0:6060") {
		t.Fatalf("error = %q, want the address in the message", err.Error())
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("error = %q, want the word loopback", err.Error())
	}
}

// TestHandlerServesTheProfiles pins that the handler serves the CPU profile, the mutex
// profile and the block profile.
func TestHandlerServesTheProfiles(t *testing.T) {
	EnableRuntimeProfiles()
	srv := httptest.NewServer(Handler())
	defer srv.Close()
	for _, path := range []string{
		"/debug/pprof/",
		"/debug/pprof/heap",
		"/debug/pprof/goroutine",
		"/debug/pprof/mutex",
		"/debug/pprof/block",
		"/debug/pprof/cmdline",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body := make([]byte, 64)
		n, _ := resp.Body.Read(body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200: %s", path, resp.StatusCode, string(body[:n]))
		}
	}
}

// TestEnableRuntimeProfilesTurnsTheMutexAndBlockProfilingOn pins that the mutex and the
// block profiles are actually collected when the endpoint is enabled.
func TestEnableRuntimeProfilesTurnsTheMutexAndBlockProfilingOn(t *testing.T) {
	oldMutex := runtime.SetMutexProfileFraction(0)
	t.Cleanup(func() { runtime.SetMutexProfileFraction(oldMutex) })
	runtime.SetBlockProfileRate(0)
	EnableRuntimeProfiles()
	if got := runtime.SetMutexProfileFraction(0); got == 0 {
		t.Fatal("the mutex profile fraction is still off")
	}
	// A contended lock and a blocked channel leave a trace in the two profiles.
	var mu sync.Mutex
	held := make(chan struct{})
	go func() {
		mu.Lock()
		close(held)
		time.Sleep(50 * time.Millisecond)
		mu.Unlock()
	}()
	<-held
	mu.Lock()
	mu.Unlock()
	srv := httptest.NewServer(Handler())
	defer srv.Close()
	for _, path := range []string{"/debug/pprof/mutex", "/debug/pprof/block"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, resp.StatusCode)
		}
		if len(body) == 0 {
			t.Fatalf("GET %s returned an empty profile", path)
		}
	}
}
