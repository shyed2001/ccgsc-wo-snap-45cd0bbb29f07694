# FIX-R1 report

Implemented only the FIX-R1 row from `WO_FIX_R8.md`.

## Changes

- Enforced worker lease capacity as separate ordinary and reserved-verification budgets, with an aggregate `slots + reservedVerificationSlots` hard bound across jobs and batches.
- Persisted canary state and evidence on the task after canary settlement, then carried PASS evidence into the final developer result (`CanaryRan`, `CanaryState`, `CanaryOK`, `Verified`, and `TrustComplete`).
- Added regression coverage for reserved verification capacity and settled canary PASS credit.

## Edited source/test files

- `pools-core/internal/pool/batch_F1.go`
- `pools-core/internal/pool/engine.go`
- `pools-core/internal/pool/model.go`
- `pools-core/internal/pool/trust_test.go`
- `pools-core/internal/pool/split_batch_F1_test.go`

## Validation

- `gofmt` check: PASS
- `go vet ./internal/pool ./internal/verify`: PASS
- `go test ./internal/pool ./internal/verify`: PASS
- `go test -race -count=1 ./internal/pool ./internal/verify`: PASS
- `go test ./tests`: PASS
- `go test ./...`: PASS
- `go build ./...`: PASS
- `graphify update .`: PASS; optional SQL parser warning only

DONE FIX-R1
