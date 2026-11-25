// Path: tests/admin_auth_test.go | Author: Codex for Claude Opus 5.5 | 2026-11-18
// What/why: acceptance tests for WO_FIX_C_ADMIN_AUTH:
// - unauth GET admin page -> 302 to login
// - wrong password -> 401 + rate limit (429)
// - right password -> 200 + HttpOnly/Secure/SameSite=Strict cookie
// - contributor role -> 403
// - allow-list on/off (CIDR 127.0.0.1/32 + 100.64.0.0/10)
// - CSRF verification on POST
package tests

import (
	"bytes"
	"ccgsc/poolscore/internal/api"
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/config"
	"ccgsc/poolscore/internal/pool"
	"ccgsc/poolscore/internal/store/sqlite"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupCustom(t *testing.T, cfg config.Config) (*fixture, *api.Server) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "custom_test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	l := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine, err := pool.New(db, l, "s1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := auth.New(db, cfg.JWTSecret, "s1")
	s := &api.Server{Engine: engine, Auth: a, Log: l, Config: cfg}
	return &fixture{db: db, engine: engine, auth: a, handler: s.Handler()}, s
}

// 1. Unauthenticated GET admin page -> 302 redirect to /pools/login.html
func TestAdminAuth_UnauthRedirect(t *testing.T) {
	f := setup(t)
	adminPages := []string{
		"/pools/monitor.html",
		"/pools/pool-monitor.html",
		"/pools/api-monitor.html",
		"/pools/thread-pool-admin.html",
		"/pools/process-pool-admin.html",
		"/pools/thread-api-admin.html",
		"/pools/process-api-admin.html",
		"/pools/manual/monitor.html",
		"/pools/manual/thread-pool-admin.html",
	}

	for _, page := range adminPages {
		t.Run(page, func(t *testing.T) {
			w := f.get(t, page)
			if w.Code != http.StatusFound {
				t.Fatalf("GET %s without auth: got %d want 302", page, w.Code)
			}
			if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("GET %s: X-Content-Type-Options = %q, want nosniff", page, got)
			}
			if got := w.Header().Get("X-Frame-Options"); got != "DENY" {
				t.Errorf("GET %s: X-Frame-Options = %q, want DENY", page, got)
			}
			if got := w.Header().Get("Content-Security-Policy"); got != "frame-ancestors 'none'" {
				t.Errorf("GET %s: Content-Security-Policy = %q, want frame-ancestors policy", page, got)
			}
			loc := w.Header().Get("Location")
			if loc != "/pools/login.html" {
				t.Fatalf("GET %s: location got %q want /pools/login.html", page, loc)
			}
		})
	}

	// Public pages must stay 200 without auth
	for _, page := range []string{"/pools/contribute.html", "/pools/login.html", "/pools/runner.html"} {
		w := f.get(t, page)
		if w.Code != http.StatusOK {
			t.Fatalf("GET public page %s: got %d want 200", page, w.Code)
		}
	}

}

func TestAdminAuth_CookieSessionWritesRequireCSRF(t *testing.T) {
	f := setup(t)
	sess, err := f.auth.CreateSession(context.Background(), &auth.AdminAccount{
		Username:     "admin",
		AccountStore: "A",
		Roles:        []string{"admin"},
	}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/thread-pool/v1/auth/keys", strings.NewReader(`{}`))
	req.AddCookie(&http.Cookie{Name: "ccgsc_admin_session", Value: sess.ID})
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cookie-authenticated write without CSRF: got %d want 403", w.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/thread-pool/v1/auth/keys", strings.NewReader(`{}`))
	req.AddCookie(&http.Cookie{Name: "ccgsc_admin_session", Value: sess.ID})
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", sess.CSRFToken)
	w = httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code == http.StatusForbidden {
		t.Fatalf("cookie-authenticated write with valid CSRF was rejected: %s", w.Body.String())
	}
}

func TestAdminAuth_LoginRequiresCSRFForSimpleRequests(t *testing.T) {
	f := setup(t)
	req := httptest.NewRequest(http.MethodPost, "/pools/auth/login", strings.NewReader("username=admin&password=wrong"))
	req.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("text/plain login without CSRF: got %d want 403", w.Code)
	}
}

func TestAdminAuth_ChangePasswordRequiresSessionAndRevokesIt(t *testing.T) {
	f := setup(t)
	oldPassword := "OldPassword123!"
	hash, err := auth.HashPassword(oldPassword)
	if err != nil {
		t.Fatal(err)
	}
	acc := &auth.AdminAccount{Username: "admin", PasswordHash: hash, Roles: []string{"admin"}}
	if err := f.auth.SaveAdminAccount(context.Background(), *acc); err != nil {
		t.Fatal(err)
	}
	sess, err := f.auth.CreateSession(context.Background(), acc, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	body := `{"username":"other","oldPassword":"OldPassword123!","newPassword":"NewPassword123!","csrf_token":"` + sess.CSRFToken + `"}`
	req := httptest.NewRequest(http.MethodPost, "/pools/auth/change-password", strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: "ccgsc_admin_session", Value: sess.ID})
	req.AddCookie(&http.Cookie{Name: "ccgsc_csrf", Value: sess.CSRFToken})
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("authenticated change-password: got %d want 200: %s", w.Code, w.Body.String())
	}
	if _, err := f.auth.ValidateSession(context.Background(), sess.ID); err != auth.ErrSessionNotFound {
		t.Fatalf("password change left session valid: %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/pools/auth/change-password", strings.NewReader(`{"username":"admin","oldPassword":"OldPassword123!","newPassword":"NewPassword123!"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated change-password: got %d want 401", w.Code)
	}
}

func TestAdminAuth_DirectForwardedIPCannotBypassAllowlist(t *testing.T) {
	cfg := config.Default()
	cfg.IPAllowlistEnabled = true
	cfg.IPAllowlist = []string{"127.0.0.1/32"}
	f, _ := setupCustom(t, cfg)
	req := httptest.NewRequest(http.MethodGet, "/pools/monitor.html", nil)
	req.RemoteAddr = "198.51.100.10:50000"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("untrusted X-Forwarded-For bypassed allowlist: got %d want 302", w.Code)
	}
}

// 2. Wrong password -> 401 + rate limit (429); right password -> 200 with HttpOnly+Secure+SameSite=Strict cookie
func TestAdminAuth_WrongPasswordRateLimitAndSuccess(t *testing.T) {
	cfg := config.Default()
	cfg.LoginRateLimit = 3
	f, _ := setupCustom(t, cfg)

	// Create admin user
	pass := "StrongAdminP@ss123"
	hash, err := auth.HashPassword(pass)
	if err != nil {
		t.Fatal(err)
	}
	acc := auth.AdminAccount{
		Username:     "admin",
		PasswordHash: hash,
		AccountStore: "A",
		Roles:        []string{"admin", "director"},
	}
	if err := f.auth.SaveAdminAccount(context.Background(), acc); err != nil {
		t.Fatal(err)
	}

	clientIP := "192.0.2.10:54321"

	loginReq := func(user, password string, ip string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{
			"username": user,
			"password": password,
		})
		r := httptest.NewRequest("POST", "/pools/auth/login", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: "ccgsc_csrf", Value: "test-csrf"})
		r.Header.Set("X-CSRF-Token", "test-csrf")
		r.RemoteAddr = ip
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		return w
	}

	// Wrong password -> 401
	for i := 1; i <= 3; i++ {
		w := loginReq("admin", "wrong-password", clientIP)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d wrong password: got %d want 401", i, w.Code)
		}
	}

	// 4th attempt -> 429 Too Many Requests (rate limit exceeded)
	wLimit := loginReq("admin", "wrong-password", clientIP)
	if wLimit.Code != http.StatusTooManyRequests {
		t.Fatalf("after 3 failures: got %d want 429 (rate limited)", wLimit.Code)
	}

	// Even right password from rate-limited IP is blocked
	wBlocked := loginReq("admin", pass, clientIP)
	if wBlocked.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked IP right password: got %d want 429", wBlocked.Code)
	}

	// Fresh client IP with right password -> 200 OK
	freshIP := "192.0.2.20:54321"
	wSuccess := loginReq("admin", pass, freshIP)
	if wSuccess.Code != http.StatusOK {
		t.Fatalf("fresh IP right password: got %d want 200: %s", wSuccess.Code, wSuccess.Body.String())
	}

	// Check cookies
	cookies := wSuccess.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "ccgsc_admin_session" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("missing ccgsc_admin_session cookie")
	}
	if !sessionCookie.HttpOnly {
		t.Errorf("session cookie missing HttpOnly")
	}
	if !sessionCookie.Secure {
		t.Errorf("session cookie missing Secure")
	}
	if sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("session cookie SameSite got %v want SameSiteStrictMode", sessionCookie.SameSite)
	}

	// Access admin page using session cookie -> 200 OK
	rPage := httptest.NewRequest("GET", "/pools/monitor.html", nil)
	rPage.AddCookie(sessionCookie)
	wPage := httptest.NewRecorder()
	f.handler.ServeHTTP(wPage, rPage)
	if wPage.Code != http.StatusOK {
		t.Fatalf("GET /pools/monitor.html with valid session: got %d want 200", wPage.Code)
	}
}

// 3. Contributor role -> 403 Forbidden
func TestAdminAuth_ContributorForbidden(t *testing.T) {
	f := setup(t)

	// Create contributor user in Store B
	pass := "WorkerPassword123"
	hash, _ := auth.HashPassword(pass)
	contrib := auth.AdminAccount{
		Username:     "worker_jim",
		PasswordHash: hash,
		AccountStore: "B",
		Roles:        []string{"contributor"},
	}
	if err := f.auth.SaveAdminAccount(context.Background(), contrib); err != nil {
		t.Fatal(err)
	}

	// Login attempt with contributor user -> 403 Forbidden
	body, _ := json.Marshal(map[string]string{
		"username": "worker_jim",
		"password": pass,
	})
	r := httptest.NewRequest("POST", "/pools/auth/login", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: "ccgsc_csrf", Value: "test-csrf"})
	r.Header.Set("X-CSRF-Token", "test-csrf")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("login contributor: got %d want 403", w.Code)
	}

	// Access admin page with contributor session cookie -> 403 Forbidden
	sess, err := f.auth.CreateSession(context.Background(), &contrib, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	rPage := httptest.NewRequest("GET", "/pools/monitor.html", nil)
	rPage.AddCookie(&http.Cookie{Name: "ccgsc_admin_session", Value: sess.ID})
	wPage := httptest.NewRecorder()
	f.handler.ServeHTTP(wPage, rPage)
	if wPage.Code != http.StatusForbidden {
		t.Fatalf("GET /pools/monitor.html with contributor session: got %d want 403", wPage.Code)
	}
}

// 4. IP Allow-list on/off (CIDR list 127.0.0.1/32 + 100.64.0.0/10 Tailscale)
func TestAdminAuth_AllowlistOnOff(t *testing.T) {
	// A. Allowlist OFF
	cfgOff := config.Default()
	cfgOff.IPAllowlistEnabled = false
	fOff, _ := setupCustom(t, cfgOff)

	reqFrom := func(f *fixture, ip string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/pools/monitor.html", nil)
		r.RemoteAddr = ip
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		return w
	}

	// When allowlist is OFF, 127.0.0.1 without session is redirected (302)
	w := reqFrom(fOff, "127.0.0.1:50000")
	if w.Code != http.StatusFound {
		t.Fatalf("allowlist OFF: 127.0.0.1 got %d want 302", w.Code)
	}

	// B. Allowlist ON
	cfgOn := config.Default()
	cfgOn.IPAllowlistEnabled = true
	cfgOn.IPAllowlist = []string{"127.0.0.1/32", "100.64.0.0/10"}
	fOn, _ := setupCustom(t, cfgOn)

	// 127.0.0.1 (matches 127.0.0.1/32) -> 200 OK (bypasses login)
	wLocal := reqFrom(fOn, "127.0.0.1:50000")
	if wLocal.Code != http.StatusOK {
		t.Fatalf("allowlist ON: 127.0.0.1 got %d want 200", wLocal.Code)
	}

	// Tailscale IP 100.64.42.1 (matches 100.64.0.0/10) -> 200 OK (bypasses login)
	wTailscale := reqFrom(fOn, "100.64.42.1:50000")
	if wTailscale.Code != http.StatusOK {
		t.Fatalf("allowlist ON: 100.64.42.1 got %d want 200", wTailscale.Code)
	}

	// Outside IP 198.51.100.5 (does not match) -> 302 redirect to login
	wOutside := reqFrom(fOn, "198.51.100.5:50000")
	if wOutside.Code != http.StatusFound {
		t.Fatalf("allowlist ON: 198.51.100.5 got %d want 302", wOutside.Code)
	}
}

func TestAdminAuth_AllowlistOnlyBypassesAdminRoutes(t *testing.T) {
	cfg := config.Default()
	cfg.IPAllowlistEnabled = true
	cfg.IPAllowlist = []string{"127.0.0.1/32"}
	f, _ := setupCustom(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/pools/monitor.html", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("allowlisted admin route: got %d want 200", w.Code)
	}

	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/thread-pool/v1/jobs"},
		{http.MethodPost, "/thread-pool/v1/workers/register"},
	} {
		t.Run(route.path, func(t *testing.T) {
			req := httptest.NewRequest(route.method, route.path, nil)
			req.RemoteAddr = "127.0.0.1:50000"
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("allowlisted non-admin route %s: got %d want 401", route.path, w.Code)
			}
		})
	}
}

// 5. CSRF token validation on POST
func TestAdminAuth_CSRFToken(t *testing.T) {
	f := setup(t)

	// Create admin user
	pass := "AdminPass123"
	hash, _ := auth.HashPassword(pass)
	_ = f.auth.SaveAdminAccount(context.Background(), auth.AdminAccount{
		Username:     "admin",
		PasswordHash: hash,
		AccountStore: "A",
		Roles:        []string{"admin"},
	})

	// GET CSRF token endpoint
	rCSRF := httptest.NewRequest("GET", "/pools/auth/csrf", nil)
	wCSRF := httptest.NewRecorder()
	f.handler.ServeHTTP(wCSRF, rCSRF)
	if wCSRF.Code != http.StatusOK {
		t.Fatalf("GET /pools/auth/csrf: got %d want 200", wCSRF.Code)
	}
	var csrfResp struct {
		CSRFToken string `json:"csrfToken"`
	}
	_ = json.Unmarshal(wCSRF.Body.Bytes(), &csrfResp)
	if csrfResp.CSRFToken == "" {
		t.Fatal("empty csrf token")
	}

	csrfCookie := wCSRF.Result().Cookies()[0]

	// Form POST with mismatched CSRF token -> 403 Forbidden
	formBody := strings.NewReader("username=admin&password=" + pass + "&csrf_token=wrong_csrf")
	rFormBad := httptest.NewRequest("POST", "/pools/auth/login", formBody)
	rFormBad.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rFormBad.AddCookie(csrfCookie)
	wFormBad := httptest.NewRecorder()
	f.handler.ServeHTTP(wFormBad, rFormBad)
	if wFormBad.Code != http.StatusForbidden {
		t.Fatalf("form with wrong CSRF: got %d want 403", wFormBad.Code)
	}

	// Form POST with matching CSRF token -> 200 OK
	formBodyGood := strings.NewReader("username=admin&password=" + pass + "&csrf_token=" + csrfResp.CSRFToken)
	rFormGood := httptest.NewRequest("POST", "/pools/auth/login", formBodyGood)
	rFormGood.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rFormGood.AddCookie(csrfCookie)
	wFormGood := httptest.NewRecorder()
	f.handler.ServeHTTP(wFormGood, rFormGood)
	if wFormGood.Code != http.StatusOK {
		t.Fatalf("form with valid CSRF: got %d want 200", wFormGood.Code)
	}
}

// 6. Argon2 password hash support in config
func TestAdminAuth_Argon2ConfigUser(t *testing.T) {
	pass := "Argon2Password123"
	argonHash, err := auth.HashPasswordArgon2id(pass)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.AdminUsers = []config.AdminUserConfig{
		{
			Username:     "argon_admin",
			PasswordHash: argonHash,
			Roles:        []string{"admin"},
		},
	}

	f, _ := setupCustom(t, cfg)
	_, _, _, err = f.auth.InitStoreA(context.Background(), cfg.AdminUsers)
	if err != nil {
		t.Fatal(err)
	}

	// Login with argon2 user
	body, _ := json.Marshal(map[string]string{
		"username": "argon_admin",
		"password": pass,
	})
	r := httptest.NewRequest("POST", "/pools/auth/login", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: "ccgsc_csrf", Value: "test-csrf"})
	r.Header.Set("X-CSRF-Token", "test-csrf")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("argon2 login: got %d want 200: %s", w.Code, w.Body.String())
	}
}
