<!-- Path: docs/T104_INTEGRATED_PAGE_PLAN.md | Author: GLM (Z.ai) for Claude Opus 5.5 head | 2026-11-21 (Dhaka)
What/why: T104 step 1 plan - one integrated contributor page. Merges /pools/contribute.html
(ContributeApp, new Web Thread Pool + Web Process Pool) and the root page
https://ccgsc-demo.digitalbd.org/ (T103 new-pools panel on top, old Prime/Matrix lane
router below) into one page with one worker budget and one status/log area.
Truth: docs/SESSION_TODO.md rows T103-T105, issue #117, vault AT20-14,
docs/decisions/GRILL_WWPP_R1.md (R29.1, R29.4, R30.1-3, R31.1, R39.1, R42.4-R42.6),
REVIEW_W7_gemini findings already applied in d464ab3d. Step 1 LIVE on the VPS in 9eb0cdc4 (2026-11-21, root worker.html sha fdb795b6; pre-T104 worker.html kept in /root/ccgsc_pools_core/archives/pre_9eb0_20261121T032512Z). -->

# T104 - One integrated contributor page (step 1 plan)

2026-11-21 (Dhaka). Owner rules for this page (T104, issue #117):

- Contributors never log in; a tab computes on visit. There is no Start button.
  `?autostart=0` stays the idle debug switch (T105).
- ONE worker budget per tab: the sum of the worker lanes of everything the tab runs
  (new pools + old lanes) never exceeds `navigator.hardwareConcurrency`; the auto
  default follows the existing rule (desktop cores-1 capped 4, phone 1 on battery /
  2 charging).
- Phones run light load: 1 worker, no GPU under 20 % battery.
- A Web Worker is a "web worker thread". There is no shared memory anywhere
  (R10.0); lanes coordinate through the page, never through a shared buffer.

## 1. What the old lane router does that ContributeApp does not

The old lane router is `startWorkerLanes` (`worker_lane_multi.mjs`, prime repo,
mounted by `prime/public/worker.html` below the T103 block). Reading the mounted
page, it owns six capabilities ContributeApp does not have:

1. **Multi-lane routing in one tab.** It starts every allowed lane at once - the
   Prime generic connector (`task_apps_connector.js`), the standalone `worker.js`
   lane, the Matrix ABI V2 lane, the generic-proof lane and the optional P2P-lite
   lane - selected by URL flags (`?family=matrix`, `?primeStandalone=1`,
   `?proofMode=1`, `?p2pLite=1`, `?lanes=all|multi`) with the legacy single-lane
   router (`startWorkerLane`) as the fallback for old URLs. ContributeApp runs
   exactly one lane: the new pools lease loop.
2. **A connector transport with device metadata.** The old lanes talk WebSocket to
   the Node coordinator through `WorkerPool.attach` (heartbeat 30 s, `open`/`close`/
   `message` events, `setMetaExtra` publishing laneMode/proofMode/pageFamily, a
   gateway-issued device id and tab id). ContributeApp has a REST lease loop
   against pools-core and no metadata channel.
3. **A lane status surface.** `createLaneStatusPanel` renders per-lane server
   health, this-tab assignment and connector state. ContributeApp renders only its
   own three badges (source, status, leader).
4. **Task-event reporting to the Director.** Every task event is POSTed to
   `/control/worker-events` (batched every 2 s, 50 per batch, queue capped at 500,
   `sendBeacon` on pagehide) and shown on the Director's Live Worker Logs page.
   ContributeApp writes only into its own log box.
5. **Refresh coordination.** The page handles `pool.refreshAll` (safe / deep / soft)
   and the safe mode skips the reload when a worker is busy, honouring the W7 hook
   `window.ccgscPoolsActiveTasks()` that the T103 bridge installs for the new pools.
6. **An app connector registry.** `task_apps_connector.js` registers connectors and
   feeds `window.ccgscReportTask(meta)` (the universal task console, WP4).

What ContributeApp has that the old lanes do not: server-signed task envelopes
verified inside the opaque null-origin sandbox before compile, the REST lease loop
with heartbeat/lease renewal, the anonymous no-login join with single-flight renewal
(T96B), verify statistics, and the T105 compute-on-visit behaviour.

## 2. The target single page

One page serves both entry points (`/` and `/pools/contribute.html`):

- **Top:** the new-pools panel (T103/T105). ContributeApp registers on visit,
  computes when `?autostart=0` is absent, keeps `?pools=0` as the new-pools off
  switch, and keeps the opaque sandbox runner iframe.
- **Below:** the old lanes (Prime/Matrix), unchanged transports, mounted by
  `startWorkerLanes` exactly as today.
- **One worker budget:** a shared `WorkerBudget` instance (this round:
  `web/pages/js/worker_budget_T104.js`). The new pools claim their CPU worker
  slots under owner `pools`; the old lanes claim theirs through the
  LegacyLaneAdapter under owner `legacy:<lane>`. `claim` grants only what is left,
  so the sum of all lanes never exceeds the tab budget; the auto default equals the
  current auto rule, so a single-lane tab keeps today's worker count exactly.
- **One status/log area:** a shared `StatusLog` instance
  (`web/pages/js/status_log_T104.js`) attached to the one log box. Sources write
  tagged lines (`pools`, `legacy:<lane>`) and per-source status chips; the element
  is written with `textContent` only.
- **GPU lanes** keep their existing guards (battery guard, adapter cap 2,
  GPU-first ordering). Folding GPU slots into the shared budget is step 8.
- Both entries share one persisted host id per browser profile (step 3), so the
  server sees one stable host whether the tab entered through `/` or
  `/pools/contribute.html`.

## 3. Ordered plan (max 10 steps)

Gemini's W7 target (persist hostId, dual pools tp+pp with ONE anonymous token,
LegacyLaneAdapter, artifact cache by sha256, one `startContributorPage()` entry)
maps to steps 3-7 below. Steps 1-5 are implemented in this round (GLM round 10,
account 3); steps 6-10 stay open.

| # | Step | Files | Status this round |
|---|---|---|---|
| 1 | Shared worker-budget module: `computeTabBudget`, `batteryAllowsGPU` (20 % battery rule), `WorkerBudget` (claim/release per owner, partial grants, invariant sum <= total). Auto default mirrors `computeWorkerCount`, so nothing changes until a second lane claims. | `web/pages/js/worker_budget_T104.js` (+ tests) | done |
| 2 | One status/log area API: `createStatusLog` ring buffer, per-source statuses, `attach` adopts existing lines, `textContent`-only rendering, subscribe. ContributeApp routes its `log()` through the shared instance when one is exposed and never writes the attached element directly. | `web/pages/js/status_log_T104.js` (+ tests) | done |
| 3 | Persist hostId: `readPersistedHostId` / `persistHostId` helpers in contribute.js. The write happens in the page bootstrap only (contribute.html auto-start block, root page wiring); ContributeApp itself only reads, so web storage is never written from the app and the anonymous token stays memory-only (T96 rule). | `web/pages/js/contribute.js`, `prime/public/worker.html` | done |
| 4 | Dual pools tp+pp with ONE anonymous token: pool registry (`resolvePools`, default `['tp']` keeps today's behaviour), one `/workers/anonymous` join reused for every pool (the worker routes gate on contributor role + worker scope only, `internal/api/api.go` `secure(p, "contributor", "worker", ...)`; `IssueAnonymous` carries no pool binding), per-pool tab ids from one register body, honest partial registration when a secondary pool refuses, round-robin claims over registered pools, per-pool heartbeat/results. | `web/pages/js/contribute.js` (+ tests) | done |
| 5 | LegacyLaneAdapter + root page wiring: `createLegacyLaneAdapter` gives the old lanes budget slots and status/log lines without touching their WebSocket; `prime/public/worker.html` exposes `window.ccgscSharedBudget`, `window.ccgscStatusLog` (attached to the T103 log box), `window.ccgscContributorHostId`, `window.ccgscLegacyLaneAdapter` and `window.ccgscLegacyLaneActiveTasks` (safe-refresh busy check), all best-effort so a down pools-core never breaks the old lanes. | `web/pages/js/legacy_lane_adapter_T104.js`, `prime/public/worker.html` (+ tests) | done |
| 6 | Artifact cache by sha256: an IndexedDB store keyed by the sha256 of the leased artifact, hash-on-load before compile, so repeat leases skip the fetch; the existing `wasmSha256` verify gate stays the authority. | `web/pages/js/artifact_cache_T104.js`, `web/pages/js/contribute.js` (or a cache module), runner untouched | done (2026-11-21, WO steps 6-10) |
| 7 | One `startContributorPage()` entry: one bootstrap that wires budget + status log + adapter, constructs ContributeApp (auto-start rules, `?autostart=0`) and mounts the lane router, replacing the two current boot paths; the T103 bridge keeps its opt-in gate until this lands. | new `web/pages/js/contribute_page_T104.js`, `prime/public/worker.html`, `pools_bridge.mjs` | done (2026-11-21, WO steps 6-10) |
| 8 | Fold GPU slots into the shared budget: budget total becomes `cpu + gpu` from `computeTabBudget(gpuSlots)`; the runner's GPU lanes claim under owner `pools:gpu`; phones keep the 20 % battery rule from `batteryAllowsGPU`. | budget module, runner, plan of R42.5/R42.6 | done (2026-11-21, WO steps 6-10) |
| 9 | Old lane router consumes the adapter: `worker_lane_multi.mjs` / `startWorkerLanes` (prime repo) read `window.ccgscLegacyLaneAdapter`, claim legacy slots around lane start/stop, and route lane logs and task-meta lines into the one status/log area; lane status panel keeps its own surface. | prime repo `worker_lane_multi.mjs`, `task_apps_connector.js` | partial: the page side is done (adapter + `ccgscLegacyLaneEnabled` gate + mount from the one entry); the router module itself lives in the prime repo and is not in this tree |
| 10 | Cutover: `/` and `/pools/contribute.html` serve the same integrated page; retire the duplicated worker-count control in favour of the shared budget split; keep `?autostart=0`, `?pools=0`, `?mock=1`; remove the old lane router script path only after the Director logs and lane status have moved. | root page, `web/pages/contribute.html` | done (2026-11-21, WO steps 6-10): both entries boot through `startContributorPage()`; the router script path is out of the root page and mounted by the entry; Director logs and the lane status panel are untouched and still live |

## 4. Constraints carried through every step

- W7 findings stay applied: single-flight `registerWorker`, `stop()` clears the
  claim/runner/token timers, the safe-refresh hook `window.ccgscPoolsActiveTasks`.
- T96B: anonymous renewal is single-flight and rate-limited; a 403 with an
  unchanged subject never re-registers (no bar evasion); claims back off
  2-60 s on 401/403.
- T105: no Start button anywhere; the tab computes on visit; `?autostart=0` keeps
  the tab registered and idle.
- R10.0: no SharedArrayBuffer, no shared WebAssembly.Memory; the budget coordinates
  lanes, it never shares memory.
- Every existing `web/pages` test and the `prime/test/client/pools_bridge.test.mjs`
  suite stay green; the pre-existing whitespace-only `runner.html` drift failure is
  the only allowed red.

## 5. What this round changed (summary)

New modules `worker_budget_T104.js`, `status_log_T104.js`,
`legacy_lane_adapter_T104.js` with their own test files; contribute.js gained the
persisted host id (read-only in the app), the dual-pool registry with one anonymous
token and round-robin claims, and the optional budget/status-log hooks (late-bound,
so standalone `contribute.html` behaves exactly as before); `prime/public/worker.html`
gained the additive T104 wiring block and the legacy-busy term in the safe-refresh
check. All details per file: `CHANGES.md` in the round zip; evidence: `REPORT.md`.

<!-- END OF docs/T104_INTEGRATED_PAGE_PLAN.md -->
