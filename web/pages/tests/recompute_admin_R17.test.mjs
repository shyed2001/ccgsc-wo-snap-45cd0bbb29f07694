// R17: red-first Director recompute-mode panel and API contract.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { setCSRFToken } from '../js/api.js';
import {
  RECOMPUTE_MODES, recomputePath, renderRecomputePanel,
  readRecomputeMode, writeRecomputeMode
} from '../js/recompute_admin_R17.js';

function content() {
  const elements = new Map(), html = [];
  return {
    elements, html,
    insertAdjacentHTML(_at, value) { html.push(value); },
    querySelector(selector) {
      if (!elements.has(selector)) elements.set(selector, {
        value: 'off', disabled: false, textContent: '', hidden: false, listeners: new Map(),
        addEventListener(type, listener) { this.listeners.set(type, listener); }
      });
      return elements.get(selector);
    }
  };
}
function response(data) {
  return { ok: true, status: 200, headers: { get: () => 'application/json' }, json: async () => data };
}
test('R17: only the two approved modes; per-pool routes never guess a pool', () => {
  assert.deepEqual(RECOMPUTE_MODES, ['registry', 'off']);
  assert.equal(recomputePath('thread-pool-admin'), '/admin/v1/pools/thread-pool/recompute');
  assert.equal(recomputePath('process-pool-admin'), '/admin/v1/pools/process-pool/recompute');
  assert.throws(() => recomputePath('monitor'), /pool/i);
});
test('R17: Director panel shows off by default and explains redundancy/canaries', () => {
  const c = content();
  renderRecomputePanel(c, true, { mode: 'off', version: 0 });
  const html = c.html.join('');
  assert.match(html, /value="registry"/);
  assert.match(html, /value="off" selected/);
  assert.match(html, /2-of-N/);
  assert.match(html, /canar/i);
  assert.match(html, /sample rate/i);
  assert.equal(c.querySelector('#recompute-mode').disabled, false);
});
test('R17: non-admin panel is read-only', () => {
  const c = content();
  renderRecomputePanel(c, false, { mode: 'registry', version: 2 });
  assert.equal(c.querySelector('#recompute-mode').disabled, true);
});
test('R17: GET reads the chosen pool, PUT sends CSRF and version', async () => {
  const seen = [];
  setCSRFToken('r17-csrf');
  globalThis.fetch = async (url, options) => {
    seen.push({ url: String(url), options });
    return response({ mode: 'registry', version: 7 });
  };
  try {
    assert.equal((await readRecomputeMode('process-pool-admin')).mode, 'registry');
    assert.equal((await writeRecomputeMode('thread-pool-admin', 'registry', 6)).version, 7);
    assert.equal(seen[0].url, '/admin/v1/pools/process-pool/recompute');
    assert.equal(seen[1].url, '/admin/v1/pools/thread-pool/recompute');
    assert.equal(seen[1].options.method, 'PUT');
    assert.equal(seen[1].options.headers['X-CSRF-Token'], 'r17-csrf');
    assert.deepEqual(JSON.parse(seen[1].options.body), { mode: 'registry', expectedVersion: 6 });
    await assert.rejects(() => writeRecomputeMode('tp', 'invalid', 7), /mode/i);
    assert.equal(seen.length, 2, 'invalid mode never sends a request');
  } finally {
    setCSRFToken('');
    delete globalThis.fetch;
  }
});
test('R17: shared admin controls actually mounts the recompute panel', () => {
  const src = fs.readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), '../js/admin_controls_MVPW.js'), 'utf8');
  assert.match(src, /mountRecomputePanel\(/);
});


test('R17: unreadable or unauthorised mode is unknown, not presumed off', () => {
 const c = content();
 renderRecomputePanel(c, false);
 const html = c.html.join('');
 assert.match(html, /value="" disabled selected/);
 assert.doesNotMatch(html, /value="off" selected/);
 assert.equal(c.querySelector('#recompute-mode').value, '');
 assert.equal(c.querySelector('#recompute-mode').disabled, true);
});

test('R17: Director displays upgrade review when legacy sampling has no saved recompute mode', async () => {
 const { mountRecomputePanel } = await import('../js/recompute_admin_R17.js');
 const c = content();
 globalThis.fetch = async () => response({
  mode: 'off', version: 0, source: 'default', upgradeReviewRequired: true
 });
 try {
  mountRecomputePanel(c, true, 'thread-pool-admin', null, null);
  await new Promise(resolve => setImmediate(resolve));
  assert.match(c.querySelector('#recompute-status').textContent, /review.*sampling|sampling.*review/i);
  assert.equal(c.querySelector('#recompute-mode').value, 'off');
 } finally {
  delete globalThis.fetch;
 }
});


test('R17: upgrade warning allows confirming the existing off mode without toggling registry on', async () => {
 const { mountRecomputePanel } = await import('../js/recompute_admin_R17.js');
 const c = content(), writes = [];
 globalThis.fetch = async (_url, options = {}) => {
  if (options.method === 'PUT') {
   writes.push(JSON.parse(options.body));
   return response({ mode: 'off', version: 1, source: 'admin', upgradeReviewRequired: false });
  }
  return response({ mode: 'off', version: 0, source: 'default', upgradeReviewRequired: true });
 };
 try {
  mountRecomputePanel(c, true, 'thread-pool-admin', null, null);
  await new Promise(resolve => setImmediate(resolve));
  assert.match(c.html.join(''), /id="recompute-confirm"/);
  const confirm = c.querySelector('#recompute-confirm');
  assert.equal(confirm.disabled, false);
  await confirm.listeners.get('click')();
  assert.deepEqual(writes, [{mode:'off', expectedVersion:0}]);
  assert.equal(confirm.hidden, true);
  assert.match(c.querySelector('#recompute-status').textContent, /Saved: off/);
 } finally { delete globalThis.fetch; }
});
