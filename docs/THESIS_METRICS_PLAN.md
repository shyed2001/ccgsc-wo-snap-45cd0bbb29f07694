<!-- File: CCGS_WASM/pools-core/docs/THESIS_METRICS_PLAN.md | Draft by mmx MiniMax-M3 for Claude Opus 5.5, 2026-11-17 (Dhaka) | input = Tier 0 slice of docs/decisions/GRILL_WWPP_R1.md + api/*.yaml | owner review pending -->

# CCGSC pools-core thesis metrics plan

2026-11-18 Codex web: applied DeepSeek + Copilot doc findings

## Goal

This plan supports the MSc MCSE thesis proof of concept for CCGSC pools-core.

Decision WWPP-R37.2 applies:

- Light metrics are always on.
- A heavy benchmark harness is used for owner-started thesis runs.
- Measurements cover overhead, speed, performance, benchmarks, effectiveness, efficiency, and accuracy.
- All CCGSC features are included.

## Metrics

**Implementation status (2026-11-18):** This is a measurement *plan*, not a list of live routes or populated PostgreSQL metrics. `internal/verify/metrics.go` implements in-memory verification timing/outcome counters; the general PostgreSQL pool store remains a stub (`internal/store/pg/pg.go`), and `internal/api/api.go` exposes no metrics endpoint. The PostgreSQL metrics table, exporter and heavy harness are **Planned - spec/integration pending (WWPP-R37.2)**. Keep absent measures explicitly unmeasured (never fabricate zero-valued measurements). gRPC services have protobuf definitions in `api/ccgsc_pools_v1.proto` but no running server. Sandbox, P2P, dew and chain paths are specified or owner-decided but not exposed as measured OpenAPI endpoints.


| Feature | Metric | Unit | How measured | Mode | Where logged |
|---|---|---|---|---|---|
| Overhead | Baseline and measured execution time | ms | Compare each feature with the same workload disabled and enabled | Light and heavy | Planned PG metrics table (not wired) |
| Overhead | Added CPU time | ms or % | Measure CPU time with each feature enabled and disabled | Heavy | Planned PG metrics table (not wired) |
| Overhead | Added memory | MiB | Measure memory use before and after each feature | Heavy | Planned PG metrics table (not wired) |
| Speed | End-to-end task latency | ms | Time from task acceptance to result acceptance | Light and heavy | Planned PG metrics table (not wired) |
| Speed | Per-stage latency | ms | Record timings for lease, sandbox, transfer, execution, verification, and result handling | Light | Planned PG metrics table (not wired) |
| Performance | Throughput | tasks/s | Count completed tasks over the measurement interval | Heavy | Planned PG metrics table (not wired) |
| Performance | Worker utilisation | % | Measure busy worker time divided by available worker time | Heavy | Planned PG metrics table (not wired) |
| Performance | Pool occupancy | workers or % | Record active, idle, busy, and unavailable workers at each sample | Light | Planned PG metrics table (not wired) |
| Benchmarks | Workload runtime | ms | Run the defined workload set under each feature and configuration | Heavy | Planned PG metrics table (not wired) |
| Effectiveness | Tamper detection rate | % or detected/total | Compare detected tampering with the tamper test set | Heavy | Planned PG metrics table (not wired) |
| Effectiveness | Verification sampling coverage | % | Record sampled workers and tasks against the configured sampling rule | Light | Planned PG metrics table (not wired) |
| Effectiveness | Canary outcome | pass, fail, or warn | Run known-answer canary tasks and record the result | Light and heavy | Planned PG metrics table (not wired) |
| Effectiveness | Reputation outcome | warn, bar, or ban | Apply the configured reputation action and record the outcome | Light and heavy | Planned PG metrics table (not wired) |
| Efficiency | Useful verified work | verified tasks/s | Divide verified completed tasks by elapsed time | Heavy | Planned PG metrics table (not wired) |
| Efficiency | Verification cost | ms per task | Divide verification time by the number of verified tasks | Heavy | Planned PG metrics table (not wired) |
| Efficiency | Completed work per resource unit | tasks/CPU-s or tasks/GPU-s | Measure completed work against CPU or GPU resource use | Heavy | Planned PG metrics table (not wired) |
| GPU | WebGPU adapter detection rate | % | Planned: tabs reporting usable adapter / tabs | Light | Planned PG metrics table (not wired) |
| GPU | WGSL shader compile latency | ms | Planned | Light | Planned PG metrics table (not wired) |
| GPU | GPU kernel dispatch latency | ms | Planned | Light | Planned PG metrics table (not wired) |
| GPU | GPU vs CPU speedup ratio | ratio | Planned | Heavy | Planned PG metrics table (not wired) |
| GPU | GPU task verification agreement rate | % | Planned | Heavy | Planned PG metrics table (not wired) |
| Accuracy | Hash check result | match or mismatch | Compare the loaded artifact hash with the expected hash | Light | Planned PG metrics table (not wired) |
| Accuracy | Signed envelope validity | valid or invalid | For implemented task envelopes, verify Ed25519 over domain-separated canonical `TaskEnvelope` (jobId, taskId, artifactSha256, inputSha256, leaseToken, workerId, issuedAt, expiresAt, keyId) in `internal/verify/envelope.go`. **Separate planned dew receipt:** `LeaseReceipt` proto carries receiptId, taskIds, inputHashes, deadline, issuedAt, windowSeconds, algorithm, keyId, signature (WWPP-R36.2); the protobuf does not specify the exact signing bytes. | Light | Planned PG metrics table (not wired) |
| Accuracy | Re-run agreement | agree or disagree | Compare the re-run result with the original result | Heavy | Planned PG metrics table (not wired) |
| Accuracy | Lease record correctness | correct or incorrect | Check lease state, ownership, and settlement consistency | Light and heavy | Planned PG metrics table (not wired) |
| Accuracy | Hash-chain validation | valid or invalid | Validate the chain and record the result | Light | Planned PG metrics table (not wired) |
| Tamper: hash on load | Hash verification latency | us | Time the hash check on every load, including IndexedDB cache hits | Light | Worker telemetry uploaded to server; aggregated into planned PG metrics table (route TBD) |
| Tamper: hash on load | Hash mismatch count | count | Count failed artifact hash checks | Light | Worker telemetry uploaded to server; aggregated into planned PG metrics table (route TBD) |
| Tamper: signed envelope | Signature verification latency | us | Time Ed25519 envelope verification | Light | Worker telemetry uploaded to server; aggregated into planned PG metrics table (route TBD) |
| Tamper: signed envelope | Invalid signature count | count | Count invalid or missing signed envelopes | Light | Worker telemetry uploaded to server; aggregated into planned PG metrics table (route TBD) |
| Tamper: re-run percent | Re-run sample rate | % | Record the configured random re-run percentage and the number of selected tasks | Light | Planned PG metrics table (not wired) |
| Tamper: re-run percent | Re-run agreement rate | % | Compare re-run results with original results | Light and heavy | Planned PG metrics table (not wired) |
| Tamper: canary and reputation | Canary success rate | % | Count successful known-answer canary tasks against all canary tasks | Light | Planned PG metrics table (not wired) |
| Tamper: canary and reputation | Reputation action count | count | Record each warn, bar, or ban action | Light | Planned PG metrics table (not wired) |
| Tamper: canary and reputation | Canary check latency | us | Planned: time OpCanary | Light | Planned PG metrics table (not wired) |
| Tamper: canary and reputation | Reputation distribution | % active/barred/banned | Planned | Light | Planned PG metrics table (not wired) |
| Pools: lease latency | Lease acquisition latency | ms | Time from lease request to lease grant | Light | Planned PG metrics table (not wired) |
| Pools: lease latency | Lease settlement latency | ms | Time from completed work to lease settlement | Light | Planned PG metrics table (not wired) |
| Pools: throughput | Completed task throughput | tasks/s | Count completed tasks over the measurement interval | Heavy | Planned PG metrics table (not wired) |
| Pools: worker utilisation | Worker busy time | % | Divide worker busy time by available worker time | Heavy | Planned PG metrics table (not wired) |
| Pools: lease store | Lease-store performance | ms and ops/s | Compare the PG lease store with the Redis lease and PG settle option; Redis benchmark needs external adapter and is post-M1 per WWPP-R19.3. | Heavy | Planned PG metrics table (not wired) |
| Sandbox runner | Sandbox startup latency | ms | Planned - spec pending (WWPP-R28.1): instrument sandbox lifecycle, compilation, transfer and resource use in the isolated runner; no sandbox path in the current OpenAPI. | Light | Worker telemetry uploaded to server; aggregated into planned PG metrics table (route TBD) |
| Sandbox runner | Sandbox execution overhead | ms or % | Planned - spec pending (WWPP-R28.1): instrument sandbox lifecycle, compilation, transfer and resource use in the isolated runner; no sandbox path in the current OpenAPI. | Heavy | Worker telemetry uploaded to server; aggregated into planned PG metrics table (route TBD) |
| Sandbox runner | WASM compile latency | ms | Planned - spec pending (WWPP-R28.1): instrument sandbox lifecycle, compilation, transfer and resource use in the isolated runner; no sandbox path in the current OpenAPI. | Light | Worker telemetry uploaded to server; aggregated into planned PG metrics table (route TBD) |
| Sandbox runner | Sandbox transfer latency | ms | Planned - spec pending (WWPP-R28.1): instrument sandbox lifecycle, compilation, transfer and resource use in the isolated runner; no sandbox path in the current OpenAPI. | Light | Worker telemetry uploaded to server; aggregated into planned PG metrics table (route TBD) |
| Sandbox runner | Sandbox resource use | MiB and % | Planned - spec pending (WWPP-R28.1): instrument sandbox lifecycle, compilation, transfer and resource use in the isolated runner; no sandbox path in the current OpenAPI. | Heavy | Worker telemetry uploaded to server; aggregated into planned PG metrics table (route TBD) |
| P2P | P2P delivery latency | ms | Planned - spec pending (WWPP-R36.2): instrument SHA-256-addressed peer artifact/input delivery attempts, latency and server fallback; results always return to server. `P2POptions` is not an instrumented delivery route. | Light | Worker telemetry uploaded to server; aggregated into planned PG metrics table (route TBD) |
| P2P | P2P success rate | % | Planned - spec pending (WWPP-R36.2): instrument SHA-256-addressed peer artifact/input delivery attempts, latency and server fallback; results always return to server. `P2POptions` is not an instrumented delivery route. | Heavy | Worker telemetry uploaded to server; aggregated into planned PG metrics table (route TBD) |
| P2P | P2P overhead | ms or % | Planned - spec pending (WWPP-R36.2): instrument SHA-256-addressed peer artifact/input delivery attempts, latency and server fallback; results always return to server. `P2POptions` is not an instrumented delivery route. | Heavy | Worker telemetry uploaded to server; aggregated into planned PG metrics table (route TBD) |
| P2P | Merkle checkpoint re-verification | valid or invalid and ms | Planned - spec pending (WWPP-R22.3): time and verify Merkle-root checkpoints built from job-chain tips; no checkpoint endpoint in current OpenAPI. | Heavy | Worker telemetry uploaded to server; aggregated into planned PG metrics table (route TBD) |
| Dew | Dew processing latency | ms | Planned - spec pending (WWPP-R36.2, R13.2): instrument signed receipt lifecycle, offline execution, late-result submission and verification; compare with online baseline. `DewOptions`/`LeaseReceipt` are schema only. | Light | Planned PG metrics table (not wired) |
| Dew | Dew throughput | items/s or units/s | Planned - spec pending (WWPP-R36.2, R13.2): instrument signed receipt lifecycle, offline execution, late-result submission and verification; compare with online baseline. `DewOptions`/`LeaseReceipt` are schema only. | Heavy | Planned PG metrics table (not wired) |
| Dew | Dew accuracy | correct/incorrect or % | Planned - spec pending (WWPP-R36.2, R13.2): instrument signed receipt lifecycle, offline execution, late-result submission and verification; compare with online baseline. `DewOptions`/`LeaseReceipt` are schema only. | Heavy | Planned PG metrics table (not wired) |
| Dew | Dew overhead | ms or % | Planned - spec pending (WWPP-R36.2, R13.2): instrument signed receipt lifecycle, offline execution, late-result submission and verification; compare with online baseline. `DewOptions`/`LeaseReceipt` are schema only. | Heavy | Planned PG metrics table (not wired) |
| Hash chain | Chain append latency | us | Planned - spec pending (WWPP-R26.2, R22.3): instrument opt-in per-job append, validation, chain checks and disabled baseline; owner requires build/test before MVP completion. | Light (when chain enabled; default OFF per WWPP-R26.2) and heavy | Planned PG metrics table (not wired) |
| Hash chain | Chain validation latency | ms | Planned - spec pending (WWPP-R26.2, R22.3): instrument opt-in per-job append, validation, chain checks and disabled baseline; owner requires build/test before MVP completion. | Light (when chain enabled; default OFF per WWPP-R26.2) and heavy | Planned PG metrics table (not wired) |
| Hash chain | Chain overhead | ms or % | Planned - spec pending (WWPP-R26.2, R22.3): instrument opt-in per-job append, validation, chain checks and disabled baseline; owner requires build/test before MVP completion. | Heavy | Planned PG metrics table (not wired) |
| Hash chain | Chain validity | valid or invalid | Planned - spec pending (WWPP-R26.2, R22.3): instrument opt-in per-job append, validation, chain checks and disabled baseline; owner requires build/test before MVP completion. | Light (when chain enabled; default OFF per WWPP-R26.2) and heavy | Planned PG metrics table (not wired) |
| API: REST | REST latency | ms | Measure REST request handling time | Light | Planned PG metrics table (not wired) |
| API: REST | REST throughput | requests/s | Count completed REST requests over the measurement interval | Heavy | Planned PG metrics table (not wired) |
| API: REST | REST error rate | % | Count failed REST requests against all REST requests | Light | Planned PG metrics table (not wired) |
| API: SSE | SSE connection latency | ms | Measure time to establish an SSE connection | Light | Planned PG metrics table (not wired) |
| API: SSE | SSE event latency | ms | Measure time from event production to event delivery | Light | Planned PG metrics table (not wired) |
| API: SSE | SSE connection count | count | Record active SSE connections at each sample | Light | Planned PG metrics table (not wired) |
| API: SSE | SSE delivery success rate | % | Count delivered events against all produced events | Heavy | Planned PG metrics table (not wired) |
| API: gRPC | gRPC latency | ms | Planned - spec pending (WWPP-R32.8): instrument service RPC start/end, status and call counters after generating/serving `api/ccgsc_pools_v1.proto`; not measurable from REST OpenAPI; developer client/CLI only once gRPC service active; browser contributors do not use gRPC. | Light | Planned PG metrics table (not wired) |
| API: gRPC | gRPC throughput | calls/s | Planned - spec pending (WWPP-R32.8): instrument service RPC start/end, status and call counters after generating/serving `api/ccgsc_pools_v1.proto`; not measurable from REST OpenAPI; developer client/CLI only once gRPC service active; browser contributors do not use gRPC. | Heavy | Planned PG metrics table (not wired) |
| API: gRPC | gRPC error rate | % | Planned - spec pending (WWPP-R32.8): instrument service RPC start/end, status and call counters after generating/serving `api/ccgsc_pools_v1.proto`; not measurable from REST OpenAPI; developer client/CLI only once gRPC service active; browser contributors do not use gRPC. | Light | Planned PG metrics table (not wired) |

## Baseline numbers already measured

The following baseline numbers are already measured. They must be copied exactly in each thesis run record.

| Measurement | Baseline |
|---|---|
| SHA-256, 1 MB | 2101 MB/s |
| Ed25519 verify | 37.3 us |
| Ed25519 sign | 15.6 us |
| Canonical JSON | 4.8 us |
| `ShouldRerun` | 79 ns |
| `CheckArtifact, 4 KiB` | 2.1 us |
| Browser WASM compile | 0.7 ms |
| Browser SHA-256 | ~1 ms/MB |
| Browser Ed25519 | 0.4 ms |

The benchmark context is `CONTROL-01 (Windows amd64, Go 1.26.2). The browser context is Spike2 (Edge headless, Chromium, Firefox, WebKit).

## Heavy harness plan

- The owner starts each heavy thesis run.
- The harness runs along or beside the MVP.
- Use small development sizes first. Final sizes are TBD (owner).
- Run negative controls: mismatched artifact SHA-256, forged/mutated Ed25519 signature, expired lease envelope, tampered payload, canary answer mismatch; then run effectiveness, efficiency, and accuracy tests.
- Run the overhead benchmark with the hash chain OFF and ON.
- Run offline and late submission ordering tests.
- Run P2P and Merkle checkpoint re-verification tests.
- Run speed, performance, and benchmark tests for all options.
- Run effectiveness, efficiency, and accuracy tests for the tamper measures and all CCGSC features.
- Run tests in parallel where possible. Otherwise, run them serially.
- Record all heavy harness results in the planned PostgreSQL metrics table **after** its schema, collector and route are implemented; until then, save timestamped reproducible run artifacts without claiming persistence.

## Comparison targets

| Comparison target | Target data |
|---|---|
| HPC | See docs/decisions/PRIOR_ART_POOLS.md; comparative metrics TBD (owner) |
| GPU | See docs/decisions/PRIOR_ART_POOLS.md; comparative metrics TBD (owner) |
| GPU servers | See docs/decisions/PRIOR_ART_POOLS.md; comparative metrics TBD (owner) |
| Local hardware alternatives | See docs/decisions/PRIOR_ART_POOLS.md; comparative metrics TBD (owner) |
| Similar systems | See docs/decisions/PRIOR_ART_POOLS.md; comparative metrics TBD (owner) |

## Source decisions

- WWPP-R37.1 — Tamper detection MVP. Includes all four tamper measures and measurement of overhead, speed, performance, benchmarks, effectiveness, efficiency, and accuracy.
- WWPP-R37.1b — Tamper measures config. All four tamper measures are controlled and configured on the admin pages.
- WWPP-R37.2 — Thesis measurement. Light metrics are always on. The heavy benchmark harness is owner-started.
- WWPP-R26.2 — Audit hash chain default. The chain is OFF by default. It must be built and tested before MVP completion.
- WWPP-R26.3 / WWPP-R35.1 — Verification default. Default 1% random re-run on other workers with exact hash comparison; developer-led re-run % (0-100%) subject to admin ceiling (WWPP-R35.1 supersedes R26.3 default).
- WWPP-R27.2 — Thesis tests for chain and pools. All listed tests must pass before MVP completion.
- WWPP-R18.4 — Lease store. The PG lease store and Redis lease with PG settle are selectable. The default is not set yet.
- WWPP-R19.3 — Lease store default. The default is decided after the M1 benchmark.
- WWPP-R28.1 — Dev code in contributor browsers. Dev code runs in a sandbox origin.
- WWPP-R33.1 — Pool GPU. GPU is a worker capability in both pools. GPU results are verified by sampling.
- WWPP-R38.7 — Config version retention. Keep the last 10 versions per admin page, any version used by a running job, and admin-named pins (used to pin immutable configuration snapshots for thesis benchmark runs).
- WWPP-R35.1 — Result re-run checks.
- WWPP-R35.2 — Tamper evidence.
- WWPP-R36.2-RES — P2P default ON until tested.
- WWPP-R40.1 — Monitor page numbers.
- WWPP-SPIKE2-20261117 — Sandbox spike v2 baseline context.

<!-- mmx MiniMax-M3 in=1738 out=2497 -->
