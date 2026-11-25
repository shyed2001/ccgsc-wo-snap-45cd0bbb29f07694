# CLI-3 Report - Docs sync after rounds 5-7

## What changed
- `pools-core/docs/MODULES.md`: synchronized package ownership, storage and lease invariants, HTTP contracts, page/asset/auth routes, remaining integration work, T42 F1 split/batch slot and heartbeat rules, and T42 F5 CSRF/client integration notes against the codebase after rounds 5-7.
- `pools-core/docs/handoff/CONTEXT_PACK_R5.md`: updated `FIX-Q3` summary and added `FIX-R1`, `FIX-R2`, and `FIX-R3` under Section 2b, and synchronized Section 4 (`## 4. Module + route map (docs/MODULES.md)`) verbatim with `pools-core/docs/MODULES.md`.

No Go, JS, SQL, YAML, or proto files were modified (`Docs only`).

## Why
CLI-3 (`F:/tmp/cli/agy_b1/pools-core/docs/work_orders/WO_CLI_B1.md`) required bringing `docs/MODULES.md` and `docs/handoff/CONTEXT_PACK_R5.md` into sync with the implemented codebase after `FIX-P1`/`P2`, `FIX-Q1`..`Q3`, and `FIX-R1`..`R3`:
- **FIX-Q1 / FIX-L / FIX-R3 (Auth, CSRF, security headers, verifier & mock badge)**:
  - Cookie-authenticated non-safe writes enforce `X-CSRF-Token` across `/auth/keys`, `/admin/reviews/*`, `PUT /admin/upload-scan`, `/admin/v1/config/*`, and `/admin/config/*`; `web/pages/js/api.js` keeps bearer tokens in memory only and attaches `X-CSRF-Token` automatically.
  - All HTTP responses set `X-Request-ID`, `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, and `Content-Security-Policy: frame-ancestors 'none'`.
  - Forwarding headers (`X-Forwarded-For`, `X-Real-IP`) are trusted only from `TrustedProxyCIDRs`; when `IPAllowlistEnabled` is true, allowlisted IPs satisfy only admin/director routes matching role and scope and never bypass developer or contributor authentication.
  - `Verifier.Verify` returns `VerdictUnverified` (`HashOK: false`) when `ExpectedSHA` is empty.
  - `getMetrics()` in `web/pages/js/api.js` calls `markMock(...)` whenever partial live metrics merge mock fallback rows so the `MOCK DATA` badge stays accurate.
- **FIX-P1 / FIX-Q3 / FIX-R1 / FIX-R2 (Canary, re-run sampling, batch slots, metering)**:
  - Canary injection and re-run sampling both use HMAC-keyed `verify.ShouldRerunWithKey` with the persisted 32-byte `rerun_sampling_secret` (`store.Metrics`).
  - Injected canary task IDs are opaque random IDs (`auth.ID()`) with unpredictable fixture choice; legacy assignment `envelope` signatures sign the final transmitted wire task including canary substitutions.
  - Canary PASS evidence (`CanaryState: "pass"`, `CanaryEvidenceID`) persists on the task and carries into settled developer results (`CanaryRan`, `CanaryState`, `CanaryOK`, `Verified`, `TrustComplete`); `Verified` and `TrustComplete` require canary coverage whenever `CanaryEnabled` is true.
  - Uncompared re-run timeout or attempt exhaustion settles the first result with `Flagged: true` (`RerunOK: false`) and records `verify.EventFail` rather than rewarding the first worker.
  - Worker capacity enforces separate ordinary (`w.Slots`) and reserved-verification (`reservedVerificationSlots = 1`) budgets with an aggregate bound of `w.Slots + reservedVerificationSlots`; batch claims count each leased task individually and free slots as members settle or expire.
  - CPU/GPU runtime seconds (`AddMeteredSeconds`) are collected inside store transactions on settlement/expiry and applied only after commit so rollbacks cannot inflate metered runtime.
- **FIX-P2 / FIX-Q3 / FIX-R2 (Merkle checkpoints & signing keyring)**:
  - Task-signing Ed25519 keys and keyring overlap windows persist in `store.Metrics` (`task_signing_key`) and restore across engine restarts.
  - `VerifyMerkleCheckpoint` verifies historical leaf tips as valid prefixes of current append-only job chains and walks full checkpoint ancestry back to sequence 1 with contiguity checks.
- **FIX-Q2 (Sessions, heartbeats, per-owner artifact cap)**:
  - Worker `Heartbeat` updates `store.Workers` and refreshes durable `Session.Tabs` heartbeats in `store.Sessions`; `expireSessions` checks live worker heartbeats.
  - Lease renewal matches the active lease (including opaque canary task IDs) and bounds deadlines by `min(TTL, remaining per-task TimeoutMS from StartedAt)` across single tasks and live batch members.
  - `pool.New` rebuilds session and session-job indexes (`rebuildSessionIndex`) from durable storage.
  - F6 warm-initializer F8 review gating is hoisted outside the module loop on session creation.
  - `UploadOwned` enforces `MaxArtifactBytesTotal` (512 MiB default) and `MaxArtifactBytesPerOwner` (128 MiB default) at `offset == 0`.
- **F4 / F10 / remaining integration**:
  - Updated `internal/store/pg` and `internal/grpcapi` rows and `## Remaining integration work` to reflect implemented PG `Store` (`pool_records`), F1-F10 completion status, and `GET /capabilities` live reporting.

## Test and gate output
Executed in `F:/tmp/cli/agy_b1/pools-core` with `GOMODCACHE=F:\GoCache\mod`, `GOCACHE=F:\GoCache\build`, `GOTOOLCHAIN=local`:

- `go build ./...`: PASS (exit 0)
- `go vet ./...`: PASS (exit 0)
- `go test -count=1 ./...`: PASS (exit 0)
  ```
  ?   	ccgsc/poolscore/cmd/pools-core	[no test files]
  ok  	ccgsc/poolscore/internal/api	0.859s
  ok  	ccgsc/poolscore/internal/auth	1.685s
  ?   	ccgsc/poolscore/internal/config	[no test files]
  ok  	ccgsc/poolscore/internal/grpcapi	1.703s
  ?   	ccgsc/poolscore/internal/httpx	[no test files]
  ?   	ccgsc/poolscore/internal/logx	[no test files]
  ok  	ccgsc/poolscore/internal/pool	9.523s
  ?   	ccgsc/poolscore/internal/store	[no test files]
  ok  	ccgsc/poolscore/internal/store/pg	0.156s
  ?   	ccgsc/poolscore/internal/store/sqlite	[no test files]
  ok  	ccgsc/poolscore/internal/verify	0.448s
  ?   	ccgsc/poolscore/internal/worker	[no test files]
  ok  	ccgsc/poolscore/tests	6.229s
  ?   	ccgsc/poolscore/web	[no test files]
  ```
- `go test -count=1 -race ./...`: blocked on this host environment (`CGO_ENABLED=0` default -> `go: -race requires cgo`; `CGO_ENABLED=1` with default PATH -> `gcc` not found; `CGO_ENABLED=1` with `E:\codeblocks-20.03mingw-nosetup\MinGW\bin\gcc.exe` 8.1.0 -> `exit status 0xc0000139` on Go 1.26 race runtime, matching `docs/reports/T4_2_WIRE_VERIFY_hermes.md`; `clang.exe` 22.1.3 -> `unsupported option '-mthreads' for target 'x86_64-pc-windows-msvc'`).
- `gofmt -l .`: listed 87 pre-existing untouched `.go` files due to CRLF (`\r\n`) line endings in the workspace copy (`gofmt -d cmd/pools-core/main.go` confirms line-ending diff only; tracked under CLI-5 / T43; no `.go` files modified by CLI-3).
- LF & parity verification: `docs/MODULES.md` (`crlf: 0`, 500 lines), `docs/handoff/CONTEXT_PACK_R5.md` (`crlf: 0`, 704 lines), `PROGRESS.log` (`crlf: 0`), `REPORT.md` (`crlf: 0`), and `CONTEXT_PACK_R5.md` Section 4 == `docs/MODULES.md` (`True`).

## AAAK
- AAAK.STATUS: CLI-3 completed (docs synced; build, vet, and full non-race Go tests pass; `-race` blocked by host C/race runtime environment; no code files touched).
- AAAK.TIER0: `gitautofleet inspect --path F:/tmp/cli/agy_b1/pools-core brief` (319 files, 0 protected), `rg` absent so Python file/text inspection used; `graphify update` skipped (docs-only change, no code modified); `mempalace` skipped (not needed / owner permission required).
- AAAK.OWNER_FILE: `pools-core/docs/MODULES.md`, `pools-core/docs/handoff/CONTEXT_PACK_R5.md`.
- AAAK.FILES_EDITED: `pools-core/docs/MODULES.md`, `pools-core/docs/handoff/CONTEXT_PACK_R5.md`, `PROGRESS.log`, `REPORT.md`.
- AAAK.NO_NEW_FILE: No new files created inside `pools-core` (`PROGRESS.log` and `REPORT.md` created in row folder `F:/tmp/cli/agy_b1` per `WO_CLI_B1.md`).
- AAAK.NO_LOSS: Preserved all existing sections, history tables, decisions, and commit log; additive sync of rounds 5-7 behavior.
- AAAK.VERIFIED: Read edited files and checked Go sources (`internal/api`, `internal/pool`, `internal/verify`, `internal/store`, `internal/worker`, `web/pages/js/api.js`) directly from disk; verified LF endings and Section 4 parity; ran `go build ./...`, `go vet ./...`, and `go test -count=1 ./...` after final edits.
- AAAK.NEXT: Head AI merges doc changes and runs `-race` on a Linux/working-cgo host if required alongside CLI-5 line-ending handling.

DONE CLI-3
