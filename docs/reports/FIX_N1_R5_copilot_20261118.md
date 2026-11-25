# FIX-N1 report

## Files changed

- `pools-core/internal/api/api.go`
- `pools-core/internal/pool/webhook_F7.go`
- `pools-core/internal/pool/webhook_F7_test.go`

## Changes

- Made webhook outbox enqueue atomic with committed job events.
- Added stable delivery IDs, frozen payloads, claim/visibility state, attempt counts, permanent failure termination, and non-blocking semaphore acquisition.
- Propagated `job.done`/`job.failed`, disabled proxy use, emitted bare-hex signatures, bounded `Retry-After`, and removed URL-bearing stored/logged errors.
- Serialized bulk JSON/NDJSON through `resultView`, constrained pages before the byte cap, and named tar members `<taskId>-<sha>.json`.
- Updated the webhook signature test for the accepted bare-hex contract.

## Validation

- `gofmt`: passed
- `go build ./...`: passed
- `go vet ./...`: passed
- `go test -count=1 ./internal/pool ./internal/api`: passed
- `go test -count=1 -race ./...`: passed
- `graphify update`: passed; SQL extraction warning reported because `tree_sitter_sql` is unavailable
- Repository-local AGENTS/CLAUDE/GEMINI and requested `.ai` pre-read files were absent; no git metadata was present in the work folder.

DONE FIXN1
