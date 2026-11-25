// Path: internal/httpx/envelope_r17_test.go | Author: cmdc | 2026-11-22
// WO R17-ENVELOPE: every problem body keeps the RFC 9457 fields it has today
// and adds message (=detail), hint, requestId (=traceId / X-Request-ID) and a
// per-error retryable flag whose default follows 429/503. The table covers
// every machine-readable code the REST surface emits.
package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
)

// r17Case is one documented rest status/code pair.
type r17Case struct {
	status    int
	code      string
	retryable bool
}

// r17Cases is the full code list of the R15 mapping table, plus the codes the
// route-level problems already emit.
var r17Cases = []r17Case{
	{400, "invalid-request", false},
	{400, "invalid-key", false},
	{400, "invalid-json", false},
	{400, "invalid-body", false},
	{400, "invalid-config", false},
	{400, "invalid-token-id", false},
	{400, "invalid-key-id", false},
	{400, "invalid-subject", false},
	{400, "invalid-metric-filter", false},
	{400, "invalid-expires-at", false},
	{400, "expired-expires-at", false},
	{400, "expires-at-too-far", false},
	{400, "invalid-caps", false},
	{401, "unauthorized", false},
	{403, "forbidden", false},
	{403, "artifact-rejected", false},
	{403, "invalid-csrf", false},
	{403, "missing-csrf", false},
	{403, "anonymous-disabled", false},
	{404, "not-found", false},
	{404, "config-not-found", false},
	{409, "state-conflict", false},
	{409, "artifact-pending", false},
	{409, "config-conflict", false},
	{409, "runner-stale", false},
	{409, "capacity-unavailable", true},
	{413, "artifact-too-large", false},
	{413, "bulk-too-large", false},
	{422, "artifact-rejected", false},
	{429, "quota-exceeded", true},
	{429, "anonymous-quota-exceeded", true},
	{429, "rate-limit-exceeded", true},
	{500, "internal-error", false},
	{500, "stream-unavailable", false},
	{500, "rotation-failed", false},
	{500, "rotation-audit-failed", false},
	{500, "password-change-failed", false},
	{500, "caps-write-failed", false},
	{501, "not-implemented", false},
	{503, "config-store-unavailable", true},
	{503, "config-schema-unavailable", false},
	{503, "service-unavailable", true},
	{503, "renewal-unavailable", true},
}

// r17NonEmptyString fails when the decoded body field is not a non-empty string.
func r17NonEmptyString(t *testing.T, body map[string]any, key string) {
	t.Helper()
	v, ok := body[key].(string)
	if !ok {
		t.Fatalf("body[%q] = %#v, want a string", key, body[key])
	}
	if v == "" {
		t.Fatalf("body[%q] is empty; every envelope field must be non-empty: %v", key, body)
	}
}

// TestR17ProblemCarriesEnvelopeFields is the table test of every error code:
// status, code and all envelope fields, old and new, must be present and
// non-empty, and retryable must follow the per-error rule.
func TestR17ProblemCarriesEnvelopeFields(t *testing.T) {
	for _, tc := range r17Cases {
		t.Run(tc.code, func(t *testing.T) {
			rec := httptest.NewRecorder()
			rec.Header().Set("X-Request-ID", "req-r17")
			r := httptest.NewRequest(http.MethodGet, "/thread-pool/v1/jobs/x", nil)
			Problem(rec, r, tc.status, tc.code, "the detail of the failure")

			if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
				t.Fatalf("content type = %q", got)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("problem body is not JSON: %v (%s)", err, rec.Body.String())
			}
			// The fields that already exist today (backward compatibility).
			for _, key := range []string{"type", "title", "detail", "instance", "code", "traceId"} {
				r17NonEmptyString(t, body, key)
			}
			// The additive envelope fields.
			for _, key := range []string{"message", "hint", "requestId"} {
				r17NonEmptyString(t, body, key)
			}
			if body["message"] != body["detail"] {
				t.Fatalf("message = %#v, detail = %#v; message must equal detail", body["message"], body["detail"])
			}
			if body["requestId"] != body["traceId"] || body["requestId"] != "req-r17" {
				t.Fatalf("requestId = %#v, traceId = %#v, want both %q", body["requestId"], body["traceId"], "req-r17")
			}
			if body["status"] != float64(tc.status) {
				t.Fatalf("status = %#v, want %d", body["status"], tc.status)
			}
			if body["code"] != tc.code {
				t.Fatalf("code = %#v, want %q", body["code"], tc.code)
			}
			if body["retryable"] != tc.retryable {
				t.Fatalf("retryable = %#v, want %v", body["retryable"], tc.retryable)
			}
		})
	}
}

// TestR17ErrorMappingStaysPinned keeps the sentinel-to-status/code mapping of
// httpx.Error unchanged while the envelope fields grow.
func TestR17ErrorMappingStaysPinned(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"anonymous quota", pool.ErrAnonymousQuota, 429, "anonymous-quota-exceeded"},
		{"artifact pending", pool.ErrArtifactPending, 409, "artifact-pending"},
		{"artifact rejected", pool.ErrArtifactRejected, 403, "artifact-rejected"},
		{"invalid", pool.ErrInvalid, 400, "invalid-request"},
		{"runner stale", pool.ErrRunnerStale, 409, "runner-stale"},
		{"conflict", pool.ErrConflict, 409, "state-conflict"},
		{"quota", &pool.QuotaError{RetryAfter: 5 * time.Second, Reason: "quota"}, 429, "quota-exceeded"},
		{"forbidden", pool.ErrForbidden, 403, "forbidden"},
		{"unauthorized", auth.ErrUnauthorized, 401, "unauthorized"},
		{"not found", store.ErrNotFound, 404, "not-found"},
		{"unsupported", pool.ErrUnsupported, 501, "not-implemented"},
		{"empty", pool.ErrEmpty, 409, "capacity-unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			rec.Header().Set("X-Request-ID", "req-r17")
			r := httptest.NewRequest(http.MethodGet, "/thread-pool/v1/jobs/x", nil)
			Error(rec, r, tc.err)
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("problem body is not JSON: %v", err)
			}
			if body["status"] != float64(tc.status) || body["code"] != tc.code {
				t.Fatalf("got status=%#v code=%#v, want %d %q", body["status"], body["code"], tc.status, tc.code)
			}
			for _, key := range []string{"message", "hint", "requestId"} {
				r17NonEmptyString(t, body, key)
			}
			if body["message"] != body["detail"] {
				t.Fatalf("message = %#v, detail = %#v", body["message"], body["detail"])
			}
		})
	}
}

// TestR17HintAndRetryableArePerError pins the two additive decisions: hint is
// never empty, and retryable is per error with the 429/503 default.
func TestR17HintAndRetryableArePerError(t *testing.T) {
	for _, tc := range r17Cases {
		if got := HintFor(tc.code, tc.status); got == "" {
			t.Errorf("HintFor(%q, %d) is empty", tc.code, tc.status)
		}
		if got := RetryableFor(tc.code, tc.status); got != tc.retryable {
			t.Errorf("RetryableFor(%q, %d) = %v, want %v", tc.code, tc.status, got, tc.retryable)
		}
	}
	// A 409 that a retry can resolve and a 503 that a retry cannot.
	if !RetryableFor("capacity-unavailable", 409) {
		t.Error("capacity-unavailable must be retryable")
	}
	if RetryableFor("config-schema-unavailable", 503) {
		t.Error("config-schema-unavailable must not be retryable")
	}
	// The default is status-derived for unknown codes.
	if !RetryableFor("an-unknown-code", 429) || !RetryableFor("an-unknown-code", 503) {
		t.Error("the default must mark 429 and 503 retryable")
	}
	if RetryableFor("an-unknown-code", 500) {
		t.Error("the default must not mark 500 retryable")
	}
}
