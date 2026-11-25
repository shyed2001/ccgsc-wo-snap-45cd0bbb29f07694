# F9 — Approved Per-App Process Pools (K Whole Tabs)

**Implementation basis:** `3209fee7` + F9 isolated candidate. Feature is optional. A missing `appId` retains the original generic Process and Thread pool behavior; no live deployment or default change.

## Trust and API contract

The administrative API can approve an immutable app identifier for a specific **developer key subject**. Admin-only, scoped `config:write`:

```http
PUT /admin/v1/process-apps/AppAlpha
Authorization: Bearer <ADMIN_TOKEN>
Content-Type: application/json

{"owner":"<DEVELOPER_KEY_ID>","maxTabs":2}
```

The admin read endpoint is `GET /admin/v1/process-apps/AppAlpha?owner=<DEVELOPER_KEY_ID>`. The developer then uses the existing `POST /process-pool/v1/jobs` body with `"appId":"AppAlpha"`. Creation checks the durable policy and exact developer owner. Unapproved or cross-owner requests fail closed; `appId` on a Thread job is rejected. The app ID accepts only bounded ASCII letters/digits/underscore/hyphen, starting with a letter; the admin limit `K` is 1–64.

No developer has the right to approve its own app. No app database credentials pass through this API. The contributor continues registering and claiming using the existing Process contributor routes; claim selects an eligible app job and atomically **binds one physical browser host+tab** to one app. For an app-bound tab, jobs of another app or generic legacy jobs are excluded until release/expiry. The contributor cannot forge its binding in the Register JSON; server-owned affinity is restored across normal tab re-registration and renewed with heartbeat.

Explicit release: `POST /process-pool/v1/workers/{tabId}/apps:release`, authenticated as the same contributor key that owns the worker tab, refused while an unexpired task lease is active.

## Durability and limits

`internal/processapp/registry_F9.go` stores one admin policy row with active hashed tab keys and a separate tab-binding row. The policy row is locked within `Store.Update`; PG `tx.Get` takes `FOR UPDATE`. `PutIfAbsentTx` atomically reserves a new tab binding, preventing two apps from simultaneously claiming the same unbound tab. This avoids process-local quota counters and supports multiple coordinators sharing one PostgreSQL store. Lease expirations use the coordinator's trusted clock and are at most 24 h; stale tabs are pruned when a new claim occurs. A re-register or heartbeat cannot silently bypass a live K limit.

This is **Process pool tab affinity**, not a new OS process, worker billing ledger, or platform shared-memory database. App data and synchronization belong entirely to the developer's own system. The future Thread Pool module is independent and must not import this Process module.

## Tests and acceptance

`internal/processapp/registry_F9_test.go`: K quota, duplicate tab idempotence, cross-app exclusivity, expiry, release, invalid/unauthorized policies, concurrent SQLite admission. `pg_F9_test.go`: five separate K=1 cross-instance admission races in a **private PostgreSQL test schema**. `internal/pool/process_app_F9_test.go`: real Engine Job→Claim binding, legacy compatibility, stolen app rejection, no release of active lease, re-register preservation, rejection of untrusted app-affinity fields. `internal/api/process_app_F9_test.go`: admin authorization, explicit quota and durable read.

**Known limitations:** contributor cannot explicitly select an app during registration; first eligible approved-app job establishes affinity. This binding is intended for the accepted shared contributor model. There is no per-app developer callback, per-app browser storage, external egress allowance, or app UI in F9; these are F10 or later gated decisions. Session affinity and GPU allocation remain subject to the existing Process pool constraints; acceptance requires the final PostgreSQL/Redis gate and additional multi-device browser testing before release.
