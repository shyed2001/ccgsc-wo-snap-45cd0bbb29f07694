package api

import (
	"ccgsc/poolscore/internal/appegress"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/verify"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestF10bRunnerCSPPerApp(t *testing.T) {
	cfg := capsTestConfig()
	cfg.RunOrigin = "https://run.example.com"
	cfg.PublicOrigin = "https://main.example.com"
	handler, _, e := capsHarnessConfig(t, cfg)
	ctx := context.Background()
	policy := appegress.Policy{Endpoints: []string{"https://api.example.com", "wss://socket.example.com"}}
	if err := e.ApproveAppEgress(ctx, pool.Thread, "dev", "AppAlpha", 1, &policy); err != nil {
		t.Fatal(err)
	}
	if err := e.ApproveAppEgress(ctx, pool.Thread, "other", "AppBeta", 1, &appegress.Policy{Endpoints: []string{"https://beta.example.com"}}); err != nil {
		t.Fatal(err)
	}
	policy, err := e.AppEgressPolicy(ctx, pool.Thread, "dev", "AppAlpha")
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("F10b module")
	id := verify.SHA256(data)
	if _, err = e.Upload(ctx, id, "", 0, int64(len(data)), data); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Create(ctx, pool.Thread, "dev", "", pool.CreateRequest{AppID: "AppAlpha", ModuleArtifactIDs: []string{id}, Tasks: []pool.TaskSpec{{TaskID: "task", Input: json.RawMessage("{}")}}}); err != nil {
		t.Fatal(err)
	}
	w, err := e.Register(ctx, pool.Thread, "contributor", pool.Worker{HostID: "host", BrowserTabID: "tab", Slots: 1, EgressConsent: true, EgressConsentIDs: []string{policy.ConsentID}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.Claim(ctx, pool.Thread, "contributor", w.TabID)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := e.IssueAppEgress(ctx, pool.Thread, "contributor", w.TabID, a.Lease)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		code  int
		csp   string
	}{
		{"", 200, "connect-src 'none';"},
		{"?grant=" + url.QueryEscape(grant.RunnerGrant), 200, "connect-src https://api.example.com wss://socket.example.com;"},
		{"?grant=" + url.QueryEscape(grant.Token), 404, ""},
		{"?grant=invalid", 404, ""},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, cfg.RunOrigin+"/runner.html"+tc.query, nil))
		if rec.Code != tc.code {
			t.Fatalf("runner status %d want %d", rec.Code, tc.code)
		}
		csp := rec.Header().Get("Content-Security-Policy")
		if tc.csp != "" && (!strings.Contains(csp, tc.csp) || strings.Contains(csp, "beta.example.com")) {
			t.Fatalf("incorrect app CSP %s", csp)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, cfg.PublicOrigin+"/.well-known/ccgsc-jwks.json", nil))
	var jwks appegress.JWKS
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &jwks) != nil || len(jwks.Keys) != 1 || jwks.Keys[0].Curve != "Ed25519" {
		t.Fatal("JWKS contract failed")
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, cfg.RunOrigin+"/runner.html?grant="+url.QueryEscape(grant.RunnerGrant), nil))
	// F10b-20: pin the complete header, not only the approved substrings.
	if got := rec.Header().Get("Content-Security-Policy"); got != "default-src 'none'; script-src 'unsafe-inline' 'wasm-unsafe-eval' blob:; worker-src 'self' blob:; connect-src https://api.example.com wss://socket.example.com; frame-ancestors "+cfg.PublicOrigin {
		t.Fatalf("exact runner CSP: %s", got)
	}
	if rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("missing referrer policy")
	}
}
