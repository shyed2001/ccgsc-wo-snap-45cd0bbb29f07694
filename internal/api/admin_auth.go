// Path: internal/api/admin_auth.go | Author: Codex for Claude Opus 5.5 | 2026-11-18
// What/why: Store A session authentication, gating for /pools/*admin* & /pools/*monitor*,
// CSRF verification, rate limiting, and IP allow-list; WO_FIX_C_ADMIN_AUTH.
package api

import (
	"ccgsc/poolscore/internal/auth"
	"ccgsc/poolscore/internal/httpx"
	"ccgsc/poolscore/internal/store"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"
)

// clientIP trusts forwarding headers only when the direct peer is a configured proxy.
func (s *Server) clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if s.isTrustedProxy(host) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			for i := len(parts) - 1; i >= 0; i-- {
				cand := strings.TrimSpace(parts[i])
				if net.ParseIP(cand) == nil {
					continue
				}
				if !s.isTrustedProxy(cand) || i == 0 {
					return cand
				}
			}
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			cand := strings.TrimSpace(xri)
			if net.ParseIP(cand) != nil {
				return cand
			}
		}
	}
	return host
}

func (s *Server) isTrustedProxy(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, cidr := range s.Config.TrustedProxyCIDRs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err == nil && ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *Server) rateLimited(key string) bool {
	if s.RateLimit == nil {
		return false
	}
	return !s.RateLimit.Allow(key)
}

func rateLimitResponse(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "60")
	httpx.Problem(w, r, http.StatusTooManyRequests, "rate-limit-exceeded", "Too many requests. Please retry later.")
}

// isIPAllowed checks if client IP matches any CIDR in Config.IPAllowlist when IPAllowlistEnabled is true.
func (s *Server) isIPAllowed(r *http.Request) bool {
	if !s.Config.IPAllowlistEnabled {
		return false
	}
	ipStr := s.clientIP(r)
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, cidr := range s.Config.IPAllowlist {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err == nil && ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *Server) rateLimiter() *auth.RateLimiter {
	return s.RateLimit
}

func (s *Server) loginRateLimiter() *auth.RateLimiter {
	s.loginOnce.Do(func() {
		limit := s.Config.LoginRateLimit
		if limit <= 0 {
			limit = 5
		}
		s.loginLimit = auth.NewRateLimiter(limit, time.Minute)
	})
	return s.loginLimit
}

// getOrSetCSRFToken retrieves existing CSRF token or issues a new one.
func (s *Server) getOrSetCSRFToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie("ccgsc_csrf"); err == nil && c.Value != "" {
		return c.Value
	}
	token := auth.ID() + auth.ID()
	http.SetCookie(w, &http.Cookie{
		Name:     "ccgsc_csrf",
		Value:    token,
		Path:     "/",
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400,
	})
	return token
}

// csrfEndpoint returns a CSRF token as JSON.
func (s *Server) csrfEndpoint(w http.ResponseWriter, r *http.Request) {
	token := s.getOrSetCSRFToken(w, r)
	httpx.JSON(w, http.StatusOK, map[string]string{"csrfToken": token})
}

// sessionEndpoint returns current login status.
func (s *Server) sessionEndpoint(w http.ResponseWriter, r *http.Request) {
	if s.isIPAllowed(r) {
		httpx.JSON(w, http.StatusOK, map[string]any{
			"authenticated": true,
			"user": map[string]any{
				"username": "ip-allowlist",
				"roles":    []string{"admin", "director"},
			},
		})
		return
	}
	if cookie, err := r.Cookie("ccgsc_admin_session"); err == nil && cookie.Value != "" {
		if sess, err := s.Auth.ValidateSession(r.Context(), cookie.Value); err == nil {
			httpx.JSON(w, http.StatusOK, map[string]any{
				"authenticated": true,
				"user": map[string]any{
					"username": sess.Username,
					"roles":    sess.Roles,
				},
			})
			return
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"authenticated": false})
}

// loginEndpoint handles Store A administrative session logins.
func (s *Server) loginEndpoint(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)

	// Rate limit check
	if !s.loginRateLimiter().Allow(ip) {
		s.Log.Warn("admin.login_rate_limited", "ip", ip)
		httpx.Problem(w, r, http.StatusTooManyRequests, "rate-limit-exceeded", "Too many failed login attempts. Please try again later.")
		return
	}

	// CSRF validation
	cookieCSRF, _ := r.Cookie("ccgsc_csrf")
	reqCSRF := r.Header.Get("X-CSRF-Token")

	var username, password, bodyCSRF string
	contentType := r.Header.Get("Content-Type")

	if strings.Contains(contentType, "application/json") {
		var req struct {
			Username  string `json:"username"`
			Password  string `json:"password"`
			CSRFToken string `json:"csrf_token"`
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			httpx.Problem(w, r, http.StatusBadRequest, "invalid-body", "Could not read request body.")
			return
		}
		if len(b) > 0 {
			if err := json.Unmarshal(b, &req); err != nil {
				httpx.Problem(w, r, http.StatusBadRequest, "invalid-json", "Malformed JSON request.")
				return
			}
			username = req.Username
			password = req.Password
			bodyCSRF = req.CSRFToken
		}
	} else {
		if err := r.ParseForm(); err != nil {
			// A malformed form body would otherwise reach the credential check as
			// empty fields and answer a misleading 401 invalid-credentials.
			s.Log.Warn("admin.login_form_invalid", "ip", ip, "error", err)
		}
		username = r.FormValue("username")
		password = r.FormValue("password")
		bodyCSRF = r.FormValue("csrf_token")
	}

	if reqCSRF == "" {
		reqCSRF = bodyCSRF
	}

	// Every login POST requires the double-submit CSRF token, including simple
	// text/plain or JSON requests without a pre-existing cookie.
	if cookieCSRF == nil || cookieCSRF.Value == "" || reqCSRF == "" || reqCSRF != cookieCSRF.Value {
		code := "invalid-csrf"
		if reqCSRF == "" {
			code = "missing-csrf"
		}
		httpx.Problem(w, r, http.StatusForbidden, code, "Invalid or missing CSRF token.")
		return
	}

	// Authenticate credentials
	acc, err := s.Auth.AuthenticateStoreA(r.Context(), username, password)
	if err != nil {
		s.loginRateLimiter().RecordFailure(ip)
		if err == auth.ErrForbiddenRole {
			s.Log.Warn("admin.login_forbidden", "username", username, "ip", ip, "role", "contributor")
			httpx.Problem(w, r, http.StatusForbidden, "forbidden", "Access forbidden: administrative Store A role required.")
			return
		}
		s.Log.Warn("admin.login_failed", "username", username, "ip", ip)
		httpx.Problem(w, r, http.StatusUnauthorized, "unauthorized", "Invalid username or password.")
		return
	}

	// Login successful: clear rate limit, establish session
	s.loginRateLimiter().RecordSuccess(ip)
	sess, err := s.Auth.CreateSession(r.Context(), acc, 24*time.Hour)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	// Set HttpOnly, Secure, SameSite=Strict session cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "ccgsc_admin_session",
		Value:    sess.ID,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     "ccgsc_csrf",
		Value:    sess.CSRFToken,
		Path:     "/",
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400,
	})

	// Audit log line per login (NO SECRETS)
	s.Log.Info("admin.login", "status", "success", "username", acc.Username, "ip", ip, "roles", acc.Roles)

	// Also record in store.Audit if available
	auditErr := s.Engine.Store.Update(r.Context(), func(tx store.Tx) error {
		return tx.Put(store.Audit, "login:"+auth.ID(), map[string]any{
			"type":      "admin.login_success",
			"username":  acc.Username,
			"ip":        ip,
			"roles":     acc.Roles,
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		})
	})
	if auditErr != nil {
		// Login already succeeded. Preserve the success response but make
		// an unavailable secondary audit store visible to operators.
		s.Log.Warn("admin.loginAuditFailed", "error", auditErr)
	}

	httpx.JSON(w, http.StatusOK, map[string]any{
		"status":             "ok",
		"user":               map[string]any{"username": acc.Username, "roles": acc.Roles},
		"mustChangePassword": acc.MustChangePassword,
		"csrfToken":          sess.CSRFToken,
	})
}

// logoutEndpoint clears cookies and expires active session.
func (s *Server) logoutEndpoint(w http.ResponseWriter, r *http.Request) {
	if !s.validCSRF(r) {
		httpx.Problem(w, r, http.StatusForbidden, "invalid-csrf", "Invalid or missing CSRF token.")
		return
	}
	if cookie, err := r.Cookie("ccgsc_admin_session"); err == nil && cookie.Value != "" {
		if err := s.Auth.RevokeSession(r.Context(), cookie.Value); err != nil {
			// The cookies are cleared below in every case, so a failure here has to
			// be reported: the server-side session stays valid.
			s.Log.Warn("admin.logout_revoke_failed", "ip", s.clientIP(r), "error", err)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "ccgsc_admin_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     "ccgsc_csrf",
		Value:    "",
		Path:     "/",
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})

	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Redirect(w, r, "/pools/login.html", http.StatusFound)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// changePasswordEndpoint updates user password and clears forced change.
func (s *Server) changePasswordEndpoint(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.loginRateLimiter().Allow(ip) {
		rateLimitResponse(w, r)
		return
	}

	cookie, err := r.Cookie("ccgsc_admin_session")
	if err != nil || cookie.Value == "" {
		httpx.Problem(w, r, http.StatusUnauthorized, "unauthorized", "Authentication required.")
		return
	}
	sess, err := s.Auth.ValidateSession(r.Context(), cookie.Value)
	if err != nil {
		httpx.Problem(w, r, http.StatusUnauthorized, "unauthorized", "Authentication required.")
		return
	}

	var q struct {
		Username    string `json:"username"`
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
		CSRFToken   string `json:"csrf_token"`
	}
	if !httpx.Decode(w, r, &q) {
		return
	}
	if !s.validCSRFValue(r, q.CSRFToken) || q.CSRFToken != sess.CSRFToken {
		httpx.Problem(w, r, http.StatusForbidden, "invalid-csrf", "Invalid CSRF token.")
		return
	}

	username := sess.Username

	err = s.Auth.ChangePassword(r.Context(), username, q.OldPassword, q.NewPassword)
	if err != nil {
		s.loginRateLimiter().RecordFailure(ip)
		s.Log.Warn("admin.password_change_failed", "username", username, "ip", ip)
		httpx.Problem(w, r, http.StatusBadRequest, "password-change-failed", "Could not change password.")
		return
	}
	s.loginRateLimiter().RecordSuccess(ip)
	if err := s.Auth.RevokeSessionsForUser(r.Context(), username); err != nil {
		httpx.Error(w, r, err)
		return
	}

	s.Log.Info("admin.password_changed", "username", username, "ip", ip)
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) validCSRF(r *http.Request) bool {
	return s.validCSRFValue(r, "")
}

func (s *Server) validCSRFValue(r *http.Request, bodyToken string) bool {
	cookie, err := r.Cookie("ccgsc_csrf")
	if err != nil || cookie.Value == "" {
		return false
	}
	token := r.Header.Get("X-CSRF-Token")
	if token == "" {
		token = bodyToken
	}
	return token != "" && token == cookie.Value
}

// isAdminOrMonitorPage returns true for admin and monitor pages and manuals that require Store A role.
// R15: handles both the /pools/ and the /web/ prefixes.
func isAdminOrMonitorPage(path string) bool {
	var sub string
	switch {
	case strings.HasPrefix(path, "/pools/"):
		sub = strings.TrimPrefix(path, "/pools/")
	case strings.HasPrefix(path, "/web/"):
		sub = strings.TrimPrefix(path, "/web/")
	default:
		return false
	}
	// Whitelist public pages & static assets
	if strings.HasPrefix(sub, "login") || strings.HasPrefix(sub, "contribute") || strings.HasPrefix(sub, "runner") {
		return false
	}
	if strings.HasPrefix(sub, "css/") || strings.HasPrefix(sub, "js/") {
		return false
	}
	if sub == "manual/contribute.html" {
		return false
	}
	// Gate pages and manuals matching *admin* or *monitor*
	return strings.Contains(sub, "admin") || strings.Contains(sub, "monitor") || strings.HasPrefix(sub, "manual/")
}

// adminPageGate applies the Store A admin auth gate to any file server handler.
// R15: one gate serves both the /pools/ and the /web/ trees.
func (s *Server) adminPageGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Ensure login page has a CSRF cookie
		if r.URL.Path == "/pools/login.html" {
			s.getOrSetCSRFToken(w, r)
		}

		// Public assets (CSS, JS, contribute, runner)
		if !isAdminOrMonitorPage(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// 1. Check IP allow-list bypass
		if s.isIPAllowed(r) {
			next.ServeHTTP(w, r)
			return
		}

		// 2. Check session cookie
		if cookie, err := r.Cookie("ccgsc_admin_session"); err == nil && cookie.Value != "" {
			if sess, err := s.Auth.ValidateSession(r.Context(), cookie.Value); err == nil {
				if slices.Contains(sess.Roles, "admin") || slices.Contains(sess.Roles, "director") {
					next.ServeHTTP(w, r)
					return
				}
				// Contributor role attempting to access admin page -> 403 Forbidden
				httpx.Problem(w, r, http.StatusForbidden, "forbidden", "Administrative Store A role required.")
				return
			}
		}

		// 3. Check Bearer token
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			if claims, err := s.Auth.Validate(r.Context(), strings.TrimPrefix(h, "Bearer ")); err == nil {
				if claims.HasRole("admin") || claims.HasRole("director") {
					next.ServeHTTP(w, r)
					return
				}
				// Contributor role attempting to access admin page -> 403 Forbidden
				httpx.Problem(w, r, http.StatusForbidden, "forbidden", "Administrative Store A role required.")
				return
			}
		}

		// 4. Unauthenticated GET admin page -> 302 redirect to login
		http.Redirect(w, r, "/pools/login.html", http.StatusFound)
	})
}

// servePoolsTree serves the /pools/ filesystem gated behind Store A admin auth.
func (s *Server) servePoolsTree(pagesRoot fs.FS) http.Handler {
	return s.adminPageGate(http.StripPrefix("/pools/", http.FileServer(http.FS(pagesRoot))))
}
