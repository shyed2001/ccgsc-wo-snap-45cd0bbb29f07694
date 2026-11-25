# API REST and SSE Reference — Current Go Source

> **Runtime reconciliation (R3E):** [R3E runtime contract](R3E_RUNTIME_CONTRACT.md) is the authoritative companion for implemented endpoints, supported versus design-only settings, DTOs, and transport-specific limits at this baseline. It supersedes conflicting historical statements. No runtime change is implied.

Registration index generated from internal/api/*.go on 24 November 2026. The checked-in OpenAPI files are labeled DRAFT, not an implementation guarantee. Dynamic paths/aliases assembled outside literal HandleFunc calls require handler inspection.

## Active route registrations
| Method | URL path | Role/scope when explicit | Source |
|---|---|---|---|
| `GET` | `/admin/metrics` | `admin/metrics:read` | `internal/api/metrics_MVPW.go:35` |
| `GET` | `/admin/v1/caps` | `admin/metrics:read` | `internal/api/admin_caps_R15.go:18` |
| `PUT` | `/admin/v1/caps` | `admin/config:write` | `internal/api/admin_caps_R15.go:22` |
| `GET` | `/admin/v1/legacy-workers` | `admin/metrics:read` | `internal/api/legacy_pool_T92.go:32` |
| `GET` | `/admin/v1/metrics` | `admin/metrics:read` | `internal/api/metrics_MVPW.go:34` |
| `GET` | `/admin/v1/pools/{pool}/recompute` | `admin/config:read` | `internal/api/admin_recompute_R17.go:29` |
| `PUT` | `/admin/v1/pools/{pool}/recompute` | `admin/config:write` | `internal/api/admin_recompute_R17.go:30` |
| `GET` | `/admin/v1/pools/{pool}/settings` | `admin/config:read` | `internal/api/pool_settings_W10C.go:19` |
| `PUT` | `/admin/v1/pools/{pool}/settings` | `admin/config:write` | `internal/api/pool_settings_W10C.go:20` |
| `GET` | `/admin/v1/redis/status` | `admin/config:read` | `internal/api/admin_config_F5.go:33` |
| `POST` | `/admin/v1/subjects:revoke` | `admin/keys:write` | `internal/api/denylist_MVPW.go:23` |
| `POST` | `/admin/v1/task-keys:rotate` | `admin/config:write` | `internal/api/api.go:212` |
| `GET` | `/admin/v1/tokens/revoked` | `admin/keys:write` | `internal/api/denylist_MVPW.go:24` |
| `POST` | `/admin/v1/tokens:revoke` | `admin/keys:write` | `internal/api/denylist_MVPW.go:20` |
| `GET` | `/admin/v1/workers` | `admin/metrics:read` | `internal/api/admin_workers_T83.go:22` |
| `GET` | `/healthz` | `handler` | `internal/api/api.go:216` |
| `GET` | `/metrics` | `handler` | `internal/api/metrics_MVPW.go:36` |
| `GET` | `/nav/` | `handler` | `internal/api/api.go:302` |
| `GET` | `/pools/` | `handler` | `internal/api/api.go:303` |
| `POST` | `/pools/auth/change-password` | `handler` | `internal/api/api.go:299` |
| `GET` | `/pools/auth/csrf` | `handler` | `internal/api/api.go:290` |
| `POST` | `/pools/auth/login` | `handler` | `internal/api/api.go:296` |
| `POST` | `/pools/auth/logout` | `handler` | `internal/api/api.go:298` |
| `GET` | `/pools/auth/session` | `handler` | `internal/api/api.go:295` |
| `POST` | `/pools/login` | `handler` | `internal/api/api.go:297` |
| `GET` | `/pools/mock-artifact` | `handler` | `internal/api/api.go:294` |
| `GET` | `/pools/runner-config` | `handler` | `internal/api/api.go:291` |
| `GET` | `/pools/{$}` | `handler` | `internal/api/api.go:304` |
| `GET` | `/process-pool/v1/admin/recompute` | `admin/config:read` | `internal/api/admin_recompute_R17.go:33` |
| `PUT` | `/process-pool/v1/admin/recompute` | `admin/config:write` | `internal/api/admin_recompute_R17.go:34` |
| `GET` | `/process-pool/v1/admin/reviews` | `admin/artifacts:read` | `internal/api/upload_scan_F8.go:43` |
| `POST` | `/process-pool/v1/admin/reviews/{action}` | `admin/artifacts:write` | `internal/api/upload_scan_F8.go:47` |
| `GET` | `/process-pool/v1/admin/reviews/{artifactId}` | `admin/artifacts:read` | `internal/api/upload_scan_F8.go:44` |
| `GET` | `/process-pool/v1/admin/settings` | `admin/config:read` | `internal/api/pool_settings_W10C.go:23` |
| `PUT` | `/process-pool/v1/admin/settings` | `admin/config:write` | `internal/api/pool_settings_W10C.go:24` |
| `GET` | `/process-pool/v1/admin/upload-scan` | `admin/artifacts:read` | `internal/api/upload_scan_F8.go:49` |
| `PUT` | `/process-pool/v1/admin/upload-scan` | `admin/artifacts:write` | `internal/api/upload_scan_F8.go:50` |
| `PUT` | `/process-pool/v1/artifacts` | `developer/artifacts:write` | `internal/api/api.go:248` |
| `DELETE` | `/process-pool/v1/artifacts/{artifactId}` | `developer/artifacts:write` | `internal/api/api.go:254` |
| `GET` | `/process-pool/v1/artifacts/{artifactId}` | `handler` | `internal/api/api.go:251` |
| `POST` | `/process-pool/v1/auth/keys` | `admin/keys:write` | `internal/api/api.go:246` |
| `POST` | `/process-pool/v1/auth/keys/{action}` | `admin/keys:write` | `internal/api/api.go:247` |
| `GET` | `/process-pool/v1/capabilities` | `developer/jobs:read` | `internal/api/api.go:264` |
| `GET` | `/process-pool/v1/events` | `developer/jobs:read` | `internal/api/api.go:263` |
| `POST` | `/process-pool/v1/jobs` | `developer/jobs:write` | `internal/api/api.go:255` |
| `POST` | `/process-pool/v1/jobs/{action}` | `developer/jobs:write` | `internal/api/api.go:260` |
| `GET` | `/process-pool/v1/jobs/{jobId}` | `developer/jobs:read` | `internal/api/api.go:257` |
| `GET` | `/process-pool/v1/jobs/{jobId}/chain` | `developer/jobs:read` | `internal/api/api.go:258` |
| `GET` | `/process-pool/v1/jobs/{jobId}/results` | `developer/jobs:read` | `internal/api/api.go:261` |
| `GET` | `/process-pool/v1/jobs/{jobId}/results:bulk` | `developer/jobs:read` | `internal/api/api.go:262` |
| `POST` | `/process-pool/v1/jobs:validate` | `developer/jobs:write` | `internal/api/api.go:256` |
| `GET` | `/process-pool/v1/merkle:checkpoints` | `developer/jobs:read` | `internal/api/api.go:265` |
| `POST` | `/process-pool/v1/sessions` | `developer/jobs:write` | `internal/api/api.go:272` |
| `POST` | `/process-pool/v1/sessions/{action}` | `developer/jobs:write` | `internal/api/api.go:275` |
| `GET` | `/process-pool/v1/sessions/{sessionId}` | `developer/jobs:read` | `internal/api/api.go:273` |
| `POST` | `/process-pool/v1/sessions/{sessionId}/tasks` | `developer/jobs:write` | `internal/api/api.go:274` |
| `POST` | `/process-pool/v1/workers/anonymous` | `handler` | `internal/api/api.go:221` |
| `POST` | `/process-pool/v1/workers/register` | `contributor/worker` | `internal/api/api.go:266` |
| `POST` | `/process-pool/v1/workers/{tabId}/heartbeat` | `contributor/worker` | `internal/api/api.go:267` |
| `POST` | `/process-pool/v1/workers/{tabId}/leases` | `contributor/worker` | `internal/api/api.go:268` |
| `POST` | `/process-pool/v1/workers/{tabId}/results` | `contributor/worker` | `internal/api/api.go:270` |
| `GET` | `/process-pool/v1/workers/{tabId}/tasks` | `contributor/worker` | `internal/api/api.go:269` |
| `GET` | `/process-pool/v1{page}` | `admin/config:read` | `internal/api/admin_config_F5.go:35` |
| `POST` | `/process-pool/v1{page}` | `admin/config:write` | `internal/api/admin_config_F5.go:36` |
| `GET` | `/thread-pool/v1/admin/recompute` | `admin/config:read` | `internal/api/admin_recompute_R17.go:33` |
| `PUT` | `/thread-pool/v1/admin/recompute` | `admin/config:write` | `internal/api/admin_recompute_R17.go:34` |
| `GET` | `/thread-pool/v1/admin/reviews` | `admin/artifacts:read` | `internal/api/upload_scan_F8.go:43` |
| `POST` | `/thread-pool/v1/admin/reviews/{action}` | `admin/artifacts:write` | `internal/api/upload_scan_F8.go:47` |
| `GET` | `/thread-pool/v1/admin/reviews/{artifactId}` | `admin/artifacts:read` | `internal/api/upload_scan_F8.go:44` |
| `GET` | `/thread-pool/v1/admin/settings` | `admin/config:read` | `internal/api/pool_settings_W10C.go:23` |
| `PUT` | `/thread-pool/v1/admin/settings` | `admin/config:write` | `internal/api/pool_settings_W10C.go:24` |
| `GET` | `/thread-pool/v1/admin/upload-scan` | `admin/artifacts:read` | `internal/api/upload_scan_F8.go:49` |
| `PUT` | `/thread-pool/v1/admin/upload-scan` | `admin/artifacts:write` | `internal/api/upload_scan_F8.go:50` |
| `PUT` | `/thread-pool/v1/artifacts` | `developer/artifacts:write` | `internal/api/api.go:248` |
| `DELETE` | `/thread-pool/v1/artifacts/{artifactId}` | `developer/artifacts:write` | `internal/api/api.go:254` |
| `GET` | `/thread-pool/v1/artifacts/{artifactId}` | `handler` | `internal/api/api.go:251` |
| `POST` | `/thread-pool/v1/auth/keys` | `admin/keys:write` | `internal/api/api.go:246` |
| `POST` | `/thread-pool/v1/auth/keys/{action}` | `admin/keys:write` | `internal/api/api.go:247` |
| `GET` | `/thread-pool/v1/capabilities` | `developer/jobs:read` | `internal/api/api.go:264` |
| `GET` | `/thread-pool/v1/events` | `developer/jobs:read` | `internal/api/api.go:263` |
| `POST` | `/thread-pool/v1/jobs` | `developer/jobs:write` | `internal/api/api.go:255` |
| `POST` | `/thread-pool/v1/jobs/{action}` | `developer/jobs:write` | `internal/api/api.go:260` |
| `GET` | `/thread-pool/v1/jobs/{jobId}` | `developer/jobs:read` | `internal/api/api.go:257` |
| `GET` | `/thread-pool/v1/jobs/{jobId}/chain` | `developer/jobs:read` | `internal/api/api.go:258` |
| `GET` | `/thread-pool/v1/jobs/{jobId}/results` | `developer/jobs:read` | `internal/api/api.go:261` |
| `GET` | `/thread-pool/v1/jobs/{jobId}/results:bulk` | `developer/jobs:read` | `internal/api/api.go:262` |
| `POST` | `/thread-pool/v1/jobs:validate` | `developer/jobs:write` | `internal/api/api.go:256` |
| `GET` | `/thread-pool/v1/merkle:checkpoints` | `developer/jobs:read` | `internal/api/api.go:265` |
| `POST` | `/thread-pool/v1/sessions` | `developer/jobs:write` | `internal/api/api.go:272` |
| `POST` | `/thread-pool/v1/sessions/{action}` | `developer/jobs:write` | `internal/api/api.go:275` |
| `GET` | `/thread-pool/v1/sessions/{sessionId}` | `developer/jobs:read` | `internal/api/api.go:273` |
| `POST` | `/thread-pool/v1/sessions/{sessionId}/tasks` | `developer/jobs:write` | `internal/api/api.go:274` |
| `POST` | `/thread-pool/v1/workers/anonymous` | `handler` | `internal/api/api.go:221` |
| `POST` | `/thread-pool/v1/workers/register` | `contributor/worker` | `internal/api/api.go:266` |
| `POST` | `/thread-pool/v1/workers/{tabId}/heartbeat` | `contributor/worker` | `internal/api/api.go:267` |
| `POST` | `/thread-pool/v1/workers/{tabId}/leases` | `contributor/worker` | `internal/api/api.go:268` |
| `POST` | `/thread-pool/v1/workers/{tabId}/results` | `contributor/worker` | `internal/api/api.go:270` |
| `GET` | `/thread-pool/v1/workers/{tabId}/tasks` | `contributor/worker` | `internal/api/api.go:269` |
| `GET` | `/thread-pool/v1{page}` | `admin/config:read` | `internal/api/admin_config_F5.go:35` |
| `POST` | `/thread-pool/v1{page}` | `admin/config:write` | `internal/api/admin_config_F5.go:36` |
| `GET` | `/web/` | `handler` | `internal/api/api.go:316` |
| `GET` | `/web/admin/{$}` | `handler` | `internal/api/api.go:309` |
| `GET` | `/web/monitor/{$}` | `handler` | `internal/api/api.go:312` |
| `GET` | `/{$}` | `handler` | `internal/api/api.go:317` |

## Contracts and fields
Request/response definitions: api/thread_pool_v1.openapi.yaml and api/process_pool_v1.openapi.yaml (DRAFT). Runtime JSON tags and validation: internal/pool/model.go, internal/api/api.go, internal/worker/worker.go, internal/httpx. Protobuf and generated Go bindings: api/ccgsc_pools_v1.proto and internal/grpcapi. Do not assume every draft-only field is accepted.

A registered GET /{pool}/v1/jobs/{jobId}/chain already exists. It verifies an opt-in per-job accepted-result chain, NOT the new F4 fail-open per-run module/input/result chain.

SSE routes use GET /{pool}/v1/events and contributor GET /{pool}/v1/workers/{tabId}/tasks. The Last-Event-ID cursor is tested in internal/checksuite. Scope and HTTP status are enforced by the actual handler code, not by this index alone.
