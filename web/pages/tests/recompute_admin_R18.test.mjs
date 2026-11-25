// R18: recompute policy selects (busy/malformed) and the write contract.
import test from 'node:test';
import assert from 'node:assert/strict';
import { setCSRFToken } from '../js/api.js';
import {
  RECOMPUTE_BUSY_POLICIES, RECOMPUTE_MALFORMED_POLICIES,
  renderRecomputePanel, writeRecomputePolicies
} from '../js/recompute_admin_R17.js';

function content() {
  const elements = new Map(), html = [];
  return {
    elements, html,
    insertAdjacentHTML(_at, value) { html.push(value); },
    querySelector(selector) {
      if (!elements.has(selector)) elements.set(selector, {
        value: '', disabled: false, textContent: '', hidden: false, listeners: new Map(),
        addEventListener(type, listener) { this.listeners.set(type, listener); }
      });
      return elements.get(selector);
    }
  };
}
function response(data) {
  return { ok: true, status: 200, headers: { get: () => 'application/json' }, json: async () => data };
}

test('R18: only the approved busy and malformed policies', () => {
  assert.deepEqual(RECOMPUTE_BUSY_POLICIES, ['skip', 'defer']);
  assert.deepEqual(RECOMPUTE_MALFORMED_POLICIES, ['reject', 'fraud']);
});

test('R18: Director panel shows the policy selects with skip/reject defaults', () => {
  const c = content();
  renderRecomputePanel(c, true, { mode: 'registry', version: 2 });
  const html = c.html.join('');
  assert.match(html, /value="skip" selected/);
  assert.match(html, /value="defer"/);
  assert.match(html, /value="reject" selected/);
  assert.match(html, /value="fraud"/);
  assert.equal(c.querySelector('#recompute-busy-policy').disabled, false);
  assert.equal(c.querySelector('#recompute-malformed-policy').disabled, false);
});

test('R18: non-admin policy selects are read-only', () => {
  const c = content();
  renderRecomputePanel(c, false, { mode: 'registry', version: 2 });
  assert.equal(c.querySelector('#recompute-busy-policy').disabled, true);
  assert.equal(c.querySelector('#recompute-malformed-policy').disabled, true);
});

test('R18: PUT sends both policies with CSRF and the version', async () => {
  const seen = [];
  setCSRFToken('r18-csrf');
  globalThis.fetch = async (url, options) => {
    seen.push({ url: String(url), options });
    return response({ mode: 'registry', busyPolicy: 'defer', malformedPolicy: 'fraud', version: 9 });
  };
  try {
    const saved = await writeRecomputePolicies('thread-pool-admin', 'defer', 'fraud', 8);
    assert.equal(saved.version, 9);
    assert.equal(seen[0].url, '/admin/v1/pools/thread-pool/recompute');
    assert.equal(seen[0].options.method, 'PUT');
    assert.equal(seen[0].options.headers['X-CSRF-Token'], 'r18-csrf');
    assert.deepEqual(JSON.parse(seen[0].options.body), { busyPolicy: 'defer', malformedPolicy: 'fraud', expectedVersion: 8 });
    await assert.rejects(() => writeRecomputePolicies('tp', 'sometimes', 'fraud', 8), /busy/i);
    await assert.rejects(() => writeRecomputePolicies('tp', 'defer', 'sometimes', 8), /malformed/i);
    assert.equal(seen.length, 1, 'invalid policies never send a request');
  } finally {
    setCSRFToken('');
    delete globalThis.fetch;
  }
});

test('R18: mounted panel applies the server policies', async () => {
  const { mountRecomputePanel } = await import('../js/recompute_admin_R17.js');
  const c = content();
  globalThis.fetch = async () => response({
    mode: 'registry', version: 3, source: 'admin', busyPolicy: 'defer', malformedPolicy: 'fraud'
  });
  try {
    mountRecomputePanel(c, true, 'thread-pool-admin', null, null);
    await new Promise(resolve => setImmediate(resolve));
    assert.equal(c.querySelector('#recompute-busy-policy').value, 'defer');
    assert.equal(c.querySelector('#recompute-malformed-policy').value, 'fraud');
  } finally {
    delete globalThis.fetch;
  }
});

test('R18: a busy-policy change saves both policies through the endpoint', async () => {
  const { mountRecomputePanel } = await import('../js/recompute_admin_R17.js');
  const c = content(), writes = [];
  globalThis.fetch = async (_url, options = {}) => {
    if (options.method === 'PUT') {
      writes.push(JSON.parse(options.body));
      return response({ mode: 'registry', busyPolicy: 'defer', malformedPolicy: 'reject', version: 4 });
    }
    return response({ mode: 'registry', version: 3, source: 'admin', busyPolicy: 'skip', malformedPolicy: 'reject' });
  };
  try {
    mountRecomputePanel(c, true, 'thread-pool-admin', null, null);
    await new Promise(resolve => setImmediate(resolve));
    const busy = c.querySelector('#recompute-busy-policy');
    busy.value = 'defer';
    await busy.listeners.get('change')();
    assert.deepEqual(writes, [{ busyPolicy: 'defer', malformedPolicy: 'reject', expectedVersion: 3 }]);
  } finally {
    delete globalThis.fetch;
  }
});
