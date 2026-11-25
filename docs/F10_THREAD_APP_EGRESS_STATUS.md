# F10 — Logical Thread Application Pool and Egress Safety

**Candidate SHA basis:** `95f0a282` plus F10 implementation, 24 November 2026. **No deployment.**

## Implemented Thread app feature

`internal/threadapp` implements a distinct Thread application pool module, with **NO import from `internal/processapp`**. An administrator approves an app per developer key subject through:

```http
PUT /admin/v1/thread-apps/AppAlpha
Authorization: Bearer <ADMIN_TOKEN>
Content-Type: application/json

{"owner":"<DEVELOPER_KEY_ID>","maxWorkers":2}
```

Read: `GET /admin/v1/thread-apps/AppAlpha?owner=<DEVELOPER_KEY_ID>` (admin only). A developer creates a Thread job with optional `appId`; unapproved app IDs are rejected, Process-only app approval does not carry over, and legacy no-`appId` jobs use the existing routes unchanged.

Each leased Thread task is assigned a distinct `workerSlotId` derived from the actual contributor host, tab and Web Worker index (0..63); existing tab slot count still gates admission. The per-app durable Thread policy tracks **W Web Worker slots across all tabs/devices**, not K whole tabs as in Process Pool. PostgreSQL row locking and `PutIfAbsentTx` preserve cross-coordinator quotas, with no global process-local mutex. Retained leases are fenced by signed tokens and generation.

Thread and Process registries use separate storage prefixes. CCGSC does **not** provide an application-owned database, shared memory, KV, locks, or pub/sub. The developer operates any external DB service itself.

## Historical F10 egress blocker (superseded by F10b candidate)

F10b introduces per-app CSP and lease-bound authorization; see [F10b developer guide](F10b_TOKEN_VERIFY_GUIDE.md). The following records the original F10 baseline, not the F10b implementation.

The binding owner policy is **default allowlist**, admin-approved per app+URL; optional admin-only open mode with abuse warning, rate limit, and a short-lived per-lease developer-scoped access capability. However the **live run origin still sets `connect-src 'none'` in `internal/api/sandbox_MVPW.go`**. The F8 browser test verified that both null-origin iframe and nested blob worker `fetch` are blocked. No F10 code changes this CSP or passes long-lived developer credentials into untrusted contributor code.

To turn egress on without breaking sandbox isolation, build and test a **trusted endpoint-scoped broker** or separate tightly scoped runner origin with these invariants: (1) admin-approved app and exact HTTPS endpoint, no wildcard or private IP/SSRF, DNS rebinding/redirect/host recheck; (2) **short-lived, narrow, per-lease capability**, not a developer master API token; (3) bounded response/upload size, rate and concurrency; (4) auditable endpoint list on contributor page, consent and revocation; (5) no results/data P2P or bypass via alternate fetch/WebSocket/WASM imports; (6) actual same-origin and null-origin Chrome tests, including rejection of unapproved endpoints. Admin-only open mode must warn and remain off unless explicitly approved and isolated.

**No egress is enabled by this commit. This F10 subcriterion is PARTIAL/BLOCKED, not DONE.** Request owner approval of broker architecture and any admin open-mode risk before switching CSP. Preserve the working default-deny security boundary.

## Tests and evidence

- `internal/threadapp/registry_F10_test.go`: W quotas, distinct slots on the *same* tab, expiry, cross-app slot exclusivity, same slot retry and invalid ownership.
- `internal/threadapp/pg_F10_test.go`: **five W=1 admission races** through two independent PostgreSQL connections against a private test schema.
- `internal/pool/thread_app_F10_test.go`: real Thread job Create→Claim twice, independent slot identities, cross-tab W limit, Process-vs-Thread approval isolation, unapproved app rejection and legacy thread continuity.
- `internal/api/thread_app_F10_test.go`: anonymous/developer cannot approve; admin can approve; quota validation and admin read.
- The modified earlier F9 test now expects **forbidden** on Thread with Process-only approval, reflecting newly approved independent Thread app support, **not a weakened security assertion**.

**Platform-readiness boundary:** registry and APIs are tested, but not every browser adapter has been updated to choose an explicit worker index. A production end-to-end Chrome Thread job and per-app egress authorization remain acceptance requirements. The final release gate and multi-node acceptance are separate owner-controlled milestones.
