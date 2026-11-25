<!-- Path: docs/MODULES.md | Author: Codex for Claude Opus 5.5 | 2026-11-17
What/why: WO-CORE-00 package ownership and integration contracts; WWPP-R18.2, R32, R37, R38, R40, R41.
WO-T2.2 (hermes MiniMax-M3, 2026-11-17): the agy pages are served from pools-core under /pools/,
one shared nav under /nav/, and the old /web/ placeholder routes redirect to /pools/monitor.html. -->
# Pools Core module contracts

2026-11-18 Codex web: applied DeepSeek + Copilot doc findings
2026-11-18 CLI-3 (agy): synced docs with rounds 5-7 fixes (FIX-P1/P2, FIX-Q1..Q3, FIX-R1..R3)
2026-11-19 DOC-S6 (agy): fixed 9 CONTEXT_PACK_R5.md drift items from WEBCHAT_R6_gemini_whole_20261118.md

WO-CORE-00 supplies a Go 1.26 debug skeleton in `F:/tmp/pools-core`, module `ccgsc/poolscore`.
Both the modernc SQLite adapter (`internal/store/sqlite`) and the pgx PostgreSQL `Store` + config-store adapters (`internal/store/pg`, F4/FIX-J3/F5) are implemented (`cmd/pools-core/main.go` opens `pg.Open(POOLS_PG_DSN)` when `POOLS_PG_DSN` is set, otherwise `sqlite.Open(c.Database)`). This is a contract handoff, not full MVP acceptance.
No deployment, Node migration, Git mutation, external messaging or global installation is included.

## Contents

1. [Package ownership](#package-ownership)
2. [Storage and lease invariants](#storage-and-lease-invariants)
3. [HTTP contracts](#http-contracts)
4. [Configuration and launch](#configuration-and-launch)
5. [Validation](#validation)
6. [Remaining integration work](#remaining-integration-work)

## Package ownership

No successor `WO_CORE*` files were found in the named API work folder. Names below are proposed
handoff slots, not issued work-order IDs or authorization to start those jobs. The coordinator
should bind each slot to its actual next WO before parallel work starts.

| Package/path | Public boundary | Implemented in CORE-00 | Next WO slot / owner | Merge constraint |
|---|---|---|---|---|
| `cmd/pools-core` | executable flags, lifecycle | HTTP + h2c gRPC, SIGINT/SIGTERM, `POOLS_PG_DSN` / SQLite store selection, maintenance and shutdown | Runtime/integration WO | Keep startup composition here; no runtime changes implied |
| `internal/config` | `Config`, `Default`, `Load`, `Validate` | JSON then env, explicit flags last | Admin-config WO | Versioned schema/config snapshots, no second owner doc |
| `internal/logx` | `New`, `DailyWriter` | slog JSON to stdout and UTC daily files | Observability WO | Security events always retained; never log credentials |
| `internal/store` | `Store.View`, `Store.Update`, `Reader`, `Tx`, bucket constants | transactional record contract for all requested domains | Storage WO | Callbacks atomic, no callback reentry |
| `internal/store/sqlite` | `Open` | modernc SQLite, embedded SQL migration, rollback/persistence | Storage WO | Preserve debug adapter; no cgo |
| `internal/store/pg` | `Open`, `DB` | pgx-backed `Store` adapter over `pool_records` (`jsonb` + `raw_body bytea` + unique accepted-result index, F4/FIX-J3) and versioned `pool_config_version` settings/policy store (T6.2/F5) | PostgreSQL WO | Preserve same `Store` contract and adversarial tests across SQLite and PostgreSQL |
| `internal/auth` | `CreateKey`, `Exchange`, `Validate`, `Revoke`, `Bootstrap` | hashed keys, HS256 900-second JWT, Store A/B roles, scopes, revocation | Auth/admin WO | Admin Store A issues keys; contributor never gets developer key |
| `internal/pool` | `Engine`, `CreateRequest`, `Job`, `Lease`, `Submission`, session methods | one engine, atomic fencing, retries, quarantine, dead letters, sessions (live heartbeat refresh + startup index rebuild, FIX-Q2), split/batch transport (per-task slot counting + reserved verification budget, FIX-P1/R1), scheduling/quotas and post-commit CPU/GPU metering (F2/FIX-Q2/R2), per-job hash chain and global Merkle checkpoints (F3/FIX-P2/Q3/R2), 3-layer job policy (F5), groupBy/pin placement and hoisted warm-initializer gate (F6/FIX-Q2), webhook outbox (F7/FIX-N1), upload scan/review state and per-owner artifact byte cap (F8/FIX-Q2), late-result settlement over expired-lease evidence (F9/T62) | Scheduling/session WO | No second job truth or separate thread/process engine |
| `internal/verify` | `Check`, `Signer`, `Sampler`, `CanaryInjector`, `Reputation` | SHA-256 (missing `ExpectedSHA` -> `VerdictUnverified`, FIX-Q1) and Ed25519 signed envelopes with persisted keyring rotation and overlap windows restored on restart (FIX-P2); HMAC-keyed re-run and canary selection (`ShouldRerunWithKey` with durable `rerun_sampling_secret`, FIX-Q3/R2), opaque canary task IDs and unpredictable fixture choice (FIX-P1), canary coverage in `Verified`/`TrustComplete` (FIX-P1/R1), and persisted reputation tracker (uncompared re-run timeout/exhaustion flags result and records failure instead of rewarding first worker, FIX-Q3) | Verification/persistence WO | Reputation and signing-key rotation with overlap are persisted and restored across restarts; admin-page wiring and detailed metrics remain successor work |
| `internal/httpx` | `Decode`, `Problem`, `Error` | bounded JSON, RFC 9457 error mapping | API/transports WO | Shared error codes across REST, gRPC and SSE |
| `internal/api` | `Server.Handler`, `RunMaintenance` | both route families, admin session login + cookie CSRF enforcement on all non-safe writes + security headers (`X-Content-Type-Options`, `X-Frame-Options`, `Content-Security-Policy`, FIX-C/Q1) + admin-only IP allowlist scope check (FIX-R3), F3 chain/Merkle routes, F5 admin config routes, F7 `results:bulk` (`ndjson`/`json`/`zstd-tar`) + durable webhook outbox, F8 upload-scan/review routes, results cursors, persisted SSE replay | API/transports WO | Maintain lowerCamelCase and draft compatibility; unsupported features explicit |
| `internal/grpcapi` | `Server`, `NewServer` | `ThreadPoolService` and `ProcessPoolService` gRPC transport mirroring the HTTP API and error model (T42 F10) | API/transports WO | Keep checked-in protobuf bindings aligned with `api/ccgsc_pools_v1.proto` |
| `internal/worker` | `Service` handlers | register, heartbeat/renew, lease pull/SSE, submit | Worker/sandbox WO | No developer code execution on server; build real browser isolation separately |
| `web` | embedded `Files`, `Nav`, `Pages`, `NavRoot()`, `PagesRoot()` | site shell placeholders, one shared nav, the agy page tree | UI WO | Nav role tags are metadata, never authorization |
| `scripts` | CMD launcher, PowerShell 7 menu | build-if-stale, start/stop/status/logs/diagnostics/test | Runtime/integration WO | Local only; verify PID belongs to this executable |
| `tests` | `go test ./...` | offline end-to-end and vital invariant tests | Every owning WO | Go package tests also reside under internal/ and cmd/; browser and workload tests live with their owning code |

## Storage and lease invariants

`Store` deliberately exposes a small transaction API instead of coupling callers to SQL.
The debug migration stores JSON records under `(bucket,id)` primary keys. Buckets cover artifacts,
jobs, tasks, leases, results, sessions, config versions, audit, metrics, users, keys, workers,
events, dead letters, webhook deliveries and idempotency. Domain structs live with their owning package.

Jobs currently aggregate tasks, active leases and accepted results in one record. Separate
`tasks`, `leases` and `results` buckets reserve names for normalization; they are not parallel
copies. SQLite uses one connection, serializing atomic update callbacks. `internal/store/pg`
implements the same `Store` contract on PostgreSQL (`pool_records` with `jsonb`, `raw_body bytea`
for exact byte preservation, and partial unique index `pool_records_results_job_task_uq` for single
accepted-result settlement). PG is the default lease mode; Redis is an optional lease-coordination backend, with durable PG settlement.

Lease generation increments durably on each claim, across engine recreation. A result must
match pool, registered tab owner, worker ID, current lease token, per-task generation, live
deadline (or unconsumed `ExpiredLease` evidence snapshot for late results before re-claim in F9,
which settle directly without re-run sampling per T62), the Ed25519 signed task envelope bound to
`jobId`, `taskId` (or opaque `canaryTaskId` on canary substitution, FIX-P1), module artifact digest,
input SHA-256, lease token and worker ID, the per-task single-use envelope digest, and a mandatory
output SHA-256 over the exact submitted bytes (T53). Legacy assignment signatures also sign the
final transmitted wire task (including canary substitutions, FIX-P1).
Exactly one settlement survives; duplicates and stale/terminal submissions return 409 and are
retained in `dead_letters`. Late results settle directly over unconsumed server `ExpiredLease`
evidence (`F9`/`T62`, bypassing re-run sampling) and emit `"late": true` (with `lateAcceptedAt`
on the settled `Result`, `internal/api/api.go:501`); offline dew receipts default enabled with a 24-hour window; explicit false disables. Full reconnect/replay evidence remains F12 work. Developer result
GET currently returns accepted/final-failed results; administrative dead-letter retrieval is
a successor API task. Results retain completion order; failed attempts become returned failure
records only on exhaustion. Three failures/expiries quarantine a poison task; a smaller
`maxAttempts` ends it earlier. Cancellation preserves prior results and revokes outstanding leases.

Canary injection and re-run sampling both use HMAC-keyed selection (`verify.ShouldRerunWithKey`
with a durable 32-byte `rerun_sampling_secret` stored in `store.Metrics`, FIX-Q3/FIX-R2); injected
canary task IDs are opaque random IDs (`auth.ID()`) indistinguishable from normal task IDs on the
wire, with unpredictable fixture selection (FIX-P1). When a canary passes, `CanaryState: "pass"`
and `CanaryEvidenceID` are persisted on the task while the developer task is requeued, and carried
into the settled developer result (`CanaryRan`, `CanaryState`, `CanaryOK`, `Verified`,
`TrustComplete`); `Verified` and `TrustComplete` require both `HashOK` and canary pass coverage
whenever `CanaryEnabled` is true, separating `HashOK` from complete trust coverage (FIX-P1/FIX-R1).
Missing `ExpectedSHA` in `Verifier.Verify` returns `VerdictUnverified` (`HashOK: false`, FIX-Q1).
When a held re-run expires its comparison window or exhausts attempts without a second worker
comparison, the first result is accepted with `Flagged: true` (`RerunOK: false`) and records a
failure reputation observation (`verify.EventFail`) rather than rewarding the uncompared worker (FIX-Q3).

Worker slot capacity enforces separate ordinary (`w.Slots`) and reserved-verification
(`reservedVerificationSlots = 1`) budgets with a hard aggregate bound of
`w.Slots + reservedVerificationSlots` across individual tasks and batches; batch claims count each
leased task individually against available slots (FIX-P1/FIX-R1). Lease renewals (`Heartbeat`)
match the active lease (including opaque canary task IDs, token, generation, worker ID, and exact
`ExpiresAt`, FIX-P1) and extend expiry by `min(TTL, remaining per-task TimeoutMS from StartedAt)`
across single tasks and live batch members (FIX-Q2). CPU and GPU runtime seconds
(`AddMeteredSeconds`) are collected on lease settlement and expiry during the store transaction and
applied to scheduler metering counters only after the transaction commits, so rollbacks cannot
inflate metered runtime (FIX-Q2/FIX-R2).

Process sessions reserve available tabs, use distinct host counts, reject post-close batches,
support draining and expire reservations. Tasks in a session can only use its tabs, and those
tabs cannot consume unrelated jobs. `hostCount` defaults to `tabCount` per the latest OpenAPI.
Worker `Heartbeat` refreshes both `store.Workers` and the durable `Session.Tabs` heartbeat snapshot
in `store.Sessions`, and `expireSessions` checks live worker heartbeats so active session tabs do
not falsely drain at `2*TTL` (FIX-Q2). On engine creation (`pool.New`), in-memory session and
session-job indexes are rebuilt from durable storage (`rebuildSessionIndex`, FIX-Q2).
F6 implements validated warm-initializer artifact state and groupBy/pin placement; the
warm-initializer F8 review gate is hoisted outside the module loop on session creation and
rejects `pending` or `rejected` artifacts with HTTP 409 (FIX-M2/FIX-Q2).

Keys and token ownership use the hashed key ID, not just a user name. Store A accepts only
admin/director roles; Store B accepts only developer/contributor roles. HTTP requires both role
and scope. Scopes include `keys:write`, `config:read`, `config:write`, `artifacts:write`,
`artifacts:read`, `jobs:write`, `jobs:read`, `worker`; `*` is the configured admin bootstrap scope.
Refresh re-exchanges `apiKey`; there is no refresh-token grant. Every stream revalidates the
key/token before more protected work. Store A cookie-authenticated admin sessions
(`ccgsc_admin_session`) require a matching `X-CSRF-Token` header on all non-safe
(non-`GET`/`HEAD`/`OPTIONS`) writes across every cookie-authenticated route (`/auth/keys`,
`/admin/reviews/*`, `PUT /admin/upload-scan`, `/admin/v1/config/*`, `/admin/config/*`, FIX-Q1).
Client IP resolution trusts `X-Forwarded-For` / `X-Real-IP` only when the direct peer matches
`TrustedProxyCIDRs` (FIX-M3/FIX-Q1); when `IPAllowlistEnabled` is true, allowlisted IPs satisfy
only administrative (`admin`/`director`) routes whose role and scope authorize the claim and never
bypass developer or contributor route authentication (FIX-R3).
Artifact uploads enforce both total storage (`MaxArtifactBytesTotal`, 512 MiB default) and
per-owner artifact byte quota (`MaxArtifactBytesPerOwner`, 128 MiB default) at upload creation
(`offset == 0`), returning 409 (`capacity-unavailable`) when exceeded, and bind artifact ownership
to the uploader (FIX-G/FIX-Q2). Developer reads require owner references; administrator/director and contributor access obey role/scope rules. DELETE removes only the caller reference. Keys and payloads must not be logged.

## HTTP contracts

Base paths: `/thread-pool/v1` (`tp`) and `/process-pool/v1` (`pp`). Literal pool IDs are canonical. Job IDs are `s1.<poolId>.<randomId>`.

| Method + relative path | Behavior / status |
|---|---|
| `POST /auth/token`, `/auth/refresh` | `{apiKey,scopes?}` -> 200 `{accessToken,tokenType,expiresIn,expiresAt,scopes,serverId,poolId}` |
| `POST /auth/keys` | Store A admin only (`X-CSRF-Token` required for cookie sessions, FIX-Q1): `{user:{id,accountStore,roles},scopes}` -> 201 key shown once |
| `PUT /artifacts` | Raw bytes, `X-Artifact-Sha256`, `Upload-Offset`; `Upload-Length` for chunking; enforces 32 MiB per-artifact ceiling, 128 MiB per-owner cap and 512 MiB total cap at `offset == 0` (FIX-Q2), and runs F8 upload scan on completion -> `200` partial chunk metadata with `Upload-Offset` header, `201 Created` on completion, or `422` `artifact-rejected` with scan report on upload-scan failure |
| `GET /artifacts/{artifactId}` | authenticated owner-scoped developer bytes, scoped admin/director or contributor access, digest checked |
| `DELETE /artifacts/{artifactId}` | artifacts:write; remove caller reference; blob removed only when no references remain |
| `POST /auth/keys/{keyId}:revoke` | keys:write; revoke key, 200 |
| `POST /jobs` | draft `moduleArtifactIds` + `tasks` or server `split`; 201 (OpenAPI draft takes precedence over prose 202 example) |
| `GET /jobs/{jobId}` | 200 job projection, no lease tokens or internal task records |
| `GET /jobs/{jobId}/chain` | 200 per-job hash-chain entries and verification report (F3) |
| `POST /jobs/{jobId}:cancel` | 202 terminal job projection on fresh cancellation, or 200 when already terminal; preserves accepted results |
| `GET /jobs/{jobId}/results` | 200 `items`, `order:as_completed`, opaque `nextCursor`, `hasMore`; `limit` and `includeFailed` |
| `GET /jobs/{jobId}/results:bulk` | paged export in `ndjson`, `json`, or `zstd-tar`, with cursor binding, ETag support and bulk-size limits |
| `GET /events` | SSE, durable numeric event IDs, `Last-Event-ID`, comma-separated `jobIds` / `sessionIds` / `types` filters |
| `POST /jobs:validate` | validation and explicit skeleton warnings, no write |
| `GET /capabilities` | live capability and trust-config values, including re-run/canary/reputation, groupBy/pin, warm initializer, `adminConfig` policy, `uploadChecks`, `tamperEvidence` and bulk-result formats; admin-UI and multi-tenant limitations remain explicit |
| `GET /merkle:checkpoints` | 200 global Merkle checkpoints and verification report; historical leaf tips verified as valid prefixes of current append-only job chains and full checkpoint ancestry walked back to seq 1 with contiguity checks (F3/FIX-P2/Q3/R2) |
| `GET /admin/v1/config/{page}`, `GET /admin/config/{page}` | Store A admin/director (`config:read`): active settings, version, and up to 100 history entries from `pool_config_version` (or 503 `config-store-unavailable` on SQLite); `?version=N` reads a retained version (F5) |
| `POST /admin/v1/config/{page}`, `POST /admin/config/{page}` | Store A admin/director (`config:write`, plus `X-CSRF-Token` for cookie sessions): save (`{settings,reason,expectedVersion?,pinName?}` -> 201), `{page}:rollback`, `{page}:pin`, or `{page}:unpin` (F5) |
| `POST /sessions` (process only) | reserve tabs, 201; no available capacity returns 409 |
| `GET /sessions/{sessionId}` (process only) | state/progress and expiry reconciliation |
| `POST /sessions/{sessionId}/tasks` (process only) | task batch -> 202 `SessionTaskBatch`; each batch has its own job ID |
| `POST /sessions/{sessionId}:close` (process only) | 202; `drain` defaults true; false cancels unfinished tasks |
| `POST /workers/register` | contributor `{hostId,slots:1..4,gpu?}` -> 201 tab identity |
| `POST /workers/{tabId}/leases` | 200 signed assignment or 204 when none available |
| `GET /workers/{tabId}/tasks` | authenticated SSE `task` events with signed assignments |
| `POST /workers/{tabId}/heartbeat` | `{lease?:Lease}` updates worker presence and durable `Session.Tabs` heartbeats (FIX-Q2), and renews matching live leases (including opaque canary task IDs, FIX-P1) bounded by `min(TTL, remaining per-task TimeoutMS from StartedAt)` (FIX-Q2) |
| `POST /workers/{tabId}/results` | `{lease,taskEnvelope,output,sha256,error}` with mandatory output SHA-256 over exact output bytes and a 256 KiB output cap -> 200 result (`hashOk`, `verified`, `trustComplete`, `canaryEnabled`, `canaryRan`, `canaryState`, `canaryOk`, `late` fields, FIX-P1/FIX-R1/F9) or 409 stale lease |

The contributor route spellings are CORE-00 additions: the developer OpenAPI drafts do not
define contributor transport. An assignment's legacy `envelope` signs the final transmitted
wire task, lease and module IDs (including canary substitutions, FIX-P1). `taskEnvelope` uses
an Ed25519 signing keyring persisted in `store.Metrics` (`task_signing_key`) with key rotation
and configurable overlap windows (`RotateTaskKey`), restored across engine restarts so retired
keys verify inside their overlap window and fail closed after expiry or revocation (FIX-P2).
SHA-256 of submitted JSON is over the raw `output` bytes.

Errors contain `type`, `title`, `status`, `detail`, `instance`, `code`, `traceId`, `retryable`.
Codes: `invalid-request`, `invalid-json`, `invalid-body`, `invalid-key`, `artifact-too-large`,
`artifact-pending`, `artifact-rejected`, `bulk-too-large`, `rate-limit-exceeded`, `quota-exceeded`,
`invalid-csrf`, `missing-csrf`, `password-change-failed`, `config-store-unavailable`,
`config-conflict`, `config-not-found`, `invalid-config`, `config-schema-unavailable`,
`unauthorized`, `forbidden`, `not-found`, `state-conflict`, `capacity-unavailable`,
`not-implemented`, `stream-unavailable`, `internal-error`. Request trace IDs are also returned in `X-Request-ID`.

### Page and asset routes (WO-T2.2)

The agy monitor and admin pages are embedded in the binary and served from the same
origin as the API. One shared navigation component and one link registry serve every
page, admin or manual. Store A cookie-session auth endpoints are also served under
`/pools/auth/` (`GET /pools/auth/csrf`, `GET /pools/auth/session`, `POST /pools/auth/login`,
`POST /pools/login`, `POST /pools/auth/logout`, `POST /pools/auth/change-password`).
All HTTP responses carry `X-Request-ID`, `X-Content-Type-Options: nosniff`,
`X-Frame-Options: DENY`, and `Content-Security-Policy: frame-ancestors 'none'` (FIX-Q1).

| Method + path | Behavior / status |
|---|---|
| `GET /` | 307 to `/pools/monitor.html` |
| `GET /pools/` | 307 to `/pools/monitor.html` |
| `GET /pools/{page}.html` | 200 `text/html`; the seven pages are `monitor`, `pool-monitor`, `api-monitor`, `thread-pool-admin`, `process-pool-admin`, `thread-api-admin`, `process-api-admin` (plus `login.html` / `contribute.html` / `runner.html`) |
| `GET /pools/manual/{page}.html` | 200 `text/html`; the full manual reference of each page |
| `GET /pools/css/style.css` | 200 `text/css`; the shared stylesheet of every page |
| `GET /pools/js/{schema,api,mock,app}.js` | 200 `application/javascript`; the page modules |
| `GET /nav/nav.js`, `GET /nav/nav.json`, `GET /nav/nav_demo.html` | the one nav component, its link registry and its demo |
| `GET /web/{admin,monitor}/` | 307 to `/pools/monitor.html`; the old placeholder routes redirect to the real pages |
| `GET /web/contribute/` | 200 `text/html`; the contributor placeholder is unchanged |
| `GET /web/nav.js`, `GET /web/nav.json` | the same nav files as `/nav/`, byte-identical; there is only one nav |

The pages read the live service first and fall back to `mock.js`: `js/api.js` calls
`GET /healthz`, `GET /{pool}/v1/capabilities` and the `GET /{pool}/v1/events` stream
when authenticated. `web/pages/js/api.js` stores bearer tokens in memory only (no
`sessionStorage` fallback), sends `X-CSRF-Token` on cookie-session non-GET requests (FIX-L),
and calls `markMock(...)` whenever `getMetrics()` merges mock fallback fields into partial
live data so the `MOCK DATA` badge is always shown when any displayed value came from `mock.js`
(FIX-Q1). The badge is absent only when all displayed values are live.

## Configuration and launch

Precedence: defaults -> `-config <JSON>` -> `POOLS_*` environment -> explicitly supplied flags.
Flags: `-addr`, `-database`, `-debug`. Environment: `POOLS_ADDR`, `POOLS_DATABASE`, `POOLS_DRIVER`,
`POOLS_PG_DSN` (when set, `cmd/pools-core/main.go` opens PostgreSQL via `pg.Open` instead of
SQLite), `POOLS_LOG_DIR`, `POOLS_JWT_SECRET`, `POOLS_BOOTSTRAP_KEY`, `POOLS_SERVER_ID`,
`POOLS_LEASE_SECONDS`, `POOLS_DEBUG`, `POOLS_IP_ALLOWLIST_ENABLED`, `POOLS_IP_ALLOWLIST`,
`POOLS_TRUSTED_PROXY_CIDRS`, `POOLS_LOGIN_RATE_LIMIT`, `POOLS_WEBHOOKS_ENABLED`,
`POOLS_WEBHOOK_ALLOW_PRIVATE_IPS`. `config.example.json` contains no API credential. Bootstrap requires an operator
supplied key of at least 24 characters; there is no default API key. The default JWT signing
secret is debug-only and is rejected when debug is disabled.

Default bind: `127.0.0.1:8091`. Database: `data/pools.db`. Logs:
`logs/pools-core-YYYYMMDD.jsonl` (UTC day rotation, retained) plus stdout.

```powershell
$env:GOMODCACHE='F:\GoCache\mod'
$env:GOCACHE='F:\GoCache\build'
$env:GOTOOLCHAIN='local'
go mod tidy
go build ./...
go test ./...
go vet ./...
go run ./cmd/pools-core -config config.example.json
```

Set the bootstrap key in the launching environment before first authenticated use; never
paste it into logs or committed files. Use `/auth/token` with it, then issue separate Store B
developer/contributor keys through `/auth/keys`.

Double-click `scripts/pools-core.cmd` to build if inputs changed, run hidden and open the monitor.
`pwsh -File scripts/pools-core.ps1` opens the menu. Console Ctrl+C performs graceful shutdown.
The menu's Stop uses Windows `Stop-Process` after checking executable identity; it is forceful,
not a claimed SIGINT shutdown. Browser launch is an intentional visible action only in the CMD
launcher. The scripts have not been used to start or stop a runtime during this implementation.

## Validation

Measurements: 2026-11-17, Windows workspace `F:/tmp/pools-core`, Go `go1.26.2 windows/amd64`.

| Check | Observed result |
|---|---|
| `gofmt -l` all Go sources | clean |
| `node --check web/nav.js` | passed |
| PowerShell AST parse of launcher | no parser errors |
| `graphify update .` | completed locally, no model; no import cycles; SQL parser missing |
| `go mod download` initial direct module metadata | exit 0, existing SQLite/pgx archives found |
| `go mod download all` | blocked by sandbox network socket denial to `proxy.golang.org` for transitive dependencies |
| Go build / test / vet acceptance | Historical CORE-00 attempt (superseded by dated R3 gates): required `F:\GoCache\build` writes denied; dependency completion also blocked |
| Git status/diff | unavailable: work folder has no `.git` |

`tests/core_test.go` includes the required three-task end-to-end flow for **both** pools, through
real handlers, real auth and file-backed SQLite, with no listener/network. It also covers chunk
retry/tamper rejection, role/scope enforcement, expiry/revocation, concurrent grant/settlement,
fencing across engine recreation, poison quarantine, session drain/expiry, SQLite rollback and
reopen, the original CORE-00 PostgreSQL stub (later implemented in F4/FIX-J3), static routes and JSON logging. These are authored tests,
not passing-test claims until execution is possible. No `DONE` acceptance is asserted.

### WO-T2.2 measurements (2026-11-17)

Measured on this machine with `GOMODCACHE=F:\GoCache\mod`, `GOCACHE=F:\GoCache\build`,
`GOTOOLCHAIN=local`, Go `go1.26.1 windows/amd64`, Node `v22.16.0`.

| Check | Observed result |
|---|---|
| `go build ./...` | exit 0 |
| `go vet ./...` | exit 0 |
| `go test ./...` (fresh cache) | `ok ccgsc/poolscore/tests 2.684 s`, `ok ccgsc/poolscore/internal/verify 3.123 s` |
| `gofmt -l .` | clean |
| `node --test web/pages/tests/web.test.mjs` | 17/17 pass |
| `node --test web/pages/tests/pages.test.mjs` | 17/17 pass |
| `node --test web/nav.test.mjs` | 22/22 pass |
| `node --test web/pages/nav/nav.test.mjs` | 22/22 pass (compatibility shim) |
| `node --check web/nav.js`, `web/pages/js/app.js` | passed |
| PowerShell AST parse of `scripts/pools-core.ps1` | no parser errors |
| `bash -n scripts/pools-core.sh` | passed |
| Live server route sweep on `127.0.0.1:8099` | `/` 307, `/pools/` 307, all 7 pages 200 `text/html`, all 7 manuals 200, `/pools/css/style.css` 200 `text/css`, `/pools/js/*.js` 200, `/nav/nav.js` 200, `/nav/nav.json` 200, `/web/admin/` 307, `/web/monitor/` 307, `/web/contribute/` 200 |
| Redirect targets | `/`, `/pools/`, `/web/admin/`, `/web/monitor/` all send `Location: /pools/monitor.html` |
| Live SSE with a real bearer token | `GET /thread-pool/v1/events` delivered real pools-core events (`mon.work.running` 1 then 0) and the page reported data source `live` |

Chromium is not installed on this machine and the work order forbids installs, so the pages
were exercised in a browser-equivalent VM module graph: the real served modules were fetched
over HTTP from the running server and evaluated as ES modules with a browser-like
`import.meta.url`, against a doubled DOM. That probe rendered 11 nav links, 34 metric cards
on `/pools/monitor.html`, 67 form fields on `/pools/thread-pool-admin.html`, the `MOCK DATA`
badge with its reason chain, and no page errors. A real browser click-through is still
unverified.

## Remaining integration work

`internal/store/pg/pg.go` implements the durable PostgreSQL `Store` adapter over
`pool_records` (F4/FIX-J3, 57/57 PG tests passing on PostgreSQL 18), alongside the
`pool_config_version` settings store (`internal/store/pg/config.go`, `admin_config_F5.go`)
exposed at `GET/POST /admin/v1/config/{page}` (and `/admin/config/{page}`, F5).

Trust and tamper measures in `internal/pool` and `internal/verify` persist reputation
(`reputation`), the Ed25519 signing key and overlap keyring (`task_signing_key`, FIX-P2), and
the 32-byte HMAC sampling secret (`rerun_sampling_secret`, FIX-Q3/FIX-R2) in `store.Metrics`;
canary injection uses opaque task IDs, unpredictable fixture selection, and carries pass
evidence into settled results so `Verified` and `TrustComplete` require canary coverage when
canaries are enabled (FIX-P1/FIX-R1); uncompared re-run timeouts/exhaustions flag the result and
record `verify.EventFail` (FIX-Q3). Tests are recorded in `docs/reports/T4_2_WIRE_VERIFY_hermes.md`
and `docs/reports/FIX_{P1,P2,Q1,Q2,Q3,R1,R2,R3}*.md`. Prometheus/admin-page metric reads and task-key rotation API are implemented; `GET /capabilities` reports
live values, so clients should use the response rather than infer support from this handoff.

All ten T42 features — F1 split/batch, F2 scheduling/quotas/metering, F3 per-job hash chain and
global Merkle checkpoints, F4 PostgreSQL store adapter, F5 3-layer job policy and admin config
routes, F6 groupBy/pin/warm initializer, F7 `results:bulk` and durable signed webhook outbox,
F8 WASM upload scan and review gate, F9 late-result settlement over expired-lease evidence, and
F10 gRPC transport — are implemented. F10's protobuf and gRPC Go bindings in `internal/grpcapi/`
(`ccgsc_pools_v1.pb.go` and `ccgsc_pools_v1_grpc.pb.go`) are generated from `api/ccgsc_pools_v1.proto`
with `protoc` v5.29.3, `protoc-gen-go` v1.36.11 and `protoc-gen-go-grpc` v1.6.2. Remaining work includes F10b app egress, F11 peer transport, F12 full dew evidence and unsupported design controls. The baseline implements browser isolation, GPU metrics validation, token deny-list and PostgreSQL metrics routes. Unsupported requests return explicit errors or capability/warning flags; no external
developer code is executed by this skeleton.

Sources: [work order](../WO_CORE_00_SKELETON.md),
`F:/tmp/wwpp/api/{thread_pool_v1.openapi.yaml,process_pool_v1.openapi.yaml,ccgsc_pools_v1.proto}`,
`F:/tmp/wwpp/GRILL_WWPP_R1.md` (WWPP-R17..R41), and vault
`CCGSC_TWO_POOLS_DESIGN.md` sections 3–16. Source documents were not edited.

### T42 F1 split and batch transport (2026-11-18)

`split` expands a complete stored artifact into 1..10000 nonempty tasks with stable
IDs `split-00001`, etc. Supply `artifactId` and `count`. `format: bytes` (default)
produces task input `{artifactId,format:"bytes",offset,length}` describing a half-open
byte slice; offset omitted means zero. Workers fetch the artifact and slice it locally.
`format: range` reads an artifact containing integer `{"start":0,"end":10}` and emits
`{artifactId,format:"range",json:{start,end}}` per task, with exclusive ends.
No developer code runs on the server. Without `chunksize`, slices are balanced and
any extra units go to the first slices. With `chunksize:4`, count must equal the
ceiling of length/4 (10 units becomes 4,4,2 with count 3). Conflicting count/chunk
settings, empty slices, invalid ranges and counts over 10000 are rejected.
The top-level `chunksize` map is supported only for the split artifact, as an alias
for `split.chunksize`; conflicting values are invalid. `rows`, `records`, inline-task
chunk maps and unrelated artifact entries still return `ErrUnsupported`.
Validation checks request shape; creation also checks source existence/completeness
and partition consistency. The original split request stays on the job; expanded
tasks are in the internal stored `job.tasks` (redacted from developer HTTP views) and participate in normal progress/results/idempotency.

`batchSize` defaults to 1 (omitted/zero), maximum 10000. For values above 1, both
lease pull and SSE return `assignment.batch`, containing **all** signed assignments,
including the first. Top-level fields mirror the first member; consume the array
once, without also executing that mirror. The final batch can be shorter. Single-task
requests retain their existing response shape. Each batch stays in one job/session and
shares a lease token, while each leased task in a batch is counted individually against
worker slots and capped by available free slots (`w.Slots - activeOrdinary`, or
`reservedVerificationSlots - activeVerification` for verification tasks) under a hard
aggregate bound of `w.Slots + reservedVerificationSlots` (FIX-P1/FIX-R1). Each member
keeps its own task ID, generation, deadline and signed envelopes; submit each member
separately through the existing results route. As individual batch members settle or expire,
their slots are freed. Heartbeat renewal matches the active lease task ID (including opaque
canary task IDs), token, generation, worker ID, and exact `ExpiresAt`, and renews all
still-live members in that batch with each member's deadline bounded by
`min(TTL, remaining per-task TimeoutMS from StartedAt)` (FIX-P1/FIX-Q2); settled or
expired siblings are not revived. Retries get fresh batch tokens and increment per-task
fences. GPU/session selection and rerun exclusions apply to every member. Existing workers
must consume `batch` before developers request batchSize > 1. Capabilities advertise
implemented split formats and maximum batch/task sizes.

### T42 F5 admin config routes and job policy (2026-11-18)

This section records the F5 admin HTTP integration.
The PostgreSQL `pool_config_version` store now backs `GET/POST
/admin/v1/config/{page}` (also `/admin/config/{page}`). GET returns active
`settings`, `version`, and up to 100 history entries; `?version=N` reads a retained
version. POST accepts `{settings, reason, expectedVersion?, pinName?}`. A stale
`expectedVersion` returns 409, and save plus optional pin is atomic. Rollback uses
`{rollbackVersion, reason, expectedVersion?}` on that route or `{page}:rollback`;
it creates a new version without deleting history. `{page}:pin` takes
`{version,name}` and `{page}:unpin` takes `{version}`. Existing schema validation,
retention methods, named pins and mandatory PostgreSQL security audit remain in
the existing config store. F5 does not add automatic pruning or expose pruning.

Routes require Store A admin/director authentication and `config:read` or
`config:write` scopes for bearer tokens. `X-CSRF-Token` is enforced on all
cookie-authenticated non-safe writes across the API (`/auth/keys`, `/admin/reviews/*`,
`PUT /admin/upload-scan`, `/admin/v1/config/*`, and `/admin/config/*`, FIX-Q1). Write
authors are resolved from the authenticated user ID/username against an active
PostgreSQL `admin_user`; a caller cannot supply an author ID. Existing generic Store A
accounts and PostgreSQL admin identities must be provisioned with matching usernames;
F5 never creates accounts or credentials. `web/pages/js/api.js` automatically attaches
`X-CSRF-Token` from the login response or `ccgsc_csrf` cookie on cookie-session admin
writes (FIX-L). These are verified HTTP contracts, not a claim of a completed browser
settings editor.

With a PostgreSQL engine the routes use its existing connection pool. SQLite
reports `adminConfig.available:false` and returns 503 for config routes; it does
not pretend to write PostgreSQL settings. Missing PostgreSQL config schema fails
closed. `adminConfig` in both pool capability routes reports backend availability,
supported policy fields, active config version and the current limits/defaults.
No server/database or schema migration was started by F5.

Provision `db/pool_config.sql`, then manually apply the additive
[`db/policy_F5.sql`](../db/policy_F5.sql) extension before saving policy controls.
It preserves existing registered schemas and adds an optional `tp.policy` or
`pp.policy` object to the respective pool page's new schema version. Other page
controls retain their existing storage semantics; F5 does not activate unbuilt
P2P, dew, group/pin, audit-chain or verification-mode features.

Example section inside a complete, schema-valid thread-pool settings document:

```json
{
  "tp.policy": {
    "adminLimits": {
      "policy": {"allowOverride": true, "allowed": ["partial", "fail-fast"]},
      "maxAttempts": {"allowOverride": true, "min": 1, "max": 5}
    },
    "poolDefault": {"policy": "partial", "maxAttempts": 3}
  }
}
```

Supported controls are `policy`, `needs`, `priority`, `maxAttempts`, `batchSize`,
`threshold`, and `rerunPercent`. A configured field requires both an admin rule
and a pool default. String rules enumerate allowed values; numeric rules require
min/max within the engine's supported range. Omitted job fields inherit the pool
default. Explicit overrides require `allowOverride:true` and must satisfy the
rule; violations are rejected, never clamped. Explicit numeric zero is preserved
and distinguished from omission in idempotency. Unsupported job fields remain
`ErrUnsupported`. A rerun policy that the active verifier would clamp is rejected.
Unconfigured fields retain existing engine validation/defaults.

Each new job resolves against the latest page version inside its job transaction,
records `chosenPolicy.values`, `chosenPolicy.chosenBy` (pool-default or developer
identity), and a `configVersion` of `page:version`. The exact PostgreSQL version
is attached under the same page lock/transaction, preventing prune races and
orphan references on rollback. Saved jobs and idempotent replays retain their
selection after later saves/rollbacks. References are conservatively retained
for the lifetime of stored jobs, including terminal jobs. When no config version exists, new jobs
use the explicit `builtin-v1` fallback. Source-level Go callers can distinguish
numeric zero from omission by decoding the same JSON request contract.

F5 validation details and environment exceptions are in
`F:/tmp/par/t42_F5/REPORT.md`; progress and check logs are alongside it.

### T42 F8 module upload scan and admin review (2026-11-18)

Every completed upload is scanned before it may serve as a job module
(WWPP-R36.1). The scan reads the bytes only; no developer code runs on the
server. Four checks are implemented, each with its own finding code:

| Finding | Severity | Meaning |
|---|---|---|
| `size-exceeded` | error | the artifact is larger than the configured ceiling |
| `wasm-malformed` | error | the WASM binary could not be parsed |
| `import-not-allowed` | error | an import is outside the allow-list |
| `shared-memory` | error | shared `WebAssembly.Memory` (forbidden by R10.0) |
| `wasm-magic-missing` | warning (error when `requireWasmMagic`) | the artifact declares `application/wasm` but has no magic bytes |
| `empty-module` | warning | the artifact has no bytes |

An error finding refuses the upload with HTTP 422 and the RFC 9457 problem
code `artifact-rejected` (with `scan` attached); the artifact and its scan report stay stored, so the
admin review queue keeps the evidence. A warning never refuses an upload.

The import allow-list accepts an exact `module.field` entry, a `module.*`
prefix rule, or `*` for everything. The default is `wasi_snapshot_preview1.*`,
`wasi_unstable.*` and `env.*`. Shared memory is refused by default and can only
be allowed by an explicit admin switch.

The admin review gate has the states `not-required`, `pending`, `approved` and
`rejected`. Approvals are counted per distinct reviewer; the artifact is
approved when the quorum is reached. The quorum is the R12.1 floor (1-of-2 for
MVP 100, 2-of-2 for MVP 200) and is an admin setting. Owner, admin and
director code is auto-approved (R13.1). A module in state `pending` or
`rejected` is refused as a job module, as a session module and as a warm
initializer, with HTTP 409. Every review decision is written to the security
log (R25.1).

New routes, on both pool prefixes:

| Method + relative path | Behavior / status |
|---|---|
| `GET /admin/reviews?state=&limit=` | the artifacts of one review state, without module bytes |
| `GET /admin/reviews/{artifactId}` | the scan report and the review state of one artifact |
| `POST /admin/reviews/{artifactId}` | `{decision:"approve"\|"reject",reason?}` -> 200 the new review state |
| `POST /admin/reviews/{artifactId}:rescan` | run the scan again -> 200 the new report and state |
| `GET /admin/upload-scan` | the active scan configuration |
| `PUT /admin/upload-scan` | store a new scan configuration; every field is optional |

The review routes require Store A admin/director with `artifacts:read` or
`artifacts:write`; the configuration write requires `artifacts:write`. The
configuration is durable (the `metrics` bucket) and is loaded at engine start.

`GET /capabilities` reports `uploadChecks` with the active switches
(`formatSizeHash`, `importPermissionScan`, `reviewGate`, `malwareAbuseScan`), so the capability view stays truthful.
`PUT /artifacts` answers with the scan report and the review state of the
artifact; the OpenAPI `Artifact` schema of the draft does not carry those
fields yet, so the F8 contract is additive over the draft.

F8 validation details are in `F:/tmp/par/t42_F8/REPORT.md`.

## Current runtime contract (R3E)

[R3E runtime contract](R3E_RUNTIME_CONTRACT.md) reconciles all 38 R3 documentation findings, including exact routes, current defaults, error/SSE frames, environment readers, worker DTOs and transport-specific gaps. Historical build observations are not current gate status. R3D 58e509b6 passed all nine local validation stages on 2026-11-24 with zero PG/Redis skips.
