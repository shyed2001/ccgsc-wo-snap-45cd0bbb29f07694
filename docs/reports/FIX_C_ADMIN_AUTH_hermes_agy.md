# WO FIX-C - Admin Authentication & Access Control Report

## Overview
Work order `WO_FIX_C_ADMIN_AUTH` has been fully implemented in `F:/tmp/hcwork/fixc/pools-core`. This resolves security and access control findings identified in `REVIEW_02` regarding unauthenticated access to the `/pools/` admin/monitor tree, implementing Store A authentication, session management, CSRF protection, IP allow-listing, rate limiting, and audit logging.

## Implemented Components & Scope

### 1. Store A Authentication & Passwords
- **Store & Role Model**: Enforces Store A roles (`admin` and `director`). Users belonging to Store B (`contributor` and `developer`) are strictly forbidden (HTTP 403) from administrative sessions.
- **Hash Support**: Supported both `bcrypt` and standard PHC-formatted `argon2id`/`argon2i` password hashes (`internal/auth/password.go`).
- **Debug Seed User**: If no Store A user exists on first startup, a debug seed user `admin` is generated with a secure 16-character random password and `MustChangePassword: true`. The password is printed ONCE to standard output (`os.Stdout`) and is never written to log files (`internal/logx`). On subsequent boots, existing accounts are recognized and no seed user is created.
- **Config User Seeding**: Configurable `adminUsers` array in `config.Config` allowing pre-configured bcrypt or Argon2 password hashes to be seeded.

### 2. Session Management & Cookies
- **Session Tokens**: Generated with cryptographically secure random identifiers (`auth.ID() + auth.ID()`), stored in isolated storage bucket `admin_sessions`.
- **Cookies**: Set upon successful login with:
  - `Name: ccgsc_admin_session`
  - `HttpOnly: true`
  - `Secure: true`
  - `SameSite: Strict`
  - `Max-Age: 86400` (24 hours)

### 3. CSRF Protection
- **Token Generation**: Dual CSRF token issuance via `GET /pools/auth/csrf` and during login, backed by the `ccgsc_csrf` cookie (`SameSite=Strict`).
- **Validation**: Enforced on all state-changing `POST` requests. Form submissions and JSON calls validate the matching header `X-CSRF-Token` or form field `csrf_token` against the cookie/session token. Mismatches return HTTP 403 Forbidden.

### 4. Gating `/pools/*admin*` and `/pools/*monitor*`
- **Route Gating**: Any page or manual matching `*admin*` or `*monitor*` requires an active Store A session (`admin` or `director` role) or an enabled IP allow-list match.
- **Redirection**: Unauthenticated GET requests to admin/monitor pages return HTTP 302 Found redirecting to `/pools/login.html`.
- **Public Assets Preserved**: Genuinely public assets (`/pools/contribute.html`, `/pools/runner.html`, `/pools/login.html`, `/pools/css/*`, `/pools/js/*`, `/nav/*`) remain unauthenticated and publicly accessible.
- **Contributor Role Restriction**: Attempted access with a Store B contributor session returns HTTP 403 Forbidden.

### 5. Optional IP Allow-List
- **Configuration**:
  - `ipAllowlistEnabled`: Boolean flag (default `false`).
  - `ipAllowlist`: Array of CIDR strings (default `["127.0.0.1/32", "100.64.0.0/10"]` covering localhost and Tailscale).
  - Environment variable overrides: `POOLS_IP_ALLOWLIST_ENABLED` and `POOLS_IP_ALLOWLIST`.
- **Behavior**: When enabled, requests from matching IPs bypass the login requirement, allowing admin/monitor access without credentials. When disabled, standard session authentication is strictly enforced.

### 6. Rate Limiting & Audit Logging
- **Sliding-Window Rate Limiter**: Configurable via `loginRateLimit` (default 5 attempts/minute). Tracks failed login attempts per client IP. Exceeding the threshold triggers HTTP 429 Too Many Requests. Successful login clears failures.
- **Audit Logging**: Every login attempt (success, wrong password, forbidden role, rate-limited) produces a structured `slog` entry (`admin.login`) and an entry in `store.Audit`. Credentials, password hashes, and session tokens are strictly excluded from logs.

### 7. UI Components & Navigation
- **Login Page (`web/pages/login.html`)**: Responsive, accessible login interface matching CCGSC dark theme styling. Features login form, hidden CSRF field, error/rate-limit alerts, forced password change transition, and loads shared navigation.
- **Navigation Registry (`web/nav.json`)**: Added public `Admin Login` link targeting `https://ccgsc-director.digitalbd.org/pools/login.html`.

## Test Results

### Go Build & Test Verification
```
go build ./... -> PASS (exit code 0)
go vet ./...   -> PASS (exit code 0)
go test -count=1 ./... -> PASS:
  ok  ccgsc/poolscore/internal/auth     2.588s
  ok  ccgsc/poolscore/internal/pool     2.848s
  ok  ccgsc/poolscore/internal/store/pg 1.276s
  ok  ccgsc/poolscore/internal/verify   1.563s
  ok  ccgsc/poolscore/tests             3.678s
```

Acceptance test coverage in `tests/admin_auth_test.go`:
- `TestAdminAuth_UnauthRedirect`: Unauthenticated GET to admin/monitor pages -> 302 to `/pools/login.html`; public pages -> 200.
- `TestAdminAuth_WrongPasswordRateLimitAndSuccess`: Wrong password -> 401; 4th failure -> 429 rate limited; right password -> 200 with HttpOnly+Secure+SameSite=Strict cookie; subsequent admin page access -> 200.
- `TestAdminAuth_ContributorForbidden`: Store B contributor login attempt -> 403; contributor session on admin page -> 403.
- `TestAdminAuth_AllowlistOnOff`: Allowlist OFF -> 302; Allowlist ON with 127.0.0.1 and 100.64.42.1 -> 200; outside IP -> 302.
- `TestAdminAuth_CSRFToken`: Mismatched CSRF token -> 403; matching CSRF token -> 200.
- `TestAdminAuth_Argon2ConfigUser`: Argon2id config hash verification -> 200.

### Node Test Suites
```
node --test web/nav.test.mjs web/pages/nav/nav.test.mjs web/pages/tests/*.test.mjs
-> 112 tests passing across 29 suites, 0 failures, 0 skipped.
```

DONE FIXC
