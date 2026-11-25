// F11, 2026-11-25: red->green tests for the G-F11-1/2 rules additions.
// RED on base 063bfac5: P2P_MODES/normalizeP2pMode/sameAppAllowed/stunUrlAllowed/
// sanitizeIceServers do not exist and turnAllowed(true config) answers true.
// GREEN with the F11 patch.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import {
  MAX_TRANSFER_BYTES, CHUNK_BYTES,
  P2P_MODES, DEFAULT_P2P_MODE, SHA256_HEX_RE,
  normalizeP2pMode, sameAppAllowed, stunUrlAllowed, sanitizeIceServers, turnAllowed
} from '../js/platform/p2p/rules.js';

test('admin P2P modes are stun/lan/off with code default off', () => {
  assert.deepEqual(P2P_MODES, ['off', 'lan', 'stun']);
  assert.equal(DEFAULT_P2P_MODE, 'off');
});

test('normalizeP2pMode fails closed on unknown values', () => {
  assert.equal(normalizeP2pMode('off'), 'off');
  assert.equal(normalizeP2pMode(' LAN '), 'lan');
  assert.equal(normalizeP2pMode('STUN'), 'stun');
  assert.equal(normalizeP2pMode('turn'), '');
  assert.equal(normalizeP2pMode('near'), '');
  assert.equal(normalizeP2pMode(undefined), '');
  assert.equal(normalizeP2pMode(null), '');
});

test('G-F11-1: sameAppAllowed refuses empty, non-string and cross-app ids', () => {
  assert.equal(sameAppAllowed('app-1', 'app-1'), true);
  assert.equal(sameAppAllowed(' app-1 ', 'app-1'), true);
  assert.equal(sameAppAllowed('app-1', 'app-2'), false);
  assert.equal(sameAppAllowed('', ''), false);
  assert.equal(sameAppAllowed(undefined, 'app-1'), false);
  assert.equal(sameAppAllowed('app-1', null), false);
  assert.equal(sameAppAllowed(7, 7), false);
});

test('G-F11-2: stunUrlAllowed accepts only self-hosted stun:/stuns: URLs', () => {
  assert.equal(stunUrlAllowed('stun:stun.corp.internal:3478'), true);
  assert.equal(stunUrlAllowed('stuns://stun.corp.internal'), true);
  assert.equal(stunUrlAllowed('stun:10.0.0.8'), true);
  for (const bad of [
    'turn:turn.corp.internal:3478', 'turns:turn.corp.internal',
    'stun:stun.l.google.com:19302', 'stun:stun.cloudflare.com',
    'stun:global.stun.twilio.com', 'stun:user@stun.corp.internal',
    'stun:stun.corp.internal/transport udp', '', 'stun:', 'stun',
    '  stun:stun.corp.internal,turn:x', 42, undefined, null
  ]) {
    assert.equal(stunUrlAllowed(bad), false, 'expected refusal: ' + String(bad));
  }
});

test('G-F11-2: LAN-only mode sanitizes to an EMPTY iceServers list (no STUN)', () => {
  assert.deepEqual(sanitizeIceServers('lan', [
    { urls: 'stun:stun.corp.internal:3478' }, { urls: ['turn:turn.corp.internal'] }
  ]), []);
  assert.deepEqual(sanitizeIceServers('off', [{ urls: 'stun:stun.corp.internal' }]), []);
  assert.deepEqual(sanitizeIceServers('', [{ urls: 'stun:stun.corp.internal' }]), []);
  assert.deepEqual(sanitizeIceServers('lan', 'junk'), []);
});

test('stun mode keeps only valid stun entries and strips credentials', () => {
  assert.deepEqual(sanitizeIceServers('stun', [
    { urls: 'stun:stun.corp.internal:3478', username: 'x', credential: 'secret', credentialType: 'password' },
    { urls: ['turn:turn.corp.internal?transport=tcp'] },
    { urls: 'stun:stun.l.google.com:19302' },
    { urls: 'stun:stun2.corp.internal:3478' },
    'junk', null
  ]), [
    { urls: ['stun:stun.corp.internal:3478'] },
    { urls: ['stun:stun2.corp.internal:3478'] }
  ]);
  assert.deepEqual(sanitizeIceServers('stun', undefined), []);
});

test('superseded D-W10-14 TURN never dials again (fail closed)', () => {
  assert.equal(turnAllowed(undefined), false);
  assert.equal(turnAllowed(null), false);
  assert.equal(turnAllowed({ enabled: true, urls: ['turn:turn.corp.internal:3478'], credentialRef: 'vault-key' }), false);
  assert.equal(turnAllowed({ enabled: true, urls: ['turns:turn.corp.internal'], credentialRef: 'vault-key' }), false);
  assert.equal(turnAllowed('truthy junk'), false);
});

test('shared digest shape and legacy transfer caps stay intact', () => {
  assert.equal(SHA256_HEX_RE.test('a'.repeat(64)), true);
  assert.equal(SHA256_HEX_RE.test('A'.repeat(64)), false);
  assert.equal(SHA256_HEX_RE.test('a'.repeat(63)), false);
  assert.equal(MAX_TRANSFER_BYTES, 32 * 1024 * 1024);
  assert.equal(CHUNK_BYTES, 16 * 1024);
});

test('platform/p2p stays free of SharedArrayBuffer (hard rule)', async () => {
  const names = ['cache.js', 'rules.js', 'transport.js', 'signaling.js', 'production.js'];
  for (const name of names) {
    const code = await readFile(new URL('../js/platform/p2p/' + name, import.meta.url), 'utf8');
    assert.doesNotMatch(code, /SharedArrayBuffer|Atomics\.(store|wait|exchange)/, name);
  }
});

test('CR-53 boundary: new modules import only inside platform/p2p', async () => {
  for (const name of ['signaling.js', 'production.js']) {
    const code = await readFile(new URL('../js/platform/p2p/' + name, import.meta.url), 'utf8');
    for (const match of code.matchAll(/from\s+['"]([^'"]+)['"]/g)) {
      const specifier = match[1];
      assert.match(specifier, /^\.\/[a-z]+\.js$/, name + ' imports ' + specifier);
    }
  }
});
