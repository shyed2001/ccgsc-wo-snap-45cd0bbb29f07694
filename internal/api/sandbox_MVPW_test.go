package api

import (
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/web"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServeRunOriginHeadersAndPathGate(t *testing.T) {
	root, err := web.PagesRoot()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Config: config.Config{RunOrigin: "http://127.0.0.1:8792", PublicOrigin: "https://main.example"}}

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8792/runner.html", nil)
	rec := httptest.NewRecorder()
	s.serveRunOrigin(root, rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("runner status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != "default-src 'none'; script-src 'unsafe-inline' 'wasm-unsafe-eval' blob:; worker-src 'self' blob:; connect-src 'none'; frame-ancestors https://main.example" {
		t.Fatalf("runner CSP = %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("runner nosniff = %q", got)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "" {
		t.Fatalf("runner X-Frame-Options = %q, must remain frameable", got)
	}
	if got := rec.Header().Get("Cross-Origin-Resource-Policy"); got != "cross-origin" {
		t.Fatalf("runner CORP = %q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8792/other.js", nil)
	rec = httptest.NewRecorder()
	s.serveRunOrigin(root, rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("other run-origin path status = %d", rec.Code)
	}
}
