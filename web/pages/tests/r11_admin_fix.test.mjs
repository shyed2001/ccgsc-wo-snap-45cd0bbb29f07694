// R11-M6/M7: the Director isolation select must acknowledge only a real write,
// must refuse without a pageId, and must never mutate the mount-time settings
// object of the page config (the pool settings endpoint is its home).
import test from 'node:test';
import assert from 'node:assert/strict';

import { mountAdminControls } from '../js/admin_controls_MVPW.js';

function contentStub() {
  const markup = [];
  const elements = new Map();
  return {
    markup,
    elements,
    insertAdjacentHTML(_position, html) { markup.push(html); },
    querySelector(selector) {
      const id = selector.startsWith('#') ? selector.slice(1) : selector;
      if (!elements.has(id)) {
        const listeners = [];
        elements.set(id, {
          id,
          value: '',
          textContent: '',
          disabled: false,
          addEventListener(type, fn) { if (type === 'change') listeners.push(fn); },
          fire() { for (const fn of listeners) fn(); }
        });
      }
      return elements.get(id);
    },
    querySelectorAll() { return []; }
  };
}

test('R11-M6: the isolation select refuses without a pageId and does not ack', () => {
  const content = contentStub();
  const messages = [];
  const errors = [];
  const previousFetch = globalThis.fetch;
  globalThis.fetch = async () => { throw new Error('unexpected fetch'); };
  try {
    mountAdminControls(content, true, (e) => errors.push(e), (m) => messages.push(m), { settings: {} });
  } finally {
    globalThis.fetch = previousFetch;
  }
  const select = content.elements.get('kernel-isolation-mode');
  assert.ok(select, 'the select is rendered');
  assert.equal(select.disabled, true, 'a control without a known page has nothing to save into');
  select.value = 'lock-only';
  select.fire();
  assert.deepEqual(messages, [], 'no "saved" acknowledgement without a real write');
  assert.equal(errors.length, 1, 'the refusal is surfaced');
  assert.match(String(errors[0] && errors[0].message), /page/i);
});

test('R11-M6: the isolation select never writes the mount-time page settings map', () => {
  const content = contentStub();
  const settings = {};
  const previousFetch = globalThis.fetch;
  globalThis.fetch = async () => ({ ok: true, status: 200, json: async () => ({}) });
  try {
    mountAdminControls(content, true, () => {}, () => {}, { pageId: 'thread-pool-admin', settings });
  } finally {
    globalThis.fetch = previousFetch;
  }
  assert.deepEqual(Object.keys(settings), [], 'the panel writes only through the pool settings endpoint');
});
