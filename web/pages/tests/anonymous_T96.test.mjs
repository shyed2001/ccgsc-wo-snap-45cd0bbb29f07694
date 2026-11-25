// Path: web/pages/tests/anonymous_T96.test.mjs | Author: Grok 4.7 for Claude Opus 5.5 | 2026-11-20
// What/why: fake-fetch coverage of the anonymous contributor join in contribute.js (WO_T96).
// The tab joins only when it has no contributor token and runner-config says anonymousJoin.
// The issued token stays in memory, is renewed before it expires, and a logged-in
// contributor still registers exactly as before.

import { describe, it, after } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { API_BASE, ContributeApp, readAuthToken } from '../js/contribute.js';

const here = path.dirname(fileURLToPath(import.meta.url));
const contributeJsPath = path.join(here, '..', 'js', 'contribute.js');

const originalFetch = globalThis.fetch;
const originalSetTimeout = globalThis.setTimeout;
const originalClearTimeout = globalThis.clearTimeout;
const originalDocument = globalThis.document;
const originalLocalStorage = globalThis.localStorage;
const originalSessionStorage = globalThis.sessionStorage;

function restoreGlobals() {
  globalThis.fetch = originalFetch;
  globalThis.setTimeout = originalSetTimeout;
  globalThis.clearTimeout = originalClearTimeout;
  if (originalDocument === undefined) {
    delete globalThis.document;
  } else {
    globalThis.document = originalDocument;
  }
  if (originalLocalStorage === undefined) {
    delete globalThis.localStorage;
  } else {
    globalThis.localStorage = originalLocalStorage;
  }
  if (originalSessionStorage === undefined) {
    delete globalThis.sessionStorage;
  } else {
    globalThis.sessionStorage = originalSessionStorage;
  }
}

after(restoreGlobals);

function jsonResponse(status, body) {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body
  };
}

/**
 * Install a fetch double and a storage double. Restores both when done.
 *
 * @param {Record<string, {status: number, body: Object}>} routes
 * @param {(app: ContributeApp, calls: Array<Object>, storage: Object) => Promise<void>} run
 */
async function withAnonymousPage(routes, run) {
  const calls = [];
  const storage = { writes: [], cookie: '' };
  globalThis.localStorage = {
    getItem() { return null; },
    setItem(key, value) { storage.writes.push(['local', key, value]); },
    removeItem() {}
  };
  globalThis.sessionStorage = {
    getItem() { return null; },
    setItem(key, value) { storage.writes.push(['session', key, value]); },
    removeItem() {}
  };
  globalThis.document = {
    get cookie() { return storage.cookie; },
    set cookie(value) { storage.writes.push(['cookie', value]); storage.cookie = String(value); }
  };
  globalThis.fetch = async (url, opts) => {
    const target = String(url);
    calls.push({
      url: target,
      method: (opts && opts.method) || 'GET',
      headers: (opts && opts.headers) || {}
    });
    const route = Object.keys(routes).find((suffix) => target.endsWith(suffix));
    if (!route) return jsonResponse(404, {});
    return jsonResponse(routes[route].status, routes[route].body);
  };
  const timers = [];
  globalThis.setTimeout = (fn, ms) => {
    const handle = { fn, ms };
    timers.push(handle);
    return handle;
  };
  globalThis.clearTimeout = (handle) => {
    const at = timers.indexOf(handle);
    if (at >= 0) timers.splice(at, 1);
  };
  try {
    const app = new ContributeApp();
    await run(app, calls, storage, timers);
    if (app.tokenRenewTimer && typeof originalClearTimeout === 'function') {
      // The stub handle is not a real timer. Drop it so the process does not keep it.
      app.tokenRenewTimer = null;
    }
  } finally {
    restoreGlobals();
  }
}

describe('WO_T96 anonymous contributor join', () => {
  it('joins anonymously only when runner-config says so, and keeps the token in memory', async () => {
    await withAnonymousPage({
      '/pools/runner-config': { status: 200, body: { anonymousJoin: true, runOrigin: null } },
      '/workers/anonymous': { status: 200, body: { accessToken: 'mem-anon-token', expiresIn: 900, tokenType: 'Bearer' } },
      '/workers/register': { status: 201, body: { tabId: 'srv-anon-1', hostId: 'host-1', poolId: 'tp', owner: 'anon-1', slots: 1, gpu: false } }
    }, async (app, calls, storage) => {
      const config = await app.loadRunnerConfig();
      assert.equal(config.anonymousJoin, true);
      assert.equal(app.anonymousJoin, true);
      await app.initSource();

      assert.equal(app.sourceMode, 'LIVE');
      assert.equal(app.registeredTabId, 'srv-anon-1');
      assert.equal(app.tabId, 'srv-anon-1');
      assert.equal(app.isAnonymous, true);
      assert.equal(app.authToken, 'mem-anon-token');
      assert.equal(storage.writes.length, 0, 'The anonymous token must not be written to cookie or web storage');
      assert.equal(storage.cookie, '');
      assert.equal(readAuthToken(), '', 'A later read must not recover the memory token from a cookie');

      const anonymousCall = calls.find((c) => c.url.endsWith('/workers/anonymous'));
      const registerCall = calls.find((c) => c.url.endsWith('/workers/register'));
      assert.ok(anonymousCall, 'The anonymous route must be called');
      assert.equal(anonymousCall.method, 'POST');
      assert.equal(anonymousCall.headers.Authorization, undefined, 'The first join has no contributor token');
      assert.ok(registerCall, 'Registration follows the join');
      assert.equal(registerCall.method, 'POST');
      assert.equal(registerCall.headers.Authorization, 'Bearer mem-anon-token');
      assert.equal(calls.filter((c) => c.url.includes('/leases')).length, 0);
    });
  });

  it('renews the memory token before it expires', async () => {
    let issued = 0;
    const calls = [];
    const timers = [];
    globalThis.fetch = async (url, opts) => {
      const target = String(url);
      calls.push({ url: target, method: (opts && opts.method) || 'GET', headers: (opts && opts.headers) || {} });
      if (target.endsWith('/pools/runner-config')) {
        return jsonResponse(200, { anonymousJoin: true, runOrigin: null });
      }
      if (target.endsWith('/workers/anonymous')) {
        issued += 1;
        return jsonResponse(200, { accessToken: 'mem-renew-' + issued, expiresIn: 180, tokenType: 'Bearer' });
      }
      if (target.endsWith('/workers/register')) {
        return jsonResponse(201, { tabId: 'srv-anon-2' });
      }
      return jsonResponse(404, {});
    };
    globalThis.setTimeout = (fn, ms) => {
      const handle = { fn, ms };
      timers.push(handle);
      return handle;
    };
    globalThis.clearTimeout = (handle) => {
      const at = timers.indexOf(handle);
      if (at >= 0) timers.splice(at, 1);
    };
    try {
      const app = new ContributeApp();
      await app.loadRunnerConfig();
      await app.initSource();
      assert.equal(app.authToken, 'mem-renew-1');
      assert.equal(timers.length, 1);
      const renewInMs = timers[0].ms;
      assert.equal(renewInMs, (180 - 120) * 1000);
      assert.ok(renewInMs < 180 * 1000, 'Renewal must be scheduled before the token expires');

      timers[0].fn();
      await new Promise((resolve) => originalSetTimeout(resolve, 0));
      assert.equal(issued, 2, 'The renewal must call the anonymous route again');
      assert.equal(app.authToken, 'mem-renew-2');
      assert.equal(app.isAnonymous, true);
      const renewal = calls.filter((c) => c.url.endsWith('/workers/anonymous'));
      assert.equal(renewal.length, 2);
      assert.equal(renewal[1].headers.Authorization, 'Bearer mem-renew-1', 'Renewal presents the current memory token');
    } finally {
      restoreGlobals();
    }
  });

  it('keeps the logged-in register path when a contributor token is already present', async () => {
    await withAnonymousPage({
      '/pools/runner-config': { status: 200, body: { anonymousJoin: true, runOrigin: null } },
      '/workers/anonymous': { status: 200, body: { accessToken: 'should-not-be-used', expiresIn: 900 } },
      '/workers/register': { status: 201, body: { tabId: 'srv-logged-in' } }
    }, async (app, calls) => {
      app.authToken = 'cookie-contributor-token';
      await app.loadRunnerConfig();
      assert.equal(app.anonymousJoin, true, 'The flag is on, and the logged-in tab still must not join anonymously');
      await app.initSource();

      assert.equal(app.sourceMode, 'LIVE');
      assert.equal(app.tabId, 'srv-logged-in');
      assert.equal(app.isAnonymous, false);
      assert.equal(app.authToken, 'cookie-contributor-token');
      assert.equal(calls.filter((c) => c.url.endsWith('/workers/anonymous')).length, 0);
      const registerCall = calls.find((c) => c.url.endsWith('/workers/register'));
      assert.ok(registerCall);
      assert.equal(registerCall.url, API_BASE + '/workers/register');
      assert.equal(registerCall.method, 'POST');
      assert.equal(registerCall.headers.Authorization, 'Bearer cookie-contributor-token');
    });
  });

  it('does not call the anonymous route when runner-config says anonymousJoin is false', async () => {
    await withAnonymousPage({
      '/pools/runner-config': { status: 200, body: { anonymousJoin: false, runOrigin: null } },
      '/workers/anonymous': { status: 200, body: { accessToken: 'should-not-be-used', expiresIn: 900 } },
      '/workers/register': { status: 201, body: { tabId: 'srv-plain' } }
    }, async (app, calls) => {
      await app.loadRunnerConfig();
      assert.equal(app.anonymousJoin, false);
      await app.initSource();
      assert.equal(app.sourceMode, 'LIVE');
      assert.equal(app.tabId, 'srv-plain');
      assert.equal(app.isAnonymous, false);
      assert.equal(app.authToken, '');
      assert.equal(calls.filter((c) => c.url.endsWith('/workers/anonymous')).length, 0);
      assert.equal(calls.filter((c) => c.url.endsWith('/workers/register')).length, 1);
    });
  });

  it('reads the contributor cookie and does not take the anonymous token from web storage', () => {
    const source = fs.readFileSync(contributeJsPath, 'utf8');
    const joinAt = source.indexOf('async joinAnonymous()');
    const renewAt = source.indexOf('scheduleTokenRenewal(expiresInSec)');
    assert.ok(joinAt > 0 && renewAt > joinAt);
    const joinFn = source.slice(joinAt, renewAt);
    assert.equal(joinFn.includes('localStorage'), false);
    assert.equal(joinFn.includes('sessionStorage'), false);
    assert.equal(joinFn.includes('document.cookie'), false);
    assert.match(source, /!this\.authToken && this\.anonymousJoin === true/);

    globalThis.document = { cookie: 'contributor_token=' + encodeURIComponent('from-cookie') };
    globalThis.localStorage = { getItem: () => 'from-local' };
    globalThis.sessionStorage = { getItem: () => 'from-session' };
    try {
      assert.equal(readAuthToken(), 'from-cookie');
      const app = new ContributeApp();
      assert.equal(app.authToken, 'from-cookie');
      assert.equal(app.isAnonymous, false);
      assert.equal(app.anonymousJoin, false);
    } finally {
      restoreGlobals();
    }
  });
});
