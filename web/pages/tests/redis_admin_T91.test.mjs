// Path: web/pages/tests/redis_admin_T91.test.mjs | Author: Codex | 2026-11-20
// T91: both Director views, typed saves, readonly status, and monitor observation.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { PAGE_SCHEMAS, validateRowValue } from '../js/schema.js';
import { CCGSCPageApp, coerceRowValue } from '../js/app.js';
import { REDIS_PAGES, redisStatusText } from '../js/redis_admin_T91.js';
import { setCSRFToken } from '../js/api.js';

const desired = {
  'redis.memory.mode': 'fixed', 'redis.memory.fixed_gb': 1,
  'redis.memory.dynamic_percent': 10, 'redis.memory.interval_min': 5,
  'redis.persistence.mode': 'aof_everysec'
};
const status = {
  enabled: true, state: 'applied', message: 'Redis settings applied', desired,
  current: { maxMemoryBytes: 2 ** 30, persistence: 'aof_everysec', policy: 'noeviction' }
};
function element(extra = {}) {
  return Object.assign({
    value: '', textContent: '', innerHTML: '', disabled: false, listeners: {},
    classList: { add() {}, remove() {}, contains() { return false; } },
    setAttribute() {}, removeAttribute() {}, appendChild() {},
    addEventListener(kind, fn) { this.listeners[kind] = fn; },
    async dispatch(kind, target = this) { await this.listeners[kind]?.({ target }); },
    insertAdjacentHTML(_, html) { this.innerHTML += html; },
    querySelector() { return null; }, querySelectorAll() { return []; }
  }, extra);
}
function dom() {
  const elements = Object.fromEntries(['main-content', 'redis-current-status', 'btn-save-config', 'save-reason', 'pin-name', 'task-key-overlap', 'task-key-rotate', 'admin-metrics-status', 'admin-metrics-list'].map(id => [id, element()]));
  elements['main-content'].querySelector = selector => elements[selector.slice(1)] || null;
  elements['save-reason'].value = 'T91 Redis policy';
  for (const id of Object.keys(desired)) {
    elements[`field-${id.replaceAll('.', '-')}`] = element();
    elements[`err-${id.replaceAll('.', '-')}`] = element();
  }
  return { elements, document: {
    getElementById(id) { return elements[id] || null; },
    querySelectorAll(selector) { return selector === '[id^="err-"]' ? Object.entries(elements).filter(([id]) => id.startsWith('err-')).map(([, el]) => el) : []; },
    createElement() { return element(); }, body: element()
  }};
}
function response(body, statusCode = 200) {
  return { ok: statusCode < 400, status: statusCode, headers: { get: () => 'application/json' }, json: async () => body };
}

test('T91 exact rows, defaults, choices, and numeric coercion on both pages', () => {
  for (const page of REDIS_PAGES) {
    const rows = PAGE_SCHEMAS[page].rows.filter(r => r.id.startsWith('redis.'));
    assert.equal(rows.length, 5);
    for (const row of rows) {
      assert.equal(row.default, desired[row.id]);
      assert.equal(row.section, 'redis');
      assert.equal(row.whoMayChange, 'admin');
      assert.equal(validateRowValue(row, row.default).valid, true);
      if (row.jsonType === 'integer') {
        assert.equal(coerceRowValue(row, '2'), 2);
        assert.equal(validateRowValue(row, coerceRowValue(row, '2.5')).valid, false);
        assert.equal(validateRowValue(row, 0).valid, false);
        assert.equal(validateRowValue(row, 100).valid, false);
      } else {
        assert.equal(validateRowValue(row, 'invalid').valid, false);
      }
    }
  }
});

for (const page of REDIS_PAGES) {
  test(`T91 ${page}: load shared policy and save typed JSON through real listeners`, async () => {
    const { document, elements } = dom();
    globalThis.document = document;
    let post;
    globalThis.fetch = async (url, options = {}) => {
      const path = String(url);
      if (path.endsWith('/pools/auth/session')) return response({ authenticated: true, user: { roles: ['director'] } });
      if (path.endsWith('/admin/v1/redis/status')) return response(status);
      if (path.endsWith(`/admin/v1/config/${page}`)) {
        if (options.method === 'POST') { post = JSON.parse(options.body); return response({ version: 8, redis: status }); }
        // Simulate a stale page-specific value: global desired must win in the form.
        return response({ version: 7, settings: { 'redis.memory.fixed_gb': 4 }, versions: [] });
      }
      if (path.endsWith('/admin/v1/metrics')) return response({ samples: [] });
      throw new Error(`unexpected fetch: ${path}`);
    };
    setCSRFToken('test-csrf');
    try {
      const app = new CCGSCPageApp(page);
      app.showSuccess = () => {};
      await app.initAdminPage();
      assert.equal(app.currentSettings['redis.memory.fixed_gb'], 1);
      assert.match(elements['main-content'].innerHTML, /id="field-redis-memory-mode"/);
      assert.match(elements['main-content'].innerHTML, /Current Redis cap: 1073741824 bytes/);
      for (const [id, value] of Object.entries({
        'redis.memory.mode': 'dynamic', 'redis.memory.fixed_gb': '3',
        'redis.memory.dynamic_percent': '40', 'redis.memory.interval_min': '8',
        'redis.persistence.mode': 'snapshot'
      })) await elements[`field-${id.replaceAll('.', '-')}`].dispatch('change', { type: 'text', value });
      await elements['btn-save-config'].dispatch('click');
      assert.equal(post.expectedVersion, 7);
      assert.deepEqual(post.settings, {
        'redis.memory.mode': 'dynamic', 'redis.memory.fixed_gb': 3,
        'redis.memory.dynamic_percent': 40, 'redis.memory.interval_min': 8,
        'redis.persistence.mode': 'snapshot'
      });
    } finally { delete globalThis.document; delete globalThis.fetch; setCSRFToken(''); }
  });
}

test('T91 disabled admin controls are readonly and display observed values, not saved defaults', async () => {
  const { document, elements } = dom();
  globalThis.document = document;
  globalThis.fetch = async url => {
    const path = String(url);
    if (path.endsWith('/pools/auth/session')) return response({ authenticated: true, user: { roles: ['admin'] } });
    if (path.endsWith('/admin/v1/redis/status')) return response({ ...status, enabled: false, state: 'disabled' });
    if (path.includes('/admin/v1/config/')) return response({ version: 1, settings: { 'redis.memory.fixed_gb': 4 } });
    return response({ samples: [] });
  };
  try {
    const app = new CCGSCPageApp('thread-pool-admin');
    await app.initAdminPage();
    const html = elements['main-content'].innerHTML;
    assert.match(html, /applied by ops, not by pools-core/);
    assert.match(html, /1073741824 bytes/);
    assert.doesNotMatch(html, /id="field-redis-/);
    assert.equal(elements['field-redis-memory-fixed_gb'].listeners.change, undefined);
  } finally { delete globalThis.document; delete globalThis.fetch; }
});

test('T91 monitor line is readonly and missing observations remain unavailable', async () => {
  const { document, elements } = dom();
  globalThis.document = document;
  globalThis.fetch = async url => String(url).endsWith('/admin/v1/redis/status') ? response(status) : response({ workers: [] });
  try {
    const app = new CCGSCPageApp('monitor');
    app.loadInitialMetrics = async () => {};
    app.connectLiveMetrics = () => {};
    await app.initMonitorPage();
    assert.match(elements['redis-current-status'].textContent, /aof_everysec/);
    assert.doesNotMatch(elements['main-content'].innerHTML, /id="field-redis-/);
    assert.match(redisStatusText(null), /unavailable/);
    assert.match(redisStatusText({ enabled: false }), /unavailable/);
    assert.doesNotMatch(redisStatusText({ enabled: false }), /1073741824/);
  } finally { delete globalThis.document; delete globalThis.fetch; }
});

// Owner MCQ 2026-11-20: read-only mode explains how to apply on the VPS.
import { redisApplyNote } from '../js/redis_admin_T91.js';
test('T91 read-only apply note gives VPS commands, enabled note does not', () => {
  const ro = redisApplyNote({ enabled: false });
  assert.match(ro, /CONFIG SET maxmemory/);
  assert.match(ro, /appendonly yes/);
  assert.match(ro, /\/etc\/redis\/redis\.conf/);
  assert.doesNotMatch(ro, /applies server-wide immediately/);
  assert.match(redisApplyNote({ enabled: true }), /applies server-wide immediately/);
});
