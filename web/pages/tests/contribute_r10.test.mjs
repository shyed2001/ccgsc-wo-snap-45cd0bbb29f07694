// R10-FIX (minimax MAJOR x7 + chatgpt MAJOR result-ack): the contributor app's
// multi-lease correctness. Each test states the CORRECT behaviour, so it fails on
// the reviewed code and passes on the fix. Fake fetch only; no network, no DOM.
//
// Rows covered:
//   1362 heartbeat renews only the last claim's lease
//   1707 activeTasks keyed by taskId only -> same id from two pools collides
//   1934/2044 a finished job nulls the shared currentLease
//   1874 artifact bytes never compared with the digest the envelope authorizes
//   1585/1610 stop/start never re-registers after a layout switch
//   1692 cpu capacity not bounded by the slots the pools actually hold
//   2035/2066 result POST not acknowledged but lease + task cleared
//   MINOR gpuAllowed ignores the battery charge level
//   MINOR register/heartbeat never send browserTabId
import { describe, it, after } from 'node:test';
import assert from 'node:assert/strict';

import {
  API_BASE,
  PROCESS_POOL_BASE,
  ContributeApp,
  sha256Hex
} from '../js/contribute.js';
import { WorkerBudget } from '../js/worker_budget_T104.js';
import { createMemoryArtifactCache } from '../js/artifact_cache_T104.js';

const originalFetch = globalThis.fetch;

after(() => { globalThis.fetch = originalFetch; });

function jsonResponse(status, body) {
  return { ok: status >= 200 && status < 300, status, headers: { get: () => null }, json: async () => body };
}

// A recording fetch double. `routes` is a list of {match, method, reply(call)}.
function installFetch(routes = []) {
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    const call = {
      url: String(url),
      method: (init.method || 'GET').toUpperCase(),
      body: init.body ? JSON.parse(init.body) : null,
      headers: init.headers || {}
    };
    calls.push(call);
    for (const r of routes) {
      if (call.url.includes(r.match) && (!r.method || r.method === call.method)) return r.reply(call);
    }
    return jsonResponse(200, {});
  };
  return calls;
}

// The single-flight heartbeat and the settlement retry use real timers; a suite
// must not leave one armed or node:test never exits.
function disarm(app) {
  for (const key of ['taskTimer', 'tokenRenewTimer', 'runnerRetryTimer']) {
    if (app[key]) { clearTimeout(app[key]); app[key] = null; }
  }
  app.isRunning = false;
  app.pendingSettlement = null;
}

function liveApp(options = {}) {
  const app = new ContributeApp({ authToken: 'token-abc', autoStart: false, ...options });
  app.sourceMode = 'LIVE';
  app.tabId = 'tab-tp';
  app.registeredTabId = 'tab-tp';
  app.tabIds = { tp: 'tab-tp' };
  app.isRunning = true;
  app.runnerReady = true;
  app.runnerNonce = 'nonce-test';
  app.port = { postMessage() {} };
  app.renderStats = () => {};
  app.render = () => {};
  return app;
}

function assignmentFor(taskId, artifactSha, extra = {}) {
  const now = Math.floor(Date.now() / 1000);
  const payload = {
    version: 1, jobId: 'job-' + taskId, taskId,
    artifactSha256: artifactSha, inputSha256: 'b'.repeat(64),
    issuedAt: now, expiresAt: now + 3600, keyId: '0123456789abcdef'
  };
  return {
    lease: { jobId: payload.jobId, taskId, generation: 1, token: 'tok-' + taskId },
    taskEnvelope: { payload: JSON.stringify(payload), signature: 'c2ln' },
    moduleArtifactIds: ['art-1'],
    task: { needs: 'cpu', timeoutMs: 30000 },
    inputJSON: '{}',
    ...extra
  };
}

const WASM = new TextEncoder().encode('export function run() { return { output: null }; }');
const WASM_SHA = await sha256Hex(WASM);

// ---------------------------------------------------------------- 1. renewal

describe('R10-FIX: every in-flight lease is renewed', () => {

  it('the heartbeat renews each claim, not only the most recent one', async () => {
    const calls = installFetch();
    const app = liveApp();
    app.tabIds = { tp: 'tab-tp', pp: 'tab-pp' };
    app.pools = ['tp', 'pp'];
    app.currentPool = 'pp';
    app.currentLease = { jobId: 'j-pp', taskId: 'task-pp' };
    app.activeTasks.set('task-tp', {
      lease: { jobId: 'j-tp', taskId: 'task-tp' }, pool: 'tp', tabId: 'tab-tp'
    });
    app.activeTasks.set('task-pp', {
      lease: { jobId: 'j-pp', taskId: 'task-pp' }, pool: 'pp', tabId: 'tab-pp'
    });
    await app.sendHeartbeat();

    const tp = calls.find((c) => c.url === API_BASE + '/workers/tab-tp/heartbeat');
    const pp = calls.find((c) => c.url === PROCESS_POOL_BASE + '/workers/tab-pp/heartbeat');
    assert.ok(tp, 'the thread pool keep-alive is missing');
    assert.ok(pp, 'the process pool keep-alive is missing');
    assert.deepEqual(tp.body && tp.body.lease, { jobId: 'j-tp', taskId: 'task-tp' },
      'the lease still running in the thread pool is never renewed');
    assert.deepEqual(pp.body && pp.body.lease, { jobId: 'j-pp', taskId: 'task-pp' },
      'the lease of the last claim is renewed');
  });

  it('an idle pool still gets its keep-alive, without a lease', async () => {
    const calls = installFetch();
    const app = liveApp();
    app.tabIds = { tp: 'tab-tp', pp: 'tab-pp' };
    app.pools = ['tp', 'pp'];
    app.activeTasks.set('task-tp', { lease: { jobId: 'j-tp', taskId: 'task-tp' }, pool: 'tp', tabId: 'tab-tp' });
    await app.sendHeartbeat();
    const pp = calls.find((c) => c.url === PROCESS_POOL_BASE + '/workers/tab-pp/heartbeat');
    assert.ok(pp, 'the tab with no lease on that pool still needs its keep-alive');
    assert.equal(pp.body.lease, undefined, 'nothing to renew on that pool');
    const tp = calls.find((c) => c.url === API_BASE + '/workers/tab-tp/heartbeat');
    assert.deepEqual(tp.body.lease, { jobId: 'j-tp', taskId: 'task-tp' });
  });

});

// ---------------------------------------------------------------- 2. duplicate ids

describe('R10-FIX: a task id that is already running is not leased twice', () => {

  it('refuses and releases a duplicate task id from another pool', async () => {
    const calls = installFetch([
      { match: '/leases', method: 'POST', reply: async () => jsonResponse(200, assignmentFor('same', WASM_SHA)) },
      { match: '/artifacts/', reply: async () => ({ ok: true, status: 200, arrayBuffer: async () => WASM.slice().buffer }) }
    ]);
    const app = liveApp();
    app.tabIds = { tp: 'tab-tp', pp: 'tab-pp' };
    app.pools = ['tp', 'pp'];
    const first = await app.getLiveJob('tp');
    assert.ok(first, 'the first lease is valid');
    app.activeTasks.set('same', { lease: app.currentLease, pool: 'tp', tabId: 'tab-tp' });

    const second = await app.getLiveJob('pp');
    assert.equal(second, null, 'the same task id is leased and dispatched twice');
    const release = calls.filter((c) => c.url.includes('/results')).pop();
    assert.ok(release, 'the duplicate lease is never released');
    assert.equal(release.body.error, 'duplicate_task_id');
    assert.equal(app.activeTasks.size, 1, 'the running task is untouched');
  });

});

// ---------------------------------------------------------------- 3. lease clearing

describe('R10-FIX: one finished job only clears its own lease', () => {

  it('a result does not drop the lease of the job still running', async () => {
    const calls = installFetch();
    const app = liveApp();
    app.tabIds = { tp: 'tab-tp', pp: 'tab-pp' };
    app.pools = ['tp', 'pp'];
    const ppLease = { jobId: 'j-pp', taskId: 'task-pp' };
    const tpLease = { jobId: 'j-tp', taskId: 'task-tp' };
    app.currentPool = 'pp';
    app.currentLease = ppLease;
    app.activeTasks.set('task-pp', { lease: ppLease, pool: 'pp', tabId: 'tab-pp' });
    app.activeTasks.set('task-tp', { lease: tpLease, pool: 'tp', tabId: 'tab-tp' });

    await app.submitResult({ taskId: 'task-pp', ok: true, output: { n: 1 }, timings: { computeMs: 1 } });

    assert.equal(app.currentLease, null, 'the finished lease is released');
    calls.length = 0;
    await app.sendHeartbeat();
    const tp = calls.find((c) => c.url === API_BASE + '/workers/tab-tp/heartbeat');
    assert.deepEqual(tp.body && tp.body.lease, tpLease,
      'the result of one job dropped the lease of the job still running');
  });

  it('a lease failure report does not drop another pool lease', async () => {
    const calls = installFetch();
    const app = liveApp();
    app.currentPool = 'pp';
    app.currentLease = { jobId: 'j-pp', taskId: 'task-pp' };
    app.currentTaskEnvelope = { payload: 'x', signature: 'y' };
    const other = { jobId: 'j-x', taskId: 'task-x' };
    // A concurrent claim for another pool installed its own lease meanwhile.
    const reported = app.currentLease;
    const report = app.reportLeaseFailure('artifact_empty');
    app.currentLease = other;
    await report;
    assert.equal(app.currentLease, other, 'the failure report cleared a lease it does not own');
    assert.equal(calls.filter((c) => c.url.includes('/results')).length, 1);
    assert.deepEqual(calls[0].body.lease, reported);
  });

});

// ---------------------------------------------------------------- 4. artifact digest

describe('R10-FIX: the artifact bytes must match the authorized digest', () => {

  it('refuses bytes whose digest differs from the signed envelope', async () => {
    const authorized = await sha256Hex(new TextEncoder().encode('good'));
    const evil = new TextEncoder().encode('evil');
    const calls = installFetch([
      { match: '/leases', method: 'POST', reply: async () => jsonResponse(200, assignmentFor('t1', authorized)) },
      { match: '/artifacts/', reply: async () => ({ ok: true, status: 200, arrayBuffer: async () => evil.slice().buffer }) }
    ]);
    const app = liveApp();
    const job = await app.getLiveJob('tp');
    assert.equal(job, null, 'mismatching artifact bytes are dispatched to the runner');
    const release = calls.filter((c) => c.url.includes('/results')).pop();
    assert.ok(release, 'the lease of the refused job is never released');
    assert.equal(release.body.error, 'artifact_digest_mismatch');
  });

  it('a poisoned cache entry never reaches the runner', async () => {
    const authorized = await sha256Hex(new TextEncoder().encode('good'));
    const evil = new TextEncoder().encode('evil');
    const cache = createMemoryArtifactCache(); // no digest implementation: no re-hash on read
    await cache.put(authorized, evil.slice().buffer);
    const calls = installFetch([
      { match: '/leases', method: 'POST', reply: async () => jsonResponse(200, assignmentFor('t2', authorized)) },
      { match: '/artifacts/', reply: async () => ({ ok: true, status: 200, arrayBuffer: async () => evil.slice().buffer }) }
    ]);
    const app = liveApp({ artifactCache: cache });
    const job = await app.getLiveJob('tp');
    assert.equal(job, null, 'a poisoned cache hit is dispatched');
    assert.ok(calls.some((c) => c.url.includes('/results')), 'the lease is not released');
  });

  it('the matching artifact is dispatched and keeps its digest', async () => {
    installFetch([
      { match: '/leases', method: 'POST', reply: async () => jsonResponse(200, assignmentFor('t3', WASM_SHA)) },
      { match: '/artifacts/', reply: async () => ({ ok: true, status: 200, arrayBuffer: async () => WASM.slice().buffer }) }
    ]);
    const app = liveApp();
    const job = await app.getLiveJob('tp');
    assert.ok(job, 'a matching artifact must dispatch');
    assert.equal(job.wasmSha256, WASM_SHA);
  });

});

// ---------------------------------------------------------------- 5. stop / start

describe('R10-FIX: a restarted tab registers again', () => {

  it('start() after stop() re-registers before claiming', async () => {
    const calls = installFetch([
      { match: '/workers/register', method: 'POST', reply: () => jsonResponse(201, { tabId: 'tab-new' }) },
      { match: '/leases', method: 'POST', reply: () => ({ ok: true, status: 204 }) }
    ]);
    const app = liveApp();
    app.stop();
    calls.length = 0;
    await app.start();
    await new Promise((r) => setTimeout(r, 60));
    app.stop();
    assert.ok(calls.some((c) => c.url.includes('/workers/register')),
      'the tab keeps a tab id the server may have reaped while it was stopped');
    assert.equal(app.tabId, 'tab-new');
  });

  it('a tab that never stopped does not re-register on start', async () => {
    const calls = installFetch();
    const app = liveApp();
    app.isRunning = false;
    await app.start();
    await new Promise((r) => setTimeout(r, 40));
    app.stop();
    assert.equal(calls.filter((c) => c.url.includes('/workers/register')).length, 0);
  });

});

// ---------------------------------------------------------------- 6. budget bound

describe('R10-FIX: the pools never run more jobs than they hold slots for', () => {

  it('the claim loop capacity is bounded by the actual grant', async () => {
    const budget = new WorkerBudget({ total: 4 });
    budget.claim('legacy:matrix', 3); // free: 1
    let leased = 0;
    installFetch([
      { match: '/leases', method: 'POST', reply: async () => jsonResponse(200, assignmentFor('task-' + (++leased), WASM_SHA)) },
      { match: '/artifacts/', reply: async () => ({ ok: true, status: 200, arrayBuffer: async () => WASM.slice().buffer }) }
    ]);
    const app = liveApp({ budget });
    app.hwConcurrency = 8;
    app.workerCount = 4;
    let dispatched = 0;
    app.port = { postMessage() { dispatched += 1; } };
    try {
      await app.scheduleNextTask();
      const held = budget.usedBy('pools');
      assert.ok(dispatched > 0, 'no job was dispatched at all');
      assert.ok(dispatched <= held,
        `the pools dispatch ${dispatched} jobs while they hold ${held} slots`);
      assert.ok(dispatched <= 4, 'the tab ceiling is never exceeded');
      assert.equal(app.activeTasks.size, dispatched, 'every dispatched job is tracked');
    } finally {
      if (app.taskTimer) { clearTimeout(app.taskTimer); app.taskTimer = null; }
      disarm(app);
    }
  });

  it('a lane that frees its slots lets the pools use them', async () => {
    const budget = new WorkerBudget({ total: 4 });
    budget.claim('legacy:matrix', 4);
    let leased = 0;
    installFetch([
      { match: '/leases', method: 'POST', reply: async () => jsonResponse(200, assignmentFor('task-' + (++leased), WASM_SHA)) },
      { match: '/artifacts/', reply: async () => ({ ok: true, status: 200, arrayBuffer: async () => WASM.slice().buffer }) }
    ]);
    const app = liveApp({ budget });
    app.hwConcurrency = 8;
    app.workerCount = 4;
    let dispatched = 0;
    app.port = { postMessage() { dispatched += 1; } };
    try {
      await app.scheduleNextTask();
      assert.equal(dispatched, 0, 'nothing may run while the legacy lanes hold every slot');
      if (app.taskTimer) { clearTimeout(app.taskTimer); app.taskTimer = null; }
      budget.releaseAll('legacy:matrix');
      await app.scheduleNextTask();
      assert.equal(dispatched, 4, 'the freed slots are available to the pools again');
      assert.equal(budget.usedBy('pools'), 4, 'the pools hold what they run');
    } finally {
      if (app.taskTimer) { clearTimeout(app.taskTimer); app.taskTimer = null; }
      disarm(app);
    }
  });

});

// ---------------------------------------------------------------- 7. backoff keep-alive

describe('R10-FIX: the claim backoff does not stop the keep-alive', () => {

  it('a claim backoff still heartbeats', async () => {
    const calls = installFetch();
    const app = liveApp();
    app.authBlockedUntil = Date.now() + 60000;
    await app.sendHeartbeat();
    assert.ok(calls.some((c) => c.url.includes('/heartbeat')),
      'the 429/5xx claim backoff stopped the keep-alive; the engine will reap the tab');
    assert.equal(app.heartbeatInFlight, false, 'the single-flight guard is released');
  });

});

// ---------------------------------------------------------------- 8. result ack

describe('R10-FIX: an unacknowledged result keeps its lease and retries', () => {

  const lease = { jobId: 'j1', taskId: 't1' };

  function appWith(status) {
    const calls = installFetch([
      { match: '/results', method: 'POST', reply: () => jsonResponse(status, {}) }
    ]);
    const app = liveApp();
    app.currentPool = 'tp';
    app.currentLease = lease;
    app.activeTasks.set('t1', { lease, pool: 'tp', tabId: 'tab-tp', taskEnvelope: { p: 1 }, leaseReceipt: { r: 1 } });
    return { app, calls };
  }

  for (const status of [401, 403, 409, 429, 503]) {
    it(`HTTP ${status} does not release the result`, async () => {
      const { app } = appWith(status);
      const accepted = await app.submitResult({ taskId: 't1', ok: true, output: { n: 160, sha256: 'abc' }, timings: { computeMs: 1 } });
      assert.equal(accepted, false, `HTTP ${status} is not an acceptance`);
      assert.deepEqual(app.currentLease, lease, 'the lease of a rejected result is cleared');
      assert.equal(app.activeTasks.has('t1'), true, 'the task of a rejected result is forgotten');
    });
  }

  it('HTTP 200 releases the result', async () => {
    const { app } = appWith(200);
    const accepted = await app.submitResult({ taskId: 't1', ok: true, output: { n: 160, sha256: 'abc' }, timings: { computeMs: 1 } });
    assert.equal(accepted, true);
    assert.equal(app.currentLease, null, 'an accepted result releases its lease');
  });

  it('a rejected result is retried until the server accepts it', async () => {
    let status = 503;
    let posts = 0;
    installFetch([
      { match: '/results', method: 'POST', reply: () => { posts += 1; return jsonResponse(status, {}); } }
    ]);
    const app = liveApp();
    app.currentPool = 'tp';
    app.currentLease = lease;
    app.activeTasks.set('t1', { lease, pool: 'tp', tabId: 'tab-tp', taskEnvelope: { p: 1 }, leaseReceipt: { r: 1 } });
    const result = { type: 'result', taskId: 't1', ok: true, output: { n: 160, sha256: 'abc' }, timings: { computeMs: 1 } };
    app.onTaskResult(result);
    await new Promise((r) => setTimeout(r, 80));
    assert.equal(posts, 1, 'the first attempt was made');
    assert.equal(app.pendingSettlement && app.pendingSettlement.taskId, 't1', 'the result is retained');
    assert.equal(app.activeTasks.has('t1'), true, 'the task is retained');

    status = 200;
    await app.settlePendingResult();
    assert.equal(posts, 2, 'the retained result was not retried');
    assert.equal(app.pendingSettlement, null, 'an accepted result is cleared');
    assert.equal(app.activeTasks.has('t1'), false, 'the task is cleared after acceptance');
    if (app.taskTimer) { clearTimeout(app.taskTimer); app.taskTimer = null; }
    app.stop();
  });

  it('a result that is never accepted is not forgotten', async () => {
    const { app } = appWith(500);
    app.onTaskResult({ type: 'result', taskId: 't1', ok: true, output: { n: 1, sha256: 'x' }, timings: { computeMs: 1 } });
    await new Promise((r) => setTimeout(r, 40));
    assert.equal(app.activeTasks.has('t1'), true);
    assert.equal(app.pendingSettlement && app.pendingSettlement.taskId, 't1');
    if (app.taskTimer) { clearTimeout(app.taskTimer); app.taskTimer = null; }
    app.stop();
  });

});

// ---------------------------------------------------------------- 9. GPU guard + tab id

describe('R10-FIX: the GPU guard follows the owner battery rule', () => {

  it('a laptop on a full battery may run GPU lanes', () => {
    const app = liveApp();
    app.hasBattery = true;
    app.batteryLevel = 0.6;
    app.isCharging = false;
    app.isPhone = false;
    app.options.isLaptop = true;
    app.backoff = false;
    assert.equal(app.gpuAllowed(), true, 'a laptop at 60 % lost its GPU lanes');
  });

  it('no GPU lane under 20 % battery, none while charging is not required', () => {
    const app = liveApp();
    app.hasBattery = true;
    app.batteryLevel = 0.1;
    app.isCharging = false;
    app.isPhone = true;
    app.backoff = false;
    assert.equal(app.gpuAllowed(), false, 'no GPU under 20 %');

    app.isCharging = true;
    assert.equal(app.gpuAllowed(), true, 'charging always allows GPU');
  });

  it('a backoff still forbids GPU lanes', () => {
    const app = liveApp();
    app.hasBattery = false;
    app.isPhone = false;
    app.backoff = true;
    assert.equal(app.gpuAllowed(), false);
  });

});

describe('R10-FIX: registration and heartbeat carry browserTabId', () => {

  it('register sends the session-scoped tab id', async () => {
    const calls = installFetch([
      { match: '/workers/register', method: 'POST', reply: () => jsonResponse(201, { tabId: 'tab-tp' }) }
    ]);
    const app = liveApp();
    app.browserTabId = 'tab-session-1';
    await app.registerWorker();
    const reg = calls.find((c) => c.url.endsWith('/workers/register'));
    assert.ok(reg, 'the registration was sent');
    assert.equal(reg.body.browserTabId, 'tab-session-1',
      'two tabs of one origin share a localStorage hostId; the server cannot tell them apart');
  });

  it('heartbeat sends the session-scoped tab id too', async () => {
    const calls = installFetch();
    const app = liveApp();
    app.browserTabId = 'tab-session-1';
    await app.sendHeartbeat();
    const beat = calls.find((c) => c.url.includes('/heartbeat'));
    assert.ok(beat, 'the heartbeat was sent');
    assert.equal(beat.body.browserTabId, 'tab-session-1');
  });

  it('a page without sessionStorage still registers (empty id is the legacy rule)', async () => {
    const calls = installFetch([
      { match: '/workers/register', method: 'POST', reply: () => jsonResponse(201, { tabId: 'tab-tp' }) }
    ]);
    const app = liveApp();
    app.browserTabId = undefined;
    await app.registerWorker();
    const reg = calls.find((c) => c.url.endsWith('/workers/register'));
    assert.ok(reg);
    assert.equal(reg.body.browserTabId, '', 'an absent tab id degrades to the legacy match');
  });

});
