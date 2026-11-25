// F11, 2026-11-25: production signaling + discovery client for pools-core.
// Author: GLM (Z.ai) for the owner. Truth: owner grill section 16, G-F11-1/2
// (BINDING): peers only between tabs locked to the SAME app; connection setup
// is a pools admin mode 'stun' (self-hosted STUN-only) or 'lan' (host
// candidates only); no public STUN, no TURN; SHA-256 checked transfers with
// HTTP fallback. File 3 transport draft (transport.js) stays byte-identical;
// this module only feeds it discovery, ICE configuration and envelopes.
//
// Wire contract (implemented server-side in internal/p2p + internal/api):
//   POST {prefix}/p2p/register {peerId}          -> {ttlMs}
//   POST {prefix}/p2p/leave    {peerId}          -> {left:true}
//   GET  {prefix}/p2p/peers                      -> {mode, iceServers, peers:[{peerId, appId, mode}]}
//   POST {prefix}/p2p/signal   {to, envelope}    -> {sent:true}
//   GET  {prefix}/p2p/signal?after=<cursor>      -> {cursor, messages:[{id, fromPeer, fromApp, envelope}]}
//
// The server relays envelopes ONLY between lease-authenticated registrations
// bound to the same app (enforced again in SQL, PostgreSQL multi-server
// safety). This client re-checks every inbound and outbound app binding and
// fails closed: a peer that claims another app is dropped before any WebRTC
// dial, and discovery payloads never carry lease or worker identifiers.
//
// Platform separation (CR-53): pure infrastructure, no imports from outside
// this directory, no compute code.
'use strict';

import {
  SHA256_HEX_RE, normalizeP2pMode, sanitizeIceServers, sameAppAllowed,
  SDP_MAX_BYTES, ICE_CANDIDATE_MAX_BYTES
} from './rules.js';

const PREFIXES = Object.freeze({ tp: '/thread-pool/v1', pp: '/process-pool/v1' });
const SIGNAL_TYPES = Object.freeze(['offer', 'answer', 'candidate', 'bye']);
const CALL_ID_RE = /^[A-Za-z0-9-]{8,128}$/;
const PEER_ID_RE = /^[A-Za-z0-9-]{8,128}$/;

/**
 * Validate one signaling envelope against the same caps the server enforces
 * (rules.js SIGNAL_* constants). Returns the normalized envelope or throws.
 * Non-trickle: offers and answers carry a full SDP; 'candidate' is accepted
 * for protocol completeness but this client never sends one.
 *
 * @param {*} raw
 * @returns {{v:number, type:string, callId:string, sdp?:string, candidate?:string, digest?:string}}
 */
export function validateEnvelope(raw) {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) throw new Error('envelope');
  if (raw.v !== 1) throw new Error('envelope');
  const type = String(raw.type || '');
  if (SIGNAL_TYPES.indexOf(type) < 0) throw new Error('envelope');
  const callId = String(raw.callId || '');
  if (!CALL_ID_RE.test(callId)) throw new Error('envelope');
  const out = { v: 1, type, callId };
  if (type === 'offer' || type === 'answer') {
    const sdp = String(raw.sdp === undefined || raw.sdp === null ? '' : raw.sdp);
    if (sdp.length < 1 || sdp.length > SDP_MAX_BYTES || /[\u0000]/.test(sdp)) throw new Error('envelope');
    out.sdp = sdp;
  }
  if (type === 'candidate') {
    const candidate = String(raw.candidate === undefined || raw.candidate === null ? '' : raw.candidate);
    if (candidate.length < 1 || candidate.length > ICE_CANDIDATE_MAX_BYTES) throw new Error('envelope');
    out.candidate = candidate;
  }
  if (raw.digest !== undefined && raw.digest !== null) {
    const digest = String(raw.digest).toLowerCase();
    if (!SHA256_HEX_RE.test(digest)) throw new Error('envelope');
    out.digest = digest;
  }
  if (type === 'offer' && !out.digest) throw new Error('envelope');
  return Object.freeze(out);
}

/**
 * Build the production signaling client.
 *
 * @param {Object} options
 * @param {string} options.origin pools-core https origin (no path/query)
 * @param {*} [options.fetch] injectable fetch (default globalThis.fetch)
 * @param {Function} options.token () => bearer lease token (G-F10-2)
 * @param {Function} options.myApp () => this tab's locked app id or ''
 * @param {Function} options.peerId () => this tab's stable random peer id
 * @returns {{register: Function, leave: Function, discover: Function,
 *            send: Function, poll: Function}}
 */
export function createSignalClient(options = {}) {
  let origin;
  try {
    origin = new URL(String(options.origin || ''));
  } catch (_) {
    throw new TypeError('createSignalClient requires an https origin');
  }
  if (origin.protocol !== 'https:' || origin.username || origin.password ||
      origin.search || origin.hash || (origin.pathname !== '/' && origin.pathname !== '')) {
    throw new TypeError('createSignalClient requires a bare https origin');
  }
  const doFetch = typeof options.fetch === 'function' ? options.fetch : globalThis.fetch;
  if (typeof options.token !== 'function' || typeof options.myApp !== 'function' ||
      typeof options.peerId !== 'function') {
    throw new TypeError('createSignalClient requires token(), myApp() and peerId()');
  }
  const base = origin.origin;

  async function call(path, init) {
    const headers = Object.assign({
      Authorization: 'Bearer ' + String(options.token())
    }, init.body ? { 'Content-Type': 'application/json' } : {});
    const response = await doFetch(base + path, Object.assign({}, init, {
      headers, credentials: 'omit', redirect: 'error', cache: 'no-store',
      referrerPolicy: 'no-referrer'
    }));
    let body = null;
    try { body = await response.json(); } catch (_) { /* non-JSON -> error below */ }
    if (!response.ok) {
      const reason = body && typeof body.error === 'string' ? body.error : 'http-' + response.status;
      throw new Error(reason);
    }
    return body;
  }

  return Object.freeze({
    /**
     * Register this tab as a same-app peer candidate for one poll interval of
     * the server TTL (rules.js PEER_TTL_MS). Refreshed by the caller's loop.
     */
    async register(pool) {
      const prefix = PREFIXES[pool];
      if (!prefix) throw new Error('pool');
      const body = await call(prefix + '/p2p/register', {
        method: 'POST', body: JSON.stringify({ peerId: options.peerId() })
      });
      const ttl = Number(body && body.ttlMs);
      return { ttlMs: Number.isSafeInteger(ttl) && ttl > 0 ? ttl : 0 };
    },

    /** Best-effort deregistration; never throws. */
    async leave(pool) {
      const prefix = PREFIXES[pool];
      if (!prefix) return;
      try {
        await call(prefix + '/p2p/leave', {
          method: 'POST', body: JSON.stringify({ peerId: options.peerId() })
        });
      } catch (_) { /* leaving is best effort */ }
    },

    /**
     * Discovery + ICE configuration in one authorized call. Fails closed:
     * unknown mode -> '', every ICE entry sanitized by rules.js (LAN-only
     * yields an empty list), peers of a different app dropped before they are
     * ever visible to the dialer, lease/worker fields never accepted.
     */
    async discover(pool) {
      const prefix = PREFIXES[pool];
      if (!prefix) throw new Error('pool');
      const body = await call(prefix + '/p2p/peers', { method: 'GET' });
      const mode = normalizeP2pMode(body && body.mode);
      const iceServers = sanitizeIceServers(mode, body && body.iceServers);
      const mine = options.myApp();
      const peers = [];
      if (Array.isArray(body && body.peers) && mine) {
        for (const peer of body.peers) {
          if (!peer || typeof peer !== 'object') continue;
          const peerId = String(peer.peerId || '');
          if (!PEER_ID_RE.test(peerId)) continue;
          if (!sameAppAllowed(String(peer.appId || ''), mine)) continue;
          const peerMode = normalizeP2pMode(peer.mode);
          if (!peerMode || peerMode === 'off') continue;
          peers.push({ peerId, appId: String(peer.appId).trim(), mode: peerMode });
        }
      }
      return Object.freeze({ mode, iceServers, peers: Object.freeze(peers) });
    },

    /** Send one validated envelope to a same-app peer. Throws on refusal. */
    async send(pool, to, envelope) {
      const prefix = PREFIXES[pool];
      if (!prefix) throw new Error('pool');
      if (!PEER_ID_RE.test(String(to || ''))) throw new Error('peer');
      const checked = validateEnvelope(envelope);
      const mine = options.myApp();
      if (!mine) throw new Error('app');
      await call(prefix + '/p2p/signal', {
        method: 'POST',
        body: JSON.stringify({ from: options.peerId(), to, envelope: checked })
      });
      return { sent: true };
    },

    /**
     * Poll inbound envelopes after a cursor. Inbound items whose fromApp is
     * not this tab's app are dropped (counted) and never returned; every
     * envelope is re-validated with the same caps the server used.
     */
    async poll(pool, after) {
      const prefix = PREFIXES[pool];
      if (!prefix) throw new Error('pool');
      const cursor = Number(after);
      const afterParam = Number.isSafeInteger(cursor) && cursor >= 0 ? cursor : 0;
      const body = await call(prefix + '/p2p/signal?after=' + afterParam +
        '&peer=' + encodeURIComponent(options.peerId()), { method: 'GET' });
      const nextCursor = Number(body && body.cursor);
      const out = [];
      let dropped = 0;
      const mine = options.myApp();
      if (Array.isArray(body && body.messages)) {
        for (const message of body.messages) {
          if (!message || typeof message !== 'object') { dropped++; continue; }
          if (!sameAppAllowed(String(message.fromApp || ''), mine)) { dropped++; continue; }
          const fromPeer = String(message.fromPeer || '');
          if (!PEER_ID_RE.test(fromPeer)) { dropped++; continue; }
          let envelope;
          try { envelope = validateEnvelope(message.envelope); } catch (_) { dropped++; continue; }
          out.push({ id: Number(message.id) || 0, fromPeer, fromApp: String(message.fromApp).trim(), envelope });
        }
      }
      return {
        cursor: Number.isSafeInteger(nextCursor) && nextCursor >= afterParam ? nextCursor : afterParam,
        messages: out,
        dropped
      };
    }
  });
}

// END OF signaling.js
