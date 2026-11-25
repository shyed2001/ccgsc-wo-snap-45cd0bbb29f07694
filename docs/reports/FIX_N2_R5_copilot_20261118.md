# FIX-N2 report

Changed `pools-core/internal/api/admin_auth.go` and `api.go` to use a separately synchronized login limiter, require a matching CSRF cookie/token for logout and password changes, and remove the legacy GET logout route.

Changed `pools-core/internal/pool/upload_scan_F8.go` and its tests to enforce a shared ZIP entry/size budget and bounded nested archive depth. Updated both OpenAPI contracts and `api/ccgsc_pools_v1.proto` with typed chain/Merkle routes, webhook events/capabilities, F8 review routes, and artifact-scan 422 responses.

The checked-in gRPC bindings already contain the chain/Merkle RPC surface used by the implementation. A protobuf compiler is not installed in this work folder, so the source-contract additions for webhook fields could not be regenerated into descriptor code.

Validation: OpenAPI YAML parsing passed; focused API/pool tests passed; `go build ./...`, `go vet ./...`, and `go test -count=1 -race ./...` passed.

DONE FIXN2
