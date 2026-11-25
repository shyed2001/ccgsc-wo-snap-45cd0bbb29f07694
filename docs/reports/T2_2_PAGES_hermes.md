# T2.2 Report - serve the admin/monitor pages from pools-core

Work order: `docs/work_orders/WO_T2_2_PAGES_INTO_CORE.md`
Author: hermes MiniMax-M3 for Claude Opus 5.5
Date: 2026-11-17
Work folder: `F:\tmp\hcwork\pools-core` (copy of repo CCGS_WASM/pools-core)
Decisions applied: `docs/decisions/GRILL_WWPP_R1.md` R38.5, R38.5b, R40.1-R40.3
Go env used: `GOMODCACHE=F:\GoCache\mod`, `GOCACHE=F:\GoCache\build`, `GOTOOLCHAIN=local`, Go `go1.26.1 windows/amd64`, Node `v22.16.0`

## What was done

### 1. The real pages are served from pools-core under /pools/

`web/embed.go` now exposes three embeds instead of one:

| Embed | Contents | Served at |
|---|---|---|
| `Files` | `admin/`, `monitor/`, `contribute/`, `nav.js`, `nav.json` | `/web/` |
| `Nav` | `nav.js`, `nav.json`, `nav_demo.html` | `/nav/` |
| `Pages` | `pages/*.html`, `pages/manual/*.html`, `pages/css/*.css`, `pages/js/*.js` | `/pools/` |

`NavRoot()` and `PagesRoot()` return the two sub-trees. `internal/api/api.go` mounts them and
adds the redirects:

| Route | Behavior |
|---|---|
| `GET /pools/{page}.html` | 200 `text/html` for all seven pages: `monitor`, `pool-monitor`, `api-monitor`, `thread-pool-admin`, `process-pool-admin`, `thread-api-admin`, `process-api-admin` |
| `GET /pools/manual/{page}.html` | 200 `text/html` for all seven manual pages |
| `GET /pools/css/style.css` | 200 `text/css` |
| `GET /pools/js/{schema,api,mock,app}.js` | 200 `application/javascript` |
| `GET /pools/` | 307 -> `/pools/monitor.html` |
| `GET /web/admin/` | 307 -> `/pools/monitor.html` (old route kept, now redirects) |
| `GET /web/monitor/` | 307 -> `/pools/monitor.html` (old route kept, now redirects) |
| `GET /web/contribute/` | 200 `text/html`, unchanged placeholder |
| `GET /` | 307 -> `/pools/monitor.html` |

A failed embed no longer returns a nil file tree: `emptyFS` stands in and the failure is logged.

### 2. js/api.js reads the real pools-core endpoints first, mock.js is the fallback

The endpoints that actually exist in `internal/api` were read before this was written:

- `GET /healthz` - always served, no auth
- `GET /{thread,process}-pool/v1/capabilities` - developer role + `jobs:read` scope
- `GET /{pool}/v1/events` - SSE, developer role + `jobs:read` scope
- there is no `/admin/v1/*` route in pools-core, so the config, save, rollback and audit calls
  all fall back to `mock.js` and say so

`js/api.js` now tracks the data source per call (`markLive` / `markMock` / `getDataSource`)
and renders a small `MOCK DATA` badge (`renderDataSourceBadge`, id `ccgsc-data-source-badge`)
whose tooltip names the exact reason. The badge is created once, never stacked, and removed
when the data is live. `LiveEventTracker` turns the durable pools-core event stream into the
two rows the stream can answer truthfully (`mon.work.tasks_per_s`, `mon.work.running`) and
nothing else.

`js/app.js` renders the badge after init, after the first metrics load and after the stream
connects, so the badge always matches what is on screen.

### 3. One nav

`web/nav.js` and `web/nav.json` are the canonical nav. The duplicates under
`web/pages/nav/` were removed; `web/pages/nav/nav.test.mjs` is a one-line compatibility shim
that re-runs the canonical suite, so the documented test path still works.

`web/nav.js` was fixed for real module use: `document.currentScript` is null inside an ES
module, so `nav.json` now resolves through `import.meta.url`. It also renders only one bar per
mount, and gained `updateServiceStatus()` for the `#status` line.

Every page - the seven `/pools/` pages, the seven `/pools/manual/` pages and the three
`/web/` placeholders - now loads `/nav/nav.js`. The `/nav/` and `/web/` paths serve the same
bytes; a Go test asserts that.

### 4. Tests

New `tests/pages_test.go` (6 tests, 22 subtests): each `/pools/*.html` and each manual page
must answer 200 with `text/html` and load the shared nav; the assets must answer 200 with the
right content type; `/pools/` must redirect; the legacy routes must redirect while
`/web/contribute/` still works; and `/nav/nav.js` + `/web/nav.js` (and the two `nav.json`)
must be byte-identical.

`tests/core_test.go` static-page expectations were updated for the new routes.

New `web/pages/tests/pages.test.mjs` (17 tests): the MOCK DATA badge, the data-source
bookkeeping, the live probes, the live event tracker and the one-nav checks.

### 5. Docs and headers

`docs/MODULES.md` gained a "Page and asset routes (WO-T2.2)" routes table and a
"WO-T2.2 measurements" validation table. `docs/SESSION_TODO.md` row T2.2 is marked done.
`scripts/pools-core.ps1`, `scripts/pools-core.sh` and `scripts/README.md` now open
`/pools/monitor.html` instead of the `/web/` placeholders.

Every changed file carries the header comment `Author: hermes MiniMax-M3 for Claude Opus 5.5`
and keeps the original agy/Codex authorship line.

## Files changed

| File | Change |
|---|---|
| `web/embed.go` | rewritten: three embeds (`Files`, `Nav`, `Pages`) + `NavRoot()` / `PagesRoot()` |
| `web/nav.js` | new canonical nav (moved from `web/pages/nav/nav.js`), module-safe URL resolution, single-render guard, `updateServiceStatus()` |
| `web/nav.json` | new canonical link registry (moved from `web/pages/nav/nav.json`) |
| `web/nav.test.mjs` | new canonical nav test suite (moved from `web/pages/nav/nav.test.mjs`) |
| `web/nav_demo.html` | moved from `web/pages/nav/nav_demo.html` |
| `web/pages/nav/nav.js` | deleted (duplicate) |
| `web/pages/nav/nav.json` | deleted (duplicate) |
| `web/pages/nav/nav_demo.html` | deleted (duplicate) |
| `web/pages/nav/nav.test.mjs` | replaced by a one-line shim that re-runs `web/nav.test.mjs` |
| `web/admin/index.html` | loads the one `/nav/nav.js` module; mount is `<header id="ccgsc-nav">` |
| `web/monitor/index.html` | same |
| `web/contribute/index.html` | same |
| `web/pages/monitor.html` | nav script path -> `/nav/nav.js` |
| `web/pages/pool-monitor.html` | nav script path -> `/nav/nav.js` |
| `web/pages/api-monitor.html` | nav script path -> `/nav/nav.js` |
| `web/pages/thread-pool-admin.html` | nav script path -> `/nav/nav.js` |
| `web/pages/process-pool-admin.html` | nav script path -> `/nav/nav.js` |
| `web/pages/thread-api-admin.html` | nav script path -> `/nav/nav.js` |
| `web/pages/process-api-admin.html` | nav script path -> `/nav/nav.js` |
| `web/pages/manual/monitor.html` | nav script path -> `/nav/nav.js` |
| `web/pages/manual/pool-monitor.html` | nav script path -> `/nav/nav.js` |
| `web/pages/manual/api-monitor.html` | nav script path -> `/nav/nav.js` |
| `web/pages/manual/thread-pool-admin.html` | nav script path -> `/nav/nav.js` |
| `web/pages/manual/process-pool-admin.html` | nav script path -> `/nav/nav.js` |
| `web/pages/manual/thread-api-admin.html` | nav script path -> `/nav/nav.js` |
| `web/pages/manual/process-api-admin.html` | nav script path -> `/nav/nav.js` |
| `web/pages/js/api.js` | live-first client, `LiveEventTracker`, data-source bookkeeping, MOCK DATA badge |
| `web/pages/js/app.js` | nav path `/nav/nav.json`, badge rendering, initial metrics load |
| `web/pages/css/style.css` | `.data-source-badge` style |
| `web/pages/tests/pages.test.mjs` | new: badge, data source, live probes, tracker, one-nav checks |
| `internal/api/api.go` | `/pools/`, `/nav/`, the redirects, `emptyFS` |
| `tests/pages_test.go` | new: the `/pools/` acceptance tests |
| `tests/core_test.go` | static-page expectations updated for the new routes |
| `docs/MODULES.md` | routes table, package row, WO-T2.2 validation table |
| `docs/SESSION_TODO.md` | row T2.2 marked done |
| `scripts/pools-core.ps1` | opens `/pools/monitor.html` |
| `scripts/pools-core.sh` | opens `/pools/monitor.html` |
| `scripts/README.md` | opens `/pools/monitor.html` |
| `T2_2_PROGRESS.log` | progress lines |
| `T2_2_REPORT.md` | this report |

No file was deleted outside `web/pages/nav/` (the duplicate nav), no dependency was added,
nothing was installed, no git operation was performed (the work folder has no `.git`).

## Test results (exact tails)

```
$ go build ./...
exit=0

$ go vet ./...
exit=0

$ go test ./...
?   	ccgsc/poolscore/cmd/pools-core	[no test files]
?   	ccgsc/poolscore/internal/api	[no test files]
?   	ccgsc/poolscore/internal/auth	[no test files]
?   	ccgsc/poolscore/internal/config	[no test files]
?   	ccgsc/poolscore/internal/httpx	[no test files]
?   	ccgsc/poolscore/internal/logx	[no test files]
?   	ccgsc/poolscore/internal/pool	[no test files]
?   	ccgsc/poolscore/internal/store	[no test files]
?   	ccgsc/poolscore/internal/store/pg	[no test files]
?   	ccgsc/poolscore/internal/store/sqlite	[no test files]
ok  	ccgsc/poolscore/internal/verify	1.563s
?   	ccgsc/poolscore/internal/worker	[no test files]
ok  	ccgsc/poolscore/tests	4.097s
?   	ccgsc/poolscore/web	[no test files]

$ gofmt -l .
(empty)

$ go test ./tests/ -run 'TestPools|TestLegacy|TestOneNav' -v
--- PASS: TestPoolsPagesAreServed (0.27s)
    --- PASS: TestPoolsPagesAreServed/monitor (0.14s)
    --- PASS: TestPoolsPagesAreServed/pool-monitor (0.00s)
    --- PASS: TestPoolsPagesAreServed/api-monitor (0.00s)
    --- PASS: TestPoolsPagesAreServed/thread-pool-admin (0.00s)
    --- PASS: TestPoolsPagesAreServed/process-pool-admin (0.00s)
    --- PASS: TestPoolsPagesAreServed/thread-api-admin (0.00s)
    --- PASS: TestPoolsPagesAreServed/process-api-admin (0.00s)
--- PASS: TestPoolsManualPagesAreServed (0.18s)
    --- PASS: TestPoolsManualPagesAreServed/monitor (0.00s)
    --- PASS: TestPoolsManualPagesAreServed/pool-monitor (0.00s)
    --- PASS: TestPoolsManualPagesAreServed/api-monitor (0.00s)
    --- PASS: TestPoolsManualPagesAreServed/thread-pool-admin (0.00s)
    --- PASS: TestPoolsManualPagesAreServed/process-pool-admin (0.00s)
    --- PASS: TestPoolsManualPagesAreServed/thread-api-admin (0.00s)
    --- PASS: TestPoolsManualPagesAreServed/process-api-admin (0.00s)
--- PASS: TestPoolsAssetsAreServed (0.17s)
--- PASS: TestPoolsRootRedirects (0.16s)
--- PASS: TestLegacyWebRoutesStillWork (0.15s)
--- PASS: TestOneNavForEveryPage (0.16s)
PASS
ok  	ccgsc/poolscore/tests	1.389s

$ node --test web/pages/tests/web.test.mjs
# tests 17
# suites 5
# pass 17
# fail 0
# cancelled 0
# skipped 0
# todo 0
# duration_ms 731.539

$ node --test web/pages/tests/pages.test.mjs
# tests 17
# suites 6
# pass 17
# fail 0
# cancelled 0
# skipped 0
# todo 0
# duration_ms 330.1103

$ node --test web/nav.test.mjs
# tests 22
# suites 6
# pass 22
# fail 0
# cancelled 0
# skipped 0
# todo 0
# duration_ms 445.8645

$ node --test web/pages/nav/nav.test.mjs
# tests 22
# suites 6
# pass 22
# fail 0
# cancelled 0
# skipped 0
# todo 0
# duration_ms 380.8407

$ node --check web/nav.js
$ node --check web/pages/js/app.js
$ pwsh -NoProfile -Command "<AST parse of scripts/pools-core.ps1>"
PARSE_OK
$ bash -n scripts/pools-core.sh
SH_SYNTAX_OK
```

## Live verification against a running server

The binary was built and started on `127.0.0.1:8099` with a bootstrap key, then the routes
were swept with `curl` and the pages were exercised in a browser-equivalent module graph.

```
/                                      307 text/html; charset=utf-8  Location: /pools/monitor.html
/pools/                                307 text/html; charset=utf-8  Location: /pools/monitor.html
/pools/monitor.html                    200 text/html; charset=utf-8
/pools/pool-monitor.html               200 text/html; charset=utf-8
/pools/api-monitor.html                200 text/html; charset=utf-8
/pools/thread-pool-admin.html          200 text/html; charset=utf-8
/pools/process-pool-admin.html         200 text/html; charset=utf-8
/pools/thread-api-admin.html           200 text/html; charset=utf-8
/pools/process-api-admin.html          200 text/html; charset=utf-8
/pools/manual/monitor.html             200 text/html; charset=utf-8
/pools/manual/thread-api-admin.html    200 text/html; charset=utf-8
/pools/css/style.css                   200 text/css; charset=utf-8
/pools/js/app.js                       200 application/javascript
/pools/js/api.js                       200 application/javascript
/nav/nav.js                            200 application/javascript
/nav/nav.json                          200 application/json
/nav/nav_demo.html                     200 text/html; charset=utf-8
/web/admin/                            307 text/html; charset=utf-8  Location: /pools/monitor.html
/web/monitor/                          307 text/html; charset=utf-8  Location: /pools/monitor.html
/web/contribute/                       200 text/html; charset=utf-8
/web/nav.js                            200 application/javascript
/web/nav.json                          200 application/json
```

Browser-equivalent probe (the real served modules fetched over HTTP and evaluated as ES
modules with a browser-like `import.meta.url`, against a doubled DOM):

```
=== /pools/monitor.html (no token) ===
nav bar rendered     : true
nav links            : 11 | Public Info, Contribute (Worker), Developer Portal, Monitor,
                               Pool Monitor, API Monitor, Thread Pool Admin, Process Pool Admin,
                               Thread API Admin, Process API Admin, Manual
metric cards         : 34
header + manual      : true true
badge                : true | text="MOCK DATA"
                        title="Mock data in use - live: pools-core /healthz answered;
                        pools-core exposes no metric counters yet, so the metric values are mock data;
                        no bearer token for the live event stream"
page errors          : []

=== /pools/thread-pool-admin.html (no token) ===
fetches              : /healthz, /nav/nav.json, /admin/v1/config/thread-pool-admin
form fields          : 67
versions panel       : true
badge                : true
page errors          : []

=== live path (real bearer token) ===
GET /thread-pool/v1/events delivered real pools-core events:
  {"mon.work.tasks_per_s":0.1,"mon.work.running":1}
  {"mon.work.tasks_per_s":0.1,"mon.work.running":0}
data source          : live      (MOCK DATA badge absent)
```

The server was stopped afterwards; port 8099 is free and the runtime `data/` and `logs/`
artifacts it created were removed.

## Notes, limits and honest gaps

- **Chromium is not installed on this machine** and the work order forbids installs, so the
  pages were never clicked through in a real browser. The VM module-graph probe above is the
  strongest evidence available here: it runs the real served modules against the real server
  with a browser-like module URL and DOM, but it is not a browser.
- **pools-core has no `/admin/v1/*` routes.** The config, save, rollback and audit calls in
  `js/api.js` therefore always fall back to `mock.js` today, and the badge says so. The
  config-version storage work is still an open successor task (see `docs/MODULES.md`,
  "Remaining integration work").
- **pools-core exposes no metric counters.** `/healthz` answers and proves the service is
  live, but the metric values themselves are mock until a metrics route exists. The badge
  therefore shows on the monitor pages with a truthful reason, rather than pretending.
- **The nav link registry was left unchanged.** `web/nav.json` holds absolute HTTPS URLs to
  `ccgsc-director.digitalbd.org` and friends, and the nav test suite requires exactly that.
  Changing it to relative `/pools/*.html` links would break the existing nav contract
  (R38.2/R38.3) and its 22 tests, so the registry was not rewritten. The served pages
  navigate to each other through the section links and the manual links, which are relative.
- **`web/pages/nav/nav.test.mjs` is a shim, not a second suite.** It imports the canonical
  suite so the documented path still runs and still reports 22/22.
- **The old `/web/admin/` and `/web/monitor/` placeholders now redirect.** The redirect target
  is `/pools/monitor.html` as the work order requires; the admin pages themselves are the
  four `/pools/*-admin.html` routes listed in the routes table.
- The work folder has no `.git`, so no commit or diff could be produced or checked.

DONE T2.2
