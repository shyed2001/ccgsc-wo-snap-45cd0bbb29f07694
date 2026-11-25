# DOC-S6 Report - Fix 9 CONTEXT_PACK_R5.md drift items

## What changed
- `pools-core/docs/handoff/CONTEXT_PACK_R5.md`: resolved all 9 drift items from `docs/reviews/WEBCHAT_R6_gemini_whole_20261118.md` (`### docs/handoff/CONTEXT_PACK_R5.md vs Code`, lines 58-66) in Section 4.
- `pools-core/docs/MODULES.md`: applied the exact matching updates so `docs/MODULES.md` and Section 4 of `docs/handoff/CONTEXT_PACK_R5.md` remain byte-for-byte identical.

No Go, JS, SQL, YAML, or proto files were modified (`Docs only`).

## Why (9 drift items resolved)
1. **Drift 1 (`CONTEXT_PACK_R5.md:176,201` — SQLite vs PostgreSQL `Store`)**: Added the `2026-11-19 DOC-S6` header entry, documented that `cmd/pools-core/main.go` selects `pg.Open(POOLS_PG_DSN)` when `POOLS_PG_DSN` is set or `sqlite.Open(c.Database)` otherwise (both in the overview and `cmd/pools-core` table row alongside `internal/store/pg`), and clarified in `Validation` that the `CORE-00` PostgreSQL stub reference applies to the original 2026-11-17 snapshot (later implemented in F4/FIX-J3).
2. **Drift 2 (`CONTEXT_PACK_R5.md:260` — `PUT /artifacts` statuses/headers + F8 scan alignment)**: Updated the `PUT /artifacts` row to specify `200` partial chunk metadata with `Upload-Offset` header, `201 Created` on completion, or `422` `artifact-rejected` with scan report on upload-scan failure (`internal/api/api.go:367-380`). Also aligned `### T42 F8` prose (`HTTP 422` `artifact-rejected` with `scan` attached instead of `HTTP 400 upload-scan-failed`, and `GET /capabilities` `uploadChecks` instead of `uploadScan`).
3. **Drift 3 (`CONTEXT_PACK_R5.md:264` — `POST /jobs/{jobId}:cancel` status codes)**: Updated to `202 terminal job projection on fresh cancellation, or 200 when already terminal` (`internal/api/api.go:474-477`).
4. **Drift 4 (`CONTEXT_PACK_R5.md:230` — Late settlement vs dew receipt verification)**: Clarified in `## Storage and lease invariants` that late results settle directly over unconsumed server `ExpiredLease` evidence (`F9`/`T62`, bypassing re-run sampling) and emit `"late": true` (with `lateAcceptedAt` on `Result`, `internal/api/api.go:501`), whereas standalone offline dew exception receipt verification remains disabled (`dew.enabled: false`).
5. **Drift 5 (`CONTEXT_PACK_R5.md:267` — `GET /events` `sessionIds` filter)**: Added `sessionIds` to `comma-separated jobIds / sessionIds / types filters` (`internal/api/api.go:1051-1052`).
6. **Drift 6 (`CONTEXT_PACK_R5.md:286-288` — Shipped RFC 9457 error codes)**: Enumerated all 26 shipped error codes explicitly (replacing `config-*` wildcard and adding `invalid-body`, `password-change-failed`, `artifact-pending`, `artifact-rejected`, `bulk-too-large`, `rate-limit-exceeded`, `quota-exceeded`, `invalid-csrf`, `missing-csrf`, `config-store-unavailable`, `config-conflict`, `config-not-found`, `invalid-config`, `config-schema-unavailable`).
7. **Drift 7 (`CONTEXT_PACK_R5.md:318-320` — 7 omitted `POOLS_*` env vars)**: Added `POOLS_PG_DSN`, `POOLS_IP_ALLOWLIST_ENABLED`, `POOLS_IP_ALLOWLIST`, `POOLS_TRUSTED_PROXY_CIDRS`, `POOLS_LOGIN_RATE_LIMIT`, `POOLS_WEBHOOKS_ENABLED`, and `POOLS_WEBHOOK_ALLOW_PRIVATE_IPS` (`cmd/pools-core/main.go:65`, `internal/config/config.go:87-132`).
8. **Drift 8 (`CONTEXT_PACK_R5.md:402` — HTTP routes exposing `internal/store/pg/config.go`)**: Added `GET /admin/v1/config/{page}`, `GET /admin/config/{page}`, `POST /admin/v1/config/{page}`, and `POST /admin/config/{page}` (with `:rollback`, `:pin`, `:unpin`) directly to the `## HTTP contracts` table alongside the updated `## Remaining integration work` and `### T42 F5` sections (`internal/api/admin_config_F5.go:30-35`, `internal/api/api.go:172`).
9. **Drift 9 (`CONTEXT_PACK_R5.md:411-413` — F10 protobuf generation)**: Replaced the stale `"protoc was unavailable; regeneration remains outstanding"` note with `protoc v5.29.3`, `protoc-gen-go v1.36.11`, and `protoc-gen-go-grpc v1.6.2` matching `internal/grpcapi/ccgsc_pools_v1.pb.go:35-36` and `ccgsc_pools_v1_grpc.pb.go:35-36`.

## Test and gate output
Executed in `F:/tmp/cli/agy_b2/pools-core` with `PATH="E:\Program Files\Strawberry\c\bin;" + $env:PATH`, `CGO_ENABLED=1`, `GOMODCACHE=F:\GoCache\mod`, `GOCACHE=F:\GoCache\build`, `GOTOOLCHAIN=local`:

- `go build ./...`: PASS (exit 0)
- `go vet ./...`: PASS (exit 0)
- `go test -count=1 -race ./...`: PASS (exit 0)
  ```
  ?   	ccgsc/poolscore/cmd/pools-core	[no test files]
  ok  	ccgsc/poolscore/internal/api	1.839s
  ok  	ccgsc/poolscore/internal/auth	8.486s
  ?   	ccgsc/poolscore/internal/config	[no test files]
  ok  	ccgsc/poolscore/internal/grpcapi	2.850s
  ?   	ccgsc/poolscore/internal/httpx	[no test files]
  ?   	ccgsc/poolscore/internal/logx	[no test files]
  ok  	ccgsc/poolscore/internal/pool	17.081s
  ?   	ccgsc/poolscore/internal/store	[no test files]
  ok  	ccgsc/poolscore/internal/store/pg	2.201s
  ?   	ccgsc/poolscore/internal/store/sqlite	[no test files]
  ok  	ccgsc/poolscore/internal/verify	1.332s
  ?   	ccgsc/poolscore/internal/worker	[no test files]
  ok  	ccgsc/poolscore/tests	13.958s
  ?   	ccgsc/poolscore/web	[no test files]
  ```
- `gofmt -l .`: exit 0, listed 88 untouched `.go` files solely due to pre-existing CRLF line endings (verified `0` non-CRLF `gofmt` formatting diffs across all 88 `.go` files; no `.go` files modified by DOC-S6).
- LF & parity verification: `docs/MODULES.md` (40688 bytes, `crlf: 0`, 510 LF lines), `docs/handoff/CONTEXT_PACK_R5.md` (80364 bytes, `crlf: 0`, 714 LF lines), `PROGRESS.log` (70 bytes, `crlf: 0`), `REPORT.md` (`crlf: 0`), and `CONTEXT_PACK_R5.md` Section 4 == `docs/MODULES.md` (`True`).

## AAAK
- AAAK.STATUS: DOC-S6 completed (all 9 drift items resolved; `go build`, `go vet`, and `go test -count=1 -race ./...` all pass; LF and Section 4 parity verified).
- AAAK.TIER0: `gitautofleet inspect --path F:/tmp/cli/agy_b2/pools-core brief` (334 text files, 0 protected files); direct reads of `WEBCHAT_R6_gemini_whole_20261118.md`, `CONTEXT_PACK_R5.md`, `MODULES.md`, `cmd/pools-core/main.go`, `internal/api/{api.go,admin_auth.go,admin_config_F5.go}`, `internal/config/config.go`, and `internal/grpcapi/{ccgsc_pools_v1.pb.go,ccgsc_pools_v1_grpc.pb.go}`; `graphify update` skipped (docs-only change, no code modified).
- AAAK.OWNER_FILE: `pools-core/docs/handoff/CONTEXT_PACK_R5.md`, `pools-core/docs/MODULES.md`.
- AAAK.FILES_EDITED: `pools-core/docs/MODULES.md`, `pools-core/docs/handoff/CONTEXT_PACK_R5.md`, `PROGRESS.log`, `REPORT.md`.
- AAAK.NO_NEW_FILE: No new files created inside `pools-core` (`PROGRESS.log` and `REPORT.md` created in row folder `F:/tmp/cli/agy_b2` per `WO_CLI_B2.md`).
- AAAK.NO_LOSS: All existing context-pack sections, decisions, history tables, and commit list preserved; CRLF->LF conversion performed after reading full files into memory first.
- AAAK.VERIFIED: Read updated files, verified Section 4 parity (`sec4 == MODULES.md: True`) and LF line endings, and ran `go build ./...`, `go vet ./...`, `go test -count=1 -race ./...`, and `gofmt` verification after final doc edits.
- AAAK.NEXT: Head AI merges doc updates from `F:/tmp/cli/agy_b2/pools-core`.

DONE DOC-S6
