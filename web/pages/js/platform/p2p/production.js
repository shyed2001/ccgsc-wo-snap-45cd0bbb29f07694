// F11, 2026-11-25: production P2P client for the pools runtime.
// Author: GLM (Z.ai) for the owner. Truth: owner grill section 16, G-F11-1/2
// (BINDING); file 3 transport draft (transport.js, kept byte-identical).
//
// What this module adds to make P2P production per the owner decisions:
//  - G-F11-1 same-app-only peers: candidate peers come from pools-core
//    discovery (lease-authenticated registrations, server-enforced same-app
//    relay) and are re-checked with rules.sameAppAllowed before any dial;
//    inbound offers from another app are dropped before an answer.
//  - G-F11-2 admin modes: 'lan' dials with an EMPTY iceServers list (host
//    candidates, no STUN), 'stun' dials with only the server-validated
//    self-hosted STUN entries, 'off' never dials and never answers. No public
//    STUN, no TURN: rules.sanitizeIceServers drops both and strips credentials
//    (no secrets in the browser).
//  - SHA-256 check + HTTP fallback: peer bytes are verified by
//    transport.receiveArtifact before use and are NEVER admitted to the
//    provenance cache (cache.js putVerified stays server-ingest only); any
//    peer failure falls back to the hash-verified server fetch once.
//  - default per admin: nothing here runs until enabled() is true, which is
//    the conjunction of the admin mode row (tp/pp.p2p.mode != 'off') and the
//    contributor consent of G-F10-4. The code default stays off.
//
// Platform separation (CR-53): bytes are module artifacts addressed
// by digest; no compute logic lives here. Results and receipts always go to
// the server (transport enforces kind:'module').
//
// Node-testable: fetch, RTCPeerConnection and the battery gate are injectable.
'use strict';

import {
  SHA256_HEX_RE, normalizeP2pMode, sameAppAllowed,
  HANDSHAKE_BUDGET_MS, OPEN_WAIT_MS, SIGNAL_HANDSHAKES_MAX,
  serveAllowed, serveBudgetTick, SERVE_BUDGET_MIB_PER_MIN
} from './rules.js';
import { createArtifactPeerClient, receiveArtifact } from './transport.js';
import { createSignalClient } from './signaling.js';
import { createP2pArtifactCache } from './cache.js';

const PREFIXES = Object.freeze({ tp: '/thread-pool/v1', pp: '/process-pool/v1' });
const GATHER_TIMEOUT_MS = 2500;
const ANSWER_TTL_MS = HANDSHAKE_BUDGET_MS * 2;

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));

/**
 * Build the production P2P client.
 *
 * @param {Object} options
 * @param {string} options.poolId 'tp' | 'pp'
 * @param {string} options.origin pools-core https origin
 * @param {Function} options.token () => bearer lease token for pools-core
 * @param {Function} options.identity () => this tab's F10 app lock
 *   ({appId, ownerId, workerId, leaseId}) or null when the tab is not locked
 * @param {Function} options.enabled () => admin mode on AND contributor consent
 * @param {Function} [options.battery] () => {charging:boolean} (default {charging:true})
 * @param {Function} [options.claimsPaused] () => boolean claims pause (D-W10-11)
 * @param {Function} [options.rtc] (config) => RTCPeerConnection-like factory
 * @param {Function} [options.peerId] () => stable random session peer id
 * @param {Object} [options.cache] provenance artifact cache (cache.js wrapper);
 *   defaults to a private owned store
 * @param {number} [options.pollIntervalMs] loop cadence (tests shorten it)
 * @param {number} [options.serveBudgetMib] serve budget override (tests)
 * @returns {{get: Function, start: Function, stop: Function, stats: Function,
 *            lastPCConfig: Function, lastFallbackReason: Function}}
 */
export function createProductionP2p(options = {}) {
  const poolId = options.poolId === 'pp' ? 'pp' : 'tp';
  const prefix = PREFIXES[poolId];
  let origin;
  try {
    origin = new URL(String(options.origin || ''));
  } catch (_) {
    throw new TypeError('createProductionP2p requires an https origin');
  }
  if (origin.protocol !== 'https:' || origin.username || origin.password ||
      origin.search || origin.hash || (origin.pathname !== '/' && origin.pathname !== '')) {
    throw new TypeError('createProductionP2p requires a bare https origin');
  }
  const doFetch = typeof options.fetch === 'function' ? options.fetch : globalThis.fetch;
  const token = options.token;
  const identity = options.identity;
  const enabled = options.enabled;
  if (typeof token !== 'function' || typeof identity !== 'function' || typeof enabled !== 'function') {
    throw new TypeError('createProductionP2p requires token(), identity() and enabled()');
  }
  const makePC = typeof options.rtc === 'function' ? options.rtc
    : (typeof globalThis.RTCPeerConnection === 'function'
      ? config => new globalThis.RTCPeerConnection(config) : null);
  const pollIntervalMs = Number(options.pollIntervalMs);
  const pollEvery = Number.isSafeInteger(pollIntervalMs) && pollIntervalMs > 0 ? pollIntervalMs : 1500;
  const serveBudgetMib = Number(options.serveBudgetMib);
  const budgetMib = Number.isFinite(serveBudgetMib) && serveBudgetMib > 0 ? serveBudgetMib : SERVE_BUDGET_MIB_PER_MIN;

  let sessionPeerId = typeof options.peerId === 'function' ? String(options.peerId()) : '';
  if (!sessionPeerId) {
    sessionPeerId = (globalThis.crypto && typeof globalThis.crypto.randomUUID === 'function')
      ? globalThis.crypto.randomUUID() : 'p2p-' + Date.now() + '-' + Math.floor(Math.random() * 1e9);
  }

  // Owned default store for the provenance cache (entries, not raw buffers).
  const ownedStore = (() => {
    const map = new Map();
    return {
      async get(key) { return map.get(key); },
      async put(key, value) { map.set(key, value); return true; },
      async delete(key) { map.delete(key); return true; }
    };
  })();
  const cache = options.cache || createP2pArtifactCache({ cache: ownedStore });

  const signal = createSignalClient({
    origin, fetch: doFetch, token,
    myApp: () => { const id = identity(); return id && id.appId ? String(id.appId) : ''; },
    peerId: () => sessionPeerId
  });

  let lastPCConfig = null;
  let lastFallbackReason = null;
  const transport = createArtifactPeerClient({
    origin, fetch: doFetch,
    enabled: true,  // transport's own two-key gate: this literal AND consent()
    authorize: async (scope, action) => {
      const id = identity();
      if (!id || !enabled()) return false;
      if (scope.poolId !== poolId || scope.kind !== 'module') return false;
      if (!sameAppAllowed(scope.appId, String(id.appId))) return false;
      if (String(scope.ownerId) !== String(id.ownerId)) return false;
      if (action !== 'http' && action !== 'receive' && action !== 'serve') return false;
      return true;
    },
    consent: () => Boolean(enabled())
  });

  const pending = new Map();   // callId -> {resolve, reject, timer}
  const active = new Set();    // callIds of handshakes in flight
  const budgetState = { usedBytes: 0, windowStartMs: Date.now() };
  let running = false;
  let loopBroken = false;
  let cursor = 0;
  let lastIceServers = [];
  let nextRegisterAt = 0;
  let served = 0;

  function scopeFor(digest) {
    const id = identity();
    if (!id) throw new Error('identity');
    return Object.freeze({
      poolId, ownerId: String(id.ownerId), appId: String(id.appId),
      artifactId: digest, workerId: String(id.workerId), leaseId: String(id.leaseId),
      kind: 'module', expiresAt: Date.now() + 60000
    });
  }

  function handshakeSlot() {
    if (active.size >= SIGNAL_HANDSHAKES_MAX) throw new Error('busy');
  }

  function dropPending(callId, error) {
    const entry = pending.get(callId);
    if (!entry) return;
    clearTimeout(entry.timer);
    pending.delete(callId);
    error ? entry.reject(error) : entry.resolve();
  }

  function waitFor(callId, type, budgetMs) {
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => dropPending(callId, new Error('timeout')), budgetMs);
      pending.set(callId, {
        resolve: () => { clearTimeout(timer); pending.delete(callId); resolve(); },
        reject: error => { clearTimeout(timer); pending.delete(callId); reject(error); },
        timer
      });
    });
  }

  /** Wait for ICE gathering to complete (bounded); non-trickle SDP. */
  function gatheredSDP(pc, description) {
    if (pc.iceGatheringState === 'complete') {
      return Promise.resolve((pc.localDescription || description).sdp || '');
    }
    return new Promise(resolve => {
      let done = false;
      const finish = () => {
        if (done) return;
        done = true;
        clearTimeout(timer);
        pc.onicegatheringstatechange = null;
        resolve((pc.localDescription || description).sdp || '');
      };
      const timer = setTimeout(finish, GATHER_TIMEOUT_MS);
      pc.onicegatheringstatechange = () => {
        if (pc.iceGatheringState === 'complete') finish();
      };
    });
  }

  function openWait(channel, budgetMs) {
    if (channel.readyState === 'open') return Promise.resolve();
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        cleanup();
        reject(new Error('open'));
      }, budgetMs);
      const cleanup = () => {
        clearTimeout(timer);
        channel.removeEventListener('open', onOpen);
      };
      const onOpen = () => { cleanup(); resolve(); };
      channel.addEventListener('open', onOpen);
    });
  }

  /**
   * Dial one same-app peer and receive the digest-verified bytes over one
   * RTCDataChannel. The pc configuration is mode-derived and sanitized:
   * 'lan' -> {iceServers: []} (host candidates only), 'stun' -> self-hosted
   * stun entries only. Returns bytes or null when there was nothing to dial;
   * throws the transport reason ('hash', 'timeout', ...) on a failed attempt.
   */
  async function dialAndReceive(peer, digest, iceServers, parentSignal) {
    handshakeSlot();
    const callId = (globalThis.crypto && typeof globalThis.crypto.randomUUID === 'function')
      ? globalThis.crypto.randomUUID() : 'call-' + Date.now() + '-' + Math.floor(Math.random() * 1e9);
    active.add(callId);
    let pc = null;
    let channel = null;
    const startedAt = Date.now();
    try {
      pc = makePC({ iceServers });
      lastPCConfig = { iceServers: JSON.parse(JSON.stringify(iceServers)) };
      channel = pc.createDataChannel('p2p-artifact', { ordered: true });
      const offer = await pc.createOffer();
      await pc.setLocalDescription(offer);
      const sdp = await gatheredSDP(pc, offer);
      await signal.send(poolId, peer.peerId, { v: 1, type: 'offer', callId, sdp, digest });
      const remaining = HANDSHAKE_BUDGET_MS - (Date.now() - startedAt);
      if (remaining <= 0) throw new Error('timeout');
      await waitFor(callId, 'answer', remaining);
      await pc.setRemoteDescription(pendingAnswerSDP(callId));
      await openWait(channel, Math.max(0, OPEN_WAIT_MS));
      const budget = Math.max(1, HANDSHAKE_BUDGET_MS - (Date.now() - startedAt));
      const bytes = await receiveArtifact(channel, digest, {
        signal: parentSignal, timeoutMs: budget
      });
      signal.send(poolId, peer.peerId, { v: 1, type: 'bye', callId }).catch(() => {});
      return bytes;
    } finally {
      active.delete(callId);
      dropPending(callId, new Error('channel'));
      if (channel) { try { channel.close(); } catch (_) { /* gone */ } }
      if (pc) { try { pc.close(); } catch (_) { /* gone */ } }
    }
  }

  // The answer SDP is stashed by the poll loop when it resolves a pending dial.
  const answerSDPs = new Map();
  function pendingAnswerSDP(callId) {
    const sdp = answerSDPs.get(callId);
    answerSDPs.delete(callId);
    if (!sdp) throw new Error('signaling');
    return { type: 'answer', sdp };
  }

  /** Discovery + up to SIGNAL_HANDSHAKES_MAX same-app attempts. */
  async function dialPeers(digest, parentSignal) {
    if (!makePC) return null;
    // Register first so an answer can reach this tab on the very first get.
    await signal.register(poolId).catch(() => {});
    const discovery = await signal.discover(poolId);
    const mode = normalizeP2pMode(discovery.mode);
    if (mode === 'off') return null;
    lastIceServers = discovery.iceServers;
    if (!makePC) return null;
    let lastError = null;
    let tried = 0;
    for (const peer of discovery.peers) {
      if (tried >= SIGNAL_HANDSHAKES_MAX) break;
      if (peer.peerId === sessionPeerId) continue;   // never dial ourselves
      if (!sameAppAllowed(peer.appId, (identity() || {}).appId || '')) continue;
      tried++;
      try {
        const bytes = await dialAndReceive(peer, digest, discovery.iceServers, parentSignal);
        if (bytes) return { bytes, reason: null };
      } catch (error) {
        lastError = error;
        if (parentSignal && parentSignal.aborted) throw error;
      }
    }
    if (tried === 0) return null;
    return { bytes: null, reason: lastError ? String(lastError.message || 'peer') : 'no-peer' };
  }

  /** Hash-verified server fetch once, through the transport's authorize seam. */
  async function serverFallback(digest, parentSignal) {
    const result = await transport.get(scopeFor(digest), {
      token: String(token()),
      signal: parentSignal,
      openPeer: async () => { throw new Error('no-peer'); }
    });
    await cache.putVerified(digest, result.bytes);
    return result.bytes;
  }

  /**
   * Verified artifact acquisition: provenance cache -> same-app peers ->
   * HTTP fallback (once). Throws 'digest' for a bad digest shape and
   * 'disabled' when the tab has no F10 app lock (the page's own loader keeps
   * its existing fetch path then; P2P never runs unlocked).
   *
   * @param {string} digest sha256 hex of the module artifact
   * @param {Object} [opts] {signal}
   * @returns {Promise<{bytes: Uint8Array, source: 'cache'|'peer'|'server', fallbackReason: string|null}>}
   */
  async function get(digest, opts = {}) {
    const key = String(digest === undefined || digest === null ? '' : digest).toLowerCase();
    if (!SHA256_HEX_RE.test(key)) throw new Error('digest');
    if (!identity() || !enabled()) throw new Error('disabled');
    if (!running) start();
    lastFallbackReason = null;
    const cached = await cache.get(key);
    if (cached) return { bytes: cached, source: 'cache', fallbackReason: null };
    let peerReason = null;
    try {
      const attempt = await dialPeers(key, opts.signal);
      if (attempt && attempt.bytes) {
        return { bytes: attempt.bytes, source: 'peer', fallbackReason: null };
      }
      if (attempt && attempt.reason) peerReason = attempt.reason;
    } catch (error) {
      if (opts.signal && opts.signal.aborted) throw error;
      peerReason = String(error.message || 'peer');
    }
    const bytes = await serverFallback(key, opts.signal);
    lastFallbackReason = peerReason;
    return { bytes, source: 'server', fallbackReason: peerReason };
  }

  /** Serving gate: consent + battery + claims pause + rolling byte budget. */
  function mayServe() {
    const tick = serveBudgetTick(budgetState, { nowMs: Date.now(), budgetMib });
    budgetState.windowStartMs = tick.windowStartMs;
    budgetState.usedBytes = tick.usedBytes;
    const battery = typeof options.battery === 'function' ? options.battery() : { charging: true };
    const paused = typeof options.claimsPaused === 'function' ? options.claimsPaused() : false;
    return serveAllowed(battery && battery.charging === true, tick.remainingBytes, paused);
  }

  function chargeBudget(bytes) {
    const tick = serveBudgetTick(budgetState, { nowMs: Date.now(), budgetMib });
    budgetState.windowStartMs = tick.windowStartMs;
    budgetState.usedBytes = tick.usedBytes + Math.max(0, bytes);
  }

  function decline(fromPeer, callId) {
    signal.send(poolId, fromPeer, { v: 1, type: 'bye', callId }).catch(() => {});
  }

  /** Answer one same-app offer, then serve from the transport's recent cache. */
  async function answerOffer(message) {
    const envelope = message.envelope;
    if (!makePC) { decline(message.fromPeer, envelope.callId); return; }
    if (!mayServe()) { decline(message.fromPeer, envelope.callId); return; }
    if (!SHA256_HEX_RE.test(String(envelope.digest || ''))) {
      decline(message.fromPeer, envelope.callId);
      return;
    }
    try { handshakeSlot(); } catch (_) { decline(message.fromPeer, envelope.callId); return; }
    const callId = envelope.callId;
    active.add(callId);
    const cleanupTimer = setTimeout(() => active.delete(callId), ANSWER_TTL_MS);
    let pc = null;
    try {
      pc = makePC({ iceServers: lastIceServers });
      pc.ondatachannel = event => {
        const channel = event && event.channel;
        if (!channel) return;
        openWait(channel, OPEN_WAIT_MS).then(async () => {
          try {
            const scope = scopeFor(String(envelope.digest));
            const servedResult = await transport.serve(scope, channel, {});
            chargeBudget(servedResult && servedResult.bytes ? servedResult.bytes : 0);
            served++;
          } catch (_) { /* cache-miss/budget/etc: the requester falls back to HTTP */ }
        }).catch(() => { try { channel.close(); } catch (_) { /* gone */ } });
      };
      await pc.setRemoteDescription({ type: 'offer', sdp: envelope.sdp });
      const answer = await pc.createAnswer();
      await pc.setLocalDescription(answer);
      const sdp = await gatheredSDP(pc, answer);
      await signal.send(poolId, message.fromPeer, { v: 1, type: 'answer', callId, sdp });
    } catch (error) {
      decline(message.fromPeer, callId);
      if (pc) { try { pc.close(); } catch (_) { /* gone */ } }
      throw error;
    } finally {
      clearTimeout(cleanupTimer);
      active.delete(callId);
    }
  }

  async function handleMessage(message) {
    // Server-enforced, re-checked here: never answer or dial across apps.
    if (!sameAppAllowed(message.fromApp, (identity() || {}).appId || '')) return;
    const envelope = message.envelope;
    if (envelope.type === 'bye') {
      dropPending(envelope.callId, new Error('no-peer'));
      answerSDPs.delete(envelope.callId);
      return;
    }
    if (envelope.type === 'answer') {
      answerSDPs.set(envelope.callId, envelope.sdp);
      dropPending(envelope.callId, null);
      return;
    }
    if (envelope.type === 'offer') {
      await answerOffer(message);
      return;
    }
    // 'candidate': this client is non-trickle (full SDP); ignore politely.
  }

  async function loop() {
    while (running) {
      try {
        if (enabled() && identity()) {
          const now = Date.now();
          if (now >= nextRegisterAt) {
            const registration = await signal.register(poolId);
            nextRegisterAt = now + (registration.ttlMs > 0
              ? Math.min(registration.ttlMs / 2, 30000) : 15000);
          }
          const polled = await signal.poll(poolId, cursor);
          cursor = polled.cursor;
          for (const message of polled.messages) {
            try { await handleMessage(message); } catch (_) { /* one bad message must not stop the loop */ }
          }
          loopBroken = false;
        }
      } catch (_) {
        loopBroken = true; // transient signaling trouble: back off one tick
      }
      await sleep(pollEvery);
    }
  }

  function start() {
    if (running) return;
    running = true;
    loop().catch(() => { running = false; });
  }

  function stop() {
    running = false;
    for (const callId of Array.from(pending.keys())) dropPending(callId, new Error('channel'));
    if (identity()) signal.leave(poolId);
  }

  return Object.freeze({
    get,
    start,
    stop,
    stats: () => ({
      running, cursor, handshakes: active.size, served,
      budget: serveBudgetTick(budgetState, { nowMs: Date.now(), budgetMib })
    }),
    lastPCConfig: () => (lastPCConfig ? JSON.parse(JSON.stringify(lastPCConfig)) : null),
    lastFallbackReason: () => lastFallbackReason
  });
}

// END OF production.js
