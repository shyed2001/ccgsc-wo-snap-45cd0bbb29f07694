<!-- File: CCGS_WASM/pools-core/docs/API_IDIOM_TABLE.md | Draft by mmx MiniMax-M3 for Claude Opus 5.5, 2026-11-17 (Dhaka) | input = Tier 0 slice of docs/decisions/GRILL_WWPP_R1.md + api/*.yaml | owner review pending -->

# API Idiom Table

2026-11-18 Codex web: applied DeepSeek + Copilot doc findings

## Purpose

This document maps common language concurrency idioms to the CCGSC pools-core REST APIs.

CCGSC exposes two pool APIs:

- Thread Pool API.
- Process Pool API.

There is no separate Threads API or Processes API. The mapping is conceptual. The document does not define adapter code.

## Idiom mapping

The source lists REST paths and canonical protobuf operation names. Operation IDs are the canonical OpenAPI/protobuf names.

| Language | Idiom | Matching pool | REST / gRPC call style | Operation ID |
|---|---|---|---|---|
| Python | `concurrent.futures` | Thread Pool | `submit` → create a job. Poll or consume results for completed jobs. `cancel` → cancel a job. Use SSE `task.done` for notifications; `GET /jobs/{jobId}/results:bulk` is the specified all-results contract after completion (currently returns 501 in the skeleton). `Executor.map` with `chunksize` maps to map + chunk. | `CreateJob, GetResultsBulk, CancelJob, StreamEvents` |
| Python | `multiprocessing` | Process Pool | `map` + `chunk` → create a process-pool session, submit tasks, and collect task results. Use a process-pool session lease for the session lifetime. `groupBy`/`pin` are contract fields only; the current Go engine does not implement their placement behavior. `apply_async` maps to single submit; `imap_unordered` maps to completion stream. | `OpenSession, StreamSessionTasks, CloseSession` |
| Java | `ExecutorService` | Thread Pool | `submit` → create a job. Use completion retrieval for `Future.get()` semantics. `cancel` → cancel a job. | `CreateJob, GetResults, CancelJob` |
| Java | `ForkJoin` | Thread Pool | `submit` and `map` + `chunk` → create jobs and request result data in bulk. Thread Pool has no task-group pinning field. | `CreateJob, GetResultsBulk` |
| C# | `Task` | Thread Pool | `submit` → create a job. Use completion retrieval for task completion semantics. `cancel` → cancel a job. | `CreateJob, GetResults, CancelJob` |
| C# | `Parallel.For` | Thread Pool | `map` + `chunk` → create jobs and request result data in bulk. Thread Pool has no task-group pinning field. | `CreateJob, GetResultsBulk` |
| Go | goroutines | Thread Pool | `submit` → create a job. Poll or consume results for completed jobs. `cancel` → cancel a job. | `CreateJob, GetResults, CancelJob` |
| Go | `errgroup` | Thread Pool | `submit with policy: fail-fast` → cancel sibling tasks on first error; consume `task.done` SSE or completed-job results. No Thread Pool task-group pinning field exists. `JoinSet`/`FuturesUnordered` map to `as_completed` via SSE or cursor polling. | `CreateJob, CancelJob, StreamEvents` |
| Rust | `rayon` | Thread Pool | `map` + `chunk` → create jobs and request result data in bulk. Thread Pool has no task-group pinning field. | `CreateJob, GetResultsBulk` |
| Rust | `tokio` | Thread Pool | `submit` → create a job. Poll or consume results for completed jobs. `cancel` → cancel a job. | `CreateJob, GetResults, CancelJob` |
| C++ | `std::async` | Thread Pool | `submit` → create a job. Use completion retrieval for future-like completion semantics. `cancel` → cancel a job. `cancel` maps conceptually to C++20 `stop_source`/`stop_token`, not `std::future`. | `CreateJob, GetResults, CancelJob` |
| C++ | thread pool | Thread Pool | `submit` and `map` + `chunk` → create jobs and request result data in bulk. | `CreateJob, GetResultsBulk` |
| Node.js | `worker_threads` | Thread Pool | `submit` → create a job. Poll or consume results for completed jobs. `cancel` → cancel a job. | `CreateJob, GetResults, CancelJob` |
| Node.js | `Promise.all` | Thread Pool | `submit` → create jobs; collect all results with the bulk results contract after job completion (currently 501 in the skeleton). This is aggregate `Promise.all` behavior, not completion order. | `CreateJob, GetResultsBulk` |
| PHP | `parallel` | Process Pool | `map` + `chunk` → create a process-pool session, submit tasks, and collect task results. Use a process-pool session lease for the session lifetime. `groupBy`/`pin` are contract fields only; the current Go engine does not implement their placement behavior. | `OpenSession, StreamSessionTasks, CloseSession` |

### Call-style meanings

| Call style | REST mapping |
|---|---|
| `submit` | Create one job through `POST /thread-pool/v1/jobs` or `POST /process-pool/v1/jobs`. |
| `map` + `chunk` | Set `batchSize` and `chunksize` in `POST /{pool}/v1/jobs`; retrieve bulk results via `GET /{pool}/v1/jobs/{jobId}/results:bulk`. |
| Completion notifications | SSE `task.done` events describe individual completions; `GET /jobs/{jobId}/results:bulk` is all-results retrieval after job completion (specified, currently 501). These are not a guarantee of Python `as_completed` ordering. |
| `cancel` | Cancel one job through `POST /thread-pool/v1/jobs/{jobId}:cancel` or `POST /process-pool/v1/jobs/{jobId}:cancel`. |
| Process-pool session lease | Create a session through `POST /process-pool/v1/sessions`, use `POST /process-pool/v1/sessions/{sessionId}/tasks`, then close the session through `POST /process-pool/v1/sessions/{sessionId}:close`. |
| Task-group pinning | Process Pool only: `groupBy` and `pin` belong to `POST /jobs` or `POST /sessions/{sessionId}/tasks` (OpenAPI contract; not wired in the skeleton). Thread Pool has no grouping field. |

## REST examples

### Thread Pool: submit one job

```http
POST /thread-pool/v1/jobs
Authorization: Bearer <jwt-token>
Content-Type: application/json

{"moduleArtifactIds":["<sha256>"],"tasks":[{"taskId":"t1","input":{"json":{}}}],"batchSize":1,"policy":"partial"}
```

The request body uses the thread-pool `JobCreateRequest` contract.

### Process Pool: create a session and submit a task

```http
POST /process-pool/v1/sessions
Authorization: Bearer <jwt-token>
Content-Type: application/json

{"tabCount":1,"hostCount":1,"ttlSeconds":900}
```

```http
POST /process-pool/v1/sessions/{sessionId}/tasks
Authorization: Bearer <jwt-token>
Content-Type: application/json

{"tasks":[{"taskId":"t1","input":{"json":{}}}],"batchSize":1}
```

The session and task bodies use the process-pool `SessionOpenRequest` and `SessionTasksRequest` contracts.

## Not supported

Shared memory is not supported.

Do not map idioms that require shared memory to the pools-core APIs. This restriction includes native thread-sharing models and MPI-style collectives (WWPP-R10.0, WWPP-R25.4).

## Source decisions and file paths

| Decision | Source | File path |
|---|---|---|
| Keep two pool APIs. Do not add separate Threads or Processes APIs. Publish an idiom table and examples. Do not add adapter code. | WWPP-R38.4 | `docs/decisions/GRILL_WWPP_R1.md` |
| Support the listed map-reduce, DAG, pipeline, and actor patterns. Do not support MPI collectives or shared memory. | WWPP-R25.4 | `docs/decisions/GRILL_WWPP_R1.md` |
| Thread Pool API paths | `WWPP-R18.2, WWPP-R32.1` pool API source | `api/thread_pool_v1.openapi.yaml` |
| Process Pool API paths | `WWPP-R18.2, WWPP-R32.1` pool API source | `api/process_pool_v1.openapi.yaml` |

<!-- mmx MiniMax-M3 in=2032 out=1394 -->
