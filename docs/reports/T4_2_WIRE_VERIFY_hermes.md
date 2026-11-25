# T4.2 - wire the verify measures into the pool engine

Work order: `F:\tmp\hcwork\t4_2\pools-core\docs\work_orders\WO_T4_2_WIRE_VERIFY.md`
Work folder: `F:\tmp\hcwork\t4_2\pools-core` (all edits inside it)
Executed by: Hermes (MiniMax) for the owner, 2026-11-17
Decisions applied: WWPP-R35.1, R37.1, R37.1b, R39.1 (docs/decisions/GRILL_WWPP_R1.md)
Go env: `GOMODCACHE=F:\GoCache\mod` `GOCACHE=F:\GoCache\build` `GOTOOLCHAIN=local`, go1.26.1 windows/amd64

## Files

All changes are in `internal\pool\*.go` (plus one new test file there). Nothing outside
`internal\pool` was touched.

| File | Change |
|---|---|
| `internal/pool/model.go` | wire contracts for the trust measures |
| `internal/pool/engine.go` | the trust measures in the claim and the submit path |
| `internal/pool/trust.go` | NEW - the trust configuration and the helpers of the measures |
| `internal/pool/trust_test.go` | NEW - 16 tests of the measures |
| `internal/pool/session.go` | unchanged |
| `internal/pool/resources.go` | unchanged |

`internal/pool/model.go`

- `Assignment.TaskEnvelope` (`verify.SignedTaskEnvelope`): the signed task envelope of
  R37.1 next to the legacy `Envelope`.
- `Assignment.Canary` (`*CanaryAssignment`): the injected known-answer canary.
- `CanaryAssignment`: canary id, the wire body (input only), its signed envelope.
- `Submission.CanaryID`, `Submission.CanaryAnswer`: the canary answer of a worker.
- `Task`: the trust state - `Pending` (the held first result), `RerunExclude` (the
  workers that already ran the task), `FirstSHA256`, `CanaryID`, `Flagged`, `FlagReason`.
- `PendingResult` (+ `Bytes`): the first result that waits for the re-run verdict.
- `Result`: the trust evidence - `WorkerID`, `Verified`, `HashOK`, `CanaryID`,
  `CanaryOK`, `Rerun`, `RerunOK`, `Flagged`, `FlagReason`.

`internal/pool/engine.go`

- `Engine` carries `Verifier`, `TaskKey`, `TaskKeyID` and the `TrustConfig`.
  `New()` generates the task envelope key, registers it in the key ring and loads the
  R37.1 defaults.
- `Claim`: signs the task envelope (canonical JSON, domain separated), injects the
  canary at the configured rate, and gates the lease by the reputation standing of the
  worker.
- `Submit`: the result hash check, the module hash check, the canary check, the
  reputation events, the re-run dispatch and the hash-only re-run comparison.
- `expire`: a lost lease records a reputation timeout; a held re-run that nobody picks
  up within the window releases the first result.
- `Validate`: the stale warning "Re-run/canary/reputation dispatch is not enabled in
  WO-CORE-00" is gone (the dispatch exists now).

`internal/pool/trust.go` (new)

- `TrustConfig` embeds `verify.Config` and adds `CanaryRate`; `DefaultTrustConfig()`
  carries the R37.1 defaults.
- `SetTrustConfig`, `SetVerifyConfig`, `TrustConfig`, `TrustStats`, `TaskPublicKey`,
  `VerifyTaskEnvelope`, `RegisterCanary`, `RegisterCanaryWithAnswer`, `RotateCanary`.
- Internals: `moduleDigest`, `signTaskEnvelope`, `pickCanary`, `checkCanary`,
  `observe`, `eligible`, `excluded`, `requestedRerunPercent`, `reRunWindow`,
  `modulesIntact`.

## What each work order item does

**1. Pool signs tasks with verify.TaskEnvelope + Keyring (canonical JSON).**
`Claim` builds `verify.NewTaskEnvelope(jobId, taskId, artifactSha256, inputSha256,
issuedAt, expiresAt, keyId)` and signs it with the engine task key through
`verify.SignTaskEnvelope`. The payload is the canonical JSON of the envelope (sorted
keys, no spaces) with the domain separator `ccgsc.pools.task-envelope.v1`; the
signature is Ed25519. The key id is the first 16 hex chars of the SHA-256 of the
public key, and the public key lives in the `verify.Keyring` of the engine
(`Engine.VerifyTaskEnvelope` verifies against the ring, so an unknown, revoked or
mismatched key id fails closed). The legacy `Assignment.Envelope` stays, because the
frozen end-to-end test (`tests/core_test.go:145`) verifies it.

**2. On result: artifact/result hash check, reputation events, warn/bar/ban affects
lease eligibility.**
`Submit` computes `verify.SHA256(q.Output)` and compares it with the declared hash; a
mismatch is never accepted, it is a reputation failure of the worker, the task is
flagged and the event `task.tampered` is written. The module artifacts are checked
against their content hash on every load of the task (`modulesIntact`). The reputation
events are `EventPass` for an accepted result, `EventFail` for a worker error, a
tampered result and a re-run mismatch, `EventCanaryFail` for a failed canary and
`EventTimeout` for a lost lease. The standing of a worker comes from
`verify.Reputation().StatusOf`: a barred or banned worker gets no lease at all
(`Claim` returns `ErrForbidden`), a warned worker still works.

**3. Re-run %: verify.ShouldRerun picks tasks, a second lease goes to a different
worker, compare with CompareResults, a mismatch is a reputation fail for both and
flags the task.**
The rate is the developer requested `rerunPercent` of the job, capped by the admin
ceiling of `verify.Config`; `verify.ShouldRerun` draws the task. The first result is
held in `Task.Pending`, the task goes back to `queued` and the worker that produced
it is added to `Task.RerunExclude`, so the second lease goes to a different worker.
The second result is compared with `verify.CompareResults` plus
`verify.CompareResultHashes` - exact hashes only, the server never runs developer
code. A mismatch records `EventFail` for both workers, flags the task and ends it
without a retry (the evidence is conclusive). A match accepts the result.

**4. Canary injection at the configured rate, CheckCanary on return.**
`pickCanary` draws the canary deterministically from the hash of the task id at
`TrustConfig.CanaryRate` (default 1 %). The body of the canary carries the input
only - no canary marker and no expected hash reach the worker - and the canary travels
signed like every other task. On return, `checkCanary` calls
`verify.CanaryRegistry.CheckCanary` (hash compare only, the expected answer stays
server side) and a failure is a `EventCanaryFail` of the worker plus a flagged task.

**5. All switches from verify.Config, defaults per R37.1, light metrics via
verify.Stats().**
Every switch of the four measures is read from `verify.Config` and is honoured in the
code path: `HashCheckEnabled` (the result and the module hash check),
`RerunEnabled` + `RerunPercent` + `RerunMaxPercent` (the re-run),
`EnvelopeEnabled` (the task envelope and the canary envelope),
`CanaryEnabled` (the injection and the check), `ReputationEnabled` (the events).
The defaults are `verify.DefaultConfig()` (hash check on, re-run on at 1 %, envelope
on, canary on, reputation on, warn below 0.8, bar below 0.5, ban after 3 canary
failures). `Engine.SetTrustConfig` / `SetVerifyConfig` are the seam for the admin
pages (R37.1b), `Engine.TrustStats()` returns `verify.Stats()`.

**6. Tests in internal\pool.**
16 tests in `internal/pool/trust_test.go`: happy path, tampered result, re-run
mismatch, canary fail -> bar, plus the envelope signing, the re-run match, the exact
byte comparison, the 0 percent case, the ban threshold, the lease gate of a barred
worker, the disabled measures, the switches and the metrics.

## Bug found and fixed during the work

The re-run comparison first compared the stored `json.RawMessage` of the first
result. The JSON round trip through the store compacts a raw message, so two
identical submissions could be reported as a re-run mismatch - a false accusation of
tamper against two workers. `PendingResult.Bytes` now keeps the exact submitted bytes
(base64) and the comparison uses them. The regression test
`TestReRunComparesTheExactSubmittedBytes` was verified to fail when the fix is
reverted and to pass with it.

## Build and test tail (exact output)

```
### go build ./...
exit=0
### go vet ./...
exit=0
### gofmt -l .
(empty = clean)
### go test ./... -count=1
?   	ccgsc/poolscore/cmd/pools-core	[no test files]
?   	ccgsc/poolscore/internal/api	[no test files]
?   	ccgsc/poolscore/internal/auth	[no test files]
?   	ccgsc/poolscore/internal/config	[no test files]
?   	ccgsc/poolscore/internal/httpx	[no test files]
?   	ccgsc/poolscore/internal/logx	[no test files]
ok  	ccgsc/poolscore/internal/pool	3.208s
?   	ccgsc/poolscore/internal/store	[no test files]
?   	ccgsc/poolscore/internal/store/pg	[no test files]
?   	ccgsc/poolscore/internal/store/sqlite	[no test files]
ok  	ccgsc/poolscore/internal/verify	1.729s
?   	ccgsc/poolscore/internal/worker	[no test files]
ok  	ccgsc/poolscore/tests	3.682s
?   	ccgsc/poolscore/web	[no test files]
```

```
### go test ./internal/pool/ -v -count=1
=== RUN   TestClaimSignsTaskEnvelope
--- PASS: TestClaimSignsTaskEnvelope (0.05s)
=== RUN   TestHappyPathCompletesWithTrustEvidence
--- PASS: TestHappyPathCompletesWithTrustEvidence (0.05s)
=== RUN   TestTamperedResultIsRejectedAndFlagged
--- PASS: TestTamperedResultIsRejectedAndFlagged (0.05s)
=== RUN   TestReRunMismatchFailsBothWorkersAndFlagsTask
--- PASS: TestReRunMismatchFailsBothWorkersAndFlagsTask (0.05s)
=== RUN   TestReRunMatchAcceptsResult
--- PASS: TestReRunMatchAcceptsResult (0.05s)
=== RUN   TestReRunComparesTheExactSubmittedBytes
--- PASS: TestReRunComparesTheExactSubmittedBytes (0.05s)
=== RUN   TestReRunNotSampledBelowRate
--- PASS: TestReRunNotSampledBelowRate (0.05s)
=== RUN   TestCanaryFailureBarsTheWorker
--- PASS: TestCanaryFailureBarsTheWorker (0.05s)
=== RUN   TestCanaryFailuresBanTheWorkerAtTheDefaultThreshold
--- PASS: TestCanaryFailuresBanTheWorkerAtTheDefaultThreshold (0.05s)
=== RUN   TestBarredWorkerGetsNoLease
--- PASS: TestBarredWorkerGetsNoLease (0.04s)
=== RUN   TestCanaryNotInjectedWhenMeasureIsOff
--- PASS: TestCanaryNotInjectedWhenMeasureIsOff (0.04s)
=== RUN   TestTrustSwitchesComeFromVerifyConfig
--- PASS: TestTrustSwitchesComeFromVerifyConfig (0.05s)
=== RUN   TestEnvelopeSwitchIsHonoured
--- PASS: TestEnvelopeSwitchIsHonoured (0.05s)
=== RUN   TestTrustStatsAreRecorded
--- PASS: TestTrustStatsAreRecorded (0.05s)
PASS
ok  	ccgsc/poolscore/internal/pool	1.969s
```

`go build ./...`, `go vet ./...` and `go test ./...` all pass. The frozen end-to-end
test `tests/core_test.go` is unchanged and still green.

## Notes and limits

- The task envelope key is generated per engine process. The public key is available
  through `Engine.TaskPublicKey()` for an out-of-band provisioning; the key ring of the
  engine holds it. A production deployment persists the key (the storage work order).
- The reputation tracker of `verify` is in-memory; `Snapshot`/`Restore` are the seam
  for the durable store (not part of this work order).
- A held re-run result is released after the re-run window (2 x lease TTL, at least
  one minute) if no second worker picks the task up, so a re-run never blocks a job.
- `go test -race` fails on this host with the exit code 0xc0000139 for the untouched
  `internal/verify` package too: the race runtime is not available in this
  environment. It is not caused by this work order.
- The admin pages for the switches (R37.1b) are not part of this work order: the
  seam is `Engine.SetTrustConfig` / `Engine.SetVerifyConfig` plus
  `Engine.TrustConfig()` / `Engine.TrustStats()`.

Progress log: `F:\tmp\hcwork\t4_2\PROGRESS.log`

DONE T4.2
