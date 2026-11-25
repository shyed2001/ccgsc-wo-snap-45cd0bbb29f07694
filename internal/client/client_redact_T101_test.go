// Path: internal/client/client_redact_T101_test.go | Author: GLM (z.ai) | 2026-11-21
// T101 (cmdc leak sweep): REST error bodies are redacted against the API key
// and the bearer token before they become error text. The fake server here
// echoes the request body (and later the Authorization header) back on
// error responses - the hostile echo case the redaction exists for.
package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const redactTestKey = "pc_top-secret-key-value"

// newEchoingServer starts a server whose /auth/token always answers 401 with
// the request body echoed back, and whose other routes answer 404 with the
// Authorization header echoed back.
func newEchoingServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/auth/token") {
			body, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write(body) // hostile echo of the submitted apiKey
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("auth=" + r.Header.Get("Authorization"))) // hostile echo of the bearer token
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRESTErrorBodyNeverCarriesAPIKey(t *testing.T) {
	c, err := New("http://127.0.0.1:1", redactTestKey, false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()
	// Point the client at the echoing server through the same loopback rule.
	c.base = newEchoingServer(t).URL
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = c.Status(ctx, Thread, "job-1")
	if err == nil {
		t.Fatal("expected the 401 echo as an error")
	}
	msg := err.Error()
	if strings.Contains(msg, redactTestKey) {
		t.Fatalf("error text still carries the API key: %q", msg)
	}
	if !strings.Contains(msg, "auth/token") || !strings.Contains(msg, "401") {
		t.Fatalf("error text lost its diagnosis: %q", msg)
	}
}

func TestRESTErrorBodyNeverCarriesBearerToken(t *testing.T) {
	srv := newEchoingServer(t)
	c, err := New(srv.URL, redactTestKey, false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()
	// Seed the token cache so the next call sends a bearer token the server
	// will echo back.
	c.tokenMu.Lock()
	c.tokens[Thread] = tokenEntry{value: "bearer-top-secret-token", validUntil: time.Now().Add(time.Minute)}
	c.tokenMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = c.Status(ctx, Thread, "job-1")
	if err == nil {
		t.Fatal("expected the 404 echo as an error")
	}
	msg := err.Error()
	if strings.Contains(msg, "bearer-top-secret-token") || strings.Contains(msg, redactTestKey) {
		t.Fatalf("error text still carries credential material: %q", msg)
	}
	if !strings.Contains(msg, "404") {
		t.Fatalf("error text lost its diagnosis: %q", msg)
	}
}
