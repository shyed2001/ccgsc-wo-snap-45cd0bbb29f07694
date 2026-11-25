// W10c row 1: isolation_admin_MVPW.test.mjs - the Director's isolation select.
// What/why: D-W10-6 makes the isolation mode selectable on the main Admin/Director
// panel. The work order also pins the schema field; the runner reads the value
// the page sends. This suite proves the panel, the row and the wiring agree, and
// that an unknown value never weakens the default.
//
// Truth: WO_W10C_CLIENT.md row 1, SPLIT_DESIGN_W10.md section 8 (D-W10-6).
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  mountAdminControls,
  renderIsolationModePanel,
  isolationModeLabel,
  ISOLATION_SELECT_ID,
  normalizeIsolationChoice
} from '../js/admin_controls_MVPW.js';
import {
  PAGE_SCHEMAS,
  ISOLATION_FIELD_BY_PAGE,
  isolationFieldFor,
  readIsolationMode
} from '../js/schema.js';
import { ISOLATION_MODES, DEFAULT_ISOLATION_MODE } from '../js/runner.js';

const here = path.dirname(fileURLToPath(import.meta.url));
const pagesDir = path.join(here, '..');

// A content stub: records the markup, resolves the select by id.
function contentStub() {
  const markup = [];
  const elements = new Map();
  return {
    markup,
    elements,
    insertAdjacentHTML(_position, html) { markup.push(html); },
    querySelector(selector) {
      if (selector === `#${ISOLATION_SELECT_ID}`) {
        if (!elements.has('select')) {
          const listeners = [];
          elements.set('select', {
            id: ISOLATION_SELECT_ID,
            value: DEFAULT_ISOLATION_MODE,
            disabled: false,
            listeners,
            addEventListener(type, fn) { if (type === 'change') listeners.push(fn); },
            // R11-M6: the change handler saves before it acknowledges, so fire()
            // waits for the write the listener started.
            async fire() { for (const fn of listeners) await fn(); }
          });
        }
        return elements.get('select');
      }
      const id = selector.startsWith('#') ? selector.slice(1) : selector;
      if (!elements.has(id)) {
        const listeners = [];
        elements.set(id, { id, value: '', textContent: '', disabled: false, addEventListener(t, f) { listeners.push(f); } });
      }
      return elements.get(id);
    },
    querySelectorAll() { return []; }
  };
}

test('D-W10-6: the Director panel renders a select with exactly the two documented modes', () => {
  const content = contentStub();
  const result = renderIsolationModePanel(content, true, {}, 'thread-pool-admin');
  assert.equal(result.mode, 'separate+lock', 'a missing setting displays the mode it will run with');
  const html = content.markup.join('\n');
  assert.ok(html.includes(`id="${ISOLATION_SELECT_ID}"`), 'the select is rendered');
  assert.ok(html.includes('D-W10-6'), 'the panel names its decision');
  for (const mode of ISOLATION_MODES) {
    assert.ok(html.includes(`value="${mode}"`), `the select offers ${mode}`);
  }
  assert.ok(html.includes(`value="separate+lock" selected`), 'the default is preselected');
  assert.equal(content.elements.get('select').disabled, false, 'an admin may change it');
});

test('D-W10-6: a non-admin sees the same mode, but the select is disabled', () => {
  const content = contentStub();
  const result = renderIsolationModePanel(content, false, { 'tp.ver.isolation': 'lock-only' }, 'thread-pool-admin');
  assert.equal(result.mode, 'lock-only', 'the stored value is shown to everyone');
  assert.equal(content.elements.get('select').disabled, true, 'only an admin may change it');
  const html = content.markup.join('\n');
  assert.ok(html.includes('value="lock-only" selected'), 'the stored value is the selected one');
});

test('D-W10-6: an unknown stored value falls back to the strict default, never to lock-only', () => {
  for (const junk of ['', 'nope', 'LOCK-ONLY', null, undefined, 3]) {
    const settings = { 'tp.ver.isolation': junk };
    assert.equal(readIsolationMode(settings, 'thread-pool-admin'), 'separate+lock', `${String(junk)} answers the default`);
    assert.equal(normalizeIsolationChoice(junk), 'separate+lock', `${String(junk)} normalizes to the default`);
  }
  assert.equal(readIsolationMode({}, 'process-pool-admin'), 'separate+lock');
  assert.equal(normalizeIsolationChoice('lock-only'), 'lock-only');
});

// R11-M6: the select saves through the pool settings endpoint (the one home of
// the field) and acknowledges only after that write is accepted. The mount-time
// settings map is never mutated: the next reload would throw the change away.
test('D-W10-6: the panel saves the isolation mode through the pool settings endpoint', async () => {
  const settings = {};
  const successes = [];
  const content = contentStub();
  const requests = [];
  // mountAdminControls renders its own panels plus this one and disables the
  // rotation controls without a session; it must not fetch protected metrics.
  const previousFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    const target = String(url);
    requests.push({ url: target, method: options.method || 'GET', body: options.body });
    if (target.includes('/settings')) {
      return {
        ok: true,
        status: 200,
        statusText: 'OK',
        headers: { get: (name) => (String(name).toLowerCase() === 'content-type' ? 'application/json' : null) },
        json: async () => ({ version: 3, isolationMode: 'lock-only' })
      };
    }
    throw new Error('unexpected fetch');
  };
  try {
    mountAdminControls(content, false, () => {}, (m) => successes.push(m),
      { pageId: 'thread-pool-admin', settings });
    const select = content.elements.get('select');
    assert.ok(select, 'mountAdminControls mounts the isolation panel');
    select.value = 'lock-only';
    await select.fire();
    const put = requests.find((r) => r.method === 'PUT');
    assert.ok(put, 'the change is written to the server');
    assert.equal(put.url, '/admin/v1/pools/thread-pool/settings');
    assert.equal(JSON.parse(put.body).isolationMode, 'lock-only');
    assert.equal(successes.length, 1, 'the acknowledged change is the one that was written');
    assert.match(successes[0], /lock-only/);
    assert.deepEqual(settings, {}, 'the mount-time settings map is not a save path');

    // An unknown value can never be stored: the select snaps back to the default.
    select.value = 'weaken-everything';
    await select.fire();
    assert.equal(select.value, 'separate+lock', 'the select shows what will actually run');
    const sent = requests.filter((r) => r.method === 'PUT');
    assert.equal(sent.length, 2);
    assert.equal(JSON.parse(sent[1].body).isolationMode, 'separate+lock');
  } finally {
    globalThis.fetch = previousFetch;
  }
});

test('D-W10-6: both pool admin pages carry the field in their verification section', () => {
  assert.deepEqual(Object.keys(ISOLATION_FIELD_BY_PAGE).sort(), ['process-pool-admin', 'thread-pool-admin']);
  for (const [pageId, fieldId] of Object.entries(ISOLATION_FIELD_BY_PAGE)) {
    const row = PAGE_SCHEMAS[pageId].rows.find((r) => r.id === fieldId);
    assert.ok(row, `${pageId} declares ${fieldId}`);
    assert.equal(row.type, 'enum');
    assert.equal(row.section, 'verification', 'it belongs to the trust section');
    assert.equal(row.default, DEFAULT_ISOLATION_MODE);
    assert.equal(row.range, ISOLATION_MODES.join('/'));
    assert.equal(row.whoMayChange, 'admin');
    assert.equal(row.wwppId, 'D-W10-6');
  }
});

test('D-W10-6: the app wires the page settings into the panel', () => {
  const app = fs.readFileSync(path.join(pagesDir, 'js', 'app.js'), 'utf8');
  assert.match(app, /mountAdminControls\(content, canAdmin,[\s\S]{0,120}pageId: this\.pageId, settings: this\.currentSettings/,
    'the save path and the panel see one settings map');
});

test('D-W10-6: the live job carries the page isolation mode, normalized', async () => {
  const { ContributeApp, DEMO_OPERATOR_KEY, createTaskEnvelope } = await import('../js/contribute.js');
  const { sha256Hex } = await import('../js/platform/hash.js');
  for (const [configValue, expected] of [['lock-only', 'lock-only'], [undefined, 'separate+lock'], ['junk', 'separate+lock']]) {
    const app = new ContributeApp({ autoStart: false });
    app.sourceMode = 'LIVE';
    app.tabIds = { tp: 'tab-iso' };
    app.tabId = 'tab-iso';
    app.runnerNonce = 'nonce-iso';
    app.runnerConfig = { isolationMode: configValue };
    const originalFetch = globalThis.fetch;
    const inputJSON = JSON.stringify({ json: { kind: 'matmul-f32-gpu', n: 4 } });
    const artifactBytes = new Uint8Array([1]);
    const artifactSha = await sha256Hex(artifactBytes);
    const envelope = createTaskEnvelope('job-iso', 'task-iso', artifactSha, 3600, DEMO_OPERATOR_KEY.keyId,
      await sha256Hex(new TextEncoder().encode(inputJSON)));
    globalThis.fetch = async (url) => {
      const target = String(url);
      if (target.includes('/leases')) {
        return {
          ok: true,
          json: async () => ({
            lease: { jobId: 'job-iso', taskId: 'task-iso' },
            inputJSON,
            taskEnvelope: { payload: { ...envelope }, signature: Buffer.alloc(64, 1).toString('base64') },
            moduleArtifactIds: ['art-iso']
          })
        };
      }
      if (target.includes('/artifacts/')) return { ok: true, arrayBuffer: async () => artifactBytes.buffer };
      return { ok: true, json: async () => ({}) };
    };
    try {
      const job = await app.getLiveJob();
      assert.ok(job, 'a job is built');
      assert.equal(job.isolationMode, expected, `config ${String(configValue)} ships as ${expected}`);
    } finally {
      globalThis.fetch = originalFetch;
    }
  }
});

test('D-W10-6: the labels say what each mode gives up', () => {
  const separate = isolationModeLabel('separate+lock');
  const lockOnly = isolationModeLabel('lock-only');
  assert.match(separate, /own worker/i);
  assert.match(separate, /hash/i);
  assert.match(lockOnly, /one worker/i);
  assert.notEqual(separate, lockOnly);
});
