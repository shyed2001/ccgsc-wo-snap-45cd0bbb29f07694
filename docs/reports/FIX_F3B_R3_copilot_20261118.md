# FIX-F3B

Implemented the current-code redo of T42 F3:

- Added the per-job SHA-256 audit chain, independent verification, tamper detection, and admin-gated `options.tamperEvidence`.
- Added periodic/global Merkle checkpoints, chained checkpoint verification, and checkpoint/job HTTP routes.
- Added chain fields to job persistence and API views, truthful capabilities reporting, accepted-result synchronization, and sweep checkpointing.
- Added the ported pool unit tests, benchmark, and HTTP acceptance tests.

Validation:

- `gofmt` PASS
- `go test -count=1 ./internal/pool ./internal/api ./tests` PASS
- `go build ./...` PASS
- `go vet ./...` PASS
- `go test -count=1 -race ./...` PASS
- `graphify update .` PASS (SQL parser warning only: `tree_sitter_sql` unavailable)

DONE FIXF3B
