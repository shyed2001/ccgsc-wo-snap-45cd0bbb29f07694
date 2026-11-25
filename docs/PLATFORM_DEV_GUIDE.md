# Platform Developer Guide — Source and Verification Boundaries

> **Runtime reconciliation (R3E):** [R3E runtime contract](R3E_RUNTIME_CONTRACT.md) is the authoritative companion for implemented endpoints, supported versus design-only settings, DTOs, and transport-specific limits at this baseline. It supersedes conflicting historical statements. No runtime change is implied.

## Code architecture
Shared core: internal/auth, internal/store, internal/pool, internal/api and internal/grpcapi. Current generic pool endpoints select Thread or Process; session APIs are Process-specific. Browser client: web/pages/js/contribute.js, runner.js, and a generated runner.html; WASM and Web Workers are isolated from coordinator storage. Workload-specific decoding stays in workload modules, never the common platform ABI.

## Storage/concurrency
PostgreSQL via POOLS_PG_DSN, local SQLite fallback, and optional Redis lease paths. Test PostgreSQL fixture loading uses a private schema and a public-catalog guard (internal/store/pg/pg_package_isolation_N3_test.go). For optional multi-server settings sync, internal/pool/recompute_notify_N11.go reconnects, executes LISTEN, and periodically refreshes durable state. It is disabled in single-server mode.

F6 review fix: recomputeWriteMu now serializes settings writes while recomputeMu protects only the in-memory settings cache. Test TestF6RecomputeReadLockFreeWhileAdminWriteWaitsForDB demonstrates the original stall and the corrected behavior.

## Logging and protocol evidence
Structured JSON in internal/logx; POOLS_LOG_MODULE_LEVELS is an opt-in per-event-module level override. REST implementation index: docs/API_REST_SSE_REFERENCE.md; gRPC service methods: docs/API_GRPC_REFERENCE.md. Go tests, race, Node pages, workload tests, runner artifact parity, and browser smoke are the full local gate.

## Pending features and strict limits
F8–F10 add app-specific logical Process/Thread pools and default-deny app network egress, but must verify null-origin worker storage/crypto/fetch/CSP behavior first. F4 must not duplicate the existing GET /jobs/{jobId}/chain: its proposed fail-open module/input/result per-run record differs materially from the existing fail-closed per-job result chain. F3 opt-in N/Q and validator callback is DEFERRED by binding owner answer.

No staging, deployment, live switch or GitHub Actions dispatch is authorized. Review findings and unresolved concurrency risks are in docs/F6_REVIEW_TRIAGE.md.
