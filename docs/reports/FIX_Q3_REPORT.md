# FIX-Q3

Implemented the live FIX-Q3 findings:

- Re-run and canary selection now use a persisted 32-byte HMAC secret with `jobId:taskId`, shared through the durable store across nodes.
- Missing previous Merkle checkpoints now invalidate verification instead of being silently ignored.
- Results accepted after re-run expiry or attempt exhaustion are flagged and recorded as failures; the first worker is not rewarded before comparison.

Held findings already fixed in the current tree:

- Webhook signatures already cover `timestamp.body` and emit `X-WWPP-Timestamp` (`internal/pool/webhook_F7.go:118-122`).
- Historical Merkle checkpoints already accept legitimate chain growth by checking for the historical tip (`internal/pool/chain.go:861-878`).

Validation:

- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `go build ./...`
- `graphify update .` completed; SQL AST extraction warned that `tree_sitter_sql` is not installed.

DONE FIX-Q3
