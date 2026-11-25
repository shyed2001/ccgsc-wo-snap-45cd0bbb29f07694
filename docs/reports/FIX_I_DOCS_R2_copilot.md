FIX-I documentation round-2 report

Scope: `F:\tmp\par\fixi_local\pools-core`

Applied:
- `API_IDIOM_TABLE.md`: corrected the file path header, REST/gRPC header, canonical protobuf operation names for all idioms, Executor/map and async-stream mappings, fail-fast behavior, C++ cancellation note, versioned REST paths, request examples, JWT/body placeholders, unsupported-model references, and WWPP source references.
- `THESIS_METRICS_PLAN.md`: corrected the file path header and WWPP-R37.2 reference; added five GPU metrics; routed client-side hash, sandbox, and P2P telemetry through worker upload; changed hash/signature timing units to microseconds; added canary/reputation metrics; clarified lease-store benchmarking; updated chain modes and timing; added developer-only gRPC scope; corrected the CheckArtifact baseline label; expanded benchmark/browser context; added negative controls; linked comparison targets to prior art; updated verification-default language; and appended the requested source decisions.
- Every cited API path was checked against `api/thread_pool_v1.openapi.yaml` or `api/process_pool_v1.openapi.yaml`.
- Every cited operation name was checked against `api/ccgsc_pools_v1.proto`; the OpenAPI files do not declare `operationId`, so protobuf RPC names are used as the canonical operation identifiers.

Review-row status:
- 37 STILL-OPEN rows: applied.
- 8 ALREADY-FIXED rows: skipped; current text already matched the requested correction.
- 1 NOT SURE row concerning `leaseToken`/`workerId` uploadability: skipped as instructed.
- The separate `DOCCHECK_01` row was not edited because the work order restricts edits to the two target documents.

Validation:
- No `TBD (owner)` placeholders remain in `API_IDIOM_TABLE.md`.
- No `.md.md` header remains in either target document.
- Required thread/process paths and all seven cited protobuf RPC names are present in source files.
- Required GPU, telemetry, negative-control, and source-decision text is present.
- No code files were changed; no build/test suite was required.

Tier 0:
- `cavemem status`: local database and worker healthy; 34,989 observations across 773 sessions.
- `subs_watch --quiet`: flat subscriptions reported stale; no subscription mutation was attempted.
- GitAutoFleet inspect: skipped because the command required interactive permission unavailable in non-interactive mode.
- MemPalace: skipped because owner permission is required.
- Graphify/indexer/caveman: skipped; this was a bounded documentation edit, not an architecture query, HQ indexing task, or output-compression request.

DONE FIXI
