# Developer User Manual — Source-Aligned

## Select a pool
Thread Pool API: /thread-pool/v1. Process Pool API: /process-pool/v1. Both supply scoped authentication, artifact upload, job submission, result retrieval and event streaming. Process Pool also supports session APIs. The app developer remains the owner of application databases and data.

## Create and observe a run
1. Obtain a scoped developer key through the admin-authorized route; exchange it for a time-limited bearer token with POST {pool}/auth/token or gRPC ExchangeToken.
2. Upload a content-addressed artifact via PUT {pool}/artifacts, using X-Artifact-Sha256 and the supported chunk headers. Keep credentials out of scripts committed to git.
3. Submit using POST {pool}/jobs or gRPC CreateJob. Check fields against internal/pool/model.go and proto CreateJobRequest; preflight with POST {pool}/jobs:validate.
4. Poll GET {pool}/jobs/{jobId}, watch GET {pool}/events SSE (Last-Event-ID supported), or gRPC StreamEvents.
5. Read GET {pool}/jobs/{jobId}/results (cursor) and results:bulk; inspect optional GET {pool}/jobs/{jobId}/chain for the existing result-chain proof. Check failure counts; a completed lease is not a correctness guarantee.

## Troubleshooting and limits
Use the machine-readable HTTP problem code, gRPC status and request ID. Reauthenticate on expiry and use documented backoff. Operator log-level overrides: POOLS_LOG_MODULE_LEVELS (documented in docs/F5_ERROR_LOGGING_AUDIT.md). See docs/NATIVE_APPS.md for pools-client/pools-e2e checks.

## Not shipped as of F6
Per-app W/K virtual pool quotas, a network-egress allowlist for sandbox workers, P2P WebRTC transport, and the proposed new per-run code/input/result audit chain are separate F8–F10/F4 tasks. Do not infer availability from generic pool names.
