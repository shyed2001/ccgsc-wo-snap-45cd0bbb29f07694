// Path: web/pages/tests/caps_admin_R15.test.mjs | Author: Hermes (MiniMax-M3) | 2026-11-22
// R15-FIX-CAPS Addendum (G-R15-3): the resource caps are editable in the admin panel.
// The test pins the panel itself: the four fields, the "applies after restart"
// wording, the soft-limit note and the body of the write.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  CAPS_FIELDS,
  CAPS_LIVE_FIELDS,
  CAPS_RESTART_FIELDS,
  capsBodyFromForm,
  capsFieldValue,
  capsPendingRestart,
  capsSoftLimitNote,
  capsStatusText,
  renderCapsPanel
} from '../js/caps_admin_R15.js';

// A caps reply of the server.
const reply = {
  numCPU: 12,
  maxProcs: 11,
  memLimitMB: 24327,
  memLimitBytes: 25505131520,
  workerPool: 11,
  dbMaxOpenConns: 12,
  physicalMemMB: 32437,
  cgroupMemMB: 0,
  cgroupSource: '',
  availableMemMB: 32437,
  memLimitSource: 'auto',
  softLimit: true,
  softLimitExplain: 'the memory limit is a soft GC target, not a hard limit on the RSS of the process'
};

// A fake form: the four inputs of the panel.
function form(values) {
  const inputs = {};
  for (const field of CAPS_FIELDS) {
    inputs[`#caps-${field.name}`] = { value: values[field.name] === undefined ? '' : String(values[field.name]) };
  }
  return { querySelector(selector) { return inputs[selector] || null; } };
}

// A fake content element that collects the rendered HTML.
function content() {
  const el = {
    html: '',
    insertAdjacentHTML(_where, html) { this.html += html; },
    querySelector() { return null; },
    querySelectorAll() { return []; }
  };
  return el;
}

test('the panel carries the four caps fields', () => {
  assert.equal(CAPS_FIELDS.length, 4);
  const names = CAPS_FIELDS.map(f => f.name);
  assert.deepEqual(names, ['maxProcs', 'memLimitMB', 'workerPool', 'dbMaxOpenConns']);
});

test('maxProcs and memLimitMB apply live, workerPool and dbMaxOpenConns need a restart', () => {
  assert.deepEqual([...CAPS_LIVE_FIELDS], ['maxProcs', 'memLimitMB']);
  assert.deepEqual([...CAPS_RESTART_FIELDS], ['workerPool', 'dbMaxOpenConns']);
});

test('the rendered panel names the restart-bound fields', () => {
  const el = content();
  renderCapsPanel(el, true, reply);
  assert.ok(el.html.includes('caps-maxProcs'));
  assert.ok(el.html.includes('caps-memLimitMB'));
  assert.ok(el.html.includes('caps-workerPool'));
  assert.ok(el.html.includes('caps-dbMaxOpenConns'));
  // The two restart-bound fields say so next to the field.
  assert.ok(el.html.includes('applies after restart'));
  assert.ok(el.html.includes('applies immediately'));
});

test('the panel states that the memory limit is a soft GC target', () => {
  const el = content();
  renderCapsPanel(el, true, reply);
  assert.ok(el.html.includes('soft GC target'));
  assert.ok(el.html.includes('not a hard limit on the RSS'));
});

test('the panel shows the effective values of the server', () => {
  const el = content();
  renderCapsPanel(el, true, reply);
  assert.ok(el.html.includes('value="11"'));
  assert.ok(el.html.includes('value="24327"'));
  assert.ok(el.html.includes('value="12"'));
});

test('a field the admin left empty is not sent, so a write never resets a field', () => {
  const body = capsBodyFromForm(form({ maxProcs: '4', memLimitMB: '', workerPool: '2', dbMaxOpenConns: '' }));
  assert.deepEqual(body, { maxProcs: 4, workerPool: 2 });
});

test('the body carries the four values when the admin filled them in', () => {
  const body = capsBodyFromForm(form({ maxProcs: '4', memLimitMB: '512', workerPool: '2', dbMaxOpenConns: '9' }));
  assert.deepEqual(body, { maxProcs: 4, memLimitMB: 512, workerPool: 2, dbMaxOpenConns: 9 });
});

test('a negative value never reaches the server as a silent fallback', () => {
  const body = capsBodyFromForm(form({ maxProcs: '-1' }));
  assert.deepEqual(body, { maxProcs: -1 });
  // The server refuses it (internal/api/admin_caps_write_R15.go); the panel does
  // not hide the value from the operator.
  assert.equal(body.maxProcs, -1);
});

test('the status line names the fields that wait for a restart', () => {
  const text = capsStatusText({ pendingRestart: ['workerPool', 'dbMaxOpenConns'] });
  assert.ok(text.includes('workerPool'));
  assert.ok(text.includes('dbMaxOpenConns'));
  assert.ok(text.includes('restart'));
});

test('a reply without the pending list answers the documented list', () => {
  assert.deepEqual(capsPendingRestart({}), ['workerPool', 'dbMaxOpenConns']);
  assert.deepEqual(capsPendingRestart({ pendingRestart: [] }), ['workerPool', 'dbMaxOpenConns']);
});

test('a missing caps value reads as 0 (auto), never as a wrong number', () => {
  assert.equal(capsFieldValue(reply, 'maxProcs'), 11);
  assert.equal(capsFieldValue(null, 'maxProcs'), 0);
  assert.equal(capsFieldValue({ maxProcs: 'not a number' }, 'maxProcs'), 0);
});

test('the soft-limit note falls back to the documented sentence', () => {
  assert.ok(capsSoftLimitNote(reply).includes('soft GC target'));
  assert.ok(capsSoftLimitNote(null).includes('soft GC target'));
});
