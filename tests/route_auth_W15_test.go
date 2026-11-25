// Path: tests/route_auth_W15_test.go | Author: Claude for the CCGSC owner | 2026-11-22
// What/why: R15-FIX-AUTH (AT22-L1-1) acceptance. One admin gate covers the /pools/
// and /web/ trees, secureArtifactRead follows the T100 owner-reference rule for a
// developer, and the IP allow-list bypass cannot be reached from an untrusted peer.
package tests

import (
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/verify"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// w15Get issues one in-memory GET with an optional Bearer token and session cookie.
func w15Get(t *testing.T, f *fixture, path, token string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

// w15KeyID resolves the key subject the T100 reference rows are keyed by.
func w15KeyID(t *testing.T, f *fixture, token string) string {
	t.Helper()
	c, err := f.auth.Validate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	return c.KeyID
}

// TestW15ArtifactReadOwnerRule pins the T100 rule on the artifact read route.
// The artifact is stored under the admin's key subject (the upload route itself
// is developer-only), so a developer without an owner reference answers 403, a
// developer that holds one answers 200, and the Store A admin/director and
// contributor rules stay intact.
func TestW15ArtifactReadOwnerRule(t *testing.T) {
	for _, base := range []string{"/thread-pool/v1", "/process-pool/v1"} {
		t.Run(base, func(t *testing.T) {
			f := setup(t)
			_, admin := f.key(t, "admin")
			_, director := f.key(t, "director")
			_, developer := f.key(t, "developer")
			_, contributor := f.key(t, "contributor")

			// An admin subject owns the artifact and its reference row.
			body := []byte("debug module fixture; never executed by server")
			module := verify.SHA256(body)
			if _, err := f.engine.UploadOwned(context.Background(), module, "application/json", 0, int64(len(body)), body, w15KeyID(t, f, admin), false); err != nil {
				t.Fatal(err)
			}

			for _, tc := range []struct {
				name  string
				token string
				want  int
			}{
				{"anonymous", "", http.StatusUnauthorized},
				{"developer_without_reference", developer, http.StatusForbidden},
				{"contributor_worker_rule", contributor, http.StatusOK},
				{"admin_store_a", admin, http.StatusOK},
				{"director", director, http.StatusOK},
			} {
				t.Run(tc.name, func(t *testing.T) {
					f.request(t, http.MethodGet, base+"/artifacts/"+module, tc.token, nil, tc.want, nil)
				})
			}

			// Uploading the identical bytes joins the shared blob and adds the
			// developer's own T100 reference (artifact_owners_T100.go:130).
			if got := f.artifact(t, base, developer); got != module {
				t.Fatalf("developer join produced %q, want the shared blob %q", got, module)
			}
			t.Run("developer_with_reference", func(t *testing.T) {
				f.request(t, http.MethodGet, base+"/artifacts/"+module, developer, nil, http.StatusOK, nil)
			})
		})
	}
}

// TestW15AdminPageGateRealPages proves the real /pools/ documents answer 200 to
// an authenticated admin (session and Bearer) and are refused to anonymous,
// developer and contributor callers.
func TestW15AdminPageGateRealPages(t *testing.T) {
	f := setup(t)
	cookie := f.adminSession(t)
	_, admin := f.key(t, "admin")
	_, developer := f.key(t, "developer")
	_, contributor := f.key(t, "contributor")

	for _, path := range []string{
		"/pools/monitor.html",
		"/pools/thread-pool-admin.html",
		"/pools/process-pool-admin.html",
		"/pools/manual/monitor.html",
	} {
		t.Run("anonymous "+path, func(t *testing.T) {
			w := f.get(t, path)
			if w.Code != http.StatusFound {
				t.Fatalf("GET %s anonymous: got %d want 302", path, w.Code)
			}
			if loc := w.Header().Get("Location"); loc != "/pools/login.html" {
				t.Fatalf("GET %s anonymous: location %q want /pools/login.html", path, loc)
			}
		})
		t.Run("admin_session "+path, func(t *testing.T) {
			w := f.getWithCookie(t, path, cookie)
			if w.Code != http.StatusOK {
				t.Fatalf("GET %s with admin session: got %d want 200: %s", path, w.Code, w.Body.String())
			}
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Fatalf("GET %s with admin session: content type %q is not text/html", path, ct)
			}
			if body := strings.ToLower(w.Body.String()); !strings.Contains(body, "<!doctype html>") {
				t.Fatalf("GET %s with admin session: body is not an HTML document", path)
			}
		})
		t.Run("admin_bearer "+path, func(t *testing.T) {
			w := w15Get(t, f, path, admin, nil)
			if w.Code != http.StatusOK {
				t.Fatalf("GET %s with admin Bearer: got %d want 200", path, w.Code)
			}
		})
		t.Run("developer "+path, func(t *testing.T) {
			w := w15Get(t, f, path, developer, nil)
			if w.Code != http.StatusForbidden {
				t.Fatalf("GET %s with developer Bearer: got %d want 403", path, w.Code)
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Fatalf("GET %s with developer Bearer: content type %q is not a problem document", path, ct)
			}
		})
		t.Run("contributor "+path, func(t *testing.T) {
			w := w15Get(t, f, path, contributor, nil)
			if w.Code != http.StatusForbidden {
				t.Fatalf("GET %s with contributor Bearer: got %d want 403", path, w.Code)
			}
		})
	}
}

// TestW15LegacyWebTreeGate proves /web/ sits behind the same gate: the real
// documents under web.Files are refused to anonymous, developer and contributor
// callers, an authenticated admin passes the gate, the public assets remain
// public, and the historical 307 redirects of the directory roots are preserved.
func TestW15LegacyWebTreeGate(t *testing.T) {
	f := setup(t)
	cookie := f.adminSession(t)
	_, admin := f.key(t, "admin")
	_, developer := f.key(t, "developer")
	_, contributor := f.key(t, "contributor")

	for _, path := range []string{"/web/admin/index.html", "/web/monitor/index.html"} {
		t.Run("anonymous "+path, func(t *testing.T) {
			w := f.get(t, path)
			if w.Code != http.StatusFound {
				t.Fatalf("GET %s anonymous: got %d want 302", path, w.Code)
			}
			if loc := w.Header().Get("Location"); loc != "/pools/login.html" {
				t.Fatalf("GET %s anonymous: location %q want /pools/login.html", path, loc)
			}
		})
		t.Run("admin_session "+path, func(t *testing.T) {
			// The gate passes; http.FileServer then canonicalises index.html to
			// the directory root, whose own 307 redirect is asserted below.
			w := f.getWithCookie(t, path, cookie)
			if w.Code != http.StatusMovedPermanently {
				t.Fatalf("GET %s with admin session: got %d want 301 canonical redirect", path, w.Code)
			}
		})
		t.Run("admin_bearer "+path, func(t *testing.T) {
			w := w15Get(t, f, path, admin, nil)
			if w.Code != http.StatusMovedPermanently {
				t.Fatalf("GET %s with admin Bearer: got %d want 301 canonical redirect", path, w.Code)
			}
		})
		t.Run("developer "+path, func(t *testing.T) {
			w := w15Get(t, f, path, developer, nil)
			if w.Code != http.StatusForbidden {
				t.Fatalf("GET %s with developer Bearer: got %d want 403", path, w.Code)
			}
		})
		t.Run("contributor "+path, func(t *testing.T) {
			w := w15Get(t, f, path, contributor, nil)
			if w.Code != http.StatusForbidden {
				t.Fatalf("GET %s with contributor Bearer: got %d want 403", path, w.Code)
			}
		})
	}

	// The exact directory routes keep their historical 307 to the real page.
	for _, path := range []string{"/web/admin/", "/web/monitor/"} {
		w := f.get(t, path)
		if w.Code != http.StatusTemporaryRedirect {
			t.Fatalf("GET %s: got %d want 307", path, w.Code)
		}
		if loc := w.Header().Get("Location"); loc != "/pools/monitor.html" {
			t.Fatalf("GET %s: location %q want /pools/monitor.html", path, loc)
		}
	}

	// The public parts of the /web/ tree stay public.
	for _, path := range []string{"/web/nav.js", "/web/nav.json", "/web/contribute/"} {
		if w := f.get(t, path); w.Code != http.StatusOK {
			t.Fatalf("GET %s without auth: got %d want 200", path, w.Code)
		}
	}
}

// TestW15AdminPageGateIPAllowlist covers the one documented bypass. An
// allow-listed peer passes both trees, a peer outside the list is redirected even
// when it spoofs X-Forwarded-For and X-Real-IP, and a valid non-admin token from
// an outside peer is still refused.
func TestW15AdminPageGateIPAllowlist(t *testing.T) {
	cfg := config.Default()
	cfg.IPAllowlistEnabled = true
	cfg.IPAllowlist = []string{"127.0.0.1/32"}
	f, _ := setupCustom(t, cfg)

	paths := []string{"/pools/monitor.html", "/web/admin/index.html", "/web/monitor/index.html"}

	for _, path := range paths {
		t.Run("allowed "+path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.RemoteAddr = "127.0.0.1:50000"
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, req)
			if w.Code == http.StatusFound {
				t.Fatalf("GET %s from allow-listed peer: redirected to login, allow-list bypass is broken", path)
			}
			if w.Code != http.StatusOK && w.Code != http.StatusMovedPermanently {
				t.Fatalf("GET %s from allow-listed peer: got %d want 200 (or the file server's 301)", path, w.Code)
			}
		})
		t.Run("spoofed_forwarding_headers "+path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.RemoteAddr = "198.51.100.10:50000"
			req.Header.Set("X-Forwarded-For", "127.0.0.1")
			req.Header.Set("X-Real-IP", "127.0.0.1")
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, req)
			if w.Code != http.StatusFound {
				t.Fatalf("GET %s with spoofed forwarding headers: got %d want 302", path, w.Code)
			}
		})
	}

	_, contributor := f.key(t, "contributor")
	req := httptest.NewRequest(http.MethodGet, "/web/admin/index.html", nil)
	req.RemoteAddr = "198.51.100.10:50000"
	req.Header.Set("Authorization", "Bearer "+contributor)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("contributor from outside the allow-list: got %d want 403", w.Code)
	}
}
