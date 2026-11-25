// Path: web/pages/tests/contribute_w7.test.mjs | Author: cmdc for Claude Opus 5.5 | 2026-11-21
// What/why: one regression test per W7 fix of the contributor client.
//
//   WO-3  sendHeartbeat had no in-flight guard, so the 1 Hz upkeep tick overlapped
//         requests and two of them raced the same 401 recovery (agy MED, now WO-3).
//   WO-4  stop() returned early when !isRunning, leaving tokenRenewTimer (and the
//         upkeep worker) alive on a tab started with ?autostart=0 (agy MED, WO-4).
//   Triage (agy MED)  submitResult() and reportLeaseFailure() ignored 401/403, so a
//         credential that died mid-task dropped completed work silently.
//   Triage (agy LOW)  no test covered "same-subject 401 keeps the tab id" and the
//         escalating backoff across repeated failures.
//
// The suite is pure Node: ContributeApp is exercised directly, fetch and the timer
// globals are replaced for the duration of a test, and every double is restored.

import { describe, it } from 'node:test';
import assert from 'node:assert/strict';

import { ContributeApp } from '../js/contribute.js';

const originalFetch = globalThis.fetch;

function fakeJwt(sub) {
  const b = (o) => Buffer.from(JSON.stringify(o)).toString('base64url');
  return b({ alg: 'HS256' }) + '.' + b({ sub }) + '.sig';
}

// A fetch double that answers from a table and counts every URL it was asked for.
function recordingFetch(answer) {
  const calls = [];
  const fetchDouble = async (url, init) => {
    const u = String(url);
    calls.push({ url: u, body: init && init.body });
    await new Promise((r) => setTimeout(r, 1));
    return answer(u, init);
  };
  return { calls, fetchDouble };
}

describe('W7 contributor client fixes', () => {

  it('WO-3: sendHeartbeat keeps one request in flight and skips overlapping ticks', async () => {
    let inFlight = 0;
    let peak = 0;
    let finished = 0;
    const { calls, fetchDouble } = recordingFetch(async () => {
      inFlight++;
      peak = Math.max(peak, inFlight);
      await new Promise((r) => setTimeout(r, 25));
      inFlight--;
      finished++;
      return { ok: true, status: 200, json: async () => ({}) };
    });
    globalThis.fetch = fetchDouble;
    try {
      const app = new ContributeApp({ autoStart: false });
      app.sourceMode = 'LIVE';
      app.isRunning = true;
      app.tabId = 'tab-hb';
      app.tabIds = { tp: 'tab-hb' };

      // Ten upkeep ticks inside one heartbeat round trip: the old code fired ten
      // overlapping requests, and a 401 on any of them started a recovery.
      const pending = [];
      for (let i = 0; i < 10; i++) pending.push(app.sendHeartbeat());
      await Promise.all(pending);

      assert.equal(calls.filter((c) => c.url.includes('/heartbeat')).length, 1,
        'ten ticks inside one request must produce exactly one heartbeat');
      assert.equal(peak, 1, 'never two heartbeats in flight');
      assert.equal(finished, 1);

      // The guard is released, so the next tick does send.
      await app.sendHeartbeat();
      assert.equal(calls.filter((c) => c.url.includes('/heartbeat')).length, 2);
      assert.equal(app.heartbeatInFlight, false, 'the guard is clear again');
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it('WO-4: stop() clears every timer and the upkeep worker even when the tab never ran', async () => {
    const originalWorker = globalThis.Worker;
    const originalCreate = URL.createObjectURL;
    const originalRevoke = URL.revokeObjectURL;
    let terminated = false;
    globalThis.Worker = class { postMessage() {} terminate() { terminated = true; } };
    URL.createObjectURL = () => 'blob:w7-idle';
    URL.revokeObjectURL = () => {};
    try {
      // ?autostart=0: init() creates the upkeep worker and the renewal path
      // schedules a timer while isRunning is still false.
      const app = new ContributeApp({ autoStart: false });
      app.initUpkeepWorker();
      app.tokenRenewTimer = setTimeout(() => {}, 60_000);
      app.taskTimer = setTimeout(() => {}, 60_000);
      app.runnerRetryTimer = setTimeout(() => {}, 60_000);
      assert.equal(app.isRunning, false);

      app.stop();

      assert.equal(app.tokenRenewTimer, null, 'tokenRenewTimer must be cleared on an idle stop()');
      assert.equal(app.taskTimer, null, 'taskTimer must be cleared on an idle stop()');
      assert.equal(app.runnerRetryTimer, null, 'runnerRetryTimer must be cleared on an idle stop()');
      assert.ok(terminated, 'the upkeep worker must be terminated on an idle stop()');
      assert.equal(app.upkeepWorker, null);
    } finally {
      globalThis.Worker = originalWorker;
      URL.createObjectURL = originalCreate;
      URL.revokeObjectURL = originalRevoke;
    }
  });

  it('WO-4: a renewal that completes after stop() does not re-arm a live timer', async () => {
    const app = new ContributeApp({ autoStart: false });
    app.isAnonymous = true;
    app.authToken = fakeJwt('anon-late');
    // Capture the scheduled callback instead of waiting 13 minutes for it.
    const originalSetTimeout = globalThis.setTimeout;
    const scheduled = [];
    globalThis.setTimeout = (fn, ms) => { scheduled.push({ fn, ms }); return { fake: true }; };
    try {
      // A renewal started before stop(), completed after it: the generation it
      // captured is stale, so firing its timer must not renew anything.
      app.scheduleTokenRenewal(900);
      assert.equal(scheduled.length, 1, 'the late renewal schedules one timer');
      app.stop();
      let renewals = 0;
      app.renewAnonymous = async () => { renewals++; };
      scheduled[0].fn();
      await new Promise((r) => originalSetTimeout(r, 5));
      assert.equal(renewals, 0, 'a timer of a stopped generation must not renew');

      // Control: without the stop, the very same timer does renew, so the guard is
      // what suppresses it and not some unrelated condition.
      const live = new ContributeApp({ autoStart: false });
      live.isAnonymous = true;
      let liveRenewals = 0;
      live.renewAnonymous = async () => { liveRenewals++; };
      live.scheduleTokenRenewal(900);
      scheduled[1].fn();
      await new Promise((r) => originalSetTimeout(r, 5));
      assert.equal(liveRenewals, 1, 'a running tab still renews from its timer');
    } finally {
      globalThis.setTimeout = originalSetTimeout;
    }
  });

  it('triage: a 401 on submitResult drives the auth recovery path', async () => {
    const { calls, fetchDouble } = recordingFetch((u) => {
      if (u.includes('/results')) return { ok: false, status: 401, headers: { get: () => null }, json: async () => ({}) };
      if (u.includes('/workers/anonymous')) {
        return { ok: true, status: 200, json: async () => ({ accessToken: fakeJwt('anon-submit'), expiresIn: 900 }) };
      }
      return { ok: true, status: 200, json: async () => ({}) };
    });
    globalThis.fetch = fetchDouble;
    try {
      const app = new ContributeApp({ autoStart: false });
      app.sourceMode = 'LIVE';
      app.isAnonymous = true;
      app.authToken = fakeJwt('anon-submit');
      app.tabId = 'tab-submit';
      app.tabIds = { tp: 'tab-submit' };
      app.currentLease = { taskId: 't1' };
      app.renderStats = () => {};

      await app.submitResult({ taskId: 't1', ok: true, output: { answer: 42 } });

      assert.ok(app.authBlockedUntil > Date.now(), 'a 401 on a result must pause claims');
      assert.equal(calls.filter((c) => c.url.includes('/workers/anonymous')).length, 1,
        'the 401 must renew the anonymous session');
      if (app.tokenRenewTimer) clearTimeout(app.tokenRenewTimer);
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it('triage: a 403 on reportLeaseFailure drives the auth recovery path', async () => {
    const { calls, fetchDouble } = recordingFetch((u) => {
      if (u.includes('/results')) return { ok: false, status: 403, headers: { get: () => null }, json: async () => ({}) };
      if (u.includes('/workers/anonymous')) {
        return { ok: true, status: 200, json: async () => ({ accessToken: fakeJwt('anon-lease'), expiresIn: 900 }) };
      }
      return { ok: true, status: 200, json: async () => ({}) };
    });
    globalThis.fetch = fetchDouble;
    try {
      const app = new ContributeApp({ autoStart: false });
      app.sourceMode = 'LIVE';
      app.isAnonymous = true;
      app.authToken = fakeJwt('anon-lease');
      app.tabId = 'tab-lease';
      app.tabIds = { tp: 'tab-lease' };
      app.currentLease = { taskId: 't1' };

      await app.reportLeaseFailure('artifact_empty');

      assert.ok(app.authBlockedUntil > Date.now(), 'a 403 on a failure report must pause claims');
      assert.equal(calls.filter((c) => c.url.includes('/workers/anonymous')).length, 1,
        'the 403 must renew the anonymous session');
      assert.equal(app.currentLease, null, 'the failed lease is released regardless');
      if (app.tokenRenewTimer) clearTimeout(app.tokenRenewTimer);
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it('triage: a same-subject 401 keeps the tab id and does not register again', async () => {
    const { calls, fetchDouble } = recordingFetch((u) => {
      if (u.includes('/leases')) return { ok: false, status: 401 };
      if (u.includes('/workers/anonymous')) {
        return { ok: true, status: 200, json: async () => ({ accessToken: fakeJwt('anon-steady'), expiresIn: 900 }) };
      }
      return { ok: true, status: 201, json: async () => ({ tabId: 'tab-should-not-be-used' }) };
    });
    globalThis.fetch = fetchDouble;
    try {
      const app = new ContributeApp({ autoStart: false });
      app.isAnonymous = true;
      app.isRunning = true;
      app.authToken = fakeJwt('anon-steady');
      app.sourceMode = 'LIVE';
      app.tabId = 'tab-steady';
      app.tabIds = { tp: 'tab-steady' };
      app.gpuAllowed = () => false;
      app.renderStats = () => {};

      assert.equal(await app.getLiveJob('tp'), null);

      assert.equal(app.tabId, 'tab-steady', 'the tab id survives a same-subject 401');
      assert.equal(calls.filter((c) => c.url.includes('/workers/register')).length, 0,
        'a same-subject 401 must not re-register the tab');
      assert.equal(calls.filter((c) => c.url.includes('/workers/anonymous')).length, 1);
      if (app.tokenRenewTimer) clearTimeout(app.tokenRenewTimer);
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it('triage: repeated 401/403 responses escalate 2 s -> 4 s -> 8 s without resetting', async () => {
    const app = new ContributeApp({ autoStart: false });
    try {
      // The failures are counted only when not already paused, exactly as the
      // recovery runs: each call clears the pause it just set.
      const delays = [];
      for (let i = 0; i < 3; i++) {
        const before = Date.now();
        app.authBlockedUntil = 0;
        await app.onAuthFailure(401);
        delays.push(app.authBlockedUntil - before);
      }
      assert.ok(delays[0] >= 2000 && delays[0] < 4000, `first pause 2 s, got ${delays[0]}`);
      assert.ok(delays[1] >= 4000 && delays[1] < 8000, `second pause 4 s, got ${delays[1]}`);
      assert.ok(delays[2] >= 8000 && delays[2] < 16000, `third pause 8 s, got ${delays[2]}`);

      // Only a granted lease resets the counter.
      assert.equal(app.authFailures, 3);
      const { fetchDouble } = recordingFetch(() => ({
        ok: true,
        status: 200,
        json: async () => ({
          lease: { jobId: 'j1', taskId: 't1', token: 'tok', generation: 1 },
          taskEnvelope: { payload: JSON.stringify({ jobId: 'j1', taskId: 't1', artifactSha256: 'a', inputSha256: 'a', issuedAt: 1, expiresAt: 2, keyId: 'k' }), signature: 'sig' },
          moduleArtifactIds: ['art-1']
        })
      }));
      globalThis.fetch = fetchDouble;
      app.sourceMode = 'LIVE';
      app.tabId = 'tab-backoff';
      app.tabIds = { tp: 'tab-backoff' };
      app.gpuAllowed = () => false;
      // The pause that the third failure set must not hide the successful claim.
      app.authBlockedUntil = 0;
      // The artifact fetch is part of the same claim; answer it without a body.
      const artifactFetch = globalThis.fetch;
      globalThis.fetch = async (url, init) => {
        if (String(url).includes('/artifacts/')) return { ok: false, status: 404 };
        return artifactFetch(url, init);
      };
      await app.getLiveJob('tp');
      assert.equal(app.authFailures, 0, 'a successful lease resets the backoff');
    } finally {
      // N13: a successful mocked lease may arm a poll/renew timer. Close the
      // tab under test so the Node test runner does not retain a live handle.
      // This strengthens teardown without changing any behavior assertions.
      app.stop();
      globalThis.fetch = originalFetch;
    }
  });

});
