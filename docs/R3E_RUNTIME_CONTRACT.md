# Pools-core runtime contract — R3E

Source baseline: 58e509b6, 2026-11-24; documentation reconciliation after R3A–R3D. This reference supersedes stale implementation-status statements in the design manuals. It is not a claim that every design control is implemented. No deployment or runtime default is changed here. Source paths below are relative to pools-core.

## 1. Admin HTTP surface, authentication and versioning (gaps 1–3)

Store A admin/director authorization is enforced by the secure middleware and each handler's additional checks. Bearer credentials need the indicated scope. Session-authenticated writes require X-CSRF-Token. Read-only access is not a grant to mutate settings. Use a read before an update and send the returned version where supported.

| Method and route | Scope | Contract |
|---|---|---|
| GET /admin/v1/config/{page} (also /admin/config/{page}) | config:read | Current configuration; optional version query; response includes page, version, settings and version history. |
| POST same route | config:write | settings, reason, optional expectedVersion and pinName; save creates an immutable version, 201. |
| POST same route with :rollback | config:write | rollbackVersion, reason, optional expectedVersion; creates a new version, 201. |
| POST with :pin or :unpin | config:write | version and, for pin, name; 200. |
| GET /admin/v1/redis/status | config:read | Redis configuration/status projection. |
| GET or PUT /admin/v1/pools/{pool}/recompute | config:read or config:write | Per-pool settings; PUT requires admin; fields in section 23. |
| GET or PUT /admin/v1/pools/{pool}/settings | config:read or config:write | GET view: poolId, version, verifyMode, redundantRate, sampleRate, isolationMode, modes, isolationModes, contributorControls, contributorControlModes. PUT optional verifyMode, redundantRate, sampleRate, isolationMode, contributorControls, expectedVersion; 200. |
| GET {pool-prefix}/admin/reviews | artifacts:read | state and limit (1–1000, default 100); items/state/count, 200. |
| GET {pool-prefix}/admin/reviews/{artifactId} | artifacts:read | Artifact scan/review view, 200. |
| POST same review path | artifacts:write | decision approve/reject and reason; 200. |
| POST review path with :rescan | artifacts:write | Rescan stored bytes; 200. |
| GET or PUT {pool-prefix}/admin/upload-scan | artifacts:read or artifacts:write | Scan config; optional fields enabled,maxBytes,requireWasmMagic,allowedImports,allowSharedMemory,requireReview,reviewQuorum,maxWasmBytes,maxJsBytes,maxJsonBytes,maxZipEntries,maxZipUncompressed,maxWasmPages; GET includes reviewStates; 200. |
| GET /admin/v1/workers, /admin/v1/legacy-workers | metrics:read | Worker inventory and legacy bridge. |
| GET or PUT /admin/v1/caps | metrics:read or config:write | GET reports effective caps, runtime counters, version, capsSource and soft-limit explanation. PUT admin only; at least one of maxProcs,memLimitMB,workerPool,dbMaxOpenConns; nonnegative, zero=auto; optional expectedVersion, stale 409; 200. |
| GET or PUT /admin/v1/thread-apps/{appId}, /admin/v1/process-apps/{appId} | config:read or config:write | GET owner query; PUT owner plus maxWorkers (Thread) or maxTabs (Process), integer 1–64; 200 policy. Process release: POST /process-pool/v1/workers/{tabId}/apps:release, contributor worker scope, 200 {released:true}. |
| POST /admin/v1/task-keys:rotate | config:write | Section 14. |
| POST /admin/v1/tokens:revoke, /admin/v1/subjects:revoke; GET /admin/v1/tokens/revoked | keys:write | Section 15. |
| GET /admin/v1/metrics or /admin/metrics | metrics:read | Section 4. |

Pool-scoped aliases for settings and recompute are {pool-prefix}/admin/settings and {pool-prefix}/admin/recompute. The canonical pool prefix is /thread-pool/v1 or /process-pool/v1. A stale expectedVersion yields 409 and no update; zero is the initial-write CAS value before a stored version exists. Omitting the field is not CAS protection. Invalid input yields 400, missing resources 404, unauthenticated 401, forbidden 403; unavailable config schema/store may yield 503. Do not blindly retry a conflict; reread and reconcile.

Admin/monitor HTML pages use a validated ccgsc_admin_session cookie, an admin/director bearer, or configured IP allow-list bypass. An authenticated incorrect role is forbidden; an unauthenticated page request redirects to /pools/login.html. Public contributor pages and assets remain public. Login/logout, session, CSRF and password-change endpoints are under /pools/auth/; login alias /pools/login. IP bypass of the page does not itself create an API authorization grant. Trusted proxies and IP allow-list configuration matter; owner RDP access is external to the application.

Sources: internal/api/{api,admin_auth,admin_config_F5,admin_recompute_R17,pool_settings_W10C,upload_scan_F8,admin_caps_R15,thread_app_F10,process_app_F9}.go; internal/store/pg/config.go; db/pool_config.sql.

## 4. Metrics

GET /admin/v1/metrics (alias /admin/metrics) requires metrics:read. Filters include scope and job/jobId and since (RFC3339Nano, duration, epoch seconds or milliseconds). The JSON response identifies scope, jobId and since and contains samples, metrics aggregates (name, unit, source, count, sum, avg, min, max, last, lastTs) and counters. PostgreSQL reads the metrics table via QueryMetricSamples; db/pool_metrics_MVPW.sql defines it. GET /metrics emits Prometheus text format, authenticated unless the explicit direct-loopback anonymous option is enabled (not forwarded requests). Unavailable observations must remain identified as unavailable or MOCK DATA, not fabricated live measurements. See internal/api/metrics_MVPW.go.

## 5–8. Runtime versus policy defaults

- P2P: capabilities.p2p.enabled is false in the reviewed baseline. Manual ON is a design target, not an available peer transfer path. Server hash-addressed artifacts remain the supported path. F11 owns actual peer transfer and fallback evidence.
- Dew: internal/pool/dew_MVPW.go defaults enabled when the option is omitted, with a 86400-second receipt window; explicit false disables. The signed receipt does not waive task envelope, worker, generation or settlement validation. F12 owns complete reconnect/late/replay evidence. No default change in R3E.
- Upload scanning: format/size/hash and configured import review are implemented. Default scan enabled; manual-review gate defaults off. Default bounds: WASM 16 MiB, JS 4 MiB, JSON 1 MiB, ZIP 256 entries and 64 MiB expanded, WASM 4096 pages, no shared memory. Malware/abuse scanner is not implemented; capabilities.malwareAbuseScan=false. Do not treat the design checkbox as an antivirus guarantee.
- Quotas: running jobs, queued tasks and requests/minute are implemented policy fields; zero limits mean unlimited in DefaultQuotaConfig. High priority default true; RetryAfter default 1 minute. Task-byte/time quota fields and maxTaskDurationMs are zero in capabilities, not an enforced per-key quota. Distinguish transport/artifact limits (32 MiB artifact ceiling, 262144-byte inline limit) and existing lease/timeout fences from a future per-key quota. Read actual capabilities.

Sources: internal/api/api.go; internal/pool/{dew_MVPW,upload_scan_F8,scheduling}.go.

## 9. Redis

POOLS_LEASE_STORE=pg is the default, redis is an optional coordination backend. POOLS_PG_DSN selects PostgreSQL durable storage; Redis does not replace durable result/generation/settlement fencing. The Redis key is `<prefix>lease:<jobId>:<taskId>`, default prefix pools:. SET NX PX acquires a token; GET, PEXPIRE and DEL support coordination. TTL derives from the lease duration/deadline (default POOLS_LEASE_SECONDS=60, validated 1–86400). Set POOLS_REDIS_ADDR to host:port or redis://host:port, no userinfo; POOLS_REDIS_USER and POOLS_REDIS_PASSWORD are ACL credentials, POOLS_LEASE_REDIS_DB (default 0) and POOLS_LEASE_REDIS_PREFIX select namespace. Redis admin credentials POOLS_REDIS_ADMIN_USER/PASSWORD are separate. Configure the versioned PG config store for Redis administration. Do not log credentials or infer that a Redis key alone authorizes a result. Sources: internal/store/redislease/client.go; internal/pool/batch_F1.go; internal/config/config.go; cmd/pools-core/main.go.

## 10. gRPC service list

Package ccgsc.pools.v1. Both ThreadPool and ProcessPool expose ExchangeToken, RefreshToken, CreateKey, UploadArtifact, GetArtifact, DeleteArtifact, CreateJob, GetJob, GetJobChain, GetMerkleCheckpoints, CancelJob, GetResults, GetResultsBulk, ValidateJob, GetCapabilities, StreamEvents. ProcessPool additionally exposes OpenSession, GetSession, StreamSessionTasks, CloseSession. Total 36 RPC declarations. Admin/worker HTTP routes are not implied RPCs. See api/ccgsc_pools_v1.proto and section 35.

## 11–12. Artifacts

GET {pool-prefix}/artifacts/{artifactId} requires bearer authorization, returns application/octet-stream and X-Artifact-Sha256. Developer with artifacts:read must have an owner reference; admin/director with scope can access other owners' artifacts; contributors use worker scope and the implemented contributor access rules. An artifact hash is not an authorization grant. DELETE same path requires developer artifacts:write, removes only caller's reference, returns artifactId, sha256, removed, blobDeleted, remainingReferences. Blob deletion occurs only when no owner references remain. Missing/forbidden references are not a cross-owner delete. PUT artifacts accepts raw bytes. Sources: internal/api/api.go; internal/pool artifact ownership implementation.

## 13. API-key revocation

POST {pool-prefix}/auth/keys/{keyId}:revoke; admin/director keys:write; no request fields needed. 200 {keyId,revoked:true}; malformed action/key is rejected and absent key is not found. API-key revocation is distinct from a task-signing key rotation and a JWT deny-list entry.

## 14. Task-signing key rotation

POST /admin/v1/task-keys:rotate, Store A admin with config:write, body {overlapMinutes: nonnegative integer}. Response 200 includes status, keyId, publicKeyHex, runnerAction. Rotation is audited and prior signing key remains within configured overlap; update pinned runner OPERATOR_KEYS and rebuild under the approved deployment process. A failure response after rotation/audit is not proof the key is unchanged. Inspect status before retrying; do not disclose private keys.

## 15. Token deny-list

POST /admin/v1/tokens:revoke {tokenId,reason,expiresAt?}; POST /admin/v1/subjects:revoke {subject,tokenId?,reason,expiresAt?} for anonymous subjects; GET /admin/v1/tokens/revoked?includeExpired=true. keys:write; 201 for revocations and 200 for list. ExpiresAt is RFC3339 with bounded validity; omitted uses token lifetime bound. Match token ID exactly; anonymous subject revocation also blocks renewal; maintenance prunes expired entries. Sources: internal/api/{denylist_MVPW,api}.go; internal/auth.

## 16. Anonymous contributors

POST {pool-prefix}/workers/anonymous. Optional existing bearer permits renewal of same subject under single-use renewal rules; 60-second grace applies only to eligible renewal, not normal bearer authorization. Both fresh issue and renewal are rate-limited; fresh issue also has live-tab caps. Defaults: enabled, 10 issues/min/IP, 8 live tabs/IP, 500 live tabs total. No body required; 200 returns accessToken,tokenType,expiresIn,expiresAt,scopes,serverId,poolId; no-store; disabled 403, cap/rate 429, renewal-store failure 503. Use POOLS_ANONYMOUS_CONTRIBUTORS, POOLS_ANONYMOUS_ISSUE_RATE_PER_MINUTE, POOLS_ANONYMOUS_MAX_LIVE_TABS_PER_IP, POOLS_ANONYMOUS_MAX_TOTAL_TABS. See internal/api/anonymous_T96.go and internal/config/anonymous_T96.go.

## 17. Runner origin and demo endpoint

POOLS_RUN_ORIGIN selects a separate runner host; only GET/HEAD /runner.html is served on it. Baseline CSP has connect-src 'none', default-src 'none', script/worker restrictions and frame-ancestors bound to public origin (or self); no-store, CORP cross-origin, no cookies. Public GET /pools/runner-config returns anonymousJoin, wasmMaxMemoryPages (4096 default), runOrigin (nullable). GET /pools/mock-artifact returns id,version,sha256,inputJSON,source for a pinned, demo-capable workload, verifies embedded digest, no-store; 503 if unavailable. This demo endpoint is not an arbitrary developer artifact download. F10b adds a live-lease runner grant for approved per-app CSP. Ungranted runner requests retain connect-src none. See F10b_TOKEN_VERIFY_GUIDE.md for admin routes, consent, token and workload ABI. Sources: internal/api/api.go and workload_seed_W10.go, web/pages/js/runner.js.

## 18. Errors

REST problem response contains type,title,status,detail,instance,code,traceId,retryable,message,hint,requestId. task-failed is an embedded result error with status 500 and retryable false, not necessarily a failed HTTP results request. service-unavailable is 503/retryable true; capacity-unavailable is a retryable 409; config-schema-unavailable is 503/nonretryable. gRPC maps to status and ErrorDetail; consult its actual transport mapping rather than treating HTTP status as gRPC code. Sources: internal/httpx/httpx.go, internal/grpcapi/errors.go and convert.go.

## 19. SSE

Developer GET {pool-prefix}/events requires developer jobs:read and filters owner/pool before optional jobIds,sessionIds,types (comma-separated). Initial Last-Event-ID is the resume cursor. Server subscribes before sorted durable replay; deduplicates exact IDs, including out-of-order live commits above initial cursor. Frames: id: <padded event id>, event: <type>, data: <Event JSON>; blank line terminates. Queue is 128 batches; overflow closes with service-unavailable. Auth is revalidated on delivery and every 250ms. About 30 seconds before bearer expiration, event: reauth with data {"reason":"token_expiring","action":"refresh_and_reconnect"} ends the stream. Error frame event: error has code,message,hint,requestId,retryable; then close. Refresh and reconnect using last processed ID; handle replay idempotently. Worker SSE GET /workers/{tabId}/tasks sends event: task, id: jobId.taskId.generation, data: Assignment, checks auth before each claim (~1s); reconnect does not reset a lease fence. Sources: internal/api/api.go; internal/worker/worker.go.

## 20. Bulk results

GET {pool-prefix}/jobs/{jobId}/results:bulk; developer jobs:read; format ndjson (default), json, zstd-tar; limit 1–10000 (default 1000); cursor, state, verified=true/false, includeFailed (default true). Cursor is opaque base64url JSON offset bound to SHA-256 of the raw format/state/verified/includeFailed query values; invalid or mismatched cursor 400. Estimated pages are trimmed to 8 MiB; actual multi-result body above 8 MiB returns 413 bulk-too-large, single-result exemption. The baseline trims items after bulkPage computes next cursor: do not assume that size-truncated pages have gap-free cursor coverage; use smaller limit pending a separate correction. ETag is body SHA-256, If-None-Match can return 304; response X-Has-More, X-Next-Cursor when more, Content-Disposition; zstd adds Content-Encoding and X-Artifact-Sha256. Per-run chain mode also returns X-Chain-Head, X-Chain-Entries, X-Audit-Gap. Source: internal/api/api.go bulk/bulkPage.

## 21–22. Cancel, close and projections

POST {pool-prefix}/jobs/{jobId}:cancel: empty body or empty reason and graceMs=0, other values unsupported. New cancellation 202; already terminal 200. Process session close: drain defaults true, false cancels unfinished work; nonzero graceMs unsupported; reason decoded but not acted upon; 202. Job startedAt=null and resultCursor="" and result durationMs=0 are placeholder values in the baseline projection, not real timing measurements; use actual metrics for observed durations.

## 23. Recompute settings

GET/PUT /admin/v1/pools/{pool}/recompute (pool aliases in section 1). Defaults: off, busyPolicy=skip, malformedPolicy=reject. Mode off/registry, busyPolicy skip/defer, malformedPolicy reject/fraud. PUT optional mode,busyPolicy,malformedPolicy,expectedVersion, only provided fields change; initial version CAS 0, stale 409, negative invalid. Versioned settings and audit commit together; cache/publish follow commit. POOLS_MULTI_SERVER_SYNC=true requires PostgreSQL. Optional LISTEN/NOTIFY invalidation with refresh, bounded operations and reconnection backoff; durable PG state remains authoritative. R3A and R3B regression gates cover CAS and notification lifecycle. Sources: internal/api/admin_recompute_R17.go; internal/pool/recompute_mode_R17.go and recompute_notify_N11.go; cmd/pools-core/main.go.

## 24–30. Verification, trace, tests, environment, controls and health

- Developer owns application correctness. Required auth/lease identity, generation/token/deadline, signed envelope and exact-byte digest integrity are distinct from optional server-side canary/rerun/redundancy/reputation and registry recompute policy. Default verifyMode is sample, not off; the administrator can choose off/sample/redundant/both. Read current pool settings and capabilities; server checks do not establish arbitrary program correctness. Recompute is off by default.
- W3C traceparent propagation into the audit chain is not implemented; design-only. X-Request-ID/requestId/traceId is implemented.
- Tests live in Go packages including internal/pool, internal/recompute, internal/store/pg and internal/grpcapi; browser page/workload tests and CLI smoke are separate. Run go test -p 1 -count=1 ./... and go test -race -p 1 -count=1 ./internal/... with real test PG/Redis services; missing DB/Redis test prerequisites invalidate the production gate. Test fixture private schema must never modify public. See PG_FIXTURE_ISOLATION_R3D.md.
- Environment names are listed in the generated appendix below. Store selection/coordination credentials are distinct. Use secret injection locally, not checked-in secrets.
- pm.ctl.pause_scheduling, am.ctl.pause_ingress and pm.ctl.clear_quarantine remain design controls without verified runtime handlers; do not promise these operations.
- Reproducible baseline evidence: R3D 58e509b6, 2026-11-24, all 9 gate stages green, PG/Redis skips 0, pages 607/607, workloads 29/29, smoke 13/13. This is historical observed baseline evidence, not R3E or deployment proof.
- /healthz returns mode:"skeleton" as a historical service label, not an assertion that the service is disabled, nor proof every dependency is healthy.

## 31–33. JS ABI, encoding and worker payloads

JS workload kind js-module is an ES module exporting run(input, ctx); execution may be asynchronous. Return {output:{n,bytes},meta?:{gpu}}; output.bytes must be convertible to bytes, and n is retained only if finite. The verified module is loaded from Blob in a locked worker realm; untrusted code has no platform credential access. Kernel sends result or error to wrapper; trusted runner validates/serializes output and computes hash. No direct network API in the reviewed locked runner. WASM ABI is separate (ccgsc_alloc, ccgsc_run, ccgsc_out); follow checked-in runner ABI checks. See web/pages/js/runner.js.
REST JSON bytes fields use standard base64 where the schema is bytes/Go []byte; protobuf binary wire is binary and protobuf JSON bytes are base64. json.RawMessage values such as output remain JSON, not automatically base64. Hash the exact output JSON bytes; raw artifact upload is binary.

Worker API, contributor worker scope, pool prefix as above:
- Register POST /workers/register body e.g. {"hostId":"example-host","slots":2,"gpu":false,"browserTabId":"tab-example"}; optional gpuSlots,gpus[{vendor,architecture}],runnerPin. 201 returns worker record with contributorControls.
- POST /workers/{tabId}/heartbeat e.g. {"runnerPin":"example"} plus optional lease; 200 worker record.
- POST /workers/{tabId}/leases without body: 200 Assignment or 204 empty; GET /workers/{tabId}/tasks SSE as section 19.
- POST /workers/{tabId}/results uses Submission: lease,taskEnvelope,output?,sha256?,error?,canaryId?,canaryAnswer?,gpu?,leaseReceipt?,offline?,peerRelayed?. Send actual assigned envelope and lease, not fabricated values; 200 Result, stale/mismatched fence rejected.
- Worker fields: tabId,hostId,owner,poolId,slots,gpu,heartbeat; optional appId,appOwner,appExpiresAt,gpuSlots,gpus,deviceClass,batteryPercent,charging,memoryBytes,sessionId,anonymous,clientIp,browserTabId,runnerStale,runnerPin.
- Lease fields: jobId,taskId,workerId,token,generation,attempt,startedAt,expiresAt; optional workerSlotId,sessionId,gpu. Assignment: lease,task,moduleArtifactIds,envelope,taskEnvelope plus inputJSON?,leaseReceipt?,batch?; batch includes first item.
- Signed TaskEnvelope payload: version,jobId,taskId,artifactSha256,inputSha256,issuedAt,expiresAt,keyId; optional workload,leaseToken,workerId,isolationMode. Wrapper fields: payload (JSON), signature (base64), keyId, algorithm. Verify the actual signature wrapper in internal/verify/envelope.go; never trust an unsigned copy.
- Result/Assignment/Worker field-by-field concrete Go definitions are in internal/pool/model.go; generated extract below is the baseline schema, including optional fields.

## 34. Comparator

toleranceCompare and comparatorArtifactId are declared in request schemas but unsupported when set; chain.go returns ErrUnsupported and gRPC capability toleranceCompareAvailable=false. There is no implemented comparator ABI to document. Do not send a comparator until a future implementation advertises it.

## 35–36. Transport and identifiers

One HTTP listener multiplexes REST and native gRPC using h2c with HTTP/2 and application/grpc content type; reflection is not registered. Use explicit checked-in proto and TLS at the approved external ingress; loopback h2c example: `grpcurl -plaintext -import-path api -proto ccgsc_pools_v1.proto -H "authorization: Bearer <token>" <host:port> ccgsc.pools.v1.ThreadPool/GetCapabilities`. Bearer metadata and x-request-id follow internal/grpcapi. Canonical pool IDs tp and pp; routes thread-pool and process-pool. Seven config page IDs: monitor, pool-monitor, api-monitor, thread-pool-admin, process-pool-admin, thread-api-admin, process-api-admin. Source: db/pool_config.sql CHECK constraint.

## 37. Events and integrity reports

Event JSON: id,type,jobId,at,owner,poolId plus taskId?,sessionId?,result?. Known durable event types from engine and session handlers include job.queued/job.done, task.leased/task.done/task.failed/task.tampered/task.canary/task.rerun/task.rerunMismatch/task.pendingVerification/task.redundantMismatch/task.recomputeMalformedRejected/task.recomputeMismatch/task.recomputeDeferred, worker.lost, session.opened/session.draining/session.renewed and terminal session states (derived from session.go below). The developer stream is owner/pool scoped, not a global monitor feed. Monitor routes use their authorized metrics/worker projections.
Merkle report: checkpoints [{seq,at,root,prevRoot?,prevHash?,hash,jobCount,leaves:[{jobId,tip}]}], verifications [{seq,valid,root,recomputedRoot,jobCount,reason?}], latestRoot?. ChainReport: jobId,mode,head,entries,runEntries?,auditGap?,verification {jobId,valid,mode,entries,head,reason?,brokenAt?}. See internal/pool/chain.go, F4 per-run contract and proto definitions. Preserve actual JSON casing.

## 38. Capability parity

REST capabilities includes adminConfig and bulk-format metadata not represented in the baseline gRPC Capabilities message. Treat the respective HTTP JSON and protobuf as transport-specific contracts; do not claim complete parity. Use live values where present; missing field is not affirmative support.


## Appendix A — environment names read by source

Names only; never print actual credentials. Direct environment reads in startup/config/API/storage source and the two config map loops:

| Name | Reader |
|---|---|
| POOLS_ADDR | internal/config/config.go |
| POOLS_ANONYMOUS_CONTRIBUTORS | internal/config/anonymous_T96.go |
| POOLS_ANONYMOUS_ISSUE_RATE_PER_MINUTE | internal/config/anonymous_T96.go |
| POOLS_ANONYMOUS_MAX_LIVE_TABS_PER_IP | internal/config/anonymous_T96.go |
| POOLS_ANONYMOUS_MAX_TOTAL_TABS | internal/config/anonymous_T96.go |
| POOLS_BOOTSTRAP_KEY | internal/config/config.go |
| POOLS_DATABASE | internal/config/config.go |
| POOLS_DB_MAX_OPEN_CONNS | internal/config/config.go |
| POOLS_DEBUG | internal/config/config.go |
| POOLS_DRIVER | internal/config/config.go |
| POOLS_GPU_ENABLED | internal/config/config.go |
| POOLS_GPU_ON_BATTERY_ALLOWED | internal/config/config.go |
| POOLS_IP_ALLOWLIST | internal/config/config.go |
| POOLS_IP_ALLOWLIST_ENABLED | internal/config/config.go |
| POOLS_JWT_SECRET | internal/config/config.go |
| POOLS_LEASE_REDIS_DB | internal/config/config.go |
| POOLS_LEASE_REDIS_PREFIX | internal/config/config.go |
| POOLS_LEASE_SECONDS | internal/config/config.go |
| POOLS_LEASE_STORE | internal/config/config.go |
| POOLS_LEGACY_POOL_URL | internal/config/config.go |
| POOLS_LOGIN_RATE_LIMIT | internal/config/config.go |
| POOLS_LOG_DIR | internal/config/config.go |
| POOLS_MAX_GPU_SLOTS_PER_TAB | internal/config/config.go |
| POOLS_MAX_PROCS | internal/config/config.go |
| POOLS_MEM_LIMIT_MB | internal/config/config.go |
| POOLS_METRICS_LOOPBACK_ANONYMOUS | internal/config/config.go |
| POOLS_MULTI_SERVER_SYNC | cmd/pools-core/main.go |
| POOLS_PG_DSN | cmd/pools-core/main.go |
| POOLS_PUBLIC_ORIGIN | internal/config/config.go |
| POOLS_REDIS_ADDR | cmd/pools-core/main.go, internal/redisadmin/runner.go |
| POOLS_REDIS_ADMIN_USER | internal/redisadmin/runner.go |
| POOLS_REDIS_ADMIN_PASSWORD | internal/redisadmin/runner.go |
| POOLS_REDIS_PASSWORD | cmd/pools-core/main.go |
| POOLS_REDIS_USER | cmd/pools-core/main.go |
| POOLS_RUN_ORIGIN | internal/config/config.go |
| POOLS_SERVER_ID | internal/config/config.go |
| POOLS_TRUSTED_PROXY_CIDRS | internal/config/config.go |
| POOLS_WEBHOOKS_ENABLED | internal/config/config.go |
| POOLS_WEBHOOK_ALLOW_PRIVATE_IPS | internal/config/config.go |
| POOLS_WORKER_POOL | internal/config/config.go |

## Appendix B — concrete baseline DTO declarations

Extracted from source; Go JSON tags define the member names and omission rules. Related nested types remain defined in their source files.

### Worker

internal/pool/model.go

```go
type Worker struct {
	AppID          string    `json:"appId,omitempty"`
	AppOwner       string    `json:"appOwner,omitempty"`
	AppExpiresAt   time.Time `json:"appExpiresAt,omitempty"` // F9: tab affinity lease
	TabID          string    `json:"tabId"`
	HostID         string    `json:"hostId"`
	Owner          string    `json:"owner"`
	PoolID         string    `json:"poolId"`
	Slots          int       `json:"slots"`
	GPU            bool      `json:"gpu"`
	GPUSlots       int       `json:"gpuSlots,omitempty"`
	GPUs           []GPUInfo `json:"gpus,omitempty"`
	DeviceClass    string    `json:"deviceClass,omitempty"`
	BatteryPercent int       `json:"batteryPercent,omitempty"`
	Charging       bool      `json:"charging,omitempty"`
	MemoryBytes    int64     `json:"memoryBytes,omitempty"`
	Heartbeat      time.Time `json:"heartbeat"`
	SessionID      string    `json:"sessionId,omitempty"`
	Anonymous      bool      `json:"anonymous,omitempty"`
	ClientIP       string    `json:"clientIp,omitempty"`
	// BrowserTabID is the identity of the browser tab that registered (R10:
	// sessionStorage, so two tabs of one browser differ). It keys the reload
	// reuse: without it two tabs of one owner share localStorage's HostID and
	// the second register overwrote the first tab's record.
	BrowserTabID string `json:"browserTabId,omitempty"`
	// RunnerStale marks a tab whose runner cannot verify the live task key
	// (T114). It gets no lease until the page reloads and registers again.
	RunnerStale bool `json:"runnerStale,omitempty"`
	// RunnerPin is the pin the tab last reported: the sha256 of its runner.html
	// and the id of the task key that page trusts, as "<sha256>:<keyId>"
	// (W9 F1). The key half is what the refund rule verifies; the sha half is
	// kept as evidence of the page the tab runs.
	RunnerPin string `json:"runnerPin,omitempty"`
}
```

### Lease

internal/pool/model.go

```go
type Lease struct {
	WorkerSlotID string    `json:"workerSlotId,omitempty"` // F10: one individual Web Worker within the contributor tab
	JobID        string    `json:"jobId"`
	TaskID       string    `json:"taskId"`
	WorkerID     string    `json:"workerId"`
	SessionID    string    `json:"sessionId,omitempty"`
	Token        string    `json:"token"`
	Generation   uint64    `json:"generation"`
	Attempt      int       `json:"attempt"`
	StartedAt    time.Time `json:"startedAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	GPU          bool      `json:"gpu,omitempty"`
}
```

### Assignment

internal/pool/model.go

```go
type Assignment struct {
	InputJSON    string               `json:"inputJSON,omitempty"`
	LeaseReceipt *verify.LeaseReceipt `json:"leaseReceipt,omitempty"`
	// Batch contains all members (including the first) when batchSize > 1.
	// Top-level fields mirror the first member; submit each member separately.
	Batch             []Assignment    `json:"batch,omitempty"`
	Lease             Lease           `json:"lease"`
	Task              TaskSpec        `json:"task"`
	ModuleArtifactIDs []string        `json:"moduleArtifactIds"`
	Envelope          verify.Envelope `json:"envelope"`
	// TaskEnvelope is the signed task envelope of WWPP-R37.1: Ed25519 over the
	// canonical JSON of {jobId,taskId,artifactSha256,inputSha256,issuedAt,expiresAt,
	// keyId} with a domain separator, verifiable against the key ring.
	TaskEnvelope verify.SignedTaskEnvelope `json:"taskEnvelope"`
}
```

### Submission

internal/pool/model.go

```go
type Submission struct {
	GPU          *GPUResultMetrics         `json:"gpu,omitempty"`
	LeaseReceipt *verify.LeaseReceipt      `json:"leaseReceipt,omitempty"`
	Offline      bool                      `json:"offline,omitempty"`
	PeerRelayed  bool                      `json:"peerRelayed,omitempty"`
	Lease        Lease                     `json:"lease"`
	TaskEnvelope verify.SignedTaskEnvelope `json:"taskEnvelope,omitempty"`
	Output       json.RawMessage           `json:"output,omitempty"`
	SHA256       string                    `json:"sha256,omitempty"`
	Error        string                    `json:"error,omitempty"`
	// CanaryID names the canary the output answers, when the task carried one
	// (WWPP-R37.1). The server checks it against its own known answer.
	CanaryID string `json:"canaryId,omitempty"`
	// CanaryAnswer is the answer of the canary task. The expected answer stays
	// server side; the comparison is a hash compare only.
	CanaryAnswer json.RawMessage `json:"canaryAnswer,omitempty"`
}
```

### Result

internal/pool/model.go

```go
type Result struct {
	GPU      *GPUResultMetrics `json:"gpu,omitempty"`
	JobID    string            `json:"jobId"`
	TaskID   string            `json:"taskId"`
	Status   string            `json:"status"`
	Attempts int               `json:"attempts"`
	Output   json.RawMessage   `json:"output,omitempty"`
	SHA256   string            `json:"sha256,omitempty"`
	At       time.Time         `json:"at"`
	Error    string            `json:"error,omitempty"`
	// Trust evidence of the result (WWPP-R37.1).
	WorkerID       string     `json:"workerId,omitempty"`
	Verified       bool       `json:"verified,omitempty"`
	HashOK         bool       `json:"hashOk,omitempty"`
	TrustComplete  bool       `json:"trustComplete,omitempty"`
	CanaryID       string     `json:"canaryId,omitempty"`
	CanaryOK       bool       `json:"canaryOk,omitempty"`
	CanaryState    string     `json:"canaryState,omitempty"`
	CanaryEnabled  bool       `json:"canaryEnabled,omitempty"`
	CanaryRan      bool       `json:"canaryRan,omitempty"`
	Rerun          bool       `json:"rerun,omitempty"`
	RerunOK        bool       `json:"rerunOk,omitempty"`
	Flagged        bool       `json:"flagged,omitempty"`
	FlagReason     string     `json:"flagReason,omitempty"`
	Late           bool       `json:"late,omitempty"`
	LateAcceptedAt *time.Time `json:"lateAcceptedAt,omitempty"`
	// Redundant marks a result settled by the 2-of-N quorum and counts the
	// results that were compared (W10C row 1).
	Redundant         bool `json:"redundant,omitempty"`
	RedundantReplicas int  `json:"redundantReplicas,omitempty"`
	RecomputeVerified bool `json:"recomputeVerified,omitempty"`
	// RecomputeUnverified marks a recompute that ran and disagreed with the
	// worker on an unpinned reference size: the mismatch is never fraud
	// (G-R17-3, WO R18).
	RecomputeUnverified bool `json:"recomputeUnverified,omitempty"`
	// RecomputeDeferred marks a result that was held for the deferred recompute
	// (G-R17-1, WO R18): the submission, the background verdict or the skip
	// fallback.
	RecomputeDeferred bool `json:"recomputeDeferred,omitempty"`
}
```

### Event

internal/pool/model.go

```go
type Event struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	JobID     string    `json:"jobId"`
	TaskID    string    `json:"taskId,omitempty"`
	At        time.Time `json:"at"`
	Owner     string    `json:"owner"`
	PoolID    string    `json:"poolId"`
	SessionID string    `json:"sessionId,omitempty"`
	Result    *Result   `json:"result,omitempty"`
}
```

### TaskEnvelope

internal/verify/envelope.go

```go
type TaskEnvelope struct {
	Version        int                 `json:"version"`
	JobID          string              `json:"jobId"`
	TaskID         string              `json:"taskId"`
	ArtifactSHA256 string              `json:"artifactSha256"`
	InputSHA256    string              `json:"inputSha256"`
	Workload       *WorkloadDescriptor `json:"workload,omitempty"`
	LeaseToken     string              `json:"leaseToken,omitempty"`
	WorkerID       string              `json:"workerId,omitempty"`
	IssuedAt       int64               `json:"issuedAt"` // unix seconds
	ExpiresAt      int64               `json:"expiresAt"`
	KeyID          string              `json:"keyId"`
	// IsolationMode is the Director selected hostile-workload isolation of the
	// pool (D-W10-6: separate+lock or lock-only), covered by the same signature.
	IsolationMode string `json:"isolationMode,omitempty"`
}
```

### SignedTaskEnvelope

internal/verify/envelope.go

```go
type SignedTaskEnvelope struct {
	Payload   json.RawMessage `json:"payload"`
	Signature []byte          `json:"signature"`
	KeyID     string          `json:"keyId"`
	Algorithm string          `json:"algorithm"`
}
```

### GPUResultMetrics

internal/pool/gpu_MVPW.go

```go
type GPUResultMetrics struct {
	AdapterDetected *bool    `json:"adapterDetected,omitempty"`
	CompileMS       *float64 `json:"compileMs,omitempty"`
	DispatchMS      *float64 `json:"dispatchMs,omitempty"`
}
```
