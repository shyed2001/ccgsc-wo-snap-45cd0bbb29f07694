### 1. Prior-Art Comparison Table (14 Systems)

| System                                          | Unit of Work                               | API Shape                                         | Memory Model                                        | Scheduling                                          | Fault Handling                                    | Result Verification                                             | Fit (1-5) | What to Copy                                                                   |
| ----------------------------------------------- | ------------------------------------------ | ------------------------------------------------- | --------------------------------------------------- | --------------------------------------------------- | ------------------------------------------------- | --------------------------------------------------------------- | --------- | ------------------------------------------------------------------------------ |
| **Java ExecutorService / ForkJoinPool**         | `Runnable` / `Callable<T>`, `ForkJoinTask` | `submit()`, `invokeAll()`, `Future.get()`         | Shared memory (JVM heap, atomics)                   | Work-stealing (FJP) / bounded FIFO queue            | `ExecutionException`, task retry at app level     | None (trusted runtime)                                          | 4         | `Future<T>` semantics, task cancellation token, completion service draining    |
| **Python concurrent.futures + multiprocessing** | Function pointer + args                    | `submit()`, `map()`, `as_completed()`             | Isolated memory, IPC pickling                       | Worker process pool, OS queue                       | Process worker restart on SIGKILL/crash           | None (trusted local processes)                                  | 5         | Cleanest synchronous-feel API: `executor.map(fn, args)` + `as_completed()`     |
| **Rust rayon**                                  | Closure over slice/iterator                | `par_iter().map()`, `join()`                      | Shared memory, borrow checker + data-race freedom   | Work-stealing pool across OS threads                | Panic unwinding/propagation across join points    | None (trusted local)                                            | 3         | Data-parallel chunking ergonomics (`into_par_iter()`) for batch submission     |
| **OpenMP**                                      | Structured block / loop iteration          | `#pragma omp parallel for` directive              | Shared memory with explicit private/shared clauses  | Static/dynamic loop chunking by runtime             | Thread abort, no built-in fault tolerance         | None (trusted runtime)                                          | 2         | Chunking strategies (static, dynamic, guided) for mapping tasks to workers     |
| **MPI**                                         | Process rank rank-indexed task             | `MPI_Send/Recv`, `MPI_Scatter/Gather`             | Shared-nothing, explicit message passing            | Static rank allocation at launch                    | Process crash kills communicator (or ULFM)        | None (trusted cluster)                                          | 2         | Collective communication primitives (`scatter`/`gather` for partition/join)    |
| **Ray**                                         | Remote function / Actor class              | `@ray.remote`, `fn.remote()`, `ray.get()`         | Distributed object store (Plasma shared-memory)     | Centralized GCS + decentralized bottom-up leases    | Lineage reconstruction, actor restart             | None (trusted cluster)                                          | 4         | Actor abstraction for stateful WASM modules + future object references         |
| **Dask Distributed**                            | Python function / graph node               | `client.submit()`, `client.map()`, `Future`       | Distributed memory, serialized over TCP             | Dynamic centralized graph scheduler (work-stealing) | Task retry, worker failure reassignment           | None (trusted cluster)                                          | 4         | Dynamic DAG resolution and fine-grained future-state callbacks                 |
| **Celery**                                      | Task function + task ID                    | `delay()`, `apply_async()`, `AsyncResult`         | Shared-nothing, broker (RabbitMQ/Redis) storage     | Pre-fetch queues per worker via broker              | Ack/nack, redelivery on timeout                   | None (trusted nodes)                                            | 3         | Task status polling via result backend (`PENDING`, `SUCCESS`, `FAILURE`)       |
| **BOINC**                                       | Workunit (binary + inputs)                 | Declarative XML/DB work generation                | Shared-nothing, file-based input/output             | Server pulls/pushes based on host capability        | Re-issue workunit if deadline missed              | Redundant computation ($N$ replicates + majority quorum voting) | 5         | Quorum voting/redundant execution, volunteer credit, credit/reputation scoring |
| **Golem**                                       | Subtask / Docker payload                   | Provider market API, `Task` contract              | Shared-nothing, encrypted containerized volume      | Market negotiation, provider bid-matching           | Re-delegation upon provider dropout               | Proof of Computation / verification tests / SGX                 | 3         | Economic/trust incentives and challenge-based verification contracts           |
| **Web Worker Pools (workerpool, threads.js)**   | JS function / task message                 | `pool.exec(fn, args)`, `pool.proxy()`             | Isolated thread memory, `postMessage` copy/transfer | Client FIFO queue + idle worker routing             | Terminate crashed worker, spawn replacement       | None (trusted local browser)                                    | 4         | Worker lifecycle supervision, task transferables/buffers, `worker.terminate()` |
| **AWS Lambda / Serverless**                     | Stateless event payload (JSON)             | `invoke(Sync/Async)`, Step Functions              | Isolated container/microVM memory                   | Autonomous auto-scaling broker                      | Ephemeral retry (3x), dead-letter queues (DLQ)    | None (trusted infrastructure)                                   | 4         | Stateless chunk execution contract, idempotency tokens, execution time limits  |
| **Kubernetes Jobs**                             | Pod batch execution                        | `kind: Job`, completions/parallelism spec         | Container isolation, network overlay                | Master scheduler to matching node capacities        | Pod restart policy (`OnFailure`/`Never`), backoff | None (trusted nodes)                                            | 3         | Work-queue pattern with indexed completions (`indexed completion mode`)        |
| **GPU.js / WebGPU Compute**                     | Compute pipeline shader / kernel           | `createComputePipeline()`, `dispatchWorkgroups()` | Private/workgroup storage buffers, explicit maps    | Host GPU hardware dispatch queue                    | Device lost event (`requestDevice.lost`)          | None (trusted local device)                                     | 3         | ArrayBuffer staging + readback pattern for WASM SIMD/GPU tensor blocks         |

---

### 2. The 6 API Conventions Most Worth Copying

1. **`Future.result(timeout)` / Awaitable Resolution (from Python `concurrent.futures`)**
   * *Call:* `Fut = pool.submit(wasm_module_hash, input_json); res = Fut.result(timeout_ms=5000)`
   * *Why:* Universal, blocking/async-neutral primitive. Native code treats remote volunteer tabs exactly like a local thread pool without managing sockets.
2. **Batch Iterator `map(fn, iterable, chunksize)` (from Python `multiprocessing.Pool`)**
   * *Call:* `results = pool.map("matrix_mult.wasm", matrix_slices, chunksize=16)`
   * *Why:* Automatically chunks large workloads to minimize HTTPS round-trip overhead and keeps browser workers busy without saturating message queues.
3. **Completion Stream `as_completed(futures)` (from Java `ExecutorCompletionService` & Python)**
   * *Call:* `for fut in pool.as_completed(fut_list): handle(fut.result())`
   * *Why:* Highly critical for volunteer grids with high latency variance (stragglers). Lets native apps consume fast workers immediately without waiting for slowest node.
4. **Task Cancellation & Abort Token (from C# / Java `Future.cancel(mayInterruptIfRunning)`)**
   * *Call:* `token = pool.create_cancellation_token(); pool.submit(..., token=token); token.cancel()`
   * *Why:* Reclaims browser tabs when local search finishes early (e.g., speculative execution, alpha-beta pruning, early genetic search termination).
5. **Worker Warm Stateful Actor `bind_actor()` (from Ray)**
   * *Call:* `actor = pool.spawn_actor("model_eval.wasm", weights_url); res = actor.call("predict", sample)`
   * *Why:* Prevents downloading/instantiating heavy WASM modules per task. Keeps weights/lookup tables warm in browser memory across successive calls.
6. **Scatter / Gather Collectives (from MPI)**
   * *Call:* `pool.scatter_gather("filter.wasm", large_array_buffer, partition_count=100)`
   * *Why:* Simplifies large data-parallel slicing where client sends partitioned inputs and aggregates binary ArrayBuffers back over HTTP without writing custom chunking code.

---

### 3. Top 8 Risks for Volunteer Browser Pools & Fixes

1. **Untrusted / Malicious Results (Poisoning, Tampering)**
   * *Fix:* **$M$-of-$N$ Redundant Execution with Quorum Verification**. Dispatch identical tasks to 2 distinct volunteers; accept on byte-match hash. On mismatch, dispatch tie-breaker (BOINC model).
2. **High Churn / Instant Disconnection (Tab Closes, Navigation)**
   * *Fix:* **Heartbeat Leases + Automatic Task Re-queuing**. Workers lease tasks with a 10s deadline. If heartbeat expires before result postback, master re-assigns work to another pool worker.
3. **Throttled Background Tabs (Browser Timer & RAF Deprioritization)**
   * *Fix:* Run compute exclusively inside dedicated **Web Workers** (not the main DOM thread) and use WebSockets/WebTransport so the tab runtime avoids heavy CPU throttling.
4. **Mobile Battery Drain & Thermal Throttling**
   * *Fix:* Enforce **Client Compute Budgets via Battery API (`navigator.getBattery()`)**. Automatically refuse or suspend execution if unplugged or battery $<50\%$; throttle worker thread count to `hardwareConcurrency / 2`.
5. **Memory Exhaustion / Browser Out-of-Memory (OOM) Crashes**
   * *Fix:* Hard clamp **WASM Initial/Max Memory (`--max-memory`)** at compilation; monitor JS heap allocation via `performance.memory`; terminate and recycle worker instances between task batches.
6. **Stragglers Delaying Synchronous Jobs (Long-Tail Latency)**
   * *Fix:* **Speculative Redundant Execution (Tail Tolerance)**. If a worker hasn’t completed within $2\times$ the median task duration, spawn a duplicate on a fast worker; accept first result and cancel the straggler.
7. **WASM Non-Determinism (Float SIMD Differences across Architectures)**
   * *Fix:* Target **Strict Deterministic WASM Compilation Flags** (disable non-deterministic float traps, conform to canonical IEEE 754 representations, avoid browser-dependent math intrinsics) so verification hashes match.
8. **Egress / Data Bandwidth Saturation on Host**
   * *Fix:* **Content-Addressable Asset Caching (`CacheStorage` API)**. Store common WASM binaries and read-only datasets in browser cache indexed by SHA-256 hash; submit tasks only as compact delta parameters.
