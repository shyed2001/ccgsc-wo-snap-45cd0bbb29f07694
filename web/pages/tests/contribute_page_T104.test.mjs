// Path: web/pages/tests/contribute_page_T104.test.mjs | Author: Claude Opus 5.5 head | 2026-11-21
// What/why: T104 steps 7-10 - the one startContributorPage() entry and the layout
// switch: which layout is selected, which area runs heavy work per layout and per
// tab, that the hidden tab starts nothing, and that both areas share ONE worker
// budget (owners "pools" / "pools:gpu" / "legacy:<lane>" never exceed the tab
// ceiling). Every consumer is injected; no network, no real DOM.

import { describe, it, after } from 'node:test';
import assert from 'node:assert/strict';
import {
  startContributorPage,
  resetContributorPageForTests,
  readPageSwitches,
  poolsDisabledBySearch,
  tabBudgetTotal,
  isKnownLayout,
  mountLegacyLanes,
  resolveNewPoolsOptIn,
  isAnonymousJoinOn,
  probeRunnerConfig,
  STANDALONE_LAYOUT,
  LAYOUT_STORAGE_KEY,
  RUNNER_CONFIG_TIMEOUT_MS
} from '../js/contribute_page_T104.js';
import { WorkerBudget, computeTabBudget } from '../js/worker_budget_T104.js';
import { createStatusLog } from '../js/status_log_T104.js';

after(() => {
  resetContributorPageForTests();
});

function mapStorage(initial = {}) {
  const store = new Map(Object.entries(initial));
  return {
    getItem: (key) => (store.has(key) ? store.get(key) : null),
    setItem: (key, value) => { store.set(key, String(value)); },
    removeItem: (key) => { store.delete(key); },
    dump: () => Object.fromEntries(store.entries())
  };
}

function classList() {
  const set = new Set();
  return {
    toggle(name, on) { if (on) set.add(name); else set.delete(name); },
    contains: (name) => set.has(name)
  };
}

function fakeElement() {
  const el = {
    hidden: false,
    open: true,
    style: {},
    attributes: {},
    listeners: {},
    textContent: '',
    classList: classList(),
    setAttribute(name, value) { this.attributes[name] = String(value); },
    addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
    removeEventListener(type, fn) {
      this.listeners[type] = (this.listeners[type] || []).filter((f) => f !== fn);
    },
    fire(type) { for (const fn of this.listeners[type] || []) fn({ type }); }
  };
  return el;
}

function fakeDoc(ids) {
  const nodes = new Map();
  for (const id of ids) nodes.set(id, fakeElement());
  return {
    body: { setAttribute() {} },
    getElementById: (id) => nodes.get(id) || null,
    nodes
  };
}

const ROOT_IDS = [
  'log-output', 'prime-lane-mode',
  't103-pools-panel', 't104-legacy-section', 't104-legacy-summary',
  't104-layout-tabs', 't104-tab-pools', 't104-tab-legacy',
  't104-layout-btn-top', 't104-layout-btn-tabs', 't104-layout-btn-pools'
];

// A controllable ContributeApp double. It records the lifecycle calls the page
// makes and holds the shared budget exactly like the real app.
function makeAppClass(trace) {
  return class FakeApp {
    constructor(options = {}) {
      this.options = options;
      this.budget = options.budget || null;
      this.statusLog = options.statusLog || null;
      this.artifactCache = options.artifactCache || null;
      this.autoStart = options.autoStart;
      this.dom = null;
      this.isRunning = false;
      this.sourceMode = 'LIVE';
      this.activeTasks = new Map();
      this.workerCount = 4;
      this.hwConcurrency = 8;
      this.gpus = [{ vendor: 'v', architecture: 'a' }, { vendor: 'v2', architecture: 'b' }];
      trace.apps.push(this);
    }

    gpuAllowed() { return true; }

    async init() {
      trace.calls.push('init');
      this.dom = {};
      if (!this.budget) return;
      // The real init() registers and leaves claiming; the page starts it later.
    }

    async start() {
      trace.calls.push('start');
      this.isRunning = true;
      this.applyBudgetClaim();
      this.applyGpuBudgetClaim();
    }

    stop() {
      trace.calls.push('stop');
      this.isRunning = false;
      this.releaseBudgetClaim();
      this.releaseGpuBudgetClaim();
    }

    applyBudgetClaim() {
      if (!this.budget) return 0;
      if (!this.isRunning) return this.budget.releaseAll('pools');
      const want = Math.min(this.workerCount, 4);
      const held = this.budget.usedBy('pools');
      if (held > want) return this.budget.release('pools', held - want), want;
      return held + this.budget.claim('pools', want - held);
    }

    releaseBudgetClaim() {
      return this.budget ? this.budget.releaseAll('pools') : 0;
    }

    applyGpuBudgetClaim() {
      if (!this.budget) return 0;
      if (!this.isRunning) return this.budget.releaseAll('pools:gpu');
      // T104 step 8: the page sizes the budget before any adapter exists, so the
      // GPU part grows the ceiling once, when adapters were discovered.
      if (!this.grown) {
        this.grown = true;
        this.budget.resize(4 + Math.min(2, this.gpus.length));
      }
      const want = this.gpuAllowed() ? Math.min(2, this.gpus.length) : 0;
      const held = this.budget.usedBy('pools:gpu');
      if (held > want) return this.budget.release('pools:gpu', held - want), want;
      return held + this.budget.claim('pools:gpu', want - held);
    }

    releaseGpuBudgetClaim() {
      return this.budget ? this.budget.releaseAll('pools:gpu') : 0;
    }
  };
}

function newTrace() {
  return { calls: [], apps: [], legacyRuns: 0 };
}

async function startPage(opts = {}) {
  resetContributorPageForTests();
  const trace = newTrace();
  const doc = opts.doc || fakeDoc(ROOT_IDS);
  const globalTarget = {};
  const storage = opts.storage || mapStorage();
  const result = await startContributorPage({
    doc,
    storage,
    globalTarget,
    navigator: { hardwareConcurrency: 8, getBattery: undefined },
    search: opts.search || '',
    ContributeApp: makeAppClass(trace),
    importModule: async (spec) => {
      if (String(spec).includes('worker_lane_multi')) {
        trace.legacyRuns += 1;
        return {
          startWorkerLanes: async () => { trace.legacyRuns += 1; }
        };
      }
      throw new Error('unexpected import: ' + spec);
    },
    indexedDB: null,
    legacyRouterSpec: './worker_lane_multi.mjs'
  });
  return { result, trace, doc, globalTarget, storage };
}

describe('T104 page switches and the one entry', () => {

  it('reads ?pools=0, ?autostart=0 and the layout once', () => {
    assert.equal(poolsDisabledBySearch('?pools=0'), true);
    assert.equal(poolsDisabledBySearch('?pools=1'), false);
    assert.equal(poolsDisabledBySearch(''), false);
    assert.equal(poolsDisabledBySearch(undefined), false);
    assert.equal(poolsDisabledBySearch('%'), false, 'a bad query is not an off switch');

    const plain = readPageSwitches('?layout=tabs');
    assert.equal(plain.layout, 'tabs');
    assert.equal(plain.poolsOff, false);
    assert.equal(plain.autoStart, true);

    const idle = readPageSwitches('?autostart=0');
    assert.equal(idle.autoStart, false);

    const off = readPageSwitches('?pools=0');
    assert.equal(off.poolsOff, true);

    // The forced layout of the standalone page wins over the query.
    assert.equal(readPageSwitches('?layout=tabs', mapStorage(), 'pools').layout, 'pools');
    assert.equal(readPageSwitches('?layout=bogus', mapStorage(), 'pools').layout, 'pools');
    assert.equal(readPageSwitches('?layout=tabs', mapStorage(), 'bogus').layout, 'tabs', 'an unknown force is ignored');
  });

  it('the tab ceiling is the CPU + GPU total of the shared rule', () => {
    assert.equal(tabBudgetTotal({ hardwareConcurrency: 8 }), computeTabBudget({ hardwareConcurrency: 8 }).total);
    assert.equal(tabBudgetTotal({ hardwareConcurrency: 8, gpuSlots: 2 }), 6);
    assert.equal(tabBudgetTotal({ hardwareConcurrency: 8, gpuSlots: 9 }), 6, 'the GPU cap of 2 holds');
    assert.equal(tabBudgetTotal({ hardwareConcurrency: 2 }), 1);
    assert.equal(tabBudgetTotal({ isPhone: true }), 1);
    assert.equal(tabBudgetTotal({ isPhone: true, isCharging: true }), 2);
    assert.ok(tabBudgetTotal({}) >= 1);
    assert.equal(isKnownLayout('tabs'), true);
    assert.equal(isKnownLayout('nope'), false);
    assert.equal(STANDALONE_LAYOUT, 'pools');
  });

  it('top (default): the new pools run, the open legacy section runs, one budget', async () => {
    const { result, trace, globalTarget } = await startPage();
    assert.equal(result.started, true);
    assert.equal(result.layout, 'top');
    assert.deepEqual(trace.calls, ['init', 'start']);
    assert.equal(result.legacy.started, true);
    assert.equal(trace.legacyRuns, 2, 'the router was imported once and started once');
    assert.ok(result.budget instanceof WorkerBudget);
    assert.equal(globalTarget.ccgscSharedBudget, result.budget);
    assert.equal(globalTarget.ccgscStatusLog, result.statusLog);
    assert.equal(globalTarget.ccgscArtifactCache, result.artifactCache);
    assert.equal(globalTarget.ccgscLegacyLaneAdapter, result.adapter);
    assert.equal(typeof globalTarget.ccgscLegacyLaneEnabled, 'function');
    // Both lanes of the tab share the one ceiling.
    assert.equal(result.budget.usedBy('pools'), 4);
    assert.equal(result.budget.usedBy('pools:gpu'), 2);
    assert.equal(result.budget.used(), 6);
    assert.equal(result.budget.total(), 6);
    assert.deepEqual(result.budget.snapshot().owners, { pools: 4, 'pools:gpu': 2 });
  });

  it('pools: the legacy lanes are hidden and never started', async () => {
    const { result, trace, doc } = await startPage({ search: '?layout=pools' });
    assert.equal(result.layout, 'pools');
    assert.equal(trace.legacyRuns, 0, 'the router is never imported');
    assert.equal(result.legacy.started, false);
    assert.equal(result.legacy.reason, 'layout_hides_legacy');
    assert.equal(doc.nodes.get('t104-legacy-section').hidden, true);
    assert.deepEqual(trace.calls, ['init', 'start'], 'the new pools still compute');
  });

  it('tabs: only the visible tab computes, the budget is shared', async () => {
    const { result, trace, doc } = await startPage({ search: '?layout=tabs' });
    assert.equal(result.layout, 'tabs');
    assert.deepEqual(trace.calls, ['init', 'start'], 'the pools tab is visible');
    assert.equal(trace.legacyRuns, 0, 'the legacy tab has not been shown');
    assert.equal(doc.nodes.get('t104-legacy-section').hidden, true);
    assert.equal(doc.nodes.get('t103-pools-panel').hidden, false);

    // The visitor shows the legacy tab: the pools stop and release, the legacy
    // lane starts. Exactly one area runs.
    const legacyTab = doc.nodes.get('t104-tab-legacy');
    legacyTab.fire('click');
    await new Promise((resolve) => setTimeout(resolve, 20));
    assert.deepEqual(trace.calls, ['init', 'start', 'stop']);
    assert.equal(result.app.isRunning, false);
    assert.equal(result.budget.usedBy('pools'), 0, 'a hidden area holds no slot');
    assert.equal(result.budget.usedBy('pools:gpu'), 0);
    assert.equal(trace.legacyRuns, 2, 'showing the tab mounts the router once');
    assert.equal(result.adapter.isEnabled(), true);

    // And back: the legacy lane is paused and the pools compute again.
    doc.nodes.get('t104-tab-pools').fire('click');
    await new Promise((resolve) => setTimeout(resolve, 20));
    assert.equal(trace.calls.filter((c) => c === 'start').length, 2);
    assert.equal(result.adapter.isEnabled(), false, 'the hidden legacy area is paused');
    assert.equal(result.budget.usedBy('pools'), 4);
  });

  it('a visitor layout click re-applies and is remembered', async () => {
    const storage = mapStorage();
    const { result, trace, doc } = await startPage({ storage });
    doc.nodes.get('t104-layout-btn-tabs').fire('click');
    await new Promise((resolve) => setTimeout(resolve, 20));
    assert.equal(result.layoutSwitch.getLayout(), 'tabs');
    assert.equal(storage.dump()[LAYOUT_STORAGE_KEY], 'tabs', 'the choice survives a reload');
    assert.equal(trace.calls.includes('stop'), false, 'the pools tab stays visible in tabs');
  });

  it('?layout is read from the query, then the stored layout, then top', async () => {
    const stored = mapStorage({ [LAYOUT_STORAGE_KEY]: 'pools' });
    const a = await startPage({ storage: stored });
    assert.equal(a.result.layout, 'pools', 'the remembered pools-only layout');
    assert.equal(a.trace.legacyRuns, 0);

    const b = await startPage({ storage: stored, search: '?layout=tabs' });
    assert.equal(b.result.layout, 'tabs', 'an explicit query wins over the stored value');

    const c = await startPage({ storage: mapStorage({ [LAYOUT_STORAGE_KEY]: 'bogus' }) });
    assert.equal(c.result.layout, 'top', 'an unknown stored value means top');
  });

  it('an unknown ?layout value gives top, not the stored layout', async () => {
    const stored = mapStorage({ [LAYOUT_STORAGE_KEY]: 'pools' });
    const { result, trace } = await startPage({ storage: stored, search: '?layout=bogus' });
    assert.equal(result.layout, 'top', 'the owner rule: an unknown value means top');
    assert.equal(trace.legacyRuns, 2, 'the top layout runs the open legacy section');
    assert.equal(stored.dump()[LAYOUT_STORAGE_KEY], 'pools', 'the stored choice is not rewritten by a typo');
  });

  it('?autostart=0 registers but computes nothing, in every layout', async () => {
    for (const search of ['?autostart=0', '?autostart=0&layout=tabs', '?autostart=0&layout=pools']) {
      const { result, trace } = await startPage({ search });
      assert.equal(result.started, true);
      assert.deepEqual(trace.calls, [], `no start with ${search}`);
      assert.equal(result.app.isRunning, false);
      assert.equal(result.budget.used(), 0, 'an idle tab holds no slot');
    }
  });

  it('?pools=0 turns the new pools off and keeps the legacy lanes', async () => {
    const { result, trace } = await startPage({ search: '?pools=0' });
    assert.equal(result.started, true);
    assert.equal(result.switches.poolsOff, true);
    assert.equal(result.app, null);
    assert.deepEqual(trace.calls, [], 'no ContributeApp was constructed');
    assert.equal(result.legacy.started, true, 'the legacy lanes still run');
  });

  it('the layout does not resize the budget, it gates which lane holds it', async () => {
    const { result } = await startPage({ search: '?layout=tabs' });
    const total = result.budget.total();
    assert.equal(total, 6, 'the registration grew the ceiling to CPU + GPU');
    const legacyTab = result.layoutSwitch.setActiveTab('legacy');
    assert.equal(legacyTab.legacy, true);
    await new Promise((resolve) => setTimeout(resolve, 10));
    assert.equal(result.budget.total(), total, 'the same ceiling in every layout');
    assert.ok(result.budget.used() <= total);
  });

  it('the page builds the artifact cache with a digest implementation', async () => {
    resetContributorPageForTests();
    const { sha256Hex } = await import('../js/contribute.js');
    const good = new Uint8Array([0, 97, 115, 109, 1, 0, 0, 0]);
    const digest = await sha256Hex(good);
    const bad = 'f'.repeat(64);
    const seen = [];
    const injected = async (buf) => {
      seen.push(1);
      return sha256Hex(buf);
    };

    const result = await startContributorPage({
      doc: fakeDoc(['log-output']),
      globalTarget: {},
      navigator: { hardwareConcurrency: 8 },
      search: '',
      ContributeApp: makeAppClass(newTrace()),
      indexedDB: null,
      sha256Hex: injected
    });
    assert.equal(result.started, true);
    assert.equal(result.artifactCache.kind, 'memory');
    // A poisoned entry: stored bytes that do not hash to their key are dropped.
    assert.equal(await result.artifactCache.put(bad, good), true);
    assert.equal(await result.artifactCache.get(bad), null, 'a poisoned entry never serves bytes');
    assert.ok(seen.length >= 1, 'the read path re-hashed the stored entry');
    assert.equal(await result.artifactCache.put(digest, good), true);
    assert.deepEqual(Array.from(new Uint8Array(await result.artifactCache.get(digest))), Array.from(good));
  });

  it('the lazy page digest loads the app module and drops a poisoned entry', async () => {
    resetContributorPageForTests();
    const { sha256Hex } = await import('../js/contribute.js');
    const good = new Uint8Array([0, 97, 115, 109, 1, 0, 0, 0]);
    const digest = await sha256Hex(good);
    const bad = 'f'.repeat(64);
    const imported = [];

    const result = await startContributorPage({
      doc: fakeDoc(['log-output']),
      globalTarget: {},
      navigator: { hardwareConcurrency: 8 },
      search: '',
      ContributeApp: makeAppClass(newTrace()),
      importModule: async (spec) => {
        imported.push(String(spec));
        if (String(spec).includes('contribute.js')) return { ContributeApp: null, sha256Hex };
        return { startWorkerLanes: async () => {} };
      },
      indexedDB: null
    });
    assert.equal(result.started, true);
    assert.deepEqual(imported.filter((s) => s.includes('contribute.js')), [],
      'the app module is not loaded by the cache alone');

    // A store entry whose bytes do not hash to its key is dropped on read: that
    // is the production behaviour this row restores.
    const cache = result.artifactCache;
    assert.equal(await cache.put(bad, good), true);
    assert.equal(await cache.get(bad), null, 'a poisoned entry never serves bytes');
    assert.equal(await cache.put(digest, good), true);
    assert.deepEqual(Array.from(new Uint8Array(await cache.get(digest))), Array.from(good));
  });

  it('the standalone page pins pools-only and never touches the legacy area', async () => {
    resetContributorPageForTests();
    const trace = newTrace();
    const result = await startContributorPage({
      doc: fakeDoc(['log-output']),
      globalTarget: {},
      navigator: { hardwareConcurrency: 8 },
      search: '?layout=tabs&autostart=1',
      forcedLayout: 'pools',
      ContributeApp: makeAppClass(trace),
      importModule: async (spec) => {
        trace.legacyRuns += 1;
        return { startWorkerLanes: async () => { trace.legacyRuns += 1; } };
      },
      indexedDB: null
    });
    assert.equal(result.layout, 'pools', 'the pinned layout wins over ?layout');
    assert.equal(trace.legacyRuns, 0, 'no legacy import at all');
    assert.equal(result.legacy.reason, 'no_legacy_area');
    assert.deepEqual(trace.calls, ['init', 'start'], 'the new pools compute');
    assert.equal(result.adapter.isEnabled(), true, 'there is no legacy gate to flip on this page');
  });

  it('the entry runs once; a second call is a no-op', async () => {
    const first = await startPage();
    assert.equal(first.result.started, true);
    // startPage resets the gate, so call the entry directly now.
    const second = await startContributorPage({ doc: fakeDoc(ROOT_IDS), globalTarget: {} });
    assert.equal(second.started, false);
    assert.equal(second.reason, 'already_started');
  });

  it('blocks the standalone fallback only while the entry owns the page', async () => {
    const previousWindow = globalThis.window;
    globalThis.window = {};
    try {
      resetContributorPageForTests();
      const started = await startPage();
      assert.equal(started.result.started, true);
      assert.equal(globalThis.window.ccgscPageBooted, true, 'a started page keeps the standalone boot out');

      resetContributorPageForTests();
      const doc = fakeDoc(ROOT_IDS);
      // A DOM failure after the app was built escapes runContributorPage and
      // reaches the catch of the entry.
      doc.body = { setAttribute() { throw new Error('dom broken'); } };
      const failed = await startContributorPage({
        doc,
        globalTarget: {},
        navigator: { hardwareConcurrency: 4 },
        search: '',
        ContributeApp: makeAppClass(newTrace()),
        indexedDB: null
      });
      assert.equal(failed.started, false);
      assert.equal(failed.reason, 'unhandled_error');
      assert.equal(globalThis.window.ccgscPageBooted, false, 'a failed entry hands the page back to the fallback boot');
    } finally {
      if (previousWindow === undefined) delete globalThis.window;
      else globalThis.window = previousWindow;
      resetContributorPageForTests();
    }
  });

  it('a page without nodes still starts, a broken import only costs its lane', async () => {
    resetContributorPageForTests();
    const result = await startContributorPage({
      doc: { getElementById: () => null },
      globalTarget: {},
      navigator: { hardwareConcurrency: 4 },
      search: '',
      ContributeApp: makeAppClass(newTrace()),
      importModule: async () => { throw new Error('down'); },
      indexedDB: null
    });
    assert.equal(result.started, true);
    assert.equal(result.legacy.started, false);
    assert.equal(result.legacy.reason, 'router_unavailable');
    assert.ok(result.statusLog.lines().some((l) => l.message.includes('legacy lane router unavailable')));
  });

});

describe('T104 anonymous opt-in gate (T103 rule carried forward)', () => {

  it('only boolean true is on; everything else is off', () => {
    assert.equal(isAnonymousJoinOn({ anonymousJoin: true }), true);
    assert.equal(isAnonymousJoinOn({ anonymousJoin: false }), false);
    assert.equal(isAnonymousJoinOn({ anonymousJoin: 'true' }), false, 'the string is not the boolean');
    assert.equal(isAnonymousJoinOn({ anonymousJoin: 1 }), false);
    assert.equal(isAnonymousJoinOn({}), false);
    assert.equal(isAnonymousJoinOn(null), false);
    assert.equal(isAnonymousJoinOn(undefined), false);
    assert.equal(isAnonymousJoinOn('yes'), false);
    assert.equal(RUNNER_CONFIG_TIMEOUT_MS, 3000);
  });

  it('every failure of the probe is a clean off, never a hang', async () => {
    const ok = (body) => ({ ok: true, status: 200, json: async () => body });
    const on = await resolveNewPoolsOptIn(async () => ok({ anonymousJoin: true }));
    assert.equal(on.allowed, true);
    assert.equal(on.reason, 'anonymous_join_on');

    const off = await resolveNewPoolsOptIn(async () => ok({ anonymousJoin: false }));
    assert.equal(off.allowed, false);
    assert.equal(off.reason, 'anonymous_join_off');

    const notOk = await resolveNewPoolsOptIn(async () => ({ ok: false, status: 503 }));
    assert.equal(notOk.allowed, false);
    assert.equal(notOk.reason, 'runner_config_not_ok');

    const badJson = await resolveNewPoolsOptIn(async () => ({ ok: true, status: 200, json: async () => { throw new Error('bad'); } }));
    assert.equal(badJson.allowed, false);
    assert.equal(badJson.reason, 'runner_config_parse_failed');

    const network = await resolveNewPoolsOptIn(async () => { throw new Error('down'); });
    assert.equal(network.allowed, false);
    assert.equal(network.reason, 'runner_config_fetch_failed');

    const noFetch = await resolveNewPoolsOptIn(null);
    assert.equal(noFetch.allowed, false);

    // A hung server answers a timeout, and the probe gives up on its own.
    const hung = await resolveNewPoolsOptIn(() => new Promise(() => {}), 30);
    assert.equal(hung.allowed, false);
    assert.equal(hung.reason, 'runner_config_timeout');
  });

  it('probeRunnerConfig answers the three shapes the bridge did', async () => {
    const ok = await probeRunnerConfig(async () => ({ ok: true }));
    assert.equal(ok.kind, 'response');
    const err = await probeRunnerConfig(async () => { throw new Error('x'); });
    assert.equal(err.kind, 'error');
    const timeout = await probeRunnerConfig(() => new Promise(() => {}), 20);
    assert.equal(timeout.kind, 'timeout');
  });

  it('with the gate on, the new pools stay off unless the config allows them', async () => {
    resetContributorPageForTests();
    const trace = newTrace();
    const result = await startContributorPage({
      doc: fakeDoc(ROOT_IDS),
      globalTarget: {},
      navigator: { hardwareConcurrency: 8 },
      search: '',
      optInGate: true,
      fetch: async () => ({ ok: true, status: 200, json: async () => ({ anonymousJoin: false }) }),
      ContributeApp: makeAppClass(trace),
      importModule: async () => ({ startWorkerLanes: async () => {} }),
      indexedDB: null
    });
    assert.equal(result.started, true);
    assert.equal(result.app, null, 'no ContributeApp: the new pools are opt-in');
    assert.equal(result.optIn.allowed, false);
    assert.equal(result.optIn.reason, 'anonymous_join_off');
    assert.deepEqual(trace.calls, []);
    assert.ok(result.statusLog.lines().some((l) => l.message.includes('new pools off')));
  });

  it('with the gate on and anonymous join allowed, the new pools join', async () => {
    resetContributorPageForTests();
    const trace = newTrace();
    const result = await startContributorPage({
      doc: fakeDoc(ROOT_IDS),
      globalTarget: {},
      navigator: { hardwareConcurrency: 8 },
      search: '',
      optInGate: true,
      fetch: async () => ({ ok: true, status: 200, json: async () => ({ anonymousJoin: true }) }),
      ContributeApp: makeAppClass(trace),
      importModule: async () => ({ startWorkerLanes: async () => {} }),
      indexedDB: null
    });
    assert.equal(result.optIn.allowed, true);
    assert.ok(result.app);
    assert.deepEqual(trace.calls, ['init', 'start']);
  });

  it('the standalone page needs no gate: it is already a contributor page', async () => {
    resetContributorPageForTests();
    const trace = newTrace();
    const result = await startContributorPage({
      doc: fakeDoc(['log-output']),
      globalTarget: {},
      navigator: { hardwareConcurrency: 8 },
      search: '',
      forcedLayout: 'pools',
      ContributeApp: makeAppClass(trace),
      importModule: async () => ({}),
      indexedDB: null
    });
    assert.equal(result.optIn.reason, 'opt_in_not_required');
    assert.ok(result.app);
  });

});

describe('T104 entry with the real ContributeApp', () => {

  it('the root page boots the real app and shares the one budget', async () => {
    const originalFetch = globalThis.fetch;
    const calls = [];
    globalThis.fetch = async (url, opts) => {
      const target = String(url);
      calls.push({ url: target, method: (opts && opts.method) || 'GET' });
      if (target.endsWith('/pools/runner-config')) return { ok: true, status: 200, json: async () => ({ anonymousJoin: true, runOrigin: null }) };
      if (target.endsWith('/workers/anonymous')) {
        return { ok: true, status: 200, json: async () => ({ accessToken: 'a.eyJzdWIiOiJhbm9uLTEifQ.s', expiresIn: 900 }) };
      }
      if (target.endsWith('/workers/register')) return { ok: true, status: 201, json: async () => ({ tabId: 'tab-real' }) };
      if (target.includes('/leases')) return { ok: true, status: 204, json: async () => null };
      return { ok: true, status: 200, json: async () => ({}) };
    };
    resetContributorPageForTests();
    try {
      const result = await startContributorPage({
        doc: fakeDoc(['log-output']),
        globalTarget: {},
        navigator: { hardwareConcurrency: 8 },
        search: '?layout=pools&mock=1',
        importModule: async (spec) => {
          if (String(spec).includes('contribute.js')) {
            return { ContributeApp: (await import('../js/contribute.js')).ContributeApp };
          }
          throw new Error('unexpected import: ' + spec);
        },
        indexedDB: null
      });
      assert.equal(result.started, true);
      assert.equal(result.layout, 'pools');
      assert.ok(result.app, 'the real app was constructed');
      assert.equal(result.app.isRunning, true, 'the visible area computes on visit (T105)');
      assert.equal(result.app.sourceMode, 'MOCK', '?mock=1 keeps its debug meaning');
      // ?mock=1 short-circuits the source detection, so the app never asks for
      // the runner config: the switch still means exactly what it meant before.
      assert.equal(calls.some((c) => c.url.endsWith('/pools/runner-config')), false);
      assert.equal(calls.some((c) => c.url.endsWith('/workers/register')), false, 'a MOCK tab registers nothing');
      // Exactly one budget, and the app holds its slots inside it. A MOCK tab
      // never enumerates adapters, so the ceiling stays the CPU part.
      assert.equal(result.budget.total(), 4, 'no registration, no GPU lane: CPU ceiling');
      assert.equal(result.budget.usedBy('pools'), 4);
      assert.equal(result.budget.usedBy('pools:gpu'), 0, 'no WebGPU in Node: no GPU lane');
      assert.ok(result.budget.used() <= result.budget.total());
      assert.ok(result.statusLog.lines().some((l) => l.source === 'pools'), 'the app writes into the one log');
      result.app.stop();
      assert.equal(result.budget.used(), 0, 'stop releases the whole claim');
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it('the real app fills the page budget with the CPU + GPU lanes it discovered', async () => {
    const originalFetch = globalThis.fetch;
    const navigatorDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'navigator');
    const hadDocument = Object.prototype.hasOwnProperty.call(globalThis, 'document');
    const realDocument = globalThis.document;
    const { ContributeApp } = await import('../js/contribute.js');
    // Two distinct adapters, so the picker's de-duplication keeps both and the
    // app has the full GPU_LANE_CAP of 2 lanes to fold into the page budget.
    const adapter = (device, description) => ({
      info: { vendor: 'v', architecture: 'a', device, description },
      requestDevice: async () => ({ destroy() {} })
    });
    const integrated = adapter('1', 'integrated');
    const discrete = adapter('2', 'discrete');
    const fakeGpu = {
      requestAdapter: async (opts) => (opts && opts.powerPreference === 'low-power' ? discrete : integrated)
    };
    Object.defineProperty(globalThis, 'navigator', {
      value: { hardwareConcurrency: 8, gpu: fakeGpu, userAgent: 'Node.js' },
      configurable: true
    });
    // init() needs a document; the page's own doc fake is what the wiring reads.
    globalThis.document = { cookie: '', getElementById: () => null };
    globalThis.fetch = async (url) => {
      const target = String(url);
      if (target.endsWith('/pools/runner-config')) return { ok: true, status: 200, json: async () => ({ anonymousJoin: true }) };
      if (target.endsWith('/workers/anonymous')) {
        return { ok: true, status: 200, json: async () => ({ accessToken: 'a.eyJzdWIiOiJhbm9uLWdwdSJ9.s', expiresIn: 900 }) };
      }
      if (target.endsWith('/workers/register')) return { ok: true, status: 201, json: async () => ({ tabId: 'tab-real-gpu' }) };
      return { ok: true, status: 204, json: async () => null };
    };
    resetContributorPageForTests();
    try {
      const result = await startContributorPage({
        doc: fakeDoc(['log-output']),
        globalTarget: {},
        search: '?layout=pools',
        ContributeApp,
        importModule: async (spec) => {
          if (String(spec).includes('contribute.js')) return { ContributeApp };
          throw new Error('unexpected import: ' + spec);
        },
        indexedDB: null
      });
      assert.equal(result.started, true);
      assert.ok(result.app instanceof ContributeApp, 'the page constructed the real app');
      assert.equal(result.app.sourceMode, 'LIVE', result.app.sourceReason);
      assert.equal(result.app.gpus.length, 2, 'the real registration discovered both adapters');
      // The page sized the budget before any adapter existed; the real app grew
      // it to CPU + GPU and then claimed both parts of the one ceiling.
      assert.equal(result.budget.total(), 6, '4 CPU lanes + 2 discovered GPU lanes');
      assert.equal(result.budget.usedBy('pools'), 4);
      assert.equal(result.budget.usedBy('pools:gpu'), 2);
      assert.equal(result.budget.used(), 6);
      assert.equal(result.budget.claim('legacy:matrix', 1), 0, 'the real app leaves no slot above the ceiling');
      result.app.stop();
      assert.equal(result.budget.used(), 0, 'stop releases the whole page claim');
    } finally {
      globalThis.fetch = originalFetch;
      if (navigatorDescriptor) Object.defineProperty(globalThis, 'navigator', navigatorDescriptor);
      else delete globalThis.navigator;
      if (hadDocument) globalThis.document = realDocument;
      else delete globalThis.document;
      resetContributorPageForTests();
    }
  });

});


describe('T104 legacy lane mount', () => {

  it('accepts both router entry points and reports every failure honestly', async () => {
    const statusLog = createStatusLog();
    const seen = [];
    const started = await mountLegacyLanes({
      statusLog,
      adapter: { owner: 'legacy:test' },
      spec: './router.mjs',
      search: '?lanes=all',
      importModule: async () => ({ startWorkerLanes: async (opts) => { seen.push(opts); } })
    });
    assert.equal(started.started, true);
    assert.equal(seen.length, 1);
    assert.equal(seen[0].search, '?lanes=all');

    const single = await mountLegacyLanes({
      statusLog,
      spec: './router.mjs',
      importModule: async () => ({ startWorkerLane: async () => {} })
    });
    assert.equal(single.started, true);

    const missing = await mountLegacyLanes({
      statusLog,
      spec: './router.mjs',
      importModule: async () => ({})
    });
    assert.equal(missing.started, false);
    assert.equal(missing.reason, 'router_missing_entry');

    const failing = await mountLegacyLanes({
      statusLog,
      spec: './router.mjs',
      importModule: async () => ({ startWorkerLanes: async () => { throw new Error('lane down'); } })
    });
    assert.equal(failing.started, false);
    assert.equal(failing.reason, 'router_start_failed');
  });

  it('without a status log a failed mount stays silent and safe', async () => {
    const mounted = await mountLegacyLanes({
      spec: './router.mjs',
      importModule: async () => { throw new Error('gone'); }
    });
    assert.equal(mounted.started, false);
    assert.equal(mounted.reason, 'router_unavailable');
  });

});
