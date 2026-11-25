# F5 — Error-suppression and logging audit (current source)

**Source:** isolated archive from Git main `a37aef81`, with the scoped F5 security/logging corrections described below.

## Method and coverage
- Deterministic scan of **every** `internal/**/*.go` source line for explicit discarded assignments (`_ =`/`_, _ =`) and exact empty inline `if err != nil { }` blocks. Includes generated files/tests/comments in the CSV, separately classified. Review of errors without these syntax patterns still requires program-specific analysis; counts are **candidates, not all semantic failures**.
- **244** total syntax matches; **44** production candidate lines; **0** simple empty error blocks. Full source: `F5_ERROR_SUPPRESSION_INVENTORY.csv`. Exact source lines may shift if future commits change these files.

## Severity and disposition
| Category | Production occurrences | Response |
|---|---:|---|
| `HTTP-output` | 5 | Inspect or log at semantic call site; not all discards are failures |
| `cleanup-or-discard` | 20 | Intentional cleanup, deterministic API contract or local no-op; keep under periodic audit |
| `deterministic-or-parse` | 7 | Intentional cleanup, deterministic API contract or local no-op; keep under periodic audit |
| `inspect` | 12 | Inspect or log at semantic call site; not all discards are failures |

## Code fixes: red first, then green
1. `internal/auth/admin.go`: previously ignored the admin-account-list read and could incorrectly seed a new admin when the database read failed. Now returns the underlying storage error and creates **no account**. Regression `TestF5InitStoreAFailsClosedOnAccountListReadError`.
2. `internal/auth/admin.go`: previously masked a database outage as wrong credentials. Now propagates genuine store faults while retaining `ErrUnauthorized` for missing accounts and `ErrForbiddenRole` for contributor identities. Regression `TestF5AuthenticateStoreAPropagatesStoreOutage`.
3. `internal/api/api.go`: `claimWebhook` previously ignored a failed store.Update; now emits structured `webhook.claimFailed` warning and safely leaves the delivery unclaimed.
4. `internal/api/admin_auth.go`: login audit-store write failure now emits `admin.loginAuditFailed` warning. Login itself remains successful, and the warning never includes secrets.
5. `internal/logx`: a **single opt-in operator variable** `POOLS_LOG_MODULE_LEVELS=admin=warn,recompute=debug,api=error` configures independent log thresholds by event prefix before `.` or a structured `module` attribute. Fallback preserves existing global `Debug` setting. Invalid input fails logger startup and does not expose credentials. TRACE, DEBUG, INFO, WARN, ERROR accepted; tests cover filtering, normalization, invalid input.

## Remaining syntactic candidates (production)
| File:line | Classification | Statement |
|---|---|---|
| `internal/api/admin_config_F5.go:258` | inspect | `_ = json.Unmarshal(policy.PoolDefault[field], &number)` |
| `internal/api/api.go:106` | HTTP-output | `_, _ = w.Write(a.Data)` |
| `internal/api/api.go:703` | inspect | `_ = tw.Close()` |
| `internal/api/api.go:704` | inspect | `_ = encoder.Close()` |
| `internal/api/api.go:714` | inspect | `_ = tw.Close()` |
| `internal/api/api.go:715` | inspect | `_ = encoder.Close()` |
| `internal/api/api.go:720` | inspect | `_ = tw.Close()` |
| `internal/api/api.go:721` | inspect | `_ = encoder.Close()` |
| `internal/api/api.go:727` | inspect | `_ = encoder.Close()` |
| `internal/api/api.go:773` | HTTP-output | `_, _ = w.Write(body)` |
| `internal/api/metrics_MVPW.go:197` | HTTP-output | `_, _ = w.Write([]byte(body))` |
| `internal/api/upload_scan_F8.go:239` | HTTP-output | `_ = json.NewEncoder(w).Encode(body)` |
| `internal/api/workload_seed_W10.go:173` | HTTP-output | `_ = json.NewEncoder(w).Encode(map[string]interface{}{` |
| `internal/caps/mem_windows.go:42` | cleanup-or-discard | `_ = err` |
| `internal/checksuite/checksuite.go:286` | cleanup-or-discard | `_ = sse.collect()` |
| `internal/checksuite/checksuite.go:287` | cleanup-or-discard | `_ = gw.collect()` |
| `internal/checksuite/checksuite.go:290` | cleanup-or-discard | `_ = jobA` |
| `internal/checksuite/checksuite.go:303` | cleanup-or-discard | `_ = sse.collect()` |
| `internal/checksuite/checksuite.go:304` | cleanup-or-discard | `_ = gw.collect()` |
| `internal/checksuite/transport.go:410` | cleanup-or-discard | `_ = res.Body.Close()` |
| `internal/checksuite/transport.go:469` | cleanup-or-discard | `_ = stream` |
| `internal/checksuite/worker.go:367` | cleanup-or-discard | `_ = jobID` |
| `internal/logx/logx.go:65` | inspect | `_ = w.Close()` |
| `internal/pool/policy_F5.go:327` | deterministic-or-parse | `raw, _ = json.Marshal(fields)` |
| `internal/pool/recompute_notify_N11.go:56` | cleanup-or-discard | `_ = conn.Close(context.Background())` |
| `internal/pool/recompute_notify_N11.go:66` | cleanup-or-discard | `defer func() { _ = conn.Close(context.Background()) }()` |
| `internal/pool/recompute_notify_N11.go:97` | cleanup-or-discard | `_ = conn.Close(context.Background())` |
| `internal/pool/recompute_notify_N11.go:114` | inspect | `_ = fresh.Close(context.Background())` |
| `internal/pool/recompute_notify_N11.go:197` | cleanup-or-discard | `defer func() { _ = conn.Close(context.Background()) }()` |
| `internal/pool/upload_scan_F8.go:571` | cleanup-or-discard | `_ = rc.Close()` |
| `internal/pool/upload_scan_F8.go:889` | inspect | `_ = e.applyUploadScan(&a)` |
| `internal/pool/webhook_F7.go:94` | deterministic-or-parse | `_, _ = mac.Write([]byte(parts[0]))` |
| `internal/pool/webhook_F7.go:95` | deterministic-or-parse | `_, _ = mac.Write([]byte("."))` |
| `internal/pool/webhook_F7.go:97` | deterministic-or-parse | `_, _ = mac.Write(body)` |
| `internal/pool/webhook_F7.go:153` | deterministic-or-parse | `payload, _ = hex.DecodeString(eventName[2])` |
| `internal/pool/webhook_F7.go:204` | deterministic-or-parse | `payload, _ = hex.DecodeString(eventName[2])` |
| `internal/redisadmin/resp.go:42` | cleanup-or-discard | `stop := context.AfterFunc(ctx, func() { _ = conn.Close() })` |
| `internal/store/pg/config.go:187` | cleanup-or-discard | `_ = tx.Rollback(ctx)` |
| `internal/store/pg/pg.go:70` | cleanup-or-discard | `_ = tx.Rollback(ctx)` |
| `internal/store/pg/pg.go:76` | cleanup-or-discard | `_ = tx.Rollback(ctx)` |
| `internal/store/pg/pg.go:371` | cleanup-or-discard | `defer func() { _ = tx.Rollback(ctx) }()` |
| `internal/store/redislease/client.go:256` | inspect | `_ = c.conn.Close()` |
| `internal/store/sqlite/sqlite.go:37` | cleanup-or-discard | `fail := func(e error) (*DB, error) { _ = db.Close(); return nil, e }` |
| `internal/verify/rerun.go:75` | deterministic-or-parse | `_, _ = mac.Write([]byte(taskID))` |

## Explicitly not changed
- Cleanup closes and rollbacks with primary error already captured; `hash.Hash.Write` cannot produce I/O errors; some HTTP writes cannot recover once response headers have been sent. These require context-sensitive reporting, not indiscriminately converting every discard into fatal errors.
- `pool.RescanArtifact` deliberately stores its failure as the review state instead of returning a failure; the source comment documents that contract. No behavior changed.
- This inventory is not a claim that every possible missing log across all control-flow paths has been proven absent. Additional semantic lint/race/fault injection should be added as a separate work order.

## Owner choice recorded, implemented as the smallest no-migration option
- One environment knob, no database schema or UI configuration migration. Module names are existing structured event prefixes or `module` slog field; there is no automatic inference from Go import paths. A later per-module admin UI/DB persisted level is **not** included in F5.
