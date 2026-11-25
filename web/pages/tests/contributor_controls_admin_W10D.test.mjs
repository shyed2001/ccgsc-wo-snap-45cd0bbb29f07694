// W10D row 2/4 (D-W10-11, CR-51): the Director select of the contributor
// controls. The suite pins the panel, the strict default, the schema rows of
// both pool admin pages, the pool settings endpoint path and the
// expectedVersion save rule the select uses.
//
// Truth: WO_W10D_CTL.md row 2/4, SPLIT_DESIGN_W10.md section 8 (D-W10-11).
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  mountAdminControls,
  renderContributorControlsPanel,
  contributorControlsLabel,
  normalizeContributorControlsChoice,
  CONTRIBUTOR_CONTROLS_SELECT_ID,
  CONTRIBUTOR_CONTROLS_STATUS_ID
} from '../js/admin_controls_MVPW.js';
import {
  PAGE_SCHEMAS,
  CONTRIBUTOR_CONTROLS_MODES,
  DEFAULT_CONTRIBUTOR_CONTROLS,
  CONTRIBUTOR_CONTROLS_FIELD_BY_PAGE,
  contributorControlsFieldFor,
  readContributorControls,
  isContributorControlsField
} from '../js/schema.js';
import {
  poolSettingsId,
  poolSettingsPath,
  getPoolSettings,
  savePoolSettings,
  setCSRFToken
} from '../js/api.js';

const here = path.dirname(fileURLToPath(import.meta.url));
const pagesDir = path.join(here, '..');

function contentStub() {
  const markup = [];
  const elements = new Map();
  function make(id) {
    const listeners = {};
    const element = {
      id,
      value: '',
      textContent: '',
      disabled: false,
      listeners,
      addEventListener(type, fn) { (listeners[type] = listeners[type] || []).push(fn); },
      fire(type) { for (const fn of listeners[type] || []) fn({ type, target: element }); }
    };
    elements.set(id, element);
    return element;
  }
  return {
    markup,
    elements,
    make,
    insertAdjacentHTML(_position, html) { markup.push(html); },
    querySelector(selector) {
      const id = selector.startsWith('#') ? selector.slice(1) : selector;
      return elements.get(id) || make(id);
    },
    querySelectorAll() { return []; }
  };
}

function jsonResponse(body, status = 200) {
  return {
    ok: status < 400,
    status,
    statusText: 'OK',
    headers: { get: () => 'application/json' },
    json: async () => body
  };
}

// ---------------------------------------------------------------- The panel

test('D-W10-11: the Director panel renders the three modes with off preselected', () => {
  const content = contentStub();
  const result = renderContributorControlsPanel(content, true, {}, 'thread-pool-admin');
  assert.equal(result.mode, 'off', 'a missing setting shows the mode it will run with');
  const html = content.markup.join('\n');
  assert.ok(html.includes('D-W10-11'), 'the panel names its decision');
  assert.ok(html.includes(`id="${CONTRIBUTOR_CONTROLS_SELECT_ID}"`), 'the select is rendered');
  for (const mode of CONTRIBUTOR_CONTROLS_MODES) {
    assert.ok(html.includes(`value="${mode}"`), `the select offers ${mode}`);
  }
  assert.ok(html.includes('value="off" selected'), 'the default is preselected');
  assert.ok(html.includes('data-row-id="tp.gen.contributor_controls"'), 'the schema row is pinned');
  assert.equal(content.elements.get(CONTRIBUTOR_CONTROLS_SELECT_ID).disabled, false, 'an admin may change it');
});

test('D-W10-11: a non-admin sees the stored mode, but the select is disabled', () => {
  const content = contentStub();
  const result = renderContributorControlsPanel(content, false,
    { contributorControls: 'startStopOptIn' }, 'process-pool-admin');
  assert.equal(result.mode, 'startStopOptIn');
  const html = content.markup.join('\n');
  assert.ok(html.includes('value="startStopOptIn" selected'));
  assert.ok(html.includes('data-row-id="pp.gen.contributor_controls"'));
  assert.equal(content.elements.get(CONTRIBUTOR_CONTROLS_SELECT_ID).disabled, true, 'only an admin may change it');
});

test('D-W10-11: an unknown stored value answers the strict default, never an invented mode', () => {
  for (const junk of ['', 'alwaysOn', 'OFF', null, undefined, 3, {}]) {
    assert.equal(normalizeContributorControlsChoice(junk), DEFAULT_CONTRIBUTOR_CONTROLS);
    assert.equal(readContributorControls({ contributorControls: junk }), DEFAULT_CONTRIBUTOR_CONTROLS);
  }
  assert.equal(readContributorControls({ 'tp.gen.contributor_controls': 'pauseResume' }, 'thread-pool-admin'), 'pauseResume');
  assert.equal(readContributorControls({ 'pp.gen.contributor_controls': 'startStopOptIn' }, 'process-pool-admin'), 'startStopOptIn');
  assert.equal(readContributorControls(null), 'off');
  assert.equal(normalizeContributorControlsChoice('pauseResume'), 'pauseResume');
});

test('D-W10-11: the labels say what each mode does', () => {
  const off = contributorControlsLabel('off');
  const pause = contributorControlsLabel('pauseResume');
  const optIn = contributorControlsLabel('startStopOptIn');
  assert.match(off, /no buttons/i);
  assert.match(off, /compute at once/i);
  assert.match(pause, /Pause\/Resume/i);
  assert.match(pause, /capacity/i);
  assert.match(optIn, /opens stopped/i);
  assert.match(optIn, /Start\/Stop/i);
  assert.match(optIn, /capacity/i);
});

// ---------------------------------------------------------------- Schema

test('D-W10-11: both pool admin pages carry the documented row, excluded from the generic form', () => {
  assert.deepEqual(Object.keys(CONTRIBUTOR_CONTROLS_FIELD_BY_PAGE).sort(),
    ['process-pool-admin', 'thread-pool-admin']);
  for (const [pageId, fieldId] of Object.entries(CONTRIBUTOR_CONTROLS_FIELD_BY_PAGE)) {
    const row = PAGE_SCHEMAS[pageId].rows.find((item) => item.id === fieldId);
    assert.ok(row, `${pageId} declares ${fieldId}`);
    assert.equal(row.type, 'enum');
    assert.equal(row.section, 'general');
    assert.equal(row.default, DEFAULT_CONTRIBUTOR_CONTROLS);
    assert.equal(row.range, CONTRIBUTOR_CONTROLS_MODES.join('/'));
    assert.equal(row.whoMayChange, 'admin');
    assert.equal(row.wwppId, 'D-W10-11');
    assert.equal(isContributorControlsField(row.id), true);
    assert.equal(contributorControlsFieldFor(pageId), fieldId);
  }
  assert.equal(isContributorControlsField('tp.gen.enabled'), false);

  // app.js renders the generic form: the field must be skipped there, so this
  // panel is the only control that edits it.
  const appSource = fs.readFileSync(path.join(pagesDir, 'js', 'app.js'), 'utf8');
  assert.ok((appSource.match(/isContributorControlsField/g) || []).length >= 3,
    'app.js imports and applies the exclusion in both render paths');
});

// ---------------------------------------------------------------- The pool settings endpoint

test('D-W10-11: the select path targets the pool settings endpoint of the page', () => {
  assert.equal(poolSettingsId('thread-pool-admin'), 'thread-pool');
  assert.equal(poolSettingsId('process-pool-admin'), 'process-pool');
  assert.equal(poolSettingsId('tp'), 'thread-pool');
  assert.equal(poolSettingsId('pp'), 'process-pool');
  assert.equal(poolSettingsId('monitor'), null);
  assert.equal(poolSettingsPath('thread-pool-admin'), '/admin/v1/pools/thread-pool/settings');
  assert.equal(poolSettingsPath('process-pool-admin'), '/admin/v1/pools/process-pool/settings');
  assert.throws(() => poolSettingsPath('monitor'), /No pool settings endpoint/);
});

test('D-W10-11: savePoolSettings PUTs the mode with expectedVersion and the CSRF token', async () => {
  let seen;
  setCSRFToken('w10d-csrf');
  globalThis.fetch = async (url, options) => {
    seen = { url: String(url), options };
    return jsonResponse({ version: 4, contributorControls: 'pauseResume' });
  };
  try {
    const saved = await savePoolSettings('thread-pool-admin', { contributorControls: 'pauseResume' }, 3);
    assert.equal(seen.url, '/admin/v1/pools/thread-pool/settings');
    assert.equal(seen.options.method, 'PUT');
    assert.equal(seen.options.headers['X-CSRF-Token'], 'w10d-csrf');
    assert.deepEqual(JSON.parse(seen.options.body), { contributorControls: 'pauseResume', expectedVersion: 3 });
    assert.equal(saved.version, 4);
  } finally {
    setCSRFToken('');
    delete globalThis.fetch;
  }
});

test('D-W10-11: getPoolSettings reads the same endpoint without a write', async () => {
  let seen;
  globalThis.fetch = async (url, options) => {
    seen = { url: String(url), options };
    return jsonResponse({ poolId: 'pp', version: 2, contributorControls: 'startStopOptIn' });
  };
  try {
    const settings = await getPoolSettings('process-pool-admin');
    assert.equal(seen.url, '/admin/v1/pools/process-pool/settings');
    assert.equal(seen.options.method, undefined, 'a GET is the default');
    assert.equal(settings.contributorControls, 'startStopOptIn');
  } finally {
    delete globalThis.fetch;
  }
});

// ---------------------------------------------------------------- Mounting

test('D-W10-11: the mounted select saves with the loaded version and shows the new value', async () => {
  const content = contentStub();
  const saved = [];
  const successes = [];
  const failures = [];
  const previousDocument = globalThis.document;
  globalThis.document = { createElement: () => ({ textContent: '' }) };
  globalThis.fetch = async () => jsonResponse({ samples: [] });
  try {
    mountAdminControls(content, true, (err) => failures.push(err), (msg) => successes.push(msg), {
      pageId: 'thread-pool-admin',
      settings: {},
      poolSettings: { contributorControls: 'pauseResume', version: 4 },
      saveContributorControls: async (mode, version) => {
        saved.push({ mode, version });
        return { version: 5, contributorControls: mode };
      }
    });
    const select = content.elements.get(CONTRIBUTOR_CONTROLS_SELECT_ID);
    assert.ok(select, 'mountAdminControls mounts the contributor panel');
    assert.equal(select.disabled, false);
    select.value = 'startStopOptIn';
    select.fire('change');
    await new Promise((resolve) => setTimeout(resolve, 0));
    assert.deepEqual(saved, [{ mode: 'startStopOptIn', version: 4 }], 'the current version travels with the save');
    assert.equal(select.value, 'startStopOptIn');
    assert.equal(select.disabled, false);
    assert.match(content.elements.get(CONTRIBUTOR_CONTROLS_STATUS_ID).textContent, /Saved: startStopOptIn/);
    assert.equal(successes.some((msg) => /startStopOptIn/.test(msg)), true);
    assert.equal(failures.length, 0);
  } finally {
    globalThis.document = previousDocument;
    delete globalThis.fetch;
  }
});

test('D-W10-11: a refused save reverts the select and reports the error', async () => {
  const content = contentStub();
  const failures = [];
  const previousDocument = globalThis.document;
  globalThis.document = { createElement: () => ({ textContent: '' }) };
  globalThis.fetch = async () => jsonResponse({ samples: [] });
  try {
    mountAdminControls(content, true, (err) => failures.push(err), () => {}, {
      pageId: 'thread-pool-admin',
      settings: {},
      poolSettings: { contributorControls: 'pauseResume', version: 2 },
      saveContributorControls: async () => { throw new Error('Config version conflict.'); }
    });
    const select = content.elements.get(CONTRIBUTOR_CONTROLS_SELECT_ID);
    select.value = 'off';
    select.fire('change');
    await new Promise((resolve) => setTimeout(resolve, 0));
    assert.equal(select.value, 'pauseResume', 'the select shows what actually runs');
    assert.equal(failures.length, 1);
    assert.match(content.elements.get(CONTRIBUTOR_CONTROLS_STATUS_ID).textContent, /Save failed/);
  } finally {
    globalThis.document = previousDocument;
    delete globalThis.fetch;
  }
});

test('D-W10-11: a non-admin mount never loads or writes the pool settings', async () => {
  const content = contentStub();
  const paths = [];
  globalThis.fetch = async (url) => {
    paths.push(String(url));
    throw new Error('unexpected fetch');
  };
  try {
    mountAdminControls(content, false, () => {}, () => {}, { pageId: 'thread-pool-admin' });
    await new Promise((resolve) => setTimeout(resolve, 0));
    assert.deepEqual(paths, [], 'no request without a session');
    assert.equal(content.elements.get(CONTRIBUTOR_CONTROLS_SELECT_ID).disabled, true);
  } finally {
    delete globalThis.fetch;
  }
});
