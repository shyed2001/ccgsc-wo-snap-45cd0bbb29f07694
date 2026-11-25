# ADMIN_MANUALS_SOURCE — CCGSC Pool Admin + Monitor Manuals (one markdown source, 7 pages)

2026-11-18 Codex web: applied DeepSeek + Copilot doc findings

**File:** `F:/tmp/hcwork/ADMIN_MANUALS_SOURCE.md` (tmp only — source for the built manual pages)
**Built by:** hermes MiniMax-M3 for Claude Opus 5.5 / owner, 2026-11-17
**Inputs (read only):** `F:/tmp/hcwork/CCGSC_TWO_POOLS_DESIGN.md` (§9 auth/quotas/errors/versioning, §12 verification and tamper, §13 P2P and dew, §14 admin pages and nav) and `F:/tmp/hcwork/ctx_rows_R37_R40.md` (owner decisions = truth).
**Purpose of this file:** one markdown source that generates the 7 manual pages `/pools/manual/<page>.html` and the short manual section shown at the bottom of each admin page (WWPP-R40.3).
**Build rule:** every button and option on every page carries a legend tooltip (one line) and every page has a manual section plus this full manual page (WWPP-R38.5, WWPP-R40.3).

**Page set (7):** `monitor.html` (tier 1, brief combined) · `pool-monitor.html` (tier 2, medium pool monitor) · `api-monitor.html` (tier 2, medium API monitor for both APIs) · `thread-pool-admin.html` (tier 3, detail) · `process-pool-admin.html` (tier 3, detail) · `thread-api-admin.html` (tier 3, detail, `/thread-pool/v1`) · `process-api-admin.html` (tier 3, detail, `/process-pool/v1`)

**Column legend for every settings table:** `id` = setting key in the versioned config; `label` = the UI label; `legend` = the tooltip text shown on hover; `type`; `default` ( **TBD** = not decided by the owner); `range` = allowed values; `who may change` = `admin` (Pool Admin sets the pool default and limits; a `live` row is a runtime display with no value to change, so it lists `admin` as its owner) / `director` (Director role only, kept for the 3 server-app dashboards; the Director layer is superseded for pool settings, so no pool row carries it) / `dev-request` (developer may only request per job inside the admin allowance; an admin-off option is unavailable) (WWPP-R23.1, WWPP-R27.1, WWPP-R35.1); `WWPP id` = owner decision row.

**Global rules that apply to all 7 pages.**
- Apply and storage: versioned PostgreSQL config save, rollback and pin/unpin are exposed. Send expectedVersion for CAS (0 for initial write, stale 409); running jobs retain their selected config. See [R3E runtime contract](R3E_RUNTIME_CONTRACT.md).
- Retention and audit: keep the last 10 versions per admin page (the count is itself an admin setting), plus any version a running job uses, plus admin-named pins such as thesis runs; older versions are pruned and their audit rows stay in the security log, which is always on and cannot be turned off (WWPP-R38.7, WWPP-R25.1).
- Access and roles: Store A admin/director and Store B contributor/developer. Admin/monitor pages are gated by session, bearer or configured IP bypass; cookie writes require CSRF. RDP remains an external owner-machine policy. See [R3E runtime contract](R3E_RUNTIME_CONTRACT.md).
- Nav: one `nav.js` + `nav.json` on every host; links tagged public / contributor / dev / admin (WWPP-R38.2).

## 1. `monitor.html` — combined brief Monitor

**Purpose.** One very brief live page that shows both pools and both APIs at a glance with simple error and warning lines only.
Every number on it links to its matching settings section on the matching control page, and the full detail lives on the admin pages (WWPP-R38.5b, WWPP-R40.1).

**Sections:** `#capacity` Capacity · `#work` Work · `#trust` Trust · `#api-system` API + system in brief · `#alerts` Error/warning lines · `#manual` Manual section.

### 1.1 `#capacity` `#work` `#trust` — Capacity, work and trust numbers (WWPP-R40.1)

**Implementation status:** authenticated metrics, worker and config HTTP routes are implemented. Distinguish actual metrics from unavailable/mock values. Design cards do not prove a live measurement. See [R3E runtime contract](R3E_RUNTIME_CONTRACT.md).

| id | label | legend | type | default | range | who may change | WWPP id |
|---|---|---|---|---|---|---|---|
| mon.cap.contributors | Contributors | Registered contributor devices available to both pools | number (live) | live | live | admin | WWPP-R40.1 |
| mon.cap.tabs | Tabs | Registered tabs acting as wwHosts | number (live) | live | live | admin | WWPP-R40.1 |
| mon.cap.cpu_workers | CPU workers | Free compute web worker threads available in the thread pool | number (live) | live | live | admin | WWPP-R40.1 |
| mon.cap.gpu_workers | GPU workers | Tabs reporting a usable WebGPU adapter | number (live) | live | live | admin | WWPP-R40.1 |
| mon.cap.free_slots_thread | Free slots — thread pool | Unleased task slots in the Web Thread Pool | number (live) | live | live | admin | WWPP-R40.1 |
| mon.cap.free_slots_process | Free slots — process pool | Unleased tab slots in the Web Process Pool | number (live) | live | live | admin | WWPP-R40.1 |
| mon.work.running | Jobs running | Jobs currently leased and not finished | number (live) | live | live | admin | WWPP-R40.1 |
| mon.work.queued | Jobs queued | Jobs admitted and waiting for a slot | number (live) | live | live | admin | WWPP-R40.1 |
| mon.work.failed | Jobs failed | Jobs that ended in failure state | number (live) | live | live | admin | WWPP-R40.1 |
| mon.work.tasks_per_s | Tasks/s | Settled tasks per second across both pools | number (live) | live | live | admin | WWPP-R40.1 |
| mon.work.avg_task_time | Avg task time | Use actual metric samples; result durationMs placeholder is not measured elapsed time | duration (live) | live | live | admin | WWPP-R40.1 |
| mon.trust.hash_fails | Hash fails | Results rejected because the recomputed SHA-256 differed | number (live) | live | live | admin | WWPP-R40.1 |
| mon.trust.rerun_mismatch | Re-run mismatches | Sampled re-runs whose digests disagreed | number (live) | live | live | admin | WWPP-R40.1 |
| mon.trust.canary_fails | Canary fails | Failed hidden known-answer tasks | number (live) | live | live | admin | WWPP-R40.1 |
| mon.trust.quarantined | Quarantined | Tasks held in the poison-task quarantine | number (live) | live | live | admin | WWPP-R40.1 |
| mon.trust.banned | Banned | Workers banned by the reputation score | number (live) | live | live | admin | WWPP-R40.1 |
| mon.trust.window_hours | Trust window | Rolling window used by every trust counter on this page | number (hours) | 24 | 1-168 | admin | WWPP-R40.1 |

### 1.2 `#api-system` `#alerts` — API, system and alert settings (WWPP-R40.1)

| id | label | legend | type | default | range | who may change | WWPP id |
|---|---|---|---|---|---|---|---|
| mon.api.req_per_min | req/min | API requests per minute across both pool APIs | number (live) | live | live | admin | WWPP-R40.1 |
| mon.api.4xx | 4xx | Client-error responses in the shown window | number (live) | live | live | admin | WWPP-R40.1 |
| mon.api.5xx | 5xx | Server-error responses in the shown window | number (live) | live | live | admin | WWPP-R40.1 |
| mon.api.429 | 429 | Rate-limit rejections, each carrying Retry-After | number (live) | live | live | admin | WWPP-R40.1 |
| mon.api.p95 | p95 | 95th percentile request latency over the chosen window | duration (live) | live | live | admin | WWPP-R40.1 |
| mon.sys.cpu | Server CPU | CPU load of the pool server | percent (live) | live | live | admin | WWPP-R40.1 |
| mon.sys.ram | Server RAM | Memory use of the pool server | percent (live) | live | live | admin | WWPP-R40.1 |
| mon.sys.disk | Server disk | Disk use of the pool server | percent (live) | live | live | admin | WWPP-R40.1 |
| mon.sys.pg_conns | PG connections | Open PostgreSQL connections | number (live) | live | live | admin | WWPP-R40.1 |
| mon.sys.sse_conns | SSE connections | Open server-sent event streams | number (live) | live | live | admin | WWPP-R40.1 |
| mon.alert.level | Warning level | Lowest severity that still prints an error or warning line | enum | warn | info/warn/error | admin | WWPP-R38.5b |
| mon.alert.refresh | Refresh interval | Seconds between automatic refreshes of the brief numbers | number (s) | 10 | 5-300 | admin | WWPP-R38.5b |
| mon.alert.auto | Auto refresh | Keeps the brief page current; off leaves the page manual | toggle | on | on/off | admin | WWPP-R38.5b |
| mon.link.numbers | Number links | Every displayed number links to its matching settings section | toggle | on | on/off | admin | WWPP-R40.1 |
| mon.link.target | Link target | Where a linked number opens the matching section | enum | same-tab | same-tab/new-tab | admin | WWPP-R40.1 |
| mon.p95.window | p95 window | Window used for the p95 latency number | enum | 5m | 1m/5m/15m | admin | WWPP-R40.1 |
| mon.metrics.read | Metrics read | Authenticated live metrics endpoint; unavailable values explicitly identified | toggle | on | on/off | admin | WWPP-R37.2 |

**`#manual` section.** Brief page only: numbers, no settings beyond display. Click any number to open the matching section on the control pages — capacity on the pool pages, work on the pool pages, trust on the verification sections, API and system on the API pages (WWPP-R40.1, WWPP-R40.3).

---

## 2. `pool-monitor.html` — medium Pool Monitor

**Purpose.** The medium monitor for both pools: bigger error messages, dashboards and control options in one place.
Every section links to its matching section on the pool admin pages, where the full detail and the settings live (WWPP-R38.5b).

**Sections:** `#capacity` Capacity dashboard · `#work` Work dashboard · `#trust` Trust dashboard · `#devices` Devices and workers · `#queue` Queue and backlog · `#errors` Error board · `#control` Control options · `#manual` Manual section.

| id | label | legend | type | default | range | who may change | WWPP id |
|---|---|---|---|---|---|---|---|
| pm.cap.refresh | Capacity refresh | Seconds between refreshes of the capacity dashboard | number (s) | 10 | 5-300 | admin | WWPP-R38.5b |
| pm.cap.slot_detail | Slot detail | Shows free and busy slots per pool, not only the totals | toggle | on | on/off | admin | WWPP-R38.5b |
| pm.work.window | Work window | Time window used by the work dashboard charts | enum | 1h | 15m/1h/24h/7d | admin | WWPP-R38.5b |
| pm.work.tasks_per_s | Tasks/s | Settled tasks per second over the chosen window | number (live) | live | live | admin | WWPP-R40.1 |
| pm.work.avg_task_time | Avg task time | Use actual metric samples; result durationMs placeholder is not measured elapsed time | duration (live) | live | live | admin | WWPP-R40.1 |
| pm.trust.hash_fails | Hash fails | Rejected results whose recomputed digest differed | number (live) | live | live | admin | WWPP-R40.1 |
| pm.trust.rerun_mismatch | Re-run mismatches | Sampled re-runs whose digests disagreed | number (live) | live | live | admin | WWPP-R40.1 |
| pm.trust.canary_fails | Canary fails | Failed hidden known-answer tasks | number (live) | live | live | admin | WWPP-R40.1 |
| pm.trust.banned | Banned | Workers banned by the reputation score | number (live) | live | live | admin | WWPP-R40.1 |
| pm.dev.workers_per_tab | Workers per tab shown | Shows the worker count each registered tab reports | toggle | on | on/off | admin | WWPP-R29.4 |
| pm.dev.device_class | Device class shown | Groups tabs by phone, laptop, desktop or owner node | toggle | on | on/off | admin | WWPP-R10.4 |
| pm.dev.gpu_reported | GPU capable tabs | Shows tabs that report a usable WebGPU adapter | toggle | on | on/off | admin | WWPP-R33.1 |
| pm.queue.depth | Queue depth | Tasks waiting for a slot in the shown pool | number (live) | live | live | admin | WWPP-R40.1 |
| pm.queue.rejected | Rejected | Tasks rejected by the bounded-queue policy | number (live) | live | live | admin | WWPP-R40.1 |
| pm.err.banner | Error banner size | Medium tier prints larger error messages than the brief page | enum | large | compact/large | admin | WWPP-R38.5b |
| pm.err.leases_expiring | Leases expiring | Shows leases close to expiry and their renewal state | toggle | on | on/off | admin | WWPP-R18.2 |
| pm.ctl.pause_scheduling | Pause scheduling | Design-only; no verified runtime handler | toggle | off | on/off | admin | WWPP-R27.1 |
| pm.ctl.requeue_failed | Requeue failed tasks | Re-queues failed tasks that are still inside the retry policy | toggle | off | on/off | admin | WWPP-R32.9 |
| pm.ctl.clear_quarantine | Clear quarantine | Design-only; no verified runtime handler | button | — | — | admin | WWPP-R32.9 |
| pm.ctl.requests | Developer requests | Shows per-job developer requests filed against the pool defaults | toggle | on | on/off | dev-request | WWPP-R23.1 |

**`#manual` section.** The medium pool monitor shows the same numbers as the brief Monitor in bigger form, adds dashboards and control options, and links every section to the pool admin pages. Control options here act on live state; the durable settings live on the admin pages (WWPP-R38.5b, WWPP-R40.3).

---

## 3. `api-monitor.html` — medium API Monitor (both APIs)

**Purpose.** The medium monitor for the two pool APIs together: bigger error messages, dashboards and control options for traffic, keys, quotas and sessions.
Each card links to the matching section of the Thread Pool API admin page or the Process Pool API admin page (WWPP-R38.5b).

**Sections:** `#traffic` Traffic · `#errors` Error board · `#keys` Key activity · `#quotas` Quota usage · `#sessions` Sessions · `#system` Server and database · `#control` Control options · `#manual` Manual section.

| id | label | legend | type | default | range | who may change | WWPP id |
|---|---|---|---|---|---|---|---|
| am.scope | API scope | Which of the two pool APIs the monitor follows | enum | both | thread/process/both | admin | WWPP-R38.5b |
| am.traffic.refresh | Traffic refresh | Seconds between refreshes of the traffic cards | number (s) | 10 | 5-300 | admin | WWPP-R38.5b |
| am.traffic.req_per_min | req/min | Requests per minute per API | number (live) | live | live | admin | WWPP-R40.1 |
| am.traffic.p95 | p95 | 95th percentile request latency over the chosen window | duration (live) | live | live | admin | WWPP-R40.1 |
| am.traffic.window | p95 window | Window used for the latency card | enum | 5m | 1m/5m/15m | admin | WWPP-R40.1 |
| am.err.banner | Error banner size | Medium tier prints larger error messages than the brief page | enum | large | compact/large | admin | WWPP-R38.5b |
| am.err.4xx | 4xx | Client-error responses in the window | number (live) | live | live | admin | WWPP-R40.1 |
| am.err.5xx | 5xx | Server-error responses in the window | number (live) | live | live | admin | WWPP-R40.1 |
| am.err.429 | 429 | Rate-limit rejections with Retry-After | number (live) | live | live | admin | WWPP-R40.1 |
| am.err.codes | Error codes | Shows the RFC 9457 code list with counts per code | toggle | on | on/off | admin | WWPP-R32.8 |
| am.keys.activity | Key activity | Shows recent issuance, approval and revocation of API keys | toggle | on | on/off | admin | WWPP-R38.3 |
| am.keys.revoked | Revoked keys | Shows keys that are revoked and their deny-list state | toggle | on | on/off | admin | WWPP-R32.4 |
| am.quota.usage | Quota usage | Shows per-key running-job, queued-task and request-per-minute usage | toggle | on | on/off | admin | WWPP-R32.5 |
| am.quota.warn | Quota warnings | Warns when a key approaches one of its quotas | toggle | on | on/off | admin | WWPP-R32.5 |
| am.quota.credits | Credit metering | Shows metered CPU-seconds and GPU-seconds per key | toggle | on | on/off | admin | WWPP-R32.5 |
| am.sessions.list | Session monitor | Shows process-pool sessions, leased tabs and expiry | toggle | on | on/off | admin | WWPP-R38.5b |
| am.sys.card | System card | Shows server CPU, RAM, disk, PG connections and SSE connections | toggle | on | on/off | admin | WWPP-R40.1 |
| am.sys.pg_conns | PG connections | Open PostgreSQL connections | number (live) | live | live | admin | WWPP-R40.1 |
| am.sys.sse_conns | SSE connections | Open server-sent event streams | number (live) | live | live | admin | WWPP-R40.1 |
| am.ctl.pause_ingress | Pause ingress | Design-only; no verified runtime handler | toggle | off | on/off | admin | WWPP-R32.5 |
| am.ctl.revoke_key | Revoke key | Revokes a key from the key activity card | button | — | — | admin | WWPP-R32.4 |

**`#manual` section.** The API monitor covers both pool APIs on one page. It is read-mostly: the cards show what is happening and the control options act immediately, while quotas, keys, metering and error-model settings are saved on the two API admin pages (WWPP-R38.5b, WWPP-R40.3).

---

## 4. `thread-pool-admin.html` — Thread (WW) Pool admin

**Purpose.** Full detail and every setting of the Web Thread Pool, where the unit of work is one task on any web worker thread of any registered tab.
The Pool Admin allows options, sets limits and sets the pool default; the developer only requests per job inside the allowance (WWPP-R38.5, WWPP-R27.1).

**Sections:** `#general` General · `#workers` Workers per tab · `#guards` Device guards · `#gpu` GPU · `#scheduling` Scheduling and queue · `#verification` Verification and tamper · `#p2p` P2P · `#dew` Dew and late results · `#metrics` Metrics · `#config` Config versions · `#manual` Manual section.

### 4.1 `#general` `#workers` `#guards` `#gpu` — Pool, workers, device guards and GPU

| id | label | legend | type | default | range | who may change | WWPP id |
|---|---|---|---|---|---|---|---|
| tp.gen.enabled | Pool enabled | Master switch; off stops new task leasing in this pool | toggle | on | on/off | admin | WWPP-R27.1 |
| tp.gen.name | Pool name | Display name shown in nav, monitors and API responses | text | Web Thread Pool | 1-64 chars | admin | WWPP-R38.5 |
| tp.gen.fetch_mode | Same-device fetch mode | Leader tab per device or every tab fetches on its own; inefficient use always warns | enum | leader | leader/each-tab | dev-request | WWPP-R22.1, WWPP-R23.2 |
| tp.gen.shareable | Shareable outputs | Lets a developer mark outputs shareable; the admin may disable it for the pool | toggle | on | on/off | admin | WWPP-R23.4 |
| tp.gen.group_by | groupBy default | Default task partitioning key applied when the developer gives none | enum | none | none/label/device-class | admin | WWPP-R32.2 |
| tp.wrk.per_tab | Workers per tab | Compute web worker threads per tab; auto means logical cores minus one | enum | auto | auto/1/2/3/4 | admin | WWPP-R29.4 |
| tp.wrk.min | Worker range minimum | Lowest number of compute workers this pool allows per tab | number | 1 | 1-4 | admin | WWPP-R29.4 |
| tp.wrk.max | Worker range maximum | Highest number of compute workers per tab; the server admin may raise it where more cores exist | number | 4 | 1-32 | admin | WWPP-R29.4, WWPP-R23.1 |
| tp.wrk.phones | Phone worker count | Workers per tab on phones | number | 1 | 1-2 | admin | WWPP-R10.4 |
| tp.wrk.laptops | Laptop worker count | Workers per tab on laptops | number | 2 | 1-4 | admin | WWPP-R10.4 |
| tp.wrk.desktops | Desktop worker count | Workers per tab on desktops | number | 4 | 1-8 | admin | WWPP-R10.4 |
| tp.wrk.owner | Owner node worker count | Workers per tab on owner nodes | enum | auto | auto/1-32 | admin | WWPP-R10.4, WWPP-R29.4 |
| tp.wrk.phone_battery | Phone workers on battery | Workers per tab while the phone runs on battery | number | 1 | 1-2 | admin | WWPP-R30.2 |
| tp.wrk.phone_charging | Phone workers charging | Workers per tab while charging with the screen on | number | 2 | 1-4 | admin | WWPP-R30.2 |
| tp.wrk.phone_stop | Stop below battery | Work stops below this battery level | percent | 20 | 5-50 | admin | WWPP-R30.2 |
| tp.gd.cpu | CPU profile | Contributor CPU effort profile the server asks for | enum | medium | low/medium/full | admin | WWPP-R30.1 |
| tp.gd.backoff | Auto back-off | Drops worker effort only when the device is very very busy | toggle | on | on/off | admin | WWPP-R30.1 |
| tp.gd.battery | Battery rule | Applies the phone battery and heat rules where the device supports them | toggle | on | on/off | admin | WWPP-R30.1 |
| tp.gd.memory | Memory cap per worker | Upper memory bound asked of each worker | number (MiB) | TBD | 1-4096 | admin | WWPP-R30.1 |
| tp.gd.hidden_tab | Pause on hidden tab | Deliberately off: hidden and background tabs must keep working | toggle | off | off only | admin | WWPP-R30.1, WWPP-R39.1 |
| tp.gpu.enabled | GPU tasks | Allows jobs to declare cpu, gpu or gpu-preferred tasks in this pool | toggle | on | on/off | admin | WWPP-R33.1 |
| tp.gpu.time_limit | GPU task time limit | Maximum runtime of one GPU task | number (s) | TBD | 1-3600 | admin | WWPP-R33.1 |
| tp.gpu.memory_limit | GPU memory limit | Maximum GPU memory one task may request | number (MiB) | TBD | 1-8192 | admin | WWPP-R33.1 |
| tp.gpu.charging_only | GPU only while charging | GPU tasks are eligible only when the device is charging | toggle | on | on/off | admin | WWPP-R33.1 |

### 4.2 `#scheduling`

| id | label | legend | type | default | range | who may change | WWPP id |
|---|---|---|---|---|---|---|---|
| tp.sch.bounded_queue | Bounded queue | Caps the admitted queue depth; the queue never grows without limit | toggle | on | on/off | admin | WWPP-R32.5 |
| tp.sch.rejection | Queue rejection policy | What happens when the bounded queue is full | enum | TBD | reject-429/block-timeout/drop-oldest/caller-runs | admin | WWPP-R25.3 |
| tp.sch.priority_high | High priority allowed | Lets the admin allow the high priority class in this pool | toggle | off | on/off | admin | WWPP-R25.2 |
| tp.sch.fair_share | Fair share | Shares pool capacity fairly between developers | toggle | on | on/off | admin | WWPP-R32.5 |
| tp.sch.batch_size | Batch size per device class | Default task batch size handed to a device class | number | TBD | 1-1000 | admin | WWPP-R34.1 |
| tp.sch.small_task | Small-task warning | Tasks below this size raise an efficiency warning on all four surfaces | number (tasks) | TBD | 1-100 | admin | WWPP-R24.4 |

### 4.3 `#verification` — all four tamper measures (WWPP-R37.1b)

| id | label | legend | type | default | range | who may change | WWPP id |
|---|---|---|---|---|---|---|---|
| tp.ver.hash_check | Hash check on every load | Verifies artifact and input SHA-256 on every load, including IndexedDB cache hits | toggle | on | on/off | admin | WWPP-R37.1, WWPP-R37.1b |
| tp.ver.rerun_percent | Result re-run rate | Percentage of accepted results re-run on another worker; exact hashes only | percent | 1 | 0-100 | dev-request | WWPP-R35.1 |
| tp.ver.signed_envelope | Signed task envelope | Server signs each envelope with Ed25519 over job and task ids, artifact and input hashes and the lease | toggle | on | on/off | admin | WWPP-R37.1, WWPP-R37.1b |
| tp.ver.canary | Canary known-answer tasks | Schedules hidden known-answer tasks to test worker trustworthiness | toggle | on | on/off | admin | WWPP-R37.1, WWPP-R37.1b |
| tp.ver.canary_warn | Canary warn threshold | Reputation score at or below which the worker is warned | number | 75 | 0-100 | admin | WWPP-R37.1, WWPP-R39.1 |
| tp.ver.canary_bar | Canary bar threshold | Reputation score at or below which the worker is barred from tasks | number | 50 | 0-100 | admin | WWPP-R37.1, WWPP-R39.1 |
| tp.ver.canary_ban | Canary ban threshold | Reputation score at or below which the worker is banned | number | 0 | 0-100 | admin | WWPP-R37.1, WWPP-R39.1 |
| tp.ver.reputation_recovery | Reputation recovery | A ban is lifted by admin review; there is no automatic recovery | enum | manual-review | manual-review/auto | admin | WWPP-R37.1, WWPP-R39.1 |
| tp.ver.mode | Verification mode | (a) server mechanisms plus developer policy, or (b) developer only with results labelled unverified | enum | a | a/b | admin | WWPP-R24.1 |
| tp.ver.k_of_n | k-of-n checks | Independent checks that must agree before a result counts as verified | number | TBD | 1-5 | admin | WWPP-R24.1 |
| tp.ver.local_after_download | Verify after download | Lets developers verify results locally after download | toggle | on | on/off | dev-request | WWPP-R24.1 |
| tp.ver.evidence | Tamper evidence | (a) server minimal, security log plus SHA-256 of every accepted result, or (b) everything opt-in | enum | a | a/b | admin | WWPP-R35.2 |
| tp.ver.chain | Audit hash chain | Per-job hash chain written by the server; built and tested even while off | toggle | off | on/off | admin | WWPP-R26.2 |
| tp.ver.merkle | Global Merkle checkpoints | Adds periodic Merkle roots over the tips of job chains | toggle | off | on/off | admin | WWPP-R22.3 |
| tp.ver.chain_authority | Chain authority | (a) admin global Merkle on/off with developer raise-only, or (b) developer free choice inside the allowance | enum | TBD | a/b | admin | WWPP-R23.3 |

### 4.4 `#p2p` — P2P default ON (WWPP-R36.2-RES)

| id | label | legend | type | default | range | who may change | WWPP id |
|---|---|---|---|---|---|---|---|
| tp.p2p.enabled | P2P sharing | Design target; runtime capability false pending F11 | toggle | off (runtime) | on/off | admin | WWPP-R36.2, WWPP-R36.2-RES |
| tp.p2p.peer_scope | Peer selection scope | Which peers may serve artifacts: same device, same phone, same LAN or very close proximity | enum | same-lan | same-device/same-phone/same-lan/near | admin | WWPP-R9.1 |
| tp.p2p.task_handoff | Task hand-off | Historical P2P scope, kept as a setting; not an active peer data flow | toggle | off | on/off | admin | WWPP-R9.1, WWPP-R36.2 |
| tp.p2p.result_relay | Result relay over P2P | Historical P2P scope, kept as a setting; results are never relayed over P2P | toggle | off | on/off | admin | WWPP-R9.1, WWPP-R36.2, WWPP-R39.1 |
| tp.p2p.rtt_probe | RTT probe step | Uses round-trip time in peer ordering once the probe exists | toggle | off | on/off | admin | WWPP-R14.1 |
| tp.p2p.fallback | CDN fallback | Falls back to server or CDN when no peer can serve the artifact | toggle | on | on/off | admin | WWPP-R36.2 |

### 4.5 `#dew` — dew ON, 24 h window (WWPP-R36.2)

| id | label | legend | type | default | range | who may change | WWPP id |
|---|---|---|---|---|---|---|---|
| tp.dew.enabled | Dew | Offline signed receipts; runtime default enabled, 24h; explicit false disables; F12 evidence pending | toggle | on | on/off | admin | WWPP-R36.2 |
| tp.dew.window | Dew receipt window | How long a signed dew receipt stays valid | hours | 24 | 1-72 | admin | WWPP-R36.2 |
| tp.dew.late_policy | Late-result policy | (a) archived lease id and attempt match, (b) accept only if no other attempt reported, (c) drop after re-lease | enum | a | a/b/c | admin | WWPP-R13.2 |
| tp.dew.max_attempts | Max attempts | Attempts allowed for one task before it fails | number | 3 | 1-10 | admin | WWPP-R32.9 |
| tp.dew.job_policy | Job failure policy | What the job does when a task cannot complete | enum | partial | partial/fail-fast/threshold | admin | WWPP-R32.9 |
| tp.dew.quarantine | Quarantine threshold | Worker crashes before a poison task is quarantined | number | 3 | 1-10 | admin | WWPP-R32.9 |
| tp.dew.dead_letter | Dead-letter store | Keeps late results that arrive after the job ended, with no second credit | toggle | on | on/off | admin | WWPP-R32.9 |

### 4.6 `#metrics` and `#config`

| id | label | legend | type | default | range | who may change | WWPP id |
|---|---|---|---|---|---|---|---|
| tp.met.light | Light metrics | Per-feature timings and counters in one PostgreSQL metrics table; disabled intervals are recorded, never fabricated | toggle | on | on/off | admin | WWPP-R37.2 |
| tp.met.detail | Metrics detail | How much per-feature detail the metrics table keeps | enum | TBD | light/standard/full | admin | WWPP-R37.2 |
| tp.met.benchmark | Thesis benchmark harness | Heavy harness for owner-started thesis runs beside the MVP | toggle | on | on/off | admin | WWPP-R37.2 |
| tp.cfg.retention | Version retention | Number of config versions kept for this admin page | number | 10 | 1-100 | admin | WWPP-R38.7 |
| tp.cfg.pin | Pin version | Admin-named pins, for example a thesis run, keep a version from pruning | text | — | 1-64 chars | admin | WWPP-R38.7 |
| tp.cfg.reason | Reason required | Each Save records who, when, what changed and why | toggle | on | on/off | admin | WWPP-R38.6 |
| tp.cfg.rollback | Rollback | One-click rollback copies an earlier version into a new audited version | button | — | — | admin | WWPP-R38.6 |

**`#manual` section.** The thread pool admin page owns the pool default and the limits for the Web Thread Pool: workers per tab, device guards, GPU, scheduling, all four tamper measures, P2P, dew, metrics and config versions. Saving creates a version; new jobs use it at once and running jobs keep theirs (WWPP-R38.6, WWPP-R40.3).

---

## 5. `process-pool-admin.html` — Process (wwHost) Pool admin

**Purpose.** Full detail and every setting of the Web Process Pool, where the unit of work is one tab acting as a wwHost holding many worker threads.
Same authority rule as the thread pool: the Pool Admin sets pool defaults and limits, developers only request inside the allowance (WWPP-R38.5, WWPP-R27.1).

**Sections:** `#general` General · `#shape` Pool shapes and sessions · `#workers` Workers and hosts · `#guards` Device guards · `#gpu` GPU · `#scheduling` Scheduling and queue · `#verification` Verification and tamper · `#p2p` P2P · `#dew` Dew and late results · `#metrics` Metrics · `#config` Config versions · `#manual` Manual section.

| id | label | legend | type | default | range | who may change | WWPP id |
|---|---|---|---|---|---|---|---|
| pp.gen.enabled | Pool enabled | Master switch; off stops new session and job leasing in this pool | toggle | on | on/off | admin | WWPP-R27.1 |
| pp.gen.name | Pool name | Display name shown in nav, monitors and API responses | text | Web Process Pool | 1-64 chars | admin | WWPP-R38.5 |
| pp.gen.fetch_mode | Same-device fetch mode | Leader tab per device or every tab fetches on its own; inefficient use always warns | enum | leader | leader/each-tab | dev-request | WWPP-R22.1, WWPP-R23.2 |
| pp.gen.shareable | Shareable outputs | Lets a developer mark outputs shareable; the admin may disable it for the pool | toggle | on | on/off | admin | WWPP-R23.4 |
| pp.shape.default | Default pool shape | (a) tab session lease, or (b) task-group pinning to one tab | enum | TBD | a/b | dev-request | WWPP-R19.1 |
| pp.shape.session_minutes | Session lease duration | Minutes a leased session stays warm before it is released | number (min) | TBD | 1-1440 | admin | WWPP-R19.1 |
| pp.shape.tabs_per_session | Tabs per session | Tabs leased for one session | number | TBD | 1-64 | admin | WWPP-R19.1 |
| pp.shape.hosts_per_session | Hosts per session | Distinct registered devices per session; one up to the tab count | number | TBD | 1-64 | admin | WWPP-R39.1 |
| pp.shape.warm_initializer | Warm initializer | Artifact preloaded into the session tab before tasks arrive | toggle | off | on/off | admin | WWPP-R19.1 |
| pp.shape.initializer_state | Initializer state | Default state of the warm initializer between sessions | enum | TBD | keep/discard | admin | WWPP-R25.3 |
| pp.shape.close_policy | Session close | What happens to the tabs on close, expiry or error | enum | release | release/drain | admin | WWPP-R19.1 |
| pp.shape.lease_store | Lease store | PG default; optional Redis lease coordination with PG settlement | enum | pg | pg/redis+pg | admin | WWPP-R18.4, WWPP-R19.3 |
| pp.wrk.per_tab | Workers per tab | Compute web worker threads inside each wwHost tab | enum | auto | auto/1/2/3/4 | admin | WWPP-R29.4 |
| pp.wrk.min | Worker range minimum | Lowest number of compute workers this pool allows per tab | number | 1 | 1-4 | admin | WWPP-R29.4 |
| pp.wrk.max | Worker range maximum | Highest number of compute workers per tab; the server admin may raise it where more cores exist | number | 4 | 1-32 | admin | WWPP-R29.4, WWPP-R23.1 |
| pp.wrk.phones | Phone worker count | Workers per tab on phones | number | 1 | 1-2 | admin | WWPP-R10.4 |
| pp.wrk.laptops | Laptop worker count | Workers per tab on laptops | number | 2 | 1-4 | admin | WWPP-R10.4 |
| pp.wrk.desktops | Desktop worker count | Workers per tab on desktops | number | 4 | 1-8 | admin | WWPP-R10.4 |
| pp.wrk.owner | Owner node worker count | Workers per tab on owner nodes | enum | auto | auto/1-32 | admin | WWPP-R10.4, WWPP-R29.4 |
| pp.wrk.phone_battery | Phone workers on battery | Workers per tab while the phone runs on battery | number | 1 | 1-2 | admin | WWPP-R30.2 |
| pp.wrk.phone_charging | Phone workers charging | Workers per tab while charging with the screen on | number | 2 | 1-4 | admin | WWPP-R30.2 |
| pp.wrk.phone_stop | Stop below battery | Work stops below this battery level | percent | 20 | 5-50 | admin | WWPP-R30.2 |
| pp.gd.cpu | CPU profile | Contributor CPU effort profile the server asks for | enum | medium | low/medium/full | admin | WWPP-R30.1 |
| pp.gd.backoff | Auto back-off | Drops worker effort only when the device is very very busy | toggle | on | on/off | admin | WWPP-R30.1 |
| pp.gd.battery | Battery rule | Applies the phone battery and heat rules where the device supports them | toggle | on | on/off | admin | WWPP-R30.1 |
| pp.gd.memory | Memory cap per worker | Upper memory bound asked of each worker | number (MiB) | TBD | 1-4096 | admin | WWPP-R30.1 |
| pp.gd.hidden_tab | Pause on hidden tab | Deliberately off: hidden and background tabs must keep working | toggle | off | off only | admin | WWPP-R30.1, WWPP-R39.1 |
| pp.gpu.enabled | GPU tasks | Allows jobs to declare cpu, gpu or gpu-preferred tasks in this pool | toggle | on | on/off | admin | WWPP-R33.1 |
| pp.gpu.time_limit | GPU task time limit | Maximum runtime of one GPU task | number (s) | TBD | 1-3600 | admin | WWPP-R33.1 |
| pp.gpu.memory_limit | GPU memory limit | Maximum GPU memory one task may request | number (MiB) | TBD | 1-8192 | admin | WWPP-R33.1 |
| pp.gpu.charging_only | GPU only while charging | GPU tasks are eligible only when the device is charging | toggle | on | on/off | admin | WWPP-R33.1 |
| pp.sch.bounded_queue | Bounded queue | Caps the admitted queue depth; the queue never grows without limit | toggle | on | on/off | admin | WWPP-R32.5 |
| pp.sch.rejection | Queue rejection policy | What happens when the bounded queue is full | enum | TBD | reject-429/block-timeout/drop-oldest/caller-runs | admin | WWPP-R25.3 |
| pp.sch.priority_high | High priority allowed | Lets the admin allow the high priority class in this pool | toggle | off | on/off | admin | WWPP-R25.2 |
| pp.sch.fair_share | Fair share | Shares pool capacity fairly between developers | toggle | on | on/off | admin | WWPP-R32.5 |
| pp.sch.small_task | Small-task warning | Tasks below this size raise an efficiency warning on all four surfaces | number (tasks) | TBD | 1-100 | admin | WWPP-R24.4 |
| pp.sch.batch_size | Batch size per device class | Default task batch size handed to a device class | number | TBD | 1-1000 | admin | WWPP-R34.1 |
| pp.ver.hash_check | Hash check on every load | Verifies artifact and input SHA-256 on every load, including IndexedDB cache hits | toggle | on | on/off | admin | WWPP-R37.1, WWPP-R37.1b |
| pp.ver.rerun_percent | Result re-run rate | Percentage of accepted results re-run on another worker; exact hashes only | percent | 1 | 0-100 | dev-request | WWPP-R35.1 |
| pp.ver.signed_envelope | Signed task envelope | Server signs each envelope with Ed25519 over job and task ids, artifact and input hashes and the lease | toggle | on | on/off | admin | WWPP-R37.1, WWPP-R37.1b |
| pp.ver.canary | Canary known-answer tasks | Schedules hidden known-answer tasks to test worker trustworthiness | toggle | on | on/off | admin | WWPP-R37.1, WWPP-R37.1b |
| pp.ver.canary_warn | Canary warn threshold | Reputation score at or below which the worker is warned | number | 75 | 0-100 | admin | WWPP-R37.1, WWPP-R39.1 |
| pp.ver.canary_bar | Canary bar threshold | Reputation score at or below which the worker is barred from tasks | number | 50 | 0-100 | admin | WWPP-R37.1, WWPP-R39.1 |
| pp.ver.canary_ban | Canary ban threshold | Reputation score at or below which the worker is banned | number | 0 | 0-100 | admin | WWPP-R37.1, WWPP-R39.1 |
| pp.ver.reputation_recovery | Reputation recovery | A ban is lifted by admin review; there is no automatic recovery | enum | manual-review | manual-review/auto | admin | WWPP-R37.1, WWPP-R39.1 |
| pp.ver.mode | Verification mode | (a) server mechanisms plus developer policy, or (b) developer only with results labelled unverified | enum | a | a/b | admin | WWPP-R24.1 |
| pp.ver.k_of_n | k-of-n checks | Independent checks that must agree before a result counts as verified | number | TBD | 1-5 | admin | WWPP-R24.1 |
| pp.ver.local_after_download | Verify after download | Lets developers verify results locally after download | toggle | on | on/off | dev-request | WWPP-R24.1 |
| pp.ver.evidence | Tamper evidence | (a) server minimal, security log plus SHA-256 of every accepted result, or (b) everything opt-in | enum | a | a/b | admin | WWPP-R35.2 |
| pp.ver.chain | Audit hash chain | Per-job hash chain written by the server; built and tested even while off | toggle | off | on/off | admin | WWPP-R26.2 |
| pp.ver.merkle | Global Merkle checkpoints | Adds periodic Merkle roots over the tips of job chains | toggle | off | on/off | admin | WWPP-R22.3 |
| pp.ver.chain_authority | Chain authority | (a) admin global Merkle on/off with developer raise-only, or (b) developer free choice inside the allowance | enum | TBD | a/b | admin | WWPP-R23.3 |
| pp.p2p.enabled | P2P sharing | Design target; runtime capability false pending F11 | toggle | off (runtime) | on/off | admin | WWPP-R36.2, WWPP-R36.2-RES |
| pp.p2p.peer_scope | Peer selection scope | Which peers may serve artifacts: same device, same phone, same LAN or very close proximity | enum | same-lan | same-device/same-phone/same-lan/near | admin | WWPP-R9.1 |
| pp.p2p.task_handoff | Task hand-off | Historical P2P scope, kept as a setting; not an active peer data flow | toggle | off | on/off | admin | WWPP-R9.1, WWPP-R36.2 |
| pp.p2p.result_relay | Result relay over P2P | Historical P2P scope, kept as a setting; results are never relayed over P2P | toggle | off | on/off | admin | WWPP-R9.1, WWPP-R36.2, WWPP-R39.1 |
| pp.p2p.rtt_probe | RTT probe step | Uses round-trip time in peer ordering once the probe exists | toggle | off | on/off | admin | WWPP-R14.1 |
| pp.p2p.fallback | CDN fallback | Falls back to server or CDN when no peer can serve the artifact | toggle | on | on/off | admin | WWPP-R36.2 |
| pp.dew.enabled | Dew | Offline signed receipts; runtime default enabled, 24h; explicit false disables; F12 evidence pending | toggle | on | on/off | admin | WWPP-R36.2 |
| pp.dew.window | Dew receipt window | How long a signed dew receipt stays valid | hours | 24 | 1-72 | admin | WWPP-R36.2 |
| pp.dew.late_policy | Late-result policy | (a) archived lease id and attempt match, (b) accept only if no other attempt reported, (c) drop after re-lease | enum | a | a/b/c | admin | WWPP-R13.2 |
| pp.dew.max_attempts | Max attempts | Attempts allowed for one task before it fails | number | 3 | 1-10 | admin | WWPP-R32.9 |
| pp.dew.job_policy | Job failure policy | What the job does when a task cannot complete | enum | partial | partial/fail-fast/threshold | admin | WWPP-R32.9 |
| pp.dew.quarantine | Quarantine threshold | Worker crashes before a poison task is quarantined | number | 3 | 1-10 | admin | WWPP-R32.9 |
| pp.dew.dead_letter | Dead-letter store | Keeps late results that arrive after the job ended, with no second credit | toggle | on | on/off | admin | WWPP-R32.9 |
| pp.met.light | Light metrics | Per-feature timings and counters in one PostgreSQL metrics table; disabled intervals are recorded, never fabricated | toggle | on | on/off | admin | WWPP-R37.2 |
| pp.met.benchmark | Thesis benchmark harness | Heavy harness for owner-started thesis runs beside the MVP | toggle | on | on/off | admin | WWPP-R37.2 |
| pp.cfg.retention | Version retention | Number of config versions kept for this admin page | number | 10 | 1-100 | admin | WWPP-R38.7 |
| pp.cfg.pin | Pin version | Admin-named pins, for example a thesis run, keep a version from pruning | text | — | 1-64 chars | admin | WWPP-R38.7 |
| pp.cfg.reason | Reason required | Each Save records who, when, what changed and why | toggle | on | on/off | admin | WWPP-R38.6 |
| pp.cfg.rollback | Rollback | One-click rollback copies an earlier version into a new audited version | button | — | — | admin | WWPP-R38.6 |

**`#manual` section.** The process pool admin page owns the pool default and limits for the Web Process Pool, including the two pool shapes, session leasing, lease-store choice, the same four tamper measures, P2P, dew, metrics and config versions. The pool shape may be requested per job inside the allowance (WWPP-R19.1, WWPP-R38.6, WWPP-R40.3).

---

## 6. `thread-api-admin.html` — Thread Pool API admin (`/thread-pool/v1`)

**Purpose.** Full detail and every setting of the Thread Pool API: keys, quotas, metering, queue, error model, versioning and upload checks.
API key issuance and approval belong to the admin account store on the director host; developers hold keys and request settings (WWPP-R38.5, WWPP-R32.5).

**Sections:** `#keys` Keys and tokens · `#quotas` Quotas · `#fairness` Fairness, priority and metering · `#queue` Queue · `#errors` Errors and contracts · `#upload` Upload checks · `#versions` API versioning · `#config` Config versions · `#manual` Manual section.

| id | label | legend | type | default | range | who may change | WWPP id |
|---|---|---|---|---|---|---|---|
| ta.keys.issuance | Key issuance mode | Manual issue by the admin, or developer self-service | enum | manual | manual/self-service | admin | WWPP-R20.4, WWPP-R32.4 |
| ta.keys.approval | Key approval mode | Approve each key request, or auto-approve all | enum | approve-each | approve-each/auto-approve-all | admin | WWPP-R20.4 |
| ta.keys.lifetime | Key lifetime | How long an issued key stays usable; the key is shown once and stored hashed | duration | TBD | 1h-365d | admin | WWPP-R32.4 |
| ta.keys.scope | Key scope | Permissions bound to a key, such as pool, artifacts, quota or events | multi-select | TBD | pool/artifacts/quota/events | admin | WWPP-R32.4 |
| ta.keys.jwt_ttl | JWT lifetime | Lifetime of the short token exchanged from the key | minutes | 15 | 1-60 | admin | WWPP-R32.4, WWPP-R39.1 |
| ta.keys.oauth2 | OAuth2 client credentials | Optional token endpoint for future SSO | toggle | off | on/off | admin | WWPP-R32.4 |
| ta.keys.revoke | Key revocation | Revokes a key at once; revoked authority cannot mint or renew leases | button | — | — | admin | WWPP-R25.1, WWPP-R32.4 |
| ta.q.running_jobs | Running jobs quota | Jobs that may run at the same time on one key | number | TBD | 1-1000 | admin | WWPP-R32.5 |
| ta.q.queued_tasks | Queued tasks quota | Tasks that may wait in the queue on one key | number | TBD | 1-100000 | admin | WWPP-R32.5 |
| ta.q.task_size | Task size quota | Unsupported per-key quota; capability zero; separate transport/lease bounds remain | number (KiB) | not active | 1-1048576 | admin | WWPP-R32.5 |
| ta.q.task_time | Task time quota | Unsupported per-key quota; capability zero; separate transport/lease bounds remain | number (s) | not active | 1-86400 | admin | WWPP-R32.5 |
| ta.q.req_per_min | Requests per minute | Maximum API requests per minute on one key | number | TBD | 1-100000 | admin | WWPP-R32.5 |
| ta.q.retry_after | 429 Retry-After | Sends Retry-After with every rate-limit rejection | toggle | on | on/off | admin | WWPP-R32.5, WWPP-R32.8 |
| ta.fair.share | Fair-share scheduler | Shares capacity fairly between developers | toggle | on | on/off | admin | WWPP-R32.5 |
| ta.fair.priority_high | High priority class | Allows the high priority class when the admin permits it | toggle | off | on/off | admin | WWPP-R25.2 |
| ta.fair.credits | Credits and metering | Meters CPU-seconds and GPU-seconds per key and reports them | toggle | on | on/off | admin | WWPP-R32.5 |
| ta.fair.rewards | Reports and rewards | Issues reports and rewards from metered credits; no payment now | toggle | off | on/off | admin | WWPP-R32.5 |
| ta.queue.bounded | Bounded queue | Caps the admitted queue depth | toggle | on | on/off | admin | WWPP-R25.3 |
| ta.queue.rejection | Rejection policy | What happens when the queue is full | enum | TBD | reject-429/block-timeout/drop-oldest/caller-runs | admin | WWPP-R25.3 |
| ta.err.problem_json | RFC 9457 problem+json | REST problem+json, SSE error frame and gRPC ErrorDetail; see runtime contract | toggle | on | on/off | admin | WWPP-R32.8 |
| ta.err.idempotency | Idempotency keys | Same key and body replays; a different body is a conflict | toggle | on | on/off | admin | WWPP-R32.8 |
| ta.err.traceparent | traceparent support | Unsupported design setting; no runtime implementation | toggle | off (effective) | on/off | admin | WWPP-R32.8 |
| ta.err.cursor | Cursor paging | Opaque cursor paging with limit on list routes | toggle | on | on/off | admin | WWPP-R32.8 |
| ta.upload.format | Format check | Checks the uploaded module and data format | toggle | on | on/off | admin | WWPP-R36.1 |
| ta.upload.size | Size check | Enforces the maximum upload size | toggle | on | on/off | admin | WWPP-R36.1 |
| ta.upload.hash | Hash check | Checks uploaded bytes against the declared SHA-256 | toggle | on | on/off | admin | WWPP-R36.1 |
| ta.upload.scan | Import and permission scan | Scans imports and permissions in the uploaded code | toggle | on | on/off | admin | WWPP-R36.1 |
| ta.upload.review_gate | Review gate | A new code upload waits for admin review | toggle | on | on/off | admin | WWPP-R36.1 |
| ta.upload.malware | Malware and abuse scan | Unsupported design setting; no runtime implementation | toggle | off (effective) | on/off | admin | WWPP-R36.1 |
| ta.ver.major | URL major version | Major version segment of this route family | text | v1 | v1 | admin | WWPP-R32.6 |
| ta.ver.notice | Deprecation notice | Notice period before the next major version | months | 6 | 3-12 | admin | WWPP-R32.6 |
| ta.ver.contract_tests | Contract tests | Contract tests generated from OpenAPI run on every change | toggle | on | on/off | admin | WWPP-R32.6 |
| ta.cfg.retention | Version retention | Number of config versions kept for this admin page | number | 10 | 1-100 | admin | WWPP-R38.7 |
| ta.cfg.pin | Pin version | Admin-named pins keep a version from pruning | text | — | 1-64 chars | admin | WWPP-R38.7 |
| ta.cfg.reason | Reason required | Each Save records who, when, what changed and why | toggle | on | on/off | admin | WWPP-R38.6 |
| ta.cfg.rollback | Rollback | One-click rollback copies an earlier version into a new audited version | button | — | — | admin | WWPP-R38.6 |

**`#manual` section.** The Thread Pool API admin page owns keys, quotas, fairness, metering, the queue policy, the error model, upload checks and the versioned config for `/thread-pool/v1`. The developer sees the results and the metering view and may request per-job settings inside the allowance (WWPP-R32.5, WWPP-R40.3).

---

## 7. `process-api-admin.html` — Process Pool API admin (`/process-pool/v1`)

**Purpose.** Full detail and every setting of the Process Pool API: keys, quotas, metering, sessions, queue, error model, versioning and upload checks.
It mirrors the Thread Pool API admin page and adds the session settings that only exist for the process pool (WWPP-R38.5, WWPP-R32.2).

**Sections:** `#keys` Keys and tokens · `#quotas` Quotas · `#fairness` Fairness, priority and metering · `#sessions` Sessions · `#queue` Queue · `#errors` Errors and contracts · `#upload` Upload checks · `#versions` API versioning · `#config` Config versions · `#manual` Manual section.

| id | label | legend | type | default | range | who may change | WWPP id |
|---|---|---|---|---|---|---|---|
| pa.keys.issuance | Key issuance mode | Manual issue by the admin, or developer self-service | enum | manual | manual/self-service | admin | WWPP-R20.4, WWPP-R32.4 |
| pa.keys.approval | Key approval mode | Approve each key request, or auto-approve all | enum | approve-each | approve-each/auto-approve-all | admin | WWPP-R20.4 |
| pa.keys.lifetime | Key lifetime | How long an issued key stays usable; the key is shown once and stored hashed | duration | TBD | 1h-365d | admin | WWPP-R32.4 |
| pa.keys.scope | Key scope | Permissions bound to a key, such as pool, artifacts, quota or events | multi-select | TBD | pool/artifacts/quota/events | admin | WWPP-R32.4 |
| pa.keys.jwt_ttl | JWT lifetime | Lifetime of the short token exchanged from the key | minutes | 15 | 1-60 | admin | WWPP-R32.4, WWPP-R39.1 |
| pa.keys.oauth2 | OAuth2 client credentials | Optional token endpoint for future SSO | toggle | off | on/off | admin | WWPP-R32.4 |
| pa.keys.revoke | Key revocation | Revokes a key at once; revoked authority cannot mint or renew leases | button | — | — | admin | WWPP-R25.1, WWPP-R32.4 |
| pa.q.running_jobs | Running jobs quota | Jobs that may run at the same time on one key | number | TBD | 1-1000 | admin | WWPP-R32.5 |
| pa.q.queued_tasks | Queued tasks quota | Tasks that may wait in the queue on one key | number | TBD | 1-100000 | admin | WWPP-R32.5 |
| pa.q.task_size | Task size quota | Unsupported per-key quota; capability zero; separate transport/lease bounds remain | number (KiB) | not active | 1-1048576 | admin | WWPP-R32.5 |
| pa.q.task_time | Task time quota | Unsupported per-key quota; capability zero; separate transport/lease bounds remain | number (s) | not active | 1-86400 | admin | WWPP-R32.5 |
| pa.q.req_per_min | Requests per minute | Maximum API requests per minute on one key | number | TBD | 1-100000 | admin | WWPP-R32.5 |
| pa.q.retry_after | 429 Retry-After | Sends Retry-After with every rate-limit rejection | toggle | on | on/off | admin | WWPP-R32.5, WWPP-R32.8 |
| pa.fair.share | Fair-share scheduler | Shares capacity fairly between developers | toggle | on | on/off | admin | WWPP-R32.5 |
| pa.fair.priority_high | High priority class | Allows the high priority class when the admin permits it | toggle | off | on/off | admin | WWPP-R25.2 |
| pa.fair.credits | Credits and metering | Meters CPU-seconds and GPU-seconds per key and reports them | toggle | on | on/off | admin | WWPP-R32.5 |
| pa.fair.rewards | Reports and rewards | Issues reports and rewards from metered credits; no payment now | toggle | off | on/off | admin | WWPP-R32.5 |
| pa.ses.enabled | Session API | Enables the explicit process-pool session API beside plain jobs | toggle | on | on/off | admin | WWPP-R32.2 |
| pa.ses.shape | Session shape | (a) tab session lease, or (b) task-group pinning | enum | TBD | a/b | dev-request | WWPP-R19.1 |
| pa.ses.lease_minutes | Session lease duration | Minutes a leased session stays warm before release | number (min) | TBD | 1-1440 | admin | WWPP-R19.1 |
| pa.ses.tab_count | Tabs per session | Tabs leased for one session | number | TBD | 1-64 | admin | WWPP-R19.1 |
| pa.ses.host_count | Hosts per session | Distinct registered devices per session; one up to the tab count | number | TBD | 1-64 | admin | WWPP-R39.1 |
| pa.ses.warm_initializer | Warm initializer | Artifact preloaded into the session tab before tasks arrive | toggle | off | on/off | admin | WWPP-R19.1 |
| pa.ses.initializer_state | Initializer state | Default state of the warm initializer between sessions | enum | TBD | keep/discard | admin | WWPP-R25.3 |
| pa.ses.close_policy | Session close | What happens to the tabs on close, expiry or error | enum | release | release/drain | admin | WWPP-R19.1 |
| pa.queue.bounded | Bounded queue | Caps the admitted queue depth | toggle | on | on/off | admin | WWPP-R25.3 |
| pa.queue.rejection | Rejection policy | What happens when the queue is full | enum | TBD | reject-429/block-timeout/drop-oldest/caller-runs | admin | WWPP-R25.3 |
| pa.err.problem_json | RFC 9457 problem+json | REST problem+json, SSE error frame and gRPC ErrorDetail; see runtime contract | toggle | on | on/off | admin | WWPP-R32.8 |
| pa.err.idempotency | Idempotency keys | Same key and body replays; a different body is a conflict | toggle | on | on/off | admin | WWPP-R32.8 |
| pa.err.traceparent | traceparent support | Unsupported design setting; no runtime implementation | toggle | off (effective) | on/off | admin | WWPP-R32.8 |
| pa.err.cursor | Cursor paging | Opaque cursor paging with limit on list routes | toggle | on | on/off | admin | WWPP-R32.8 |
| pa.upload.format | Format check | Checks the uploaded module and data format | toggle | on | on/off | admin | WWPP-R36.1 |
| pa.upload.size | Size check | Enforces the maximum upload size | toggle | on | on/off | admin | WWPP-R36.1 |
| pa.upload.hash | Hash check | Checks uploaded bytes against the declared SHA-256 | toggle | on | on/off | admin | WWPP-R36.1 |
| pa.upload.scan | Import and permission scan | Scans imports and permissions in the uploaded code | toggle | on | on/off | admin | WWPP-R36.1 |
| pa.upload.review_gate | Review gate | A new code upload waits for admin review | toggle | on | on/off | admin | WWPP-R36.1 |
| pa.upload.malware | Malware and abuse scan | Unsupported design setting; no runtime implementation | toggle | off (effective) | on/off | admin | WWPP-R36.1 |
| pa.ver.major | URL major version | Major version segment of this route family | text | v1 | v1 | admin | WWPP-R32.6 |
| pa.ver.notice | Deprecation notice | Notice period before the next major version | months | 6 | 3-12 | admin | WWPP-R32.6 |
| pa.ver.contract_tests | Contract tests | Contract tests generated from OpenAPI run on every change | toggle | on | on/off | admin | WWPP-R32.6 |
| pa.cfg.retention | Version retention | Number of config versions kept for this admin page | number | 10 | 1-100 | admin | WWPP-R38.7 |
| pa.cfg.pin | Pin version | Admin-named pins keep a version from pruning | text | — | 1-64 chars | admin | WWPP-R38.7 |
| pa.cfg.reason | Reason required | Each Save records who, when, what changed and why | toggle | on | on/off | admin | WWPP-R38.6 |
| pa.cfg.rollback | Rollback | One-click rollback copies an earlier version into a new audited version | button | — | — | admin | WWPP-R38.6 |

**`#manual` section.** The Process Pool API admin page owns keys, quotas, fairness, metering, the session API, the queue policy, the error model, upload checks and the versioned config for `/process-pool/v1`. The session shape may be requested per job inside the allowance; the admin sets the pool default (WWPP-R19.1, WWPP-R32.2, WWPP-R40.3).

---

## Open items carried into this source

**Current integration boundary:** runtime admin routes, authentication, PostgreSQL config and metrics are implemented; unsupported design controls remain identified individually. See [R3E runtime contract](R3E_RUNTIME_CONTRACT.md).


1. TBD labels are historical design values, not the current runtime default. Read the runtime contract and live capabilities; PG is the default lease store.
2. Page filenames, anchor names, the config schema v1 `{schemaVersion, version, parentVersion, actorId, createdAt, reason, settings}` and the `pool_config_version` table are the approved §14 proposals and still need owner confirmation of the spelling (WWPP-R38.6, WWPP-R40.2, WWPP-R39.1).
3. The `director` value stays available in the vocabulary but is not used on any pool row: for pool settings the Pool Admin is the middle authority and the Director layer is superseded, while Directors keep only the 3 server-app dashboards (WWPP-R27.1, WWPP-R38.3).

END
