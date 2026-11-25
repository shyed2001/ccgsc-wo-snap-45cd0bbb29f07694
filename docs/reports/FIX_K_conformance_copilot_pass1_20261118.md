# FIX-K report

Implemented the Gemini conformance fixes in `pools-core`:

- Reconciled capabilities JSON with the proto/OpenAPI contract, including quota names, top-level priority classes, server default batch sizes, upload checks, P2P, and dew.
- Added schema-shaped REST task results with `TaskOutput`-style output and `ErrorDetail`-style errors.
- Returned `201` for complete uploads and `200` plus `Upload-Offset` for incomplete uploads; added proto `content_type`.
- Added `GetArtifact`, `RefreshToken`, and `CreateKey` declarations to both proto services and documented the routes in both OpenAPI files.
- Fixed split validation estimates and device classes.
- Added job `startedAt`, `groupBy`, `pin`, `resultCursor`, and `configVersion` response fields; added proto `config_version`.
- Enforced configured REST rate limits with `429` and `Retry-After`.
- Added SSE `sessionIds` filtering and documented `reauth`.
- Added focused FIX-K API response-shape tests.

Already correct and held: HTTP artifact download (`internal/api/api.go:138-160`), auth refresh/key routes (`internal/api/api.go:116-134`), SSE renewal emission (`internal/api/api.go:565-570`), and bulk `includeFailed` handling (`internal/api/api.go:430-450`). gRPC interceptor/runtime work remains F10 and was not built.

Validation: `gofmt`, `go test ./internal/...`, and YAML parsing for both OpenAPI documents passed. The broader legacy `tests` package still contains pre-FIX-K expectations for upload `200` and removed capability names; those tests were not used as FIX-K acceptance because the contract now requires `201` for complete uploads.

DONE FIXK
