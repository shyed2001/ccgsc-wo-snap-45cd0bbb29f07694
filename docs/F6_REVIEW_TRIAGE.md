# F6 — Merged R2/R3 review triage (code-verified)
**Source:** `F:\tmp\webchat_upload\MERGED_R2_R3_FINDINGS.md`, twenty items. **Source baseline:** `2f41d781`; work is in an isolated temp archive, never a live server. This is not a claim that absent R3 web-chat draft files have been received.

| # | Verdict | Evidence and disposition |
|---|---|---|
| 1 | FALSE on shipped PostgreSQL write transactions | `internal/store/pg/pg.go:160-186`: `transaction.Get` chooses `getQuery(t.lock)`; write transactions include `FOR UPDATE`. Durable settings CAS is inside `Store.Update`. |
| 2 | FALSE on shipped PostgreSQL first insert | `pg.go:258`: `PutIfAbsent` uses atomic `INSERT ... ON CONFLICT DO NOTHING` and rows affected. |
| 3 | TRUE — FIXED in F6 | `CommitRecomputeSettings` used `recomputeMu` while waiting for database operations; blocked hot-path readers. Red→green deterministic barrier `TestF6RecomputeReadLockFreeWhileAdminWriteWaitsForDB` in `internal/pool/recompute_lock_F6_test.go`. New independent `recomputeWriteMu`; cache lock held only for short monotonic version updates. |
| 4 | TRUE architectural contention; OPEN | `internal/api/api.go:eventStore.Update` holds `eventStore.mu` during callback and Store.Update. Refactoring risks event publication/subscribe ordering and close/send races; needs red-first concurrent publish test, not an unchecked move of mutex. |
| 5 | CONDITIONAL | `CommitRecomputeSettings` intentionally returns `store.ErrNotImplemented` if a store/wrapper lacks `PutIfAbsentTx`; shipped PG+SQLite implement it and `eventTx` forwards it. Third-party stores must explicitly implement capability; no unsafe Get-then-Put fallback permitted. |
| 6 | SUPPORTED / test gap | Callback `ErrSettingsConflict` must be preserved under storage wrappers; existing tests exercise 409 and durable CAS, but all adversarial wrappers are not exhaustively certified. |
| 7 | FALSE as stated for multi-server setting sync | `internal/pool/recompute_notify_N11.go` has an opt-in PG listener, periodic refresh, and reconnect `LISTEN` plus `refreshRecomputeAll`. Single-server retains local cache by design. |
| 8 | VALID CONTRACT QUESTION; OPEN | Nil expectedVersion is documented as unconditional, but a first-write insert conflict can return 409 (no blind overwrite). Owner must decide retry semantics; preserving safety is preferred. |
| 9 | INTENTIONAL RESTRICTION | `eventTx.PutIfAbsent` rejects Events bucket to preserve event broadcast contract. Do not bypass silently. |
| 10 | TEST GAP / open | Production `eventTx.PutIfAbsent` path merits explicit contention and rejected Events-bucket tests; basic CAS uses it. |
| 11 | CLOSED in prior commit `a37aef81` | N3 fixture uses `n3ScopeFixtureSQL` for both SET and function-local SET; rejects unrecognized public schema escapes. |
| 12 | CLOSED for public-catalog mutation in `a37aef81` | N3 loader snapshots public catalog before/after; private-schema validation remains separately desirable. |
| 13 | INFRASTRUCTURE POLICY; open | TestMain schema cleanup is fatal on failure; distinguish environmental inability to drop schemas from source test defects. No test weakened. |
| 14 | ENV-DEPENDENT; existing preflight | TestMain skips entire PG test setup when DSN absent; release gate requires configured PG/Redis and skips=0. |
| 15 | MAINTENANCE GAP; open | Fixture SQL filenames are hard-coded. Future migrations should be added to explicit manifest/test, not blindly executed. |
| 16 | LOW PRIORITY / open | Panic in TestMain or abrupt process exit can leave ephemeral test schema. Add owner-run orphan-schemas cleanup with age policy; never auto-drop production. |
| 17 | SECURITY LOGGING CAUTION | DSN parsing is not logged as raw text deliberately, but library errors may include fragments. Redact or suppress details in test logs; no credential values inspected. |
| 18 | TEST RIGOR GAP; open | Concurrent first-write test is scheduling-dependent; controlled competing transactions recommended. |
| 19 | TEST COVERAGE GAP; open | CAS assertions should prove the complete winning saved policy, not just version/mode. |
| 20 | TEST SCOPE LABEL GAP; open | Some two-engine tests use a non-PG harness; must label backend and add distinct PG multi-instance test. |

## Source-based F6 documentation outputs
- `API_REST_SSE_REFERENCE.md` and `F6_ROUTE_SOURCE_INDEX.csv`: 104 statically resolved Go mux registrations from current `internal/api/*.go`; draft OpenAPI marked as draft, not assumed shipped.
- `API_GRPC_REFERENCE.md`: 36 RPC method declarations, message types checked against checked-in Proto + Go handler functions.
- `DEV_USER_MANUAL.md` and `PLATFORM_DEV_GUIDE.md`: source-derived current workflows and explicit future F8/F9/F10/F4 limitations.
- External R3 site draft filenames requested in F6 were **not present** in `F:\tmp\webchat_upload` at inspection. Do not misattribute these source-generated guides to external reviewers.

## Owner decisions / risk
- Avoid changing eventStore synchronization without proving websocket/SSE broadcast order and close-vs-send behavior.
- Prefer a documented conflict on contended first-write over an unsynchronized “unconditional” overwrite.
- Postpone suggested generic opt-in N/Q result verification: F3 is DEFERRED under owner §5.
