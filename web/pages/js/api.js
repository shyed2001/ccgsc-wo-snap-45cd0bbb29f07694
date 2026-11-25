// WO-HC-WEB-01 api.js - REST & SSE API Client with RFC 9457 Problem Details & Bearer Auth
// Author: hermes MiniMax-M3 for Claude Opus 5.5 | 2026-11-17 (WO-T2.2: live pools-core endpoints,
//   mock.js fallback and the MOCK DATA badge).
// Original author: Claude Opus 5.5 head / agy Gemini 3.8 Flash high. Date: 2026-11-17.
// WO_FIX_L (hermes MiniMax-M3, 2026-11-18): the bearer token lives in memory only (the
//   sessionStorage read path is gone) and the admin config POSTs carry the CSRF token
//   of the FIX-C cookie session, so a cookie session can save and roll back a config.
// Truth: ctx_rows_R37_R40.md (R38.6, R40.2) + CCGSC_TWO_POOLS_DESIGN.md §9
// Live sources read from internal/api: GET /healthz, GET /{thread,process}-pool/v1/capabilities,
//   GET /{pool}/v1/events (SSE). Everything else is not served by pools-core yet and falls back
//   to mock.js; the badge then says so.

import { isMockMode, getMockData, handleMockPost, createMockEventStream } from './mock.js';

export const DATA_SOURCE = { LIVE: 'live', MOCK: 'mock' };

const state = { live: false, mock: false, reasons: [] };

// --- data source bookkeeping -------------------------------------------------

export function markLive(reason) {
  state.live = true;
  if (reason) {
    const msg = `live: ${reason}`;
    if (!state.reasons.includes(msg)) state.reasons.push(msg);
  }
  return state;
}

export function markMock(reason) {
  state.mock = true;
  if (reason && !state.reasons.includes(reason)) state.reasons.push(reason);
  return state;
}

// getDataSource reports MOCK as soon as any displayed value came from mock.js.
export function getDataSource() {
  return state.mock ? DATA_SOURCE.MOCK : (state.live ? DATA_SOURCE.LIVE : DATA_SOURCE.MOCK);
}

export function isMockData() {
  return getDataSource() === DATA_SOURCE.MOCK;
}

export function getDataSourceReasons() {
  return state.reasons.slice();
}

// --- the small MOCK DATA badge ------------------------------------------------

export const BADGE_ID = 'ccgsc-data-source-badge';

// renderDataSourceBadge shows or removes the "MOCK DATA" badge. It is a small,
// non-blocking hint: the page still renders, but the reader knows the numbers
// did not come from pools-core.
export function renderDataSourceBadge() {
  if (typeof document === 'undefined') return null;
  if (!isMockData()) {
    const stale = findBadge();
    if (stale && stale.parentNode) stale.parentNode.removeChild(stale);
    return null;
  }
  let badge = findBadge();
  if (!badge) {
    badge = document.createElement('span');
    badge.id = BADGE_ID;
    badge.className = 'data-source-badge';
    badge.setAttribute('role', 'status');
    badge.textContent = 'MOCK DATA';
    if (document.body) document.body.appendChild(badge);
  }
  const reasons = getDataSourceReasons();
  badge.title = reasons.length
    ? `Mock data in use - ${reasons.join('; ')}`
    : 'Mock data in use: pools-core did not answer the real endpoints.';
  return badge;
}

// findBadge locates the one badge element, so repeated calls never stack copies.
function findBadge() {
  if (typeof document === 'undefined') return null;
  if (typeof document.getElementById === 'function') {
    const byId = document.getElementById(BADGE_ID);
    if (byId) return byId;
  }
  if (document.body && typeof document.body.querySelector === 'function') {
    return document.body.querySelector('.data-source-badge');
  }
  return null;
}

// --- configuration ------------------------------------------------------------

export function getApiBase() {
  if (typeof window !== 'undefined' && window.CCGSC_API_BASE) {
    return window.CCGSC_API_BASE;
  }
  // Same origin: pools-core serves /healthz and /{pool}/v1 from the page host.
  return '';
}

let _inMemoryToken = '';

export function setBearerToken(token) {
  _inMemoryToken = token || '';
}

// getBearerToken returns the in-memory bearer token and nothing else.
// WO_FIX_L: the sessionStorage fallback ('ccgsc_token') is gone. sessionStorage
// belongs to the origin, survives until the tab closes and is readable by every
// script of the origin, so a token kept there is a token an XSS can take. The
// server also accepts the HttpOnly admin session cookie (FIX-C), so the pages
// do not need a readable token at all.
export function getBearerToken() {
  return _inMemoryToken;
}

// --- cookies and the CSRF token of the admin session --------------------------

// The FIX-C admin login (internal/api/admin_auth.go) sets two cookies:
//   - ccgsc_admin_session: the session id, HttpOnly + Secure + SameSite=Strict.
//     A script can never read it, so the client cannot detect it.
//   - ccgsc_csrf: the CSRF token of that session. It is deliberately NOT
//     HttpOnly, so the page can put it into the X-CSRF-Token header.
export const SESSION_COOKIE = 'ccgsc_admin_session';
export const CSRF_COOKIE = 'ccgsc_csrf';
export const CSRF_HEADER = 'X-CSRF-Token';
// The FIX-C session endpoint answers {"authenticated": true|false, "user": {...}}.
export const SESSION_PATH = '/pools/auth/session';

// readCookie returns the value of one cookie of the current origin, or ''.
export function readCookie(name) {
  if (typeof document === 'undefined') return '';
  if (typeof document.cookie !== 'string' || !document.cookie) return '';
  const prefix = `${name}=`;
  for (const part of document.cookie.split(';')) {
    const entry = part.trim();
    if (!entry.startsWith(prefix)) continue;
    const raw = entry.slice(prefix.length);
    try {
      return decodeURIComponent(raw);
    } catch (_) {
      return raw;
    }
  }
  return '';
}

let _csrfToken = '';

// setCSRFToken hands the CSRF token of the login response (POST /pools/auth/login
// answers {"csrfToken": ...}) to the API client. It wins over the cookie.
export function setCSRFToken(token) {
  _csrfToken = token || '';
}

// getCSRFToken returns the CSRF token of the admin session: the one handed over
// by the login response, or the ccgsc_csrf cookie of the FIX-C session.
// The HttpOnly session cookie cannot be read, so the CSRF cookie is the only
// proof a client has that it runs under a cookie session.
export function getCSRFToken() {
  if (_csrfToken) return _csrfToken;
  return readCookie(CSRF_COOKIE);
}

// getCSRFHeaders returns the header a state-changing request needs when the
// client runs under a cookie session. pools-core rejects such a write with
// 403 invalid-csrf when the X-CSRF-Token header does not match the CSRF token
// of the session (internal/api/admin_config_F5.go, configAccess).
export function getCSRFHeaders() {
  const headers = {};
  const token = getCSRFToken();
  if (token) {
    headers[CSRF_HEADER] = token;
  }
  return headers;
}

// hasCookieSession reports whether the browser holds an admin session.
// The session cookie itself is HttpOnly and unreadable, but the CSRF cookie of
// the same session is readable and is issued together with it, so its presence
// is the proof that a cookie session exists.
export function hasCookieSession() {
  return !!getCSRFToken();
}

export function getAuthHeaders() {
  const headers = {
    'Accept': 'application/json, application/problem+json',
    'Content-Type': 'application/json'
  };
  const token = getBearerToken();
  if (token) {
    headers['Authorization'] = `Bearer ${token}`;
  }
  return headers;
}

// RFC 9457 Problem Details Custom Error
// WO R17-ENVELOPE: the body may also carry message, hint, requestId and a
// per-error retryable; message is preferred over detail, with detail as the
// fallback for a server that still sends the old body.
export class ProblemError extends Error {
  constructor(problem) {
    super(problem.message || problem.detail || problem.title || `HTTP ${problem.status}`);
    this.name = 'ProblemError';
    this.type = problem.type || 'about:blank';
    this.title = problem.title || 'Error';
    this.status = problem.status || 500;
    this.detail = problem.detail || '';
    this.hint = problem.hint || '';
    this.requestId = problem.requestId || problem.traceId || '';
    this.retryable = problem.retryable === true;
    this.instance = problem.instance || '';
    this.errors = problem.errors || [];
    this.raw = problem;
  }
}

// isSameOriginApi reports whether the API base points at the origin of the page.
// A relative base is the same origin; an absolute one is not. The CSRF token
// and the session cookie must never leave the origin that issued them.
function isSameOriginApi() {
  const base = getApiBase();
  if (!base) return true;
  if (base.startsWith('//')) return false;
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(base)) return false;
  return base.startsWith('/');
}

// The methods that change state. A cookie session needs the CSRF header on them.
const MUTATING_METHODS = new Set(['POST', 'PUT', 'PATCH', 'DELETE']);

// Universal fetch wrapper parsing RFC 9457 responses
export async function request(path, options = {}) {
  const base = getApiBase();
  const url = `${base}${path}`;
  const headers = { ...getAuthHeaders(), ...(options.headers || {}) };
  // WO_FIX_L: a state-changing request under a cookie session must carry the
  // CSRF token of that session. pools-core answers 403 invalid-csrf when the
  // X-CSRF-Token header is missing or does not match the session token
  // (internal/api/admin_config_F5.go, configAccess). A bearer token request
  // has no CSRF token and is not affected.
  const method = String(options.method || 'GET').toUpperCase();
  if (MUTATING_METHODS.has(method) && isSameOriginApi()) {
    const csrf = getCSRFHeaders();
    for (const [name, value] of Object.entries(csrf)) {
      if (headers[name] === undefined) headers[name] = value;
    }
  }
  const signal = options.signal || (typeof AbortSignal !== 'undefined' && AbortSignal.timeout ? AbortSignal.timeout(5000) : undefined);

  try {
    // The admin session of FIX-C lives in an HttpOnly cookie, so the cookie has
    // to travel with the request. 'same-origin' keeps it off cross-origin hosts.
    const credentials = options.credentials || (isSameOriginApi() ? 'same-origin' : 'omit');
    const res = await fetch(url, { ...options, headers, signal, credentials });
    const contentType = res.headers.get('content-type') || '';

    if (!res.ok) {
      let problem = null;
      if (contentType.includes('json')) {
        try { problem = await res.json(); } catch (_) {}
      }
      if (!problem) {
        problem = {
          status: res.status,
          title: res.statusText || 'Request Failed',
          detail: `Server returned HTTP ${res.status} for ${path}`
        };
      }
      throw new ProblemError(problem);
    }

    if (contentType.includes('json')) {
      return await res.json();
    }
    return await res.text();
  } catch (err) {
    if (err instanceof ProblemError) throw err;
    throw new ProblemError({
      status: 0,
      title: 'Network or Connection Error',
      detail: err.message || 'Failed to communicate with API server'
    });
  }
}

// --- live pools-core probes ---------------------------------------------------

// getServiceHealth calls the one endpoint pools-core always serves.
export async function getServiceHealth() {
  try {
    const signal = (typeof AbortSignal !== 'undefined' && AbortSignal.timeout) ? AbortSignal.timeout(5000) : undefined;
    const res = await fetch(`${getApiBase()}/healthz`, {
      headers: { 'Accept': 'application/json' },
      signal
    });
    if (!res.ok) return null;
    const body = await res.json();
    if (!body || body.status !== 'ok') return null;
    return body;
  } catch (_) {
    return null;
  }
}

// getSessionState asks pools-core whether the browser holds an admin session.
// The session cookie is HttpOnly, so the client cannot look at it itself: only
// the server can answer. This is what tells the page that it may talk to the
// protected routes without a bearer token.
export async function getSessionState() {
  if (!isSameOriginApi()) return null;
  try {
    const signal = (typeof AbortSignal !== 'undefined' && AbortSignal.timeout) ? AbortSignal.timeout(5000) : undefined;
    const res = await fetch(`${getApiBase()}${SESSION_PATH}`, {
      headers: { 'Accept': 'application/json' },
      credentials: 'same-origin',
      signal
    });
    if (!res.ok) return null;
    const body = await res.json();
    if (!body || typeof body.authenticated !== 'boolean') return null;
    return body;
  } catch (_) {
    return null;
  }
}

// getPoolCapabilities reads the real capability projection of one pool family.
// It needs a bearer token or an admin session cookie; without either one the
// server answers 401 and the caller gets null.
export async function getPoolCapabilities(prefix) {
  const token = getBearerToken();
  if (!token && !hasCookieSession()) return null;
  try {
    const signal = (typeof AbortSignal !== 'undefined' && AbortSignal.timeout) ? AbortSignal.timeout(5000) : undefined;
    const headers = { 'Accept': 'application/json' };
    if (token) headers['Authorization'] = `Bearer ${token}`;
    const res = await fetch(`${getApiBase()}/${prefix}/capabilities`, {
      headers,
      credentials: isSameOriginApi() ? 'same-origin' : 'omit',
      signal
    });
    if (!res.ok) return null;
    return await res.json();
  } catch (_) {
    return null;
  }
}

// LiveEventTracker turns the durable pools-core event stream into the few
// monitor rows the stream can answer truthfully.
export class LiveEventTracker {
  constructor() {
    this.reset();
  }

  reset() {
    this.settled = [];       // timestamps of settled tasks
    this.runningJobs = new Set();
    this.counts = { taskDone: 0, taskFailed: 0, jobDone: 0, workerLost: 0 };
  }

  accept(event) {
    if (!event || !event.type) return;
    switch (event.type) {
      case 'task.done':
        this.counts.taskDone++;
        this.settled.push(Date.now());
        if (event.jobId) this.runningJobs.add(event.jobId);
        break;
      case 'task.failed':
        this.counts.taskFailed++;
        this.settled.push(Date.now());
        if (event.jobId) this.runningJobs.add(event.jobId);
        break;
      case 'job.done':
        this.counts.jobDone++;
        if (event.jobId) this.runningJobs.delete(event.jobId);
        break;
      case 'worker.lost':
        this.counts.workerLost++;
        break;
      default:
        break;
    }
  }

  // snapshot returns only rows whose meaning matches the event stream exactly.
  snapshot() {
    const now = Date.now();
    const window = 10000;
    this.settled = this.settled.filter(t => now - t <= window);
    const rows = {
      'mon.work.tasks_per_s': Number((this.settled.length / (window / 1000)).toFixed(1)),
      'mon.work.running': this.runningJobs.size
    };
    return rows;
  }
}

let liveTracker = null;

// getLiveMetricRows returns the live rows known right now, if any.
export function getLiveMetricRows() {
  if (!liveTracker) return {};
  return liveTracker.snapshot();
}

// --- data endpoints: live first, mock as the documented fallback --------------

// Fetch live metrics across both pools and APIs
export async function getMetrics() {
  if (isMockMode()) {
    markMock('mock mode requested');
    return getMockData('metrics');
  }
  const health = await getServiceHealth();
  if (!health) {
    markMock('pools-core /healthz did not answer');
    return getMockData('metrics');
  }
  markLive('pools-core /healthz answered');
  const live = getLiveMetricRows();
  if (Object.keys(live).length === 0) {
    markMock('pools-core exposes no metric counters yet, so the metric values are mock data');
    return getMockData('metrics');
  }
  markMock('pools-core exposed only partial metric counters; remaining metric values are mock data');
  return { ...getMockData('metrics'), ...live };
}

// Fetch config versions and active settings for a page
export async function getConfig(page) {
  if (isMockMode()) {
    markMock('mock mode requested');
    return getMockData(`config/${page}`);
  }
  try {
    const live = await request(`/admin/v1/config/${page}`);
    markLive(`pools-core served /admin/v1/config/${page}`);
    return live;
  } catch (err) {
    markMock(`pools-core has no config route for ${page} (${err.status || err.message})`);
    return getMockData(`config/${page}`);
  }
}

// Save a new version of configuration with required audit reason and conflict check
export async function saveConfig(page, { settings, reason, pinName, expectedVersion }) {
  if (!reason || !reason.trim()) {
    throw new ProblemError({
      type: 'https://ccgsc.digitalbd.org/errors/missing-reason',
      title: 'Validation Error',
      status: 400,
      detail: 'A changelog reason is required on every configuration save (WWPP-R38.6).'
    });
  }

  const payload = { settings, reason, pinName: pinName || null, expectedVersion };

  if (isMockMode()) {
    markMock('mock mode requested');
    return handleMockPost(`config/${page}`, payload);
  }
  // Live writes never fall back to mock on failure
  // WO_FIX_L: the admin config write carries the CSRF token of the cookie
  // session, otherwise pools-core answers 403 invalid-csrf (admin_config_F5.go).
  const live = await request(`/admin/v1/config/${page}`, {
    method: 'POST',
    headers: getCSRFHeaders(),
    body: JSON.stringify(payload)
  });
  markLive(`pools-core stored the config version for ${page}`);
  return live;
}

// Rollback to a prior version (creates a new audited version copying prior settings)
export async function rollbackConfig(page, targetVersion, reason) {
  const rollbackReason = reason || `Rollback to version ${targetVersion}`;
  const payload = { rollbackVersion: targetVersion, reason: rollbackReason };

  if (isMockMode()) {
    markMock('mock mode requested');
    return handleMockPost(`config/${page}/rollback`, payload);
  }
  // Live writes never fall back to mock on failure
  // WO_FIX_L: the rollback is a state-changing write too, so it carries the
  // CSRF token of the cookie session as well.
  const live = await request(`/admin/v1/config/${page}`, {
    method: 'POST',
    headers: getCSRFHeaders(),
    body: JSON.stringify(payload)
  });
  markLive(`pools-core rolled the config of ${page} back`);
  return live;
}

// --- Director pool settings (W10D rows 1/2, D-W10-11) ------------------------

// The pool settings endpoint of internal/api/pool_settings_W10C.go. It is the
// one home of verifyMode, the rates, isolationMode and contributorControls, and
// it applies the expectedVersion conflict rule. Both the page ids and the pool
// ids are accepted, so a caller never has to know which URL form the server
// prefers.
export const POOL_SETTINGS_IDS = Object.freeze({
  'thread-pool-admin': 'thread-pool',
  'process-pool-admin': 'process-pool',
  'thread-pool': 'thread-pool',
  'process-pool': 'process-pool',
  tp: 'thread-pool',
  pp: 'process-pool'
});

/**
 * Resolve one pool id of the settings endpoint, or null for an unknown name.
 *
 * @param {string} pageOrPool
 * @returns {string|null}
 */
export function poolSettingsId(pageOrPool) {
  const key = String(pageOrPool || '');
  return POOL_SETTINGS_IDS[key] || null;
}

/**
 * The settings path of one pool. An unknown page is a client error, never a
 * request to a guessed pool.
 *
 * @param {string} pageOrPool
 * @returns {string}
 */
export function poolSettingsPath(pageOrPool) {
  const id = poolSettingsId(pageOrPool);
  if (!id) {
    throw new ProblemError({
      type: 'https://ccgsc.digitalbd.org/errors/unknown-pool',
      title: 'Unknown pool',
      status: 400,
      detail: `No pool settings endpoint for ${String(pageOrPool || 'an empty name')}.`
    });
  }
  return `/admin/v1/pools/${id}/settings`;
}

/**
 * Read the Director settings of one pool (verifyMode, rates, isolationMode,
 * contributorControls and their version).
 *
 * @param {string} pageOrPool
 * @returns {Promise<Object>}
 */
export async function getPoolSettings(pageOrPool) {
  const settings = await request(poolSettingsPath(pageOrPool));
  markLive(`pools-core served the pool settings of ${poolSettingsId(pageOrPool)}`);
  return settings;
}

/**
 * Write the fields present in `changes` with the same expectedVersion rule as
 * every other versioned admin write. A stale version rejects with a ProblemError
 * of status 409 and nothing is written.
 *
 * @param {string} pageOrPool
 * @param {Object} changes
 * @param {number} [expectedVersion]
 * @returns {Promise<Object>}
 */
export async function savePoolSettings(pageOrPool, changes, expectedVersion) {
  const payload = { ...(changes || {}) };
  if (expectedVersion !== undefined && expectedVersion !== null) {
    payload.expectedVersion = expectedVersion;
  }
  const saved = await request(poolSettingsPath(pageOrPool), {
    method: 'PUT',
    body: JSON.stringify(payload)
  });
  markLive(`pools-core saved the pool settings of ${poolSettingsId(pageOrPool)}`);
  return saved;
}

// Fetch security audit log (append-only)
export async function getAuditLog() {
  if (isMockMode()) {
    markMock('mock mode requested');
    return getMockData('audit');
  }
  try {
    const live = await request('/admin/v1/audit');
    markLive('pools-core served /admin/v1/audit');
    return live;
  } catch (err) {
    markMock(`pools-core has no audit route yet (${err.status || err.message})`);
    return getMockData('audit');
  }
}

// --- live event stream --------------------------------------------------------

export async function readEventStream(url, token, onEvent, signal, options = {}) {
  const headers = { 'Accept': 'text/event-stream' };
  if (token) headers['Authorization'] = `Bearer ${token}`;
  if (options.lastEventId) headers['Last-Event-ID'] = options.lastEventId;

  // WO_FIX_L: without a bearer token the stream authenticates with the admin
  // session cookie, so the cookie has to travel with the request.
  const credentials = options.credentials || (token ? 'omit' : 'same-origin');
  const res = await fetch(url, { headers, signal, credentials });
  if (!res.ok || !res.body) {
    throw new Error(`event stream returned HTTP ${res.status}`);
  }
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  let currentLastEventId = options.lastEventId || '';

  for (;;) {
    const { done, value } = await reader.read();
    if (done) {
      return { ended: true, lastEventId: currentLastEventId };
    }
    buffer += decoder.decode(value, { stream: true });
    // Normalize CRLF to LF for reliable SSE framing
    buffer = buffer.replace(/\r\n/g, '\n');
    let split = buffer.indexOf('\n\n');
    while (split !== -1) {
      const frame = buffer.slice(0, split);
      buffer = buffer.slice(split + 2);
      const lines = frame.split('\n');
      for (const line of lines) {
        if (line.startsWith('id:')) {
          currentLastEventId = line.slice(3).trim();
          if (options.onId) options.onId(currentLastEventId);
        }
      }
      const data = lines
        .filter(line => line.startsWith('data:'))
        .map(line => line.slice(5).trim())
        .join('\n');
      if (data) {
        try {
          const parsed = JSON.parse(data);
          onEvent(parsed);
        } catch (e) {
          console.error('SSE parse error:', e);
        }
      }
      split = buffer.indexOf('\n\n');
    }
  }
}

export function getPoolUrls(poolOption) {
  const base = getApiBase();
  if (poolOption === 'process-pool' || poolOption === 'process') {
    return [`${base}/process-pool/v1/events`];
  }
  if (poolOption === 'thread-pool' || poolOption === 'thread') {
    return [`${base}/thread-pool/v1/events`];
  }
  return [
    `${base}/thread-pool/v1/events`,
    `${base}/process-pool/v1/events`
  ];
}

// Connect to Server-Sent Events stream for live numbers with backoff + Last-Event-ID + markMock on give-up
export function connectEvents(onMessage, onError, options = {}) {
  if (isMockMode()) {
    markMock('mock mode requested');
    return createMockEventStream(onMessage, onError);
  }
  const token = getBearerToken();
  // WO_FIX_L: the live event stream accepts a bearer token or an admin session
  // cookie. Without either one there is nothing to authenticate with, so the
  // page falls back to the mock stream as before.
  if (!token && !hasCookieSession()) {
    markMock('no bearer token and no admin session for the live event stream');
    return createMockEventStream(onMessage, onError);
  }
  const controller = new AbortController();
  const tracker = options.tracker || new LiveEventTracker();
  liveTracker = tracker;

  const poolUrls = getPoolUrls(options.pool);
  const maxRetries = options.maxRetries ?? 3;
  const baseDelay = options.baseDelay ?? 100;
  let fallback = null;
  const timers = new Set();

  function sleep(ms, signal) {
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        timers.delete(timer);
        resolve();
      }, ms);
      timers.add(timer);
      if (signal) {
        signal.addEventListener('abort', () => {
          clearTimeout(timer);
          timers.delete(timer);
          reject(new Error('aborted'));
        }, { once: true });
      }
    });
  }

  function startStream(url) {
    let lastEventId = '';
    let retries = 0;

    async function loop() {
      while (!controller.signal.aborted) {
        try {
          await readEventStream(url, token, (event) => {
            retries = 0;
            tracker.accept(event);
            markLive('pools-core event stream connected');
            if (onMessage) onMessage(tracker.snapshot());
          }, controller.signal, {
            lastEventId,
            onId: (id) => { lastEventId = id; }
          });

          if (controller.signal.aborted) return;
          if (retries >= maxRetries) {
            giveUp(new Error(`event stream closed cleanly on ${url}`));
            return;
          }
          retries++;
          tracker.reset();
          const delay = Math.min(baseDelay * (2 ** (retries - 1)), 5000);
          await sleep(delay, controller.signal);
        } catch (err) {
          if (controller.signal.aborted || (err && err.name === 'AbortError')) return;
          if (retries >= maxRetries) {
            giveUp(err);
            return;
          }
          retries++;
          tracker.reset();
          const delay = Math.min(baseDelay * (2 ** (retries - 1)), 5000);
          try {
            await sleep(delay, controller.signal);
          } catch (_) {
            return;
          }
        }
      }
    }

    function giveUp(err) {
      if (controller.signal.aborted) return;
      markMock(`live event stream unavailable (${err.message || err})`);
      if (!fallback) fallback = createMockEventStream(onMessage, onError);
      if (onError) onError(err);
    }

    loop();
  }

  for (const url of poolUrls) {
    startStream(url);
  }

  return {
    close() {
      controller.abort();
      for (const t of timers) clearTimeout(t);
      timers.clear();
      if (fallback) fallback.close();
    }
  };
}

// Display RFC 9457 error in standard DOM error banner (safe DOM construction, whitelist http(s) hrefs)
export function displayError(container, error) {
  if (!container) return;
  const problem = (error instanceof ProblemError) ? error : new ProblemError({
    title: error?.title || 'Unexpected Error',
    status: error?.status || 500,
    detail: error?.detail || error?.message || String(error),
    instance: error?.instance || '',
    type: error?.type || ''
  });

  container.innerHTML = '';

  const h4 = document.createElement('h4');
  h4.textContent = `[RFC 9457] ${problem.status ? `HTTP ${problem.status}: ` : ''}${problem.title}`;
  container.appendChild(h4);

  const detailDiv = document.createElement('div');
  detailDiv.className = 'error-detail';
  // WO R17-ENVELOPE: show message and hint; ProblemError.message already falls
  // back to detail when the server predates the additive fields.
  detailDiv.textContent = problem.hint ? `${problem.message} ${problem.hint}` : problem.message;
  container.appendChild(detailDiv);

  if (problem.instance) {
    const instDiv = document.createElement('div');
    instDiv.style.cssText = 'font-size: 0.75rem; color: #f87171; margin-top: 4px;';
    instDiv.appendChild(document.createTextNode('Instance: '));
    const code = document.createElement('code');
    code.textContent = problem.instance;
    instDiv.appendChild(code);
    container.appendChild(instDiv);
  }

  if (problem.type && problem.type !== 'about:blank') {
    const typeDiv = document.createElement('div');
    typeDiv.style.cssText = 'font-size: 0.75rem; color: #f87171;';
    typeDiv.appendChild(document.createTextNode('Type: '));
    const isHttp = /^https?:\/\//i.test(problem.type);
    if (isHttp) {
      const a = document.createElement('a');
      a.href = problem.type;
      a.target = '_blank';
      a.rel = 'noopener noreferrer';
      a.style.color = '#fca5a5';
      a.textContent = problem.type;
      typeDiv.appendChild(a);
    } else {
      const span = document.createElement('span');
      span.textContent = problem.type;
      typeDiv.appendChild(span);
    }
    container.appendChild(typeDiv);
  }
  container.classList.add('visible');
}

// Clear error banner
export function clearError(container) {
  if (!container) return;
  container.innerHTML = '';
  container.classList.remove('visible');
}

// Escapes special HTML and attribute characters
export function esc(str) {
  if (str === null || str === undefined) return '';
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

// END OF api.js
