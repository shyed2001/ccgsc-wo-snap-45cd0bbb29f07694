# WO FIX-F Report: Admin/Monitor Pages JS Refactoring & Security Hardening

**Location:** `F:\tmp\par\fixf\pools-core`
**Source Work Order:** `docs/work_orders/WO_FIX_F_ADMIN_PAGES_JS.md`
**Source Review:** `docs/reviews/WEBCHAT_R2_glm_pages_20261118.md` (1 BLOCKER, 4 MAJOR, 17 MINOR)
**Author/Harness:** agy Gemini 3.8 Flash for Claude Opus 5.5 / User
**Date:** 2026-11-18

---

## 1. Findings Resolution Matrix (Row -> Fix -> Test)

### [BLOCKER]
| # | Finding & File/Lines | Fix Description | Test Verification |
|---|---|---|---|
| 1 | **api.js:314-317, 336-339**<br>Live config save/rollback silently fell back to mock state on error and returned success. | Removed mock fallback in `saveConfig` and `rollbackConfig` when not in explicit mock mode. Errors rethrow as `ProblemError` and trigger `displayError` on caller. | `web/pages/tests/pages.test.mjs` (`live saveConfig never falls back to mock on failure and rethrows ProblemError`, `live rollbackConfig never falls back to mock on failure and rethrows ProblemError`). |

---

### [MAJOR]
| # | Finding & File/Lines | Fix Description | Test Verification |
|---|---|---|---|
| 2 | **app.js:450-452**<br>`renderConfigVersionTable` interpolated server data (`v.pinnedName`, `v.reason`, `v.author`) raw into `innerHTML` (stored XSS). | All server data fields now safely escaped via `esc()` helper before HTML insertion. | `web/pages/tests/security.test.mjs` (`renderConfigVersionTable escapes author, pinnedName, and reason to prevent stored XSS`). |
| 3 | **api.js:435-440**<br>`displayError` interpolated `problem.title/detail/instance` raw and allowed arbitrary URL schemes like `javascript:`. | Refactored `displayError` to use safe DOM text nodes (`createElement`, `textContent`) and strict `^https?://` URL whitelisting for `problem.type` links. | `web/pages/tests/security.test.mjs` (`displayError uses DOM text nodes and whitelists only http(s) URLs for problem.type`). |
| 4 | **app.js:371**<br>`renderFormField` injected `value="${val}"` unescaped into attribute (attribute breakout XSS). | Escaped setting value via `esc(rawVal)` and attributes via `esc()`, preventing quote breakouts. | `web/pages/tests/security.test.mjs` (`renderFormField escapes values to prevent attribute breakout XSS`). |
| 5 | **api.js:372-389**<br>`readEventStream` clean stream end resolved silently without reconnect or badge update. | Implemented automatic stream reconnect with exponential backoff, `Last-Event-ID` tracking and header delivery, and `markMock` with fallback on retry exhaustion. | `web/pages/tests/pages.test.mjs` (`reconnects with backoff and Last-Event-ID when event stream ends cleanly`). |

---

### [MINOR]
| # | Finding & File/Lines | Fix Description | Test Verification |
|---|---|---|---|
| 6 | **api.js:20, 410**<br>`markLive` pushed duplicate reasons on every SSE event without deduping. | Added deduplication check `!state.reasons.includes(msg)` in `markLive`. | `web/pages/tests/pages.test.mjs` (`dedupes markLive reasons to avoid unbounded growth on SSE events`). |
| 7 | **api.js:407**<br>Event stream URL hardcoded to `/thread-pool/v1/events` for all pages. | Added `getPoolUrls` to stream both pools (`/thread-pool/v1/events` and `/process-pool/v1/events`) by default, or page-specified pool. | `web/pages/tests/pages.test.mjs` (`resolves both pools URLs by default or single pool when specified`). |
| 8 | **api.js:203-224**<br>`runningJobs` in `LiveEventTracker` remained stale after mid-job stream reconnect. | Added `LiveEventTracker.reset()` method and invoked it on stream reconnect to clear stale jobs and counters. | `web/pages/tests/pages.test.mjs` (`tracker.reset() clears runningJobs and settled tasks upon stream reconnect`). |
| 9 | **api.js:375-377**<br>SSE frames split on `\n\n` only; CRLF framing failed to parse. | Buffer now normalizes `\r\n` to `\n` before frame splitting. | `web/pages/tests/pages.test.mjs` (`parses SSE events framed with CRLF (\r\n\r\n) and extracts Last-Event-ID`). |
| 10 | **api.js:172, 135**<br>No fetch timeout on `/healthz` probes and `request()`. | Added default `AbortSignal.timeout(5000)` (5s) to `request()`, `getServiceHealth()`, and `getPoolCapabilities()`. | `web/pages/tests/pages.test.mjs` (`returns null from getServiceHealth when /healthz does not answer`). |
| 11 | **api.js:96-99**<br>Bearer token stored solely in `sessionStorage` (vulnerable to XSS). | Added in-memory token management with `setBearerToken` and `getBearerToken`, falling back to `sessionStorage` when no in-memory token is set. | `web/pages/tests/pages.test.mjs` (`supports in-memory bearer token via setBearerToken and getBearerToken`). |
| 12 | **app.js:88-91**<br>Unconditionally forced `window.CCGSC_NAV_ROLES=['admin']` for all visitors. | Derived roles from real login state (`window.CCGSC_NAV_ROLES` or `window.CCGSC_NAV.getNavRoles()`), defaulting to `[]` for anonymous users. | `web/pages/tests/pages.test.mjs` (`initNavBar does not force admin roles for anonymous users`). |
| 13 | **schema.js:465**<br>Toggle check ran before `'off only'`, allowing `tp.gd.hidden_tab` to accept `'on'`. | Reordered check so `range === 'off only'` executes before general toggle check, strictly rejecting `'on'`. | `web/pages/tests/web.test.mjs` (`validates 'off only' strictly and rejects 'on' even when type is toggle (WWPP-R30.1)`). |
| 14 | **schema.js:511**<br>Unknown duration ranges fell through and accepted invalid strings like `'banana'`. | Added explicit duration validation regex `^(\d+)\s*(h\|d\|m\|s)$` for `range === '1h-365d'` and `row.type === 'duration'`. | `web/pages/tests/web.test.mjs` (`validates duration ranges and rejects invalid patterns like banana`). |
| 15 | **schema.js:497-505**<br>Fractional numbers passed numeric ranges (e.g. 3.5 workers). | Enforced `Number.isInteger(num)` for numeric count types. | `web/pages/tests/web.test.mjs` (`validates integer worker counts and rejects fractional numbers`). |
| 16 | **schema.js:480 & app.js:355**<br>Multi-select rows validated as single enum and rendered as single `<select>`. | Added multi-select array validation in `schema.js` and rendered multi-select rows as checkbox groups in `app.js`. | `web/pages/tests/web.test.mjs` (`validates multi-select arrays and comma-separated lists`) & `security.test.mjs` (`renderFormField renders multi-select rows as multiple checkboxes`). |
| 17 | **app.js:420-439**<br>Save proceeded while errors were visible; no double-submit guard. | Added pre-save validation checking all `[id^="err-"]` and schema rules; disabled save button in-flight. | `web/pages/tests/security.test.mjs` (`blocks save and shows validation error when active field errors exist`, `prevents double-submit by disabling save button in flight`). |
| 18 | **app.js:355-364**<br>Enum rows with empty/TBD value displayed first option while setting was `''`. | Rendered disabled `<option value="" disabled selected>-- Select --</option>` placeholder when enum setting is empty. | `web/pages/tests/security.test.mjs` (`renderFormField renders enum placeholder when setting value is empty/TBD`). |
| 19 | **mock.js:248-283**<br>`handleMockPost` ignored `expectedVersion`, skipping conflict detection. | Throws RFC 9457 HTTP 409 `ProblemError` when `expectedVersion !== entry.currentVersion`. | `web/pages/tests/web.test.mjs` (`honours expectedVersion and detects version conflicts in handleMockPost (WWPP-R38.6)`). |
| 20 | **app.js:457**<br>Inline `onclick` with interpolated version in version table (CSP-hostile). | Replaced inline `onclick` with `data-rollback-version` and attached DOM event listeners. | `web/pages/tests/security.test.mjs` (`renderConfigVersionTable has zero inline onclick attributes (CSP compliance)`). |
| 21 | **app.js:220, 381**<br>Focusable tooltips lacked `aria-describedby` links and error inputs lacked `aria-invalid`. | Linked inputs to tooltips and error alerts via `aria-describedby`; toggled `aria-invalid="true"` on invalid inputs. | `web/pages/tests/security.test.mjs` (`renderFormField wires aria-describedby for tooltips and error messages`). |
| 22 | **app.js:216**<br>Metric card `<a>` wrapped focusable tooltip `<span>` (nested interactive content). | Separated card into structured `div.metric-card` with independent anchor links and external tooltip button. | `web/pages/tests/security.test.mjs` (`renderMetricCard places tooltip outside the interactive anchor tag (WWPP-R40.1)`). |

---

## 2. File Mod Logs & Header/Footer Compliance

Only the permitted files were edited:
- `web/pages/js/api.js` (Maintained `WO-HC-WEB-01` header & `// END OF api.js` footer)
- `web/pages/js/app.js` (Maintained `WO-HC-WEB-01` header & `// END OF app.js` footer)
- `web/pages/js/schema.js` (Maintained `WO-HC-WEB-01` header & `// END OF schema.js` footer)
- `web/pages/js/mock.js` (Maintained `WO-HC-WEB-01` header & `// END OF mock.js` footer)
- `web/pages/tests/pages.test.mjs` (Maintained `WO-T2.2` header & `// END OF pages.test.mjs` footer)
- `web/pages/tests/web.test.mjs` (Maintained `WO-HC-WEB-01` header & `// END OF web.test.mjs` footer)
- `web/pages/tests/security.test.mjs` (New test suite, `WO-HC-WEB-01` header & `// END OF security.test.mjs` footer)

---

## 3. Test Suites Execution

Running:
```bash
node --test web/pages/tests/pages.test.mjs web/pages/tests/web.test.mjs web/pages/tests/security.test.mjs
```
Output:
```
# tests 59
# suites 15
# pass 59
# fail 0
# cancelled 0
# skipped 0
# todo 0
```

DONE FIXF
