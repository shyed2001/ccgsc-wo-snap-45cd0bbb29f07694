// Path: web/pages/js/contribute_page_T104.js | Author: Claude Opus 5.5 head | 2026-11-21
// What/why: T104 step 7 - the ONE startContributorPage() entry of the integrated
// contributor page, plus the owner layout decision of 2026-11-21 (layout = "All")
// and the step 10 cutover it enables.
//
// One bootstrap wires everything the plan lists, in one place:
//   - the shared WorkerBudget (worker_budget_T104.js) with the CPU + GPU total of
//     computeTabBudget, so every lane of the tab lives inside one ceiling,
//   - the one StatusLog (status_log_T104.js) attached to the one log box,
//   - the LegacyLaneAdapter (legacy_lane_adapter_T104.js),
//   - the artifact cache (artifact_cache_T104.js),
//   - the layout switch (layout_T104.js): top | tabs | pools,
//   - ContributeApp (auto-start rules, ?autostart=0, ?pools=0),
//   - the legacy lane router, when the layout runs the legacy area.
//
// Layout contract (owner 2026-11-21, "All"):
//   top   (default): new pools panel first, one status/log area, one worker
//         budget, the legacy lanes in a collapsible section.
//   tabs  : two tabs "Pools" and "Legacy lanes"; only the visible tab runs heavy
//           work; the budget is shared.
//   pools : pools only; the legacy lanes are hidden and never started.
// Selection: ?layout=top|tabs|pools, else localStorage ccgsc_layout, else top.
// An unknown value is top (T104c row 3: an unusable ?layout= is an unknown
// effective value, not a fallback to the stored one).
//
// Nothing here is mandatory for the old lanes: every import is dynamic and the
// old lane router is only imported when the layout asks for it, so a down
// pools-core never breaks them (T104 step 5 rule, carried forward).
//
// Truth: WO_T104_STEPS_6_10.md, docs/T104_INTEGRATED_PAGE_PLAN.md (steps 7-10),
// owner T104 rules (issue #117), GRILL_WPP_R1.md (R29.4, R30.2, R42.5, R42.6).

'use strict';

import { createStatusLog } from './status_log_T104.js';
import { computeTabBudget, WorkerBudget } from './worker_budget_T104.js';
import { createLegacyLaneAdapter } from './legacy_lane_adapter_T104.js';
import { createArtifactCache } from './artifact_cache_T104.js';
import {
  LAYOUTS,
  DEFAULT_LAYOUT,
  LAYOUT_STORAGE_KEY,
  TAB_POOLS,
  TAB_LEGACY,
  normalizeLayout,
  resolveLayout,
  persistLayout,
  computeAreaRunState,
  createLayoutSwitch
} from './layout_T104.js';

// The module gate of the one entry (mirrors the bridge gate of T103).
/** @type {'idle'|'pending'|'started'} */
let pageGate = 'idle';

// ------------------------------------------------------------------ Legacy lane mount

/**
 * The old lane router of the prime repo. It is served next to the root page
 * (the old page imported './worker_lane_multi.mjs'), so the default is the same
 * absolute path; a caller may override it (tests, or a deployment that serves
 * another path).
 *
 * @type {string}
 */
export const LEGACY_LANE_ROUTER_SPEC = '/worker_lane_multi.mjs';

/**
 * Mount the legacy lane router. The router keeps its own WebSocket transport,
 * lane modules and lifecycle; the adapter only gives it slots and log lines.
 * A missing router is an honest "legacy lanes unavailable" line, never an
 * exception: the new pools keep working.
 *
 * @param {Object} params
 * @param {Function} params.importModule
 * @param {Object} params.adapter the legacy lane adapter
 * @param {Object} params.statusLog
 * @param {string} [params.search]
 * @param {Object} [params.pool] the WorkerPool instance of the page
 * @param {Function} [params.fetchImpl]
 * @param {Function} [params.setStatus]
 * @param {Function} [params.onLaneMode]
 * @param {string} [params.spec]
 * @returns {Promise<{started: boolean, reason?: string, error?: any}>}
 */
export async function mountLegacyLanes(params = {}) {
  const importModule = params.importModule || ((spec) => import(spec));
  const spec = String(params.spec || LEGACY_LANE_ROUTER_SPEC);
  const source = params.adapter ? params.adapter.owner : 'legacy:router';
  let mod;
  try {
    mod = await importModule(spec);
  } catch (err) {
    if (params.statusLog) {
      params.statusLog.log(source, 'legacy lane router unavailable; the new pools keep running');
    }
    return { started: false, reason: 'router_unavailable', error: err };
  }
  const start = mod && (mod.startWorkerLanes || mod.startWorkerLane);
  if (typeof start !== 'function') {
    if (params.statusLog) {
      params.statusLog.log(source, 'legacy lane router exports no startWorkerLanes; nothing mounted');
    }
    return { started: false, reason: 'router_missing_entry' };
  }
  try {
    await start({
      search: params.search !== undefined ? params.search : '',
      pool: params.pool || null,
      fetchImpl: params.fetchImpl,
      importModule,
      setStatus: params.setStatus,
      onLaneMode: params.onLaneMode,
      adapter: params.adapter || null
    });
    return { started: true };
  } catch (err) {
    if (params.statusLog) {
      params.statusLog.log(source, 'legacy lane router failed to start; the new pools keep running');
    }
    return { started: false, reason: 'router_start_failed', error: err };
  }
}

// ------------------------------------------------------------------ Budget of the tab

/**
 * The ceiling of this tab as one number. Since step 8 the budget covers CPU and
 * GPU lanes together, so computeTabBudget(...).total is the ceiling every lane
 * of the tab is measured against.
 *
 * @param {Object} [params]
 * @returns {number}
 */
export function tabBudgetTotal(params = {}) {
  const budget = computeTabBudget(params);
  const total = Math.floor(Number(budget.total));
  return Number.isFinite(total) && total >= 1 ? total : 1;
}

/**
 * The sha256 implementation of the page artifact cache, loaded from the app
 * module on the first read. The app owns the digest; the entry injects it at
 * construction, so the production cache re-hashes a stored entry on read instead
 * of serving bytes it cannot vouch for (T104c row 2). The import stays lazy,
 * like every other import of this entry: a visit that never reads the cache
 * (or has the new pools off) never loads the app module for it.
 *
 * @param {Function} importModule
 * @returns {Function} async (bytes) => hex digest
 */
function pageSha256Hex(importModule) {
  let pending = null;
  return function sha256HexOfPage(input) {
    if (!pending) {
      pending = Promise.resolve()
        .then(() => importModule('./contribute.js'))
        .then((mod) => {
          const fn = mod && mod.sha256Hex;
          if (typeof fn !== 'function') throw new Error('contribute module exports no sha256Hex');
          return fn;
        })
        .catch((err) => {
          pending = null;
          throw err;
        });
    }
    return pending.then((fn) => fn(input));
  };
}

// ------------------------------------------------------------------ Page switches

/**
 * Whether the new pools are off for this visit. `?pools=0` is the long-standing
 * new-pools off switch (T103); no layout turns the new pools off.
 *
 * @param {string} [search]
 * @returns {boolean}
 */
export function poolsDisabledBySearch(search) {
  if (typeof search !== 'string' || !search) return false;
  try {
    return new URLSearchParams(search).get('pools') === '0';
  } catch (_) {
    return false;
  }
}

/**
 * The pool ids of the `pools` query switch ("?pools=tp,pp").
 *
 * The page only splits the list; the app validates the names (resolvePools of
 * contribute.js) and falls back to the thread pool when none is valid. The off
 * switch `?pools=0` and an empty value name no pool, so the app keeps its
 * default.
 *
 * @param {string} [search]
 * @returns {string[]|undefined}
 */
export function poolsFromQuery(search) {
  if (typeof search !== 'string' || !search) return undefined;
  try {
    const raw = new URLSearchParams(search).get('pools');
    if (!raw || raw === '0') return undefined;
    const list = raw.split(',').map((name) => String(name).trim().toLowerCase()).filter(Boolean);
    return list.length > 0 ? list : undefined;
  } catch (_) {
    return undefined;
  }
}

/**
 * Read the switches of this visit once, so the bootstrap and ContributeApp can
 * never disagree about them.
 *
 * @param {string} [search]
 * @param {Object} [storage] injectable storage for the remembered layout
 * @param {string} [forcedLayout] a page that has no legacy area (the standalone
 *   /pools/contribute.html) pins its layout instead of reading the visitor one
 * @returns {{search: string, layout: string, poolsOff: boolean, pools: string[]|undefined, autoStart: boolean}}
 */
export function readPageSwitches(search, storage, forcedLayout) {
  const query = typeof search === 'string'
    ? search
    : (typeof window !== 'undefined' && window.location && typeof window.location.search === 'string'
      ? window.location.search
      : '');
  const poolsOff = poolsDisabledBySearch(query);
  // T104: the integrated page honours ?pools= exactly like the standalone page.
  // Without this the root page always registered in the thread pool only, so the
  // process pool was never used there (R10-FIX minimax BLOCKER).
  const pools = poolsOff ? undefined : poolsFromQuery(query);
  let autoStart = true;
  if (query) {
    try {
      if (new URLSearchParams(query).get('autostart') === '0') autoStart = false;
    } catch (_) { /* a bad query does not disable auto-start */ }
  }
  const forced = normalizeLayout(forcedLayout);
  return {
    search: query,
    layout: forced || resolveLayout(query, storage),
    poolsOff,
    pools,
    autoStart
  };
}

// ------------------------------------------------------------------ The anonymous opt-in

/** The opt-in probe of the root page aborts after this many milliseconds. */
export const RUNNER_CONFIG_TIMEOUT_MS = 3000;

/**
 * GET /pools/runner-config with a hard abort. A hung fetch cannot stall the
 * page. Answers the same shapes as the T103 bridge, so the opt-in rule is
 * unchanged: a timeout, a failure and a non-ok answer are all "off".
 *
 * @param {Function} fetchImpl
 * @param {number} timeoutMs
 * @returns {Promise<{kind: 'response'|'timeout'|'error', response?: any, error?: any}>}
 */
export async function probeRunnerConfig(fetchImpl, timeoutMs = RUNNER_CONFIG_TIMEOUT_MS) {
  if (typeof fetchImpl !== 'function') return { kind: 'error', error: new Error('no fetch') };
  const limit = Number.isFinite(timeoutMs) && timeoutMs > 0 ? timeoutMs : RUNNER_CONFIG_TIMEOUT_MS;
  const controller = typeof AbortController === 'function' ? new AbortController() : null;
  let timedOut = false;
  let timer = null;
  const timeout = new Promise((resolve) => {
    timer = setTimeout(() => {
      timedOut = true;
      try { if (controller) controller.abort(); } catch (_) { /* already aborted */ }
      resolve({ kind: 'timeout' });
    }, limit);
  });
  const call = Promise.resolve()
    .then(() => fetchImpl('/pools/runner-config', controller ? { signal: controller.signal } : {}))
    .then((response) => ({ kind: 'response', response }), (error) => ({ kind: timedOut ? 'timeout' : 'error', error }));
  try {
    return await Promise.race([call, timeout]);
  } finally {
    clearTimeout(timer);
  }
}

/**
 * Whether this deployment allows a contributor tab to join without a login.
 * The rule of T103, carried into the one entry: only boolean true is on, every
 * missing key, {}, false, "true", 1 and an error JSON is off.
 *
 * @param {Object} [config]
 * @returns {boolean}
 */
export function isAnonymousJoinOn(config) {
  return Boolean(config && typeof config === 'object' && config.anonymousJoin === true);
}

/**
 * The opt-in gate of the new pools on the root page: ask GET
 * /pools/runner-config and answer whether this tab may join. A down or slow
 * pools-core keeps the old lanes working and leaves the new pools off.
 *
 * @param {Function} fetchImpl
 * @param {number} [timeoutMs]
 * @returns {Promise<{allowed: boolean, reason: string, config?: Object|null}>}
 */
export async function resolveNewPoolsOptIn(fetchImpl, timeoutMs) {
  const probed = await probeRunnerConfig(fetchImpl, timeoutMs);
  if (!probed || probed.kind === 'timeout') {
    return { allowed: false, reason: 'runner_config_timeout' };
  }
  if (probed.kind !== 'response') return { allowed: false, reason: 'runner_config_fetch_failed' };
  const response = probed.response;
  if (!response || !response.ok) return { allowed: false, reason: 'runner_config_not_ok' };
  let config;
  try {
    config = await response.json();
  } catch (_) {
    return { allowed: false, reason: 'runner_config_parse_failed' };
  }
  if (!isAnonymousJoinOn(config)) return { allowed: false, reason: 'anonymous_join_off', config };
  return { allowed: true, reason: 'anonymous_join_on', config };
}

// ------------------------------------------------------------------ One entry

/**
 * Start the integrated contributor page.
 *
 * The wiring is best effort in the order the plan lists, and every piece is
 * independent: a missing element, a blocked IndexedDB, a down pools-core or a
 * missing legacy router costs that piece, never the page.
 *
 * @param {Object} [opts]
 * @param {string} [opts.search] page query override
 * @param {Object} [opts.doc] document override
 * @param {Object} [opts.storage] web storage override (layout memory)
 * @param {Object} [opts.navigator] navigator override
 * @param {Function} [opts.fetch]
 * @param {Function} [opts.importModule]
 * @param {Object} [opts.indexedDB]
 * @param {Function} [opts.sha256Hex] digest of the artifact cache; the app
 *   module's sha256Hex is injected when this is absent
 * @param {Object} [opts.pool] the WorkerPool of the root page
 * @param {Function} [opts.onLaneMode]
 * @param {Object} [opts.globalTarget] globalThis / window
 * @param {string} [opts.legacyRouterSpec]
 * @param {string} [opts.forcedLayout] a page with no legacy area pins its layout
 * @param {boolean} [opts.isPhone] device hint when the navigator does not say
 * @param {number} [opts.gpuSlots] GPU lanes the host already knows about; the
 *   app grows the ceiling to the adapters it discovers when this is absent
 * @param {boolean} [opts.optInGate] ask GET /pools/runner-config first and stay
 *   off unless it says anonymousJoin === true (the root page of T103)
 * @param {number} [opts.runnerConfigTimeoutMs]
 * @param {Object} [opts.ContributeApp] constructor override (tests)
 * @returns {Promise<Object>} the started page
 */
export async function startContributorPage(opts = {}) {
  if (pageGate !== 'idle') {
    return { started: false, reason: 'already_started' };
  }
  pageGate = 'pending';
  // This entry owns the page controller while it boots: the standalone auto-boot
  // block of contribute.js must stay out, whatever the load order of the two
  // modules. The flag is a claim, not a success mark - the catch below hands it
  // back when the entry fails, so the fallback boot is not lost.
  if (typeof window !== 'undefined' && window) window.ccgscPageBooted = true;
  try {
    const result = await runContributorPage(opts);
    pageGate = result && result.started ? 'started' : 'idle';
    return result;
  } catch (err) {
    pageGate = 'idle';
    // The entry never owned the page: clear the claim so the standalone boot of
    // contribute.js can still construct the app when the DOMContentLoaded of
    // that module has not run yet.
    if (typeof window !== 'undefined' && window) window.ccgscPageBooted = false;
    const doc = opts.doc !== undefined
      ? opts.doc
      : (typeof document !== 'undefined' ? document : null);
    const log = elementById(doc, 'log-output');
    if (log) log.textContent = `contributor page error: ${err && err.message ? err.message : String(err)}`;
    return { started: false, reason: 'unhandled_error', error: err };
  }
}

/**
 * Test-only. node:test reuses the module, so a suite resets the one-shot gate.
 */
export function resetContributorPageForTests() {
  pageGate = 'idle';
}

function elementById(doc, id) {
  if (!doc || typeof doc.getElementById !== 'function') return null;
  try { return doc.getElementById(id) || null; } catch (_) { return null; }
}

async function runContributorPage(opts) {
  const doc = opts.doc !== undefined
    ? opts.doc
    : (typeof document !== 'undefined' ? document : null);
  const globalTarget = opts.globalTarget !== undefined
    ? opts.globalTarget
    : (typeof window !== 'undefined' ? window : (typeof globalThis !== 'undefined' ? globalThis : null));
  const storage = opts.storage;
  const importModule = opts.importModule || ((spec) => import(spec));
  const fetchImpl = opts.fetch || (typeof globalThis !== 'undefined' && typeof globalThis.fetch === 'function'
    ? globalThis.fetch.bind(globalThis)
    : null);
  const nav = opts.navigator !== undefined
    ? opts.navigator
    : (typeof navigator !== 'undefined' ? navigator : null);
  const switches = readPageSwitches(opts.search, storage, opts.forcedLayout);

  // 1. The one status/log area. The log box of the page is its only owner.
  const statusLog = createStatusLog();
  const logEl = elementById(doc, 'log-output');
  if (logEl) statusLog.attach(logEl);
  if (globalTarget) globalTarget.ccgscStatusLog = statusLog;

  // 2. One worker budget for the whole tab: CPU + GPU lanes together (step 8).
  let isCharging = false;
  let hasBattery = false;
  let level = undefined;
  try {
    if (nav && typeof nav.getBattery === 'function') {
      const battery = await nav.getBattery();
      hasBattery = true;
      isCharging = Boolean(battery && battery.charging);
      level = typeof (battery && battery.level) === 'number' ? battery.level : undefined;
    }
  } catch (_) { /* no Battery API: desktop default */ }
  const total = tabBudgetTotal({
    hardwareConcurrency: nav ? nav.hardwareConcurrency : undefined,
    isPhone: Boolean(globalTarget && globalTarget.CCGSC_IS_PHONE) || opts.isPhone === true,
    isCharging,
    hasBattery,
    level,
    gpuSlots: opts.gpuSlots
  });
  const budget = new WorkerBudget({ total });
  if (globalTarget) globalTarget.ccgscSharedBudget = budget;

  // 3. The legacy lane adapter and its hooks. The old lanes read these; a lane
  //    that never calls the adapter behaves exactly as before.
  const adapter = createLegacyLaneAdapter({
    laneMode: (globalTarget && globalTarget.CCGSC_LANE_MODE) || 'generic',
    budget,
    statusLog
  });
  if (globalTarget) adapter.exposeOn(globalTarget);

  // 4. The artifact cache (step 6). Storage is optional: a blocked IndexedDB
  //    degrades to the in-memory cache. The digest is injected here (T104c row
  //    2): without it the production cache stored bytes it never re-hashed, so
  //    the read path could serve an entry it cannot vouch for. Tests may inject
  //    their own implementation.
  const artifactCache = createArtifactCache({
    indexedDB: opts.indexedDB,
    sha256Hex: typeof opts.sha256Hex === 'function' ? opts.sha256Hex : pageSha256Hex(importModule)
  });
  if (globalTarget) globalTarget.ccgscArtifactCache = artifactCache;

  // 5. The new pools. ?pools=0 turns them off; otherwise the app is built now
  //    and started by the layout (a hidden area runs no heavy work). On the root
  //    page the T103 opt-in still decides: the tab joins the new pools only when
  //    GET /pools/runner-config says anonymousJoin === true, so a deployment with
  //    anonymous join off keeps the old lanes and no error panel.
  let poolsApp = null;
  let optIn = { allowed: true, reason: 'opt_in_not_required' };
  if (!switches.poolsOff && opts.optInGate === true) {
    optIn = await resolveNewPoolsOptIn(fetchImpl, opts.runnerConfigTimeoutMs);
    if (!optIn.allowed) {
      statusLog.log('pools', `new pools off: ${optIn.reason}`);
      statusLog.setStatus('pools', 'OFF');
    }
  }
  const newPoolsOn = !switches.poolsOff && optIn.allowed;
  if (newPoolsOn) {
    try {
      const mod = opts.ContributeApp
        ? { ContributeApp: opts.ContributeApp }
        : await importModule('./contribute.js');
      const ContributeAppClass = mod && (mod.ContributeApp || mod.default);
      if (typeof ContributeAppClass === 'function') {
        poolsApp = new ContributeAppClass({
          search: switches.search,
          // R10-FIX minimax BLOCKER: the pools of the visit reach the app, so the
          // root page registers in the process pool too (?pools=tp,pp).
          pools: switches.pools,
          // R10-FIX (chatgpt MAJOR): the per-tab identity, same rule as the host
          // id: the app only reads what the bootstrap persisted.
          browserTabId: (globalTarget && globalTarget.ccgscBrowserTabId) || undefined,
          // The page decides when the new pools compute: the tab must register
          // and stay idle when the legacy area is the visible one, and
          // ?autostart=0 keeps it idle for the whole visit.
          autoStart: false,
          budget,
          statusLog,
          artifactCache
        });
        if (globalTarget) {
          globalTarget.ccgscPoolsApp = poolsApp;
          globalTarget.ccgscPoolsActiveTasks = () => (poolsApp.activeTasks && poolsApp.activeTasks.size) || 0;
          if (globalTarget.ccgscContributorHostId) {
            poolsApp.hostId = String(globalTarget.ccgscContributorHostId);
          }
        }
      } else {
        statusLog.log('pools', 'contribute module exports no ContributeApp; the new pools stay off');
      }
    } catch (err) {
      statusLog.log('pools', 'new pools unavailable: ' + (err && err.message ? err.message : String(err)));
    }
  }

  // 6. The legacy lane router, mounted once and by the layout only. A page
  //    without a legacy area never imports it.
  const hasLegacyArea = normalizeLayout(opts.forcedLayout) !== 'pools';
  const legacy = { started: false, reason: hasLegacyArea ? 'layout_hides_legacy' : 'no_legacy_area', mounted: false };
  async function mountLegacyOnce() {
    if (legacy.mounted) return legacy;
    legacy.mounted = true;
    const mounted = await mountLegacyLanes({
      importModule,
      statusLog,
      adapter,
      spec: opts.legacyRouterSpec,
      search: switches.search,
      pool: opts.pool || (globalTarget && globalTarget.__ccgsc_pool) || null,
      fetchImpl,
      setStatus: (text, klass) => {
        const el = elementById(doc, 'prime-lane-mode');
        if (!el) return;
        el.textContent = text;
        if (klass) el.className = `status-box ${klass}`;
      },
      onLaneMode: (laneMode) => {
        if (globalTarget) globalTarget.CCGSC_LANE_MODE = laneMode;
        try { if (globalTarget) globalTarget.ccgscLaneStatus?.setAssignment(laneMode); } catch (_) { /* panel is optional */ }
        if (typeof opts.onLaneMode === 'function') {
          try { opts.onLaneMode(laneMode); } catch (_) { /* consumer is optional */ }
        }
      }
    });
    legacy.started = Boolean(mounted && mounted.started);
    legacy.reason = (mounted && mounted.reason) || null;
    return legacy;
  }

  // 7. Apply a layout state to the two areas. Only the visible area runs heavy
  //    work; the one budget is shared, it is never resized per layout. A
  //    controller event is async and two quick clicks must not interleave, so
  //    the applies are serialised.
  let applyChain = Promise.resolve();
  function applyRunState(next) {
    applyChain = applyChain
      .then(() => applyRunStateOnce(next))
      .catch(() => next);
    return applyChain;
  }

  async function applyRunStateOnce(next) {
    const poolsShouldRun = Boolean(next.pools) && !switches.poolsOff && switches.autoStart;
    if (poolsApp) {
      if (poolsShouldRun) {
        if (!poolsApp.dom) {
          // init() registers and renders; autoStart is false, so it stays idle
          // until the start() below.
          await poolsApp.init();
        }
        // W10D row 3 (D-W10-11): startStopOptIn opens stopped even though the
        // page would auto-start; its Start button (or a later mode) starts it.
        if (!poolsApp.contributorOptInStopped && !poolsApp.isRunning) await poolsApp.start();
      } else if (poolsApp.isRunning) {
        poolsApp.stop();
      }
    }
    // The legacy lane gate: a hidden area runs no heavy work. The adapter holds
    // the gate the lane router reads (plan step 9), and closing it releases the
    // slots the lane held. A page without a legacy area (the standalone
    // /pools/contribute.html) has no gate to flip and no router to mount.
    if (hasLegacyArea) {
      adapter.setEnabled(Boolean(next.legacy));
      if (next.legacy) await mountLegacyOnce();
    }
    const snap = budget.snapshot();
    statusLog.setStatus('budget', `${snap.used}/${snap.total} slots, ${Object.keys(snap.owners).length} lane(s)`);
    statusLog.setStatus('opt-in', optIn.allowed ? 'on' : 'off');
    return next;
  }

  // 8. The layout switch (owner decision 2026-11-21). One switch, wired to the
  //    apply path above; a click of the visitor reaches the consumers. The
  //    initial state is applied explicitly below, after both consumers exist.
  const layoutSwitch = createLayoutSwitch({
    doc,
    layout: switches.layout,
    emitInitial: false,
    onRunStateChange(next) {
      applyRunState(next);
    },
    onLayoutChange(next) {
      persistLayout(next, storage);
      statusLog.log('page', `layout switched to ${next}`);
    }
  });
  if (globalTarget) globalTarget.ccgscLayoutSwitch = layoutSwitch;

  const state = await applyRunState(layoutSwitch.initial);

  statusLog.log('page', `T104 page ready: layout=${layoutSwitch.getLayout()}, budget=${budget.total()}, pools=${switches.poolsOff ? 'off' : 'on'}`);

  return {
    started: true,
    layout: layoutSwitch.getLayout(),
    state,
    budget,
    statusLog,
    adapter,
    artifactCache,
    app: poolsApp,
    // W10D row 3 (D-W10-11): the mode the app read at boot, for the page host
    // and the tests; the app keeps applying heartbeat changes itself.
    contributorControls: poolsApp ? poolsApp.contributorControls : 'off',
    optIn,
    layoutSwitch,
    legacy,
    switches
  };
}

/**
 * The layout of the standalone /pools/contribute.html page: that page has no
 * legacy area at all, so it never imports the old lane router.
 */
export const STANDALONE_LAYOUT = 'pools';

/**
 * Whether a layout exists by name.
 *
 * @param {string} value
 * @returns {boolean}
 */
export function isKnownLayout(value) {
  return normalizeLayout(value) !== null;
}

export {
  LAYOUTS,
  DEFAULT_LAYOUT,
  LAYOUT_STORAGE_KEY,
  TAB_POOLS,
  TAB_LEGACY,
  computeAreaRunState,
  resolveLayout,
  persistLayout,
  normalizeLayout
};

// END OF contribute_page_T104.js
