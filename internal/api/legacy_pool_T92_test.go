// Path: internal/api/legacy_pool_T92_test.go | Author: Codex | 2026-11-20
// What/why: prove bounded localhost reads, truthful failure states and route auth.
package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
)

func TestT92LegacySummary(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/pool/cluster/summary" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("credentials forwarded")
		}
		io.WriteString(w, `{"ok":true,"totalWorkers":3,"liveWorkers":0,"clusters":{"node":{"total":3,"live":0}},"error":"private details"}`)
	}))
	defer node.Close()
	// A configured proxy must not be contacted, even for localhost.
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyCalls.Add(1); w.WriteHeader(502) }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	for _, origin := range []string{node.URL, strings.Replace(node.URL, "127.0.0.1", "localhost", 1)} {
		got := fetchLegacyPool(context.Background(), origin)
		if !got.Available || got.Source != "node-pool-admin" || got.FetchedAt.IsZero() || got.Reason != "" {
			t.Fatalf("view: %+v", got)
		}
		if len(got.Workers) != 0 || got.DetailsUnavailable == "" {
			t.Fatalf("fabricated workers: %+v", got)
		}
		if got.Raw["totalWorkers"] != int64(3) || got.Raw["liveWorkers"] != int64(0) {
			t.Fatalf("counts: %+v", got.Raw)
		}
		for _, absent := range []string{"staleWorkers", "error", "distinctDevices"} {
			if _, exists := got.Raw[absent]; exists {
				t.Fatalf("invented or leaked %s", absent)
			}
		}
		cluster := got.Raw["clusters"].(map[string]any)["node"].(map[string]any)
		if _, exists := cluster["stale"]; exists {
			t.Fatal("invented stale count")
		}
	}
	if proxyCalls.Load() != 0 {
		t.Fatal("proxy contacted")
	}
}

func TestT92LegacyOptionalWorkers(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"workers":[{"id":"old-1","lane":"thread","pool":"alpha","lastSeen":"2026-11-20T00:00:00Z","status":"live","ip":"private"},{"id":"old-2"}]}`)
	}))
	defer node.Close()
	got := fetchLegacyPool(context.Background(), node.URL)
	if !got.Available || len(got.Workers) != 2 || got.DetailsUnavailable != "" {
		t.Fatalf("view: %+v", got)
	}
	if len(got.Workers[0]) != 5 || got.Workers[0]["pool"] != "alpha" || len(got.Workers[1]) != 1 {
		t.Fatalf("normalization: %+v", got.Workers)
	}
	if len(got.Raw) != 0 {
		t.Fatal("invented counts")
	}
}

func TestT92LegacyFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body, reason string
		status             int
	}{
		{"bad-json", `{`, "upstream_bad_json", 200},
		{"trailing-json", `{"ok":true} {}`, "upstream_bad_json", 200},
		{"null", `null`, "upstream_bad_json", 200},
		{"array", `[]`, "upstream_bad_json", 200},
		{"degraded", `{"ok":false,"degraded":true,"liveTabs":0}`, "upstream_degraded", 200},
		{"degraded-true", `{"ok":true,"degraded":true}`, "upstream_degraded", 200},
		{"negative", `{"ok":true,"totalWorkers":-1}`, "upstream_invalid_counts", 200},
		{"null-count", `{"ok":true,"liveWorkers":null}`, "upstream_invalid_counts", 200},
		{"invalid-cluster", `{"ok":true,"clusters":{"a":null}}`, "upstream_invalid_counts", 200},
		{"invalid-worker", `{"ok":true,"workers":[null]}`, "upstream_invalid_workers", 200},
		{"too-large", strings.Repeat(" ", legacyBodyLimit+1), "upstream_body_too_large", 200},
		{"server-error", `{}`, "upstream_http_error", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); io.WriteString(w, tc.body) }))
			defer node.Close()
			s := &Server{Config: config.Config{LegacyPoolURL: node.URL}}
			rec := httptest.NewRecorder()
			s.readLegacyWorkers(rec, httptest.NewRequest("GET", "/", nil), auth.Claims{}, "")
			var got legacyPoolView
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if rec.Code != 200 || got.Available || got.Reason != tc.reason || len(got.Raw) != 0 || len(got.Workers) != 0 {
				t.Fatalf("HTTP %d: %+v", rec.Code, got)
			}
		})
	}
}

func TestT92LegacyDownSlowAndRedirect(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	origin := down.URL
	down.Close()
	if got := fetchLegacyPool(context.Background(), origin); got.Available || got.Reason != "upstream_unavailable" {
		t.Fatalf("down: %+v", got)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
			io.WriteString(w, `{"ok":true}`)
		}
	}))
	defer slow.Close()
	start := time.Now()
	got := fetchLegacyPool(context.Background(), slow.URL)
	if elapsed := time.Since(start); got.Available || got.Reason != "upstream_unavailable" || elapsed < 1800*time.Millisecond || elapsed > 2800*time.Millisecond {
		t.Fatalf("slow: %+v elapsed %v", got, elapsed)
	}
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); io.WriteString(w, `{"ok":true}`) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redirect.Close()
	if got := fetchLegacyPool(context.Background(), redirect.URL); got.Available || got.Reason != "upstream_http_error" || calls.Load() != 0 {
		t.Fatalf("followed redirect: %+v", got)
	}
}

func TestT92LegacyAuthAndDisabled(t *testing.T) {
	_, svc, _ := workersHarness(t)
	// Trace actual connection attempts, including the bridge's private transport.
	var dials atomic.Int32
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		ConnectStart: func(string, string) { dials.Add(1) },
		DNSStart:     func(httptrace.DNSStartInfo) { dials.Add(1) },
	})
	for _, origin := range []string{"", "http://example.com"} {
		got := fetchLegacyPool(ctx, origin)
		if got.Available || got.Reason == "" {
			t.Fatalf("off/invalid: %+v", got)
		}
	}
	if dials.Load() != 0 {
		t.Fatal("disabled bridge dialed")
	}
	s := &Server{Auth: svc, Config: config.Default()}
	mux := http.NewServeMux()
	s.legacyPoolRoutes(mux)
	for _, tc := range []struct {
		role, scope string
		want        int
	}{
		{"", "", 401}, {"developer", "metrics:read", 403}, {"admin", "config:read", 403},
		{"admin", "metrics:read", 200}, {"director", "metrics:read", 200},
	} {
		t.Run(tc.role+tc.scope, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/admin/v1/legacy-workers", nil)
			if tc.role != "" {
				accountStore := "A"
				if tc.role == "developer" {
					accountStore = "B"
				}
				raw, _, err := svc.CreateKey(context.Background(), auth.User{ID: tc.role + tc.scope, AccountStore: accountStore, Roles: []string{tc.role}}, []string{tc.scope})
				if err != nil {
					t.Fatal(err)
				}
				token, err := svc.Exchange(context.Background(), raw, nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+token.AccessToken)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status %d want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == 200 && !strings.Contains(rec.Body.String(), `"reason":"bridge_disabled"`) {
				t.Fatalf("not disabled: %s", rec.Body.String())
			}
		})
	}
	// Verify the production Handler actually mounts the route, too.
	handler, authService, _ := workersHarness(t)
	req := httptest.NewRequest("GET", "/admin/v1/legacy-workers", nil)
	req.Header.Set("Authorization", "Bearer "+bearerKey(t, authService, "A", "admin"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "bridge_disabled") {
		t.Fatalf("missing production route: %d %s", rec.Code, rec.Body.String())
	}
}
