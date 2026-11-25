// F11, 2026-11-24: isolated transport contract; fake grants are NOT server auth evidence.
import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { createArtifactPeerClient, receiveArtifact, LIMITS } from '../js/platform/p2p/transport.js';

const bytes = new Uint8Array(40000).map((_, i) => i % 251);
const sha = b => createHash('sha256').update(b).digest('hex');
const scope = (extra = {}) => ({ poolId: 'tp', ownerId: 'owner', appId: 'app',
  artifactId: sha(bytes), workerId: 'worker', leaseId: 'lease',
  kind: 'module', expiresAt: Date.now() + 60000, ...extra });
class Channel extends EventTarget {
  readyState = 'open'; ordered = true; maxRetransmits = null; maxPacketLifeTime = null;
  bufferedAmount = 0; sent = [];
  send(data) { this.sent.push(data); queueMicrotask(() => {
    if (this.other?.readyState === 'open') this.other.dispatchEvent(new MessageEvent('message', { data }));
  }); }
  close() { if (this.readyState === 'closed') return; this.readyState = 'closed';
    this.dispatchEvent(new Event('close')); if (this.other) { const o = this.other; this.other = null; o.close(); } }
}
function pair() { const a = new Channel(), b = new Channel(); a.other = b; b.other = a; return [a, b]; }
function fixture(options = {}) {
  const calls = [];
  const client = createArtifactPeerClient({ origin: 'https://pools.test',
    authorize: async () => true, consent: () => true, ...options,
    fetch: options.fetch || (async (url, init) => { calls.push({ url, init }); return new Response(bytes,
      { headers: { 'Content-Type': 'application/octet-stream', 'X-Artifact-Sha256': sha(bytes) } }); }) });
  return { client, calls };
}
test('default OFF uses authenticated, bounded HTTP; no peer dial', async () => {
  const { client, calls } = fixture();
  const r = await client.get(scope(), { token: 'test', openPeer: () => { throw Error('must not dial'); } });
  assert.equal(r.source, 'server'); assert.deepEqual(r.bytes, bytes);
  assert.equal(calls[0].init.headers.Authorization, 'Bearer test');
  assert.equal(calls[0].init.redirect, 'error'); assert.equal(calls[0].init.credentials, 'omit');
  assert.equal(calls[0].url, 'https://pools.test/thread-pool/v1/artifacts/' + sha(bytes));
});
test('server-ingested module transfers in bounded chunks; peer data cannot be re-served', async () => {
  const sender = fixture(), receiver = fixture({ enabled: true }); const s = scope();
  await sender.client.get(s, { token: 'test' });
  // Serving requires explicit opt-in too.
  const serving = fixture({ enabled: true }); await serving.client.get(s, { token: 'test' });
  let served; let channels;
  const r = await receiver.client.get(s, { token: 'test', openPeer: async () => {
    channels = pair(); served = serving.client.serve(s, channels[1]); return channels[0];
  } });
  assert.equal(r.source, 'peer'); assert.deepEqual(r.bytes, bytes); await served;
  assert.equal(receiver.calls.length, 0);
  const binary = channels[1].sent.filter(x => x instanceof ArrayBuffer);
  assert.equal(binary.length, 3); assert.ok(binary.every(b => b.byteLength <= LIMITS.chunkBytes));
  const [a, b] = pair(); await assert.rejects(receiver.client.serve(s, b), /cache-miss/); a.close();
});
for (const mode of ['corrupt', 'oversize', 'timeout', 'drop', 'head-unavailable', 'bad-frame']) {
  test(mode + ' falls back once to independently hashed HTTP', async () => {
    const { client, calls } = fixture({ enabled: true, peerTimeoutMs: 30 });
    const r = await client.get(scope(), { token: 'test', openPeer: async () => {
      if (mode === 'head-unavailable') throw Error(mode);
      const [a, b] = pair();
      b.addEventListener('message', () => {
        if (mode === 'timeout') return;
        if (mode === 'drop') { b.close(); return; }
        if (mode === 'bad-frame') { b.send('not-json'); return; }
        b.send(JSON.stringify({ v: 1, type: 'artifact', sha256: sha(bytes),
          size: mode === 'oversize' ? LIMITS.maxBytes + 1 : 1 }));
        b.send(new Uint8Array([0]).buffer); b.send(JSON.stringify({ v: 1, type: 'end' }));
      }, { once: true }); return a;
    } });
    assert.equal(r.source, 'server'); assert.deepEqual(r.bytes, bytes); assert.equal(calls.length, 1);
    assert.ok(r.fallbackReason);
  });
}
test('corrupt HTTP, false digest header, MIME and streamed oversize fail closed', async () => {
  for (const response of [new Response('wrong'),
    new Response(bytes, { headers: { 'X-Artifact-Sha256': '0'.repeat(64) } }),
    new Response(bytes, { headers: { 'Content-Type': 'text/html' } }),
    new Response(new Uint8Array(LIMITS.maxBytes + 1), { headers: { 'Content-Type': 'application/octet-stream' } })]) {
    const { client } = fixture({ fetch: async () => response });
    await assert.rejects(client.get(scope(), { token: 'test' }));
  }
});
test('no permission adapter, denied grant, expired lease or non-module means no network', async () => {
  for (const options of [{ authorize: undefined }, { authorize: async () => false }]) {
    const { client, calls } = fixture(options); await assert.rejects(client.get(scope(), { token: 'test' }));
    assert.equal(calls.length, 0);
  }
  const { client, calls } = fixture();
  for (const extra of [{ expiresAt: 0 }, { kind: 'result' }, { kind: 'receipt' }, { kind: 'input' },
    { ownerId: '' }, { poolId: '__proto__' }, { artifactId: '../secret' }]) {
    await assert.rejects(client.get(scope(extra), { token: 'test' }));
  }
  assert.equal(calls.length, 0);
});
test('server provenance is isolated by owner, app and pool; serving reauthorizes worker/lease', async () => {
  const seen = []; const { client } = fixture({ enabled: true, authorize: async (s, action) => {
    seen.push([s.workerId, s.leaseId, action]); return s.workerId !== 'denied';
  } });
  const s = scope(); await client.get(s, { token: 'test' });
  for (const extra of [{ ownerId: 'other' }, { appId: 'other' }, { poolId: 'pp' }, { workerId: 'denied' }]) {
    const [a,b] = pair(); await assert.rejects(client.serve({ ...s, ...extra }, b)); assert.equal(b.sent.length, 0); a.close();
  }
  assert.ok(seen.some(x => x[0] === 'denied' && x[2] === 'serve'));
});
test('consent off prevents dial and serve; HTTP remains supported', async () => {
  const { client } = fixture({ enabled: true, consent: () => false });
  const r = await client.get(scope(), { token: 'test', openPeer: () => assert.fail('dial') });
  assert.equal(r.source, 'server'); const [a,b] = pair();
  await assert.rejects(client.serve(scope(), b), /disabled/); a.close();
});
test('abort closes peer and suppresses HTTP fallback; hanging HTTP has deadline', async () => {
  const ac = new AbortController(); const { client, calls } = fixture({ enabled: true });
  const pending = client.get(scope(), { token: 'test', signal: ac.signal, openPeer: () => new Promise(() => {}) });
  ac.abort(); await assert.rejects(pending); assert.equal(calls.length, 0);
  const slow = fixture({ httpTimeoutMs: 20, fetch: () => new Promise(() => {}) });
  await assert.rejects(slow.client.get(scope(), { token: 'test' }), /timeout/);
});
test('receiver rejects unordered/unreliable channels and pre-header binary', async () => {
  const [a,b] = pair(); a.ordered = false;
  await assert.rejects(receiveArtifact(a, sha(bytes)), /channel/); b.close();
  const [c,d] = pair(); const pending = receiveArtifact(c, sha(bytes)); d.send(new Uint8Array([1]).buffer);
  await assert.rejects(pending, /protocol/); d.close();
});
