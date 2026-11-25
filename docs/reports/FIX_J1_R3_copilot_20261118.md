FIX-J1 completed.

Changed:
- `pools-core/internal/pool/batch_F1.go`: suppress duplicate task IDs while assembling a batch; existing per-task lease generation, deadline, envelope input/artifact binding, and settlement fencing remain active.
- `pools-core/internal/pool/engine.go`: expand split tasks before queued-task quota admission, preventing split requests from bypassing the developer quota.
- `pools-core/internal/pool/fix_j1_test.go`: regression test for split-expanded queued quota.

Validation:
- `gofmt -w internal\pool\batch_F1.go internal\pool\engine.go internal\pool\fix_j1_test.go` — passed.
- `go build ./...` — passed.
- `go vet ./...` — passed.
- `go test -count=1 ./internal/pool -run FIXJ1` — passed.
- `go test -count=1 -race ./...` — passed.
- `graphify update .` — completed; reported only the pre-existing missing SQL tree-sitter dependency warning.

AAAK.TIER0: GitAutoFleet inspect was attempted but denied by the non-interactive permission gate; graphify update ran successfully; no installs or git commands used.
DONE FIXJ1