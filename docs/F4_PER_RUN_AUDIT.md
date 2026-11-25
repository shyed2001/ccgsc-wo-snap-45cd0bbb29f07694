# F4 — Developer-owned per-run audit evidence

Opt in at job creation (Thread or Process): `"options":{"tamperEvidence":{"mode":"per-run-chain"}}`.
Existing `per-job-chain` and Merkle modes are unchanged; the new mode does not enable or write global checkpoints, and no per-pool chain is added.

## Developer contract

- Create/GetJob and result pages expose `chainHead`, `chainEntries` (sequence/count), and `auditGap`. The bulk REST formats also return these as X-Chain-Head, X-Chain-Entries and X-Audit-Gap headers. The head is the snapshot at the time of the result read; it may advance on later settlement.
- Existing authenticated GET /{thread-pool|process-pool}/v1/jobs/{jobId}/chain and gRPC GetJobChain export `runEntries` with the verification verdict. No second route is needed. The old `entries` collection is preserved for legacy modes.
- Record order: module artifact IDs, task input digests, accepted result digests, plus a gap record if required. Digests are SHA-256 of the stored exact input/output bytes; module IDs are the content-addressed artifact digests. Reference index distinguishes entries. Each hash uses the domain `ccgsc.pools.run-audit.v1`, a zero byte and canonical JSON covering sequence, kind, job/task identity, index, digest, worker, UTC time, predecessor (excluding its own hash).
- Store the final head and sequence independently on the developer system. The exported chain can be checked by recomputing hashes and comparing this anchor, module/input/result digests and record counts. A server-controlled chain without an external trusted anchor does not prove resistance to server-wide rewriting or computation correctness.

## Failure semantics

Result settlement is committed first; audit is attempted in its own bounded transaction, fenced by the job row. An audit append failure never rolls back a committed result. A warning and best-effort durable gap flag are recorded, and reconciliation inserts the gap record. Any gap invalidates the completeness verdict even after missing records are reconciled. A complete DB outage can prevent persisting the gap flag itself; structured warnings are emitted, and the developer must treat missing/stale evidence as unverified. No platform checkpoint service is introduced.

Deferred/background results are reconciled when the developer next reads the job or chain. No worker penalty, computation validation, rewards or billing are implied.

## Local verification

The F4 targeted tests cover binding and tamper/truncation detection, failure injection and result retention, PG two-engine concurrency and restart, and REST/gRPC transport mappings. Run the approved full PG+Redis gate before promotion.
