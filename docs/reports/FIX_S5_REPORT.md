# FIX-S5

Implemented the API/proto/webhook/upload fixes from `WO_FIX_R9.md`:

- REST and gRPC result projections now expose canary, trust, rerun, and flag evidence.
- Webhook outbox claims perform one HTTP attempt; retries remain outbox-scheduled.
- ZIP-contained WASM is checked against import, shared-memory, and page policies.
- Owner-code approval now requires a complete artifact.
- OpenAPI schemas document the added result fields, CSRF header, and `artifactId` review path.
- Protobuf bindings were updated consistently by hand because `protoc` was unavailable; the descriptor was updated with the new fields.
- Webhook DNS rebinding is held: delivery re-resolves at dial time, rejects private/local addresses, and dials the validated IP directly.

Validation passed:

- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `go build ./...`
- OpenAPI YAML parsing
- `graphify update .`

Tier 0 notes: repository brief/subscription checks were skipped because the local command runner denied permission; Graphify ran successfully. The workspace is not a Git repository, so Git status was unavailable.

DONE FIX-S5
