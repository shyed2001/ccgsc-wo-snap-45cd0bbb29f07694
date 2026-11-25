// WO R14 step 2 (D-W10-14): pure P2P rules of platform/p2p/rules.js. The suite
// pins the constants of design section 5, the tier/scope mapping of section 3,
// the chunk plan and deadline rules, the fallback state machine over every
// reason code, the serving rules and the optional TURN gate.
//
// Truth: WO_R13_WS3_P2P_CLIENT_DESIGN.md sections 2.2/2.4/3/5/6; D-W10-14.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  MAX_TRANSFER_BYTES, CHUNK_BYTES, BUFFER_HWM_BYTES, BUFFER_RESUME_BYTES,
  SIGNAL_MSG_MAX_BYTES, SIGNAL_RATE_PER_SEC, SIGNAL_HANDSHAKES_MAX,
  HANDSHAKE_BUDGET_MS, OPEN_WAIT_MS, STALL_TIMEOUT_MS,
  SERVE_BUDGET_MIB_PER_MIN, SERVE_BUDGET_WINDOW_MS,
  P2P_SCOPES, P2P_TIERS, P2P_FALLBACK_REASONS, P2P_ABORT_REASONS, P2P_TRANSFER_STATES,
  peerScopeAllows, normalizePeerScope, chunkPlan, transferDeadline,
  transferDeadlineBudget, fallbackFSM, serveAllowed, serveBudgetTick, turnAllowed
} from '../js/platform/p2p/rules.js';

const MIB = 1024 * 1024;
const here = path.dirname(fileURLToPath(import.meta.url));

// ---------------------------------------------------------------- Constants

test('D-W10-14: the constants carry the design section 5 values', () => {
  assert.equal(MAX_TRANSFER_BYTES, 32 * MIB, 'server cap pool.MaxArtifactBytes');
  assert.equal(CHUNK_BYTES, 16 * 1024, 'cross-browser SCTP safe message size');
  assert.equal(BUFFER_HWM_BYTES, MIB, 'backpressure high-water mark');
  assert.equal(BUFFER_RESUME_BYTES, 256 * 1024, 'resume below the mark');
  assert.equal(SIGNAL_MSG_MAX_BYTES, 64 * 1024, 'signaling message cap');
  assert.equal(SIGNAL_RATE_PER_SEC, 10, 'signaling messages per second');
  assert.equal(SIGNAL_HANDSHAKES_MAX, 2, 'concurrent handshakes per peer');
  assert.equal(HANDSHAKE_BUDGET_MS, 8000, 'offer -> ICE -> open budget');
  assert.equal(OPEN_WAIT_MS, 5000, 'open wait inside the handshake budget');
  assert.equal(STALL_TIMEOUT_MS, 8000, 'dead-peer stall abort');
  assert.equal(SERVE_BUDGET_MIB_PER_MIN, 64, 'serve budget per tab');
  assert.equal(SERVE_BUDGET_WINDOW_MS, 60000, 'rolling minute window');
});

test('D-W10-14: constants are frozen and every exported list is complete', () => {
  for (const list of [P2P_SCOPES, P2P_TIERS, P2P_FALLBACK_REASONS, P2P_ABORT_REASONS, P2P_TRANSFER_STATES]) {
    assert.equal(Object.isFrozen(list), true);
  }
  assert.deepEqual([...P2P_SCOPES], ['same-device', 'same-phone', 'same-lan', 'near']);
  assert.deepEqual([...P2P_TIERS], ['t0', 't1', 't2']);
  assert.deepEqual([...P2P_FALLBACK_REASONS].sort(),
    ['busy', 'channel', 'hash', 'ice', 'no-peer', 'signaling', 'size', 'stall', 'timeout']);
  for (const reason of P2P_FALLBACK_REASONS) {
    assert.ok(P2P_ABORT_REASONS.includes(reason), `${reason} is abortable`);
  }
  assert.ok(P2P_ABORT_REASONS.includes('deadline'), 'the abort wire reason deadline exists');
});

// ---------------------------------------------------------------- peerScopeAllows

test('peer scope -> tier mapping of design section 3', () => {
  assert.equal(peerScopeAllows('same-device', 't0'), true);
  assert.equal(peerScopeAllows('same-device', 't1'), false);
  assert.equal(peerScopeAllows('same-device', 't2'), false);
  assert.equal(peerScopeAllows('same-lan', 't0'), true);
  assert.equal(peerScopeAllows('same-lan', 't1'), true);
  assert.equal(peerScopeAllows('same-lan', 't2'), false);
  assert.equal(peerScopeAllows('near', 't0'), true);
  assert.equal(peerScopeAllows('near', 't1'), true);
  assert.equal(peerScopeAllows('near', 't2'), true);
});

test('same-phone is a same-device alias (F12 owner pick), never a LAN tier', () => {
  assert.equal(peerScopeAllows('same-phone', 't0'), true);
  assert.equal(peerScopeAllows('same-phone', 't1'), false);
  assert.equal(peerScopeAllows('same-phone', 't2'), false);
  assert.equal(normalizePeerScope('same-phone'), 'same-phone');
});

test('peerScopeAllows fails closed on unknown scopes, tiers and junk', () => {
  for (const scope of [undefined, null, '', 'off', 'internet', 7, {}]) {
    for (const tier of ['t0', 't1', 't2', 0, 1, 2, 'junk', undefined]) {
      assert.equal(peerScopeAllows(scope, tier), false, `scope=${String(scope)} tier=${String(tier)} must be refused`);
    }
  }
  assert.equal(peerScopeAllows('SAME-DEVICE ', 't0'), true, 'case and space tolerant; same-device stays T0-only');
  assert.equal(peerScopeAllows('SAME-DEVICE ', 't1'), false);
  assert.equal(peerScopeAllows('SAME-LAN', 'T1'), true, 'case-insensitive inputs');
  assert.equal(peerScopeAllows('same-lan', 1), true, 'numeric tier inputs');
  assert.equal(peerScopeAllows('same-lan', 't3'), false);
  assert.equal(peerScopeAllows('same-lan', '3'), false);
  assert.equal(normalizePeerScope('junk'), '');
});

// ---------------------------------------------------------------- chunkPlan

test('chunkPlan plans 16 KiB ordered frames inside the cap', () => {
  assert.deepEqual(chunkPlan(1), { ok: true, reason: null, chunks: 1, chunkBytes: CHUNK_BYTES, totalBytes: 1, lastChunkBytes: 1 });
  assert.equal(chunkPlan(CHUNK_BYTES).chunks, 1, 'one exact chunk');
  assert.equal(chunkPlan(CHUNK_BYTES + 1).chunks, 2, 'one byte over one chunk');
  assert.equal(chunkPlan(CHUNK_BYTES + 1).lastChunkBytes, 1);
  assert.equal(chunkPlan(32 * MIB).chunks, 2048, 'a full 32 MiB artifact is 2048 frames');
  assert.equal(chunkPlan(32 * MIB).lastChunkBytes, CHUNK_BYTES, 'exact multiple ends on a full frame');
  assert.equal(chunkPlan(3 * MIB + 7).chunks, 193);
});

test('chunkPlan refuses above the cap and junk sizes', () => {
  assert.equal(chunkPlan(32 * MIB + 1).ok, false);
  assert.equal(chunkPlan(32 * MIB + 1).reason, 'size');
  assert.equal(chunkPlan(0).ok, false);
  assert.equal(chunkPlan(0).reason, 'size');
  assert.equal(chunkPlan(-5).ok, false);
  assert.equal(chunkPlan(1.5).ok, false, 'fractional bytes are junk');
  assert.equal(chunkPlan(undefined).ok, false);
  assert.equal(chunkPlan(NaN).ok, false);
  const custom = chunkPlan(1000, { chunkBytes: 300 });
  assert.equal(custom.chunks, 4, 'ceil(1000/300)');
  assert.equal(custom.lastChunkBytes, 100, '1000 - 3*300');
});

// ---------------------------------------------------------------- transferDeadline

test('transfer deadline: min(30 s, 10 s + 1 s per 4 MiB)', () => {
  assert.equal(transferDeadlineBudget(0), 10000, 'the base budget floors every attempt');
  assert.equal(transferDeadlineBudget(1), 11000);
  assert.equal(transferDeadlineBudget(4 * MIB), 11000);
  assert.equal(transferDeadlineBudget(8 * MIB), 12000);
  assert.equal(transferDeadlineBudget(32 * MIB), 18000);
  assert.equal(transferDeadlineBudget(96 * MIB), 30000, 'the 30 s cap binds');
  assert.equal(transferDeadlineBudget(undefined), 10000);
  assert.equal(transferDeadline(8 * MIB, 5000), 17000, 'absolute deadline = now + budget');
  assert.equal(transferDeadline(8 * MIB, undefined), 12000);
});

// ---------------------------------------------------------------- fallbackFSM

test('fallbackFSM: the happy path of design section 2.5 step 3', () => {
  let s = fallbackFSM('idle', 'offer');
  assert.deepEqual(s, { state: 'dialing', reason: null });
  s = fallbackFSM(s.state, 'open');
  assert.deepEqual(s, { state: 'open', reason: null });
  s = fallbackFSM(s.state, 'bytes');
  assert.equal(s.state, 'transferring');
  s = fallbackFSM(s.state, 'bytes');
  assert.equal(s.state, 'transferring', 'streaming frames stays');
  s = fallbackFSM(s.state, 'complete');
  assert.equal(s.state, 'verifying');
  s = fallbackFSM(s.state, 'verified');
  assert.deepEqual(s, { state: 'served', reason: null }, 'done(tier=1), no fallback');
  s = fallbackFSM(s.state, 'close');
  assert.deepEqual(s, { state: 'closed', reason: null });
});

test('fallbackFSM: every abort reason ends at fallback with its metric code', () => {
  for (const active of ['idle', 'dialing', 'open', 'transferring', 'verifying']) {
    for (const reason of P2P_FALLBACK_REASONS) {
      const out = fallbackFSM(active, 'abort', reason);
      assert.deepEqual(out, { state: 'fallback', reason }, `${reason} from ${active}`);
    }
  }
});

test('fallbackFSM: deadline aborts record the timeout metric reason', () => {
  assert.deepEqual(fallbackFSM('transferring', 'abort', 'deadline'), { state: 'fallback', reason: 'timeout' });
  assert.deepEqual(fallbackFSM('open', 'abort', 'deadline'), { state: 'fallback', reason: 'timeout' });
});

test('fallbackFSM: unknown reasons fail closed to channel, never an invented code', () => {
  assert.deepEqual(fallbackFSM('transferring', 'abort', 'junk'), { state: 'fallback', reason: 'channel' });
  assert.deepEqual(fallbackFSM('transferring', 'abort', undefined), { state: 'fallback', reason: 'channel' });
  assert.deepEqual(fallbackFSM('transferring', 'abort', 42), { state: 'fallback', reason: 'channel' });
});

test('fallbackFSM: abort after an ended attempt is a no-op', () => {
  assert.deepEqual(fallbackFSM('served', 'abort', 'stall'), { state: 'served', reason: null });
  assert.deepEqual(fallbackFSM('fallback', 'abort', 'stall'), { state: 'fallback', reason: null });
  assert.deepEqual(fallbackFSM('closed', 'abort', 'stall'), { state: 'closed', reason: null });
});

test('fallbackFSM: protocol junk in an active state fails closed to channel', () => {
  assert.deepEqual(fallbackFSM('open', 'complete'), { state: 'fallback', reason: 'channel' }, 'cannot skip streaming');
  assert.deepEqual(fallbackFSM('dialing', 'bytes'), { state: 'fallback', reason: 'channel' }, 'no bytes before open');
  assert.deepEqual(fallbackFSM('verifying', 'bytes'), { state: 'fallback', reason: 'channel' });
  assert.deepEqual(fallbackFSM('idle', 'open'), { state: 'fallback', reason: 'channel' }, 'no open without offer');
});

test('fallbackFSM: terminal states ignore junk; unknown states never resume', () => {
  assert.deepEqual(fallbackFSM('closed', 'bytes'), { state: 'closed', reason: null });
  assert.deepEqual(fallbackFSM('fallback', 'offer'), { state: 'fallback', reason: null });
  assert.deepEqual(fallbackFSM('garbage-state', 'open'), { state: 'fallback', reason: null });
  assert.deepEqual(fallbackFSM(undefined, 'abort', 'ice'), { state: 'fallback', reason: 'ice' });
});

// ---------------------------------------------------------------- serveAllowed

test('serveAllowed: charging, inside budget, not paused (D-W10-11 extends to serving)', () => {
  assert.equal(serveAllowed(true, 64 * MIB, false), true);
  assert.equal(serveAllowed(true, 1, false), true, 'one byte left is still inside the budget');
  assert.equal(serveAllowed(true, 64 * MIB, undefined), true, 'no pause flag is not a pause');
});

test('serveAllowed refuses every failing leg', () => {
  assert.equal(serveAllowed(false, 64 * MIB, false), false, 'on battery without serve_on_battery');
  assert.equal(serveAllowed(undefined, 64 * MIB, false), false);
  assert.equal(serveAllowed(1, 64 * MIB, false), false, 'strict boolean charging');
  assert.equal(serveAllowed(true, 0, false), false, 'budget exhausted');
  assert.equal(serveAllowed(true, -1, false), false);
  assert.equal(serveAllowed(true, NaN, false), false);
  assert.equal(serveAllowed(true, undefined, false), false);
  assert.equal(serveAllowed(true, 64 * MIB, true), false, 'claimsPaused stops serving');
  assert.equal(serveAllowed(true, 64 * MIB, 1), false);
});

// ---------------------------------------------------------------- serveBudgetTick

test('serveBudgetTick: fresh window holds the full 64 MiB budget', () => {
  const t = serveBudgetTick({}, { nowMs: 1000 });
  assert.deepEqual(t, { windowStartMs: 1000, usedBytes: 0, budgetBytes: 64 * MIB, remainingBytes: 64 * MIB, resetsInMs: 60000 });
});

test('serveBudgetTick: inside the window the used bytes shrink the remainder', () => {
  const t = serveBudgetTick({ usedBytes: 10 * MIB, windowStartMs: 1000 }, { nowMs: 30000 });
  assert.equal(t.remainingBytes, 54 * MIB);
  assert.equal(t.resetsInMs, 31000);
});

test('serveBudgetTick: a window older than one minute resets to zero used', () => {
  const t = serveBudgetTick({ usedBytes: 63 * MIB, windowStartMs: 1000 }, { nowMs: 1000 + 60000 });
  assert.equal(t.usedBytes, 0);
  assert.equal(t.windowStartMs, 61000);
  assert.equal(t.remainingBytes, 64 * MIB);
  assert.equal(t.resetsInMs, 60000);
});

test('serveBudgetTick: the pool knob bounds the budget; junk state resets', () => {
  assert.equal(serveBudgetTick({}, { nowMs: 0, budgetMib: 32 }).budgetBytes, 32 * MIB);
  assert.equal(serveBudgetTick({ usedBytes: -50, windowStartMs: 0 }, { nowMs: 10 }).usedBytes, 0, 'negative used clamps');
  assert.equal(serveBudgetTick({ usedBytes: 'junk', windowStartMs: 'junk' }, { nowMs: 10 }).remainingBytes, 64 * MIB);
  assert.equal(serveBudgetTick().remainingBytes, 64 * MIB, 'no arguments is a fresh window');
});

// ---------------------------------------------------------------- turnAllowed

test('turnAllowed: TURN is removed by the F11 admin rule', () => {
  assert.equal(turnAllowed({
    enabled: true,
    urls: ['turn:turn.example.org:3478?transport=udp', 'turns:turn.example.org:5349?transport=tcp'],
    credentialRef: 'pools/turn/shared-secret'
  }), false);
});

test('turnAllowed fails closed on every missing or malformed leg (D-W10-14)', () => {
  const good = { enabled: true, urls: ['turn:turn.example.org:3478'], credentialRef: 'ref' };
  assert.equal(turnAllowed(null), false);
  assert.equal(turnAllowed(undefined), false);
  assert.equal(turnAllowed('turn:turn.example.org'), false);
  assert.equal(turnAllowed({ ...good, enabled: false }), false, 'TURN stays optional, default off');
  assert.equal(turnAllowed({ ...good, enabled: 'on' }), false, 'the caller converts the schema toggle');
  assert.equal(turnAllowed({ ...good, urls: [] }), false);
  assert.equal(turnAllowed({ ...good, urls: undefined }), false);
  assert.equal(turnAllowed({ ...good, urls: 'turn:turn.example.org' }), false, 'urls is a list');
  assert.equal(turnAllowed({ ...good, urls: ['http://turn.example.org'] }), false, 'no plain http relay');
  assert.equal(turnAllowed({ ...good, urls: ['turn:turn a.example.org:3478'] }), false, 'no whitespace');
  assert.equal(turnAllowed({ ...good, urls: ['turn:turn.example.org:3478,turn:other:1'] }), false, 'a comma would split the list');
  assert.equal(turnAllowed({ ...good, urls: ['turn:turn.example.org:3478', 'bogus'] }), false, 'one bad URL refuses the config');
  assert.equal(turnAllowed({ enabled: true, urls: ['turn:turn.example.org:3478'] }), false, 'no credential ref');
  assert.equal(turnAllowed({ ...good, credentialRef: '   ' }), false);
  assert.equal(turnAllowed({ ...good, credentialRef: 7 }), false);
});

// ---------------------------------------------------------------- CR-53 boundary

test('boundary: nothing under platform/p2p imports or names web/workloads (CR-53)', () => {
  const dir = path.join(here, '..', 'js', 'platform', 'p2p');
  const files = fs.readdirSync(dir).filter(f => f.endsWith('.js'));
  assert.ok(files.includes('rules.js') && files.includes('cache.js'), 'the P2P platform files are present');
  for (const file of files) {
    const src = fs.readFileSync(path.join(dir, file), 'utf8');
    assert.doesNotMatch(src, /workload/i, `${file} must not name web/workloads`);
  }
});

test('boundary: platform/p2p imports only from inside its own directory (CR-53)', () => {
  const dir = path.join(here, '..', 'js', 'platform', 'p2p');
  for (const file of fs.readdirSync(dir).filter(f => f.endsWith('.js'))) {
    const src = fs.readFileSync(path.join(dir, file), 'utf8');
    const specs = [...src.matchAll(/^import\s+[^'"]*['"]([^'"]+)['"];?$/gm)].map(m => m[1]);
    for (const spec of specs) {
      assert.ok(spec.startsWith('./') || spec.startsWith('../'),
        `${file} imports ${spec}: platform/p2p stays self-contained`);
      assert.ok(!spec.includes('..', 2) || spec === '../rules.js' || path.dirname(spec) !== '..',
        `${file} import ${spec} must not climb out of platform/p2p`);
      assert.doesNotMatch(spec, /workload/i, `${file} imports ${spec}`);
    }
  }
});
