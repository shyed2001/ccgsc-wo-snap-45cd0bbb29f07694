// R10-FIX owner D-W10-9 ("Runner self-reports"): the page bakes a pin of the
// runner it ships, the runner reads that pin back and reports it in its 'ready'
// message, and the parent forwards it on register and heartbeat. Without it the
// W9 stale-runner refund path is unreachable from the browser.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { fileURLToPath } from 'node:url';

import { runnerSelfPin } from '../js/runner.js';
import { buildInlineRunnerScript, OPERATOR_KEYS } from '../js/runner.js';
import {
  ContributeApp,
  validateMessageSchema
} from '../js/contribute.js';

const pagesDir = path.join(path.dirname(fileURLToPath(import.meta.url)), '..');
const html = fs.readFileSync(path.join(pagesDir, 'runner.html'), 'utf8');
const runnerSource = fs.readFileSync(path.join(pagesDir, 'js', 'runner.js'), 'utf8');

function pagePin() {
  const m = html.match(/<html[^>]*data-runner-pin="([^"]*)"/);
  return m ? m[1] : null;
}

test('the generated page pins "<sha256 of the exact script element text>:<keyId>"', () => {
  const pin = pagePin();
  assert.ok(pin, 'runner.html must carry data-runner-pin');
  const [hash, keyId] = pin.split(':');
  assert.match(hash, /^[0-9a-f]{64}$/, 'the pin hash is a sha256 hex digest');
  assert.equal(keyId, Object.keys(OPERATOR_KEYS)[0], 'the pin names the operator key the runner verifies against');
  // D-W10-9 (one pin): the pin covers the exact text of the inline module script,
  // the same bytes the CSP hash covers. Hashing `inline` alone is the superseded
  // rule that made the server compare a pin no truthful page could report.
  const script = html.match(/<script type="module">([\s\S]*?)<\/script>/);
  assert.ok(script, 'the generated page carries the inline module script');
  assert.equal(hash, crypto.createHash('sha256').update(script[1], 'utf8').digest('hex'),
    'the pin hashes the exact script element text the page ships');
  assert.equal(script[1], '\n' + buildInlineRunnerScript(runnerSource) + '  ',
    'the element text is the padded runner body the pin and the CSP both cover');
});

test('the runner reads the pin off the generated document', () => {
  const pin = pagePin();
  assert.equal(runnerSelfPin({ documentElement: { getAttribute: () => pin } }), pin);
  assert.equal(runnerSelfPin({ documentElement: { getAttribute: () => null } }), '',
    'a page without a pin reports none rather than a lie');
  assert.equal(runnerSelfPin(null), '');
  assert.equal(runnerSelfPin({ documentElement: { getAttribute: () => { throw new Error('blocked'); } } }), '');
});

test('the runner announces its pin in the ready message and the parent keeps it', () => {
  // The schema accepts the optional field but only as a string.
  assert.equal(validateMessageSchema({ type: 'ready', nonce: 'n', runnerPin: 'a:b' }).valid, true);
  assert.equal(validateMessageSchema({ type: 'ready', nonce: 'n' }).valid, true, 'an older runner has no pin');
  const bad = validateMessageSchema({ type: 'ready', nonce: 'n', runnerPin: 7 });
  assert.equal(bad.valid, false);
  assert.equal(bad.error, 'ready_runnerPin_must_be_string');

  // The parent stores it on a matching nonce and ignores a mismatched one.
  const app = new ContributeApp({ autoStart: false });
  app.runnerNonce = 'nonce-1';
  app.render = () => {};
  app.log = () => {};
  app.handleRunnerPortMessage({ type: 'ready', nonce: 'nonce-wrong', runnerPin: 'dead:beef' });
  assert.equal(app.runnerPin, '', 'a mismatched nonce must not install a pin');
  app.handleRunnerPortMessage({ type: 'ready', nonce: 'nonce-1', runnerPin: 'dead:beef' });
  assert.equal(app.runnerPin, 'dead:beef');
});

test('register and heartbeat both carry the reporter pin', async () => {
  const originalFetch = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    calls.push({ url: String(url), body: init.body ? JSON.parse(init.body) : null });
    if (String(url).endsWith('/workers/register')) {
      return { ok: true, status: 201, headers: { get: () => null }, json: async () => ({ tabId: 'tab-1' }) };
    }
    return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({}) };
  };
  try {
    const app = new ContributeApp({ authToken: 't', autoStart: false });
    app.sourceMode = 'LIVE';
    app.tabId = 'tab-1';
    app.tabIds = { tp: 'tab-1' };
    app.runnerPin = 'live:key';
    app.log = () => {};
    await app.registerWorker();
    await app.sendHeartbeat();
    const reg = calls.find((c) => c.url.endsWith('/workers/register'));
    const beat = calls.find((c) => c.url.includes('/heartbeat'));
    assert.equal(reg.body.runnerPin, 'live:key', 'the registration never sends the runner pin');
    assert.equal(beat.body.runnerPin, 'live:key', 'the heartbeat never sends the runner pin');
  } finally {
    globalThis.fetch = originalFetch;
  }
});
