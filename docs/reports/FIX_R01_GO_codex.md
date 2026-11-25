# FIXGO review-01 implementation report

Date: 2026-11-17. Measured host/profile: DESKTOP-NAF9NIA / User. Go: go1.26.2 windows/amd64.
Authority: pools-core/docs/work_orders/WO_FIX_REVIEW01_GO.md and docs/reviews/REVIEW_01_agy_20261117.md.

AAAK.STATUS: All 13 findings within the assigned Go/launcher file scope implemented, including both low findings. Go checks and supplemental launcher regressions pass. Exact requested-environment acceptance is PARTIALLY BLOCKED: F:\GoCache\build is not writable and PowerShell 7 cannot be launched in this sandbox. This report does not claim the complete PowerShell 7 suite passed. Owner acceptance remains pending.

AAAK.TIER0: GitAutoFleet attempted first: uv cache access denied; installed executable inventoried 107 files and zero protected matches but failed writing its external run-state directory. Graphify query initially found no graph; required AST-only graphify update . succeeded (final: 1083 nodes, 2041 edges, 112 communities); SQL extraction skipped because tree_sitter_sql is absent, and generated community labels remain heuristic. CBM search could not create its secure coordination endpoint. rg located exact source/test references. Memory registry had no pools-core/fixgo match; Cavemem skipped because this self-contained work order and review supply the task history. MemPalace skipped because separate owner permission is required. Graft skipped because exact locations were supplied and literal searches answered the lookup. No external model lanes, metered calls, installs, or PATH changes.

AAAK.NO_LOSS: Source baseline retained in baseline.json. Every internal/pool/*.go SHA-256 matches pool-hashes.json; no edits to internal/pool, worker, store, main, or web source. Existing tests retained and extended. The requested 100-event reputation retention policy intentionally bounds detailed history; lifetime aggregate counters remain. Git metadata is absent in this staged folder: git status --short and git diff --stat both report `not a git repository`; baseline comparison substitutes for an unavailable Git diff. No git mutations.

## Finding -> fix -> test

| Review finding | Fix | Regression evidence |
|---|---|---|
| verifier.go:279 (high) | Signed canary injection uses the registered Canary.ArtifactSHA256; validates supplied digests; fails closed when a signed fixture has no module hash. | TestReviewCanaryEnvelopeBindsArtifact |
| verifier.go:209 (high) | Enabled envelope verification now runs even for nil/empty payloads and returns a rejected verdict with an error reason. | TestReviewMissingEnvelopeFailsClosed; existing signed-envelope tests |
| pools-core.ps1:301 (high) | Hidden helper attaches only to the verified isolated service console and sends CTRL_BREAK; Stop waits 12 seconds (Go shutdown budget: 10), then rechecks PID/start time before force. PID is cleared only after exit. Helper timeout is bounded. | 4 launcher regressions, including real Go exit code 0 and server.stopped log |
| api.go:192 (med) | Missing Upload-Offset defaults to zero only with known Content-Length; malformed offsets and missing offsets with unknown length still reject. | TestReviewStandardUploadAndAdminRead |
| api.go:341 (med) | Both event and worker-task SSE routes emit event: reauth with refresh_and_reconnect 30 seconds before expiration, then close cleanly. Revocation checks remain active. | TestReviewSSERenewalBothRoutes; TestReviewActiveSSERenewalBothRoutes; SSE revocation regression |
| api.go:346 (med) | One historical replay per connection; live events broadcast after successful Engine.Store transaction commit. Bounded 128-batch queues close on overflow for Last-Event-ID recovery. Filtered records advance the cursor. No per-tick event-table scan. | TestReviewSSECommitBroadcastReplayAndNoPollingScans: two clients, replay, rollback exclusion, owner filtering, live broadcast, two total historical scans, revocation |
| canonical.go:106 (med) | Use JSON.stringify-compatible short escapes for backspace and form feed. | TestReviewCanonicalControlEscapes |
| reputation.go:153 (med) | Retain latest 100 evidence entries; keep aggregate totals. Copy evidence on return/snapshot and bound restored evidence. | TestReviewReputationBoundAndUnban |
| reputation.go:308 (med) | Unban clears accumulated canary-failure count and restores score to at least BarBelow / configured StartScore. | TestReviewReputationBoundAndUnban; existing TestReputationUnban |
| api.go:88 (med) | Store A admin may read artifacts with artifacts:read or wildcard scope; director remains denied. | TestReviewStandardUploadAndAdminRead (both pool APIs) |
| pools-core.ps1:106 (med) | Normalize :port to 127.0.0.1:port, retaining existing wildcard-host normalization. | browser address normalizes port-only and wildcard listeners |
| envelope.go:283 (low) | PublicKeyAt uses caller verification clock for retirement, including the exact retirement boundary; PublicKey retains its existing wall-clock convenience API. | TestReviewRetirementUsesCallerClock |
| auth.go:194 (low) | Allow up to 120 seconds future issued-at skew; expiration and maximum token lifetime remain enforced. | TestReviewJWTIssuedAtSkewAndExpiry (120-second acceptance, 121-second rejection, exact expiry rejection) |

## Edited source and test files

- pools-core/internal/verify/canary.go
- pools-core/internal/verify/canonical.go
- pools-core/internal/verify/envelope.go
- pools-core/internal/verify/reputation.go
- pools-core/internal/verify/verifier.go
- pools-core/internal/verify/verify_test.go
- pools-core/internal/api/api.go
- pools-core/internal/auth/auth.go
- pools-core/scripts/pools-core.ps1
- pools-core/tests/launcher.Tests.ps1
- pools-core/tests/review_go_test.go

AAAK.FILES_EDITED: The 11 source/test files listed above, plus this requested REPORT.md. Additional local evidence: baseline.json, pool-hashes.json, required-checks.log, final-go-checks.log, launcher-regressions.log; generated Go build cache and graphify-out are derived artifacts.

## Validation and exact test output

AAAK.VERIFIED: Read changed files from disk and compared against the saved baseline; verified pool hashes; gofmt reports no files; go build ./..., go vet ./..., go test ./..., and targeted race tests all exit 0 with the local build cache. Both PowerShell scripts parse without errors under Windows PowerShell 5.1.26100.9818. Four supplemental launcher regression tests pass, including a real isolated Go service stopped gracefully through Stop-Core.

Working directory for commands: F:\tmp\par\fixgo\pools-core.
Requested environment was attempted unchanged:

```text
$ go build ./...

pattern ./...: open F:\GoCache\build\50\5090df5d5ad7f97aef8f57971d7790f8d26f5d7c438bd5cda62870b1e9145a3f-d: Access is denied.



exit=1

$ go vet ./...

pattern ./...: open F:\GoCache\build\50\5090df5d5ad7f97aef8f57971d7790f8d26f5d7c438bd5cda62870b1e9145a3f-d: Access is denied.



exit=1

$ go test ./...

FAIL	./... [setup failed]

FAIL

# ./...

pattern ./...: open F:\GoCache\build\50\5090df5d5ad7f97aef8f57971d7790f8d26f5d7c438bd5cda62870b1e9145a3f-d: Access is denied.



exit=1

$ pwsh -File tests\launcher.Tests.ps1

[WinError 1920] The file cannot be accessed by the system

NOT STARTED
```

Go fallback changed only the process environment GOCACHE to F:\tmp\par\fixgo\.go-build. GOMODCACHE remained F:\GoCache\mod and GOTOOLCHAIN remained local. Final exact Go check output:

```text
$ go version

go version go1.26.2 windows/amd64



exit=0

$ gofmt -l internal/verify internal/api/api.go internal/auth/auth.go tests/review_go_test.go



exit=0

$ go build ./...



exit=0

$ go vet ./...



exit=0

$ go test ./...

?   	ccgsc/poolscore/cmd/pools-core	[no test files]

?   	ccgsc/poolscore/internal/api	[no test files]

?   	ccgsc/poolscore/internal/auth	[no test files]

?   	ccgsc/poolscore/internal/config	[no test files]

?   	ccgsc/poolscore/internal/httpx	[no test files]

?   	ccgsc/poolscore/internal/logx	[no test files]

?   	ccgsc/poolscore/internal/pool	[no test files]

?   	ccgsc/poolscore/internal/store	[no test files]

?   	ccgsc/poolscore/internal/store/pg	[no test files]

?   	ccgsc/poolscore/internal/store/sqlite	[no test files]

ok  	ccgsc/poolscore/internal/verify	0.490s

?   	ccgsc/poolscore/internal/worker	[no test files]

ok  	ccgsc/poolscore/tests	2.194s

?   	ccgsc/poolscore/web	[no test files]



exit=0

$ go test -race ./internal/verify ./tests

ok  	ccgsc/poolscore/internal/verify	1.512s

ok  	ccgsc/poolscore/tests	3.543s



exit=0
```

The installed PowerShell 7 package at C:\Program Files\WindowsApps\Microsoft.PowerShell_7.7.0.0_x64__8wekyb3d8bbwe\pwsh.exe also returned access denied. No installation or permission changes were attempted.

Supplemental launcher checks loaded the actual launcher function ASTs and the Review-01 regression section of tests/launcher.Tests.ps1 into the available Windows PowerShell host, without executing the PowerShell 7 dispatcher. These are behavioral tests, not a substitute claim that the original PowerShell 7 suite ran. Exact output:

```text
PASS  browser address normalizes port-only and wildcard listeners

Stopped the verified local process (PID 123).

Graceful shutdown timed out after 12 seconds; forcing the verified process to exit.

Stopped the verified local process (PID 123).

PASS  Stop-Core signals and waits before forcing only after timeout

PASS  Stop-Core refuses force when process identity changes

Stopped the verified local process (PID 39744).

PASS  CTRL_BREAK produces real Go graceful shutdown and server.stopped log



=== summary ===

passed: 4

failed: 0

ALL TESTS PASSED
```

## Integration notes and remaining acceptance

- Signed canaries must register the actual module digest in Canary.ArtifactSHA256. Legacy unsigned canary registration remains available; signed injection refuses an absent digest instead of inventing one.
- Event broadcasting is installed by Server.Handler before serving requests or starting maintenance, matching the existing application startup order. Historical reconnect replay still uses one List call because store.Reader has no range-query contract; steady-state live delivery no longer scans history. Event producers must use Engine.Store, as current production producers do. This is an in-process bus for the current single-process service, not cross-process database change detection.
- SSE clients receive a reauth event before closure and must refresh/reconnect with Last-Event-ID where applicable. Web client changes are outside this work order.
- Other review rows owned by web files remain outside this assignment.

AAAK.NEXT: Run the unchanged required commands from a host/session that can write F:\GoCache\build and execute PowerShell 7, specifically pwsh -File tests\launcher.Tests.ps1, then obtain owner acceptance. Implementation and all executable local checks are complete; those environmental acceptance checks remain blocked.

DONE FIXGO
