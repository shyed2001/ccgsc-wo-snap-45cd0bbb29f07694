// F11, 2026-11-25: red->green tests for the pools-core signaling client.
// RED on base: web/pages/js/platform/p2p/signaling.js does not exist.
import test from 'node:test';
import assert from 'node:assert/strict';
import { createSignalClient, validateEnvelope } from '../js/platform/p2p/signaling.js';

const okEnvelope = () => ({ v: 1, type: 'offer', callId: 'call-abcdef12', sdp: 'v=0...', digest: 'a'.repeat(64) });

function client(fake, { app = 'app-1', origin = 'https://pools.test' } = {}) {
  return createSignalClient({
    origin, fetch: fake,
    token: () => 'lease-token',
    myApp: () => app,
    peerId: () => 'peer-aaaaaaaa'
  });
}

test('requires a bare https origin', () => {
  for (const origin of ['http://pools.test', 'https://pools.test/x', 'https://u:p@pools.test',
    'https://pools.test/?a=1', 'not a url']) {
    assert.throws(() => client(() => { throw new Error('no fetch'); }, { origin }), /origin|URL|https/i, origin);
  }
});

test('validateEnvelope enforces the server caps client-side too', () => {
  assert.equal(validateEnvelope(okEnvelope()).digest, 'a'.repeat(64));
  assert.throws(() => validateEnvelope({ ...okEnvelope(), v: 2 }), /envelope/);
  assert.throws(() => validateEnvelope({ ...okEnvelope(), type: 'turn' }), /envelope/);
  assert.throws(() => validateEnvelope({ ...okEnvelope(), callId: 'short' }), /envelope/);
  assert.throws(() => validateEnvelope({ ...okEnvelope(), sdp: 'x'.repeat(16 * 1024 + 1) }), /envelope/);
  assert.throws(() => validateEnvelope({ v: 1, type: 'offer', callId: 'call-abcdef12', sdp: 'v=0' }), /envelope/);
  assert.throws(() => validateEnvelope({ ...okEnvelope(), digest: '../secret' }), /envelope/);
  assert.doesNotThrow(() => validateEnvelope({ v: 1, type: 'bye', callId: 'call-abcdef12' }));
});

test('bad envelopes are refused before any network byte moves', async () => {
  let called = 0;
  const c = client(async () => { called++; return new Response('{}'); });
  await assert.rejects(c.send('tp', 'peer-bbbbbbbb', { ...okEnvelope(), callId: 'no' }), /envelope/);
  await assert.rejects(c.send('zz', 'peer-bbbbbbbb', okEnvelope()), /pool/);
  await assert.rejects(c.send('tp', 'not a peer', okEnvelope()), /peer/);
  assert.equal(called, 0);
});

test('register/send/poll carry the bearer token, omit credentials, hit the right paths', async t => {
  const seen = [];
  const c = client(async (url, init) => {
    seen.push({ url: String(url), init });
    return new Response('{"sent":true,"cursor":0,"messages":[],"ttlMs":90000}', { status: 200 });
  });
  await c.register('tp');
  await c.send('tp', 'peer-bbbbbbbb', okEnvelope());
  await c.poll('tp', 4);
  assert.equal(seen.length, 3);
  assert.equal(seen[0].url, 'https://pools.test/thread-pool/v1/p2p/register');
  assert.equal(seen[1].url, 'https://pools.test/thread-pool/v1/p2p/signal');
  assert.equal(seen[2].url, 'https://pools.test/thread-pool/v1/p2p/signal?after=4&peer=peer-aaaaaaaa');
  for (const s of seen) {
    assert.equal(s.init.headers.Authorization, 'Bearer lease-token');
    assert.equal(s.init.credentials, 'omit');
    assert.equal(s.init.redirect, 'error');
  }
  const body = JSON.parse(seen[1].init.body);
  assert.equal(body.from, 'peer-aaaaaaaa');
  assert.equal(body.to, 'peer-bbbbbbbb');
  assert.equal(body.envelope.callId, 'call-abcdef12');
});

test('process pool uses its own prefix', async () => {
  const seen = [];
  const c = client(async url => { seen.push(String(url)); return new Response('{}'); });
  await c.discover('pp');
  assert.equal(seen[0], 'https://pools.test/process-pool/v1/p2p/peers');
});

test('discover sanitizes ICE and drops cross-app peers before dialing', async () => {
  const c = client(async () => new Response(JSON.stringify({
    mode: 'stun',
    iceServers: [
      { urls: 'stun:stun.corp.internal:3478', credential: 'must-not-travel' },
      { urls: ['turn:turn.corp.internal'] },
      { urls: 'stun:stun.l.google.com:19302' }
    ],
    peers: [
      { peerId: 'peer-sameaaa1', appId: 'app-1', mode: 'stun' },
      { peerId: 'peer-otheraa2', appId: 'app-2', mode: 'stun' },
      { peerId: 'peer-offpeer3', appId: 'app-1', mode: 'off' },
      { peerId: '../bad', appId: 'app-1', mode: 'stun' },
      { peerId: 'peer-leasebad', appId: 'app-1', mode: 'stun', leaseId: 'leak', workerId: 'w' }
    ]
  }), { status: 200 }));
  const d = await c.discover('tp');
  assert.equal(d.mode, 'stun');
  assert.deepEqual(d.iceServers, [{ urls: ['stun:stun.corp.internal:3478'] }]);
  // Same-app peers pass with only peerId/appId/mode: lease and worker ids never travel.
  assert.deepEqual(d.peers, [
    { peerId: 'peer-sameaaa1', appId: 'app-1', mode: 'stun' },
    { peerId: 'peer-leasebad', appId: 'app-1', mode: 'stun' }
  ]);
  for (const peer of d.peers) {
    assert.equal('leaseId' in peer, false);
    assert.equal('workerId' in peer, false);
  }
});

test('discover in lan mode yields an empty iceServers list (no STUN)', async () => {
  const c = client(async () => new Response(JSON.stringify({
    mode: 'lan',
    iceServers: [{ urls: 'stun:stun.corp.internal:3478' }],
    peers: []
  }), { status: 200 }));
  const d = await c.discover('tp');
  assert.deepEqual(d.iceServers, []);
  assert.deepEqual(d.peers, []);
});

test('discover with no app lock returns no peers (fail closed)', async () => {
  const c = client(async () => new Response(JSON.stringify({
    mode: 'lan', iceServers: [], peers: [{ peerId: 'peer-sameaaa1', appId: 'app-1', mode: 'lan' }]
  }), { status: 200 }), { app: '' });
  const d = await c.discover('tp');
  assert.deepEqual(d.peers, []);
});

test('poll drops foreign-app and malformed messages without surfacing them', async () => {
  const c = client(async () => new Response(JSON.stringify({
    cursor: 9,
    messages: [
      { id: 5, fromPeer: 'peer-sameaaa1', fromApp: 'app-1', envelope: okEnvelope() },
      { id: 6, fromPeer: 'peer-otheraa2', fromApp: 'app-2', envelope: okEnvelope() },
      { id: 7, fromPeer: 'peer-sameaaa1', fromApp: 'app-1', envelope: { v: 1, type: 'evil', callId: 'call-abcdef12' } },
      { id: 8, fromPeer: 'peer-ok', fromApp: 'app-1', envelope: okEnvelope() }
    ]
  }), { status: 200 }));
  const polled = await c.poll('tp', 0);
  assert.equal(polled.cursor, 9);
  assert.equal(polled.messages.length, 1);
  assert.equal(polled.dropped, 3);
  assert.equal(polled.messages[0].fromApp, 'app-1');
});

test('server refusals surface as errors; leave is best effort', async () => {
  const c = client(async (url, init) => {
    if (String(url).endsWith('/p2p/signal') && init.method === 'POST') {
      return new Response('{"error":"cross-app"}', { status: 403 });
    }
    throw new Error('network down');
  });
  await assert.rejects(c.send('tp', 'peer-bbbbbbbb', okEnvelope()), /cross-app/);
  await assert.doesNotReject(c.leave('tp'));
});

test('server-managed mode rows keep results and receipts off the peer path', () => {
  for (const kind of ['result', 'receipt', 'input']) {
    assert.throws(() => validateEnvelope({ v: 1, type: 'offer', callId: 'call-abcdef12', sdp: 'v=0', kind }), /envelope/);
  }
});
