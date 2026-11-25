import { test } from 'node:test';
import assert from 'node:assert/strict';
import { getAdminMetrics, rotateTaskKey, metricRows, mountAdminControls, saveAdminConfig, METRIC_PANEL_NAMES } from '../js/admin_controls_MVPW.js';
import { setCSRFToken } from '../js/api.js';
import { CCGSCPageApp } from '../js/app.js';

function response(data, status = 200) {
  return {
    ok: status < 400, status, statusText: 'Error',
    headers: { get: () => 'application/json' },
    json: async () => data
  };
}

test('rotation uses session credentials and CSRF with overlap minutes', async () => {
  let seen;
  globalThis.fetch = async (url, options) => {
    seen = { url, options };
    return response({ status: 'ok' });
  };
  setCSRFToken('csrf-test');
  try {
    await rotateTaskKey(15);
    assert.equal(seen.url, '/admin/v1/task-keys:rotate');
    assert.equal(seen.options.credentials, 'same-origin');
    assert.equal(seen.options.headers['X-CSRF-Token'], 'csrf-test');
    assert.deepEqual(JSON.parse(seen.options.body), { overlapMinutes: 15 });
  } finally {
    setCSRFToken('');
    delete globalThis.fetch;
  }
});

test('config save sends version and propagates conflicts', async () => {
  let seen;
  setCSRFToken('config-csrf');
  globalThis.fetch = async (url, options) => {
    seen = { url, options };
    return response({ status: 409, title: 'Conflict', detail: 'Config version conflict.' }, 409);
  };
  try {
    await assert.rejects(saveAdminConfig('thread-pool-admin', { 'tp.test': 1 }, 'change', '', 4), /Config version conflict/);
    assert.equal(seen.url, '/admin/v1/config/thread-pool-admin');
    assert.equal(JSON.parse(seen.options.body).expectedVersion, 4);
    assert.equal(seen.options.headers['X-CSRF-Token'], 'config-csrf');
  } finally {
    setCSRFToken('');
    delete globalThis.fetch;
  }
});

test('role gating disables rotation and avoids protected metric fetch', () => {
  const elements = new Map();
  for (const id of ['task-key-overlap', 'task-key-rotate', 'admin-metrics-status', 'admin-metrics-list']) {
    elements.set(id, { disabled: false, textContent: '', addEventListener() {} });
  }
  const content = {
    insertAdjacentHTML() {},
    querySelector(selector) { return elements.get(selector.slice(1)); }
  };
  let fetches = 0;
  globalThis.fetch = async () => { fetches++; throw new Error('unexpected'); };
  try {
    mountAdminControls(content, false, () => {}, () => {});
    assert.equal(elements.get('task-key-overlap').disabled, true);
    assert.equal(elements.get('task-key-rotate').disabled, true);
    assert.match(elements.get('admin-metrics-status').textContent, /Admin role required/);
    assert.equal(fetches, 0);
  } finally {
    delete globalThis.fetch;
  }
});

test('unauthorized session disables config controls without fetching a fake config', async () => {
  const configInput = { disabled: false };
  const elements = new Map();
  for (const id of ['task-key-overlap', 'task-key-rotate', 'admin-metrics-status', 'admin-metrics-list']) {
    elements.set(id, { disabled: false, textContent: '', addEventListener() {} });
  }
  const content = {
    insertAdjacentHTML() {},
    querySelector(selector) { return elements.get(selector.slice(1)); },
    querySelectorAll() { return [configInput]; }
  };
  globalThis.document = { getElementById: id => id === 'main-content' ? content : null };
  const paths = [];
  globalThis.fetch = async url => {
    paths.push(url);
    return response({ authenticated: true, user: { roles: ['developer'] } });
  };
  try {
    const app = new CCGSCPageApp('thread-pool-admin');
    app.renderAdminForms = () => {};
    app.renderConfigVersionTable = () => {};
    await app.initAdminPage();
    assert.equal(configInput.disabled, true);
    assert.deepEqual(paths, ['/pools/auth/session']);
  } finally {
    delete globalThis.document;
    delete globalThis.fetch;
  }
});

test('metric samples preserve real zero and missing names are not measured', async () => {
  globalThis.fetch = async () => response({ samples: [{ name: 'lease_issued', value: 0, unit: 'count' }] });
  try {
    const samples = await getAdminMetrics();
    const rows = metricRows(samples, ['lease_issued', 'lease_expired']);
    assert.equal(rows[0].value, 0);
    assert.equal(rows[1].value, 'not measured');
  } finally {
    delete globalThis.fetch;
  }
});

test('admin metrics panel names include the thesis GPU metric names', () => {
  for (const name of ['gpu_adapter_detected', 'gpu_compile_ms', 'gpu_dispatch_ms', 'gpu_vs_cpu']) {
    assert.ok(METRIC_PANEL_NAMES.includes(name), `panel names must contain ${name}`);
  }
  // The panel still lists the Slice B names it had before the GPU rows.
  for (const name of ['lease_issued', 'lease_settled', 'task_latency_ms', 'canary_pass']) {
    assert.ok(METRIC_PANEL_NAMES.includes(name), `panel names must keep ${name}`);
  }
  // Timing rows are shown in ms; counts carry no fabricated zeros.
  const gpuNames = METRIC_PANEL_NAMES.filter(name => name.startsWith('gpu_'));
  assert.equal(gpuNames.length, 4, `exactly four gpu_* panel names expected, got ${gpuNames.join(', ')}`);
});
