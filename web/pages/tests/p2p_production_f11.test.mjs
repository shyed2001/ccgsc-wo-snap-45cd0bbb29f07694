// F11, 2026-11-25: red->green end-to-end tests for production P2P.
// RED on base: web/pages/js/platform/p2p/production.js does not exist.
//
// Two production instances (two "tabs") + an in-memory fake of the pools-core
// signaling contract + a loopback RTCPeerConnection bus. The fake server
// enforces the same rules the Go implementation must: lease-token auth,
// same-app-only relay and discovery, admin mode + ICE configuration. The
// client must additionally enforce them (defense in depth).
//
// No real network, no public STUN, no TURN, no SharedArrayBuffer.
import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { createProductionP2p } from '../js/platform/p2p/production.js';

const sha256 = bytes => createHash('sha256').update(bytes).digest('hex');
const ARTIFACT = new Uint8Array(48 * 1024).map((_, i) => i % 251);
const DIGEST = sha256(ARTIFACT);
const OTHER = new Uint8Array(4096).map((_, i) => 255 - i % 251);
const OTHER_DIGEST = sha256(OTHER);

// ---------------------------------------------------------------- loopback RTC

class FakeChannel extends EventTarget {
  constructor(label) {
    super();
    this.label = label;
    this.readyState = 'connecting';
    this.ordered = true;
    this.maxRetransmits = null;
    this.maxPacketLifeTime = null;
    this.bufferedAmount = 0;
    this.binaryType = 'arraybuffer';
    this._peer = null;
    this._tamper = null;
    this._pending = [];
  }
  send(data) {
    if (this.readyState !== 'open') throw new Error('closed channel');
    const deliver = out => {
      if (!out || out.readyState !== 'open') return false;
      let payload = data;
      if (this._tamper && data instanceof ArrayBuffer) {
        payload = this._tamper(data);
        this._tamper = null;
      }
      out.dispatchEvent(new MessageEvent('message', { data: payload }));
      return true;
    };
    // Macrotask dispatch: mirrors real SCTP asynchrony so both sides have
    // their listeners attached before the first frame moves.
    this._pending.push(deliver);
    setTimeout(() => {
      const at = this._pending.indexOf(deliver);
      if (at >= 0) { this._pending.splice(at, 1); deliver(this._peer); }
    }, 0);
  }
  open() {
    if (this.readyState === 'open') return;
    this.readyState = 'open';
    this.dispatchEvent(new Event('open'));
  }
  close() {
    if (this.readyState === 'closed') return;
    // Flush in-flight frames first: a real browser delivers data handed to
    // send() immediately before close (SCTP flush), so the peer's final ack
    // is not lost.
    const pending = this._pending.splice(0);
    const peer = this._peer;
    this._peer = null;
    this.readyState = 'closed';
    for (const deliver of pending) deliver(peer);
    this.dispatchEvent(new Event('close'));
    if (peer) { peer._pending.splice(0); peer.close(); }
  }
}

class RTCBus {
  constructor() { this.pending = new Map(); this.serial = 0; }
  link(pc) {
    const id = 'link-' + (++this.serial);
    this.pending.set(id, pc._outbound);
    return { id };
  }
  connect(answererPC, sdpObject) {
    if (!sdpObject || !sdpObject.links) return;
    const offererChannel = this.pending.get(sdpObject.links.id);
    if (!offererChannel) return;
    this.pending.delete(sdpObject.links.id);
    const inbound = new FakeChannel('p2p-artifact');
    if (this.tamper) inbound._tamper = this.tamper;
    inbound._peer = offererChannel;
    offererChannel._peer = inbound;
    queueMicrotask(() => {
      if (typeof answererPC.ondatachannel === 'function') {
        answererPC.ondatachannel({ channel: inbound });
      }
    });
    setTimeout(() => { inbound.open(); offererChannel.open(); }, 1);
  }
}

class FakePC {
  constructor(config, bus) {
    this.config = config;
    this._bus = bus;
    this._outbound = null;
    this.iceGatheringState = 'new';
    this.localDescription = null;
    this.ondatachannel = null;
    this.onicegatheringstatechange = null;
    this._inbound = [];
  }
  createDataChannel(label, init) {
    const channel = new FakeChannel(label);
    this._outbound = channel;
    return channel;
  }
  async createOffer() {
    return { type: 'offer', sdp: JSON.stringify({ v: 1, ice: this.config, links: this._bus.link(this) }) };
  }
  async createAnswer() {
    return { type: 'answer', sdp: JSON.stringify({ v: 1, ice: this.config, links: { id: 'answer-noop' } }) };
  }
  async setLocalDescription(description) {
    this.localDescription = description;
    this.iceGatheringState = 'complete';
    this.onicegatheringstatechange && this.onicegatheringstatechange();
  }
  async setRemoteDescription(description) {
    if (description.type === 'offer') this._bus.connect(this, JSON.parse(description.sdp));
    this.iceGatheringState = 'complete';
  }
  close() {
    if (this._outbound) this._outbound.close();
    for (const channel of this._inbound) channel.close();
  }
}

// ------------------------------------------------------- fake pools-core server

function fakePoolsServer({ mode = 'lan', stunUrls = [], leakCrossApp = false, tamper = null } = {}) {
  const tokens = new Map();      // token -> {appId, ownerId}
  const registrations = new Map(); // peerId -> {appId, token, pool, queue: []}
  const requests = [];
  let sequence = 0;
  const bus = new RTCBus();
  bus.tamper = tamper;
  const ice = mode === 'stun'
    ? [{ urls: [...stunUrls, 'turn:turn.corp.internal:3478?transport=tcp'] }]  // server bug simulation: client must strip
    : [];

  function auth(init) {
    const header = init && init.headers && init.headers.Authorization;
    if (typeof header !== 'string' || !header.startsWith('Bearer ')) return null;
    return tokens.get(header.slice(7)) || null;
  }

  async function fetch(url, init = {}) {
    const target = new URL(String(url));
    requests.push({ path: target.pathname + target.search, method: init.method || 'GET' });
    const me = auth(init);
    if (!me) return Response.json({ error: 'unauthorized' }, { status: 401 });
    const poolPrefix = /\/(thread-pool|process-pool)\/v1\/p2p\//.exec(target.pathname);
    const pool = poolPrefix ? (poolPrefix[1] === 'thread-pool' ? 'tp' : 'pp') : null;
    const action = poolPrefix ? target.pathname.split('/v1/p2p/')[1] : null;
    if (!pool || ['register', 'leave', 'peers', 'signal'].indexOf(action) < 0) {
      return Response.json({ error: 'notfound' }, { status: 404 });
    }
    const body = init.body ? JSON.parse(String(init.body)) : {};

    if (action === 'register' && init.method === 'POST') {
      registrations.set(body.peerId, { appId: me.appId, token: me.token, pool, queue: [] });
      return Response.json({ ttlMs: 90000 });
    }
    if (action === 'leave' && init.method === 'POST') {
      const reg = registrations.get(body.peerId);
      if (reg && reg.token === me.token) registrations.delete(body.peerId);
      return Response.json({ left: true });
    }
    if (action === 'peers') {
      const peers = [];
      for (const [peerId, reg] of registrations) {
        if (reg.pool !== pool) continue;
        if (reg.appId !== me.appId && !leakCrossApp) continue;
        peers.push({ peerId, appId: reg.appId, mode });
      }
      return Response.json({ mode, iceServers: ice, peers });
    }
    if (action === 'signal' && init.method === 'POST') {
      const targetReg = registrations.get(body.to);
      const fromReg = registrations.get(body.from);
      if (!targetReg) return Response.json({ error: 'no-peer' }, { status: 404 });
      if (!fromReg || fromReg.token !== me.token) return Response.json({ error: 'unknown-peer' }, { status: 403 });
      if (targetReg.appId !== fromReg.appId) return Response.json({ error: 'cross-app' }, { status: 403 });
      targetReg.queue.push({
        id: ++sequence, fromPeer: body.from, fromApp: fromReg.appId, envelope: body.envelope
      });
      return Response.json({ sent: true });
    }
    if (action === 'signal') {
      const after = Number(target.searchParams.get('after')) || 0;
      const mine = registrations.get(target.searchParams.get('peer'));
      if (!mine || mine.token !== me.token) return Response.json({ error: 'unknown-peer' }, { status: 403 });
      const messages = mine.queue.filter(m => m.id > after);
      mine.queue = mine.queue.filter(m => m.id <= after);
      return Response.json({ cursor: sequence, messages });
    }
    return Response.json({ error: 'notfound' }, { status: 404 });
  }

  return {
    fetch, requests, bus,
    addToken(token, appId, ownerId) { tokens.set(token, { appId, ownerId, token }); },
    registrations
  };
}

// ---------------------------------------------------------------- fixtures

function makeTab(server, { appId, ownerId = 'owner-1', poolId = 'tp', enabled = true,
  battery = () => ({ charging: true }), fetchArtifact = dynamicArtifact, pollIntervalMs = 15 } = {}) {
  const token = 'tok-' + appId + '-' + Math.random().toString(36).slice(2, 8);
  server.addToken(token, appId, ownerId);
  let artifactCalls = 0;
  const instance = createProductionP2p({
    poolId,
    origin: 'https://pools.test',
    fetch: async (url, init) => {
      const path = new URL(String(url)).pathname;
      if (path.includes('/artifacts/')) {
        if (!fetchArtifact) throw new Error('no artifact route in test');
        artifactCalls++;
        return fetchArtifact(String(url), init);
      }
      return server.fetch(url, init);
    },
    token: () => token,
    identity: () => ({ appId, ownerId, workerId: 'w-' + appId, leaseId: 'lease-' + appId }),
    enabled: () => enabled,
    battery,
    rtc: config => new FakePC(config, server.bus),
    peerId: () => 'peer-' + appId + '-' + Math.random().toString(36).slice(2, 10),
    pollIntervalMs,
    serveBudgetMib: 8
  });
  return { instance, artifactCalls: () => artifactCalls, token, stop: () => instance.stop() };
}

// Default server artifact route: echoes the digest the transport requested,
// exactly like the pools-core artifact endpoint (X-Artifact-Sha256 header).
function dynamicArtifact(url) {
  const digest = String(url).split('/artifacts/')[1].split('?')[0];
  return new Response(ARTIFACT.slice(), {
    status: 200,
    headers: { 'Content-Type': 'application/octet-stream', 'X-Artifact-Sha256': digest }
  });
}

const artifactResponse = digest => new Response(ARTIFACT.slice(), {
  status: 200,
  headers: { 'Content-Type': 'application/octet-stream', 'X-Artifact-Sha256': digest }
});

async function withTabs(tabs, body) {
  try {
    return await body();
  } finally {
    for (const tab of tabs) tab.stop();
  }
}

// ---------------------------------------------------------------- tests

test('G-F11-1/2: same-app LAN peer serves the artifact with zero HTTP fetches', async () => {
  const server = fakePoolsServer({ mode: 'lan' });
  const donor = makeTab(server, { appId: 'app-1' });
  const requester = makeTab(server, {
    appId: 'app-1',
    fetchArtifact: () => { throw new Error('HTTP fetch must not happen on a peer hit'); }
  });
  await withTabs([donor, requester], async () => {
    const acquired = await donor.instance.get(DIGEST);
    assert.equal(acquired.source, 'server');                        // donor seeds via HTTP
    assert.ok(donor.artifactCalls() >= 1);
    const result = await requester.instance.get(DIGEST);
    assert.equal(result.source, 'peer');                            // served by the same-app peer
    assert.equal(sha256(result.bytes), DIGEST);                     // SHA-256 checked
    assert.equal(requester.artifactCalls(), 0);                     // zero HTTP fetches
    assert.deepEqual(requester.instance.lastPCConfig().iceServers, []); // LAN-only: no STUN
    await new Promise(resolve => setTimeout(resolve, 50));  // donor's serve resolves after the ack
    assert.ok(donor.instance.stats().served >= 1);                  // donor actually served
    assert.ok(donor.instance.stats().budget.usedBytes >= ARTIFACT.length);
  });
});

test('stun mode dials with only the sanitized self-hosted STUN entry', async () => {
  const server = fakePoolsServer({ mode: 'stun', stunUrls: ['stun:stun.corp.internal:3478'] });
  const donor = makeTab(server, { appId: 'app-1' });
  const requester = makeTab(server, { appId: 'app-1' });
  await withTabs([donor, requester], async () => {
    await donor.instance.get(DIGEST);
    const result = await requester.instance.get(DIGEST);
    assert.equal(result.source, 'peer');
    const ice = requester.instance.lastPCConfig().iceServers;
    assert.equal(ice.length, 1);
    assert.deepEqual(ice[0].urls, ['stun:stun.corp.internal:3478']); // turn: entry stripped
  });
});

test('peer bytes that fail SHA-256 fall back to hash-verified HTTP once', async () => {
  const server = fakePoolsServer({ mode: 'lan', tamper: data => {
    const bad = new Uint8Array(data.slice());
    bad[0] ^= 0xff;
    return bad.buffer;
  } });
  const donor = makeTab(server, { appId: 'app-1' });
  const requester = makeTab(server, { appId: 'app-1', fetchArtifact: () => artifactResponse(DIGEST) });
  await withTabs([donor, requester], async () => {
    await donor.instance.get(DIGEST);
    const result = await requester.instance.get(DIGEST);
    assert.equal(result.source, 'server');                 // corrupt peer bytes refused
    assert.equal(sha256(result.bytes), DIGEST);            // HTTP bytes verified
    assert.equal(requester.artifactCalls(), 1);            // exactly one fallback fetch
    assert.equal(requester.instance.lastFallbackReason(), 'hash');
  });
});

test('a not-serving peer (battery gate) is declined; requester uses HTTP fallback', async () => {
  const server = fakePoolsServer({ mode: 'lan' });
  const donor = makeTab(server, { appId: 'app-1', battery: () => ({ charging: false }) });
  const requester = makeTab(server, { appId: 'app-1' });
  await withTabs([donor, requester], async () => {
    await donor.instance.get(DIGEST);
    const result = await requester.instance.get(DIGEST);
    assert.equal(result.source, 'server');
    assert.equal(requester.artifactCalls(), 1);
    assert.equal(donor.instance.stats().served, 0);
    assert.equal(requester.instance.lastFallbackReason(), 'no-peer');
  });
});

test('cross-app peers are never dialed even when discovery leaks them', async () => {
  const server = fakePoolsServer({ mode: 'lan', leakCrossApp: true });
  const other = makeTab(server, {
    appId: 'app-2',
    fetchArtifact: () => new Response(OTHER.slice(), {
      status: 200,
      headers: { 'Content-Type': 'application/octet-stream', 'X-Artifact-Sha256': OTHER_DIGEST }
    })
  });
  const donor = makeTab(server, { appId: 'app-1', fetchArtifact: null }); // registered, has nothing
  const requester = makeTab(server, { appId: 'app-1' });
  await withTabs([other, donor, requester], async () => {
    await other.instance.get(OTHER_DIGEST);                        // other app seeds its own artifact
    await assert.rejects(donor.instance.get(DIGEST), /artifact/);  // registers but stays empty-handed
    const result = await requester.instance.get(DIGEST);
    assert.equal(result.source, 'server');                          // same-app donor empty -> HTTP
    assert.equal(requester.artifactCalls(), 1);
    for (const reg of server.registrations.values()) {
      if (reg.appId === 'app-2') assert.equal(reg.queue.length, 0); // no offer ever reached app-2
    }
  });
});

test('admin mode off: the client never touches the signaling plane', async () => {
  const server = fakePoolsServer({ mode: 'off' });
  const tab = makeTab(server, { appId: 'app-1', enabled: false });
  await withTabs([tab], async () => {
    await assert.rejects(tab.instance.get(DIGEST), /disabled/);
    assert.equal(tab.artifactCalls(), 0);
    assert.equal(server.requests.filter(r => r.path.includes('/p2p/')).length, 0);
  });
});

test('server mode off with consent on: discovery happens, no dial, HTTP fallback', async () => {
  const server = fakePoolsServer({ mode: 'off' });
  const tab = makeTab(server, { appId: 'app-1' });
  await withTabs([tab], async () => {
    const result = await tab.instance.get(DIGEST);
    assert.equal(result.source, 'server');
    assert.equal(tab.artifactCalls(), 1);
    assert.equal(tab.instance.lastPCConfig(), null);
  });
});

test('get() is fail-closed on bad digests and unlocked tabs', async () => {
  const server = fakePoolsServer({ mode: 'lan' });
  const tab = makeTab(server, { appId: 'app-1' });
  const unlocked = createProductionP2p({
    poolId: 'tp', origin: 'https://pools.test',
    fetch: server.fetch, token: () => 'x',
    identity: () => null, enabled: () => true,
    rtc: config => new FakePC(config, server.bus)
  });
  await withTabs([tab], async () => {
    await assert.rejects(tab.instance.get('not-a-digest'), /digest/);
    await assert.rejects(tab.instance.get('../secret'), /digest/);
    await assert.rejects(unlocked.get(DIGEST), /disabled/);
  });
});

test('constructor requires the fail-closed seams', () => {
  const base = { poolId: 'tp', origin: 'https://pools.test', fetch: async () => new Response('{}') };
  assert.throws(() => createProductionP2p({ ...base, token: () => 't', enabled: () => true }), /identity/);
  assert.throws(() => createProductionP2p({ ...base, identity: () => null, enabled: () => true }), /token/);
  assert.throws(() => createProductionP2p({ ...base, token: () => 't', identity: () => null }), /enabled/);
  assert.throws(() => createProductionP2p({ ...base, token: () => 't', identity: () => null, enabled: () => true, origin: 'http://pools.test' }), /origin/);
});
