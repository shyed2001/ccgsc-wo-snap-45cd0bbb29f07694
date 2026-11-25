// Path: internal/client/envelope_r17_test.go | Author: cmdc | 2026-11-22
// WO R17-ENVELOPE: the CLI reads the additive message/hint of a REST problem
// and falls back to detail when a server still sends only the old body.
package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// r17ProblemServer answers the token exchange and then serves one problem body
// for the capabilities route.
func r17ProblemServer(t *testing.T, problem string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/auth/token"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"accessToken":"r17-token","tokenType":"Bearer","expiresIn":900}`)
		case strings.HasSuffix(r.URL.Path, "/capabilities"):
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, problem)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// TestR17ClientReadsMessageAndHint covers a new server: message and hint are
// preferred and both reach the CLI error text.
func TestR17ClientReadsMessageAndHint(t *testing.T) {
	server := r17ProblemServer(t, `{"type":"about:blank","title":"Bad Request","status":400,"detail":"legacy detail","message":"envelope message","hint":"envelope hint","requestId":"req-1","retryable":false,"code":"invalid-request","traceId":"req-1"}`)
	c, err := New(server.URL, "pc_r17_key", false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Capabilities(context.Background(), Thread)
	if err == nil {
		t.Fatal("Capabilities accepted an error response")
	}
	text := err.Error()
	if !strings.Contains(text, "envelope message") {
		t.Fatalf("error text lost the message: %q", text)
	}
	if !strings.Contains(text, "envelope hint") {
		t.Fatalf("error text lost the hint: %q", text)
	}
}

// TestR17ClientFallsBackToDetail covers an old server: with no message the
// detail is the error text, and the raw JSON is not echoed.
func TestR17ClientFallsBackToDetail(t *testing.T) {
	server := r17ProblemServer(t, `{"type":"about:blank","title":"Bad Request","status":400,"detail":"legacy detail","code":"invalid-request","traceId":"req-1","retryable":false}`)
	c, err := New(server.URL, "pc_r17_key", false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Capabilities(context.Background(), Thread)
	if err == nil {
		t.Fatal("Capabilities accepted an error response")
	}
	text := err.Error()
	if !strings.Contains(text, "legacy detail") {
		t.Fatalf("error text lost the detail fallback: %q", text)
	}
	if strings.Contains(text, `"detail"`) {
		t.Fatalf("error text still carries the raw JSON body: %q", text)
	}
}
