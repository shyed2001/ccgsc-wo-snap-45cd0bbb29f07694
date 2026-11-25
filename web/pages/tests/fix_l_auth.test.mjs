// WO_FIX_L fix_l_auth.test.mjs - Token storage and CSRF header of the admin config writes
// Author: hermes MiniMax-M3 for the owner | 2026-11-18
// What/why: WO_FIX_R3 row FIX-L (GLM R3 PARTIAL). Two findings are proved here:
//   1. getBearerToken must not read a token from sessionStorage any more. The
//      sessionStorage read path is an XSS readable token store, and nothing in the
//      tree ever writes one (the admin login of FIX-C hands out an HttpOnly
//      session cookie instead).
//   2. The admin config POSTs must carry the CSRF token of the cookie session.
//      internal/api/admin_config_F5.go (configAccess) answers 403 invalid-csrf
//      when the X-CSRF-Token header of such a write is missing or wrong.

import { describe, it, beforeEach, afterEach } from 'node:test';
import assert from 'node:assert/strict';

import {
  CSRF_COOKIE,
  CSRF_HEADER,
  SESSION_COOKIE,
  SESSION_PATH,
  getAuthHeaders,
  getBearerToken,
  getCSRFHeaders,
  getCSRFToken,
  getPoolCapabilities,
  getSessionState,
  hasCookieSession,
  readCookie,
  request,
  rollbackConfig,
  saveConfig,
  setBearerToken,
  setCSRFToken
} from '../js/api.js';

// --- test doubles -------------------------------------------------------------

// withCookie installs a document.cookie double and removes it again.
// The window double keeps an existing CCGSC_API_BASE override, so a test can
// point the client at another origin while the cookie is present.
function withCookie(value, fn) {
  const previousDoc = globalThis.document;
  const previousWin = globalThis.window;
  globalThis.document = { cookie: value };
  const base = (previousWin && previousWin.CCGSC_API_BASE) || undefined;
  globalThis.window = { location: { search: '' } };
  if (base) globalThis.window.CCGSC_API_BASE = base;
  try {
    return fn();
  } finally {
    globalThis.document = previousDoc;
    globalThis.window = previousWin;
  }
}

// withCookies installs a document.cookie double built from a name/value map.
function withCookies(map, fn) {
  const value = Object.entries(map)
    .map(([k, v]) => `${k}=${v}`)
    .join('; ');
  return withCookie(value, fn);
}

// A fetch double that records the request it was given.
function recordingFetch(response) {
  const calls = [];
  globalThis.fetch = async (url, options) => {
    calls.push({ url, options });
    return response;
  };
  return calls;
}

function jsonResponse(body, ok = true, status = 200) {
  return {
    ok,
    status,
    headers: { get: () => 'application/json' },
    json: async () => body
  };
}

const originalFetch = globalThis.fetch;
const originalDocument = globalThis.document;
const originalWindow = globalThis.window;
const originalSessionStorage = globalThis.sessionStorage;

// resetTokenState clears the module level state between the tests.
function resetTokenState() {
  setBearerToken('');
  setCSRFToken('');
}

describe('WO_FIX_L - token storage and CSRF of the admin config writes', () => {

  beforeEach(() => {
    resetTokenState();
    delete globalThis.sessionStorage;
    delete globalThis.document;
    delete globalThis.window;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    globalThis.document = originalDocument;
    globalThis.window = originalWindow;
    if (originalSessionStorage === undefined) delete globalThis.sessionStorage;
    else globalThis.sessionStorage = originalSessionStorage;
    resetTokenState();
  });

  describe('1. The bearer token stays in memory (MINOR token fallback)', () => {

    it('does not read the token from sessionStorage', () => {
      globalThis.sessionStorage = { getItem: (k) => (k === 'ccgsc_token' ? 'tok-from-storage' : null) };
      assert.equal(getBearerToken(), '', 'sessionStorage must not be a token source any more');
      assert.equal(getAuthHeaders().Authorization, undefined, 'no Authorization header may be derived from sessionStorage');
    });

    it('does not read the token from localStorage either', () => {
      globalThis.localStorage = { getItem: () => 'tok-from-local' };
      assert.equal(getBearerToken(), '', 'localStorage must not be a token source');
      delete globalThis.localStorage;
    });

    it('still returns the token that was set in memory', () => {
      setBearerToken('mem-token');
      assert.equal(getBearerToken(), 'mem-token');
      assert.equal(getAuthHeaders().Authorization, 'Bearer mem-token');
    });

    it('reads no cookie as a bearer token: the session cookie is HttpOnly', () => {
      withCookies({ [SESSION_COOKIE]: 'sess-123', [CSRF_COOKIE]: 'csrf-abc' }, () => {
        assert.equal(getBearerToken(), '', 'a cookie session must not produce an Authorization header');
      });
    });
  });

  describe('2. Reading the CSRF cookie of the admin session', () => {

    it('reads one named cookie out of the cookie string', () => {
      withCookies({ [CSRF_COOKIE]: 'csrf-abc', other: 'x' }, () => {
        assert.equal(readCookie(CSRF_COOKIE), 'csrf-abc');
        assert.equal(readCookie('other'), 'x');
        assert.equal(readCookie('missing'), '');
      });
    });

    it('survives a cookie string with spaces and an encoded value', () => {
      withCookie(` a=1; ${CSRF_COOKIE}=tok%20en; b=2`, () => {
        assert.equal(readCookie(CSRF_COOKIE), 'tok en');
      });
    });

    it('returns the empty string without a document', () => {
      assert.equal(readCookie(CSRF_COOKIE), '');
      assert.equal(getCSRFToken(), '');
      assert.equal(hasCookieSession(), false);
    });

    it('takes the CSRF token of the login response over the cookie', () => {
      withCookies({ [CSRF_COOKIE]: 'cookie-token' }, () => {
        setCSRFToken('login-token');
        assert.equal(getCSRFToken(), 'login-token');
        assert.equal(getCSRFHeaders()[CSRF_HEADER], 'login-token');
      });
    });

    it('falls back to the CSRF cookie when the login response gave nothing', () => {
      withCookies({ [CSRF_COOKIE]: 'cookie-token' }, () => {
        assert.equal(getCSRFToken(), 'cookie-token');
        assert.equal(hasCookieSession(), true);
        assert.equal(getCSRFHeaders()[CSRF_HEADER], 'cookie-token');
      });
    });

    it('sends no CSRF header when there is no cookie session', () => {
      assert.deepEqual(getCSRFHeaders(), {});
    });
  });

  describe('3. The admin config POSTs carry the CSRF token', () => {

    it('saveConfig sends the X-CSRF-Token header of the cookie session', async () => {
      delete globalThis.window; // live mode
      const calls = recordingFetch(jsonResponse({ version: 3 }, true, 201));
      await withCookies({ [CSRF_COOKIE]: 'csrf-session-token' }, async () => {
        await saveConfig('thread-pool-admin', {
          settings: { 'tp.wrk.max': 4 },
          reason: 'FIX-L CSRF header',
          expectedVersion: 2
        });
      });
      assert.equal(calls.length, 1, 'saveConfig must issue exactly one request');
      const headers = calls[0].options.headers;
      assert.equal(headers[CSRF_HEADER], 'csrf-session-token', 'the CSRF header must be present');
      assert.equal(calls[0].options.method, 'POST');
      assert.equal(calls[0].options.credentials, 'same-origin', 'the session cookie must ride along');
      delete globalThis.fetch;
    });

    it('rollbackConfig sends the X-CSRF-Token header of the cookie session', async () => {
      delete globalThis.window; // live mode
      const calls = recordingFetch(jsonResponse({ version: 1 }, true, 201));
      await withCookies({ [CSRF_COOKIE]: 'csrf-session-token' }, async () => {
        await rollbackConfig('thread-pool-admin', 1, 'FIX-L rollback CSRF header');
      });
      assert.equal(calls.length, 1);
      const headers = calls[0].options.headers;
      assert.equal(headers[CSRF_HEADER], 'csrf-session-token', 'the rollback must carry the CSRF header');
      assert.equal(calls[0].options.method, 'POST');
      delete globalThis.fetch;
    });

    it('sends no CSRF header when the client has no cookie session', async () => {
      delete globalThis.window; // live mode
      const calls = recordingFetch(jsonResponse({ version: 3 }, true, 201));
      await saveConfig('thread-pool-admin', {
        settings: {},
        reason: 'No session at all',
        expectedVersion: 1
      });
      const headers = calls[0].options.headers;
      assert.equal(headers[CSRF_HEADER], undefined, 'without a session there is no CSRF token to send');
      delete globalThis.fetch;
    });

    it('a caller supplied CSRF header is not overwritten', async () => {
      delete globalThis.window; // live mode
      const calls = recordingFetch(jsonResponse({ ok: true }, true, 201));
      await request('/admin/v1/config/thread-pool-admin', {
        method: 'POST',
        headers: { [CSRF_HEADER]: 'explicit-token' },
        body: '{}'
      });
      assert.equal(calls[0].options.headers[CSRF_HEADER], 'explicit-token');
      delete globalThis.fetch;
    });

    it('a GET carries no CSRF header', async () => {
      delete globalThis.window; // live mode
      const calls = recordingFetch(jsonResponse({ page: 'thread-pool-admin' }));
      await withCookies({ [CSRF_COOKIE]: 'csrf-session-token' }, async () => {
        await request('/admin/v1/config/thread-pool-admin');
      });
      assert.equal(calls[0].options.headers[CSRF_HEADER], undefined, 'a read must not carry a CSRF token');
      delete globalThis.fetch;
    });

    it('a cross origin API base gets no CSRF token and no cookie', async () => {
      globalThis.window = { CCGSC_API_BASE: 'https://other.example/api', location: { search: '' } };
      const calls = recordingFetch(jsonResponse({ ok: true }));
      await withCookies({ [CSRF_COOKIE]: 'csrf-session-token' }, async () => {
        await request('/admin/v1/config/thread-pool-admin', { method: 'POST', body: '{}' });
      });
      assert.equal(calls[0].options.headers[CSRF_HEADER], undefined, 'the CSRF token must not leave the origin');
      assert.equal(calls[0].options.credentials, 'omit', 'the session cookie must not leave the origin');
      delete globalThis.window;
      delete globalThis.fetch;
    });
  });

  describe('4. The cookie session keeps the live routes reachable', () => {

    it('getSessionState asks the server, because the session cookie is HttpOnly', async () => {
      const calls = recordingFetch(jsonResponse({ authenticated: true, user: { username: 'admin', roles: ['admin'] } }));
      const state = await getSessionState();
      assert.equal(calls[0].url, SESSION_PATH, 'the session endpoint of FIX-C must be used');
      assert.equal(calls[0].options.credentials, 'same-origin');
      assert.equal(state.authenticated, true);
      delete globalThis.fetch;
    });

    it('getSessionState answers null when the endpoint does not answer', async () => {
      globalThis.fetch = async () => { throw new Error('network down'); };
      assert.equal(await getSessionState(), null);
      delete globalThis.fetch;
    });

    it('getPoolCapabilities works with a cookie session and without a bearer token', async () => {
      const calls = recordingFetch(jsonResponse({ pool: 'thread' }));
      let caps = null;
      await withCookies({ [CSRF_COOKIE]: 'csrf-session-token' }, async () => {
        caps = await getPoolCapabilities('thread-pool/v1');
      });
      assert.ok(caps, 'the capability projection must be read');
      assert.equal(calls[0].options.headers.Authorization, undefined, 'no bearer token may be invented');
      assert.equal(calls[0].options.credentials, 'same-origin');
      delete globalThis.fetch;
    });

    it('getPoolCapabilities answers null without a token and without a session', async () => {
      const calls = recordingFetch(jsonResponse({ pool: 'thread' }));
      const caps = await getPoolCapabilities('thread-pool/v1');
      assert.equal(caps, null);
      assert.equal(calls.length, 0, 'no request may be sent without any credential');
      delete globalThis.fetch;
    });
  });

});

// END OF fix_l_auth.test.mjs
