# FIX-H tamper-path completion

## Row -> fix -> test

| Review row | Fix | Test/evidence |
|---|---|---|
| Successful hidden canary could settle the developer task | Canary input is assignment-only; the stored developer input remains intact. A passing canary returns a non-settling canary result, clears the canary, and requeues the real task. Canaries run only on the initial attempt. | `TestSuccessfulCanaryRequeuesDeveloperTask`; `TestCanaryFailureBarsTheWorker` |
| Lease renewal outlived the signed envelope | Submit accepts an expired envelope only when the currently stored, still-valid lease deadline was extended beyond the envelope deadline; signature, lease token, worker, task, artifact and input bindings remain mandatory. | `TestRenewedLeaseAcceptsOriginalEnvelopeUntilRenewedDeadline` |
| Re-run fallback claimed independent verification | Attempt/window fallback results now set `RerunOK:false`, omit `Verified:true`, and state that no comparison occurred. | `go test -race ./...`; existing re-run coverage |
| Disabled hash checking emitted positive evidence | A disabled hash check can allow acceptance, but result evidence is `HashOK:false` and `Verified:false`; a declared SHA-256 is mandatory whenever output is submitted. | `TestDisabledHashCheckDoesNotClaimVerification`; `TestMissingResultHashIsRejected` |
| Verifier configuration race | Invalid-envelope handling uses the locked local configuration snapshot instead of reading `v.cfg` directly. | `go test -race ./...` |
| Reputation was memory-only | Reputation snapshots are loaded from and persisted to the store metrics bucket, after committed observations. | Persistence wiring in `internal/pool/trust.go`; full build/test suite |
| Capabilities advertised trust measures as unavailable | `/capabilities` now reports live rerun, canary, and reputation availability/rates and no longer lists them as unavailable. | `go build ./...`; proof scan C01-C10 |
| Job responses hardcoded rerun percent to zero | Job projections expose the engine’s effective default/requested/admin-capped rerun rate. | `go build ./...`; proof scan C08 |

## FIX-A claims

1. **Hash required on submit — HELD.** Missing `sha256` is rejected; submitted bytes must match it when hash checking is enabled.
2. **Failed canary rejected — HELD.** Failed canaries are flagged and never settle a successful result.
3. **Canaries indistinguishable from real tasks — HELD.** Canary markers and expected answers remain server-side; the worker sees only task input and the normal signed envelope.
4. **Config race — HELD.** Verifier configuration reads are synchronized; the required race suite passes.
5. **Eligibility checked on submit — HELD.** Submit applies the same reputation gate as Claim.
6. **Barred worker equals rejected — HELD.** Barred and banned workers receive `ErrForbidden` at claim and settlement.
7. **Envelope bound to lease/worker and verified on submit — HELD.** Submit verifies the signature, key, job, task, worker, lease token, artifact digest, input digest, deadline and one-use envelope digest.
8. **Sampling keyed by `jobId:taskId` — HELD.** Re-run selection uses the deterministic combined key.
9. **Owner-level exclusion — HELD.** Re-run admission excludes prior workers and owners.
10. **Reputation after commit — HELD.** Observations are applied only after the store transaction succeeds and are then persisted.
11. **Server corruption is not blamed on the worker — HELD.** Server/store/module-integrity failures do not create worker reputation failures; worker-controlled hash and canary failures do.

## Validation

- `gofmt -w` on all changed Go files: pass
- `go build ./...`: pass
- `go vet ./...`: pass
- `go test -race ./...`: pass
- Optional `r2_harness.sh --repo .`: pass
- Optional `r2_proof_scan.py --dir .`: found all target files and anchors for C01-C11
- Graphify update: completed (`1554 nodes`, `3055 edges`, `169 communities`); SQL extraction warning only because `tree_sitter_sql` is not installed.
- Git status/diff inspection was unavailable because this work folder is not a Git worktree.

AAAK.STATUS: FIX-H implementation complete.
AAAK.TIER0: repository brief command was permission-blocked; subscription check was permission-blocked; graph query initially skipped because no graph existed; graphify update completed after code changes. No protected files were read.
AAAK.VERIFIED: required Go checks, optional harness, proof scan, and on-disk report were completed.
AAAK.NO_LOSS: no unrelated source files were changed; work-order scope was respected.
AAAK.NEXT: owner review of the generated report.
DONE FIXH
