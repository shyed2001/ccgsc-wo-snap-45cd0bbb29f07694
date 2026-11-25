// Path: web/pages/tests/gpu_admin_T83.test.mjs | Author: GLM for Claude Opus 5.5 | 2026-11-19
// What/why: round-8 acceptance for the T83 Pool Admin GPU controls (gpuEnabled,
// maxGpuSlotsPerTab 1..4 default 2, gpuOnBatteryAllowed default false) and the
// read-only monitor devices panel. Every test runs against a fake fetch and a
// minimal DOM double: load, typed save, version conflict, role-disabled, and the
// worker GPU report panel (absent reports stay absent, never zero).

import { test } from 'node:test';
import assert from 'node:assert/strict';

import { CCGSCPageApp, coerceRowValue } from '../js/app.js';
import { getWorkerReports, formatReportedGpus, renderWorkerGpuRows, mountTabGpuPanel, CONTRIBUTOR_CPU_LIMITS_TEXT } from '../js/admin_controls_MVPW.js';
import { setCSRFToken } from '../js/api.js';
import { PAGE_SCHEMAS } from '../js/schema.js';

const GPU_IDS = ['tp.gpu.enabled', 'tp.gpu.max_slots_per_tab', 'tp.gpu.on_battery_allowed'];

// --- helpers -------------------------------------------------------------------

function response(data, status = 200) {
  return {
    ok: status < 400,
    status,
    statusText: status < 400 ? 'OK' : 'Conflict',
    headers: { get: () => 'application/json' },
    json: async () => data
  };
}

function makeElement(extra = {}) {
  const el = {
    disabled: false,
    textContent: '',
    value: '',
    innerHTML: '',
    children: [],
    classList: {
      add() { el.classListCalls.push('add'); },
      remove() { el.classListCalls.push('remove'); },
      contains: () => false
    },
    classListCalls: [],
    attributes: {},
    setAttribute(k, v) { el.attributes[k] = String(v); },
    removeAttribute(k) { delete el.attributes[k]; },
    getAttribute(k) { return el.attributes[k]; },
    addEventListener(type, fn) { (el.listeners[type] = el.listeners[type] || []).push(fn); },
    listeners: {},
    async dispatch(type, event) {
      for (const fn of el.listeners[type] || []) await fn(event);
    },
    appendChild(child) { el.children.push(child); child.parentNode = el; return child; },
    querySelectorAll() { return []; }
  };
  return Object.assign(el, extra);
}

// adminDom doubles the small slice of DOM that initAdminPage touches. Only the
// GPU field ids, the save bar and the mountAdminControls ids answer; every other
// schema row resolves to null and is skipped by the real controller code.
function adminDom(contentExtras = {}) {
  const elements = {};
  for (const id of GPU_IDS) {
    elements[`field-${id.replace(/\./g, '-')}`] = makeElement();
    elements[`err-${id.replace(/\./g, '-')}`] = makeElement();
  }
  elements['btn-save-config'] = makeElement();
  elements['save-reason'] = makeElement({ value: 'T83 GPU limits tuned for thesis run' });
  elements['pin-name'] = makeElement({ value: '' });
  for (const id of ['task-key-overlap', 'task-key-rotate', 'admin-metrics-status', 'admin-metrics-list']) {
    elements[id] = makeElement();
  }
  const content = Object.assign({
    insertAdjacentHTML() {},
    querySelector(selector) { return elements[selector.slice(1)] || null; },
    querySelectorAll() {
      return GPU_IDS.map(id => elements[`field-${id.replace(/\./g, '-')}`]);
    }
  }, contentExtras);
  const document = {
    getElementById: id => (id === 'main-content' ? content : (elements[id] || null)),
    createElement: () => makeElement(),
    querySelectorAll: () => [],
    body: makeElement()
  };
  return { elements, content, document };
}

const ADMIN_SESSION = { authenticated: true, user: { username: 'admin', roles: ['admin'] } };
const CONFIG_BODY = page => ({
  page,
  version: 7,
  settings: {
    'tp.wrk.per_tab': 'auto',
    'tp.gpu.enabled': true,
    'tp.gpu.max_slots_per_tab': 2,
    'tp.gpu.on_battery_allowed': false
  },
  versions: [{ version: 7, reason: 'baseline', author: 'admin', pinnedName: null, inUseByJobs: 0, createdAt: '2026-11-19T00:00:00Z' }]
});

// --- 1. the schema rows exist on both admin pages ------------------------------

test('T83 GPU control rows exist with the decided defaults and ranges', () => {
  for (const [page, prefix] of [['thread-pool-admin', 'tp'], ['process-pool-admin', 'pp']]) {
    const rows = PAGE_SCHEMAS[page].rows;
    const enabled = rows.find(r => r.id === `${prefix}.gpu.enabled`);
    const slots = rows.find(r => r.id === `${prefix}.gpu.max_slots_per_tab`);
    const battery = rows.find(r => r.id === `${prefix}.gpu.on_battery_allowed`);
    assert.ok(enabled && slots && battery, `${page} must carry the three T83 GPU rows`);
    assert.equal(enabled.jsonType, 'boolean');
    assert.equal(slots.type, 'number');
    assert.equal(slots.range, '1-4');
    assert.equal(slots.default, 2);
    assert.equal(slots.jsonType, 'integer');
    assert.equal(battery.type, 'toggle');
    assert.equal(battery.default, 'off');
    assert.equal(battery.jsonType, 'boolean');
    assert.equal(slots.section, 'gpu');
    assert.equal(battery.whoMayChange, 'admin');
  }
});

test('coerceRowValue types the GPU controls and leaves other rows untouched', () => {
  const slots = PAGE_SCHEMAS['thread-pool-admin'].rows.find(r => r.id === 'tp.gpu.max_slots_per_tab');
  const enabled = PAGE_SCHEMAS['thread-pool-admin'].rows.find(r => r.id === 'tp.gpu.enabled');
  const plain = PAGE_SCHEMAS['thread-pool-admin'].rows.find(r => r.id === 'tp.ver.hash_check');
  assert.equal(coerceRowValue(slots, '3'), 3);
  assert.equal(coerceRowValue(slots, 4), 4);
  assert.equal(coerceRowValue(enabled, 'on'), true);
  assert.equal(coerceRowValue(enabled, 'off'), false);
  assert.equal(coerceRowValue(enabled, false), false);
  // Untyped rows keep the historical 'on'/'off' wire format.
  assert.equal(coerceRowValue(plain, 'on'), 'on');
});

// --- 2. load -------------------------------------------------------------------

test('load: admin session loads the GPU settings into the form state', async () => {
  const { elements, content, document } = adminDom();
  globalThis.document = document;
  const paths = [];
  globalThis.fetch = async url => {
    paths.push(String(url));
    if (String(url).endsWith('/pools/auth/session')) return response(ADMIN_SESSION);
    if (String(url).endsWith('/admin/v1/config/thread-pool-admin')) return response(CONFIG_BODY('thread-pool-admin'));
    if (String(url).endsWith('/admin/v1/metrics')) return response({ samples: [] });
    throw new Error(`unexpected fetch ${url}`);
  };
  try {
    const app = new CCGSCPageApp('thread-pool-admin');
    app.renderAdminForms = () => {};
    app.renderConfigVersionTable = () => {};
    await app.initAdminPage();
    assert.ok(paths.some(p => p.endsWith('/admin/v1/config/thread-pool-admin')), 'config GET must be issued');
    for (const id of GPU_IDS) {
      assert.ok(id in app.currentSettings, `${id} must load into the form state`);
    }
    assert.equal(app.currentSettings['tp.gpu.max_slots_per_tab'], 2);
    assert.equal(app.currentSettings['tp.gpu.on_battery_allowed'], false);
    assert.equal(app.currentSettings['tp.gpu.enabled'], true);
    // Role gating like every other control: an admin session keeps controls live.
    for (const id of GPU_IDS) {
      assert.equal(elements[`field-${id.replace(/\./g, '-')}`].disabled, false);
    }
  } finally {
    delete globalThis.document;
    delete globalThis.fetch;
  }
});

// --- 3. save -------------------------------------------------------------------

test('save: GPU controls reach the wire as JSON booleans and integers with expectedVersion', async () => {
  const { elements, content, document } = adminDom();
  globalThis.document = document;
  let post;
  globalThis.fetch = async (url, options = {}) => {
    const path = String(url);
    if (path.endsWith('/pools/auth/session')) return response(ADMIN_SESSION);
    if (path.endsWith('/admin/v1/config/thread-pool-admin') && (options.method || 'GET') === 'GET') {
      return response(CONFIG_BODY('thread-pool-admin'));
    }
    if (path.endsWith('/admin/v1/config/thread-pool-admin') && options.method === 'POST') {
      post = { url: path, options };
      return response({ page: 'thread-pool-admin', version: 8 });
    }
    if (path.endsWith('/admin/v1/metrics')) return response({ samples: [] });
    throw new Error(`unexpected fetch ${url}`);
  };
  setCSRFToken('t83-csrf');
  try {
    const app = new CCGSCPageApp('thread-pool-admin');
    app.renderAdminForms = () => {};
    app.renderConfigVersionTable = () => {};
    app.errorBanner = makeElement();
    app.successBanner = makeElement();
    await app.initAdminPage();
    // The real form listeners: attachFormListeners is normally called by
    // renderAdminForms, which this test replaces with the element doubles above.
    app.attachFormListeners();

    // The admin flips both GPU switches and raises the slot cap through the
    // real change listeners that attachFormListeners installed.
    await elements['field-tp-gpu-enabled'].dispatch('change', { target: { type: 'checkbox', checked: false } });
    await elements['field-tp-gpu-max_slots_per_tab'].dispatch('change', { target: { type: 'number', value: '3' } });
    await elements['field-tp-gpu-on_battery_allowed'].dispatch('change', { target: { type: 'checkbox', checked: true } });
    assert.equal(app.currentSettings['tp.gpu.enabled'], false);
    assert.equal(app.currentSettings['tp.gpu.max_slots_per_tab'], 3);
    assert.equal(app.currentSettings['tp.gpu.on_battery_allowed'], true);

    await elements['btn-save-config'].dispatch('click', {});
    assert.ok(post, 'the save must POST the page config');
    const body = JSON.parse(post.options.body);
    assert.equal(body.expectedVersion, 7, 'the save carries the loaded version for conflict detection');
    assert.equal(body.reason, 'T83 GPU limits tuned for thesis run');
    assert.equal(body.settings['tp.gpu.enabled'], false);
    assert.equal(body.settings['tp.gpu.max_slots_per_tab'], 3);
    assert.equal(body.settings['tp.gpu.on_battery_allowed'], true);
    assert.equal(post.options.headers['X-CSRF-Token'], 't83-csrf');
    assert.equal(post.options.credentials, 'same-origin');
  } finally {
    setCSRFToken('');
    delete globalThis.document;
    delete globalThis.fetch;
  }
});

// --- 4. conflict ---------------------------------------------------------------

test('conflict: a stale expectedVersion surfaces the 409 in the error banner', async () => {
  const { elements, document } = adminDom();
  globalThis.document = document;
  globalThis.fetch = async (url, options = {}) => {
    const path = String(url);
    if (path.endsWith('/pools/auth/session')) return response(ADMIN_SESSION);
    if (path.endsWith('/admin/v1/config/thread-pool-admin') && (options.method || 'GET') === 'GET') {
      return response(CONFIG_BODY('thread-pool-admin'));
    }
    if (path.endsWith('/admin/v1/config/thread-pool-admin') && options.method === 'POST') {
      return response({ status: 409, title: 'Conflict', detail: 'Config version conflict.' }, 409);
    }
    if (path.endsWith('/admin/v1/metrics')) return response({ samples: [] });
    throw new Error(`unexpected fetch ${url}`);
  };
  try {
    const app = new CCGSCPageApp('thread-pool-admin');
    app.renderAdminForms = () => {};
    app.renderConfigVersionTable = () => {};
    app.errorBanner = makeElement();
    app.successBanner = makeElement();
    await app.initAdminPage();
    app.attachFormListeners();
    await elements['field-tp-gpu-max_slots_per_tab'].dispatch('change', { target: { type: 'number', value: '4' } });
    await elements['btn-save-config'].dispatch('click', {});
    const heading = app.errorBanner.children.map(c => c.textContent).join(' ');
    assert.match(heading, /409/, 'the RFC 9457 banner must report the HTTP 409');
    const details = app.errorBanner.children.map(c => c.textContent).join(' ');
    assert.match(details, /Config version conflict/);
    // The save button is re-enabled, so the admin can reload and retry.
    assert.equal(elements['btn-save-config'].disabled, false);
    // The success banner stayed silent.
    assert.deepEqual(app.successBanner.classListCalls.filter(c => c === 'add'), []);
  } finally {
    delete globalThis.document;
    delete globalThis.fetch;
  }
});

// --- 5. role-disabled ----------------------------------------------------------

test('role-disabled: a non-admin session disables every GPU control and fetches no config', async () => {
  const { elements, content, document } = adminDom();
  globalThis.document = document;
  const paths = [];
  globalThis.fetch = async url => {
    paths.push(String(url));
    return response({ authenticated: true, user: { roles: ['developer'] } });
  };
  try {
    const app = new CCGSCPageApp('thread-pool-admin');
    app.renderAdminForms = () => {};
    app.renderConfigVersionTable = () => {};
    app.errorBanner = makeElement();
    await app.initAdminPage();
    assert.deepEqual(paths, ['/pools/auth/session'], 'no config fetch without an admin or director role');
    for (const id of GPU_IDS) {
      assert.equal(elements[`field-${id.replace(/\./g, '-')}`].disabled, true, `${id} must be disabled`);
    }
    assert.match(app.errorBanner.children.map(c => c.textContent).join(' '), /admin or director/);
    assert.match(content.querySelector('#admin-metrics-status').textContent, /Admin role required/);
  } finally {
    delete globalThis.document;
    delete globalThis.fetch;
  }
});

// --- 6. the read-only monitor devices panel ------------------------------------

test('monitor panel: GPU reports render from the worker records, CPU limits are decided text', async () => {
  const inserted = [];
  const tbody = makeElement();
  const status = makeElement();
  const content = {
    insertAdjacentHTML(_pos, html) { inserted.push(html); },
    querySelector(selector) {
      if (selector === '#tab-gpu-tbody') return tbody;
      if (selector === '#tab-gpu-status') return status;
      return null;
    }
  };
  globalThis.document = { createElement: () => makeElement() };
  globalThis.fetch = async url => {
    assert.equal(String(url), '/admin/v1/workers');
    return response({
      workers: [
        { tabId: 'tab-laptop-01', poolId: 'thread-pool', slots: 4, gpuSlots: 2, gpus: [{ vendor: 'amd', architecture: 'rdna3' }] },
        { tabId: 'tab-desktop-02', poolId: 'process-pool', slots: 4 }
      ],
      count: 2
    });
  };
  try {
    await mountTabGpuPanel(content);
    assert.equal(inserted.length, 1);
    assert.match(inserted[0], /id="devices-gpu"/);
    assert.match(inserted[0], /min 1, max 4/);
    assert.equal(status.textContent, 'Read-only: from the server worker record gpuSlots + gpus list (T83).');
    assert.equal(tbody.children.length, 2);
    const gpuRow = tbody.children[0].children.map(c => c.textContent);
    assert.deepEqual(gpuRow, ['tab-laptop-01', 'thread-pool', '2', 'amd / rdna3']);
    const cpuRow = tbody.children[1].children.map(c => c.textContent);
    // The desktop tab never reported GPUs: absent, never zero.
    assert.deepEqual(cpuRow, ['tab-desktop-02', 'process-pool', '\u2014', '\u2014']);
  } finally {
    delete globalThis.document;
    delete globalThis.fetch;
  }
});

test('monitor panel: an unavailable worker listing reads "not measured" and renders no rows', async () => {
  const tbody = makeElement();
  const status = makeElement();
  const content = {
    insertAdjacentHTML() {},
    querySelector(selector) {
      if (selector === '#tab-gpu-tbody') return tbody;
      if (selector === '#tab-gpu-status') return status;
      return null;
    }
  };
  globalThis.document = { createElement: () => makeElement() };
  globalThis.fetch = async () => response({ status: 401, title: 'Unauthorized', detail: 'unauthorized' }, 401);
  try {
    await mountTabGpuPanel(content);
    assert.match(status.textContent, /^Not measured: worker GPU reports unavailable/);
    assert.equal(tbody.children.length, 0, 'no fabricated rows and no zeros');
  } finally {
    delete globalThis.document;
    delete globalThis.fetch;
  }
});

test('monitor panel: mock mode serves the mock worker records', async () => {
  globalThis.window = { location: { search: '?mock=1' } };
  globalThis.document = { createElement: () => makeElement() };
  let fetches = 0;
  globalThis.fetch = async () => { fetches++; throw new Error('unexpected network in mock mode'); };
  try {
    const workers = await getWorkerReports();
    assert.equal(fetches, 0);
    assert.equal(workers.length, 2);
    assert.equal(workers[0].gpuSlots, 2);
    assert.equal(formatReportedGpus(workers[0]), 'amd / rdna3');
    assert.equal('gpuSlots' in workers[1], false, 'the mock CPU tab carries no GPU fields at all');
    assert.equal(formatReportedGpus(workers[1]), '\u2014');
    assert.match(CONTRIBUTOR_CPU_LIMITS_TEXT, /auto = logical cores - 1, min 1, max 4/);
  } finally {
    delete globalThis.window;
    delete globalThis.document;
    delete globalThis.fetch;
  }
});

test('renderWorkerGpuRows ignores malformed records and escapes nothing into HTML', () => {
  const tbody = makeElement();
  globalThis.document = { createElement: () => makeElement() };
  const count = renderWorkerGpuRows(tbody, [
    null,
    'not-an-object',
    { tabId: '<img src=x onerror=alert(1)>', poolId: 'thread-pool', gpuSlots: 1, gpus: [{ vendor: '<script>', architecture: 'gpu' }] }
  ]);
  assert.equal(count, 1, 'malformed records are skipped, not rendered');
  const cells = tbody.children[0].children.map(c => c.textContent);
  assert.equal(cells[0], '<img src=x onerror=alert(1)>', 'server data lands as text, never markup');
  assert.equal(cells[3], '<script> / gpu');
});
