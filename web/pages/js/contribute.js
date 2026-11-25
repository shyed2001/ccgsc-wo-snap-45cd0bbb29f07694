// WO_T3_CONTRIBUTE_PAGE contribute.js - Contributor pool page controller (Tier 0 / Crowd Cloud)
// Author: Claude Opus 5.5 head / agy Gemini 3.8 Flash high. Date: 2026-11-17.
// WO_FIX_B (hermes MiniMax-M3, 2026-11-17): the contributor registers through the real
// worker register endpoint and keeps the server issued tab id, never probes the lease
// endpoint, never substitutes demo bytes for a real artifact and runs one job at a time.
// T104 step 1 (GLM Z.ai for Claude Opus 5.5 head, 2026-11-21): persisted host id,
// dual pools tp+pp with ONE anonymous token, and the optional hooks of the shared
// worker budget and the one status/log area of the integrated page.
// T104 step 6: the artifact cache keyed by the sha256 of the leased artifact.
// T104 step 8: the tab ceiling is computeTabBudget(...).total (CPU + GPU) and the
// GPU lanes claim under their own owner, so the sum of every lane of the tab
// stays within one budget.
// T104 steps 7/10: the page bootstrap of the integrated page is
// contribute_page_T104.js; this module keeps its own default behaviour.
// Truth: WO_T3_CONTRIBUTE_PAGE.md, docs/T104_INTEGRATED_PAGE_PLAN.md,
//        GRILL_WWPP_R1.md (R29.1, R30.1-3, R31.1-2, R37.1, R39.1)

'use strict';
import { createGPUPicker } from './gpu_picker_T83.js';
import {
  computeTabBudget,
  batteryAllowsGPU,
  POOLS_BUDGET_OWNER,
  POOLS_GPU_BUDGET_OWNER
} from './worker_budget_T104.js';
// D-W10-6/8: the runner's isolation modes and the multi-file cap. The workload
// bytes still arrive only as verified runtime artifacts (CR-53).
import {
  MAX_TOTAL_WORKLOAD_BYTES as RUNNER_MAX_TOTAL_WORKLOAD_BYTES,
  MAX_WORKLOAD_FILES,
  normalizeIsolationMode
} from './runner.js';
// CR-53: workloads are fetched as verified runtime artifact data, never imported or inlined.

// ---------------------------------------------------------------- Pool Endpoints (internal/api/api.go)

// The live API prefix of the thread pool. The route prefixes of internal/api/api.go
// are "/thread-pool/v1" and "/process-pool/v1"; the contributor pool is the thread
// pool (pool.Thread == "tp" is the pool id inside the JSON, not the URL prefix).
export const API_BASE = '/thread-pool/v1';

// The live API prefix of the process pool (T104 step: dual pools).
export const PROCESS_POOL_BASE = '/process-pool/v1';

// URL prefix per pool id. Both pools serve the same contributor worker routes
// (internal/api/api.go registers /workers/register, /heartbeat, /leases and
// /results under both prefixes with the same role and scope gates).
export const POOL_BASES = Object.freeze({ tp: API_BASE, pp: PROCESS_POOL_BASE });

// The owner keys of the new pools inside the shared WorkerBudget of the
// integrated page. They are the SAME constants worker_budget_T104.js exports: a
// second copy could drift and the app would claim under an owner nobody releases.
export const POOL_BUDGET_OWNER = POOLS_BUDGET_OWNER;
export const POOL_GPU_BUDGET_OWNER = POOLS_GPU_BUDGET_OWNER;

// The server caps an artifact at pool.MaxArtifactBytes (32 MiB). The client uses the
// same bound so a huge artifact is refused before it is transferred into the runner.
export const MAX_ARTIFACT_BYTES = 32 * 1024 * 1024;

// R11-M5: one file and one whole workload are two different bounds. A legal
// multi-file workload may sum above MAX_ARTIFACT_BYTES (that is the point of the
// cap on files), but never above the total the runner will accept. The sum is
// enforced while fetching, so a hostile lease cannot push MAX_WORKLOAD_FILES
// times the per-file bound into the page.
export const MAX_TOTAL_WORKLOAD_BYTES = RUNNER_MAX_TOTAL_WORKLOAD_BYTES;

// WO_T96B: anonymous renewal is at most once per 30 s; a 401/403 on a lease or
// heartbeat pauses claims for 2 s, 4 s, ... up to 60 s.
export const ANON_RENEW_MIN_MS = 30000;
export const AUTH_BACKOFF_BASE_MS = 2000;
export const AUTH_BACKOFF_MAX_MS = 60000;

// ---------------------------------------------------------------- T104 Pool Registry & Host Id

// The pool ids a contributor tab may register in (T104 step: dual pools with ONE
// anonymous token). The default stays ['tp']: a single-pool tab behaves exactly
// as before, and dual pools start only when the host passes options.pools.
const POOL_NAMES = ['tp', 'pp'];

/**
 * Read the pools this tab registers in. Unknown and duplicate entries are
 * dropped; an empty result falls back to the thread pool.
 *
 * @param {string|string[]|null|undefined} value
 * @returns {string[]}
 */
export function resolvePools(value) {
  if (value === undefined || value === null) return ['tp'];
  const list = Array.isArray(value) ? value : [value];
  const out = [];
  for (const item of list) {
    const pool = String(item || '').trim().toLowerCase();
    if (POOL_NAMES.includes(pool) && !out.includes(pool)) out.push(pool);
  }
  return out.length > 0 ? out : ['tp'];
}

// T104 step (persist hostId): the tab keeps one stable host id across reloads so
// the server sees one stable host. The id is NOT a credential: it is a random
// label sent in the register body. Only the page bootstrap writes it (the
// auto-start block at the end of this file and the root page wiring);
// ContributeApp itself never touches web storage, and the anonymous token stays
// in memory (WO_T96).
export const HOST_ID_STORAGE_KEY = 'ccgsc_contributor_host_id';
const HOST_ID_RE = /^[a-z0-9][a-z0-9-]{3,63}$/i;

// R10-FIX (chatgpt MAJOR): the per-tab identity lives in sessionStorage, which is
// scoped to one tab, unlike the shared localStorage hostId.
export const BROWSER_TAB_ID_STORAGE_KEY = 'ccgsc_browser_tab_id';

/**
 * Read the persisted host id, or null. A stored value that does not match the
 * host-id shape is ignored, never trusted.
 *
 * @param {Object} [storage] injectable storage (defaults to localStorage)
 * @returns {string|null}
 */
export function readPersistedHostId(storage) {
  let store = storage;
  if (!store) {
    try {
      if (typeof localStorage !== 'undefined') store = localStorage;
    } catch (_) { /* storage can throw on privacy modes */ }
  }
  if (!store || typeof store.getItem !== 'function') return null;
  try {
    const raw = store.getItem(HOST_ID_STORAGE_KEY);
    return HOST_ID_RE.test(String(raw || '')) ? String(raw) : null;
  } catch (_) {
    return null;
  }
}

/**
 * Keep one stable host id: keep the argument when valid, else the stored id,
 * else a fresh random id. Writes only when the stored value differs.
 *
 * @param {string} [hostId]
 * @param {Object} [storage] injectable storage (defaults to localStorage)
 * @returns {string} the host id to use
 */
export function persistHostId(hostId, storage) {
  let store = storage;
  if (!store) {
    try {
      if (typeof localStorage !== 'undefined') store = localStorage;
    } catch (_) { /* storage can throw on privacy modes */ }
  }
  const existing = readPersistedHostId(store);
  let id;
  if (hostId && HOST_ID_RE.test(String(hostId))) id = String(hostId);
  else id = existing || ('host-' + Math.random().toString(36).substring(2, 10));
  if (store && typeof store.setItem === 'function' && existing !== id) {
    try { store.setItem(HOST_ID_STORAGE_KEY, id); } catch (_) { /* private mode */ }
  }
  return id;
}

/**
 * Read the `sub` claim of a JWT without verifying it (display and change
 * detection only; the server verifies every token).
 *
 * @param {string|null} token
 * @returns {string|null}
 */
export function jwtSubject(token) {
  if (typeof token !== 'string') return null;
  const part = token.split('.')[1];
  if (!part) return null;
  try {
    const b64 = part.replace(/-/g, '+').replace(/_/g, '/');
    const json = typeof atob === 'function'
      ? atob(b64 + '==='.slice((b64.length + 3) % 4))
      : Buffer.from(b64, 'base64').toString('utf8');
    const sub = JSON.parse(json).sub;
    return typeof sub === 'string' ? sub : null;
  } catch (_) {
    return null;
  }
}

// The demo operator key of the runner: the RFC 8032 Ed25519 test vector. The runner
// pins its PUBLIC half; the private half lives here so the MOCK path can produce a
// real signature. It is a published test vector, and the runner pins it with scope
// 'demo', so it may only authorize the demo module digest.
export const DEMO_OPERATOR_KEY = Object.freeze({
  keyId: '21fe31dfa154a261',
  pkcs8B64: 'MC4CAQAwBQYDK2VwBCIEIJ1hsZ3v/VpguoRK9JLsLMREScVpezJpGXA7rAMcrn9g'
});

// 87-byte valid WebAssembly module exporting add(a, b) and sum(n)
// Same verified bytes as spikes/sandbox2/parent.html
export const SAMPLE_WASM = new Uint8Array([
  0, 97, 115, 109, 1, 0, 0, 0, 1, 12, 2, 96, 2, 127, 127, 1, 127, 96, 1, 127, 1, 127,
  3, 3, 2, 0, 1, 7, 13, 2, 3, 97, 100, 100, 0, 0, 3, 115, 117, 109, 0, 1,
  10, 43, 2, 7, 0, 32, 0, 32, 1, 106, 11, 33, 1, 2, 127, 3, 64, 32, 1, 32, 0, 72, 4, 64,
  32, 2, 32, 1, 106, 33, 2, 32, 1, 65, 1, 106, 33, 1, 12, 1, 11, 11, 32, 2, 11
]);

// ---------------------------------------------------------------- Pure Rule Functions

/**
 * Worker-count auto rule per GRILL_WWPP_R1.md R29.4, R30.2:
 * - Desktop/laptop: auto = cores - 1, min 1, max 4
 * - Phone: 1 worker on battery, 2 workers when charging
 *
 * @param {Object} opts
 * @param {number} [opts.hardwareConcurrency]
 * @param {boolean} [opts.isPhone]
 * @param {boolean} [opts.isCharging]
 * @returns {number} Worker count
 */
export function computeWorkerCount(opts = {}) {
  const isPhone = Boolean(opts.isPhone);
  if (isPhone) {
    return opts.isCharging ? 2 : 1;
  }
  const rawCores = typeof opts.hardwareConcurrency === 'number' && opts.hardwareConcurrency > 0
    ? opts.hardwareConcurrency
    : 2;
  return Math.max(1, Math.min(4, Math.floor(rawCores - 1)));
}

/**
 * Runner worker cap rule per WWPP-R39.1:
 * Runner alone allocates compute workers; cap = min(4, max(1, hardwareConcurrency - 1)).
 *
 * @param {number} [hardwareConcurrency]
 * @returns {number} Maximum allowed workers
 */
export function computeWorkerCap(hardwareConcurrency) {
  const cores = typeof hardwareConcurrency === 'number' && hardwareConcurrency > 0
    ? hardwareConcurrency
    : 2;
  return Math.max(1, Math.min(4, Math.floor(cores - 1)));
}

/**
 * Detect whether the user-agent represents a mobile/phone device.
 *
 * @param {string} [ua]
 * @returns {boolean}
 */
export function detectIsPhone(ua) {
  const str = String(ua || (typeof navigator !== 'undefined' ? navigator.userAgent : '') || '');
  return /Mobi|Android|iPhone|iPod/i.test(str) && !/iPad|Tablet/i.test(str);
}

// ---------------------------------------------------------------- Cryptographic Helpers

// Domain separator and version for task envelopes (WWPP-R39.1, Go TaskEnvelope compatibility)
export const ENVELOPE_DOMAIN = 'ccgsc.pools.task-envelope.v1';
export const ENVELOPE_VERSION = 1;

/**
 * Deterministic canonical JSON serialization (sorted keys).
 * Exact bytes required for Ed25519 envelope signature.
 */
export function canonicalJson(v) {
  if (v === null || typeof v !== 'object') return JSON.stringify(v);
  if (Array.isArray(v)) return '[' + v.map(canonicalJson).join(',') + ']';
  return '{' + Object.keys(v).sort().map(k => JSON.stringify(k) + ':' + canonicalJson(v[k])).join(',') + '}';
}

/**
 * Hex string from Uint8Array or ArrayBuffer.
 */
export function hexOf(u8) {
  const bytes = u8 instanceof Uint8Array ? u8 : new Uint8Array(u8);
  return Array.from(bytes).map(b => b.toString(16).padStart(2, '0')).join('');
}

/**
 * Compute SHA-256 hex digest using WebCrypto.
 *
 * @param {ArrayBuffer|Uint8Array|string} input
 * @returns {Promise<string>}
 */
export async function sha256Hex(input) {
  const cryptoObj = globalThis.crypto;
  if (!cryptoObj || !cryptoObj.subtle) {
    throw new Error('WebCrypto subtle digest unavailable');
  }
  let bytes;
  if (input instanceof Uint8Array) {
    bytes = input;
  } else if (input instanceof ArrayBuffer) {
    bytes = new Uint8Array(input);
  } else {
    bytes = new TextEncoder().encode(String(input));
  }
  const digest = await cryptoObj.subtle.digest('SHA-256', bytes);
  return hexOf(new Uint8Array(digest));
}

/**
 * Prepend Go domain prefix "ccgsc.pools.task-envelope.v1\0" to canonical JSON bytes.
 * Matches Go TaskEnvelope.CanonicalBytes().
 *
 * @param {Object} envelope
 * @returns {Uint8Array}
 */
export function envelopeSigningBytes(envelope) {
  const enc = new TextEncoder();
  const domainBytes = enc.encode(ENVELOPE_DOMAIN + '\0');
  const jsonBytes = enc.encode(canonicalJson(envelope));
  const combined = new Uint8Array(domainBytes.length + jsonBytes.length);
  combined.set(domainBytes, 0);
  combined.set(jsonBytes, domainBytes.length);
  return combined;
}

/**
 * Create a task envelope object aligned with Go TaskEnvelope struct (internal/verify/envelope.go).
 * Fields: version, jobId, taskId, artifactSha256, inputSha256, issuedAt, expiresAt, keyId.
 *
 * @param {string} jobId
 * @param {string} taskId
 * @param {string} artifactSha256
 * @param {number} [leaseDurationSec=3600]
 * @param {string} [keyId='0123456789abcdef']
 * @param {string} [inputSha256=null]
 * @returns {Object}
 */
export function createTaskEnvelope(jobId, taskId, artifactSha256, leaseDurationSec = 3600, keyId = '0123456789abcdef', inputSha256 = null) {
  const nowSec = Math.floor(Date.now() / 1000);
  return {
    version: ENVELOPE_VERSION,
    jobId: String(jobId),
    taskId: String(taskId),
    artifactSha256: String(artifactSha256),
    inputSha256: String(inputSha256 || artifactSha256),
    issuedAt: nowSec,
    expiresAt: nowSec + leaseDurationSec,
    keyId: String(keyId)
  };
}

/**
 * Sign task envelope with an Ed25519 private key over domain-prefixed canonical JSON.
 *
 * @param {Object} envelope
 * @param {CryptoKey} privateKey
 * @returns {Promise<{ envPlain: Uint8Array, envSig: Uint8Array }>}
 */
export async function signTaskEnvelope(envelope, privateKey) {
  const signingBytes = envelopeSigningBytes(envelope);
  const sigBuffer = await globalThis.crypto.subtle.sign('Ed25519', privateKey, signingBytes);
  return {
    envPlain: signingBytes,
    envSig: new Uint8Array(sigBuffer)
  };
}

// ---------------------------------------------------------------- Message Schema Validator

/**
 * Validate MessageChannel communication protocol schemas (R39.1).
 *
 * @param {Object} msg
 * @returns {{ valid: boolean, error: string|null }}
 */
export function validateMessageSchema(msg) {
  if (!msg || typeof msg !== 'object') {
    return { valid: false, error: 'message_must_be_object' };
  }
  if (typeof msg.type !== 'string') {
    return { valid: false, error: 'missing_type_field' };
  }

  switch (msg.type) {
    case 'init':
      if (typeof msg.nonce !== 'string' || !msg.nonce) {
        return { valid: false, error: 'init_requires_nonce_string' };
      }
      return { valid: true, error: null };

    case 'ready':
      if (typeof msg.nonce !== 'string') {
        return { valid: false, error: 'ready_requires_nonce_string' };
      }
      // R10-FIX owner D-W10-9: the runner self-reports its pin. The field is
      // optional (an older runner has none) but must be a string when present.
      if (msg.runnerPin !== undefined && typeof msg.runnerPin !== 'string') {
        return { valid: false, error: 'ready_runnerPin_must_be_string' };
      }
      return { valid: true, error: null };

    case 'job':
      if (typeof msg.jobId !== 'string' || !msg.jobId) return { valid: false, error: 'job_requires_jobId' };
      if (typeof msg.taskId !== 'string' || !msg.taskId) return { valid: false, error: 'job_requires_taskId' };
      if (typeof msg.nonce !== 'string' || !msg.nonce) return { valid: false, error: 'job_requires_nonce_string' };
      if (!(msg.wasm instanceof ArrayBuffer) && !(msg.wasm instanceof Uint8Array)) {
        return { valid: false, error: 'job_requires_wasm_buffer' };
      }
      if (typeof msg.wasmSha256 !== 'string' || !/^[0-9a-f]{64}$/.test(msg.wasmSha256)) {
        return { valid: false, error: 'job_requires_wasmSha256' };
      }
      if (!msg.envelope || typeof msg.envelope !== 'object') {
        return { valid: false, error: 'job_requires_envelope' };
      }
      if (!msg.envSig) return { valid: false, error: 'job_requires_envSig' };
      return { valid: true, error: null };

    case 'result':
      if (typeof msg.jobId !== 'string') return { valid: false, error: 'result_requires_jobId' };
      if (typeof msg.taskId !== 'string') return { valid: false, error: 'result_requires_taskId' };
      if (typeof msg.ok !== 'boolean') return { valid: false, error: 'result_requires_ok_boolean' };
      if (!msg.timings || typeof msg.timings !== 'object') {
        return { valid: false, error: 'result_requires_timings_object' };
      }
      return { valid: true, error: null };

    case 'check':
      if (typeof msg.name !== 'string') return { valid: false, error: 'check_requires_name' };
      if (!['PASS', 'FAIL', 'SKIP'].includes(msg.status)) return { valid: false, error: 'check_status_invalid' };
      return { valid: true, error: null };

    case 'error':
      if (typeof msg.msg !== 'string') return { valid: false, error: 'error_requires_msg' };
      return { valid: true, error: null };

    case 'ping':
    case 'pong':
      if (typeof msg.seq !== 'number') return { valid: false, error: 'ping_pong_requires_seq_number' };
      return { valid: true, error: null };

    default:
      return { valid: false, error: `unknown_message_type:${msg.type}` };
  }
}

// ---------------------------------------------------------------- Credential Reader

/**
 * Read the origins that may host the pinned runner.html.
 *
 * The shipped configuration serves the runner from the page origin, so the list is
 * empty by default. A deployment that serves the runner from a separate ccgsc-run
 * origin names it here, as a comma separated list of origins.
 *
 * @returns {string[]}
 */
export function readRunnerOrigins() {
  const raw = (typeof globalThis !== 'undefined' && globalThis.CCGSC_RUNNER_ORIGINS) || '';
  return String(raw)
    .split(',')
    .map(s => s.trim())
    .filter(s => s.length > 0);
}

/**
 * Read the bearer token of the contributor session.
 *
 * The token is taken from the contributor cookie. Storage-backed bearer tokens
 * are intentionally not read by this page.
 *
 * @returns {string} the token, or '' when there is none
 */
/**
 * Format the durable contributor counters. A missing number stays unmeasured.
 *
 * @param {Object|null} data
 * @returns {{acceptedText: string, computeText: string}}
 */
export function formatContributorCounters(data) {
  const accepted = data && typeof data.acceptedTasks === 'number' && Number.isFinite(data.acceptedTasks)
    ? data.acceptedTasks : null;
  const compute = data && typeof data.computeMs === 'number' && Number.isFinite(data.computeMs)
    ? data.computeMs : null;
  return {
    acceptedText: accepted == null ? 'not measured' : String(accepted),
    computeText: compute == null ? 'not measured' : `${compute} ms`
  };
}

/**
 * The read route for this page. Signed-in reads are the account. Anonymous
 * reads pass the install id this page keeps, never an id from the runner.
 *
 * @param {string} hostId
 * @param {boolean} anonymous
 * @returns {string}
 */
export function contributorCounterURL(hostId, anonymous) {
  return '/contributor/v1/counters';
}

/**
 * Write the counter text into the elements the contribute page owns.
 *
 * @param {{accepted?: {textContent: string}, compute?: {textContent: string}}} els
 * @param {Object|null} data
 * @param {boolean} failed
 */
export function applyContributorCounterText(els, data, failed) {
  if (!els) return;
  const view = failed
    ? { acceptedText: 'not measured', computeText: 'not measured' }
    : formatContributorCounters(data);
  if (els.accepted) els.accepted.textContent = view.acceptedText;
  if (els.compute) els.compute.textContent = view.computeText;
}

export function readAuthToken() {
  try {
    if (typeof document !== 'undefined' && typeof document.cookie === 'string') {
      const match = document.cookie.split(';').map(c => c.trim()).find(c => c.startsWith('contributor_token='));
      if (match) return decodeURIComponent(match.slice('contributor_token='.length));
    }
  } catch (_) {}
  return '';
}

// ---------------------------------------------------------------- Contribute Page App

/**
 * Whether this tab should claim work without a Start click.
 *
 * The default is on. `options.autoStart === false` turns it off in code.
 * The page query `?autostart=0` turns it off for debugging and wins over the default.
 *
 * @param {Object} [options]
 * @returns {boolean}
 */
export function resolveAutoStart(options = {}) {
  if (options && options.autoStart === false) return false;
  const search = options && typeof options.search === 'string'
    ? options.search
    : (typeof window !== 'undefined' && window.location && typeof window.location.search === 'string'
      ? window.location.search
      : '');
  if (search) {
    try {
      if (new URLSearchParams(search).get('autostart') === '0') return false;
    } catch (_) { /* a bad query does not disable auto-start */ }
  }
  return true;
}

/**
 * Read the session-scoped browser tab id, or null.
 *
 * sessionStorage is scoped to one tab, unlike the localStorage hostId, so two
 * tabs of one origin never share it while a reload of the same tab keeps it. Like
 * the host id, the id is NOT a credential: it is a random label sent in the
 * register body and the heartbeats. Only the page bootstrap writes it; the app
 * itself never touches web storage.
 *
 * @param {Object} [storage] injectable storage (defaults to sessionStorage)
 * @returns {string|null}
 */
export function readBrowserTabId(storage) {
  let store = storage;
  if (!store) {
    try {
      if (typeof sessionStorage !== 'undefined') store = sessionStorage;
    } catch (_) { /* storage can throw in privacy modes */ }
  }
  if (!store || typeof store.getItem !== 'function') return null;
  try {
    const raw = store.getItem(BROWSER_TAB_ID_STORAGE_KEY);
    return raw ? String(raw) : null;
  } catch (_) {
    return null;
  }
}

/**
 * Keep one session-scoped tab id: keep the argument when present, else the stored
 * id, else a fresh random one. Writes only when the stored value differs.
 *
 * @param {string} [browserTabId]
 * @param {Object} [storage] injectable storage (defaults to sessionStorage)
 * @returns {string} the tab id to use, or '' without a session store
 */
export function persistBrowserTabId(browserTabId, storage) {
  let store = storage;
  if (!store) {
    try {
      if (typeof sessionStorage !== 'undefined') store = sessionStorage;
    } catch (_) { /* storage can throw in privacy modes */ }
  }
  const existing = readBrowserTabId(store);
  const id = browserTabId ? String(browserTabId) : (existing || ('tab-' + hexOf(globalThis.crypto.getRandomValues(new Uint8Array(16)))));
  if (store && typeof store.setItem === 'function' && existing !== id) {
    try { store.setItem(BROWSER_TAB_ID_STORAGE_KEY, id); } catch (_) { /* private mode */ }
  }
  return id;
}

// ---------------------------------------------------------------- Contributor controls (D-W10-11)

// The three modes of D-W10-11 / CR-51, in the order of the Director panel. The
// server stores the mode per pool and returns it on the register and heartbeat
// replies; this module only reads it and applies it to the page.
export const CONTRIBUTOR_CONTROLS_MODES = Object.freeze(['off', 'pauseResume', 'startStopOptIn']);
export const DEFAULT_CONTRIBUTOR_CONTROLS = 'off';

// The control section. off removes it completely: that is the page of every
// earlier round, with no buttons and compute starting at once.
export const CONTRIBUTOR_CONTROLS_ID = 'contributor-controls';
export const CONTRIBUTOR_CONTROL_ACTION_ID = 'contributor-controls-action';
export const CONTRIBUTOR_CONTROL_CAPACITY_ID = 'contributor-controls-capacity';
export const CONTRIBUTOR_CONTROL_CAPACITY_VALUE_ID = 'contributor-controls-capacity-value';
export const CONTRIBUTOR_CONTROL_STATUS_ID = 'contributor-controls-status';

/**
 * Normalize a mode read from the server. Anything unknown (including the absent
 * field of a server of an earlier round) answers off, so a bad value can only
 * remove buttons, never invent one.
 *
 * @param {*} value
 * @returns {'off'|'pauseResume'|'startStopOptIn'}
 */
export function normalizeContributorControls(value) {
  const mode = typeof value === 'string' ? value.trim() : '';
  return CONTRIBUTOR_CONTROLS_MODES.includes(mode) ? mode : DEFAULT_CONTRIBUTOR_CONTROLS;
}

/**
 * The view of the contributor control section as a pure rule.
 *
 * off: no section at all. pauseResume: Pause while computing, Resume while
 * paused. startStopOptIn: Stop while computing, Start while stopped. Both
 * modes carry the capacity slider.
 *
 * @param {Object} [state]
 * @param {string} [state.mode]
 * @param {boolean} [state.isRunning]
 * @param {boolean} [state.claimsPaused]
 * @param {number} [state.workerCount]
 * @param {number} [state.capacityMax]
 * @returns {{visible: boolean, mode: string, action: string|null, actionLabel: string, status: string, capacityMax: number, capacityValue: number}}
 */
export function contributorControlView(state = {}) {
  const mode = normalizeContributorControls(state.mode);
  const isRunning = Boolean(state.isRunning);
  const claimsPaused = Boolean(state.claimsPaused);
  const capacityMax = Math.max(1, Math.floor(Number(state.capacityMax)) || 1);
  const wanted = Math.max(1, Math.floor(Number(state.workerCount)) || 1);
  const capacityValue = Math.min(capacityMax, wanted);
  if (mode === DEFAULT_CONTRIBUTOR_CONTROLS) {
    return { visible: false, mode, action: null, actionLabel: '', status: '', capacityMax, capacityValue };
  }
  if (mode === 'pauseResume') {
    const computing = isRunning && !claimsPaused;
    return {
      visible: true,
      mode,
      action: computing ? 'pause' : 'resume',
      actionLabel: computing ? 'Pause' : 'Resume',
      status: computing ? 'Computing' : 'Paused: no new leases; the running task still finishes and submits.',
      capacityMax,
      capacityValue
    };
  }
  return {
    visible: true,
    mode,
    action: isRunning ? 'stop' : 'start',
    actionLabel: isRunning ? 'Stop' : 'Start',
    status: isRunning ? 'Computing' : 'Stopped: press Start to contribute.',
    capacityMax,
    capacityValue
  };
}

export class ContributeApp {
  constructor(options = {}) {
    this.options = options;
    // The tab id is issued by the server at registration time. Until then this
    // placeholder is never used against a lease endpoint.
    this.tabId = options.tabId || null;
    this.registeredTabId = null;
    // T104 (persist hostId): the id comes from the host page, the persisted
    // storage or a fresh random label - read only, never written here.
    this.hostId = options.hostId
      || (typeof globalThis !== 'undefined' && globalThis.ccgscContributorHostId)
      || readPersistedHostId()
      || ('host-' + Math.random().toString(36).substring(2, 10));
    // R10-FIX (chatgpt MAJOR): a localStorage hostId identifies a DEVICE, not a
    // tab. Two tabs of one origin therefore registered as the same worker and the
    // second overwrote the first. The session-scoped id distinguishes them and
    // rides in the register body and every heartbeat. Read only, like the host id.
    this.browserTabId = options.browserTabId || readBrowserTabId() || '';
    // T104 (dual pools): the pools this tab registers in. The default keeps the
    // single thread-pool behaviour of every earlier round.
    this.pools = resolvePools(options.pools);
    this.tabIds = {};        // pool id -> server issued tab id
    this.claimIndex = null;  // round-robin cursor over the registered pools
    this.currentPool = null; // pool the current lease came from
    // T104 (integrated page): optional shared budget and the one status/log
    // area. Both are read late (budgetRef/sharedStatusLog), so the root page
    // wiring may finish before or after the app was constructed. A host that
    // passes neither keeps the standalone behaviour byte for byte.
    this.statusLog = options.statusLog || null;
    this.authToken = options.authToken || readAuthToken();
    // A missing cookie is not an anonymous session. That session exists only
    // after a successful join, and the token stays in this object (memory).
    this.isAnonymous = false;
    this.anonymousJoin = false;
    this.runnerConfig = null;
    this.tokenRenewTimer = null;
    this.egressReloadTimer = null;
    this.tokenExpiresAt = 0;
    // W7 fix: one heartbeat at a time, and a stop() generation that a renewal
    // completing after the stop compares against before re-arming its timer.
    this.heartbeatInFlight = false;
    this.stopGeneration = 0;
    this.currentLease = null;
    this.activeTasks = new Map();
    this.claiming = false;
    this.gpus = [];
    // T104c row 5: the GPU part the shared budget was already grown for. The
    // ceiling follows later discoveries with more lanes (0 until the first).
    this.gpuBudgetGrownFor = 0;
    this.backoff = false;
    this.hasBattery = false;
    // R10-FIX (minimax MINOR): the charge level is part of the owner GPU rule
    // (no GPU under 20 %). Without it the app refused half the allowed lanes.
    this.batteryLevel = undefined;
    this.registrationPending = false;
    // R10-FIX (minimax MAJOR): the server may reap this tab while it is stopped,
    // so a restart must register before it claims.
    this.needsRegistration = false;
    // R10-FIX (chatgpt MAJOR): a result the server has not acknowledged is kept
    // here, with its lease and task, and retried until it is accepted.
    this.pendingSettlement = null;
    this.settlementInFlight = false;
    // G-F12-3: true while a Dew receipt is still open and its result is not
    // accepted. The idle timer stays paused and the app lock is held.
    this.dewIdlePaused = false;

    this.isRunning = false;
    // Default on. options.autoStart === false, or ?autostart=0, leaves the tab idle.
    this.autoStart = resolveAutoStart(options);
    // W10D row 3 (D-W10-11): the contributor controls. off keeps today's page;
    // the server mode arrives with the register reply and every heartbeat
    // refreshes it, so a Director change applies without a reload.
    this.contributorControls = normalizeContributorControls(options.contributorControls);
    this.contributorControlsSeen = false;
    // True means this tab must not auto-start: set by startStopOptIn at boot and
    // by its Stop button, cleared by its Start button and by any other mode.
    this.contributorOptInStopped = false;
    // pauseResume: no new leases while true; an in-flight task still submits and
    // the tab keeps its worker (tab) id.
    this.claimsPaused = false;
    this.controlElements = null;
    this.isLeader = false;
    this.degraded = false;            // true when Web Locks are missing
    this.sourceMode = 'MOCK';        // 'MOCK' | 'LIVE' | 'ERROR'
    this.sourceReason = '';
    this.channel = null;
    this.port = null;
    this.runnerReady = false;
    this.runnerNonce = null;
    this.runnerBuildId = null;
    // R10-FIX owner D-W10-9: the pin the runner reported in its ready message.
    this.runnerPin = '';
    this.runnerRetryTimer = null;

    // Device state
    this.hwConcurrency = (typeof navigator !== 'undefined' && navigator.hardwareConcurrency) || 4;
    this.isPhone = detectIsPhone();
    this.isCharging = false;
    this.workerCount = computeWorkerCount({
      hardwareConcurrency: this.hwConcurrency,
      isPhone: this.isPhone,
      isCharging: this.isCharging
    });
    this.manualOverride = false;

    // The origins that may host the pinned runner.html. The shipped configuration
    // serves the runner from the page origin, so the list is empty by default.
    // A deployment that serves the runner from a separate ccgsc-run origin adds
    // that origin here (or through the CCGSC_RUNNER_ORIGINS page option).
    this.pinnedRunnerOrigins = Array.isArray(options.pinnedRunnerOrigins)
      ? options.pinnedRunnerOrigins.slice()
      : readRunnerOrigins();

    // Upkeep worker (WWPP-R31.1: 1 upkeep worker on demo origin)
    this.upkeepWorker = null;
    this.upkeepWorkerUrl = null;

    // Stats
    this.stats = {
      tasksDone: 0,
      totalTaskMs: 0,
      avgMsPerTask: 0,
      verifyPass: 0,
      verifyFail: 0,
      startTime: null,
      ratePerSec: 0
    };
    // Durable server counters. They stay unmeasured until a read succeeds.
    this.contributorCounters = null;
    this.contributorCountersKnown = false;

    // The demo operator key of the runner (the MOCK signing key).
    this.demoKeyPair = null;

    // T104 step 6: the artifact cache. When the host passes one (or a page
    // bootstrap installs it before the first lease) a repeat lease of the same
    // digest skips the fetch. Without one there is no cache and every lease
    // fetches exactly as before. The cache is never the authority: the digest is
    // computed on every job and the runner verifies envSig + sha256 itself.
    this.artifactCache = options.artifactCache || null;
  }

  getAuthHeaders() {
    const headers = {};
    if (this.authToken) {
      headers['Authorization'] = `Bearer ${this.authToken}`;
    }
    return headers;
  }

  /**
   * The document this app renders into. A host may inject one (tests, or an
   * embedded page); the browser global is the default.
   *
   * @returns {Object|null}
   */
  documentRef() {
    if (this.options && this.options.doc) return this.options.doc;
    return typeof document !== 'undefined' ? document : null;
  }

  /**
   * URL prefix of one pool id (T104). Unknown ids answer with the thread pool,
   * which keeps every caller of earlier rounds on the exact same URLs.
   *
   * @param {string} pool
   * @returns {string}
   */
  poolBase(pool) {
    return POOL_BASES[pool] || API_BASE;
  }

  /**
   * The pool the next claim should draw from (T104 dual pools). Round-robin
   * over the pools that actually registered; a single pool keeps answering
   * itself, which is the behaviour of every earlier round.
   *
   * @returns {string}
   */
  nextClaimPool() {
    const registered = this.pools.filter((pool) => this.tabIds[pool]);
    const order = registered.length > 0 ? registered : this.pools;
    if (order.length <= 1) return order[0] || 'tp';
    this.claimIndex = (this.claimIndex === null ? -1 : this.claimIndex) + 1;
    return order[this.claimIndex % order.length];
  }

  /**
   * The host id to send at registration time (T104 persist hostId). Options and
   * the host page win; the stored value was already resolved in the
   * constructor, so the late check only catches a host page that finished its
   * wiring after the app was constructed.
   *
   * @returns {string}
   */
  resolveHostId() {
    if (this.options.hostId) return String(this.options.hostId);
    if (typeof globalThis !== 'undefined' && globalThis.ccgscContributorHostId) {
      return String(globalThis.ccgscContributorHostId);
    }
    return this.hostId;
  }

  /**
   * The shared WorkerBudget of the integrated page, when the host exposes one
   * (options.budget or window.ccgscSharedBudget). Read late, every time.
   *
   * @returns {Object|null}
   */
  budgetRef() {
    if (this.options.budget && typeof this.options.budget.claim === 'function') return this.options.budget;
    if (typeof globalThis !== 'undefined' && globalThis.ccgscSharedBudget
        && typeof globalThis.ccgscSharedBudget.claim === 'function') {
      return globalThis.ccgscSharedBudget;
    }
    return null;
  }

  /**
   * The one status/log area of the integrated page, when the host exposes one
   * (options.statusLog or window.ccgscStatusLog). Read late, every call.
   *
   * @returns {Object|null}
   */
  sharedStatusLog() {
    if (this.statusLog && typeof this.statusLog.log === 'function') return this.statusLog;
    if (typeof globalThis !== 'undefined' && globalThis.ccgscStatusLog
        && typeof globalThis.ccgscStatusLog.log === 'function') {
      return globalThis.ccgscStatusLog;
    }
    return null;
  }

  /**
   * Reconcile the CPU worker claim of the new pools inside the shared budget
   * (T104 one worker budget per tab). Claims only while running; an idle tab
   * releases, so the old lanes can use every slot. Partial grants are shared
   * budgeting and are logged once per change.
   *
   * @returns {number} slots the new pools now hold
   */
  applyBudgetClaim() {
    const budget = this.budgetRef();
    if (!budget) return 0;
    if (!this.isRunning && !this.dewResultPending()) {
      // An idle tab holds nothing: a stale claim is released so the old lanes
      // can use every slot. The return is the slots the pools now hold.
      // A Dew result that is still inside its receipt keeps the claim (G-F12-3).
      this.dewIdlePaused = false;
      if (typeof budget.releaseAll === 'function') budget.releaseAll(POOL_BUDGET_OWNER);
      return 0;
    }
    if (!this.isRunning) {
      this.dewIdlePaused = true;
      return typeof budget.usedBy === 'function' ? budget.usedBy(POOL_BUDGET_OWNER) : 0;
    }
    const want = Math.max(1, Math.min(this.workerCount, computeWorkerCap(this.hwConcurrency)));
    const held = typeof budget.usedBy === 'function' ? budget.usedBy(POOL_BUDGET_OWNER) : 0;
    let granted = held;
    if (held > want) {
      budget.release(POOL_BUDGET_OWNER, held - want);
      granted = want;
    } else if (held < want) {
      granted = held + budget.claim(POOL_BUDGET_OWNER, want - held);
      if (granted < want) {
        this.log(`Worker budget shared with other lanes: the new pools run ${granted} of ${want} CPU workers.`);
      }
    }
    return granted;
  }

  /**
   * Reconcile the GPU lane claim (T104 step 8). A GPU lane is a worker slot like
   * any other: the tab ceiling is the CPU + GPU total of computeTabBudget, the
   * new pools hold their lanes under owner "pools:gpu", and the battery guard
   * (batteryAllowsGPU, no GPU under 20 %) plus the adapter cap of 2 decide how
   * many lanes may exist at all. An idle tab releases, exactly like the CPU part.
   *
   * @returns {number} GPU lanes the new pools now hold
   */
  applyGpuBudgetClaim() {
    const budget = this.budgetRef();
    if (!budget) return 0;
    // T104 step 8: the tab ceiling is `cpu + gpu` of computeTabBudget. The page
    // builds the budget before any adapter is known (its CPU part), so the GPU
    // part is grown here, once, when this registration discovered adapters. A
    // budget the host already sized (computeTabBudget(...).total) reports the
    // same total and is not touched. This runs even while idle: discovering an
    // adapter changes the ceiling, not the claim.
    this.growBudgetForGpus(budget);
    if (!this.isRunning && !this.dewResultPending()) {
      this.dewIdlePaused = false;
      if (typeof budget.releaseAll === 'function') budget.releaseAll(POOL_GPU_BUDGET_OWNER);
      return 0;
    }
    if (!this.isRunning) {
      this.dewIdlePaused = true;
      return typeof budget.usedBy === 'function' ? budget.usedBy(POOL_GPU_BUDGET_OWNER) : 0;
    }
    const want = this.gpuAllowed() ? Math.max(0, Math.min(2, this.gpus.length)) : 0;
    const held = typeof budget.usedBy === 'function' ? budget.usedBy(POOL_GPU_BUDGET_OWNER) : 0;
    if (held > want) {
      budget.release(POOL_GPU_BUDGET_OWNER, held - want);
      return want;
    }
    if (held < want) {
      const granted = held + budget.claim(POOL_GPU_BUDGET_OWNER, want - held);
      if (granted < want) {
        this.log(`Worker budget shared with other lanes: the new pools run ${granted} of ${want} GPU lanes.`);
      }
      return granted;
    }
    return held;
  }

  /**
   * Grow the shared budget to include the GPU lanes this tab discovered.
   *
   * Discovery can happen more than once (a re-registration after an anonymous
   * identity change, a guard change that re-enumerates), and the second
   * discovery may see MORE adapters than the first. The growth therefore
   * follows the GPU part that is now allowed instead of latching on the first
   * call (T104c row 5): every increase grows the ceiling, a smaller or equal
   * part is a no-op (the guard may have eaten the lanes of the first
   * discovery), and `resize` never shrinks.
   *
   * @param {Object} budget
   * @returns {number} the ceiling after the call
   */
  growBudgetForGpus(budget) {
    if (!budget || typeof budget.resize !== 'function') return 0;
    const discovered = Array.isArray(this.gpus) ? this.gpus.length : 0;
    const gpuPart = this.gpuAllowed() ? Math.max(0, Math.min(2, discovered)) : 0;
    const grownFor = Math.max(0, Math.floor(Number(this.gpuBudgetGrownFor)) || 0);
    if (gpuPart <= grownFor) return typeof budget.total === 'function' ? budget.total() : 0;
    this.gpuBudgetGrownFor = gpuPart;
    const cpuPart = computeTabBudget({
      hardwareConcurrency: this.hwConcurrency,
      isPhone: this.isPhone,
      isCharging: this.isCharging
    }).cpu;
    const want = cpuPart + gpuPart;
    const total = typeof budget.total === 'function' ? budget.total() : 0;
    if (want > total) {
      budget.resize(want);
      this.log(`Worker budget grew to ${want} slots (${cpuPart} CPU + ${gpuPart} GPU) for this tab.`);
    }
    return typeof budget.total === 'function' ? budget.total() : want;
  }

  /**
   * Release the whole claim of the new pools (stop, or the idle branch of the
   * budget reconciliation).
   *
   * @returns {number} freed slots
   */
  releaseBudgetClaim() {
    const budget = this.budgetRef();
    if (!budget || typeof budget.releaseAll !== 'function') return 0;
    return budget.releaseAll(POOL_BUDGET_OWNER);
  }

  /**
   * Release the GPU lane claim (T104 step 8).
   *
   * @returns {number} freed GPU lanes
   */
  releaseGpuBudgetClaim() {
    const budget = this.budgetRef();
    if (!budget || typeof budget.releaseAll !== 'function') return 0;
    return budget.releaseAll(POOL_GPU_BUDGET_OWNER);
  }

  /**
   * A Dew receipt is open while its deadline (unix seconds) is still ahead.
   *
   * @param {Object} receipt
   * @returns {boolean}
   */
  dewReceiptOpen(receipt) {
    if (!receipt || receipt.deadline == null || receipt.deadline === '') return false;
    const deadlineMs = Number(receipt.deadline) * 1000;
    return Number.isFinite(deadlineMs) && deadlineMs > Date.now();
  }

  /**
   * True when this tab still owes a Dew result and the receipt has not expired.
   * The app stays locked and the idle timer stays paused until submit or expiry
   * (G-F12-3).
   *
   * @returns {boolean}
   */
  dewResultPending() {
    const result = this.pendingSettlement;
    if (!result) return false;
    const context = result.taskId ? this.activeTasks.get(result.taskId) : null;
    const receipt = (context && context.leaseReceipt) || this.currentLeaseReceipt;
    return this.dewReceiptOpen(receipt);
  }

  /**
   * Drop the Dew hold. When the tab is already stopped, release the budget and
   * the upkeep worker that stop() kept alive for the outstanding result.
   */
  releaseDewHold() {
    const still = this.dewResultPending();
    this.dewIdlePaused = still;
    if (still || this.isRunning) return;
    this.releaseBudgetClaim();
    this.releaseGpuBudgetClaim();
    this.port?.postMessage({ type: 'gpu-guard', allowed: false });
    if (this.upkeepWorker) this.upkeepWorker.postMessage('stop');
    this.releaseUpkeepWorker();
  }

  async init() {
    if (!this.documentRef()) return;

    this.bindDOM();
    await this.initBatteryListener();
    await this.initWebLockLeader();
    await this.initDemoKeys();
    await this.loadRunnerConfig();
    await this.loadEgressApps();
    await this.initSource();
    this.initUpkeepWorker();
    await this.initRunnerOrigin();
    this.setupRunnerFrame();
    this.render();
    await this.refreshContributorCounters();
    // Registration has finished (LIVE, MOCK, or ERROR). Claim at once unless
    // auto-start was turned off. The runner handshake may still be in flight;
    // the ready handler kicks the claim loop when the port can take a job.
    // W7 review (agy): a failed registration (ERROR) does not spin a claim loop.
    // W10D row 3: startStopOptIn opens stopped even though auto-start is on.
    if (this.autoStart && !this.contributorOptInStopped && this.sourceMode !== 'ERROR') {
      await this.start();
    }
  }

  async loadEgressApps() {
    const doc = this.documentRef();
    const list = doc?.getElementById('egress-app-list');
    const consent = doc?.getElementById('egress-consent');
    if (!list || !consent) return;
    try {
      const response = await fetch('/pools/egress-apps', {cache:'no-store'});
      if (!response.ok) throw new Error('unavailable');
      const data = await response.json();
      const apps = Array.isArray(data.apps) ? data.apps.filter(a =>
        typeof a.appId === 'string' && /^[a-f0-9]{64}$/.test(a.consentId) &&
        Array.isArray(a.endpoints) && a.endpoints.every(e => typeof e === 'string')) : [];
      // F10b-13: consent binds to the displayed policy versions. When the
      // approved set changes while the page is open, drop the recorded consent
      // and force a fresh explicit opt-in (the server rejects stale IDs too).
      const ids = apps.map(a => a.consentId).sort();
      if (this.egressDisplayedIDs && JSON.stringify(ids) !== JSON.stringify(this.egressDisplayedIDs)) {
        this.egressConsentIDs = [];
        consent.checked = false;
        if (this.sourceMode === 'LIVE' && !this.activeTasks.size && !this.claiming) this.registerWorker().catch(() => {});
      }
      this.egressDisplayedIDs = ids;
      list.textContent = apps.length ? apps.map(a => a.pool + ' / ' + a.appId + ': ' + a.endpoints.join(', ')).join('\n') : 'No egress-enabled applications approved.';
      consent.disabled = apps.length === 0;
      consent.onchange = () => {
        this.egressConsentIDs = consent.checked ? apps.map(a => a.consentId) : [];
        if (this.sourceMode === 'LIVE') {
          this.registrationPending = true;
          if (!this.activeTasks.size && !this.claiming) this.registerWorker().catch(() => {});
        }
      };
    } catch (_) { list.textContent = 'Endpoint list unavailable; egress remains disabled.'; }
    if (!this.egressReloadTimer) {
      this.egressReloadTimer = setInterval(() => { this.loadEgressApps().catch(() => {}); }, 60000);
    }
  }

  async ensureAppRunner(grant) {
    const key = grant ? JSON.stringify([grant.owner,grant.appId,grant.pool,grant.policyVersion]) : '';
    if (grant && (!this.egressConsentIDs?.includes(grant.consentId) ||
        typeof grant.runnerGrant !== 'string' || typeof grant.token !== 'string' ||
        !Number.isSafeInteger(grant.expiresAt) || !Array.isArray(grant.endpoints))) return false;
    if ((this.egressFrameKey || '') === key) return grant ? this.runnerReady : true;
    if (this.activeTasks.size) return false;
    const frame = this.dom?.runnerFrame;
    if (!frame || !this.baseRunnerSrc || !this.isRunnerFramePinned(frame)) return false;
    try {
      const url = new URL(this.baseRunnerSrc, location.href);
      if (grant) {
        if (url.origin === location.origin || !this.pinnedRunnerOrigins.includes(url.origin)) return false;
        url.searchParams.set('grant', grant.runnerGrant);
      }
      const replacement = frame.cloneNode(false);
      replacement.src = url.href;
      clearTimeout(this.runnerRetryTimer);
      try { this.channel?.port1?.close(); this.channel?.port2?.close(); } catch (_) {}
      this.runnerReady = false;
      this.port = null;
      frame.replaceWith(replacement);
      this.dom.runnerFrame = replacement;
      this.egressFrameKey = key;
      this.setupRunnerFrame();
      const deadline = Date.now() + 6000;
      while (!this.runnerReady && Date.now() < deadline) await new Promise(resolve => setTimeout(resolve,50));
      return this.runnerReady;
    } catch (_) { return false; }
  }

  async initRunnerOrigin() {
    const frame = this.dom && this.dom.runnerFrame;
    if (!frame || typeof fetch !== 'function') return;
    try {
      const response = await fetch('/pools/runner-config', { headers: this.getAuthHeaders() });
      if (!response.ok) return;
      const body = await response.json();
      const origin = new URL(String(body && body.runOrigin || ''));
      if (!['http:', 'https:'].includes(origin.protocol) || origin.pathname !== '/' ||
          origin.search || origin.hash ||
          (typeof location !== 'undefined' && origin.origin === location.origin)) {
        return;
      }
      const pinnedOrigin = origin.origin;
      if (!this.pinnedRunnerOrigins.includes(pinnedOrigin)) {
        this.pinnedRunnerOrigins.push(pinnedOrigin);
      }
      frame.src = `${pinnedOrigin}/runner.html`;
      this.baseRunnerSrc = frame.src;
    } catch (_) {
      // An absent or invalid deployment origin leaves the runner disabled.
    }
  }

  bindDOM() {
    const doc = this.documentRef();
    if (!doc) return;
    this.dom = {
      statusBadge: doc.getElementById('status-badge'),
      sourceBadge: doc.getElementById('source-badge'),
      leaderBadge: doc.getElementById('leader-badge'),
      workerCountSelect: doc.getElementById('worker-count-select'),
      hwInfo: doc.getElementById('hw-info'),
      statTasksDone: doc.getElementById('stat-tasks-done'),
      statAvgMs: doc.getElementById('stat-avg-ms'),
      statRate: doc.getElementById('stat-rate'),
      statVerifyPass: doc.getElementById('stat-verify-pass'),
      statVerifyFail: doc.getElementById('stat-verify-fail'),
      statAcceptedTasks: doc.getElementById('stat-accepted-tasks'),
      statComputeMs: doc.getElementById('stat-compute-ms'),
      logOutput: doc.getElementById('log-output'),
      runnerFrame: doc.getElementById('runner-frame')
    };

    if (this.dom.workerCountSelect) {
      this.dom.workerCountSelect.addEventListener('change', (e) => {
        const val = e.target.value;
        if (val === 'auto') {
          this.manualOverride = false;
          this.workerCount = computeWorkerCount({
            hardwareConcurrency: this.hwConcurrency,
            isPhone: this.isPhone,
            isCharging: this.isCharging
          });
        } else {
          this.manualOverride = true;
          this.workerCount = Math.max(1, Math.min(4, parseInt(val, 10) || 1));
        }
        this.log(`Worker count set to ${this.workerCount} (${this.manualOverride ? 'manual' : 'auto'})`);
        this.applyBudgetClaim();
        this.render();
      });
    }
  }

  async initBatteryListener() {
    if (typeof navigator !== 'undefined' && typeof navigator.getBattery === 'function') {
      try {
        const battery = await navigator.getBattery();
        this.hasBattery = true;
        this.isCharging = Boolean(battery.charging);
        this.batteryLevel = typeof battery.level === 'number' ? battery.level : undefined;
        // R10-FIX (minimax MINOR): the owner rule needs the charge level, so the
        // guard follows it as well as the charging state.
        battery.addEventListener('levelchange', () => {
          this.batteryLevel = typeof battery.level === 'number' ? battery.level : undefined;
          this.updateGPUGuard();
          this.applyGpuBudgetClaim();
          this.log(`Battery level changed: ${this.batteryLevel}`);
        });
        battery.addEventListener('chargingchange', () => {
          this.isCharging = Boolean(battery.charging);
          this.updateGPUGuard();
          if (!this.manualOverride) {
            this.workerCount = computeWorkerCount({
              hardwareConcurrency: this.hwConcurrency,
              isPhone: this.isPhone,
              isCharging: this.isCharging
            });
            this.render();
            this.applyBudgetClaim();
          }
          // T104 step 8: the battery guard decides whether GPU lanes may exist,
          // so the GPU claim of the tab is reconciled with every guard change.
          this.applyGpuBudgetClaim();
          this.log(`Battery status changed: charging=${this.isCharging}`);
        });
      } catch (_) {
        // Battery API not supported or permissions blocked
      }
    }
  }

  async initWebLockLeader() {
    // WWPP-R39.1: Web Locks leader election only.
    //
    // The request must NOT be awaited: the callback holds the lock until the
    // window unloads, so awaiting it would block the whole page start. The lock
    // is granted when it is actually free, so a standby tab becomes the leader
    // as soon as the holder goes away.
    if (typeof navigator !== 'undefined' && navigator.locks && navigator.locks.request) {
      navigator.locks.request('ccgsc_contribute_leader', async () => {
        this.isLeader = true;
        this.degraded = false;
        this.log('Web Lock acquired: This tab is the ACTIVE LEADER');
        this.render();
        // Hold the lock until the window unloads.
        await new Promise((resolve) => {
          if (typeof window !== 'undefined') {
            window.addEventListener('unload', resolve, { once: true });
          }
        });
      }).catch(() => {
        this.isLeader = false;
        this.degraded = true;
        this.log('Web Lock request failed: this tab cannot be elected leader');
        this.render();
      });
    } else {
      // Without Web Locks every tab would claim to be the leader. The tab keeps
      // working, but it says so: the degraded mode is surfaced, not hidden.
      this.isLeader = false;
      this.degraded = true;
      this.log('DEGRADED: Web Locks are unavailable, this tab cannot be elected leader');
      this.render();
    }
  }

  async initDemoKeys() {
    // The demo operator key of the runner: the MOCK path signs with it, so the
    // runner can verify a mock job against its own pinned key.
    try {
      const pkcs8 = base64ToBytes(DEMO_OPERATOR_KEY.pkcs8B64);
      this.demoKeyPair = await globalThis.crypto.subtle.importKey(
        'pkcs8',
        pkcs8,
        { name: 'Ed25519' },
        true,
        ['sign']
      );
    } catch (e) {
      this.log('Warning: demo operator key import failed: ' + (e.message || e));
    }
  }

  /**
   * Read whether this deployment allows a contributor tab to join without a login.
   *
   * The flag comes from GET /pools/runner-config (`anonymousJoin`). A missing or
   * failed config leaves the flag false, so the page keeps the logged-in register
   * path.
   *
   * @returns {Promise<Object|null>}
   */
  async loadRunnerConfig() {
    this.anonymousJoin = false;
    if (typeof fetch !== 'function') return null;
    try {
      const response = await fetch('/pools/runner-config', { headers: this.getAuthHeaders() });
      if (!response.ok) return null;
      const body = await response.json();
      this.runnerConfig = body && typeof body === 'object' ? body : null;
      this.anonymousJoin = Boolean(this.runnerConfig && this.runnerConfig.anonymousJoin === true);
      return this.runnerConfig;
    } catch (_) {
      return null;
    }
  }

  /**
   * WO_T96B: single-flight anonymous renewal. The upkeep tick, the renewal timer
   * and the auth-failure path all call this; only one request is in flight, and a
   * renewal is not repeated within ANON_RENEW_MIN_MS of the last one.
   *
   * @param {boolean} [force] skip the minimum interval (auth failure path)
   * @returns {Promise<boolean>}
   */
  renewAnonymous(force = false) {
    if (this.renewPromise) return this.renewPromise;
    const now = Date.now();
    if (!force && this.lastRenewAt && now - this.lastRenewAt < ANON_RENEW_MIN_MS) {
      return Promise.resolve(false);
    }
    this.lastRenewAt = now;
    const before = jwtSubject(this.authToken);
    this.renewPromise = this.joinAnonymous()
      .then(async () => {
        const after = jwtSubject(this.authToken);
        // A new subject owns no tab: the old tab id answers 403. Register again.
        if (before && after && before !== after && this.sourceMode === 'LIVE') {
          this.log('Anonymous identity changed, registering this tab again.');
          this.registrationPending = true;
          await this.registerWorker();
        }
        return true;
      })
      .catch((err) => {
        // W7 review: a transient failure retries in 5 s, not after the full 30 s.
        this.lastRenewAt = Date.now() - ANON_RENEW_MIN_MS + 5000;
        throw err;
      })
      .finally(() => { this.renewPromise = null; });
    return this.renewPromise;
  }

  /**
   * W7 review (deepseek): pause claims, register and heartbeat after a failed
   * call (429, 5xx, network, failed registration) with exponential backoff
   * (2 s .. 60 s). A Retry-After header longer than the backoff wins, capped at
   * 300 s. Only a granted lease resets the count.
   *
   * @param {number} status HTTP status, 0 for a network error
   * @param {Response} [res]
   */
  pauseAfterServerFailure(status, res) {
    const now = Date.now();
    this.busyFailures = (this.busyFailures || 0) + 1;
    let delay = Math.min(AUTH_BACKOFF_MAX_MS, AUTH_BACKOFF_BASE_MS * 2 ** (this.busyFailures - 1));
    const header = res && res.headers && typeof res.headers.get === 'function' ? res.headers.get('Retry-After') : null;
    const seconds = header ? Number.parseInt(header, 10) : NaN;
    if (Number.isFinite(seconds) && seconds > 0) delay = Math.max(delay, Math.min(seconds, 300) * 1000);
    this.authBlockedUntil = Math.max(this.authBlockedUntil || 0, now + delay);
    this.log(`Server unavailable (HTTP ${status || 'network'}). Pausing for ${Math.round(delay / 1000)} s.`);
  }

  /**
   * WO_T96B: a lease, heartbeat or result call returned 401/403. Pause claims with
   * exponential backoff (2 s .. 60 s), then renew the anonymous token and register
   * again. A non-anonymous tab only backs off: its credentials are the user's.
   *
   * @param {number} status
   */
  async onAuthFailure(status) {
    // T112 (deepseek): a 401/403 that lands while a renewal is in flight was sent
    // with the token being replaced; wait for the renewal instead of backing off.
    if (this.renewPromise) {
      await this.renewPromise.catch(() => {});
      return;
    }
    const now = Date.now();
    if (this.authBlockedUntil && now < this.authBlockedUntil) return;
    this.authFailures = (this.authFailures || 0) + 1;
    const delay = Math.min(AUTH_BACKOFF_MAX_MS, AUTH_BACKOFF_BASE_MS * 2 ** (this.authFailures - 1));
    this.authBlockedUntil = now + delay;
    this.log(`Server answered HTTP ${status}. Pausing claims for ${Math.round(delay / 1000)} s.`);
    if (!this.isAnonymous) return;
    try {
      // Keep the old bearer: the server re-issues the same subject when it still
      // validates (60 s grace), and mints a fresh one otherwise. renewAnonymous
      // registers a new tab ONLY when the subject changed. A 403 with the same
      // subject (reputation bar/ban) never re-registers: that would evade the bar
      // (W7 review, Gemini). It only backs off.
      const tabBefore = this.tabId;
      await this.renewAnonymous(status === 401);
      if (this.tabId !== tabBefore) {
        // W7 review (cmdc): no backoff reset here; only a successful lease resets it.
        this.log('Anonymous tab joined again as ' + this.tabId);
      }
    } catch (e) {
      this.log('Re-join failed: ' + (e.message || e));
    }
  }

  async joinAnonymous() {
    try {
      // T104 (dual pools, ONE anonymous token): the token is minted by the
      // first pool of this.pools and then used for every pool of the tab. The
      // worker routes of internal/api/api.go gate on the contributor role and
      // the worker scope only (secure(p, "contributor", "worker", ...)), and
      // auth.IssueAnonymous carries no pool binding; the poolId field of the
      // reply is informational. A second join would mint a second identity and
      // break the renewal chain (WO_T96B), so there is exactly one.
      const res = await fetch(`${this.poolBase(this.pools[0] || 'tp')}/workers/anonymous`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...this.getAuthHeaders() }
      });
      if (!res.ok) {
        const err = new Error('anonymous join rejected with HTTP ' + res.status);
        err.status = res.status;
        throw err;
      }
      const data = await res.json();
      if (!data || !data.accessToken) {
        throw new Error('anonymous join returned no access token');
      }
      this.authToken = data.accessToken;
      this.isAnonymous = true;
      const expiresIn = typeof data.expiresIn === 'number' ? data.expiresIn : 900;
      this.tokenExpiresAt = Date.now() + expiresIn * 1000;
      this.scheduleTokenRenewal(expiresIn);
      return true;
    } catch (e) {
      this.log('Anonymous join failed: ' + (e.message || e));
      throw e;
    }
  }

  scheduleTokenRenewal(expiresInSec) {
    if (this.tokenRenewTimer) {
      clearTimeout(this.tokenRenewTimer);
      this.tokenRenewTimer = null;
    }
    // W7 fix: a renewal that was in flight when stop() ran must not re-arm the
    // timer on a stopped tab. stop() bumps the generation, so the late completion
    // is dropped instead of resurrecting the timer (agy row on stop leaks).
    const generation = this.stopGeneration || 0;
    const renewInMs = Math.max(10, (expiresInSec - 120)) * 1000;
    this.tokenRenewTimer = setTimeout(() => {
      if ((this.stopGeneration || 0) !== generation) return;
      if (this.isAnonymous) {
        this.renewAnonymous().catch(err => {
          this.log('Token renewal warning: ' + (err.message || err));
        });
      }
    }, renewInMs);
  }

  async initSource() {
    // The live source starts with the real worker register endpoint. The lease
    // endpoint is never probed: a probe would claim a lease and drop it.
    const urlParams = typeof window !== 'undefined' ? new URLSearchParams(window.location.search) : null;
    if (urlParams && urlParams.get('mock') === '1') {
      this.sourceMode = 'MOCK';
      this.sourceReason = 'mock=1 requested';
      this.render();
      return;
    }

    try {
      // The anonymous route is used only when this tab has no contributor token
      // and runner-config says anonymousJoin. A logged-in contributor registers
      // exactly as before, with no call to /workers/anonymous.
      if (!this.authToken && this.anonymousJoin === true) {
        await this.joinAnonymous();
      }
      const worker = await this.registerWorker();
      this.sourceMode = 'LIVE';
      this.sourceReason = 'registered as ' + worker.tabId;
      this.log(`Registered with the pool server. Server issued tab id: ${worker.tabId}`);
    } catch (err) {
      const status = err && err.status;
      if (status === 401 || status === 403) {
        // An authentication or authorization failure is never hidden behind MOCK.
        this.sourceMode = 'ERROR';
        this.sourceReason = 'registration rejected with HTTP ' + status;
        this.log('ERROR: the server rejected the registration (HTTP ' + status + '). Fix the credentials, MOCK is not used as a cover.');
      } else if (status === 429) {
        this.sourceMode = 'ERROR';
        this.sourceReason = 'quota exceeded (HTTP 429)';
        this.log('ERROR: anonymous contributor quota exceeded (HTTP 429).');
      } else if (status) {
        this.sourceMode = 'ERROR';
        this.sourceReason = 'registration failed with HTTP ' + status;
        this.log('ERROR: registration failed with HTTP ' + status);
      } else {
        // The server is not reachable: the page stays usable in MOCK mode.
        this.sourceMode = 'MOCK';
        this.sourceReason = 'pool server not reachable';
        this.log('Pool server not reachable, running in MOCK mode.');
      }
    }
    this.render();
  }

  /**
   * Register this tab as a worker and keep the server issued tab id.
   *
   * The real route is POST /{pool}/v1/workers/register (internal/api/api.go). The
   * server generates the tab id in Engine.Register, so a locally generated id would
   * be unknown to the server and every claim would fail.
   *
   * @returns {Promise<Object>} the worker record of the server
   */
  gpuAllowed() {
    // R10-FIX (minimax MINOR): the owner rule is "no GPU under 20 % battery"
    // (batteryAllowsGPU), not "no GPU on a battery at all". The old guard never
    // read the charge level, so a laptop at 60 % lost its GPU lanes while the
    // shared budget still counted them in the tab ceiling.
    return !this.backoff && batteryAllowsGPU({
      hasBattery: this.hasBattery,
      level: this.batteryLevel,
      isCharging: this.isCharging,
      isPhone: this.isPhone || this.options.isLaptop === true
    });
  }

  updateGPUGuard() {
    this.port?.postMessage({type:'gpu-guard', allowed:this.gpuAllowed() && this.isRunning});
    // T104 step 8: the guard can take the GPU lanes away (low battery, backoff),
    // and the shared budget must hear about it at once.
    this.applyGpuBudgetClaim();
    // T112 (deepseek): only a LIVE tab registers. A MOCK/ERROR tab never sets the
    // flag, so its claim loop is not stalled; a stopped tab registers on start.
    if (this.sourceMode !== 'LIVE') return;
    this.registrationPending = true;
    if (this.isRunning && !this.activeTasks.size) {
      this.registerWorker().catch(err => this.log('GPU registration: ' + err.message));
    }
  }

  setBackoff(value) {
    if (this.backoff === Boolean(value)) return;
    this.backoff = Boolean(value);
    this.updateGPUGuard();
  }

  /**
   * Single-flight registration (W7 review): updateGPUGuard, renewal and init
   * may ask at the same time; they share one POST /workers/register.
   *
   * @returns {Promise<Object>}
   */
  registerWorker() {
    if (this.registerPromise) return this.registerPromise;
    this.registerPromise = this.registerWorkerOnce()
      .finally(() => { this.registerPromise = null; });
    return this.registerPromise;
  }

  async registerWorkerOnce() {
    const slots = Math.max(1, Math.min(computeWorkerCap(this.hwConcurrency), this.workerCount));
    this.gpus = [];
    if (this.gpuAllowed()) {
      const adapters = await createGPUPicker().enumerate(this.options.gpu);
      for (const adapter of adapters) {
        try {
          const device = await adapter.createDevice();
          device.destroy?.();
          this.gpus.push(adapter);
        } catch (err) {
          // An adapter that cannot open a device is not registered; say so, or
          // the tab silently registers fewer GPU lanes than it discovered.
          this.log(`GPU adapter rejected: ${err && err.message ? err.message : String(err)}`);
        }
        if (this.gpus.length === 2) break;
      }
    }
    const gpuSlots = this.gpus.length;
    // T104 (dual pools): one register body for every pool of this tab. The
    // first pool is the primary: a failure there is the old single-pool failure
    // and stays fatal (no MOCK cover, WO_T96B rules). A secondary pool that
    // refuses keeps the tab alive on the pools that did register; the claim
    // round-robin skips it until a later registration picks it up.
    const body = JSON.stringify({
      hostId: this.resolveHostId(),
      // R10-FIX (chatgpt MAJOR): the device-level hostId is shared by every tab
      // of the origin; the session-scoped id tells the server which tab this is.
      browserTabId: this.browserTabId || '',
      ...(this.egressConsentIDs?.length ? {egressConsent:true,egressConsentIds:this.egressConsentIDs} : {}),
      // R10-FIX (owner D-W10-9): the id of the runner this tab is actually
      // driving, so the server can refund a stale runner's leases to the task.
      runnerPin: this.runnerPin || '',
      slots,
      gpu: gpuSlots > 0,
      gpuSlots,
      gpus: this.gpus.map(({vendor,architecture}) => ({vendor,architecture}))
    });
    const tabIds = {};
    const controlsByPool = {};
    for (const pool of this.pools) {
      const res = await fetch(`${this.poolBase(pool)}/workers/register`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...this.getAuthHeaders() },
        body
      });
      if (!res.ok) {
        const err = new Error('registration failed with HTTP ' + res.status);
        err.status = res.status;
        err.pool = pool;
        if (pool === this.pools[0]) throw err;
        this.log(`Pool ${pool} registration rejected (HTTP ${res.status}); this tab stays on the other pool(s).`);
        continue;
      }
      const worker = await res.json();
      if (!worker || typeof worker.tabId !== 'string' || !worker.tabId) {
        const err = new Error('registration returned no tab id');
        err.status = res.status;
        err.pool = pool;
        if (pool === this.pools[0]) throw err;
        this.log(`Pool ${pool} registration returned no tab id; this tab stays on the other pool(s).`);
        continue;
      }
      tabIds[pool] = worker.tabId;
      // W10D row 3 (D-W10-11): the register reply carries the pool's mode. A
      // server of an earlier round omits it, which keeps the default off.
      if (typeof worker.contributorControls === 'string') controlsByPool[pool] = worker.contributorControls;
    }
    const primary = tabIds[this.pools[0]] || Object.values(tabIds)[0] || null;
    if (!primary) {
      const err = new Error('registration returned no tab id');
      throw err;
    }
    this.registrationPending = false;
    this.needsRegistration = false;
    this.tabIds = tabIds;
    this.registeredTabId = primary;
    this.tabId = primary;
    // W10D row 3: the primary pool decides the mode of this tab. boot is true
    // only for the first reply, so startStopOptIn opens stopped; every later
    // reply is a live change that applies to the current run.
    const primaryPool = tabIds[this.pools[0]] ? this.pools[0] : Object.keys(tabIds)[0];
    const serverControls = primaryPool ? controlsByPool[primaryPool] : null;
    if (typeof serverControls === 'string') {
      this.applyContributorControls(serverControls, { boot: !this.contributorControlsSeen });
      this.contributorControlsSeen = true;
    }
    // T104 step 8: the GPU lane count changed with this registration, so the
    // GPU claim is reconciled now (a stop() released it; a fresh adapter set
    // claims again).
    this.applyGpuBudgetClaim();
    return { tabId: primary, tabIds, pools: Object.keys(tabIds) };
  }

  initUpkeepWorker() {
    // WWPP-R31.1: 1 upkeep worker on the demo origin for keepalive & lease renewal
    // Prevents timer suspension in background tabs
    const upkeepCode = `
      let intervalId = null;
      self.onmessage = (e) => {
        if (e.data === 'start') {
          if (!intervalId) {
            intervalId = setInterval(() => {
              postMessage({ type: 'tick', now: Date.now() });
            }, 1000);
          }
        } else if (e.data === 'stop') {
          if (intervalId) { clearInterval(intervalId); intervalId = null; }
        }
      };
    `;
    try {
      const blob = new Blob([upkeepCode], { type: 'application/javascript' });
      this.upkeepWorkerUrl = URL.createObjectURL(blob);
      this.upkeepWorker = new Worker(this.upkeepWorkerUrl);
      this.upkeepWorker.onmessage = (e) => {
        if (e.data && e.data.type === 'tick') {
          this.setBackoff(Date.now() - e.data.now > 2000);
          this.onUpkeepTick();
        }
      };
    } catch (_) {
      // Worker blocked or unsupported, fallback to main thread timer
    }
  }

  onUpkeepTick() {
    if (!this.isRunning) {
      // G-F12-3: heartbeats continue while a Dew result is outstanding, so the
      // tab stays registered. Once the receipt expires, the idle release runs.
      if (!this.dewResultPending()) {
        if (this.dewIdlePaused) this.releaseDewHold();
        return;
      }
      this.dewIdlePaused = true;
      this.sendHeartbeat().catch(() => {});
      return;
    }
    // Calculate throughput rate
    if (this.stats.startTime) {
      const elapsedSec = (Date.now() - this.stats.startTime) / 1000;
      if (elapsedSec > 0) {
        this.stats.ratePerSec = (this.stats.tasksDone / elapsedSec).toFixed(1);
      }
    }
    this.renderStats();
    // The upkeep worker also keeps the server side registration alive: the engine
    // refuses a claim when the heartbeat is older than two lease TTLs.
    this.sendHeartbeat().catch(() => {});
    // WO_T96B: renewAnonymous is single-flight and rate limited; the old direct
    // joinAnonymous call here fired once per tick for the last 120 s.
    // T112 (deepseek): no renewal while a server-failure or auth pause is active.
    const paused = this.authBlockedUntil && Date.now() < this.authBlockedUntil;
    if (!paused && this.isAnonymous && this.tokenExpiresAt && (this.tokenExpiresAt - Date.now() < 120000)) {
      this.renewAnonymous().catch(() => {});
    }
  }

  /**
   * Send the heartbeat of this tab to the server, renewing the current lease.
   *
   * The route is POST /{pool}/v1/workers/{tabId}/heartbeat (internal/worker/worker.go).
   *
   * @returns {Promise<void>}
   */
  async sendHeartbeat() {
    if (this.sourceMode !== 'LIVE' || !this.tabId) return;
    // R10-FIX (minimax MAJOR): the 429/5xx claim backoff must NOT stop the
    // keep-alive. The engine refuses a claim when the heartbeat is older than two
    // lease TTLs, so a tab that pauses its heartbeat for its own backoff reaps
    // itself and its in-flight leases die with it.
    // W7 fix (agy): the upkeep tick fires once per second and the server can be
    // slower than that. Without this guard, heartbeats overlap, two of them report
    // the same 401, and the recovery (renew + register) runs twice. One request at
    // a time; the next tick simply finds the guard set and skips its turn.
    if (this.heartbeatInFlight) return;
    this.heartbeatInFlight = true;
    // T104 (dual pools): every registered tab of this host gets its keep-alive
    // on its own pool, so no pool reaps a tab while the other pool computes.
    // R10-FIX (minimax MAJOR): the old body renewed only the lease of the LAST
    // claim, so every other job in flight lost its lease and the server
    // reclaimed the task under a tab that was still computing it. One request per
    // in-flight lease, plus a plain keep-alive for a pool with no lease.
    const targets = Object.entries(this.tabIds);
    if (targets.length === 0) targets.push(['tp', this.tabId]);
    const requests = [];
    for (const [pool, tabId] of targets) {
      const leases = [];
      for (const ctx of this.activeTasks.values()) {
        if (ctx && ctx.pool === pool && ctx.lease) leases.push(ctx);
      }
      if (leases.length === 0) requests.push({ pool, tabId, lease: null });
      else for (const context of leases) requests.push({ pool, tabId, lease:context.lease, context });
    }
    try {
      for (const { pool, tabId, lease, context } of requests) {
        try {
          // F10b-12: refresh a full minute before expiry so one slow heartbeat
          // cycle cannot let the credential die during a long task.
          const body = lease
            ? { lease, browserTabId: this.browserTabId || '', runnerPin: this.runnerPin || '', ...(context?.egress && context.egress.expiresAt*1000-Date.now()<60000 ? {egressRefresh:true} : {}) }
            : { browserTabId: this.browserTabId || '', runnerPin: this.runnerPin || '' };
          const tokenAtStart = this.authToken;
          const res = await fetch(`${this.poolBase(pool)}/workers/${tabId}/heartbeat`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json', ...this.getAuthHeaders() },
            body: JSON.stringify(body)
          });
          // W7: a 401/403 for a token replaced mid-flight is stale (renewal is single use).
          if (res && (res.status === 401 || res.status === 403) && this.authToken === tokenAtStart) {
            await this.onAuthFailure(res.status);
          } else if (res && (res.status === 429 || res.status >= 500)) {
            this.pauseAfterServerFailure(res.status, res);
          } else if (res && res.ok && typeof res.json === 'function') {
            // W10D row 3: the heartbeat reply carries the pool's current mode,
            // so a Director change applies on the next heartbeat. A body that is
            // empty or of an earlier shape leaves the mode exactly as it was.
            try {
              const reply = await res.json();
              if (reply?.egress && context?.egress && reply.egress.consentId === context.egress.consentId &&
                  reply.egress.expiresAt > context.egress.expiresAt) {
                const refreshed = {token:reply.egress.token,expiresAt:reply.egress.expiresAt};
                context.egress = {...context.egress,...refreshed};
                this.port?.postMessage({type:'egress-refresh',nonce:this.runnerNonce,jobId:lease.jobId,taskId:lease.taskId,egress:refreshed});
              }
              if (reply && typeof reply.contributorControls === 'string') {
                this.applyContributorControls(reply.contributorControls);
              }
            } catch (_) { /* an empty or opaque body is not a setting */ }
          }
        } catch (e) {
          this.log('Heartbeat failed: ' + (e.message || e));
        }
      }
    } finally {
      this.heartbeatInFlight = false;
    }
  }

  setupRunnerFrame() {
    const frame = this.dom.runnerFrame;
    if (!frame) return;

    this.runnerNonce = hexOf(globalThis.crypto.getRandomValues(new Uint8Array(16)));

    // Listen on MessagePort (WWPP-R39.1: MessageChannel handshake)
    const onPortMessage = (ev) => {
      this.handleRunnerPortMessage(ev.data);
    };
    const openChannel = () => {
      const channel = new MessageChannel();
      channel.port1.onmessage = onPortMessage;
      return channel;
    };
    const closeChannel = (channel) => {
      if (!channel) return;
      for (const port of [channel.port1, channel.port2]) {
        try {
          if (port && typeof port.close === 'function') port.close();
        } catch (_) {}
      }
    };
    this.channel = openChannel();
    this.port = this.channel.port1;

    let attempts = 0;
    const sendInit = () => {
      // The port is only handed to the pinned runner page. The target origin has
      // to stay '*' because the runner runs in an opaque sandbox whose origin is
      // 'null': a specific target origin would never match. The pin on the frame
      // source plus the nonce handshake is what protects the port.
      if (!this.isRunnerFramePinned(frame)) {
        this.log('Runner frame is not the pinned runner.html, handshake refused');
        return false;
      }
      // Every attempt uses a FRESH port pair: a transfer that was sent before the
      // runner installed its listener is lost together with its port, and the
      // runner accepts exactly one bootstrap port.
      closeChannel(this.channel);
      const channel = openChannel();
      this.channel = channel;
      this.port = channel.port1;
      this.log('Runner iframe loaded. Initiating MessageChannel handshake...');
      try {
        frame.contentWindow.postMessage(
          { type: 'init', nonce: this.runnerNonce },
          '*',
          [channel.port2]
        );
        return true;
      } catch (err) {
        this.log('Error transferring port to runner: ' + err.message);
        return false;
      }
    };

    // The load event can fire before this listener is attached, and a frame of
    // another origin cannot be probed through contentDocument. The handshake is
    // therefore retried for a short while. The runner accepts only the FIRST
    // init, so a retry after a lost race is simply refused by the runner.
    const retry = () => {
      if (this.runnerReady) return;
      attempts++;
      sendInit();
      if (!this.runnerReady && attempts < 20) {
        this.runnerRetryTimer = setTimeout(retry, 250);
      } else {
        this.runnerRetryTimer = null;
      }
    };

    // Fix early-load race: check if iframe document is already ready, or await load
    frame.addEventListener('load', () => {
      sendInit();
    });

    try {
      if (frame.contentDocument && (frame.contentDocument.readyState === 'complete' || frame.contentDocument.readyState === 'interactive')) {
        sendInit();
      }
    } catch (_) {}

    // The retry starts in every case: it covers the lost load event and the
    // cross origin frame that cannot be probed.
    this.runnerRetryTimer = setTimeout(retry, 0);

    // Per WWPP-R39.1: ignore any window message after initialization
    if (typeof window !== 'undefined') {
      window.addEventListener('message', (ev) => {
        // Do not accept results over window.postMessage; strictly dead channel
      });
    }
  }

  disposeRunnerFrame() {
    if (this.runnerRetryTimer) {
      clearTimeout(this.runnerRetryTimer);
      this.runnerRetryTimer = null;
    }
    if (this.channel) {
      for (const port of [this.channel.port1, this.channel.port2]) {
        try {
          if (port && typeof port.close === 'function') port.close();
        } catch (_) {}
      }
      this.channel = null;
      this.port = null;
    }
  }

  /**
   * Report whether the runner frame is the pinned runner.html of this page.
   *
   * Accepted are:
   *  - a relative path that ends in runner.html and does not climb out of the
   *    page directory (the shipped configuration: the iframe src is "runner.html"),
   *  - an absolute URL on the origin of the page itself,
   *  - an absolute URL on an origin the owner pinned explicitly, for the
   *    deployment that serves the runner from a separate ccgsc-run origin.
   *
   * Everything else is refused, so the port is never handed to a page of an
   * unknown origin.
   *
   * @param {Object} frame
   * @returns {boolean}
   */
  isRunnerFramePinned(frame) {
    if (!frame) return false;
    const src = typeof frame.getAttribute === 'function' ? frame.getAttribute('src') : frame.src;
    if (typeof src !== 'string' || !src) return false;
    const clean = src.split('?')[0].split('#')[0];
    if (!clean) return false;

    const leaf = clean.split('/').pop();
    if (leaf !== 'runner.html') return false;

    const isAbsolute = clean.includes('://') || clean.startsWith('//');
    if (!isAbsolute) {
      // A relative path: it must not climb out of the page directory.
      if (clean.startsWith('/')) return false;
      if (clean.split('/').includes('..')) return false;
      return true;
    }

    let origin;
    try {
      origin = new URL(clean, typeof location !== 'undefined' ? location.href : 'http://localhost/').origin;
    } catch (_) {
      return false;
    }
    const pageOrigin = (typeof location !== 'undefined' && location.origin) || '';
    if (pageOrigin && origin === pageOrigin) return true;
    return Array.isArray(this.pinnedRunnerOrigins) && this.pinnedRunnerOrigins.includes(origin);
  }

  handleRunnerPortMessage(data) {
    if (!data || typeof data !== 'object') return;

    // Runtime schema validation: an invalid message from the sandbox is dropped.
    const schema = validateMessageSchema(data);
    if (!schema.valid) {
      this.log(`DROPPED invalid runner message: ${schema.error}`);
      return;
    }

    if (data.type === 'ready') {
      if (data.nonce === this.runnerNonce) {
        this.runnerReady = true;
        this.runnerBuildId = data.buildId || null;
        // R10-FIX owner D-W10-9: the runner self-reports the pin of the page it
        // was actually loaded from. Keep it and forward it on register and
        // heartbeat; the server refunds a task whose lease went to a stale runner.
        this.runnerPin = typeof data.runnerPin === 'string' ? data.runnerPin : '';
        this.log(`Runner handshake READY. Sandbox origin: ${data.selfOrigin || 'opaque (null)'}${data.buildId ? ' build: ' + data.buildId : ''}${this.runnerPin ? ' pin: ' + this.runnerPin : ' pin: none'}`);
        this.render();
        // The pin rides every later register and heartbeat. If the registration
        // already went out without it, the next heartbeat carries it, so no extra
        // registration is forced here (that could loop on a busy registration).
        if (this.isRunning) {
          this.scheduleNextTask();
        }
      } else {
        this.log('Runner handshake failed: nonce mismatch');
      }
      return;
    }

    if (data.type === 'result') {
      this.onTaskResult(data);
      return;
    }

    if (data.type === 'check') {
      this.log(`CHECK ${data.name}: ${data.status} ${data.detail || ''}`);
      return;
    }

    if (data.type === 'error') {
      this.log(`RUNNER ERROR: ${data.msg}`);
      return;
    }
  }

  async start() {
    if (this.isRunning) return;
    this.isRunning = true;
    // R10-FIX (minimax MAJOR): a stopped tab sends no heartbeat, so the engine may
    // have reaped it (a layout switch stops and starts the same tab). Register
    // again before the first claim, otherwise every claim answers 403.
    if (this.sourceMode === 'LIVE' && this.needsRegistration) {
      this.needsRegistration = false;
      this.registrationPending = true;
    }
    // A start is an explicit resume: the pause of the contributor controls does
    // not survive a stop/start cycle.
    this.claimsPaused = false;
    this.stats.startTime = Date.now();
    this.log(`Compute STARTED. Source=${this.sourceMode}, Workers=${this.workerCount}`);

    // T104 (one worker budget per tab): claim the CPU worker slots of the new
    // pools inside the shared budget of the integrated page, if one is wired.
    // T104 step 8: the GPU lanes claim under their own owner as well, so the
    // whole tab (CPU + GPU) stays inside one budget.
    this.applyBudgetClaim();
    this.applyGpuBudgetClaim();

    // The upkeep worker was released by the last stop(): it is created again.
    if (!this.upkeepWorker) {
      this.initUpkeepWorker();
    }
    if (this.upkeepWorker) {
      this.upkeepWorker.postMessage('start');
    }

    this.render();
    this.scheduleNextTask();
  }

  stop() {
    // W7 fix (agy, WO finding 4): the cleanup below is unconditional. With
    // ?autostart=0, init() has already created the upkeep worker and a renewal may
    // have scheduled tokenRenewTimer while isRunning is still false; the old early
    // return left both alive on a stopped instance, and a renewal that completed
    // after this call re-armed the timer on a tab that never started.
    const wasRunning = this.isRunning;
    this.isRunning = false;
    this.stopGeneration = (this.stopGeneration || 0) + 1;
    // G-F12-3: an open Dew receipt pauses idle. New claims stop, but the budget
    // and the heartbeat stay until the result is accepted or the receipt expires.
    const dewHold = this.dewResultPending();
    this.dewIdlePaused = dewHold;
    // R10-FIX (minimax MAJOR): the heartbeat stops with the run, so the engine may
    // reap this tab while it is idle. The next start() registers again.
    // A Dew hold keeps heartbeats, so the registration stays.
    if (wasRunning && this.sourceMode === 'LIVE' && !dewHold) this.needsRegistration = true;
    if (wasRunning) this.log('Compute STOPPED.');
    // T104: the budget claim of the new pools is released with the run, so the
    // old lanes can use every slot while this tab is idle. Step 8: the GPU lane
    // claim is released with it, one budget for the whole tab.
    if (!dewHold) {
      this.releaseBudgetClaim();
      this.releaseGpuBudgetClaim();
      this.port?.postMessage({type:'gpu-guard', allowed:false});
    }

    if (this.tokenRenewTimer) {
      clearTimeout(this.tokenRenewTimer);
      this.tokenRenewTimer = null;
    }
    if (this.egressReloadTimer) {
      clearInterval(this.egressReloadTimer);
      this.egressReloadTimer = null;
    }
    // W7 review (Gemini): stop() then start() within 1 s ran two claim loops.
    if (this.taskTimer) {
      clearTimeout(this.taskTimer);
      this.taskTimer = null;
    }
    if (this.runnerRetryTimer) {
      clearTimeout(this.runnerRetryTimer);
      this.runnerRetryTimer = null;
    }

    if (!dewHold) {
      if (this.upkeepWorker) {
        this.upkeepWorker.postMessage('stop');
      }
      // The upkeep worker and its blob url are released with the run.
      this.releaseUpkeepWorker();
    }

    if (wasRunning) this.render();
  }

  /**
   * Terminate the upkeep worker and revoke its blob url.
   */
  releaseUpkeepWorker() {
    if (this.upkeepWorker) {
      try { this.upkeepWorker.terminate(); } catch (_) {}
      this.upkeepWorker = null;
    }
    if (this.upkeepWorkerUrl && typeof URL !== 'undefined' && URL.revokeObjectURL) {
      try { URL.revokeObjectURL(this.upkeepWorkerUrl); } catch (_) {}
      this.upkeepWorkerUrl = null;
    }
  }

  // ------------------------------------------------------------ Contributor controls (D-W10-11)

  /**
   * The capacity the slider may offer: the worker cap of this device, inside the
   * shared budget of the tab when the host wired one.
   *
   * @returns {number}
   */
  contributorCapacityMax() {
    let max = Math.max(1, computeWorkerCap(this.hwConcurrency));
    const budget = this.budgetRef();
    if (budget && typeof budget.total === 'function') {
      const total = Math.floor(Number(budget.total()));
      if (Number.isFinite(total) && total >= 1) max = Math.min(max, total);
    }
    return Math.max(1, max);
  }

  /**
   * The current view of the control section.
   *
   * @returns {Object}
   */
  contributorControlState() {
    return contributorControlView({
      mode: this.contributorControls,
      isRunning: this.isRunning,
      claimsPaused: this.claimsPaused,
      workerCount: this.workerCount,
      capacityMax: this.contributorCapacityMax()
    });
  }

  /**
   * Apply a mode read at boot or on a heartbeat.
   *
   * off            : remove the section and keep today's page.
   * pauseResume    : auto-start (unless ?autostart=0), Pause/Resume + capacity.
   * startStopOptIn : the page opens stopped, Start/Stop + capacity.
   *
   * A live change never interrupts an in-flight task: it only changes what the
   * next claim does. An unknown mode is off, so it can never add a control.
   *
   * @param {*} value
   * @param {Object} [options]
   * @param {boolean} [options.boot] the first reply of this page visit
   * @returns {string} the applied mode
   */
  applyContributorControls(value, options = {}) {
    const mode = normalizeContributorControls(value);
    const boot = options.boot === true;
    const changed = mode !== this.contributorControls;
    const wasPaused = this.claimsPaused;
    this.contributorControls = mode;
    if (changed) this.log(`Contributor controls: ${mode}`);
    // The pause exists only in pauseResume. A live change to any other mode
    // lifts it, otherwise a tab would stay silent under a mode with no Pause.
    if (changed && mode !== 'pauseResume') this.claimsPaused = false;
    if (mode === DEFAULT_CONTRIBUTOR_CONTROLS) {
      this.claimsPaused = false;
      this.contributorOptInStopped = false;
      this.removeContributorControls(this.documentRef());
      if (changed && !boot && this.autoStart && !this.isRunning && this.sourceMode !== 'ERROR') {
        this.start().catch(() => {});
      } else if (changed && wasPaused && this.isRunning) {
        this.scheduleNextTask();
      }
      return mode;
    }
    if (mode === 'startStopOptIn' && boot) {
      // D-W10-11: the page opens stopped in this mode, even though auto-start
      // is on; only the Start button starts this tab.
      this.contributorOptInStopped = true;
    } else if (mode === 'pauseResume') {
      this.contributorOptInStopped = false;
      if (changed && !boot && this.autoStart && !this.isRunning && this.sourceMode !== 'ERROR') {
        this.start().catch(() => {});
      }
    }
    this.renderContributorControls();
    if (changed && wasPaused && this.isRunning) this.scheduleNextTask();
    return mode;
  }

  /**
   * Pause from the pauseResume section: no new leases. The running task finishes
   * and submits, and the tab keeps its worker (tab) id.
   */
  pauseContributorWork() {
    this.claimsPaused = true;
    this.log('Contributor controls: PAUSED. No new leases; the running task finishes and submits.');
    this.renderContributorControls();
  }

  /**
   * Resume from the pauseResume section.
   */
  resumeContributorWork() {
    this.claimsPaused = false;
    this.log('Contributor controls: RESUMED.');
    if (!this.isRunning) {
      this.start().catch(() => {});
    } else {
      this.scheduleNextTask();
    }
    this.renderContributorControls();
  }

  /**
   * Start from the startStopOptIn section.
   */
  startContributorWork() {
    this.contributorOptInStopped = false;
    this.claimsPaused = false;
    this.start().catch(() => {});
    this.renderContributorControls();
  }

  /**
   * Stop from the startStopOptIn section. The run stops; the tab id and the
   * registration stay, so Start resumes the same worker.
   */
  stopContributorWork() {
    this.contributorOptInStopped = true;
    this.stop();
    this.renderContributorControls();
  }

  /**
   * The one action button of the section. The view decides which action the
   * current mode and run state offer.
   */
  onContributorControlAction() {
    const view = this.contributorControlState();
    if (view.mode === 'pauseResume') {
      if (view.action === 'pause') this.pauseContributorWork();
      else this.resumeContributorWork();
      return;
    }
    if (view.mode === 'startStopOptIn') {
      if (view.action === 'stop') this.stopContributorWork();
      else this.startContributorWork();
    }
  }

  /**
   * The capacity slider: the worker count stays inside the device cap and the
   * shared budget of the tab.
   *
   * @param {*} value
   */
  onContributorCapacity(value) {
    const max = this.contributorCapacityMax();
    const count = Math.max(1, Math.min(max, Math.floor(Number(value)) || 1));
    this.manualOverride = true;
    this.workerCount = count;
    this.log(`Contributor controls: capacity set to ${count} worker(s).`);
    this.applyBudgetClaim();
    this.render();
  }

  /**
   * Create and attach the section when a mode needs it. The section is created
   * by this module, so a page with the default off carries no control markup.
   *
   * @param {Object} doc
   * @returns {Object|null}
   */
  ensureContributorControls(doc) {
    if (!doc || typeof doc.createElement !== 'function') return null;
    const existing = typeof doc.getElementById === 'function' ? doc.getElementById(CONTRIBUTOR_CONTROLS_ID) : null;
    if (this.controlElements && existing && this.controlElements.section === existing) return existing;
    if (existing && typeof existing.querySelector === 'function') {
      const status = existing.querySelector(`#${CONTRIBUTOR_CONTROL_STATUS_ID}`);
      const button = existing.querySelector(`#${CONTRIBUTOR_CONTROL_ACTION_ID}`);
      const slider = existing.querySelector(`#${CONTRIBUTOR_CONTROL_CAPACITY_ID}`);
      const capacityValue = existing.querySelector(`#${CONTRIBUTOR_CONTROL_CAPACITY_VALUE_ID}`);
      if (status && button && slider) {
        this.controlElements = { section: existing, status, button, slider, capacityValue };
        this.bindContributorControls();
        return existing;
      }
    }
    const section = doc.createElement('section');
    section.id = CONTRIBUTOR_CONTROLS_ID;
    section.className = 'control-panel';
    const title = doc.createElement('h2');
    title.textContent = 'Contributor controls';
    const button = doc.createElement('button');
    button.id = CONTRIBUTOR_CONTROL_ACTION_ID;
    button.type = 'button';
    button.className = 'btn btn-primary';
    const status = doc.createElement('p');
    status.id = CONTRIBUTOR_CONTROL_STATUS_ID;
    if (typeof status.setAttribute === 'function') status.setAttribute('role', 'status');
    const label = doc.createElement('label');
    if (typeof label.setAttribute === 'function') label.setAttribute('for', CONTRIBUTOR_CONTROL_CAPACITY_ID);
    label.textContent = 'Compute workers';
    const slider = doc.createElement('input');
    slider.id = CONTRIBUTOR_CONTROL_CAPACITY_ID;
    slider.type = 'range';
    slider.min = '1';
    slider.step = '1';
    const capacityValue = doc.createElement('span');
    capacityValue.id = CONTRIBUTOR_CONTROL_CAPACITY_VALUE_ID;
    section.appendChild(title);
    section.appendChild(button);
    section.appendChild(status);
    section.appendChild(label);
    section.appendChild(slider);
    section.appendChild(capacityValue);
    const anchor = typeof doc.getElementById === 'function' ? doc.getElementById('log-output') : null;
    const host = anchor && anchor.parentNode ? anchor.parentNode : null;
    if (host && typeof host.insertBefore === 'function') {
      host.insertBefore(section, anchor);
    } else if (doc.body && typeof doc.body.appendChild === 'function') {
      doc.body.appendChild(section);
    } else {
      return null;
    }
    this.controlElements = { section, status, button, slider, capacityValue };
    this.bindContributorControls();
    return section;
  }

  /**
   * Bind the one action button and the capacity slider once per created section.
   */
  bindContributorControls() {
    const refs = this.controlElements;
    if (!refs || refs.bound) return;
    if (typeof refs.button.addEventListener === 'function') {
      refs.button.addEventListener('click', () => this.onContributorControlAction());
    }
    if (typeof refs.slider.addEventListener === 'function') {
      const onCapacity = () => this.onContributorCapacity(refs.slider.value);
      refs.slider.addEventListener('input', onCapacity);
      refs.slider.addEventListener('change', onCapacity);
    }
    refs.bound = true;
  }

  /**
   * Update the section, or remove it completely for the default off.
   *
   * @returns {Object|null}
   */
  renderContributorControls() {
    const doc = this.documentRef();
    if (!doc) return null;
    const view = this.contributorControlState();
    if (!view.visible) {
      this.removeContributorControls(doc);
      return null;
    }
    const section = this.ensureContributorControls(doc);
    const refs = this.controlElements;
    if (!section || !refs) return null;
    const blocked = this.sourceMode === 'ERROR';
    if (refs.button) {
      refs.button.textContent = view.actionLabel;
      refs.button.disabled = blocked;
      if (typeof refs.button.setAttribute === 'function') {
        refs.button.setAttribute('aria-label', `${view.actionLabel} contributing`);
      }
    }
    if (refs.status) refs.status.textContent = view.status;
    if (refs.slider) {
      refs.slider.max = String(view.capacityMax);
      refs.slider.value = String(view.capacityValue);
      refs.slider.disabled = blocked;
    }
    if (refs.capacityValue) refs.capacityValue.textContent = `${view.capacityValue} / ${view.capacityMax}`;
    section.hidden = false;
    return section;
  }

  /**
   * Remove the control section. Used by the default off and by a live change
   * back to off: no control section exists at all then.
   *
   * @param {Object} [doc]
   */
  removeContributorControls(doc) {
    const target = this.controlElements ? this.controlElements.section : null;
    const section = target
      || (doc && typeof doc.getElementById === 'function' ? doc.getElementById(CONTRIBUTOR_CONTROLS_ID) : null);
    if (section) {
      const parent = section.parentNode;
      if (parent && typeof parent.removeChild === 'function') parent.removeChild(section);
      else section.hidden = true;
    }
    this.controlElements = null;
  }

  async scheduleNextTask() {
    if (!this.isRunning) return;
    // W10D row 3 (D-W10-11): a paused tab claims no new lease. The in-flight
    // task is untouched, still submits, and the tab keeps its worker id.
    if (this.claimsPaused) return;
    if (this.claiming || !this.port || !this.runnerReady || this.backoff) return;
    if (this.authBlockedUntil && Date.now() < this.authBlockedUntil) {
      clearTimeout(this.taskTimer);
      this.taskTimer = setTimeout(() => this.scheduleNextTask(), this.authBlockedUntil - Date.now());
      return;
    }
    this.claiming = true;
    try {
      if (this.registrationPending && !this.activeTasks.size && this.sourceMode === 'LIVE') await this.registerWorker();
      if (this.registrationPending) return;
      // T104 (one worker budget per tab): the CPU worker count is bounded by
      // the shared budget of the integrated page. T104 step 8: the GPU lanes
      // claim against the same budget, so the capacity of this tab is what the
      // budget actually holds for it - CPU slots under "pools", GPU lanes under
      // "pools:gpu" - and the sum of every lane of the tab stays within it.
      let cpuCapacity = Math.min(this.workerCount, computeWorkerCap(this.hwConcurrency));
      let gpuCapacity = this.gpuAllowed() ? Math.max(0, Math.min(2, this.gpus.length)) : 0;
      const budget = this.budgetRef();
      if (budget && typeof budget.remaining === 'function') {
        // R10-FIX (minimax MAJOR): reconcile the claim BEFORE the capacity. A lane
        // that freed its slots (the legacy lanes finished) must be available to the
        // pools again, and the pools must never run more jobs than they actually
        // hold: the capacity is bounded by the grant, not only by the free part of
        // the tab. The old code bounded by `remaining() + own`, which let the pools
        // dispatch work for slots another lane still held.
        this.applyBudgetClaim();
        this.applyGpuBudgetClaim();
        const usedBy = typeof budget.usedBy === 'function' ? budget.usedBy.bind(budget) : () => 0;
        const cpuGrant = usedBy(POOL_BUDGET_OWNER);
        const gpuGrant = usedBy(POOL_GPU_BUDGET_OWNER);
        cpuCapacity = Math.min(cpuCapacity, cpuGrant);
        gpuCapacity = Math.min(gpuCapacity, gpuGrant);
      }
      const capacity = cpuCapacity + gpuCapacity;
      while (this.isRunning && !this.backoff && !this.claimsPaused && this.activeTasks.size < capacity) {
        const job = await this.getNextJob();
        if (!job) break;
        const claimedPool = this.currentPool || 'tp';
        const context = {
          lease: this.currentLease,
          taskEnvelope: this.currentTaskEnvelope,
          leaseReceipt: this.currentLeaseReceipt,
          egress:job.egress,
          tabId: this.tabIds[claimedPool] || this.tabId,
          pool: this.currentPool
        };
        this.activeTasks.set(job.taskId, context);
        job.gpuAllowed = this.gpuAllowed();
        this.port.postMessage(job, [job.wasm]);
      }
    } catch (err) {
      this.log('Failed to fetch/prepare job: ' + (err.message || err));
      // W7 review (deepseek): a failed registration no longer re-POSTs every second.
      this.pauseAfterServerFailure(err && err.status ? err.status : 0);
    } finally {
      this.claiming = false;
      if (this.isRunning) {
        clearTimeout(this.taskTimer);
        const wait = Math.max(1000, (this.authBlockedUntil || 0) - Date.now());
        this.taskTimer = setTimeout(() => this.scheduleNextTask(), wait);
      }
    }
  }

  async getNextJob() {
    if (this.sourceMode === 'LIVE') {
      return this.getLiveJob();
    }
    if (this.sourceMode === 'ERROR') {
      // The registration failed: the page does not pretend to work.
      return null;
    }
    return this.getMockJob();
  }

  /**
   * Claim one lease from the live pool and turn it into a runner job.
   *
   * T104 (dual pools): the claim draws from the pool `poolArg` names or from
   * the round-robin cursor over the registered pools, and every follow-up call
   * of the lease (artifact fetch, failure report, result) uses the same pool
   * and tab. One registered pool answers exactly as in earlier rounds.
   *
   * @param {string} [poolArg] pool id to claim from ('tp' | 'pp')
   * @returns {Promise<Object|null>}
   */
  async getLiveJob(poolArg) {
    // R11-M2: no error path may escape this method with a lease still held. The
    // lease is reported (and dropped) before the caller sees a null job, so a bad
    // lease can never leak to its TTL and stall the claim loop for minutes.
    try {
      return await this.prepareLiveJob(poolArg);
    } catch (err) {
      this.log('Lease preparation failed: ' + ((err && err.message) || err));
      await this.reportLeaseFailure('client_error');
      return null;
    }
  }

  async prepareLiveJob(poolArg) {
    const pool = poolArg || this.nextClaimPool();
    const base = this.poolBase(pool);
    const tabId = this.tabIds[pool] || this.tabId;
    let res;
    try {
      var leaseTokenAtStart = this.authToken;
      res = await fetch(`${base}/workers/${tabId}/leases`, {
        method: 'POST',
        headers: this.getAuthHeaders()
      });
    } catch (e) {
      this.log('Lease request failed: ' + (e.message || e));
      this.pauseAfterServerFailure(0);
      return null;
    }

    if (res.status === 204) {
      // No task currently in queue
      return null;
    }
    if (res.status === 401 || res.status === 403) {
      if (this.authToken === leaseTokenAtStart) await this.onAuthFailure(res.status);
      return null;
    }
    if (!res.ok) {
      this.log('Lease request rejected with HTTP ' + res.status);
      if (res.status === 429 || res.status >= 500) this.pauseAfterServerFailure(res.status, res);
      return null;
    }
    this.authFailures = 0;
    this.busyFailures = 0;

    const assignment = await res.json();
    this.currentPool = pool;
    this.currentLease = assignment.lease;
    this.currentTaskEnvelope = assignment.taskEnvelope;
    this.currentLeaseReceipt = assignment.leaseReceipt;
    if (!(await this.ensureAppRunner(assignment.egress || null))) {
      await this.reportLeaseFailure('app_runner_policy_unavailable');
      return null;
    }

    const signed = assignment.taskEnvelope;
    let payload = null;
    try {
      payload = signed && signed.payload
        ? (typeof signed.payload === 'string' ? JSON.parse(signed.payload) : signed.payload)
        : null;
    } catch (_) {
      payload = null;
    }
    if (!payload || typeof payload !== 'object' || Array.isArray(payload) || typeof signed.signature !== 'string') {
      // Without a signed task envelope the runner cannot verify the job.
      await this.reportLeaseFailure('task_envelope_missing');
      return null;
    }
    // R11-M2: decode the signature up front. A lease whose signature is not even
    // base64 used to throw out of this method after every artifact had been
    // fetched. The runner verifies the signature itself, so a value it cannot
    // verify is simply left for it to refuse.
    const envSigBytes = safeBase64ToBytes(signed.signature);
    // R11-M1: forward the signed payload VERBATIM. The server signs every field
    // it puts in the envelope (isolationMode, workload, leaseToken, ...); a
    // rebuilt whitelist drops them and the runner's canonicalJson no longer
    // matches the signed bytes, so every live task failed with envSig. The copy
    // keeps this page from mutating the response object it still holds.
    const envelope = { ...payload };
    // R10-FIX (minimax MAJOR): the runner result carries the task id but NOT the
    // pool, so two pools handing out the same task id overwrote each other in
    // activeTasks (the first result then travelled with the lease of the second).
    // A duplicate is released instead of dispatched; it also keeps activeTasks.size
    // honest, which is what bounds the claim loop.
    if (envelope.taskId && this.activeTasks.has(envelope.taskId)) {
      await this.reportLeaseFailure('duplicate_task_id');
      return null;
    }
    // D-W10-8: a multi-file workload is executable only when the signed envelope
    // pins a digest per file; the parent then fetches ALL of them and passes the
    // extra ones to the runner as ctx.files. Files the envelope does not pin are
    // never fetched, and a pinned file that does not arrive declines the task.
    const artifactIds = Array.isArray(assignment.moduleArtifactIds) ? assignment.moduleArtifactIds : [];
    if (artifactIds.length > MAX_WORKLOAD_FILES) {
      await this.reportLeaseFailure('workload_too_many_files');
      return null;
    }
    const declaredFiles = envelope.workload && Array.isArray(envelope.workload.files)
      ? envelope.workload.files
      : null;
    if (artifactIds.length > 1 && !declaredFiles) {
      await this.reportLeaseFailure('artifact_multi_file_requires_signed_envelope');
      return null;
    }
    if (!artifactIds[0]) {
      await this.reportLeaseFailure('module_artifact_missing');
      return null;
    }
    // R11-M2: the count is checked BEFORE the loop. A longer or shorter id list
    // used to read a missing entry (or drop a declared file); the mismatch now
    // declines the whole task, so no file is fetched for a job that cannot run.
    if (declaredFiles && artifactIds.length !== declaredFiles.length) {
      await this.reportLeaseFailure('artifact_file_count_mismatch');
      return null;
    }
    // R11-M3: every declared file must pin a digest, and the entry digest the
    // signature authorizes must be the entry the file list names. A missing
    // digest used to skip the check; it now fails closed.
    if (declaredFiles) {
      for (const file of declaredFiles) {
        if (!file || typeof file.path !== 'string' || !file.path
            || typeof file.sha256 !== 'string' || !/^[0-9a-f]{64}$/.test(file.sha256)) {
          await this.reportLeaseFailure('file_digest_missing');
          return null;
        }
      }
      if (!declaredFiles.some(file => file.sha256 === envelope.artifactSha256)) {
        await this.reportLeaseFailure('artifact_entry_digest_mismatch');
        return null;
      }
    } else if (typeof envelope.artifactSha256 !== 'string' || !/^[0-9a-f]{64}$/.test(envelope.artifactSha256)) {
      await this.reportLeaseFailure('artifact_digest_missing');
      return null;
    }

    // T104 step 6: a digest already in the artifact cache skips the fetch. The
    // cache is keyed by the sha256 the signed lease authorizes; the read
    // re-hashes the stored bytes, and the hash-on-load below runs on whatever
    // bytes are about to travel to the runner - the existing wasmSha256 gate and
    // the runner's own verifyEnvelope stay the authority. A miss fetches,
    // hashes and stores exactly like a lease of an earlier round.
    // W10c (D-W10-8): every declared file is fetched by its own artifact id and
    // checked against its own pinned digest before any of them is executed.
    const declaredDigests = declaredFiles ? declaredFiles.map(f => f.sha256) : [envelope.artifactSha256];
    // R11-M10: a null prototype map. `extraFiles['__proto__'] = buf` on a plain
    // object silently sets the prototype instead of a key, so the declared file
    // vanished and the runner saw one file fewer than the envelope pinned.
    const extraFiles = Object.create(null);
    let entryArtifact = null;
    let totalBytes = 0;
    for (let i = 0; i < artifactIds.length; i++) {
      const isEntry = i === 0;
      const declaredSha = declaredDigests[i];
      let artifactBuf = null;
      let artifactCached = false;
      const cacheable = this.artifactCache && typeof this.artifactCache.get === 'function'
        && typeof declaredSha === 'string' && /^[0-9a-f]{64}$/.test(declaredSha);
      if (cacheable) {
        try {
          // Finish Dash 2026-11-24: a corrupt cache is a miss, so the
          // authenticated server path below remains available in both pools.
          // Own the bytes before awaiting the hash; cache objects may alias.
          // The runner still checks the signed envelope before execution.
          const hit = await this.artifactCache.get(declaredSha);
          const isBytes = hit instanceof ArrayBuffer
            || (hit instanceof Uint8Array && hit.buffer instanceof ArrayBuffer);
          if (isBytes && hit.byteLength > 0 && hit.byteLength <= MAX_ARTIFACT_BYTES) {
            const owned = new Uint8Array(hit instanceof ArrayBuffer ? new Uint8Array(hit) : hit).buffer;
            if (await sha256Hex(owned) === declaredSha) {
              artifactBuf = owned;
              artifactCached = true;
            }
          }
        } catch (_) { /* a broken cache is only a lost speedup */ }
      }
      if (!artifactCached) {
        try {
          const artRes = await fetch(`${base}/artifacts/${artifactIds[i]}`, {
            headers: this.getAuthHeaders()
          });
          if (!artRes.ok) {
            await this.reportLeaseFailure('artifact_fetch_http_' + artRes.status);
            return null;
          }
          artifactBuf = await artRes.arrayBuffer();
        } catch (e) {
          await this.reportLeaseFailure('artifact_fetch_failed');
          return null;
        }
      }
      if (!artifactBuf || artifactBuf.byteLength === 0) {
        await this.reportLeaseFailure('artifact_empty');
        return null;
      }
      // R11-M5: the per-file cap stays; the total across the workload is the
      // separate (larger) MAX_TOTAL_WORKLOAD_BYTES bound.
      if (artifactBuf.byteLength > MAX_ARTIFACT_BYTES) {
        await this.reportLeaseFailure('artifact_too_large');
        return null;
      }
      totalBytes += artifactBuf.byteLength;
      if (totalBytes > MAX_TOTAL_WORKLOAD_BYTES) {
        await this.reportLeaseFailure('workload_too_large');
        return null;
      }

      // R11-M3: every byte that travels is hashed and checked. The digest that
      // authorizes it was already required above, so a mismatch always fails.
      const artifactSha = await sha256Hex(artifactBuf);
      // D-W10-8: a pinned file whose bytes disagree with its digest declines the
      // whole task before the first import.
      // R10-FIX (minimax MAJOR) + D-W10-8: the digest the signed envelope
      // authorizes is the authority for these bytes; a mismatch fails closed
      // before any import (single-file: envelope.artifactSha256).
      if (artifactSha !== declaredSha) {
        await this.reportLeaseFailure(declaredFiles ? 'artifact_sha256_mismatch' : 'artifact_digest_mismatch');
        return null;
      }
      if (isEntry && artifactSha !== envelope.artifactSha256) {
        // R11-M3: the entry module is the one the signature names, not merely
        // one of the declared files.
        await this.reportLeaseFailure('artifact_entry_digest_mismatch');
        return null;
      }
      if (!artifactCached && this.artifactCache && typeof this.artifactCache.put === 'function') {
        // Store under the digest of the bytes that were actually received, never
        // under the claimed one: only verified bytes can ever be served again.
        try { await this.artifactCache.put(artifactSha, artifactBuf.slice(0)); } catch (_) { /* speedup only */ }
      }
      if (isEntry) {
        entryArtifact = { buf: artifactBuf, sha256: artifactSha };
      } else {
        // R11-M10: a signed path is still only data. A prototype key never
        // becomes a key of the file map (Object.create(null) is the second gate).
        const path = declaredFiles[i].path;
        if (path === '__proto__' || path === 'constructor' || path === 'prototype') {
          await this.reportLeaseFailure('workload_file_path_invalid');
          return null;
        }
        extraFiles[path] = artifactBuf;
      }
    }

    return {
      type: 'job',
      ...(assignment.egress ? {egress:{
        consentId:assignment.egress.consentId, endpoints:assignment.egress.endpoints,
        token:assignment.egress.token, expiresAt:assignment.egress.expiresAt
      }} : {}),
      jobId: assignment.lease.jobId,
      taskId: assignment.lease.taskId,
      nonce: this.runnerNonce,
      // The module artifact of the job is the verified workload source. The
      // runner hashes these bytes against the envelope before it starts a Worker.
      wasm: entryArtifact.buf,
      wasmSha256: entryArtifact.sha256,
      // D-W10-8: the data/.wasm companions of the entry module. Each one was
      // fetched by its own artifact id and checked against its signed digest.
      ...(Object.keys(extraFiles).length ? { files: extraFiles } : {}),
      // D-W10-6 + R11-M1: the signed envelope is the authority. When it carries
      // an isolationMode, that is the mode; the page config field
      // (runner-config) is only the fallback for an older server that does not
      // sign one. The runner resolves it the same way (resolveIsolationMode).
      isolationMode: payload.isolationMode !== undefined
        ? normalizeIsolationMode(payload.isolationMode)
        : normalizeIsolationMode(this.runnerConfig?.isolationMode),
      // D-W10-10: the deployment's wasm-abi memory bound in pages. The runner
      // normalizes it (a hostile value can only lower the cap) and the signed
      // envelope wins when it carries one.
      wasmMaxPages: this.runnerConfig?.wasmMaxMemoryPages,
      envelope: envelope,
      envSig: envSigBytes,
      // The public key is NOT sent: the runner resolves it from its own pinned
      // operator key ring by the keyId of the envelope.
      needs: assignment.task?.needs || assignment.needs || 'cpu',
      workersRequested: 1,
      timeoutMs: (assignment.task && typeof assignment.task.timeoutMs === 'number' && assignment.task.timeoutMs > 0)
        ? assignment.task.timeoutMs
        : 30000,
      // Use exact signed input bytes when available; JSON transport may compact whitespace.
      inputJSON: assignment.inputJSON ?? JSON.stringify(assignment.task?.input ?? null)
    };
  }

  /**
   * Report a live lease failure to the server so the task is released and retried.
   * No result is fabricated: the error travels with the lease.
   *
   * @param {string} reason
   */
  async reportLeaseFailure(reason) {
    if (!this.currentLease) return;
    const pool = this.currentPool || 'tp';
    const base = this.poolBase(pool);
    const tabId = this.tabIds[pool] || this.tabId;
    // R10-FIX (minimax MAJOR): only THIS lease is released. Clearing the shared
    // field unconditionally dropped the lease of a job still in flight.
    const lease = this.currentLease;
    this.log('Lease failure reported to the server: ' + reason);
    try {
      // W7 fix (agy): a 401/403 here is a stale credential, not a task fault. It
      // must drive the same recovery as a lease or heartbeat failure, otherwise a
      // tab whose token expired mid-task keeps failing silently.
      const tokenAtStart = this.authToken;
      const res = await fetch(`${base}/workers/${tabId}/results`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...this.getAuthHeaders() },
        body: JSON.stringify({ lease, taskEnvelope: this.currentTaskEnvelope, leaseReceipt: this.currentLeaseReceipt, error: reason })
      });
      if (res && (res.status === 401 || res.status === 403) && this.authToken === tokenAtStart) {
        await this.onAuthFailure(res.status);
      } else if (res && (res.status === 429 || res.status >= 500)) {
        this.pauseAfterServerFailure(res.status, res);
      }
    } catch (e) {
      this.log('Failed to report the lease failure: ' + (e.message || e));
    } finally {
      if (this.currentLease === lease) this.currentLease = null;
    }
  }

  /**
   * Fetch a separate reference workload as runtime DATA. The platform never
   * imports or embeds the reference kernel (CR-53). When the origin cannot
   * serve the artifact, MOCK jobs are declined rather than running untrusted
   * or hardcoded fallback workload code.
   */
  async loadDemoArtifact() {
    const res = await fetch('/pools/mock-artifact', { cache: 'no-store' });
    if (!res.ok) throw new Error('mock_artifact_http_' + res.status);
    const data = await res.json();
    if (!data || typeof data.source !== 'string' || !data.source ||
        typeof data.sha256 !== 'string' || !/^[0-9a-f]{64}$/.test(data.sha256) ||
        typeof data.inputJSON !== 'string' || !data.inputJSON) {
      throw new Error('mock_artifact_invalid');
    }
    const bytes = new TextEncoder().encode(data.source);
    if (bytes.byteLength > MAX_ARTIFACT_BYTES ||
        await sha256Hex(bytes) !== data.sha256) {
      throw new Error('mock_artifact_sha256_mismatch');
    }
    return { bytes: bytes.buffer, sha256: data.sha256, inputJSON: data.inputJSON };
  }

  /**
   * Build a mock job from the separately delivered, byte-pinned reference
   * artifact and the example input supplied by its own workload manifest.
   * @returns {Promise<Object|null>}
   */
  async getMockJob() {
    if (!this.demoKeyPair) {
      // No key pair: an unsigned job would only produce a verify failure loop.
      this.log('MOCK path skipped: no demo signing key available');
      return null;
    }
    let demo;
    try {
      demo = await this.loadDemoArtifact();
    } catch (err) {
      this.log('MOCK artifact unavailable: ' + ((err && err.message) || err));
      return null;
    }
    const wasmCopy = demo.bytes;
    const wasmSha = demo.sha256;
    const inputJSON = demo.inputJSON;
    const inputSha = await sha256Hex(inputJSON);
    const jobId = 'mock-job-' + Math.random().toString(36).substring(2, 9);
    const taskId = 'task-' + Math.random().toString(36).substring(2, 9);
    const envelope = createTaskEnvelope(jobId, taskId, wasmSha, 3600, DEMO_OPERATOR_KEY.keyId, inputSha);
    const signed = await signTaskEnvelope(envelope, this.demoKeyPair);

    return {
      type: 'job',
      jobId: jobId,
      taskId: taskId,
      nonce: this.runnerNonce,
      wasm: wasmCopy,
      wasmSha256: wasmSha,
      envelope: envelope,
      envPlain: signed.envPlain,
      envSig: signed.envSig,
      // No public key is sent: the runner knows the demo operator key.
      needs: 'cpu',
      workersRequested: 1,
      inputJSON,
      timeoutMs: 30000
    };
  }

  /**
   * Submit one result to the pool the lease came from.
   *
   * R10-FIX (chatgpt MAJOR): the result used to be dropped whatever the server
   * answered (401/403/409/429/5xx): the lease and the task context were cleared
   * anyway, so the work was lost. The verdict is returned instead, and the lease
   * is released ONLY when the server accepted the result (2xx). A rejection keeps
   * both so onTaskResult() can retain and retry it.
   *
   * @param {Object} result the runner result
   * @returns {Promise<boolean>} whether the server accepted the result
   */
  async submitResult(result) {
    const context = this.activeTasks.get(result.taskId);
    const lease = context?.lease || this.currentLease;
    // T104 (dual pools): the result goes back to the pool the lease came from.
    const pool = (context && context.pool) || this.currentPool || 'tp';
    const base = this.poolBase(pool);
    const tabId = (context && context.tabId) || this.tabIds[pool] || this.tabId;
    if (this.sourceMode !== 'LIVE' || !lease) {
      // Nothing to submit (MOCK) or nothing to release: the task is still done.
      if (context) this.activeTasks.delete(result.taskId);
      if (lease && this.currentLease === lease) this.currentLease = null;
      return true;
    }
    try {
      const body = {
        lease,
        taskEnvelope: context?.taskEnvelope || this.currentTaskEnvelope,
        leaseReceipt: context?.leaseReceipt || this.currentLeaseReceipt,
        ...(result.gpu ? { gpu: result.gpu } : {}),
        output: result.output !== undefined ? result.output : null,
        sha256: result.output !== undefined ? await sha256Hex(new TextEncoder().encode(JSON.stringify(result.output))) : '',
        error: result.ok ? '' : (result.error || 'task_failed')
      };
      const computeMs = result && result.timings ? Number(result.timings.computeMs) : NaN;
      if (Number.isFinite(computeMs) && computeMs >= 0) body.computeMs = Math.round(computeMs);
      // W7 fix (agy): a 401/403 here is a stale credential, not a lost result:
      // the work is already computed, so the tab must recover the way it does
      // for a lease or heartbeat failure instead of dropping the result.
      const tokenAtStart = this.authToken;
      const res = await fetch(`${base}/workers/${tabId}/results`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          ...this.getAuthHeaders()
        },
        body: JSON.stringify(body)
      });
      if (res && (res.status === 401 || res.status === 403) && this.authToken === tokenAtStart) {
        await this.onAuthFailure(res.status);
      } else if (res && (res.status === 429 || res.status >= 500)) {
        this.pauseAfterServerFailure(res.status, res);
      }
      if (!res || res.ok !== true) {
        this.log(`Result for ${result.taskId} not acknowledged (HTTP ${res && res.status}); retaining its lease for retry.`);
        return false;
      }
      if (context) this.activeTasks.delete(result.taskId);
      if (this.currentLease === lease) this.currentLease = null;
      return true;
    } catch (e) {
      this.log('Failed to submit result to server: ' + (e.message || e));
      return false;
    }
  }

  /**
   * Settle the retained result, retrying until the server accepts it.
   *
   * R10-FIX (chatgpt MAJOR): the result survives a rejection. While it is
   * unsettled the task stays in activeTasks and the claim loop stays bounded by
   * it, so the tab cannot silently lose computed work or start a second job for
   * the same slot.
   *
   * @returns {Promise<void>}
   */
  async settlePendingResult() {
    const result = this.pendingSettlement;
    if (!result) return;
    if (this.settlementInFlight) return;
    this.settlementInFlight = true;
    let accepted = false;
    try {
      accepted = await this.submitResult(result);
    } finally {
      this.settlementInFlight = false;
    }
    if (!accepted) {
      if (this.isRunning) {
        if (this.taskTimer) clearTimeout(this.taskTimer);
        this.taskTimer = setTimeout(() => this.settlePendingResult(), 3000);
      }
      return;
    }
    if (this.pendingSettlement === result) this.pendingSettlement = null;
    this.releaseDewHold();
    await this.refreshContributorCounters();
    this.renderStats();
    if (this.isRunning) {
      clearTimeout(this.taskTimer);
      this.taskTimer = setTimeout(() => this.scheduleNextTask(), 10);
    }
  }

  onTaskResult(result) {
    if (result.verifyPass) {
      this.stats.verifyPass++;
    } else {
      this.stats.verifyFail++;
      this.log(`Task ${result.taskId} signature/hash verification FAILED: ${result.error || 'untrusted'}`);
    }

    if (result.ok) {
      this.stats.tasksDone++;
      const duration = (result.timings && result.timings.computeMs) || 1;
      this.stats.totalTaskMs += duration;
      this.stats.avgMsPerTask = Math.round(this.stats.totalTaskMs / this.stats.tasksDone);
      this.log(`Task ${result.taskId} DONE in ${duration.toFixed(1)}ms. Total done=${this.stats.tasksDone}`);
    }

    // R10-FIX (chatgpt MAJOR): the task context (lease, envelope, receipt) is NOT
    // dropped here. It is dropped by submitResult() when the server accepts the
    // result; until then the retained result keeps the lease renewed by the
    // heartbeat and the claim loop sees the slot as busy.
    this.pendingSettlement = result;
    this.renderStats();
    this.settlePendingResult().catch(() => {});
  }

  render() {
    // T104 (one status/log area): publish the state of the new pools as the
    // status chip of the shared area, next to the chips of the old lanes. This
    // runs before the DOM guard: the shared area renders through its own owner.
    const shared = this.sharedStatusLog();
    if (shared && typeof shared.setStatus === 'function') {
      shared.setStatus('pools', this.sourceMode === 'ERROR' ? 'ERROR' : (this.isRunning ? 'COMPUTING' : 'IDLE'));
    }
    // W10D row 3: the control section follows start/stop/pause and the capacity.
    this.renderContributorControls();
    if (!this.dom) return;

    if (this.dom.statusBadge) {
      this.dom.statusBadge.textContent = this.isRunning ? 'COMPUTING' : 'IDLE';
      this.dom.statusBadge.className = this.isRunning ? 'badge badge-success' : 'badge badge-dim';
    }

    if (this.dom.sourceBadge) {
      if (this.sourceMode === 'LIVE') {
        this.dom.sourceBadge.textContent = 'LIVE SERVER';
        this.dom.sourceBadge.className = 'badge badge-primary';
      } else if (this.sourceMode === 'ERROR') {
        this.dom.sourceBadge.textContent = 'ERROR';
        this.dom.sourceBadge.className = 'badge badge-danger';
      } else {
        this.dom.sourceBadge.textContent = 'MOCK';
        this.dom.sourceBadge.className = 'badge badge-warning';
      }
      if (this.sourceReason) {
        this.dom.sourceBadge.title = this.sourceReason;
      }
    }

    if (this.dom.leaderBadge) {
      if (this.degraded) {
        this.dom.leaderBadge.textContent = 'DEGRADED TAB';
        this.dom.leaderBadge.className = 'badge badge-warning';
      } else {
        this.dom.leaderBadge.textContent = this.isLeader ? 'LEADER TAB' : 'STANDBY TAB';
        this.dom.leaderBadge.className = this.isLeader ? 'badge badge-info' : 'badge badge-dim';
      }
    }

    if (this.dom.hwInfo) {
      const typeStr = this.isPhone ? 'Phone' : 'Desktop/Laptop';
      const powerStr = this.isPhone ? (this.isCharging ? ' (charging)' : ' (battery)') : '';
      this.dom.hwInfo.textContent = `${typeStr} • ${this.hwConcurrency} cores${powerStr} • Target workers: ${this.workerCount}`;
    }

    this.renderStats();
  }

  renderStats() {
    if (!this.dom) return;
    if (this.dom.statTasksDone) this.dom.statTasksDone.textContent = this.stats.tasksDone;
    if (this.dom.statAvgMs) this.dom.statAvgMs.textContent = `${this.stats.avgMsPerTask} ms`;
    if (this.dom.statRate) this.dom.statRate.textContent = `${this.stats.ratePerSec || 0} /s`;
    if (this.dom.statVerifyPass) this.dom.statVerifyPass.textContent = this.stats.verifyPass;
    if (this.dom.statVerifyFail) this.dom.statVerifyFail.textContent = this.stats.verifyFail;
    applyContributorCounterText({
      accepted: this.dom.statAcceptedTasks,
      compute: this.dom.statComputeMs
    }, this.contributorCountersKnown ? this.contributorCounters : null, !this.contributorCountersKnown);
  }

  /**
   * Read this contributor's durable counters. The install id, when the session
   * is anonymous, is the one this page keeps.
   *
   * @returns {Promise<void>}
   */
  async refreshContributorCounters() {
    if (!this.dom || (!this.dom.statAcceptedTasks && !this.dom.statComputeMs)) return;
    try {
      if (typeof fetch !== 'function') return;
      const hostId = this.resolveHostId();
      const headers = { ...this.getAuthHeaders() };
      if (this.isAnonymous === true && hostId) {
        headers['X-CCGSC-Install'] = hostId;
      }
      const res = await fetch(contributorCounterURL(hostId, this.isAnonymous === true), {
        headers,
        credentials: 'same-origin'
      });
      if (!res || !res.ok || typeof res.json !== 'function') {
        this.contributorCountersKnown = false;
      } else {
        this.contributorCounters = await res.json();
        this.contributorCountersKnown = true;
      }
    } catch (_) {
      this.contributorCountersKnown = false;
    }
    this.renderStats();
  }

  log(msg) {
    // T104 (one status/log area): when the host page wired the shared area, the
    // line goes through it - it owns the attached element, so the app must not
    // write the same element directly. Without a shared area the standalone
    // page behaves exactly as before.
    const shared = this.sharedStatusLog();
    if (shared) {
      shared.log('pools', String(msg));
      return;
    }
    const time = new Date().toLocaleTimeString();
    const line = `[${time}] ${msg}`;
    if (this.dom && this.dom.logOutput) {
      this.dom.logOutput.textContent = line + '\n' + this.dom.logOutput.textContent;
    } else {
      console.log(line);
    }
  }
}

/**
 * Decode base64 bytes, or null when the value is not base64. Used on data the
 * page only forwards: the runner's own verifier stays the authority on it.
 *
 * @param {*} b64
 * @returns {Uint8Array|null}
 */
export function safeBase64ToBytes(b64) {
  if (typeof b64 !== 'string') return null;
  try {
    return base64ToBytes(b64);
  } catch (_) {
    return null;
  }
}

/**
 * Decode a base64 string into bytes, in the browser and in Node.
 *
 * @param {string} b64
 * @returns {Uint8Array}
 */
export function base64ToBytes(b64) {
  const s = String(b64 || '');
  if (typeof globalThis.atob === 'function') {
    const bin = globalThis.atob(s);
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  }
  if (typeof Buffer !== 'undefined') {
    return new Uint8Array(Buffer.from(s, 'base64'));
  }
  throw new Error('no base64 decoder available');
}

// Auto-instantiate in browser. T104 steps 7/10: this standalone block is the
// fallback of /pools/contribute.html only. The integrated page boots through
// contribute_page_T104.js, which constructs the app with the shared budget, the
// one status/log area and the artifact cache and decides when it computes. That
// entry sets window.ccgscPageBooted before the DOM is ready, so the app is never
// constructed twice.
if (typeof window !== 'undefined' && typeof document !== 'undefined') {
  window.addEventListener('DOMContentLoaded', () => {
    // A page that started the one entry owns the app.
    if (window.ccgscPageBooted) return;
    window.ccgscPageBooted = true;
    // T104 (persist hostId): the page bootstrap keeps one stable host id in
    // web storage. The write happens HERE, outside the app, so ContributeApp
    // itself never touches web storage (WO_T96: the anonymous token stays in
    // memory only). On the integrated root page the wiring of worker.html
    // persists the same id and exposes it as window.ccgscContributorHostId.
    const hostId = persistHostId(readPersistedHostId() || undefined);
    // R10-FIX (chatgpt MAJOR): the per-tab identity has the same rule as the host
    // id: the bootstrap keeps it in sessionStorage, the app only reads it.
    const browserTabId = persistBrowserTabId(readBrowserTabId() || undefined);
    // ?pools=tp,pp lets one tab contribute to both pools (thesis runs, T104).
    // The integrated entry passes its own options; this standalone fallback must
    // keep the same query switch, or a fallback boot would silently register in
    // the thread pool only (T104c row 4).
    const poolsParam = new URLSearchParams(window.location.search).get('pools');
    const pools = poolsParam ? resolvePools(poolsParam.split(',')) : undefined;
    window.contributeApp = new ContributeApp({ hostId, browserTabId, pools });
    window.contributeApp.init().catch(console.error);
  });
}

// END OF contribute.js



