# FIX-A tamper-detection completion

| Review row | Fix | Regression test / validation |
|---|---|---|
| Empty result hash bypass | Hash checking now requires a non-empty declared SHA-256 matching submitted bytes. | `TestMissingResultHashIsRejected` |
| Failed canary accepted | Failed canaries return a failed result and never enter accepted results. | `TestCanaryFailureBarsTheWorker` |
| Detectable sidecar canary | Canary input is scheduled as the ordinary task input; no canary sidecar or expected answer is sent. | `TestCanaryFailureBarsTheWorker` |
| Verifier config race / split update | Verifier config is locked; engine trust and verifier config update under one engine lock. | `go test -race ./...` |
| Barred worker can submit | Submit now applies the same eligibility gate as Claim. | `TestBarredWorkerCannotSettleExistingLease` |
| Bar mapped to warning | `StatusBar` now produces `VerdictRejected`. | `TestVerifierRejectsBarredReputation` |
| Replayable/unvalidated envelopes | Envelope validation is enforced; envelopes bind job/task, lease token, worker, artifact/input hashes, and are deduplicated per lease. | `TestClaimSignsTaskEnvelope`, full `tests` suite |
| Sampling only task ID | Rerun and canary sampling use `jobId:taskId`. | rerun pool tests plus full suite |
| Owner exclusion / pending loss | Reruns exclude both worker ID and owner; exhausted reruns accept the held pending result instead of discarding it. | rerun pool tests, `go test -race ./...` |
| Reputation mutation before commit | Reputation observations are queued and applied only after successful store transactions. | `go test -race ./...` |
| Sub-second envelope TTL / repeated clock reads | Envelope TTL is clamped to at least one second and signing uses one captured clock value. | `TestNewTaskEnvelopeHasAtLeastOneSecondTTL` |
| Server artifact corruption blamed on worker | Module corruption still flags/fails the task but does not record worker failure reputation. | full pool and race suites |

**Files changed:** `internal/pool/*.go`, `internal/verify/*.go`, and focused tests in `internal/pool/trust_test.go`, `internal/verify/verify_test.go`, `tests/core_test.go`.

**Validation tail:**

```text
go build ./...                 PASS
go vet ./...                   PASS
go test ./...                  PASS
go test -race ./...            PASS
  ok  	ccgsc/poolscore/internal/pool	4.108s
  ok  	ccgsc/poolscore/internal/store/pg	1.558s
  ok  	ccgsc/poolscore/internal/verify	1.363s
  ok  	ccgsc/poolscore/tests	4.562s
```

**AAAK.STATUS:** FIX-A implemented and verified.

**AAAK.TIER0:** Work-order read completed; `cavemem` was invoked but returned CLI usage because no query was supplied; `graphify update .` completed with 1,352 nodes and 2,784 edges; GitAutoFleet inspection was blocked by unavailable interactive permission; `subs_watch` was skipped because its repository-relative script was absent; existing graph query was skipped because `graphify-out\graph.json` did not exist.

**AAAK.VERIFIED:** Source and tests were read from disk; formatting, build, vet, full tests, race tests, and graph update completed successfully. Git status/diff could not run because this work folder is not a Git repository.

**AAAK.NO_LOSS:** No unrelated source files were reverted or deleted. Graphify reported two SQL files omitted from graph extraction because `tree_sitter_sql` is not installed; Go validation is unaffected.

**AAAK.NEXT:** None.

DONE FIXA
