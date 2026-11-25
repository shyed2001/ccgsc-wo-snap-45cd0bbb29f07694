// Path: tests/pages_test.go | Author: hermes MiniMax-M3 for Claude Opus 5.5 | 2026-11-17
// What/why: WO-T2.2 acceptance for the agy pages served from the pools-core embed:
// every /pools/*.html page and every manual page answers 200 text/html, the old
// placeholder routes redirect to /pools/monitor.html, and one nav serves every page.
package tests

import (
	"ccgsc/poolscore/internal/auth"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// poolsPageIDs are the seven agy pages served under /pools/ (WWPP-R38.5, R38.5b).
var poolsPageIDs = []string{
	"monitor",
	"pool-monitor",
	"api-monitor",
	"thread-pool-admin",
	"process-pool-admin",
	"thread-api-admin",
	"process-api-admin",
}

func (f *fixture) adminSession(t *testing.T) *http.Cookie {
	t.Helper()
	acc := auth.AdminAccount{
		Username:     "pagesadmin",
		PasswordHash: "unused",
		AccountStore: "A",
		Roles:        []string{"admin"},
	}
	if err := f.auth.SaveAdminAccount(context.Background(), acc); err != nil {
		t.Fatal(err)
	}
	sess, err := f.auth.CreateSession(context.Background(), &acc, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: "ccgsc_admin_session", Value: sess.ID}
}

func (f *fixture) getWithCookie(t *testing.T, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

// get issues an in-memory GET against the real handler; no listener is opened.
func (f *fixture) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

// TestPoolsPagesAreServed proves every /pools/*.html page answers 200 text/html when authenticated.
func TestPoolsPagesAreServed(t *testing.T) {
	f := setup(t)
	cookie := f.adminSession(t)
	for _, id := range poolsPageIDs {
		t.Run(id, func(t *testing.T) {
			w := f.getWithCookie(t, "/pools/"+id+".html", cookie)
			if w.Code != http.StatusOK {
				t.Fatalf("GET /pools/%s.html: got %d want 200", id, w.Code)
			}
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Fatalf("GET /pools/%s.html: content type %q is not text/html", id, ct)
			}
			body := w.Body.String()
			if !strings.Contains(body, "<!DOCTYPE html>") {
				t.Fatalf("GET /pools/%s.html: body is not an HTML document", id)
			}
			if !strings.Contains(body, `id="ccgsc-nav"`) {
				t.Fatalf("GET /pools/%s.html: nav mount missing", id)
			}
			// Every page must load the one shared nav.
			if !strings.Contains(body, `src="/nav/nav.js"`) {
				t.Fatalf("GET /pools/%s.html: does not load the shared /nav/nav.js", id)
			}
		})
	}
}

// TestPoolsManualPagesAreServed proves every manual page answers 200 text/html when authenticated.
func TestPoolsManualPagesAreServed(t *testing.T) {
	f := setup(t)
	cookie := f.adminSession(t)
	for _, id := range poolsPageIDs {
		t.Run(id, func(t *testing.T) {
			w := f.getWithCookie(t, "/pools/manual/"+id+".html", cookie)
			if w.Code != http.StatusOK {
				t.Fatalf("GET /pools/manual/%s.html: got %d want 200", id, w.Code)
			}
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Fatalf("GET /pools/manual/%s.html: content type %q is not text/html", id, ct)
			}
			body := w.Body.String()
			if !strings.Contains(body, "<!DOCTYPE html>") {
				t.Fatalf("GET /pools/manual/%s.html: body is not an HTML document", id)
			}
			if !strings.Contains(body, `src="/nav/nav.js"`) {
				t.Fatalf("GET /pools/manual/%s.html: does not load the shared /nav/nav.js", id)
			}
		})
	}
}

// TestPoolsAssetsAreServed proves the shared stylesheet and the page scripts are
// reachable from the pages, otherwise the served HTML would be unstyled.
func TestPoolsAssetsAreServed(t *testing.T) {
	f := setup(t)
	for path, kind := range map[string]string{
		"/pools/css/style.css": "text/css",
		"/pools/js/app.js":     "javascript",
		"/pools/js/api.js":     "javascript",
		"/pools/js/mock.js":    "javascript",
		"/pools/js/schema.js":  "javascript",
		"/nav/nav.js":          "javascript",
		"/nav/nav.json":        "json",
	} {
		w := f.get(t, path)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: got %d want 200", path, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, kind) {
			t.Fatalf("GET %s: content type %q does not contain %q", path, ct, kind)
		}
	}
}

// TestPoolsRootRedirects proves /pools/ lands on the combined monitor page.
func TestPoolsRootRedirects(t *testing.T) {
	f := setup(t)
	w := f.get(t, "/pools/")
	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("GET /pools/: got %d want 307", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/pools/monitor.html" {
		t.Fatalf("GET /pools/: location %q is not /pools/monitor.html", loc)
	}
}

// TestLegacyWebRoutesStillWork proves the old placeholder routes keep working
// and that the admin and monitor ones now redirect to the real pages.
func TestLegacyWebRoutesStillWork(t *testing.T) {
	f := setup(t)
	for _, path := range []string{"/web/admin/", "/web/monitor/"} {
		w := f.get(t, path)
		if w.Code != http.StatusTemporaryRedirect {
			t.Fatalf("GET %s: got %d want 307", path, w.Code)
		}
		if loc := w.Header().Get("Location"); loc != "/pools/monitor.html" {
			t.Fatalf("GET %s: location %q is not /pools/monitor.html", path, loc)
		}
	}
	// The contribute placeholder is untouched.
	w := f.get(t, "/web/contribute/")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /web/contribute/: got %d want 200", w.Code)
	}
	// The root lands on the real monitor page too.
	w = f.get(t, "/")
	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("GET /: got %d want 307", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/pools/monitor.html" {
		t.Fatalf("GET /: location %q is not /pools/monitor.html", loc)
	}
}

// TestOneNavForEveryPage proves there is exactly one nav implementation and one
// link registry, and that both are reachable from every page.
func TestOneNavForEveryPage(t *testing.T) {
	f := setup(t)
	nav := f.get(t, "/nav/nav.js")
	if nav.Code != http.StatusOK {
		t.Fatalf("GET /nav/nav.js: got %d want 200", nav.Code)
	}
	body := nav.Body.String()
	if !strings.Contains(body, "CCGSC_NAV") {
		t.Fatal("/nav/nav.js is not the shared navigation component")
	}
	registry := f.get(t, "/nav/nav.json")
	if registry.Code != http.StatusOK {
		t.Fatalf("GET /nav/nav.json: got %d want 200", registry.Code)
	}
	if !strings.Contains(registry.Body.String(), "\"links\"") {
		t.Fatal("/nav/nav.json is not the link registry")
	}
	// The historical path serves the same registry.
	legacy := f.get(t, "/web/nav.json")
	if legacy.Code != http.StatusOK {
		t.Fatalf("GET /web/nav.json: got %d want 200", legacy.Code)
	}
	if legacy.Body.String() != registry.Body.String() {
		t.Fatal("/web/nav.json and /nav/nav.json differ: there must be one nav")
	}
	// The legacy nav.js path serves the same component.
	legacyJS := f.get(t, "/web/nav.js")
	if legacyJS.Code != http.StatusOK {
		t.Fatalf("GET /web/nav.js: got %d want 200", legacyJS.Code)
	}
	if legacyJS.Body.String() != body {
		t.Fatal("/web/nav.js and /nav/nav.js differ: there must be one nav")
	}
}
