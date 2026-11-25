// Path: cmd/pools-client/main_test.go | Author: GLM (z.ai) | 2026-11-21
// T101 (cmdc leak sweep): client errors are printed through a redaction
// wrapper, so a server that echoes the submitted API key back in an error
// body cannot leak it through fatal() or the TUI loop.
package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"ccgsc/poolscore/internal/redact"
)

func TestSanitizeStripsAPIKeyFromError(t *testing.T) {
	const key = "pc_top-secret-key-value"
	err := fmt.Errorf("thread-pool/v1/auth/token: HTTP 401: {\"apiKey\":\"%s\",\"error\":\"invalid key\"}", key)
	got := sanitize(err, key).Error()
	if strings.Contains(got, key) {
		t.Fatalf("sanitized error still carries the API key: %q", got)
	}
	if !strings.Contains(got, redact.Marker) {
		t.Fatalf("sanitized error has no redaction marker: %q", got)
	}
	if sanitize(nil, key) != nil {
		t.Fatal("sanitize(nil) is not nil")
	}
}

func TestSanitizeKeepsDiagnosis(t *testing.T) {
	const key = "pc_top-secret-key-value"
	got := sanitize(errors.New("thread-pool/v1/jobs: HTTP 404"), key).Error()
	if got != "thread-pool/v1/jobs: HTTP 404" {
		t.Fatalf("sanitized error changed an error without the key: %q", got)
	}
}
