// Path: internal/httpx/httpx.go | Author: Codex for Claude Opus 5.5 | 2026-11-17
// What/why: consistent bounded JSON and RFC 9457 errors for both route families; WWPP-R32.8.
package httpx

import (
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already on the wire, so the response cannot be
		// replaced; record the truncated body instead of dropping the failure.
		slog.Default().Warn("httpx.encode_failed", "path", "json", "status", status, "error", err)
	}
}

// hintByCode is the actionable hint of one machine-readable code. A code
// without an entry falls back to the hint of its status, so the field is never
// empty (WO R17-ENVELOPE).
var hintByCode = map[string]string{
	"invalid-request":           "Correct the request fields and retry.",
	"invalid-key":               "Send the key in the request body as apiKey.",
	"invalid-json":              "Send exactly one valid JSON value.",
	"invalid-body":              "Send a readable request body.",
	"invalid-config":            "Send a complete config, a reason and a version.",
	"invalid-token-id":          "Send the tokenId of the token to deny.",
	"invalid-key-id":            "Send the keyId of the key to revoke.",
	"invalid-subject":           "Send an anonymous subject with the anon- prefix.",
	"invalid-metric-filter":     "Use one of the documented metric filters.",
	"invalid-expires-at":        "Send expiresAt as an RFC 3339 timestamp.",
	"expired-expires-at":        "Send an expiresAt in the future.",
	"expires-at-too-far":        "Send an expiresAt inside the maximum token lifetime.",
	"invalid-caps":              "Send known, non-negative cap fields.",
	"unauthorized":              "Send a valid bearer token or sign in again.",
	"forbidden":                 "Use a credential with the required role and scope.",
	"artifact-rejected":         "Inspect the scan report and upload a clean artifact.",
	"invalid-csrf":              "Send the CSRF token of the current admin session.",
	"missing-csrf":              "Send the CSRF token of the current admin session.",
	"anonymous-disabled":        "Sign in or use an API key; anonymous join is off.",
	"not-found":                 "Check the identifier and retry.",
	"config-not-found":          "Check the page name or version and retry.",
	"state-conflict":            "Reload the current state and retry.",
	"artifact-pending":          "Wait for the artifact review to finish, then retry.",
	"config-conflict":           "Reload the latest version and retry with expectedVersion.",
	"runner-stale":              "Register the runner again and retry.",
	"capacity-unavailable":      "Retry when a worker is available.",
	"artifact-too-large":        "Upload a smaller artifact.",
	"bulk-too-large":            "Request a smaller bulk page.",
	"quota-exceeded":            "Wait for Retry-After seconds and retry.",
	"anonymous-quota-exceeded":  "Wait for Retry-After seconds and retry.",
	"rate-limit-exceeded":       "Wait for Retry-After seconds and retry.",
	"internal-error":            "Inspect the server logs; retry after the fault is cleared.",
	"stream-unavailable":        "Use a server that supports streaming responses.",
	"rotation-failed":           "Inspect the server key state before retrying.",
	"rotation-audit-failed":     "Inspect the server before retrying.",
	"password-change-failed":    "Check the password rules and retry.",
	"caps-write-failed":         "Inspect the server caps state before retrying.",
	"not-implemented":           "This feature is not implemented; do not retry.",
	"config-store-unavailable":  "Provision PostgreSQL with the pool_config schema and retry.",
	"config-schema-unavailable": "Provision the config schema; retrying will not help.",
	"service-unavailable":       "Retry later when the service is available.",
	"renewal-unavailable":       "Retry later when token renewal is available.",
}

// retryableByCode is the per-error answer of the retryable field. A code
// without an entry uses the default rule of RetryableFor: 429 and 503 retry,
// every other status does not.
var retryableByCode = map[string]bool{
	// A 409 that resolves when a worker joins the pool.
	"capacity-unavailable": true,
	// A 503 that needs provisioning; the same request will keep failing.
	"config-schema-unavailable": false,
}

// HintFor returns the actionable hint of one failure. It is never empty.
func HintFor(code string, status int) string {
	if hint, ok := hintByCode[code]; ok {
		return hint
	}
	switch status {
	case 400:
		return "Correct the request and retry."
	case 401:
		return "Send a valid bearer token or sign in again."
	case 403:
		return "Use a credential with the required role and scope."
	case 404:
		return "Check the identifier and retry."
	case 409:
		return "Reload the current state and retry."
	case 413:
		return "Send a smaller payload."
	case 422:
		return "Correct the request and retry."
	case 429:
		return "Wait for Retry-After seconds and retry."
	case 500:
		return "Inspect the server logs; retry after the fault is cleared."
	case 501:
		return "This feature is not implemented; do not retry."
	case 503:
		return "Retry later when the service is available."
	}
	return "Inspect the error detail before retrying."
}

// RetryableFor reports whether the caller may retry the same request. It is
// per error, with the historical 429/503 rule as the default.
func RetryableFor(code string, status int) bool {
	if retryable, ok := retryableByCode[code]; ok {
		return retryable
	}
	return status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable
}

// ProblemFields builds the error envelope shared by the REST body, the SSE
// error frame and the gRPC ErrorDetail. The first fields are the RFC 9457 body
// that shipped before WO R17; message (=detail), hint and requestId (=traceId)
// are additive and every one of them is always populated.
func ProblemFields(r *http.Request, w http.ResponseWriter, status int, code, detail string) map[string]any {
	requestID := w.Header().Get("X-Request-ID")
	return map[string]any{
		"type":      "about:blank",
		"title":     http.StatusText(status),
		"status":    status,
		"detail":    detail,
		"instance":  r.URL.Path,
		"code":      code,
		"traceId":   requestID,
		"retryable": RetryableFor(code, status),
		"message":   detail,
		"hint":      HintFor(code, status),
		"requestId": requestID,
	}
}

func Problem(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(ProblemFields(r, w, status, code, detail)); err != nil {
		slog.Default().Warn("httpx.encode_failed", "path", r.URL.Path, "status", status, "code", code, "error", err)
	}
}
func Error(w http.ResponseWriter, r *http.Request, e error) {
	status, code, detail := 500, "internal-error", "The operation could not be completed."
	var quota *pool.QuotaError
	switch {
	case errors.Is(e, pool.ErrAnonymousQuota):
		status, code = 429, "anonymous-quota-exceeded"
		detail = e.Error()
		w.Header().Set("Retry-After", "60")
	case errors.Is(e, pool.ErrArtifactPending):
		status, code, detail = 409, "artifact-pending", e.Error()
		w.Header().Set("Retry-After", "30")
	case errors.Is(e, pool.ErrArtifactRejected):
		status, code, detail = 403, "artifact-rejected", e.Error()
	case errors.Is(e, pool.ErrInvalid):
		status, code = 400, "invalid-request"
		detail = e.Error()
	case errors.Is(e, pool.ErrRunnerStale):
		status, code = 409, "runner-stale"
		detail = e.Error()
		w.Header().Set("Retry-After", "60")
	case errors.Is(e, pool.ErrConflict):
		status, code = 409, "state-conflict"
		detail = e.Error()
	case errors.As(e, &quota):
		status, code = 429, "quota-exceeded"
		detail = e.Error()
		seconds := int(quota.RetryAfter / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
	case errors.Is(e, pool.ErrForbidden):
		status, code = 403, "forbidden"
		detail = e.Error()
	case errors.Is(e, auth.ErrUnauthorized):
		status, code = 401, "unauthorized"
		detail = e.Error()
	case errors.Is(e, store.ErrNotFound):
		status, code = 404, "not-found"
		detail = e.Error()
	case errors.Is(e, pool.ErrUnsupported), errors.Is(e, store.ErrNotImplemented):
		status, code = 501, "not-implemented"
		detail = e.Error()
	case errors.Is(e, pool.ErrEmpty):
		status, code = 409, "capacity-unavailable"
		detail = e.Error()
	}
	Problem(w, r, status, code, detail)
}
func Decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*1024*1024))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		Problem(w, r, 400, "invalid-json", e.Error())
		return false
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		Problem(w, r, 400, "invalid-json", "Expected exactly one JSON value.")
		return false
	}
	return true
}
