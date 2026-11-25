# FIX-R2

Implemented only the FIX-R2 row from `WO_FIX_R8.md`.

Changes:
- Canary injection now uses `ShouldRerunWithKey` with the persisted sampling secret, matching scheduling decisions.
- Metering is collected during store transactions and applied only after a successful commit; rollback cannot increase counters.
- Merkle checkpoint verification walks every predecessor back to the sequence-1 anchor and rejects missing ancestors.
- Added regressions for keyed canary selection, rollback metering, and incomplete checkpoint ancestry.

Edited files:
- `pools-core/internal/pool/trust.go`
- `pools-core/internal/pool/engine.go`
- `pools-core/internal/pool/chain.go`
- `pools-core/internal/pool/trust_test.go`
- `pools-core/internal/pool/fix_q2_test.go`
- `pools-core/internal/pool/chain_test.go`

Validation:
- `go test ./...` passed, including `./tests`.
- `go test -race -count=1 ./internal/pool ./internal/verify` passed.
- `go vet ./...` passed.
- `go build ./...` passed.
- `graphify . --update --no-viz --code-only` completed and wrote `graphify-out/graph.json`.
DONE FIX-R2
