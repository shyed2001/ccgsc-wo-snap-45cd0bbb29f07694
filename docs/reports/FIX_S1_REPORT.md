# FIX-S1 Report

Implemented the FIX-S1 auth hardening:

- Removed IP-allowlist API authentication bypass; allowlisting remains limited to admin pages.
- Enforced CSRF on every admin login POST, including simple requests.
- Required an authenticated session for password changes, rate-limited attempts, and revoked all user sessions after success.
- Parsed trusted proxy X-Forwarded-For chains right-to-left.
- Validated trusted proxy CIDRs independently of the IP allowlist.
- Required `Bearer` gRPC metadata, added structured `ErrorDetail` responses, and applied per-key rate limiting.
- Exposed canary, trust, rerun, and tamper fields in REST and gRPC result responses; refreshed the generated protobuf descriptor consistently.

Regression coverage was added for each affected behavior.

Validation:

- `go build ./...` — PASS
- `go vet ./...` — PASS
- `go test -count=1 ./...` — PASS
- `go test -race -count=1 ./...` — PASS
- `node --test web/pages/tests/*.test.mjs` — PASS
- `graphify update .` — completed

Tier 0: repository brief and Graphify ran successfully; Cavemem exposed only CLI usage (no prior-memory query was needed); subscription check was skipped because the referenced script is not present under this workspace; Git status/diff was unavailable because this directory is not a Git repository.
DONE FIX-S1
