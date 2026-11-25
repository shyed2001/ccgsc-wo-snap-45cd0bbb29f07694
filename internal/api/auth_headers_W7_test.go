// Path: internal/api/auth_headers_W7_test.go | Author: cmdc for Claude Opus 5.5 | 2026-11-21
// What/why: W7 fix of the cmdc LOW row - the anonymous bearer was returned in a 200
// JSON body with no Cache-Control, so a proxy, bfcache or service worker could retain
// a credential.
package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestW7AnonymousTokenResponseIsNotCacheable covers both paths of the only route that
// hands out an anonymous bearer: a fresh join and a renewal.
func TestW7AnonymousTokenResponseIsNotCacheable(t *testing.T) {
	f := newW7Anonymous(t)
	const remote = "198.51.100.130:4242"

	first := f.issue(t, remote)

	for name, bearer := range map[string]string{
		"fresh join": "",
		"renewal":    first.AccessToken,
	} {
		r := httptest.NewRequest(http.MethodPost, "/thread-pool/v1/workers/anonymous", nil)
		r.RemoteAddr = remote
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: got %d want 200: %s", name, w.Code, w.Body.String())
		}
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s: Cache-Control = %q, want no-store", name, got)
		}
	}
}
