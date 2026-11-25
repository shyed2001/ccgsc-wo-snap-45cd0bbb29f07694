// Path: web/pages/tests/contribute_T104.test.mjs | Author: GLM (Z.ai) for Claude Opus 5.5 head | 2026-11-21
// What/why: T104 step 1 - the integrated page work that lives in contribute.js:
// the persisted host id (read-only inside the app), dual pools tp+pp with ONE
// anonymous token, round-robin claims, per-pool heartbeats, and the optional
// shared worker budget and status/log hooks. All servers are fake fetch doubles;
// no network. Every global this suite touches is restored in a finally block.

import { describe, it, after } from 'node:test';
import assert from 'node:assert/strict';
import {
  API_BASE,
  PROCESS_POOL_BASE,
  POOL_BASES,
  HOST_ID_STORAGE_KEY,
  ContributeApp,
  readPersistedHostId,
  persistHostId,
  resolvePools,
  sha256Hex
} from '../js/contribute.js';
import { WorkerBudget } from '../js/worker_budget_T104.js';
import { createStatusLog } from '../js/status_log_T104.js';

const originalFetch = globalThis.fetch;
const originalLocalStorage = globalThis.localStorage;

function jsonResponse(status, body) {
  return { ok: status >= 200 && status < 300, status, json: async () => body };
}

function mapStorage(initial = {}) {
  const store = new Map(Object.entries(initial));
  return {
    getItem: (key) => (store.has(key) ? store.get(key) : null),
    setItem: (key, value) => { store.set(key, String(value)); },
    removeItem: (key) => { store.delete(key); },
    dump: () => Object.fromEntries(store.entries())
  };
}

after(() => {
  globalThis.fetch = originalFetch;
  if (originalLocalStorage === undefined) delete globalThis.localStorage;
  else globalThis.localStorage = originalLocalStorage;
});

function fakeJwt(sub) {
  const b = (o) => Buffer.from(JSON.stringify(o)).toString('base64url');
  return b({ alg: 'EdDSA' }) + '.' + b({ sub }) + '.sig';
}

// A root page fetch double with the standard contributor routes of both pools.
async function withDualPoolPage(options, run) {
  const calls = [];
  globalThis.fetch = async (url, opts) => {
    const target = String(url);
    calls.push({
      url: target,
      method: (opts && opts.method) || 'GET',
      headers: (opts && opts.headers) || {},
      body: opts && opts.body
    });
    if (target.endsWith('/pools/runner-config')) {
      return jsonResponse(200, { anonymousJoin: true, runOrigin: null });
    }
    if (target.endsWith('/workers/anonymous')) {
      return jsonResponse(200, { accessToken: fakeJwt('anon-one'), expiresIn: 900, tokenType: 'Bearer' });
    }
    if (target.endsWith('/workers/register')) {
      const pool = target.startsWith(PROCESS_POOL_BASE) ? 'pp' : 'tp';
      return jsonResponse(201, { tabId: 'tab-' + pool, poolId: pool });
    }
    if (target.includes('/leases')) return jsonResponse(204, null);
    return jsonResponse(200, {});
  };
  try {
    const app = new ContributeApp(options);
    try {
      await run(app, calls);
    } finally {
      // The renewal timer is a real timeout minutes away; drop it so the suite
      // does not keep the event loop alive (WO_T96B timer contract).
      if (app.tokenRenewTimer) clearTimeout(app.tokenRenewTimer);
      if (app.taskTimer) clearTimeout(app.taskTimer);
      if (app.runnerRetryTimer) clearTimeout(app.runnerRetryTimer);
      try { app.stop(); } catch (_) { /* already idle */ }
    }
  } finally {
    globalThis.fetch = originalFetch;
  }
}

describe('T104 host id persistence', () => {

  it('persists a host id and reads it back (page bootstrap path)', () => {
    const store = mapStorage();
    const first = persistHostId(undefined, store);
    assert.match(first, /^host-/);
    assert.equal(store.dump()[HOST_ID_STORAGE_KEY], first);
    const second = persistHostId(undefined, store);
    assert.equal(second, first, 'one stable id across reloads');
    assert.equal(readPersistedHostId(store), first);
  });

  it('keeps a valid argument and writes only when the stored value differs', () => {
    const store = mapStorage();
    assert.equal(persistHostId('host-mybox-1', store), 'host-mybox-1');
    assert.equal(persistHostId('host-mybox-1', store), 'host-mybox-1');
    assert.equal(readPersistedHostId(store), 'host-mybox-1');
  });

  it('ignores a stored value that does not match the host-id shape', () => {
    const store = mapStorage({ [HOST_ID_STORAGE_KEY]: '../../etc/passwd' });
    assert.equal(readPersistedHostId(store), null);
    const id = persistHostId(undefined, store);
    assert.match(id, /^host-/);
    assert.equal(readPersistedHostId(store), id, 'the junk value was replaced');
  });

  it('a storage-less environment stays functional and silent', () => {
    const id = persistHostId(undefined, null);
    assert.match(id, /^host-/);
    assert.equal(readPersistedHostId(null), null);
    assert.equal(readPersistedHostId({ getItem: () => { throw new Error('blocked'); } }), null);
    assert.equal(persistHostId(undefined, { setItem: () => { throw new Error('blocked'); } }).startsWith('host-'), true);
  });

  it('the app reads the host id but never writes web storage', () => {
    const store = mapStorage();
    const writes = [];
    const recording = {
      getItem: (key) => store.getItem(key),
      setItem: (key, value) => { writes.push([key, value]); store.setItem(key, value); },
      removeItem: (key) => store.removeItem(key)
    };
    // Without any storage the constructor falls back to a fresh random id.
    const app = new ContributeApp({});
    assert.match(app.hostId, /^host-/);
    // The host page wins over everything and is re-checked at register time.
    globalThis.ccgscContributorHostId = 'host-from-page';
    try {
      assert.equal(app.resolveHostId(), 'host-from-page');
      const explicit = new ContributeApp({ hostId: 'host-explicit' });
      assert.equal(explicit.resolveHostId(), 'host-explicit');
    } finally {
      delete globalThis.ccgscContributorHostId;
    }
    // The bootstrap writes once; a later app only reads the same id.
    assert.equal(persistHostId('host-stored-9', recording), 'host-stored-9');
    globalThis.localStorage = recording;
    try {
      const app2 = new ContributeApp({});
      assert.equal(app2.hostId, 'host-stored-9', 'the app reads the persisted id');
    } finally {
      if (originalLocalStorage === undefined) delete globalThis.localStorage;
      else globalThis.localStorage = originalLocalStorage;
    }
    assert.equal(writes.length, 1, 'only the explicit bootstrap write happened');
  });

});

describe('T104 pool registry', () => {

  it('resolvePools defaults to the thread pool and sanitises the rest', () => {
    assert.deepEqual(resolvePools(undefined), ['tp']);
    assert.deepEqual(resolvePools(null), ['tp']);
    assert.deepEqual(resolvePools('tp'), ['tp']);
    assert.deepEqual(resolvePools(['tp', 'pp']), ['tp', 'pp']);
    assert.deepEqual(resolvePools(['pp', 'TP', 'pp', 'bogus', '']), ['pp', 'tp']);
    assert.deepEqual(resolvePools([]), ['tp']);
    assert.deepEqual(resolvePools(['nope']), ['tp']);
    assert.equal(POOL_BASES.tp, API_BASE);
    assert.equal(POOL_BASES.pp, PROCESS_POOL_BASE);
  });

  it('the default app keeps the single thread-pool behaviour', () => {
    const app = new ContributeApp({ autoStart: false });
    assert.deepEqual(app.pools, ['tp']);
    assert.deepEqual(app.tabIds, {});
    assert.equal(app.nextClaimPool(), 'tp');
    assert.equal(app.poolBase('tp'), API_BASE);
    assert.equal(app.poolBase('pp'), PROCESS_POOL_BASE);
    assert.equal(app.poolBase('bogus'), API_BASE, 'unknown ids answer with the thread pool');
  });

});

describe('T104 dual pools with ONE anonymous token', () => {

  it('registers in both pools with one token and keeps per-pool tab ids', async () => {
    await withDualPoolPage({ pools: ['tp', 'pp'], autoStart: false }, async (app, calls) => {
      await app.loadRunnerConfig();
      await app.initSource();
      assert.equal(app.sourceMode, 'LIVE');
      assert.deepEqual(app.tabIds, { tp: 'tab-tp', pp: 'tab-pp' });
      assert.equal(app.tabId, 'tab-tp', 'the thread pool tab stays primary');
      const joins = calls.filter((c) => c.url.endsWith('/workers/anonymous'));
      assert.equal(joins.length, 1, 'exactly ONE anonymous token for both pools');
      const registers = calls.filter((c) => c.url.endsWith('/workers/register'));
      assert.equal(registers.length, 2);
      assert.equal(registers[0].url, API_BASE + '/workers/register');
      assert.equal(registers[1].url, PROCESS_POOL_BASE + '/workers/register');
      for (const register of registers) {
        assert.equal(register.headers.Authorization, 'Bearer ' + app.authToken, 'the same token serves both pools');
        assert.equal(JSON.parse(register.body).hostId, app.hostId);
      }
    });
  });

  it('a secondary pool that refuses keeps the tab alive on the primary', async () => {
    await withDualPoolPage({ pools: ['tp', 'pp'], autoStart: false }, async (app, calls) => {
      const realFetch = globalThis.fetch;
      globalThis.fetch = async (url, opts) => {
        if (String(url).startsWith(PROCESS_POOL_BASE) && String(url).endsWith('/workers/register')) {
          calls.push({ url: String(url), method: 'POST' });
          return jsonResponse(503, { title: 'unavailable' });
        }
        return realFetch(url, opts);
      };
      await app.loadRunnerConfig();
      await app.initSource();
      assert.equal(app.sourceMode, 'LIVE', 'a pp outage is honest partial registration, not an error');
      assert.deepEqual(app.tabIds, { tp: 'tab-tp' });
      const ppRegister = calls.filter((c) => c.url === PROCESS_POOL_BASE + '/workers/register');
      assert.equal(ppRegister.length, 1, 'the secondary pool was tried exactly once per registration');
    });
  });

  it('the primary pool failing stays a fatal, honest error', async () => {
    globalThis.fetch = async (url) => {
      if (String(url).endsWith('/pools/runner-config')) return jsonResponse(200, { anonymousJoin: true });
      if (String(url).endsWith('/workers/anonymous')) return jsonResponse(200, { accessToken: fakeJwt('anon-x'), expiresIn: 900 });
      if (String(url) === API_BASE + '/workers/register') return jsonResponse(403, {});
      return jsonResponse(200, {});
    };
    try {
      const app = new ContributeApp({ pools: ['tp', 'pp'], autoStart: false });
      try {
        await app.loadRunnerConfig();
        await app.initSource();
        assert.equal(app.sourceMode, 'ERROR', 'a 403 from the primary pool is never covered up');
        assert.equal(app.sourceReason, 'registration rejected with HTTP 403');
      } finally {
        if (app.tokenRenewTimer) clearTimeout(app.tokenRenewTimer);
      }
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it('claims round-robin over the registered pools and each result returns to its pool', async () => {
    await withDualPoolPage({ pools: ['tp', 'pp'], autoStart: false }, async (app, calls) => {
      await app.loadRunnerConfig();
      await app.initSource();
      const seen = [];
      // First claim -> tp, second -> pp, third -> tp again. Each answer is a
      // complete, valid assignment so the lease survives the envelope checks:
      // the digest the envelope authorizes must be the digest of the artifact
      // bytes, otherwise the R10 digest gate refuses the lease.
      const assignments = [];
      const WASM_BYTES = new Uint8Array([0, 97, 115, 109, 1, 0, 0, 0]);
      const WASM_DIGEST = await sha256Hex(WASM_BYTES);
      const realFetch = globalThis.fetch;
      globalThis.fetch = async (url) => {
        const target = String(url);
        if (target.includes('/leases')) {
          seen.push(target);
          const pool = target.startsWith(PROCESS_POOL_BASE) ? 'pp' : 'tp';
          const taskId = 'task-' + seen.length;
          assignments.push({ pool, taskId });
          return jsonResponse(200, {
            lease: { jobId: 'job-' + pool, taskId, generation: 1, token: 'tok-' + seen.length },
            taskEnvelope: {
              payload: JSON.stringify({
                version: 1, jobId: 'job-' + pool, taskId,
                artifactSha256: WASM_DIGEST, inputSha256: 'b'.repeat(64),
                issuedAt: 1, expiresAt: 9999999999, keyId: 'k'
              }),
              signature: 'c2ln'
            },
            moduleArtifactIds: ['art-1'],
            task: { input: { kind: 'matmul-f32-gpu', n: 4 } }
          });
        }
        if (target.includes('/artifacts/')) {
          return { ok: true, status: 200, arrayBuffer: async () => WASM_BYTES.slice().buffer };
        }
        return realFetch(url);
      };
      await app.getLiveJob();
      assert.equal(app.currentPool, 'tp');
      await app.getLiveJob();
      assert.equal(app.currentPool, 'pp');
      await app.getLiveJob();
      assert.equal(app.currentPool, 'tp');
      assert.equal(seen[0], API_BASE + '/workers/tab-tp/leases');
      assert.equal(seen[1], PROCESS_POOL_BASE + '/workers/tab-pp/leases');
      assert.equal(seen[2], API_BASE + '/workers/tab-tp/leases');
      // A result goes back to the pool the lease came from, with that tab id.
      calls.length = 0;
      await app.submitResult({ taskId: 'task-2', ok: true, output: { n: 1 }, timings: { computeMs: 1 } });
      const results = calls.filter((c) => c.url.includes('/results'));
      assert.equal(results.length, 1);
      assert.equal(results[0].url, API_BASE + '/workers/tab-tp/results');
    });
  });

  it('both registered tabs get their keep-alive on their own pool', async () => {
    await withDualPoolPage({ pools: ['tp', 'pp'], autoStart: false }, async (app, calls) => {
      await app.loadRunnerConfig();
      await app.initSource();
      calls.length = 0;
      await app.sendHeartbeat();
      const beats = calls.filter((c) => c.url.includes('/heartbeat'));
      assert.equal(beats.length, 2);
      assert.equal(beats[0].url, API_BASE + '/workers/tab-tp/heartbeat');
      assert.equal(beats[1].url, PROCESS_POOL_BASE + '/workers/tab-pp/heartbeat');
    });
  });

});

describe('T104 shared budget in the app', () => {

  it('start claims what the tab has left, stop releases, legacy claims stay untouched', async () => {
    const budget = new WorkerBudget({ total: 4 });
    budget.claim('legacy:matrix', 3);
    const app = new ContributeApp({ budget, autoStart: false });
    app.hwConcurrency = 8;
    app.workerCount = 4;
    await app.start();
    assert.equal(budget.usedBy('pools'), 1, 'the new pools get what the tab has left');
    app.stop();
    assert.equal(budget.usedBy('pools'), 0);
    assert.equal(budget.usedBy('legacy:matrix'), 3, 'the legacy claim is untouched by the new pools');
    if (app.taskTimer) { clearTimeout(app.taskTimer); app.taskTimer = null; }
  });

  it('an idle app releases its claim instead of blocking the old lanes', () => {
    const budget = new WorkerBudget({ total: 4 });
    const app = new ContributeApp({ budget, autoStart: false });
    app.workerCount = 4;
    assert.equal(app.applyBudgetClaim(), 0, 'an idle tab claims nothing');
    budget.claim('pools', 2); // simulate a stale claim
    assert.equal(app.applyBudgetClaim(), 0);
    assert.equal(budget.usedBy('pools'), 0, 'the idle reconciliation releases it');
  });

  it('the claim loop never takes more CPU slots than the budget grants', async () => {
    const budget = new WorkerBudget({ total: 4 });
    budget.claim('legacy:matrix', 3); // capacity left for the pools: 1
    let leased = 0;
    const WASM_BYTES = new Uint8Array([0, 97, 115, 109, 1, 0, 0, 0]);
    const WASM_DIGEST = await sha256Hex(WASM_BYTES);
    globalThis.fetch = async (url) => {
      const target = String(url);
      if (target.includes('/leases')) {
        leased += 1;
        return jsonResponse(200, {
          lease: { jobId: 'job-1', taskId: 'task-' + leased, generation: 1, token: 'tok-' + leased },
          taskEnvelope: {
            payload: JSON.stringify({
              version: 1, jobId: 'job-1', taskId: 'task-' + leased,
              artifactSha256: WASM_DIGEST, inputSha256: 'b'.repeat(64),
              issuedAt: 1, expiresAt: 9999999999, keyId: 'k'
            }),
            signature: 'c2ln'
          },
          moduleArtifactIds: ['art-1'],
          task: { input: { kind: 'matmul-f32-gpu', n: 4 } }
        });
      }
      if (target.includes('/artifacts/')) {
        return { ok: true, status: 200, arrayBuffer: async () => WASM_BYTES.slice().buffer };
      }
      return jsonResponse(200, {});
    };
    try {
      const app = new ContributeApp({ budget, autoStart: false });
      app.hwConcurrency = 8;
      app.workerCount = 4;
      app.sourceMode = 'LIVE';
      app.tabIds = { tp: 'tab-tp' };
      app.tabId = 'tab-tp';
      app.registeredTabId = 'tab-tp';
      app.runnerReady = true;
      app.runnerNonce = 'nonce-t104';
      app.port = { postMessage() {} };
      app.isRunning = true;
      await app.scheduleNextTask();
      if (app.taskTimer) { clearTimeout(app.taskTimer); app.taskTimer = null; }
      assert.equal(leased, 1, 'the budget-bounded capacity runs one claim, not four');
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it('without a shared budget the claim loop keeps the old capacity', async () => {
    let leased = 0;
    const WASM_BYTES = new Uint8Array([0, 97, 115, 109, 1, 0, 0, 0]);
    const WASM_DIGEST = await sha256Hex(WASM_BYTES);
    globalThis.fetch = async (url) => {
      const target = String(url);
      if (target.includes('/leases')) {
        leased += 1;
        return jsonResponse(200, {
          lease: { jobId: 'job-1', taskId: 'task-' + leased, generation: 1, token: 'tok-' + leased },
          taskEnvelope: {
            payload: JSON.stringify({
              version: 1, jobId: 'job-1', taskId: 'task-' + leased,
              artifactSha256: WASM_DIGEST, inputSha256: 'b'.repeat(64),
              issuedAt: 1, expiresAt: 9999999999, keyId: 'k'
            }),
            signature: 'c2ln'
          },
          moduleArtifactIds: ['art-1'],
          task: { input: { kind: 'matmul-f32-gpu', n: 4 } }
        });
      }
      if (target.includes('/artifacts/')) {
        return { ok: true, status: 200, arrayBuffer: async () => WASM_BYTES.slice().buffer };
      }
      return jsonResponse(200, {});
    };
    try {
      const app = new ContributeApp({ autoStart: false });
      app.hwConcurrency = 8;
      app.workerCount = 4;
      app.sourceMode = 'LIVE';
      app.tabIds = { tp: 'tab-tp' };
      app.tabId = 'tab-tp';
      app.registeredTabId = 'tab-tp';
      app.runnerReady = true;
      app.runnerNonce = 'nonce-t104b';
      app.port = { postMessage() {} };
      app.isRunning = true;
      await app.scheduleNextTask();
      if (app.taskTimer) { clearTimeout(app.taskTimer); app.taskTimer = null; }
      assert.equal(leased, 4, 'no budget: the old capacity of 4 CPU workers applies');
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

});

describe('T104 one status/log area in the app', () => {

  it('with a shared area the app routes its lines there and publishes its chip', () => {
    const log = createStatusLog();
    const el = { textContent: '' };
    log.attach(el);
    const app = new ContributeApp({ statusLog: log, autoStart: false });
    app.log('hello from the app');
    assert.ok(el.textContent.includes('[pools] hello from the app'), el.textContent);
    app.render(); // no DOM: only the status chip of the shared area is published
    assert.ok(el.textContent.includes('[status] pools=IDLE'), el.textContent);
    app.isRunning = true;
    app.render();
    assert.ok(el.textContent.includes('pools=COMPUTING'), el.textContent);
    app.isRunning = false;
    // The statusLog passed by option wins over the global; both work.
    globalThis.ccgscStatusLog = log;
    try {
      const app2 = new ContributeApp({ autoStart: false });
      app2.log('via the host wiring');
      assert.ok(el.textContent.includes('[pools] via the host wiring'), el.textContent);
    } finally {
      delete globalThis.ccgscStatusLog;
    }
  });

  it('without a shared area the standalone path is unchanged', () => {
    const app = new ContributeApp({ autoStart: false });
    const seen = [];
    const originalConsole = console.log;
    console.log = (line) => seen.push(line);
    try {
      app.log('standalone line');
    } finally {
      console.log = originalConsole;
    }
    assert.equal(seen.length, 1);
    assert.ok(seen[0].includes('standalone line'));
    app.render(); // no DOM, no shared area: a silent no-op
  });

});

describe('T104 GPU lanes in the shared budget (step 8)', () => {

  const gpuAdapter = (device, description) => ({
    info: { vendor: 'v', architecture: 'a', device, description },
    requestDevice: async () => ({ destroy() {} })
  });
  const fakeGpu = () => {
    const a = gpuAdapter('1', 'integrated');
    const b = gpuAdapter('2', 'discrete');
    return { requestAdapter: async (o) => (o && o.powerPreference === 'low-power' ? b : a) };
  };

  it('the registration discovers the adapters and the tab grows to CPU + GPU', async () => {
    const budget = new WorkerBudget({ total: 4 });
    globalThis.fetch = async (url) => {
      if (String(url).endsWith('/workers/register')) return jsonResponse(201, { tabId: 'tab-gpu' });
      return jsonResponse(200, {});
    };
    try {
      const app = new ContributeApp({ budget, gpu: fakeGpu(), autoStart: false });
      app.hwConcurrency = 8;
      assert.equal(budget.total(), 4, 'the page sized the CPU part before any adapter existed');
      await app.registerWorker();
      assert.equal(app.gpus.length, 2);
      assert.equal(budget.total(), 6, 'the ceiling grew by the GPU lanes of this tab');
      assert.equal(budget.usedBy('pools:gpu'), 0, 'a registration without a run claims nothing');
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it('start claims the GPU lanes, stop releases them, the battery guard wins', async () => {
    const budget = new WorkerBudget({ total: 4 });
    globalThis.fetch = async () => jsonResponse(201, { tabId: 'tab-gpu' });
    try {
      const app = new ContributeApp({ budget, gpu: fakeGpu(), autoStart: false });
      app.hwConcurrency = 8;
      app.workerCount = 4;
      app.runnerReady = false;
      await app.registerWorker();
      await app.start();
      assert.equal(budget.usedBy('pools'), 4);
      assert.equal(budget.usedBy('pools:gpu'), 2);
      assert.equal(budget.used(), 6);
      assert.equal(budget.total(), 6);
      assert.ok(budget.used() <= budget.total());

      // The battery guard takes the GPU lanes away: the claim follows at once.
      app.hasBattery = true;
      app.isCharging = false;
      app.updateGPUGuard();
      assert.equal(budget.usedBy('pools:gpu'), 0, 'no GPU lane under 20 % / on battery');

      app.stop();
      assert.equal(budget.usedBy('pools'), 0);
      assert.equal(budget.usedBy('pools:gpu'), 0);
      if (app.taskTimer) { clearTimeout(app.taskTimer); app.taskTimer = null; }
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it('an idle reconciliation releases a stale GPU claim', () => {
    const budget = new WorkerBudget({ total: 6 });
    const app = new ContributeApp({ budget, autoStart: false });
    app.gpus = [{}, {}];
    budget.claim('pools:gpu', 2);
    assert.equal(app.applyGpuBudgetClaim(), 0);
    assert.equal(budget.usedBy('pools:gpu'), 0, 'an idle tab holds no GPU lane');
  });

  it('grows again when a later discovery finds more GPU lanes', () => {
    const budget = new WorkerBudget({ total: 4 });
    const app = new ContributeApp({ budget, autoStart: false });
    app.hwConcurrency = 8;
    app.isPhone = false;
    // First registration: one adapter, so one GPU lane.
    app.gpus = [{}];
    assert.equal(app.growBudgetForGpus(budget), 5, 'the ceiling follows the first discovery');
    // A re-registration sees more adapters. The old build latched after the
    // first call and never grew again, so the second lane had no ceiling.
    app.gpus = [{}, {}];
    assert.equal(app.growBudgetForGpus(budget), 6, 'a later discovery grows the ceiling again');
    // No new information: the ceiling stays and resize never shrinks.
    app.gpus = [{}];
    assert.equal(app.growBudgetForGpus(budget), 6, 'a smaller discovery keeps the ceiling');
    assert.equal(budget.total(), 6);
  });

  it('grows for adapters the guard blocked at the first discovery', () => {
    // The page sized the phone budget on battery: 1 CPU lane, no GPU part.
    const budget = new WorkerBudget({ total: 1 });
    const app = new ContributeApp({ budget, autoStart: false });
    app.isPhone = true;
    app.hasBattery = true;
    app.isCharging = false;
    app.gpus = [{}, {}];
    app.backoff = false;
    assert.equal(app.gpuAllowed(), false, 'no GPU lane under 20 % battery on a phone');
    assert.equal(app.growBudgetForGpus(budget), 1, 'nothing to grow while the guard blocks the lanes');
    // Charging: 2 CPU lanes + the 2 adapters, so the ceiling follows.
    app.isCharging = true;
    assert.equal(app.growBudgetForGpus(budget), 4, 'the GPU part is added when the guard allows it');
  });

  it('without a budget the GPU claim is a silent no-op', () => {
    const app = new ContributeApp({ autoStart: false });
    assert.equal(app.applyGpuBudgetClaim(), 0);
    assert.equal(app.releaseGpuBudgetClaim(), 0);
    assert.equal(app.growBudgetForGpus(null), 0);
  });

  it('the claim loop capacity is bounded by the whole tab ceiling', async () => {
    // The page sized the tab: 4 CPU + 2 GPU lanes. The legacy lane holds all 6.
    const budget = new WorkerBudget({ total: 6 });
    budget.claim('legacy:matrix', 6);
    let leased = 0;
    const WASM_BYTES = new Uint8Array([0, 97, 115, 109, 1, 0, 0, 0]);
    const WASM_DIGEST = await sha256Hex(WASM_BYTES);
    globalThis.fetch = async (url) => {
      const target = String(url);
      if (target.includes('/leases')) {
        leased += 1;
        return jsonResponse(200, {
          lease: { jobId: 'job-1', taskId: 'task-' + leased, generation: 1, token: 'tok' },
          taskEnvelope: {
            payload: JSON.stringify({
              version: 1, jobId: 'job-1', taskId: 'task-' + leased,
              artifactSha256: WASM_DIGEST, inputSha256: 'b'.repeat(64),
              issuedAt: 1, expiresAt: 9999999999, keyId: 'k'
            }),
            signature: 'c2ln'
          },
          moduleArtifactIds: ['art-1'],
          task: { input: { kind: 'matmul-f32-gpu', n: 4 } }
        });
      }
      if (target.includes('/artifacts/')) {
        return { ok: true, status: 200, arrayBuffer: async () => WASM_BYTES.slice().buffer };
      }
      return jsonResponse(200, {});
    };
    try {
      const app = new ContributeApp({ budget, autoStart: false });
      app.hwConcurrency = 8;
      app.workerCount = 4;
      app.gpus = [{}, {}];
      app.sourceMode = 'LIVE';
      app.tabIds = { tp: 'tab-tp' };
      app.tabId = 'tab-tp';
      app.runnerReady = true;
      app.runnerNonce = 'nonce-gpu';
      app.port = { postMessage() {} };
      app.isRunning = true;
      // The registration claims the GPU lanes; without a claim the loop cannot
      // start a GPU job either (a registration is the only way to get them).
      app.applyGpuBudgetClaim();
      await app.scheduleNextTask();
      if (app.taskTimer) { clearTimeout(app.taskTimer); app.taskTimer = null; }
      assert.equal(leased, 0, 'the legacy lane holds the whole tab: no slot for the new pools');
      // The GPU lanes are not a back door into the ceiling either.
      assert.ok(budget.used() <= budget.total());
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

});

describe('T104 fallback boot path (contribute.js standalone block)', () => {

  // The standalone block installs its DOMContentLoaded listener while the module
  // is evaluated, so each case imports a distinct copy of the module (a query
  // suffix), with `window` and `document` installed for that import only.
  async function withFallbackBoot(search, tag, run) {
    const hadWindow = Object.prototype.hasOwnProperty.call(globalThis, 'window');
    const realWindow = globalThis.window;
    const realDocument = globalThis.document;
    const realFetch = globalThis.fetch;
    const listeners = [];
    const host = {
      location: { search },
      addEventListener(type, fn) { if (type === 'DOMContentLoaded') listeners.push(fn); }
    };
    globalThis.window = host;
    globalThis.document = { cookie: '', getElementById: () => null };
    globalThis.fetch = async (url) => {
      if (String(url).endsWith('/workers/register')) return jsonResponse(201, { tabId: 'tab-fallback' });
      return jsonResponse(200, {});
    };
    try {
      await import('../js/contribute.js?fallback-boot=' + tag);
      assert.equal(listeners.length, 1, 'the standalone block is installed');
      listeners[0]({ type: 'DOMContentLoaded' });
      const app = host.contributeApp;
      assert.ok(app, 'the standalone block constructed the app');
      try {
        // The block starts init() without awaiting it; let it settle so its
        // fetches do not outlive the fake window of this case.
        await app.init().catch(() => {});
        await run(app);
      } finally {
        if (app.tokenRenewTimer) clearTimeout(app.tokenRenewTimer);
        if (app.taskTimer) clearTimeout(app.taskTimer);
        if (app.runnerRetryTimer) clearTimeout(app.runnerRetryTimer);
        try { app.stop(); } catch (_) { /* already idle */ }
      }
    } finally {
      if (hadWindow) globalThis.window = realWindow;
      else delete globalThis.window;
      if (realDocument === undefined) delete globalThis.document;
      else globalThis.document = realDocument;
      globalThis.fetch = realFetch;
    }
  }

  it('the fallback boot reads ?pools=tp,pp, exactly like the old path', async () => {
    await withFallbackBoot('?pools=tp,pp', 'pools', (app) => {
      assert.deepEqual(app.pools, ['tp', 'pp'], '?pools=tp,pp reaches the constructor');
    });
  });

  it('the fallback boot of a plain visit keeps the thread pool only', async () => {
    await withFallbackBoot('', 'plain', (app) => {
      assert.deepEqual(app.pools, ['tp'], 'no ?pools: the default pool');
    });
  });

  it('the fallback boot drops a junk ?pools value', async () => {
    await withFallbackBoot('?pools=bogus', 'junk', (app) => {
      assert.deepEqual(app.pools, ['tp'], 'a junk value falls back to the thread pool');
    });
  });

});

// END OF contribute_T104.test.mjs
