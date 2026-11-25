# T42 F5 — admin config routes and job policy

Implemented in `F:/tmp/par/t42_F5/pools-core`, 2026-11-18, Windows/amd64,
Go 1.26.2. Scope: F5 only. No previous F5 progress/report was present.

AAAK.STATUS: Implemented and locally verified; owner acceptance remains pending.
Live PostgreSQL behavior and schema deployment are not claimed verified.

## Behavior

- Admin GET/POST config routes at `/admin/v1/config/{page}` and `/admin/config/{page}` expose existing PostgreSQL versions, schema-validated saves, history, rollback, named pin/unpin, and expected-version conflicts. Save plus optional pin is atomic; rollback creates a new audited version.
- Writes resolve the authenticated Store A user to an active PG `admin_user`, never trust a body-supplied author ID, and require the session CSRF token for cookie authentication. Bearer scopes are `config:read` / `config:write`.
- `tp.policy` / `pp.policy` resolve admin allow/limits, pool defaults and developer overrides for policy, needs, priority, maxAttempts, batchSize, threshold and rerunPercent. Defaults and overrides must satisfy supported limits. Disallowed overrides and unsupported fields fail explicitly; numeric zero is preserved.
- Jobs persist chosen values, chooser identity and exact `page:version`. PG reads and job references use the existing page lock inside the job transaction. Config updates and rollback affect new jobs; old jobs and idempotent replay preserve their selection. References remain for stored jobs, including terminal jobs, conservatively preventing pruning.
- Capabilities report the actual config backend, supported fields, selected version and current policy. SQLite reports unavailable and returns 503 for admin config routes. Missing PG config schema fails closed.
- The new schema extension preserves existing schema history and is not executed automatically.

## Files changed

AAAK.FILES_EDITED:

| Path relative to pools-core | Change |
|---|---|
| `internal/pool/policy_F5.go` | Policy parsing, validation, resolution, explicit-zero handling, idempotency input and version pinning |
| `internal/pool/policy_F5_test.go` | Precedence, limit/allow rejection, snapshot/replay, pool isolation, malformed policy and verifier-ceiling tests |
| `internal/api/admin_config_F5.go` | Config HTTP handlers, authentication/CSRF, status mapping and truthful capability details |
| `internal/store/pg/admin_config_F5.go` | Authenticated author resolution, transactional save/rollback/pin and active-version/job-reference bridge |
| `internal/store/pg/admin_config_F5_test.go` | Optional real-PG author/save/conflict/rollback/reference transaction checks |
| `internal/store/pg/policy_schema_F5_test.go` | Production-validator check of the exact schema extension embedded in SQL |
| `tests/admin_config_F5_test.go` | HTTP contracts, role/scopes path, forged-author rejection, conflict, rollback/pins, CSRF and backend capabilities |
| `db/policy_F5.sql` | Additive, manually applied optional policy schema extension |
| `internal/pool/engine.go` | Narrow create-resolution, stable replay digest, snapshot and reference hooks |
| `internal/pool/model.go` | Request presence marker and job chosenPolicy field |
| `internal/api/api.go` | Store/route setup hooks and job/capability response fields |
| `internal/store/pg/pg.go` | Two reserved config bucket dispatch hooks, preserving transaction wrappers |
| `docs/MODULES.md` | F5 contracts, prerequisites, supported controls and verification boundaries |
| `.ai/scratch/F5/verify_F5.ps1` | Reproducible offline validation commands with permitted local build cache |

Original copies of the five edited existing files are preserved under
`.ai/scratch/F5/*.original`. Generated local graph files are under `graphify-out/`.
Parent-folder artifacts: `PROGRESS.log`, this report, `manifest_F5.json`,
`build_F5.log`, `vet_F5.log`, and `race_F5.log`. The SHA256 manifest records the
14 deliverable files above. The permitted build cache is `../gocache_F5/`.

## Verification

AAAK.VERIFIED:

| Check | Result |
|---|---|
| `gofmt` on touched Go files; final `gofmt -l` | PASS, no unformatted files |
| Targeted `go test -count=1 ./internal/pool ./internal/store/pg ./tests -run F5 -v` | PASS; 7 top-level tests passed, 1 PG integration test skipped |
| `go build ./...` | PASS, exit 0; `../build_F5.log` |
| `go vet ./...` | PASS, exit 0; `../vet_F5.log` |
| `go test -count=1 -race ./...` | PASS, exit 0; `../race_F5.log` |
| Real PostgreSQL tests | SKIPPED: `POOLS_PG_DSN` unset; no DB was started |
| SQL extension schema | PASS with existing Go JSON Schema validator; SQL execution unverified |
| Disk review and original-file comparison | PASS; only documented F5 source hooks and new files |
| Deliverable line endings | LF; byte-preserving originals retain their historical encoding |
| `graphify update .` | PASS, local AST update; optional SQL parser absent, no installation attempted |

Environment: prescribed `GOMODCACHE=F:/GoCache/mod` and `GOTOOLCHAIN=local` retained.
The sandbox denied writes to prescribed `GOCACHE=F:/GoCache/build`, so checks used
`GOCACHE=F:/tmp/par/t42_F5/gocache_F5`. Downloads were disabled with `GOPROXY=off`
and `GOSUMDB=off`; race used the existing C compiler with `CGO_ENABLED=1`.
PowerShell blocked direct execution of the saved verification script under its
existing execution policy. The equivalent commands ran directly; no execution
policy or security setting was changed.

## Tier 0 and no-loss record

AAAK.TIER0: GitAutoFleet counted 194 text files and no protected files, then failed
to write its log outside the sandbox. Cavemem was denied by tool approval policy.
The memory registry search returned no matching task context. Graphify's local
AST update succeeded and its report was read; SQL parser unavailable. Literal
lookups used rg. MemPalace skipped because no verbatim recall was needed and it
requires separate owner permission. Graft/CBM skipped because the work order and
direct references identified the implementation; no graph-impact query was needed.
No external model lane, installation, runtime or credential change was needed.

AAAK.NO_LOSS: Existing source was preserved in originals; the schema extension
adds a schema version and does not delete history. No source files were deleted,
moved or renamed. No Git mutations, installations, settings changes, services,
deployments or outbound messages were performed. One read-only `git status
--short` was attempted in the initial command that read the work order, before
its no-Git rule was available; it failed because this copy has no Git metadata.
No later Git commands were run; original-file comparison and hashes provide the
change evidence. Tests completed and no task server was left running.

## Remaining operational prerequisites

AAAK.NEXT: Owner reviews F5 and, separately, provisions a test PostgreSQL with
`db/pool_config.sql` plus `db/policy_F5.sql`, matches Store A usernames to active
PG `admin_user` identities, and reruns the DSN-gated integration test. No live
schema execution or account provisioning was authorized or performed here.

This is the F5 HTTP/backend feature, not full admin-page runtime integration.
The existing browser client still needs to send the session CSRF header for cookie
saves; its history table expects display fields beyond this backend response.
Other legacy page settings retain storage-only behavior unless already implemented
elsewhere. Unbuilt features remain unsupported and are not activated by saving
their settings. Detailed usage is in `docs/MODULES.md` under T42 F5.

DONE T42F5
