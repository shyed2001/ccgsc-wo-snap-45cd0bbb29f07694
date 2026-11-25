# KEEPALIVE / HIDDEN-TAB AUDIT — CCGS_WASM worker fleet

Read-only search of `prime_distributed_projectPrlCc1B_V5_10K`: `public/*.js`, `public/*.html`,
`public/ccgsc_control/*.js`, `coordinator_server.js` (node_modules/build/dist skipped). Two rows cite
`public/matrix_app/*` — an adjacent dir outside the literal glob — because it holds the only Web Locks
and worker-owned heartbeat code in the tree. 18 mechanisms found.

| Method | file:line | What it does | Works in hidden tab? | Reuse for pools |
|---|---|---|---|---|
| WS pool heartbeat, main-thread timer | public/WorkerPool.js:425-431 | `setInterval` posts `pool.heartbeat` every 30 s on the pool WS; restarted on each reconnect (:484) | partly — socket stays open, but hidden-tab timers are throttled to >=1/min and the page is frozen after ~5 min, so the 30 s cadence dies | no — this is exactly what breaks hidden compute; it is pool presence, not lease renewal |
| Pool WS reconnect backoff | public/WorkerPool.js:263, 516-526 | exponential 1s->30s reopen on close | yes — `onclose` still fires; only the retry delay stretches | yes — the upkeep worker needs a self-healing connection |
| visibilitychange wake nudge `{t:'hb'}` | public/WorkerPool.js:488-498 (server side: server/pool_gateway.js:231-246) | on becoming visible sends a lite heartbeat; gateway re-expires the Redis TTL + lastSeen | no — fires only when the tab becomes visible; cannot sustain hidden compute | no — wrong polarity: it helps after wake, not during hiding |
| visibilitychange handler (worker page) | public/worker.html:469-481 | activity poke + pool meta `status:online`; its "wake-up nudge" at :479 evaluates `pool && pool` and discards it — a no-op | no | no — cosmetic; the real nudge is WorkerPool.js:488 |
| `beforeunload` -> `pool.unregister` | public/WorkerPool.js:536-540 | deregisters the tab on unload | n/a | no — the opposite of the goal; an upkeep worker must never unregister on hide |
| `pagehide` + `navigator.sendBeacon` flush | public/worker.html:401-407 | flushes the queued worker-event batch with sendBeacon so it survives unload | yes — survives the unload, does not keep the page alive | yes — reuse the pattern to surrender the lease at hide time |
| `keepalive:true` batched POST | public/worker.html:387; public/prime_segment_worker.js:385-388 | POST /control/worker-events with `keepalive:true`, batch 50 / 2 s | yes — keeps the *request* alive past page teardown | yes — same for the final result / surrender POST |
| Worker-thread lease heartbeat (SSPrime) | public/prime_segment_worker.js:283-308, 447, 505-511 | from the Web Worker: POST `/prime/lease/heartbeat` with held leaseIds every ttl/3, single in-flight promise, gate re-evaluated after each segment | yes — worker timers are throttled far less than the page's | yes — canonical reusable pattern: the worker owns the heartbeat, the page owns policy |
| Worker-thread lease heartbeat (Matrix) | public/matrix_app/matrix_compute_worker.js:162-186 | `matrix.startHeartbeat` -> worker `setInterval(ttl/3)` (max 30 s), reports heartbeatStarted/Ok/Failure | yes — same reason as above | yes — plus the worker/main-thread split with one decision owner |
| Main-thread lease heartbeat fallback | public/matrix_app/matrix_app.js:620-646 | legacy `setInterval` POST /matrix/lease/heartbeat when no Worker is available | no — main-thread timer, the exact bug I77-T4 fixed | no — keep only as a degraded fallback |
| Dead-lease recovery (abandon + re-lease) | public/matrix_app/matrix_app.js:435-444, 778-790 | on MX_ERR_STALE_LEASE / MX_ERR_LEASE_EXPIRED / UNKNOWN_LEASE / LEASE_NOT_ACTIVE / MX_LEASE_NOT_FOUND / LEASE_EXPIRED / MX_LEASE_EXPIRED: stop heartbeat, drop result, abandon and re-lease | yes — visibility-independent | yes — this is the lease-recovery contract to reuse |
| `stillWorking` sub-chunk pulse chain | public/computation_worker.js:59 -> public/task_apps_connector.js:794-810 -> coordinator_server.js:4534-4546 | worker emits a pulse every 100k numbers; main thread relays it; server resets `taskAssignedTime` so the run timeout never trips | partly — the pulse originates in the worker but the relay crosses the throttled main thread | yes, with the fix: the relay must not depend on a frozen page |
| Main-thread `stillWorking` pulse (15 s) | public/worker.js:135-166, 297-299 | standalone page pulses the coordinator every 15 s; memory update every 15 s | partly — throttled to >=1/min (still under the 300 s timeout) but stops dead once the tab is frozen | no — keep the pulse in the worker thread |
| Server WS ping/pong, staged stale | coordinator_server.js:188, 2175-2177, 3300-3345, 3398-3410 | 30 s server tick pings every socket; pong resets the liveness clock; misses escalate stale-online (300 s) -> stale-offline (1 h) -> purge (10 h) | yes — TCP-level, visibility-independent; but a frozen tab stops answering and its task is freed only after 300 s | yes — but the 300 s stale-online is the long tail the new design must beat |
| Server `surrenderTask` (early task return) | coordinator_server.js:2857-2958, 2959-2963, 3153, 5049 | requeues the held task NOW on a client surrender, restores dispatch budget, 60 s re-dispatch cooldown | n/a (server) | yes — reuse as the surrender protocol; only the client send is missing |
| Reconnect after coordinator restart | public/worker.js:182-217; public/task_apps_connector.js:152, 754 | reopens the coordinator WS 5 s after close with the same handlers | partly — the retry fires, but the delay stretches to >=1/min when hidden | yes — required for the upkeep worker |
| Web Locks (`navigator.locks`) | public/matrix_app/matrix_app.js:97-127 | `navigator.locks.request('ccgsc-worker-session', ...)` serializes POST /matrix/worker/session so one tab mints and the rest find the cookie | yes — a lock is not visibility-dependent | yes (primitive) — but the existing call holds the lock only across one fetch; an upkeep election needs a lock held open, exclusive for the upkeep role |
| Visibility-gated dashboard polling | public/director.html:2408-2418, 2748; public/ccgsc_control/pool_workers_panel.js:239-258; public/ccgsc_control/worker_events_control.js:170-209; public/progress_details.html:381-392 | polls are skipped unless `visibilityState==='visible'`; refresh once on visibilitychange | n/a (UI, not compute) | no — correct for dashboards, wrong for compute pools |
| NOT PRESENT anywhere in scope | — | `navigator.wakeLock`, service worker, audio/silent-oscillator trick, EventSource/SSE, page `freeze`/`resume` listeners, iframe + sandbox | — | gap: none of the four keeper mechanisms the new design needs exist in client code today |

## Gaps vs the five decisions

1. **Hidden tabs must keep computing.** Only two mechanisms survive hiding: the Web-Worker-owned lease
   heartbeats (public/prime_segment_worker.js:283-308, public/matrix_app/matrix_compute_worker.js:162-186).
   Everything else is a main-thread `setInterval` (WorkerPool.js:431, worker.js:135) that Chrome throttles
   to >=1/min and then freezes outright after ~5 min hidden, and the `stillWorking` relay still crosses the
   main thread (task_apps_connector.js:794), so a frozen tab stops refreshing `taskAssignedTime` and trips
   the 300 s run timeout. No code listens for the page `freeze` event, so the tab cannot hand work back
   before it is frozen — it only learns it is stale from the server sweep.
2. **1 upkeep worker on demo origin + compute workers in an opaque sandbox iframe.** Neither exists: zero
   `iframe`/`sandbox` hits in scope, and the topology today is many top-level tabs (worker.html), each
   self-registering as a pool worker with its own tabId (WorkerPool.js:300-333, :483). There is no upkeep
   role, no election, and no exclusivity — every tab claims upkeep duty. The standalone path also hardcodes
   `wss://ccgsc-demo.digitalbd.org/ws` (public/worker.js:195), bypassing the same-origin pool entirely.
3. **Web Lock + open connection.** `navigator.locks` appears exactly once in the whole client tree and only
   for session minting (matrix_app.js:123-126). No lock elects or serializes an upkeep worker, and the
   existing call releases as soon as its fetch resolves — the "hold the lock open for the page's life"
   pattern needed for a single keeper does not exist. The reconnect logic exists (worker.js:182-217) but is
   not tied to a lock, so two tabs can both open connections and both believe they are the keeper.
4. **Screen Wake Lock on phones.** Zero `navigator.wakeLock` calls anywhere in the repo. The option exists
   only as inert config (config/delivery_limits.json:35-37, 92-94, 154-156 and
   server/delivery/delivery_schema.mjs:264-361, shipped `true`) with no client consumer, so on a phone the
   device sleeps mid-batch and the compute stops with no client-side signal. Even once implemented it must be
   re-acquired on visibilitychange, and there is no freeze/resume handler to detect that the lock was lost.
5. **Lease recovery.** The server half is built and the client half is missing: `handleSurrenderTask`
   (coordinator_server.js:2857-2958, wired at :5049), `POST /prime/lease/surrender`
   (server/routes/prime.js:2227) and the Matrix dead-lease abandon/re-lease path (matrix_app.js:435-444,
   778-790) all exist, but no client in `public/` ever sends `surrenderTask` or calls `/prime/lease/surrender`
   (0 hits). A hidden or closed IsPrime tab therefore still waits out the 300 s Timeout300s sweep — the
   4-5 min tail recorded in the CHANGELOG. SSPrime has a heartbeat but no surrender client path at all, so
   its leases age out on the GC sweep instead of being returned immediately.
