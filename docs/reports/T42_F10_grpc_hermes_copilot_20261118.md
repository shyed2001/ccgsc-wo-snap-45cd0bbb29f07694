# T42 F10B - F10 gRPC port

Ported the prior `internal/grpcapi` implementation and tests onto the current
`pools-core` base. Added the shared gRPC service, auth/error mapping, artifact,
job, result, session, capability, chain, Merkle, and event-stream adapters;
wired h2c gRPC and REST multiplexing into `cmd/pools-core`; and added the
required gRPC dependencies.

Adapted the current engine cancellation return signature and updated the gRPC
artifact fixture to satisfy the current upload scanner's WASM validation.
The environment had no available `protoc` executable, so the checked-in
protobuf bindings were carried forward from the prior F10 generation; exact
FIX-M1/FIX-M3 regeneration remains outstanding.

Validation passed:

- `gofmt -l internal cmd`
- `go build ./...`
- `go vet ./...`
- `go test -count=1 -race ./...`

DONE T42F10B
