// WO R14 step 2 (D-W10-14) - P2P pure rules for the pools contributor page.
// Author: GLM (Z.ai) for Claude Opus 5.5 head / owner. Date: 2026-11-21.
// Truth: WO_R13_WS3_P2P_CLIENT_DESIGN.md sections 3/5/6; owner decisions
// D-W10-14: same-origin wss signaling; direct (same-device/same-LAN) AND
// optional TURN; every P2P knob is a control in the pools P2P admin panel;
// code default OFF until staged qualification evidence.
//
// Pure functions only: no DOM, no network, no storage, no timers, no imports
// from outside this directory (CR-53 boundary; asserted by tests/p2p_rules.test.mjs).

// Finish Dash 2026-11-24: clamp the hard byte cap and reject inherited FSM keys.
// These are inert planning predicates, not peer authentication or transport.
'use strict';

// ---------------------------------------------------------------- Sizes (bytes)

// Max artifact bytes per peer transfer. Server cap pool.MaxArtifactBytes; the
// client refuses above it before any byte moves (design section 5).
export const MAX_TRANSFER_BYTES = 32 * 1024 * 1024;

// DataChannel chunk: 16 KiB, the cross-browser SCTP safe message size (D-W10-10).
export const CHUNK_BYTES = 16 * 1024;

// bufferedAmount backpressure: stop writing at the high-water mark, resume
// below it (WWPP-R30.1 memory rule).
export const BUFFER_HWM_BYTES = 1024 * 1024;
export const BUFFER_RESUME_BYTES = 256 * 1024;

// Signaling plane caps (design section 2.2): message size, per-connection
// message rate, concurrent handshakes per peer, SDP and ICE candidate bounds.
export const SIGNAL_MSG_MAX_BYTES = 64 * 1024;
export const SIGNAL_RATE_PER_SEC = 10;
export const SIGNAL_HANDSHAKES_MAX = 2;
export const SDP_MAX_BYTES = 16 * 1024;
export const ICE_CANDIDATE_MAX_BYTES = 2 * 1024;
export const SIGNAL_PING_INTERVAL_MS = 30000;
export const PEER_TTL_MS = 90000;

// ---------------------------------------------------------------- Timeouts (ms)

// Handshake budget offer -> ICE -> open; the open wait is inside it.
export const HANDSHAKE_BUDGET_MS = 8000;
export const OPEN_WAIT_MS = 5000;

// No bytes for this long -> abort r:'stall' (dead-peer detection).
export const STALL_TIMEOUT_MS = 8000;

// Transfer total deadline: min(30 s, 10 s + 1 s per 4 MiB).
export const TRANSFER_DEADLINE_MAX_MS = 30000;
export const TRANSFER_DEADLINE_BASE_MS = 10000;
export const TRANSFER_DEADLINE_PER_4MIB_MS = 1000;
export const TRANSFER_DEADLINE_SPAN_BYTES = 4 * 1024 * 1024;

export const MIB = 1024 * 1024;

// ---------------------------------------------------------------- Serve budget

// Rolling per-tab serve budget: 64 MiB per minute (design section 5).
export const SERVE_BUDGET_MIB_PER_MIN = 64;
export const SERVE_BUDGET_WINDOW_MS = 60000;

// ---------------------------------------------------------------- Tiers & scopes

// T0 same-device, T1 same-LAN, T2 near (design section 3). T2 stays deferred:
// tp.p2p.rtt_probe is off and no probe exists (WWPP-R14.1).
export const P2P_TIERS = Object.freeze(['t0', 't1', 't2']);

// The enum of schema row tp.p2p.peer_scope. 'same-phone' is a 'same-device'
// alias (owner pick on design FINDING F12); the default stays 'same-lan'.
export const P2P_SCOPES = Object.freeze(['same-device', 'same-phone', 'same-lan', 'near']);
export const DEFAULT_PEER_SCOPE = 'same-lan';

const SCOPE_TIERS = Object.freeze({
  'same-device': Object.freeze({ t0: true }),
  'same-phone': Object.freeze({ t0: true }),
  'same-lan': Object.freeze({ t0: true, t1: true }),
  'near': Object.freeze({ t0: true, t1: true, t2: true })
});

const TIER_KEYS = Object.freeze({ t0: 't0', t1: 't1', t2: 't2', 0: 't0', 1: 't1', 2: 't2' });

/**
 * Normalize a peer_scope value. Unknown values answer '' so every rule that
 * consumes a scope fails closed. 'same-phone' stays itself (alias of
 * 'same-device' inside SCOPE_TIERS).
 *
 * @param {*} scope
 * @returns {string} a P2P_SCOPES member or ''
 */
export function normalizePeerScope(scope) {
  const value = String(scope === undefined || scope === null ? '' : scope).trim().toLowerCase();
  return Object.prototype.hasOwnProperty.call(SCOPE_TIERS, value) ? value : '';
}

/**
 * Whether the peer tier may be dialed under the pool's peer scope.
 *
 * same-device -> T0; same-lan -> T0+T1; near -> T0+T1+T2; 'same-phone' behaves
 * exactly like 'same-device'. Unknown scope or tier answers false.
 *
 * @param {*} scope a tp.p2p.peer_scope value
 * @param {*} tier 't0'|'t1'|'t2' (case-insensitive) or 0|1|2
 * @returns {boolean}
 */
export function peerScopeAllows(scope, tier) {
  const norm = normalizePeerScope(scope);
  if (!norm) return false;
  const key = String(tier).trim().toLowerCase();
  const tierId = Object.prototype.hasOwnProperty.call(TIER_KEYS, key) ? TIER_KEYS[key] : '';
  if (!tierId) return false;
  return SCOPE_TIERS[norm][tierId] === true;
}

// ---------------------------------------------------------------- Chunk plan

/**
 * Plan the binary chunking of one transfer (design section 2.4: 16 KiB
 * ordered binary frames).
 *
 * Refuses anything not a positive safe integer and anything above maxBytes
 * (reason 'size'), so a peer can never talk a requester into a larger
 * transfer than the cap.
 *
 * @param {*} size byte length of the transfer
 * @param {Object} [opts]
 * @param {*} [opts.chunkBytes] defaults to CHUNK_BYTES
 * @param {*} [opts.maxBytes] defaults to MAX_TRANSFER_BYTES
 * @returns {{ok: boolean, reason: string|null, chunks: number, chunkBytes: number, totalBytes: number, lastChunkBytes: number}}
 */
export function chunkPlan(size, opts = {}) {
  let chunkBytes = Math.floor(Number(opts.chunkBytes));
  if (!Number.isSafeInteger(chunkBytes) || chunkBytes <= 0) chunkBytes = CHUNK_BYTES;
  let maxBytes = Math.floor(Number(opts.maxBytes));
  if (!Number.isSafeInteger(maxBytes) || maxBytes <= 0) maxBytes = MAX_TRANSFER_BYTES;
  maxBytes = Math.min(maxBytes, MAX_TRANSFER_BYTES);
  const total = Number(size);
  if (!Number.isSafeInteger(total) || total <= 0 || total > maxBytes) {
    return { ok: false, reason: 'size', chunks: 0, chunkBytes, totalBytes: Number.isSafeInteger(total) && total > 0 ? total : 0, lastChunkBytes: 0 };
  }
  const chunks = Math.ceil(total / chunkBytes);
  return {
    ok: true,
    reason: null,
    chunks,
    chunkBytes,
    totalBytes: total,
    lastChunkBytes: total - (chunks - 1) * chunkBytes
  };
}

// ---------------------------------------------------------------- Deadline

/**
 * The total-deadline budget of one transfer: min(30 s, 10 s + 1 s per 4 MiB).
 * A non-finite or non-positive byte count answers the base budget.
 *
 * @param {*} bytes
 * @returns {number} budget in ms
 */
export function transferDeadlineBudget(bytes) {
  const value = Number(bytes);
  const safe = Number.isFinite(value) && value > 0 ? value : 0;
  const per4 = Math.ceil(safe / TRANSFER_DEADLINE_SPAN_BYTES) * TRANSFER_DEADLINE_PER_4MIB_MS;
  return Math.min(TRANSFER_DEADLINE_MAX_MS, TRANSFER_DEADLINE_BASE_MS + per4);
}

/**
 * Absolute wall-clock deadline of one transfer started at `nowMs`.
 *
 * @param {*} bytes
 * @param {*} nowMs epoch ms of the transfer start
 * @returns {number} epoch ms deadline
 */
export function transferDeadline(bytes, nowMs) {
  const now = Number(nowMs);
  return (Number.isFinite(now) ? now : 0) + transferDeadlineBudget(bytes);
}

// ---------------------------------------------------------------- Fallback FSM

// Every fallback reason of design section 6. The abort wire reason 'deadline'
// is recorded as 'timeout' so the metric enum stays exactly this list.
export const P2P_FALLBACK_REASONS = Object.freeze([
  'no-peer', 'signaling', 'ice', 'timeout', 'stall', 'hash', 'size', 'busy', 'channel'
]);

// Abort reasons of the DataChannel protocol (design section 2.4) plus the
// pre-dial and pre-open reasons; 'deadline' maps onto 'timeout'.
export const P2P_ABORT_REASONS = Object.freeze([
  ...P2P_FALLBACK_REASONS, 'deadline'
]);

const ABORT_TO_FALLBACK = Object.freeze({
  'no-peer': 'no-peer',
  'signaling': 'signaling',
  'ice': 'ice',
  'timeout': 'timeout',
  'stall': 'stall',
  'hash': 'hash',
  'size': 'size',
  'busy': 'busy',
  'channel': 'channel',
  'deadline': 'timeout'
});

// States of one peer transfer attempt (design section 2.5 step 3).
export const P2P_TRANSFER_STATES = Object.freeze([
  'idle', 'dialing', 'open', 'transferring', 'verifying', 'served', 'fallback', 'closed'
]);

const ACTIVE_STATES = Object.freeze({
  idle: true, dialing: true, open: true, transferring: true, verifying: true
});

const STEPS = Object.freeze({
  'idle>offer': 'dialing',
  'dialing>open': 'open',
  'open>bytes': 'transferring',
  'transferring>bytes': 'transferring',
  'transferring>complete': 'verifying',
  'verifying>verified': 'served',
  'served>close': 'closed',
  'fallback>close': 'closed'
});

/**
 * One pure step of the fallback state machine.
 *
 * From every active state any abort ends at 'fallback' carrying the mapped
 * reason; an unknown or missing reason is recorded as 'channel' so the metric
 * enum is never invented. An abort after 'served' or 'fallback' is a no-op:
 * the attempt already ended. A protocol event that makes no sense in the
 * current active state aborts with 'channel' (fail closed). Terminal states
 * ignore junk events.
 *
 * @param {*} state a P2P_TRANSFER_STATES member; unknown values fail closed
 * @param {*} event 'offer'|'open'|'bytes'|'complete'|'verified'|'abort'|'close'
 * @param {*} [reason] an abort reason from P2P_ABORT_REASONS
 * @returns {{state: string, reason: string|null}}
 */
export function fallbackFSM(state, event, reason) {
  let cur;
  if (Object.hasOwn(ACTIVE_STATES, state)) cur = state;
  else if (state === 'served' || state === 'fallback' || state === 'closed') cur = state;
  else cur = 'fallback'; // unknown incoming state: never resume, stay failed
  if (event === 'abort') {
    if (state === 'served' || state === 'fallback' || state === 'closed') {
      return { state, reason: null };
    }
    const mapped = Object.hasOwn(ABORT_TO_FALLBACK, reason) ? ABORT_TO_FALLBACK[reason] : 'channel';
    return { state: 'fallback', reason: mapped || 'channel' };
  }
  const next = STEPS[`${cur}>${event}`];
  if (next) return { state: next, reason: null };
  if (Object.hasOwn(ACTIVE_STATES, cur)) return { state: 'fallback', reason: 'channel' };
  return { state: cur, reason: null };
}

// ---------------------------------------------------------------- Serving

/**
 * Whether this tab may serve bytes to a peer right now.
 *
 * `charging` folds the device truth and the pool knobs into one bit: charging,
 * or no battery present, or tp.p2p.serve_on_battery on. `budget` is the
 * remaining byte count of the rolling serve budget window (serveBudgetTick).
 * `paused` is the D-W10-11 claims pause, which extends to serving (design
 * section 3); the caller additionally ANDs isRunning of the tab.
 *
 * @param {*} charging strict boolean
 * @param {*} budget remaining bytes of the rolling window
 * @param {*} paused claims pause of the tab
 * @returns {boolean}
 */
export function serveAllowed(charging, budget, paused) {
  if (charging !== true) return false;
  if (paused) return false;
  const remaining = Number(budget);
  if (!Number.isFinite(remaining) || remaining <= 0) return false;
  return true;
}

/**
 * Roll the rolling serve-budget window and report what is left to serve.
 *
 * A window older than SERVE_BUDGET_WINDOW_MS resets to zero used. The caller
 * adds served bytes to usedBytes and re-feeds the result on the next tick or
 * serve decision.
 *
 * @param {Object} [state] { usedBytes, windowStartMs }
 * @param {Object} [opts]
 * @param {*} [opts.nowMs] epoch ms (default 0; tests pass a fixed clock)
 * @param {*} [opts.budgetMib] defaults to SERVE_BUDGET_MIB_PER_MIN
 * @returns {{windowStartMs: number, usedBytes: number, budgetBytes: number, remainingBytes: number, resetsInMs: number}}
 */
export function serveBudgetTick(state = {}, opts = {}) {
  const budgetMib = Number(opts.budgetMib);
  const budget = Number.isFinite(budgetMib) && budgetMib > 0 ? budgetMib : SERVE_BUDGET_MIB_PER_MIN;
  const budgetBytes = Math.floor(budget * MIB);
  const now = Number(opts.nowMs);
  const nowMs = Number.isFinite(now) ? now : 0;
  const start = Number(state.windowStartMs);
  const used = Number(state.usedBytes);
  const fresh = !Number.isFinite(start) || !Number.isFinite(used) || nowMs - start >= SERVE_BUDGET_WINDOW_MS;
  const windowStartMs = fresh ? nowMs : start;
  const usedBytes = fresh ? 0 : Math.max(0, Math.floor(used));
  return {
    windowStartMs,
    usedBytes,
    budgetBytes,
    remainingBytes: Math.max(0, budgetBytes - usedBytes),
    resetsInMs: Math.max(0, SERVE_BUDGET_WINDOW_MS - (nowMs - windowStartMs))
  };
}

// ---------------------------------------------------------------- Modes (F11, G-F11-1/2)

// Admin P2P modes on the pools admin panel (owner grill section 16, G-F11-1/2,
// BINDING; supersedes the D-W10-14 TURN decision above):
//  - 'off'  : no discovery, no dialing, no answering. The code default, and the
//             value every pool keeps until an admin opts in ("default per
//             admin": only the admin decides the mode, never a job or a page).
//  - 'lan'  : LAN-only. The RTC configuration carries an EMPTY iceServers list,
//             so ICE falls back to host candidates: no STUN at all, no TURN.
//  - 'stun' : self-hosted STUN only (the owner's coturn, configured in pools
//             server config at deploy). Still no TURN and no public providers.
export const P2P_MODES = Object.freeze(['off', 'lan', 'stun']);
export const DEFAULT_P2P_MODE = 'off';

// The SHA-256 hex digest shape shared by cache.js, transport.js, signaling.js
// and the server-side relay (one definition, previously private copies).
export const SHA256_HEX_RE = /^[0-9a-f]{64}$/;

/**
 * Normalize an admin P2P mode value. Unknown values answer '' so every
 * consumer fails closed into 'off' behaviour.
 *
 * @param {*} mode
 * @returns {string} a P2P_MODES member or ''
 */
export function normalizeP2pMode(mode) {
  const value = String(mode === undefined || mode === null ? '' : mode).trim().toLowerCase();
  return P2P_MODES.indexOf(value) >= 0 ? value : '';
}

/**
 * G-F11-1: peers exist only between tabs locked to the SAME app. Both ids
 * come from lease-authenticated registrations (F10 lease token), never from a
 * peer's own claim. Empty, non-string or mismatching ids are refused.
 *
 * @param {*} a app id of this tab
 * @param {*} b app id of the candidate peer
 * @returns {boolean}
 */
export function sameAppAllowed(a, b) {
  if (typeof a !== 'string' || typeof b !== 'string') return false;
  const x = a.trim();
  return x.length > 0 && x === b.trim();
}

// Well-known public STUN providers (G-F11-2: no public STUN). An exact-host
// denylist is defense in depth only; the binding control is that the URL
// comes from pools admin server configuration, never from a job option
// (internal/pool applyOptions still refuses options.p2p), a peer message or a
// page input.
const PUBLIC_ICE_HOSTS = Object.freeze([
  'stun.l.google.com', 'stun1.l.google.com', 'stun2.l.google.com',
  'stun3.l.google.com', 'stun4.l.google.com', 'stun.cloudflare.com',
  'global.stun.twilio.com', 'stun.sipgate.net', 'stun.nextcloud.com',
  'stun.stunprotocol.org', 'stun.sipnet.ru'
]);

// stun: or stuns: URL of the RFC 7064 shape (scheme ':' host[:port], optional
// '//' tolerated), no whitespace and no comma: a config list is
// comma-separated, so a comma would split into a broken URL.
const STUN_URL_RE = /^stuns?:(?:\/\/)?[^\s,/]+$/;

/**
 * Whether one URL may be handed to RTCPeerConnection as a STUN server
 * (G-F11-2: self-hosted STUN-only). Refuses anything that is not a stun:/
 * stuns: URL and every well-known public provider host.
 *
 * @param {*} raw
 * @returns {boolean}
 */
export function stunUrlAllowed(raw) {
  if (typeof raw !== 'string') return false;
  const value = raw.trim();
  if (!STUN_URL_RE.test(value) || value.indexOf('@') >= 0) return false;
  const host = value.replace(/^stuns?:(?:\/\/)?/i, '').split(':')[0].toLowerCase();
  return host.length > 0 && PUBLIC_ICE_HOSTS.indexOf(host) < 0;
}

/**
 * Sanitize the iceServers payload of the discovery response before it may
 * reach RTCPeerConnection. 'lan' and 'off' always answer an EMPTY list:
 * LAN-only means host candidates only. 'stun' keeps only fully valid
 * self-hosted stun:/stuns: entries and drops every other field: STUN needs no
 * credential and no secret may reach the browser (hard rule).
 *
 * @param {*} mode a P2P mode
 * @param {*} iceServers the server-provided list
 * @returns {Array<{urls: string[]}>}
 */
export function sanitizeIceServers(mode, iceServers) {
  if (normalizeP2pMode(mode) !== 'stun') return [];
  if (!Array.isArray(iceServers)) return [];
  const out = [];
  for (const server of iceServers) {
    if (!server || typeof server !== 'object') continue;
    const raw = server.urls;
    const urls = (Array.isArray(raw) ? raw : [raw])
      .filter(u => stunUrlAllowed(u))
      .map(u => String(u).trim());
    if (urls.length > 0) out.push({ urls });
  }
  return out;
}

/**
 * Superseded by G-F11-2 (owner grill section 16, BINDING): TURN is gone.
 * The D-W10-14 optional relay must never dial again, whatever a saved admin
 * row says. Kept as a fail-closed stub so existing callers keep compiling and
 * every call site stays visible to the audit; the next cleanup may remove it.
 *
 * @param {Object|null} [config] ignored
 * @returns {boolean} always false
 */
export function turnAllowed(config) {
  return false;
}

// END OF rules.js
