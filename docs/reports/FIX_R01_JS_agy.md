# FIX-R01-JS Resolution Report

## Executive Summary
All high and medium findings identified in `docs/reviews/REVIEW_01_agy_20261117.md` for `web/pages/runner.html`, `web/pages/js/runner.js`, and `web/pages/js/contribute.js` have been fixed completely. Unit and conformance tests were added to `web/pages/tests/contribute.test.mjs`. All 51 tests across `contribute.test.mjs` and `web.test.mjs` pass cleanly.

## Finding -> Fix -> Test Mapping

| Finding (file:line / sev) | Problem | Fix | Test Name |
|---|---|---|---|
| `web/pages/runner.html:12` (high) | `<script type="module" src="./js/runner.js">` inside `sandbox="allow-scripts"` iframe has opaque origin `null` and is blocked by CORS | Inlined the runner execution script into `<script type="module">` inside `runner.html` to execute without cross-origin network fetches | `verifies runner.html inlines runner script to avoid opaque sandbox CORS error` |
| `web/pages/js/runner.js:132` (high) | Handshake does not check `ev.source === parent` or validate `ev.origin`, allowing arbitrary frames to hijack runner MessagePort | Enforced strict `ev.source === parent` check and referrer-based origin verification in `handleBootstrapMessage` and window listener | `handshake strictly requires ev.source === parent before accepting port transfer` |
| `web/pages/js/runner.js:73` (high) | `verifyEnvelope` signs/verifies bare canonical JSON without domain separator, diverging from Go backend `CanonicalBytes()` | Implemented `envelopeSigningBytes` prepending `ccgsc.pools.task-envelope.v1\0` to canonical JSON before Ed25519 signing and verification | `envelope adheres to Go canonical form with domain prefix ccgsc.pools.task-envelope.v1\0` |
| `web/pages/js/contribute.js:117` (high) | `createTaskEnvelope` schema (`leaseExp`, missing `version`/`issuedAt`/`keyId`) incompatible with Go `TaskEnvelope` struct | Aligned JS envelope schema with Go `TaskEnvelope` struct: `version`, `jobId`, `taskId`, `artifactSha256`, `inputSha256`, `issuedAt`, `expiresAt`, `keyId` | `createTaskEnvelope aligns with Go TaskEnvelope struct fields` |
| `web/pages/js/contribute.js:459` (high) | `checkTaskSource` and `getNextJob` query non-existent `/thread-pool/v1/tasks/next`, forcing contributor page into MOCK mode | Updated client to use standard worker endpoints `POST /thread-pool/v1/workers/${this.tabId}/leases` and `POST /thread-pool/v1/workers/${this.tabId}/results`, preserving MOCK fallback | `contributor client uses standard worker endpoints without invented /tasks/next` |
| `web/pages/js/runner.js:237` (high) | Runner calculates `workersToSpawn` based on cap, but always instantiates only a single `Worker` | Implemented worker pool instantiation spawning `workersToSpawn` workers in parallel per job request | `runner calculates and spawns workersToSpawn workers based on worker cap` |
| `web/pages/js/runner.js:239` (high) | Worker task execution has no timeout mechanism; infinite loops or hung WASM code hang runner forever | Wrapped worker execution in `Promise.race` with deadline (`timeoutMs`), terminating all workers and reporting `task_timeout` without hung fallback | `runner enforces per-task timeout and terminates workers upon timeout` |
| `web/pages/js/runner.js:43` (med) | `verifyEnvelope` does not check lease expiration (`expiresAt` / `leaseExp`), allowing expired task envelopes to run | Added expiration timestamp check `Date.now() / 1000 > envelope.expiresAt`, failing closed with `error: 'lease_expired'` | `verifyEnvelope verifies lease expiration against expiresAt` |
| `web/pages/js/runner.js:56` (med) | `verifyEnvelope` skips artifact hash verification if `wasmBytes` is missing instead of failing closed | Added missing/empty byte buffer check failing closed with `error: 'missing_wasm_bytes'` | `verifyEnvelope fails closed with missing_wasm_bytes when bytes missing or empty` |
| `web/pages/js/contribute.js:527` (med) | `setupRunnerFrame` attaches `load` listener to iframe after DOM is parsed; early load causes handshake drop | Added readyState inspection on `frame.contentDocument` to trigger immediate handshake if iframe is already loaded | `setupRunnerFrame handles early iframe load race condition without dropping handshake` |
| `web/pages/js/runner.js:105` (low) | `WORKER_SRC` uses `d.n || 1000` which coerces `n: 0` to 1000 | Replaced falsy coercion with `typeof d.n === 'number' ? d.n : 1000` to preserve integer 0 | `preserves WORKER_SRC integer zero value without falsy coercion` |

## Test Verification Output

Command: `node --test web/pages/tests/contribute.test.mjs web/pages/tests/web.test.mjs`

```
▶ WO_T3_CONTRIBUTE_PAGE Test Suite
  ▶ 1. Worker-Count & Allocation Rules (WWPP-R29.4, WWPP-R30.2, WWPP-R39.1)
    ✔ computes auto worker count for desktop systems (cores - 1, min 1, max 4) (0.8148ms)
    ✔ handles missing or zero hardwareConcurrency gracefully (0.2187ms)
    ✔ computes mobile phone worker count (1 on battery, 2 when charging) (0.138ms)
    ✔ enforces runner worker cap (WWPP-R39.1) (0.1614ms)
    ✔ accurately identifies phone user agents (0.2884ms)
  ✔ 1. Worker-Count & Allocation Rules (WWPP-R29.4, WWPP-R30.2, WWPP-R39.1) (3.2741ms)
  ▶ 2. Cryptographic Envelope & Pre-Execution Verification (WWPP-R37.1)
    ✔ canonicalJson produces sorted deterministic JSON strings (0.3547ms)
    ✔ computes correct SHA-256 hex digest on WASM bytes (9.6171ms)
    ✔ signs and verifies a valid task envelope with Ed25519 test key (4.1235ms)
    ✔ fails closed when envelope signature is tampered (1.5801ms)
    ✔ fails closed when envelope content is modified after signing (8.7515ms)
    ✔ fails closed when WASM bytes do not match envelope artifactSha256 (1.2479ms)
    ✔ fails closed when task lease has expired (0.9275ms)
  ✔ 2. Cryptographic Envelope & Pre-Execution Verification (WWPP-R37.1) (27.2052ms)
  ▶ 3. MessageChannel Protocol & Message Schema Validation (WWPP-R39.1)
    ✔ validates init message schema (0.2339ms)
    ✔ validates ready message schema (0.0685ms)
    ✔ validates job message schema (0.1228ms)
    ✔ validates result message schema (0.1139ms)
    ✔ validates check diagnostic message schema (0.0872ms)
    ✔ validates error and ping/pong schemas (0.0839ms)
  ✔ 3. MessageChannel Protocol & Message Schema Validation (WWPP-R39.1) (0.8581ms)
  ▶ 4. File Integrity & Architectural Conformance Smoke Checks
    ✔ verifies all 6 required files exist (0.3528ms)
    ✔ verifies contribute.html conforms to architecture decisions (1.0473ms)
    ✔ verifies runner.html conforms to sandbox runner design (0.9715ms)
    ✔ verifies manual/contribute.html contains complete manual documentation (0.9517ms)
    ✔ verifies JS modules have proper headers and footers (0.9571ms)
  ✔ 4. File Integrity & Architectural Conformance Smoke Checks (4.4844ms)
  ▶ 5. WO_FIX_REVIEW01_JS Regression & Conformance Tests
    ✔ verifies runner.html inlines runner script to avoid opaque sandbox CORS error (1.0852ms)
    ✔ handshake strictly requires ev.source === parent before accepting port transfer (0.3751ms)
    ✔ envelope adheres to Go canonical form with domain prefix ccgsc.pools.task-envelope.v1\0 (0.1662ms)
    ✔ createTaskEnvelope aligns with Go TaskEnvelope struct fields (0.717ms)
    ✔ verifyEnvelope fails closed with missing_wasm_bytes when bytes missing or empty (11.8989ms)
    ✔ verifyEnvelope verifies lease expiration against expiresAt (1.5934ms)
    ✔ runner calculates and spawns workersToSpawn workers based on worker cap (3.2302ms)
    ✔ runner enforces per-task timeout and terminates workers upon timeout (29.7115ms)
    ✔ contributor client uses standard worker endpoints without invented /tasks/next (2.1723ms)
    ✔ setupRunnerFrame handles early iframe load race condition without dropping handshake (50.5743ms)
    ✔ preserves WORKER_SRC integer zero value without falsy coercion (0.1358ms)
  ✔ 5. WO_FIX_REVIEW01_JS Regression & Conformance Tests (102.5196ms)
✔ WO_T3_CONTRIBUTE_PAGE Test Suite (138.9812ms)
▶ WO-HC-WEB-01 Test Suite
  ▶ 1. Schema Row Counts & Field Completeness (Truth: ADMIN_MANUALS_SOURCE.md)
    ✔ verifies all 7 pages exist in PAGE_SCHEMAS with exact row counts (0.8358ms)
    ✔ verifies every single row contains all mandatory schema attributes (0.8627ms)
    ✔ verifies all 34 monitor metrics map to exact detail anchors (WWPP-R40.1) (0.2066ms)
    ✔ verifies every row has a one-line legend tooltip text (0.3531ms)
  ✔ 1. Schema Row Counts & Field Completeness (Truth: ADMIN_MANUALS_SOURCE.md) (3.1653ms)
  ▶ 2. Input Range Validation Engine
    ✔ validates numeric ranges accurately (0.4897ms)
    ✔ validates percentage ranges accurately (0.1857ms)
    ✔ validates enum values strictly (4.456ms)
    ✔ validates toggle on/off states (0.154ms)
    ✔ validates string character length bounds (0.1581ms)
  ✔ 2. Input Range Validation Engine (5.7531ms)
  ▶ 3. Mock System & API Layer (?mock=1)
    ✔ generates complete mock metrics with capacity, work, trust, API and system counters (1.4328ms)
    ✔ saves a new configuration version with audit reason and increments version number (0.3655ms)
    ✔ performs one-click rollback by creating a new audited version copying target settings (0.2107ms)
    ✔ starts and closes simulated SSE live metric event stream (211.6739ms)
    ✔ enforces required changelog reason on saveConfig per WWPP-R38.6 (1.0909ms)
  ✔ 3. Mock System & API Layer (?mock=1) (215.0416ms)
  ▶ 4. Web Files Smoke Verification (String & Structure Checks)
    ✔ verifies all 7 main HTML pages exist and contain mandatory structural tags (6.9855ms)
    ✔ verifies all 7 manual reference HTML pages exist and match schema rows (5.4918ms)
    ✔ verifies shared CSS and JS files exist with required Header and END footers (3.8639ms)
  ✔ 4. Web Files Smoke Verification (String & Structure Checks) (16.5224ms)
✔ WO-HC-WEB-01 Test Suite (241.9473ms)
ℹ tests 51
ℹ suites 11
ℹ pass 51
ℹ fail 0
ℹ cancelled 0
ℹ skipped 0
ℹ todo 0
ℹ duration_ms 360.2476
```

DONE FIXJS
