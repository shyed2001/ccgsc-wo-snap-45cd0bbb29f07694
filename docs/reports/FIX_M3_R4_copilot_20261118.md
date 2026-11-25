# FIX-M3 report

The FIX-M3 row was applied in `pools-core`. The API now only trusts forwarded client addresses from configured trusted-proxy CIDRs, applies rate limiting to authenticated requests by API key and to token exchange by client address, and routes artifact reads through authenticated protection. Cancellation distinguishes a newly accepted cancellation (`202`) from an already-terminal repeat (`200`), and result projections preserve late status while encoding non-object or non-JSON output as base64 inline data.

The proto and OpenAPI contracts were aligned for refresh requests, nested key users, webhook/job-chain fields, event identifiers, lease receipts, and thread-pool session capability fields. Contributor pages no longer read bearer tokens from session storage, and runner reports the raw requested worker count while keeping the execution cap. The generated `runner.html` was regenerated.

Checks passed: `go test ./...`, `go vet ./...`, `gofmt -l internal`, and `node --test web/pages/tests/contribute.test.mjs`.

DONE FIXM3
