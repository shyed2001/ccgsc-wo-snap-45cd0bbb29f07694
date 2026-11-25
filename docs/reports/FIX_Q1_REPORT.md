# FIX-Q1 Report

Implemented FIX-Q1 in `pools-core`:

- Cookie-authenticated non-safe writes now require the validated session CSRF token.
- Missing `ExpectedSHA` now returns an explicit `unverified` verdict instead of an accepted hash check; stronger envelope/reputation failures still take precedence.
- Partial live metrics now mark the data source as mock so the MOCK DATA badge is shown.
- Added `X-Frame-Options: DENY` and `Content-Security-Policy: frame-ancestors 'none'`; `X-Content-Type-Options: nosniff` was already present.
- Added regression coverage for cookie CSRF, direct `X-Forwarded-For` spoofing, missing hashes, and security headers.

Held as already fixed in the current tree: `internal/api/admin_auth.go:21` only accepts forwarding headers from configured trusted proxies, and login limiting uses that validated client IP at `internal/api/admin_auth.go:155-158`. The contributor-token item remains deployment-conditional and was not changed.

Validation:

- `go build ./...` passed.
- `go vet ./...` passed.
- `go test -count=1 ./internal/api ./internal/verify ./tests` passed.
- `go test -count=1 -race ./...` passed.
- `node --check js/api.js js/app.js js/runner.js` passed.
- Required `cd web/pages && node --test tests/*.test.mjs`: 145/146 passed; the sole failure is the pre-existing unrelated `runner.html` versus `js/runner.js` generated-content drift in `contribute.test.mjs:1226`.

DONE FIX-Q1
