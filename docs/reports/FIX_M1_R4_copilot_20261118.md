# FIX-M1 Report

Implemented the FIX-M1 row in `pools-core`: hardened F7 webhook validation and delivery, removed whole-job webhook leakage, added DNS-rebinding protection, signed timestamp/body headers, bounded retries, configuration controls, event filtering, delivery concurrency limits, and durable delivery records. Added bulk result export formats (`ndjson`, `json`, `zstd-tar`) with bound cursors, filters, byte limits, ETags, 304 handling, and complete result fields.

Also fixed session-task webhook validation and warm-initializer review gating, updated config examples, OpenAPI contracts, and the bulk protobuf request.

Checks passed: `go build ./...`, `go vet ./...`, `go test ./...`, `go test -race ./...`, targeted webhook tests, and `graphify update`.

DONE FIXM1
