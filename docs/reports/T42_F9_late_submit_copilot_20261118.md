# T42 F9 report

Implemented row F9 in `pools-core`.

Files changed:

- `internal/pool/model.go`: durable `LateLeaseEvidence`; late result metadata.
- `internal/pool/engine.go`: expiry snapshot and atomic late-submit path with generation/token/worker/owner fencing, signed-envelope verification, replay consumption, and existing hash/canary/re-run checks.
- `internal/pool/batch_F1.go`: clear superseded expiry evidence when a new generation is claimed.
- `internal/pool/late_submit_F9.go`: expiry snapshot helper, expired-envelope verification helper, and P2P/dew interfaces only.
- `internal/pool/late_submit_F9_test.go`: focused F9 acceptance, replay, re-lease, terminal, and authenticity tests.
- `internal/verify/envelope.go`: verifier variant that permits expiry only after caller-proven lease evidence while retaining signature, canonical payload, key status, and issuance checks.

Validation:

- `go build ./...` — PASS
- `go vet ./...` — PASS
- `go test -count=1 -race ./...` — PASS
- Focused `go test -count=1 ./internal/pool ./internal/verify` — PASS

P2P/dew remains interface-only with no peer transport, discovery, offline execution, or settlement bypass.

DONE T42F9
