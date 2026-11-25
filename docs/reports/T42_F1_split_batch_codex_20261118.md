# T42 F1 - split/batch

Implemented row F1 only in `F:/tmp/par/t42_F1/pools-core`.
Measured 2026-11-18 on DESKTOP-NAF9NIA / User; Go 1.26.2 windows/amd64.
No prior F1 progress/report or partial F1 implementation was present.

## Behavior

- Complete artifact sources expand into deterministic tasks: byte slices, or half-open integer ranges read from a JSON start/end artifact. Count is 1..10000. Without chunksize, the partition is balanced; explicit chunksize produces a final remainder and must agree with count. Empty partitions, malformed ranges, overflow, conflicting hints and over-limit requests fail explicitly.
- `batchSize > 1` returns all signed members in `assignment.batch`, atomically leased from one job/session with a shared token. One batch occupies one worker slot. Top-level assignment fields mirror the first member. Per-task generations, signatures, submission, retries and result verification remain intact. Omitted/zero/one batchSize retains the single-task response shape; maximum is 10000.
- A valid member heartbeat renews still-live siblings; settled or expired members are not revived. Slots remain occupied while any batch member is active. Existing GPU/session and rerun eligibility filters remain in force.
- Capabilities advertise bytes/range split support and batching with limits. Unsupported rows/records formats, inline-task chunk maps and unrelated chunk-map entries still return ErrUnsupported; options/groupBy/pin and other feature rows remain gated.
- Workers consuming batches must process the complete `batch` array once, not execute both it and the top-level mirror. Submit results separately for each member. Byte inputs reference slices; workers fetch/slice the artifact. This work does not modify browser workers or implement bulk result submission.

## Files changed

| File | Change |
| --- | --- |
| `internal/pool/split_F1.go` | New split/batch validation, limits, slice wire types and artifact expansion. |
| `internal/pool/batch_F1.go` | New atomic batch selection, relocated existing task-signing logic, slot accounting and renewal helper. |
| `internal/pool/split_batch_F1_test.go` | New boundary, validation, lifecycle, concurrency, max-size and rollback tests. |
| `internal/pool/engine.go` | Hooks for validation, task expansion, batch claims and active batch counting; removed obsolete batching warning. |
| `internal/pool/model.go` | Added optional Assignment.batch field and contract comments. |
| `internal/pool/resources.go` | One renewal helper call after existing member authentication. |
| `internal/api/api.go` | Capability fields/limits and removed split-helper limitation. |
| `tests/core_test.go` | Existing three-task HTTP acceptance now consumes the requested real batch. |
| `tests/split_batch_F1_test.go` | New HTTP split/batch/capability acceptance for both pools. |
| `api/thread_pool_v1.openapi.yaml` | Split format/count/chunk rules and batch limit/response documentation. |
| `api/process_pool_v1.openapi.yaml` | Matching process-pool schema documentation. |
| `api/ccgsc_pools_v1.proto` | Split comments only; no gRPC implementation or field changes. |
| `docs/MODULES.md` | Updated existing owner documentation with split/batch contracts and remaining limitations. |

## Validation

| Check | Result |
| --- | --- |
| gofmt on all 9 changed/new Go files, final gofmt -l | PASS; no outstanding formatting. |
| Focused existing signing and HTTP acceptance tests | PASS (pool 0.778s, tests 0.326s). |
| `go test ./internal/pool -run '^TestF1' -count=1` | PASS (2.840s). |
| `go build ./...` | PASS, exit 0. |
| `go vet ./...` | PASS, exit 0. |
| `go test -count=1 -race ./...` | PASS, exit 0. Pool 20.227s; HTTP/integration tests 11.221s; all other packages passed or had no test files. |
| Both OpenAPI YAML documents parsed; range/count schema assertions | PASS. |
| Read/diff against saved pre-edit files; hash and LF checks | PASS; 13 changed/new source/documentation files, all LF. |
| `graphify update .` | PASS; AST only, 1753 nodes / 3581 edges / 176 communities. Optional SQL parser absent, two SQL files omitted; no install. |

Coverage includes 1/N/remainder/10000 partitions, a 10000-member lease, explicit rejection above limits, split idempotency, missing/incomplete sources, invalid and overflowing ranges, chunk-map conflicts, batch remainder, one-slot behavior, concurrent claims with two slots, renewal beyond the original signed deadline, expired/stale fences, and transaction rollback on signing failure. Real HTTP handlers/auth/SQLite are exercised without network listeners.

### Environment deviation

The prescribed `GOMODCACHE=F:/GoCache/mod`, `GOCACHE=F:/GoCache/build`, `GOTOOLCHAIN=local` was attempted first. The initial targeted test failed before compilation because the sandbox denied writes under `F:/GoCache/build`.
All successful commands retained the required module cache and local toolchain but used the writable `GOCACHE=F:/tmp/par/t42_F1/gocache_F1`. CGO_ENABLED=1 and the existing gcc were used for race testing. No installation, PATH change or permission bypass was performed.

## Review artifacts and AAAK

- `F:/tmp/par/t42_F1/PROGRESS.log`: streamed steps and check exit codes.
- `F:/tmp/par/t42_F1/CHANGES_F1.patch`: unified source/documentation diff, generated without git.
- `F:/tmp/par/t42_F1/SHA256_F1.txt`: final hashes for the 13 source/documentation files.
- `F:/tmp/par/t42_F1/baseline_F1/`: pre-edit copies and manifest retained for review.
- `F:/tmp/par/t42_F1/gocache_F1/` and repo `graphify-out/`: generated local artifacts; not source changes for integration.

AAAK.STATUS: F1 implemented and verified; ready for owner review/integration.
AAAK.TIER0: GitAutoFleet brief enumerated 183 files but failed saving its report outside writable roots; used targeted rg and baseline file comparisons. Graphify AST update succeeded. Initial graph queries skipped because no graph existed; Graft/CBM were unnecessary for the narrow literal lookups answered by rg. No prior F1 artifacts required session recall; MemPalace was not accessed. No external model lane was needed for this assigned Codex row.
AAAK.FILES_EDITED: 13 source/documentation files listed above; F1 evidence outside the work folder plus generated graph/cache artifacts.
AAAK.NO_LOSS: Original split request and per-task evidence retained; no git commands, deletes, installs, server starts, credentials/settings/PATH changes, or other feature-row implementations.
AAAK.VERIFIED: Build, vet, full race suite, focused tests, YAML parsing, LF checks and disk diff/hash review passed with the disclosed cache deviation.
AAAK.NEXT: Owner reviews/integrates F1; batch-consuming workers use the documented assignment.batch contract. No F2-F10 work performed.
DONE T42F1
