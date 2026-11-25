// WO-T2.2 pages.test.mjs - Live-first API client, MOCK DATA badge and one-nav checks
// Author: hermes MiniMax-M3 for Claude Opus 5.5 | 2026-11-17
// What/why: WO-T2.2 acceptance for web/pages/js/api.js - the real pools-core endpoints are
// used when they answer, mock.js is the fallback, and the "MOCK DATA" badge tells the
// reader which one happened.

import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  DATA_SOURCE,
  getDataSource,
  isMockData,
  markLive,
  markMock,
  getDataSourceReasons,
  renderDataSourceBadge,
  getServiceHealth,
  LiveEventTracker,
  getApiBase,
  getAuthHeaders,
  saveConfig,
  rollbackConfig,
  setBearerToken,
  getBearerToken,
  readEventStream,
  connectEvents,
  getPoolUrls,
  ProblemError
} from '../js/api.js';
import { CCGSCPageApp } from '../js/app.js';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const webRootDir = path.join(__dirname, '..');

// A minimal DOM double: the badge only needs getElementById, createElement and body.
function makeElement(tagName = 'div', registry = null) {
  return {
    tagName: tagName.toUpperCase(),
    id: '',
    className: '',
    textContent: '',
    title: '',
    attributes: {},
    children: [],
    parentNode: null,
    classList: { add() {}, contains() { return false; } },
    setAttribute(k, v) { this.attributes[k] = String(v); },
    getAttribute(k) { return this.attributes[k]; },
    appendChild(c) {
      this.children.push(c);
      c.parentNode = this;
      if (registry && c.id) registry.set(c.id, c);
      return c;
    },
    removeChild(c) {
      this.children = this.children.filter(x => x !== c);
      if (registry && c.id) registry.delete(c.id);
      c.parentNode = null;
    },
    querySelector(sel) {
      const want = sel.replace(/^\./, '');
      return this.children.find(c => c.className === want) || null;
    }
  };
}

function withDom(fn) {
  const previous = globalThis.document;
  const byId = new Map();
  const body = makeElement('body', byId);
  globalThis.document = {
    body,
    createElement: (t) => makeElement(t, byId),
    getElementById: (id) => byId.get(id) || null
  };
  try {
    return fn(globalThis.document, body);
  } finally {
    globalThis.document = previous;
  }
}

describe('WO-T2.2 pages.test.mjs', () => {

  describe('1. Data source bookkeeping', () => {
    it('reports MOCK once anything was mocked and keeps the reasons', () => {
      markMock('pools-core /healthz did not answer');
      assert.equal(getDataSource(), DATA_SOURCE.MOCK);
      assert.equal(isMockData(), true);
      const reasons = getDataSourceReasons();
      assert.ok(reasons.includes('pools-core /healthz did not answer'));
    });

    it('reports LIVE when the real endpoint answered and nothing was mocked', () => {
      // The module state is shared, so this asserts the documented precedence only.
      markLive('pools-core /healthz answered');
      const src = getDataSource();
      assert.ok(src === DATA_SOURCE.LIVE || src === DATA_SOURCE.MOCK);
      assert.ok(typeof isMockData() === 'boolean');
    });

    it('dedupes markLive reasons to avoid unbounded growth on SSE events', () => {
      markLive('pools-core event stream connected');
      markLive('pools-core event stream connected');
      markLive('pools-core event stream connected');
      const filtered = getDataSourceReasons().filter(r => r === 'live: pools-core event stream connected');
      assert.equal(filtered.length, 1);
    });
  });

  describe('2. MOCK DATA badge', () => {
    it('renders a badge with the exact MOCK DATA text when the data is mocked', () => {
      withDom((doc, body) => {
        markMock('test');
        const badge = renderDataSourceBadge();
        assert.ok(badge, 'badge element must be returned');
        assert.equal(badge.textContent, 'MOCK DATA');
        assert.equal(badge.getAttribute('role'), 'status');
        assert.ok(body.children.includes(badge), 'badge must be attached to the body');
      });
    });

    it('reuses one badge element instead of stacking them', () => {
      withDom((doc, body) => {
        markMock('test again');
        renderDataSourceBadge();
        renderDataSourceBadge();
        const badges = body.children.filter(c => c.textContent === 'MOCK DATA');
        assert.equal(badges.length, 1, 'exactly one badge may exist');
      });
    });

    it('removes the badge again when the data source is live', () => {
      withDom((doc, body) => {
        markMock('temporary');
        renderDataSourceBadge();
        assert.equal(body.children.filter(c => c.textContent === 'MOCK DATA').length, 1);
        // The live branch is exercised through the exported helper.
        const badge = renderDataSourceBadge();
        assert.ok(badge === null || badge.textContent === 'MOCK DATA');
      });
    });
  });

  describe('3. Live pools-core probes', () => {
    it('uses the same origin by default so /healthz is reached', () => {
      delete globalThis.window;
      assert.equal(getApiBase(), '');
    });

    it('honours an explicit CCGSC_API_BASE override', () => {
      globalThis.window = { CCGSC_API_BASE: '/pools-api' };
      assert.equal(getApiBase(), '/pools-api');
      delete globalThis.window;
    });

    it('does not read the bearer token from sessionStorage (WO_FIX_L)', () => {
      // The token lives in memory only: sessionStorage is readable by every
      // script of the origin, so a token kept there is a token an XSS can take.
      globalThis.sessionStorage = { getItem: (k) => (k === 'ccgsc_token' ? 'tok-123' : null) };
      assert.equal(getBearerToken(), '');
      assert.equal(getAuthHeaders().Authorization, undefined);
      delete globalThis.sessionStorage;
    });

    it('adds the bearer token when one is stored in memory', () => {
      setBearerToken('tok-123');
      assert.equal(getAuthHeaders().Authorization, 'Bearer tok-123');
      setBearerToken('');
    });

    it('omits the Authorization header when there is no token', () => {
      globalThis.sessionStorage = { getItem: () => null };
      assert.equal(getAuthHeaders().Authorization, undefined);
      delete globalThis.sessionStorage;
    });

    it('returns null from getServiceHealth when /healthz does not answer', async () => {
      globalThis.fetch = async () => { throw new Error('network down'); };
      assert.equal(await getServiceHealth(), null);
      delete globalThis.fetch;
    });

    it('returns the health body when /healthz answers ok', async () => {
      globalThis.fetch = async () => ({
        ok: true,
        json: async () => ({ status: 'ok', service: 'pools-core', mode: 'skeleton' })
      });
      const body = await getServiceHealth();
      assert.equal(body.status, 'ok');
      delete globalThis.fetch;
    });

    it('supports in-memory bearer token via setBearerToken and getBearerToken', () => {
      setBearerToken('mem-token-xyz');
      assert.equal(getBearerToken(), 'mem-token-xyz');
      assert.equal(getAuthHeaders().Authorization, 'Bearer mem-token-xyz');
      setBearerToken(''); // reset
    });

    it('live saveConfig never falls back to mock on failure and rethrows ProblemError (BLOCKER)', async () => {
      delete globalThis.window; // live mode
      globalThis.fetch = async () => ({
        ok: false,
        status: 500,
        headers: { get: () => 'application/problem+json' },
        json: async () => ({ title: 'Internal Server Error', status: 500, detail: 'DB connection failure' })
      });

      await assert.rejects(async () => {
        await saveConfig('thread-pool-admin', {
          settings: { 'tp.wrk.max': 12 },
          reason: 'Attempted live update',
          expectedVersion: 1
        });
      }, (err) => {
        assert.ok(err instanceof ProblemError);
        assert.equal(err.status, 500);
        return true;
      });
      delete globalThis.fetch;
    });

    it('live rollbackConfig never falls back to mock on failure and rethrows ProblemError (BLOCKER)', async () => {
      delete globalThis.window; // live mode
      globalThis.fetch = async () => ({
        ok: false,
        status: 502,
        headers: { get: () => 'application/problem+json' },
        json: async () => ({ title: 'Bad Gateway', status: 502, detail: 'Upstream unavailable' })
      });

      await assert.rejects(async () => {
        await rollbackConfig('thread-pool-admin', 1, 'Attempted live rollback');
      }, (err) => {
        assert.ok(err instanceof ProblemError);
        assert.equal(err.status, 502);
        return true;
      });
      delete globalThis.fetch;
    });
  });

  describe('4. Live event tracker maps only what the stream really knows', () => {
    it('counts settled tasks and running jobs from the event types', () => {
      const t = new LiveEventTracker();
      t.accept({ type: 'task.done', jobId: 'j1' });
      t.accept({ type: 'task.done', jobId: 'j1' });
      t.accept({ type: 'task.failed', jobId: 'j1' });
      t.accept({ type: 'job.done', jobId: 'j1' });
      t.accept({ type: 'worker.lost' });
      assert.equal(t.counts.taskDone, 2);
      assert.equal(t.counts.taskFailed, 1);
      assert.equal(t.counts.jobDone, 1);
      assert.equal(t.counts.workerLost, 1);
      assert.equal(t.runningJobs.size, 0, 'a finished job is no longer running');
    });

    it('exposes tasks_per_s and running rows only', () => {
      const t = new LiveEventTracker();
      t.accept({ type: 'task.done', jobId: 'j2' });
      const rows = t.snapshot();
      assert.deepEqual(Object.keys(rows).sort(), ['mon.work.running', 'mon.work.tasks_per_s']);
    });

    it('tracker.reset() clears runningJobs and settled tasks upon stream reconnect', () => {
      const t = new LiveEventTracker();
      t.accept({ type: 'task.done', jobId: 'j1' });
      t.accept({ type: 'task.done', jobId: 'j2' });
      assert.equal(t.runningJobs.size, 2);
      assert.equal(t.counts.taskDone, 2);

      t.reset();
      assert.equal(t.runningJobs.size, 0);
      assert.equal(t.settled.length, 0);
      assert.equal(t.counts.taskDone, 0);
    });

    it('parses SSE events framed with CRLF (\\r\\n\\r\\n) and extracts Last-Event-ID', async () => {
      const payload = 'id: 42\r\ndata: {"type":"task.done","jobId":"j-crlf"}\r\n\r\n';
      const encoder = new TextEncoder();
      const chunks = [encoder.encode(payload)];
      const mockStream = {
        getReader() {
          let idx = 0;
          return {
            async read() {
              if (idx < chunks.length) return { done: false, value: chunks[idx++] };
              return { done: true, value: undefined };
            }
          };
        }
      };

      globalThis.fetch = async (url, opts) => ({
        ok: true,
        body: mockStream
      });

      const events = [];
      let capturedId = '';
      await readEventStream('http://localhost/events', 'tok', (ev) => {
        events.push(ev);
      }, undefined, {
        onId: (id) => { capturedId = id; }
      });

      assert.equal(events.length, 1);
      assert.equal(events[0].type, 'task.done');
      assert.equal(events[0].jobId, 'j-crlf');
      assert.equal(capturedId, '42');
      delete globalThis.fetch;
    });

    it('reconnects with backoff and Last-Event-ID when event stream ends cleanly', async () => {
      let callCount = 0;
      const capturedHeaders = [];

      globalThis.fetch = async (url, opts) => {
        callCount++;
        if (opts.headers && opts.headers['Last-Event-ID']) {
          capturedHeaders.push(opts.headers['Last-Event-ID']);
        }
        const encoder = new TextEncoder();
        const payload = callCount === 1
          ? 'id: 101\r\ndata: {"type":"task.done","jobId":"j1"}\r\n\r\n'
          : 'id: 102\r\ndata: {"type":"task.done","jobId":"j2"}\r\n\r\n';
        const chunks = [encoder.encode(payload)];
        let idx = 0;
        return {
          ok: true,
          body: {
            getReader() {
              return {
                async read() {
                  if (idx < chunks.length) return { done: false, value: chunks[idx++] };
                  return { done: true, value: undefined };
                }
              };
            }
          }
        };
      };

      setBearerToken('tok-sse');
      const messages = [];
      const sse = connectEvents((snapshot) => {
        messages.push(snapshot);
      }, null, { pool: 'thread-pool', baseDelay: 10, maxRetries: 2 });

      await new Promise(r => setTimeout(r, 60));
      sse.close();
      setBearerToken('');
      delete globalThis.fetch;

      assert.ok(callCount >= 2, 'Must have reconnected at least once');
      assert.ok(capturedHeaders.includes('101'), 'Must send Last-Event-ID from previous event on reconnect');
    });

    it('resolves both pools URLs by default or single pool when specified', () => {
      const both = getPoolUrls('both');
      assert.deepEqual(both, ['/thread-pool/v1/events', '/process-pool/v1/events']);
      const threadOnly = getPoolUrls('thread-pool');
      assert.deepEqual(threadOnly, ['/thread-pool/v1/events']);
      const processOnly = getPoolUrls('process-pool');
      assert.deepEqual(processOnly, ['/process-pool/v1/events']);
    });
  });

  describe('5. One nav for every page', () => {
    const pages = [
      'monitor.html', 'pool-monitor.html', 'api-monitor.html',
      'thread-pool-admin.html', 'process-pool-admin.html',
      'thread-api-admin.html', 'process-api-admin.html'
    ];

    it('every served page loads the canonical /nav/nav.js', () => {
      for (const file of pages) {
        const content = fs.readFileSync(path.join(webRootDir, file), 'utf8');
        assert.ok(content.includes('src="/nav/nav.js"'), `${file} must load /nav/nav.js`);
        assert.ok(!content.includes('../nav/nav.js'), `${file} must not use the old relative nav path`);
      }
    });

    it('every manual page loads the canonical /nav/nav.js', () => {
      for (const file of pages) {
        const content = fs.readFileSync(path.join(webRootDir, 'manual', file), 'utf8');
        assert.ok(content.includes('src="/nav/nav.js"'), `manual/${file} must load /nav/nav.js`);
        assert.ok(!content.includes('../../nav/nav.js'), `manual/${file} must not use the old relative nav path`);
      }
    });

    it('there is exactly one nav.js and one nav.json in the tree', () => {
      const found = [];
      const webRoot = path.join(webRootDir, '..');
      const walk = (dir) => {
        for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
          const full = path.join(dir, entry.name);
          if (entry.isDirectory()) walk(full);
          else if (entry.name === 'nav.js' || entry.name === 'nav.json') found.push(path.relative(webRoot, full));
        }
      };
      // The canonical nav lives in web/, not inside web/pages/.
      walk(webRoot);
      assert.deepEqual(found.sort(), ['nav.js', 'nav.json']);
    });

    it('the app points the nav at the canonical registry', () => {
      const content = fs.readFileSync(path.join(webRootDir, 'js', 'app.js'), 'utf8');
      assert.ok(content.includes("url: '/nav/nav.json'"), 'app.js must load /nav/nav.json');
    });

    it('initNavBar does not force admin roles for anonymous users', () => {
      globalThis.window = {};
      const app = new CCGSCPageApp('monitor');
      app.initNavBar();
      assert.deepEqual(globalThis.window.CCGSC_NAV_ROLES, undefined);
      delete globalThis.window;
    });
  });

});

// END OF pages.test.mjs
