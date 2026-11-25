# T53 report

Stored result output now preserves the exact worker-submitted JSON bytes in both stores. `Job.MarshalStoredJSON` changes only durable serialization; normal API `json.Marshal` output remains unchanged. Existing records remain readable because their JSON shape is unchanged.

`internal/store/pg/store_test.go` now asserts the original whitespace-bearing bytes. `tests/core_test.go` submits whitespace-bearing output, reads the persisted result from SQLite, and verifies its SHA-256.

Validation passed:

- `go test ./...`
- `go test -race ./...`
- `go vet ./...`

DONE FIXT53
