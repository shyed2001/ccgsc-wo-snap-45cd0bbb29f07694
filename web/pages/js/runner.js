// WO_T3_CONTRIBUTE_PAGE runner.js - Sandbox Runner (Run-Origin Iframe)
// Author: Claude Opus 5.5 head / agy Gemini 3.8 Flash high. Date: 2026-11-17.
// WO_FIX_B (hermes MiniMax-M3, 2026-11-17): the runner pins the operator key ring (it
// never trusts a key that arrives with a job), runs one job at a time, requires a
// Worker, fails closed on the envelope schema and caps the artifact size.
// W10 platform/workload split (2026-11-21): the runner is platform only. It knows
// no workload: the job carries the Workload manifest bytes as its module artifact,
// the runner verifies them, runs them in the generic worker shell and hashes the
// output with platform/hash.js. Nothing from the workload tree is imported or inlined.
// W10b (2026-11-21), owner decisions D-W10-3/4/5: the signed envelope may pin the
// workload id/version and the sha256 of EVERY workload file (no new key); the
// sandbox worker is a module Blob Worker and the workload keeps `export function
// run` (no module-worker support = task declined, not failed); runner.html carries
// a strict CSP. Raw output bytes hash (this file) and settlement JSON hash
// (contribute.js submit) stay two separate digest domains.
// Truth: WO_T3_CONTRIBUTE_PAGE.md, GRILL_WWPP_R1.md (R29.1, R37.1, R39.1), SPLIT_DESIGN_W10.md + section 8
// W10c (2026-11-21), owner decisions D-W10-6/D-W10-8:
//   - isolation mode per signed envelope / page config: "separate+lock" (default)
//     runs the kernel in a second, empty module Blob Worker while this shell keeps
//     the MessagePort and computes the output digest; "lock-only" keeps one worker
//     and the same capability lock. Unknown values answer the strict default.
//   - a workload is up to MAX_WORKLOAD_FILES files (entry .js + data/.wasm); the
//     envelope pins every file's sha256, the parent fetches all of them and the
//     runner verifies each before the first import. Companions reach the workload
//     as ctx.files { path: ArrayBuffer }.
// W10d (2026-11-21), owner decision D-W10-10: the SECOND workload kind is the
// generic WASM ABI. envelope.workload.kind = "wasm-abi" names an entry .wasm
// file (sha256-pinned like every file) that exports memory, ccgsc_alloc(len),
// ccgsc_run(inPtr,inLen) -> outLen (negative = error code) and ccgsc_out().
// The kernel instantiates it with NO imports (only an env.abort stub), copies
// the task input bytes in, reads the output bytes out and the SHELL hashes them;
// no digest is ever computed in the kernel realm. The kind is platform ABI, not
// a workload identity: no problem-specific adapter exists here. "js-module" is
// the default and keeps the W10c path byte-for-byte (WebGPU needs it).

'use strict';

import { createGPUPicker, createLanePool } from './gpu_picker_T83.js';
import { sha256Hex, sha256Pure, toBytes } from './platform/hash.js';

// ---------------------------------------------------------------- Constants

// Domain separator and version for task envelopes (WWPP-R39.1, Go TaskEnvelope).
export const ENVELOPE_DOMAIN = 'ccgsc.pools.task-envelope.v1';
export const ENVELOPE_VERSION = 1;

// Build id of this runner. The parent page logs the id echoed by the sandbox, so a
// stale or drifted runner.html is visible instead of silent.
export const RUNNER_BUILD_ID = 'ccgsc-runner-2026-11-21-w10c';

// The largest artifact the runner will hash and compile. The server caps uploads at
// pool.MaxArtifactBytes (32 MiB); the runner uses the same bound so a hostile job
// cannot push a multi-GB buffer into the page.
export const MAX_WASM_BYTES = 32 * 1024 * 1024;

// D-W10-8: a workload is at most this many files (entry .js plus data or .wasm).
export const MAX_WORKLOAD_FILES = 8;

// R11-M5: the cap of one whole workload, entry module plus every companion. The
// per-file cap (MAX_WASM_BYTES) alone let a multi-file job push
// MAX_WORKLOAD_FILES times that bound into the page, while the runner declines
// anything above this total. The parent uses the same bound before it fetches.
export const MAX_TOTAL_WORKLOAD_BYTES = MAX_WASM_BYTES;

// D-W10-6: the isolation mode of the compute worker. "separate+lock" (default)
// runs the kernel in a second, empty module Blob Worker while the shell keeps
// the port and the digest; "lock-only" keeps one worker and the same lock.
export const ISOLATION_MODES = Object.freeze(['separate+lock', 'lock-only']);
export const DEFAULT_ISOLATION_MODE = 'separate+lock';

// The signed input of a task is bounded before it is hashed or parsed: a hostile
// job cannot push a multi-megabyte JSON into the binding check.
export const MAX_INPUT_JSON_BYTES = 1024 * 1024;

// The workload output is bounded before it is hashed and posted back. The largest
// published result is the 240^3 f32 matrix (~52.7 MB), so 64 MiB is the cap.
export const MAX_OUTPUT_BYTES = 64 * 1024 * 1024;

// D-W10-10: the two platform workload kinds. A kind is ABI, not a workload
// identity: "js-module" is an ES module exporting `run(input, ctx)` (W10c, kept
// unchanged because WebGPU cannot be reached from WASM), "wasm-abi" is a
// wasm32 module exporting the generic C ABI below.
export const WORKLOAD_KINDS = Object.freeze(['js-module', 'wasm-abi']);
export const DEFAULT_WORKLOAD_KIND = 'js-module';

// The generic WASM ABI of D-W10-10. The module must EXPORT its memory and these
// three functions; the input bytes are copied in and the output bytes out.
export const WASM_ABI_EXPORTS = Object.freeze(['ccgsc_alloc', 'ccgsc_run', 'ccgsc_out']);

// One WebAssembly page is 64 KiB. The declared maximum of the module memory is
// bounded by the page config, default 4096 pages = 256 MiB.
export const WASM_PAGE_BYTES = 65536;
export const DEFAULT_WASM_MAX_MEMORY_PAGES = 4096;
export const MAX_WASM_MEMORY_PAGES = 65536;

// The only import a wasm-abi module may declare. Anything else is refused before
// instantiation, so a hostile module can never reach a host function.
export const WASM_ABI_ALLOWED_IMPORTS = Object.freeze(['env.abort']);

// The PINNED operator key ring. The verify gate resolves the public key by the keyId
// inside the signed envelope; a key that travels with the job is never used.
//
// How an operator adds a real key:
//   1. take the raw 32 byte Ed25519 public key of the server task key
//      (Engine.TaskPublicKey),
//   2. compute the key id: the first 16 hex characters of SHA-256(public key),
//      the same rule as Go verify.KeyID,
//   3. add an entry { keyId, publicKeyHex } to OPERATOR_KEYS.
//
// The shipped entry is the RFC 8032 Ed25519 test vector. It is a DEMO key: the
// runner pins it with scope 'demo', so it may only authorize the demo module digest.
// Publishing it does not open the gate for arbitrary modules. Since W10 the demo
// module is fetched as an independent, byte-pinned runtime artifact from the
// workload tree (CR-53); the scope pin remains the reference manifest digest.
export const OPERATOR_KEYS = Object.freeze({
  '21fe31dfa154a261': Object.freeze({
    keyId: '21fe31dfa154a261',
    publicKeyHex: 'd75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a',
    scope: 'demo',
    moduleSha256: 'ef4efbb653948565ccf86b524e37fb89b2b8f7b5185fcb112fbf64f1b74b74e1'
  }),
  // Live VPS task key (ccgsc-demo pools-core, PostgreSQL store), pinned 2026-11-21
  // after the thread-pool campaign failed every task with unknown_operator_key.
  // Public half only: read from the store record (private key bytes 32..63 = the
  // Ed25519 public key); keyId re-derived = first 16 hex of SHA-256(public key).
  // After `POST /admin/v1/task-keys:rotate`, pin the successor here before retiring.
  '7dcc2218b5ba8363': Object.freeze({
    keyId: '7dcc2218b5ba8363',
    publicKeyHex: '00738abedf14405a1c8e732914cfaafe6f45c4a6cb6acdaaf15331e1d05a42a2',
    scope: 'operator'
  })
});

/**
 * The runner's self-reported identity: "<sha256 of this runner body>:<keyId>".
 *
 * R10-FIX owner D-W10-9 ("Runner self-reports"): build-runner-html.mjs hashes the
 * runner body it emitted and writes the result onto the document element as
 * `data-runner-pin="<sha256>:<keyId>"`. The runner reads exactly that attribute
 * and posts it in its 'ready' message, so the parent never has to guess which
 * runner it is driving; the parent forwards it on register and heartbeat and the
 * server can refund a task whose lease went to a stale runner. A runner loaded
 * without a generated page reports '' rather than a lie.
 *
 * @param {Object} [doc] document override (tests)
 * @returns {string}
 */
export function runnerSelfPin(doc = (typeof document !== 'undefined' ? document : null)) {
  try {
    const el = doc && (doc.documentElement || doc.body);
    const pin = el && typeof el.getAttribute === 'function' ? el.getAttribute('data-runner-pin') : '';
    return typeof pin === 'string' ? pin : '';
  } catch (_) {
    return '';
  }
}

// ---------------------------------------------------------------- Pure Helpers

export function canonicalJson(v) {
  if (v === null || typeof v !== 'object') return JSON.stringify(v);
  if (Array.isArray(v)) return '[' + v.map(canonicalJson).join(',') + ']';
  return '{' + Object.keys(v).sort().map(k => JSON.stringify(k) + ':' + canonicalJson(v[k])).join(',') + '}';
}

export function hexOf(u8) {
  const bytes = u8 instanceof Uint8Array ? u8 : new Uint8Array(u8);
  return Array.from(bytes).map(b => b.toString(16).padStart(2, '0')).join('');
}

export function bytesOfHex(hex) {
  const s = String(hex || '');
  if (s.length % 2 !== 0 || !/^[0-9a-f]*$/.test(s)) {
    throw new Error('not a hex string');
  }
  const out = new Uint8Array(s.length / 2);
  for (let i = 0; i < out.length; i++) {
    out[i] = parseInt(s.substr(i * 2, 2), 16);
  }
  return out;
}

/**
 * Decode standard RFC 4648 base64 (with padding) into bytes. Self-contained on
 * purpose: a wasm-abi task input arrives as base64 inside the signed JSON, and
 * the runner may not import a second platform module for it.
 *
 * @param {string} b64
 * @returns {Uint8Array}
 */
export function bytesOfBase64(b64) {
  const whole = String(b64 || '').replace(/\s+/g, '');
  if (!/^[A-Za-z0-9+/]*={0,2}$/.test(whole)) throw new Error('not a base64 string');
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';
  const clean = whole.replace(/=+$/, '');
  const out = new Uint8Array(Math.floor((clean.length * 3) / 4));
  let bits = 0;
  let acc = 0;
  let o = 0;
  for (const ch of clean) {
    const v = alphabet.indexOf(ch);
    if (v < 0) throw new Error('not a base64 string');
    acc = (acc << 6) | v;
    bits += 6;
    if (bits >= 8) {
      bits -= 8;
      out[o++] = (acc >> bits) & 0xff;
    }
  }
  return o === out.length ? out : out.subarray(0, o);
}

export function computeWorkerCap(hardwareConcurrency) {
  const cores = typeof hardwareConcurrency === 'number' && hardwareConcurrency > 0
    ? hardwareConcurrency
    : 2;
  return Math.max(1, Math.min(4, Math.floor(cores - 1)));
}

function nowMs() {
  return (typeof performance !== 'undefined' ? performance.now() : Date.now());
}

// ---------------------------------------------------------------- Operator Key Ring

/**
 * Derive the key id of a raw Ed25519 public key: the first 16 hex characters of
 * SHA-256(public key). Same rule as Go verify.KeyID.
 *
 * @param {Uint8Array|ArrayBuffer} publicKey
 * @returns {Promise<string>}
 */
export async function deriveKeyId(publicKey) {
  const sha = await sha256Hex(publicKey);
  return sha.slice(0, 16);
}

/**
 * Build a pinned operator key entry. The key id is derived, never taken from a job.
 *
 * @param {Uint8Array|ArrayBuffer} publicKey
 * @param {Object} [opts]
 * @param {string} [opts.scope] 'operator' (default) or 'demo'
 * @param {string} [opts.moduleSha256] the only module digest a demo key may authorize
 * @returns {Promise<Object>}
 */
export async function operatorKeyEntry(publicKey, opts = {}) {
  const keyId = await deriveKeyId(publicKey);
  const entry = {
    keyId: keyId,
    publicKeyHex: hexOf(publicKey),
    scope: opts.scope || 'operator'
  };
  if (opts.moduleSha256) entry.moduleSha256 = opts.moduleSha256;
  return entry;
}

/**
 * Resolve the pinned public key for an envelope key id.
 *
 * @param {string} keyId
 * @param {string} [artifactSha256] the digest the envelope authorizes
 * @param {Object} [keyRing] defaults to the pinned OPERATOR_KEYS
 * @returns {{ valid: boolean, error: string|null, publicKey: Uint8Array|null, keyId: string|null }}
 */
export function resolveOperatorKey(keyId, artifactSha256, keyRing = OPERATOR_KEYS) {
  if (typeof keyId !== 'string' || !keyId) {
    return { valid: false, error: 'envelope_missing_key_id', publicKey: null, keyId: null };
  }
  const ring = keyRing && typeof keyRing === 'object' ? keyRing : OPERATOR_KEYS;
  const entry = Object.prototype.hasOwnProperty.call(ring, keyId) ? ring[keyId] : null;
  if (!entry) {
    return { valid: false, error: 'unknown_operator_key', publicKey: null, keyId: keyId };
  }
  if (entry.scope === 'demo') {
    // Fail closed: a demo key may ONLY authorize the one module digest it pins. A
    // missing, empty or non-string digest used to skip the check entirely, so a
    // job without an artifactSha256 turned the scope pin into a no-op.
    if (!entry.moduleSha256 || typeof artifactSha256 !== 'string' || entry.moduleSha256 !== artifactSha256) {
      return { valid: false, error: 'operator_key_scope_violation', publicKey: null, keyId: keyId };
    }
  }
  let publicKey = null;
  try {
    publicKey = bytesOfHex(entry.publicKeyHex);
  } catch (e) {
    return { valid: false, error: 'operator_key_malformed', publicKey: null, keyId: keyId };
  }
  if (publicKey.length !== 32) {
    return { valid: false, error: 'operator_key_malformed', publicKey: null, keyId: keyId };
  }
  return { valid: true, error: null, publicKey: publicKey, keyId: keyId };
}

// ---------------------------------------------------------------- Envelope Schema

const HEX64 = /^[0-9a-f]{64}$/;

// R10-FIX row 4: a workload imports nothing - no module-graph edge at all. The
// ban therefore covers the static `import` form, the dynamic `import(` form and
// the `export ... from` re-export forms (a re-export is an import of the named
// module). The CSP `blob:` allowance is the only reason a banned edge matters,
// so this is the backstop: the shell imports exactly one URL, its own workload.
// The leading `(?:^|[^\w$.])` keeps identifiers such as `important` and object
// properties out of the match.
export const WORKLOAD_IMPORT_BAN =
  /(?:^|[^\w$.])(?:import\s*[\s(]|export\s+(?:\*(?:\s+as\s+[\w$]+)?|\{[^}]*\})\s*from\s*)/m;

/**
 * Whether a workload source tries to import or re-export another module.
 *
 * @param {string} source the verified workload module source
 * @returns {boolean}
 */
export function workloadImports(source) {
  return WORKLOAD_IMPORT_BAN.test(String(source == null ? '' : source));
}

/**
 * Byte length of a UTF-8 string. The input cap is a byte cap: `.length` counts
 * UTF-16 code units, so a string of non-BMP or multi-byte characters could pass
 * the cap while being several times larger on the wire and under the hash.
 *
 * @param {string} text
 * @returns {number}
 */
export function utf8ByteLength(text) {
  return new TextEncoder().encode(String(text == null ? '' : text)).length;
}

/**
 * Validate the complete Go TaskEnvelope schema (internal/verify/envelope.go) before
 * any signature check. Fails closed: a missing or malformed field is rejected.
 *
 * @param {Object} envelope
 * @returns {{ valid: boolean, error: string|null }}
 */
export function validateTaskEnvelope(envelope) {
  if (!envelope || typeof envelope !== 'object') {
    return { valid: false, error: 'missing_envelope' };
  }
  if (envelope.version === undefined || envelope.version === null) {
    return { valid: false, error: 'envelope_missing_version' };
  }
  if (envelope.version !== ENVELOPE_VERSION) {
    return { valid: false, error: 'envelope_unsupported_version' };
  }
  if (typeof envelope.jobId !== 'string' || !envelope.jobId) {
    return { valid: false, error: 'envelope_missing_job_id' };
  }
  if (typeof envelope.taskId !== 'string' || !envelope.taskId) {
    return { valid: false, error: 'envelope_missing_task_id' };
  }
  if (typeof envelope.artifactSha256 !== 'string' || !HEX64.test(envelope.artifactSha256)) {
    return { valid: false, error: 'envelope_invalid_artifact_sha256' };
  }
  if (typeof envelope.inputSha256 !== 'string' || !HEX64.test(envelope.inputSha256)) {
    return { valid: false, error: 'envelope_invalid_input_sha256' };
  }
  if (typeof envelope.issuedAt !== 'number' || !Number.isFinite(envelope.issuedAt)) {
    return { valid: false, error: 'envelope_missing_issued_at' };
  }
  if (typeof envelope.expiresAt !== 'number' || !Number.isFinite(envelope.expiresAt)) {
    return { valid: false, error: 'envelope_missing_expires_at' };
  }
  if (typeof envelope.keyId !== 'string' || !envelope.keyId) {
    return { valid: false, error: 'envelope_missing_key_id' };
  }
  // D-W10-3: the envelope MAY pin the workload id/version and the sha256 of
  // every workload file. The field is optional (older envelopes stay valid);
  // when present it is part of the signed canonical JSON and must be well-formed.
  if (envelope.workload !== undefined) {
    const w = envelope.workload;
    if (!w || typeof w !== 'object' || Array.isArray(w)) {
      return { valid: false, error: 'envelope_invalid_workload' };
    }
    if (typeof w.id !== 'string' || !w.id) {
      return { valid: false, error: 'envelope_invalid_workload' };
    }
    if (!Number.isInteger(w.version) || w.version < 1) {
      return { valid: false, error: 'envelope_invalid_workload' };
    }
    // D-W10-10: the kind, when present, must be one the platform implements.
    // An unknown kind is a malformed envelope, never a silent js-module run.
    if (w.kind !== undefined && !WORKLOAD_KINDS.includes(w.kind)) {
      return { valid: false, error: 'envelope_invalid_workload' };
    }
    if (!Array.isArray(w.files) || w.files.length === 0) {
      return { valid: false, error: 'envelope_invalid_workload' };
    }
    // D-W10-8: a workload carries at most MAX_WORKLOAD_FILES files.
    if (w.files.length > MAX_WORKLOAD_FILES) {
      return { valid: false, error: 'envelope_invalid_workload' };
    }
    const seenPaths = new Set();
    let entryCount = 0;
    for (const file of w.files) {
      if (!file || typeof file !== 'object' || typeof file.path !== 'string' || !file.path ||
          typeof file.sha256 !== 'string' || !HEX64.test(file.sha256)) {
        return { valid: false, error: 'envelope_invalid_workload' };
      }
      // A workload has exactly one entry module and it is the first file.
      if (file.entry === true) {
        entryCount++;
        if (w.files[0] !== file) return { valid: false, error: 'envelope_invalid_workload' };
      }
      if (seenPaths.has(file.path)) {
        return { valid: false, error: 'envelope_invalid_workload' };
      }
      seenPaths.add(file.path);
    }
    if (entryCount > 1) {
      return { valid: false, error: 'envelope_invalid_workload' };
    }
  }
  return { valid: true, error: null };
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
 * Verify a signed envelope and the artifact hash before compile/execution (R37.1).
 *
 * The public key is ALWAYS taken from the pinned OPERATOR_KEYS ring, selected by the
 * keyId inside the envelope. A key that arrives with the job is ignored on purpose:
 * otherwise the signer would authenticate itself.
 *
 * Checks:
 * 1. The envelope, the signature and the artifact bytes must be present.
 * 2. The artifact must not exceed MAX_WASM_BYTES.
 * 3. The complete Go TaskEnvelope schema must hold (fails closed).
 * 4. SHA-256 of the artifact bytes must equal envelope.artifactSha256.
 * 5. The envelope must not be expired and its time bounds must be sane.
 * 6. The Ed25519 signature must verify against the PINNED operator key.
 *
 * @param {Object} params
 * @param {Object} params.envelope
 * @param {Uint8Array} params.envSig
 * @param {ArrayBuffer|Uint8Array} params.wasmBytes
 * @param {Object} [params.keyRing] defaults to the pinned OPERATOR_KEYS
 * @returns {Promise<{ valid: boolean, error: string|null, verifyMs: number }>}
 */
export async function verifyEnvelope({ envelope, envSig, wasmBytes, keyRing }) {
  const t0 = nowMs();
  const ring = keyRing || OPERATOR_KEYS;
  if (!envelope || typeof envelope !== 'object') {
    return { valid: false, error: 'missing_envelope', verifyMs: 0 };
  }
  if (!envSig) {
    return { valid: false, error: 'missing_signature', verifyMs: 0 };
  }

  // 1. Fail closed when the wasm bytes are missing or empty.
  const byteLength = wasmBytes ? (wasmBytes.byteLength !== undefined ? wasmBytes.byteLength : wasmBytes.length) : 0;
  if (!wasmBytes || byteLength === 0) {
    return { valid: false, error: 'missing_wasm_bytes', verifyMs: nowMs() - t0 };
  }

  // 2. Size cap before any hashing or compiling.
  if (byteLength > MAX_WASM_BYTES) {
    return { valid: false, error: 'wasm_too_large', verifyMs: nowMs() - t0 };
  }

  // 3. Full schema validation before the signature check.
  const schema = validateTaskEnvelope(envelope);
  if (!schema.valid) {
    return { valid: false, error: schema.error, verifyMs: nowMs() - t0 };
  }

  // 4. SHA-256 hash match against the artifact bytes (R37.1).
  try {
    const computedSha = await sha256Hex(wasmBytes);
    if (computedSha !== envelope.artifactSha256) {
      return { valid: false, error: 'artifact_sha256_mismatch', verifyMs: nowMs() - t0 };
    }
  } catch (e) {
    return { valid: false, error: 'hash_check_failed: ' + (e.message || e), verifyMs: nowMs() - t0 };
  }

  // 4b. D-W10-3: when the envelope pins the workload files, the delivered
  //     artifact must be one of the signed files (the entry). The signature
  //     already covers the file list; no key and no manifest is added here.
  if (envelope.workload !== undefined) {
    const declaredDigests = envelope.workload.files.map(f => f.sha256);
    if (!declaredDigests.includes(envelope.artifactSha256)) {
      return { valid: false, error: 'artifact_not_in_workload_files', verifyMs: nowMs() - t0 };
    }
  }

  // 5. Expiry and time bounds: fail closed on a missing or malformed bound.
  //    The expiry verdict comes first, so an expired lease is reported as expired
  //    even when its bounds are inverted.
  const nowSec = Date.now() / 1000;
  if (nowSec > envelope.expiresAt) {
    return { valid: false, error: 'lease_expired', verifyMs: nowMs() - t0 };
  }
  if (envelope.expiresAt <= envelope.issuedAt) {
    return { valid: false, error: 'envelope_invalid_time_bounds', verifyMs: nowMs() - t0 };
  }

  // 6. Resolve the PINNED key. A key delivered with the job is never consulted.
  const resolved = resolveOperatorKey(envelope.keyId, envelope.artifactSha256, ring);
  if (!resolved.valid) {
    return { valid: false, error: resolved.error, verifyMs: nowMs() - t0 };
  }

  const sigBytes = envSig instanceof Uint8Array ? envSig : new Uint8Array(envSig);
  if (sigBytes.length !== 64) {
    return { valid: false, error: 'invalid_signature_length', verifyMs: nowMs() - t0 };
  }

  // 7. Ed25519 signature over the domain-prefixed canonical JSON.
  try {
    const pk = await globalThis.crypto.subtle.importKey(
      'raw',
      resolved.publicKey,
      { name: 'Ed25519' },
      true,
      ['verify']
    );
    const signingBytes = envelopeSigningBytes(envelope);
    const verified = await globalThis.crypto.subtle.verify('Ed25519', pk, sigBytes, signingBytes);
    if (!verified) {
      return { valid: false, error: 'signature_invalid', verifyMs: nowMs() - t0 };
    }
    return { valid: true, error: null, verifyMs: nowMs() - t0 };
  } catch (err) {
    return { valid: false, error: 'verify_exception: ' + (err.message || err), verifyMs: nowMs() - t0 };
  }
}

// ---------------------------------------------------------------- Message Schema

/**
 * Validate the MessageChannel protocol schemas (R39.1).
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
      return { valid: true, error: null };

    case 'egress-refresh':
      if (typeof msg.nonce !== 'string' || typeof msg.jobId !== 'string' ||
          typeof msg.taskId !== 'string' || !msg.egress ||
          typeof msg.egress.token !== 'string' || !Number.isSafeInteger(msg.egress.expiresAt)) {
        return {valid:false,error:'egress_refresh_invalid'};
      }
      return {valid:true,error:null};
    case 'job':
      if (typeof msg.jobId !== 'string' || !msg.jobId) return { valid: false, error: 'job_requires_jobId' };
      if (typeof msg.taskId !== 'string' || !msg.taskId) return { valid: false, error: 'job_requires_taskId' };
      if (typeof msg.nonce !== 'string' || !msg.nonce) return { valid: false, error: 'job_requires_nonce_string' };
      if (!(msg.wasm instanceof ArrayBuffer) && !(msg.wasm instanceof Uint8Array)) {
        return { valid: false, error: 'job_requires_wasm_buffer' };
      }
      if (typeof msg.wasmSha256 !== 'string' || !HEX64.test(msg.wasmSha256)) {
        return { valid: false, error: 'job_requires_wasmSha256' };
      }
      if (!msg.envelope || typeof msg.envelope !== 'object') {
        return { valid: false, error: 'job_requires_envelope' };
      }
      if (!msg.envSig) return { valid: false, error: 'job_requires_envSig' };
      if (msg.inputJSON !== undefined && typeof msg.inputJSON !== 'string') {
        return { valid: false, error: 'job_requires_inputJSON_string' };
      }
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

// ---------------------------------------------------------------- Compute Worker Source (Inside Opaque Sandbox)
//
// D-W10-4: the sandbox worker is a MODULE Worker started from a blob: URL
// (`new Worker(url, { type: 'module' })`). WORKER_SRC is the static shell
// template. A platform without module workers declines the task (declined, not
// failed). The shell carries no workload code and never interprets a workload
// kind; the KIND selects which of the two kernel templates is started, never a
// problem-specific adapter. `typeof run` stays 'undefined' -> 'unknown_workload'
// for a module without the entry, so a broken workload can never produce an
// output of zero.
//
// D-W10-6, two isolation modes:
//  - 'lock-only': one module Blob Worker that locks its own realm and imports
//    the workload (the W10b behaviour plus the lock);
//  - 'separate+lock' (default): the shell imports NO workload code and starts a
//    second, empty module Blob Worker (KERNEL_SRC) that holds the workload. The
//    shell owns the port and the digest: the kernel can only post to the shell,
//    and the shell hashes the returned bytes itself. A forged task_done, a
//    forged hash or an overridden postMessage in the kernel therefore never
//    reaches the runner.
//
// The capability lock removes eval, Function (including the constructor chains
// of async/generator functions), importScripts, Worker, SharedWorker, Blob,
// fetch, XMLHttpRequest, WebSocket and EventSource from the realm that runs
// untrusted code, before the workload module is imported.
// The realm lock, shared by the lock-only shell and the kernel. It is a single
// function so both modes enforce exactly the same denials. Everything it needs
// lives inside its own body: its source travels into a blob: Worker as text, so
// a module-level constant would not exist there.
export function lockWorkloadRealm(target) {
  const realm = target && typeof target === 'object' ? target : self;
  // A real Worker realm IS the global realm. A test harness that drives the
  // shell with a fake `self` shares the harness realm, which must never be
  // locked: that would disable the test runner itself.
  if (realm !== globalThis) return { locked: false };
  // The constructor chains are severed FIRST: once `Function` itself is denied,
  // its prototype is no longer reachable through the global name.
  const FunctionCtor = Function;
  const protoOf = (fn) => {
    try { return Object.getPrototypeOf(fn); } catch (_) { return null; }
  };
  const sever = (holder) => {
    if (!holder) return;
    try {
      Object.defineProperty(holder, 'constructor', { value: undefined, writable: false, configurable: false });
    } catch (_) {}
  };
  sever(FunctionCtor.prototype);
  // `async function` and generator functions have their own prototypes, and both
  // reach back to the Function constructor.
  sever(protoOf(async function () {}));
  sever(protoOf(function* () {}));
  sever(protoOf(async function* () {}));
  // postMessage is denied AFTER the realm captured its own reply channel, so a
  // workload can never send a message of its own (forged task_done included).
  const denied = ['eval', 'Function', 'importScripts', 'Worker', 'SharedWorker', 'Blob',
    'fetch', 'XMLHttpRequest', 'WebSocket', 'EventSource', 'WebTransport', 'RTCPeerConnection', 'webkitRTCPeerConnection', 'postMessage'];
  const deny = (name) => {
    try {
      Object.defineProperty(realm, name, { value: undefined, writable: false, configurable: false });
    } catch (_) { /* a non-configurable global stays as it is */ }
  };
  for (const name of denied) deny(name);
  try {
    if (typeof URL !== 'undefined' && typeof URL.createObjectURL === 'function') {
      Object.defineProperty(URL, 'createObjectURL', { value: undefined, writable: false, configurable: false });
    }
  } catch (_) {}
  return realm;
}

// This function is serialized into the locked workload realm. It captures the
// actual transport before importing developer code; the native constructors are
// still denied by lockWorkloadRealm. CSP is the hard origin boundary.
export function createLeaseEgress(initial) {
  if (!initial || !Array.isArray(initial.endpoints) || !initial.endpoints.length ||
      typeof initial.token !== 'string' || !initial.token || !Number.isSafeInteger(initial.expiresAt)) return null;
  const clock = Date.now.bind(Date);
  const URLCtor = URL, HeadersCtor = Headers, AbortCtor = AbortController;
  const nativeFetch = typeof fetch === 'function' ? fetch.bind(globalThis) : null;
  const Socket = typeof WebSocket === 'function' ? WebSocket : null;
  const later = setTimeout.bind(globalThis), cancel = clearTimeout.bind(globalThis);
  const stringify = JSON.stringify.bind(JSON), parse = JSON.parse.bind(JSON);
  const enc = new TextEncoder(), dec = new TextDecoder();
  const maxBody = 1024 * 1024;
  const approved = initial.endpoints.slice();
  const allowed = approved.includes.bind(approved);
  let token = initial.token, expiresAt = initial.expiresAt;
  let windowStart = clock(), count = 0, openSocket = null, socketTimer = null;
  const take = () => {
    const now = clock();
    if (now >= expiresAt * 1000) throw new Error('egress_token_expired');
    if (now - windowStart >= 60000) { windowStart = now; count = 0; }
    if (++count > 60) throw new Error('egress_rate_limit');
  };
  const endpoint = (raw, protocol) => {
    const u = new URLCtor(String(raw));
    if (u.protocol !== protocol || u.username || u.password || !allowed(u.origin)) throw new Error('egress_endpoint_denied');
    return u.href;
  };
  const fetchBound = async (url, init = {}) => {
    take();
    const href = endpoint(url, 'https:');
    if (!nativeFetch) throw new Error('egress_fetch_unavailable');
    const method = String(init.method || 'GET').toUpperCase();
    if (!['GET','HEAD','POST','PUT','PATCH','DELETE'].includes(method)) throw new Error('egress_method_denied');
    const body = init.body;
    if (body !== undefined && body !== null &&
        !((typeof body === 'string' && enc.encode(body).byteLength <= 262144) ||
          (body instanceof Uint8Array && body.byteLength <= 262144))) throw new Error('egress_body_limit');
    const headers = new HeadersCtor(init.headers || {});
    headers.set('Authorization', 'Bearer ' + token);
    const controller = new AbortCtor();
    const timer = later(() => controller.abort(), Math.max(1, Math.min(15000, expiresAt * 1000 - clock())));
    try {
      const res = await nativeFetch(href, {method, headers, body, credentials:'omit', redirect:'error',
        cache:'no-store', referrerPolicy:'no-referrer', signal:controller.signal});
      const reader = res.body?.getReader();
      const chunks = []; let size = 0;
      if (reader) {
        for (;;) {
          const item = await reader.read();
          if (item.done) break;
          size += item.value.byteLength;
          if (size > maxBody) { await reader.cancel(); throw new Error('egress_response_limit'); }
          chunks.push(item.value);
        }
      }
      const bytes = new Uint8Array(size); let offset = 0;
      for (const chunk of chunks) { bytes.set(chunk,offset); offset += chunk.byteLength; }
      return Object.freeze({ok:res.ok,status:res.status,bytes,text:()=>dec.decode(bytes),json:()=>parse(dec.decode(bytes))});
    } finally { cancel(timer); }
  };
  const connect = (url, handlers = {}) => {
    take();
    const href = endpoint(url, 'wss:');
    if (!Socket || openSocket) throw new Error('egress_socket_unavailable');
    const ws = new Socket(href); ws.binaryType = 'arraybuffer';
    openSocket = ws;
    const sendNative = ws.send.bind(ws), closeNative = ws.close.bind(ws);
    let authenticated = false;
    const shutdown = () => { try { closeNative(); } catch (_) {} };
    const deadline = later(shutdown, 5000);
    socketTimer = later(shutdown, Math.max(1, expiresAt*1000-clock()));
    ws.onopen = () => {
      if (clock() >= expiresAt*1000) { shutdown(); return; }
      sendNative(stringify({type:'auth',token}));
    };
    ws.onmessage = ev => {
      if (clock() >= expiresAt*1000) { shutdown(); return; }
      if (!authenticated) {
        try { if (parse(ev.data).type !== 'auth-ok') throw new Error('auth'); } catch (_) { shutdown(); return; }
        authenticated = true; cancel(deadline); handlers.onReady?.(); return;
      }
      if ((typeof ev.data === 'string' && enc.encode(ev.data).byteLength > maxBody) ||
          (ev.data instanceof ArrayBuffer && ev.data.byteLength > maxBody)) { shutdown(); return; }
      try { take(); } catch (_) { shutdown(); return; }
      handlers.onMessage?.(ev.data);
    };
    ws.onerror = () => handlers.onError?.('egress_socket_error');
    ws.onclose = () => { cancel(deadline); cancel(socketTimer); if (openSocket === ws) openSocket = null; handlers.onClose?.(); };
    return Object.freeze({send(data) {
      take(); if (!authenticated) throw new Error('egress_socket_auth_required');
      if (!(typeof data === 'string' && enc.encode(data).byteLength <= 262144) &&
          !(data instanceof Uint8Array && data.byteLength <= 262144)) throw new Error('egress_body_limit');
      sendNative(data);
    },close:shutdown});
  };
  const api = Object.freeze({fetch:fetchBound,connect});
  return {api,refresh(next) {
    if (!next || typeof next.token !== 'string' || !next.token ||
        !Number.isSafeInteger(next.expiresAt) || next.expiresAt * 1000 <= clock()) return;
    token=next.token; expiresAt=next.expiresAt;
    // A WSS connection authenticated with the old bearer must reconnect.
    if (openSocket) { try { openSocket.close(); } catch (_) {} }
  }};
}

const egressRealmSource = createLeaseEgress.toString();

// The kernel: an empty module Blob Worker. It imports the verified workload,
// runs exactly one entry and posts the raw result back to the shell. The shell
// never shares its port with it.
export const KERNEL_SRC = `
const WORKLOAD_URL = __WORKLOAD_URL__;
__REALM_LOCK__
__EGRESS_IMPL__
let leaseEgress = null;
let run = null;
const reply = typeof postMessage === 'function' ? postMessage.bind(self) : null;
self.onmessage = async (ev) => {
  const d = ev.data;
  if (!d) return;
  if (d.type === 'egress_refresh') { leaseEgress?.refresh(d.egress); return; }
  if (d.type === 'init_workload') {
    try {
      leaseEgress = createLeaseEgress(d.egress);
      lockWorkloadRealm(self);
      const wl = await import(WORKLOAD_URL);
      run = typeof wl.run === 'function' ? wl.run : null;
    } catch (e) {
      reply({ type: 'module_ready', entry: false, error: 'workload_import_failed: ' + ((e && e.message) || e) });
      return;
    }
    reply({ type: 'module_ready', entry: run !== null });
    return;
  }
  if (d.type === 'compute_task') {
    const t0 = performance.now();
    try {
      if (typeof run !== 'function') {
        reply({ type: 'kernel_error', error: 'unknown_workload: the module exports no run function' });
        return;
      }
      const result = leaseEgress ? await run(d.input, {...(d.ctx || {}),egress:leaseEgress.api}) : await run(d.input, d.ctx || {});
      reply({ type: 'kernel_done', result, computeMs: performance.now() - t0 });
    } catch (e) {
      reply({ type: 'kernel_error', error: (e && e.message) || String(e) });
    }
  }
};
`;

// The wasm-abi kernel: a second empty Blob Worker that holds the verified .wasm
// bytes and the generic ABI runtime. It hashes nothing: the shell does that.
// It is the SAME realm lock as the module kernel, applied before the instantiate.
// The verified bytes and the task input travel over postMessage: the lock denies
// fetch and Blob in this realm long before any workload code runs.
//
// The task input travels as bytes (`inputBytes`), not as JSON: the ABI is bytes
// in, bytes out, and the platform never parses a workload's input a second time.
export const WASM_ABI_KERNEL_SRC = `
const MAX_PAGES = __MAX_PAGES__;
const MAX_OUTPUT_BYTES = __MAX_OUTPUT_BYTES__;
__REALM_LOCK__
__WASM_ABI_IMPL__
let wasmBytes = null;
let moduleReady = false;
const reply = typeof postMessage === 'function' ? postMessage.bind(self) : null;
self.onmessage = async (ev) => {
  const d = ev.data;
  if (!d) return;
  if (d.type === 'init_workload') {
    try {
      lockWorkloadRealm(self);
      wasmBytes = d.wasmBytes ? new Uint8Array(d.wasmBytes) : null;
      if (!wasmBytes || wasmBytes.length === 0) throw new Error('missing_wasm_bytes');
      const module = new WebAssembly.Module(wasmBytes);
      const verdict = __classifyWasmAbiModule(module, MAX_PAGES);
      if (!verdict.valid) {
        reply({ type: 'module_ready', entry: false, error: verdict.error });
        return;
      }
      moduleReady = true;
      reply({ type: 'module_ready', entry: true });
    } catch (e) {
      reply({ type: 'module_ready', entry: false, error: (e && e.message) || String(e) });
    }
    return;
  }
  if (d.type === 'compute_task') {
    const t0 = performance.now();
    try {
      if (!moduleReady || !wasmBytes) {
        reply({ type: 'kernel_error', error: 'wasm_abi_not_initialized' });
        return;
      }
      const input = d.inputBytes ? new Uint8Array(d.inputBytes) : new Uint8Array(0);
      const run = await __wasmAbiRuntime(input, wasmBytes, {
        maxPages: MAX_PAGES,
        maxOutputBytes: MAX_OUTPUT_BYTES
      });
      reply({ type: 'kernel_done', result: { output: { bytes: run.outputBytes } }, computeMs: performance.now() - t0 });
    } catch (e) {
      reply({ type: 'kernel_error', error: (e && e.message) || String(e) });
    }
  }
};
`;

export const WORKER_SRC = `
const ISOLATION_MODE = __ISOLATION_MODE__;
const KERNEL_URL = __KERNEL_URL__;
const WORKLOAD_URL = __WORKLOAD_URL__;
__REALM_LOCK__
__EGRESS_IMPL__
let leaseEgress = null;
__HASH_IMPL__
// Captured BEFORE any untrusted code runs. The workload can clobber every global
// it likes; these bindings are what the shell hashes with (D-W10-6).
const __hash = __sha256Impl;
const __bytes = __bytesImpl;
let run = null;
let kernel = null;
let kernelReady = false;
// The one reply channel to the runner. It is captured as a value BEFORE any
// untrusted code loads, so overriding self.postMessage cannot divert it.
const reply = typeof postMessage === 'function' ? postMessage.bind(self) : null;
const send = (msg) => { if (reply) reply(msg); };

// Accept only the three kernel message types. A forged task_done from the
// kernel is dropped here: the shell is the only writer of task_done.
function onKernelMessage(msgEv) {
  const m = msgEv && msgEv.data;
  if (!m || typeof m !== 'object') return;
  if (m.type === 'module_ready') {
    kernelReady = true;
    send({ type: 'module_ready', entry: m.entry === true, error: m.error || undefined });
    return;
  }
  if (m.type === 'kernel_done') {
    settle(m.result, m.computeMs);
    return;
  }
  if (m.type === 'kernel_error') {
    send({ type: 'task_error', error: String(m.error || 'kernel_failed') });
  }
}

// The shell hashes the RAW output bytes of whatever the workload returned and
// forwards only the allowlisted shape. A claimed hash is never consulted.
function settle(result, computeMs) {
  const out = result && typeof result === 'object' ? result.output : null;
  if (!out || typeof out !== 'object' || !out.bytes) {
    send({ type: 'task_error', error: 'workload_output_invalid' });
    return;
  }
  let bytes;
  try { bytes = __bytes(out.bytes); } catch (_) {
    send({ type: 'task_error', error: 'workload_output_invalid' });
    return;
  }
  const digest = __hash(bytes);
  const gpu = result && result.meta && typeof result.meta === 'object' ? result.meta.gpu : null;
  send({
    type: 'task_done',
    result: {
      output: { n: Number.isFinite(out.n) ? out.n : null, bytes: bytes },
      meta: gpu === null || gpu === undefined ? {} : { gpu: gpu }
    },
    shellSha256: digest,
    computeMs: Number.isFinite(computeMs) ? computeMs : undefined
  });
}

self.onmessage = async (ev) => {
  const d = ev.data;
  if (!d) return;
  if (d.type === 'egress_refresh') {
    if (kernel) kernel.postMessage(d); else leaseEgress?.refresh(d.egress);
    return;
  }
  if (d.type === 'init_workload') {
    if (ISOLATION_MODE === 'separate+lock') {
      if (typeof Worker === 'undefined' || !KERNEL_URL) {
        send({ type: 'module_ready', entry: false, error: 'kernel_worker_unavailable' });
        return;
      }
      try {
        kernel = new Worker(KERNEL_URL, { type: 'module' });
        kernel.onmessage = onKernelMessage;
        kernel.onerror = (e) => {
          if (!kernelReady) {
            send({ type: 'module_ready', entry: false, error: 'kernel_worker_load_failed' });
            return;
          }
          send({ type: 'task_error', error: 'kernel_worker_failed: ' + ((e && e.message) || e) });
        };
        kernel.postMessage({ type: 'init_workload', egress:d.egress });
      } catch (e) {
        send({ type: 'module_ready', entry: false, error: 'kernel_worker_load_failed' });
      }
      return;
    }
    try {
      leaseEgress = createLeaseEgress(d.egress);
      lockWorkloadRealm(self);
      const wl = await import(WORKLOAD_URL);
      run = typeof wl.run === 'function' ? wl.run : null;
    } catch (e) {
      send({ type: 'module_ready', entry: false, error: 'workload_import_failed: ' + ((e && e.message) || e) });
      return;
    }
    send({ type: 'module_ready', entry: run !== null });
    return;
  }
  if (d.type === 'compute_task') {
    if (ISOLATION_MODE === 'separate+lock') {
      if (!kernel) {
        send({ type: 'task_error', error: 'kernel_unavailable' });
        return;
      }
      kernel.postMessage({ type: 'compute_task', input: d.input, ctx: d.ctx || {} });
      return;
    }
    const t0 = performance.now();
    try {
      if (typeof run !== 'function') {
        send({ type: 'task_error', error: 'unknown_workload: the module exports no run function' });
        return;
      }
      const result = leaseEgress ? await run(d.input, {...(d.ctx || {}),egress:leaseEgress.api}) : await run(d.input, d.ctx || {});
      settle(result, performance.now() - t0);
    } catch (e) {
      send({ type: 'task_error', error: (e && e.message) || String(e) });
    }
  }
};
`;

// The wasm-abi shell: the same contract as WORKER_SRC (it owns the reply channel
// and hashes the raw output bytes), but the workload is .wasm bytes and the
// input is bytes, so there is no module import and no `run` entry.
//
// In BOTH isolation modes the shell starts one kernel Worker: the wasm realm must
// never be the shell realm, because the shell holds the digest. A platform
// without a nested Worker declines the task instead of instantiating in place.
// The shell does NOT apply the realm lock: it holds no untrusted code, and the
// lock would remove the `Worker` capability the kernel is started with.
export const WASM_ABI_WORKER_SRC = `
const KERNEL_URL = __KERNEL_URL__;
__HASH_IMPL__
const __hash = __sha256Impl;
const __bytes = __bytesImpl;
let kernel = null;
let kernelReady = false;
const reply = typeof postMessage === 'function' ? postMessage.bind(self) : null;
const send = (msg) => { if (reply) reply(msg); };

function onKernelMessage(msgEv) {
  const m = msgEv && msgEv.data;
  if (!m || typeof m !== 'object') return;
  if (m.type === 'module_ready') {
    kernelReady = true;
    send({ type: 'module_ready', entry: m.entry === true, error: m.error || undefined });
    return;
  }
  if (m.type === 'kernel_done') {
    settle(m.result, m.computeMs);
    return;
  }
  if (m.type === 'kernel_error') {
    send({ type: 'task_error', error: String(m.error || 'kernel_failed') });
  }
}

// The shell hashes the RAW output bytes and forwards only the allowlisted shape.
// The kernel never writes task_done, so a forged digest cannot reach the runner.
function settle(result, computeMs) {
  const out = result && typeof result === 'object' ? result.output : null;
  if (!out || typeof out !== 'object' || !out.bytes) {
    send({ type: 'task_error', error: 'workload_output_invalid' });
    return;
  }
  let bytes;
  try { bytes = __bytes(out.bytes); } catch (_) {
    send({ type: 'task_error', error: 'workload_output_invalid' });
    return;
  }
  const digest = __hash(bytes);
  send({
    type: 'task_done',
    result: { output: { n: Number.isFinite(out.n) ? out.n : null, bytes: bytes }, meta: {} },
    shellSha256: digest,
    computeMs: Number.isFinite(computeMs) ? computeMs : undefined
  });
}

self.onmessage = async (ev) => {
  const d = ev.data;
  if (!d) return;
  if (d.type === 'init_workload') {
    if (typeof Worker === 'undefined' || !KERNEL_URL) {
      send({ type: 'module_ready', entry: false, error: 'kernel_worker_unavailable' });
      return;
    }
    try {
      kernel = new Worker(KERNEL_URL, { type: 'module' });
      kernel.onmessage = onKernelMessage;
      kernel.onerror = (e) => {
        if (!kernelReady) {
          send({ type: 'module_ready', entry: false, error: 'kernel_worker_load_failed' });
          return;
        }
        send({ type: 'task_error', error: 'kernel_worker_failed: ' + ((e && e.message) || e) });
      };
      // The verified .wasm bytes travel to the kernel, which re-classifies them
      // under its own lock. The shell never instantiates a module (D-W10-10).
      kernel.postMessage({ type: 'init_workload', wasmBytes: d.wasmBytes });
    } catch (e) {
      send({ type: 'module_ready', entry: false, error: 'kernel_worker_load_failed' });
    }
    return;
  }
  if (d.type === 'compute_task') {
    if (!kernel) {
      send({ type: 'task_error', error: 'kernel_unavailable' });
      return;
    }
    const inputBytes = d.inputBytes ? new Uint8Array(d.inputBytes) : new Uint8Array(0);
    kernel.postMessage({ type: 'compute_task', inputBytes: inputBytes });
  }
};
`;

// The realm lock and the hash implementation travel as source text: a blob:
// Worker cannot import platform modules (its origin is opaque), so both are
// inlined from the same functions the platform itself uses. The hash functions
// get PRIVATE names inside the Worker (`__bytesImpl`, `__sha256Impl`): a workload
// that clobbers the global `toBytes`/`sha256Pure` name then has nothing to
// clobber, and the shell digest stays the shell's (D-W10-6).
const realmLockSource = lockWorkloadRealm.toString();
const hashSource = [
  toBytes.toString().replace(/^export\s+/, '').replace(/\btoBytes\b/g, '__bytesImpl'),
  sha256Pure.toString().replace(/^export\s+/, '').replace(/\btoBytes\b/g, '__bytesImpl').replace(/\bsha256Pure\b/g, '__sha256Impl')
].join('\n');

// The wasm-abi runtime and classifier travel the same way. They are given
// private names (`__wasmAbiRuntime`, `__classifyWasmAbiModule`) so a workload
// cannot reach them by their platform name.
const wasmAbiSource = classifyWasmAbiModule.toString().replace(/^export\s+/, '').replace(/\bclassifyWasmAbiModule\b/g, '__classifyWasmAbiModule')
  + '\n' + wasmAbiRuntime.toString().replace(/^export\s+/, '').replace(/\bwasmAbiRuntime\b/g, '__wasmAbiRuntime');

/**
 * Build the wasm-abi kernel Worker script.
 *
 * @param {Object} options
 * @param {number} options.maxPages the memory cap in 64 KiB pages
 * @param {number} options.maxOutputBytes the output cap
 * @returns {string} the kernel Worker script
 */
export function buildWasmAbiKernelSource(options = {}) {
  return WASM_ABI_KERNEL_SRC
    .replace('__REALM_LOCK__', () => realmLockSource)
    .replace('__WASM_ABI_IMPL__', () => wasmAbiSource)
    .replace('__MAX_PAGES__', () => JSON.stringify(normalizeWasmMaxPages(options.maxPages)))
    .replace('__MAX_OUTPUT_BYTES__', () => JSON.stringify(
      Number.isFinite(options.maxOutputBytes) ? options.maxOutputBytes : MAX_OUTPUT_BYTES));
}

/**
 * Build the wasm-abi shell Worker script. It is the same shell contract as the
 * module kind: it owns the reply channel, hashes the raw output bytes and never
 * lets the kernel write `task_done`.
 *
 * Distinct from buildModuleWorkerSource because a wasm-abi workload has no
 * `run` export and no import URL: the shell posts the verified bytes instead.
 *
 * @param {Object} options
 * @param {string} options.kernelUrl blob: URL of the wasm-abi kernel script
 * @param {number} options.maxPages memory cap in 64 KiB pages
 * @param {number} options.maxOutputBytes output cap
 * @returns {string} the shell Worker script
 */
export function buildWasmAbiWorkerSource(options = {}) {
  return WASM_ABI_WORKER_SRC
    .replace('__HASH_IMPL__', () => hashSource)
    .replace('__KERNEL_URL__', () => JSON.stringify(options.kernelUrl ? String(options.kernelUrl) : ''));
}

/**
 * Create the blob URL of the wasm-abi kernel Worker script.
 *
 * @param {Object} options the same options buildWasmAbiKernelSource takes
 * @returns {string} blob: URL of the kernel script
 */
export function createWasmAbiKernelBlobUrl(options = {}) {
  return URL.createObjectURL(new Blob([buildWasmAbiKernelSource(options)], { type: 'text/javascript' }));
}

/**
 * Build the kernel Worker script with the verified workload URL pinned into it.
 *
 * @param {string} workloadUrl blob: URL of the verified workload module
 * @returns {string} the kernel Worker script
 */
export function buildKernelSource(workloadUrl) {
  return KERNEL_SRC
    .replace('__REALM_LOCK__', () => realmLockSource)
    .replace('__EGRESS_IMPL__', () => egressRealmSource)
    .replace('__WORKLOAD_URL__', () => JSON.stringify(String(workloadUrl)));
}

/**
 * Normalize an isolation mode. Unknown and missing values answer the strict
 * default, never the weaker mode.
 *
 * @param {*} value
 * @returns {string} 'separate+lock' | 'lock-only'
 */
export function normalizeIsolationMode(value) {
  const mode = typeof value === 'string' ? value.trim() : '';
  return ISOLATION_MODES.includes(mode) ? mode : DEFAULT_ISOLATION_MODE;
}

/**
 * Resolve the isolation mode of a job: the signed envelope wins, then the page
 * config field the parent sent with the job, then the default.
 *
 * @param {Object} job
 * @returns {string}
 */
export function resolveIsolationMode(job) {
  if (job && job.envelope && job.envelope.isolationMode !== undefined) {
    return normalizeIsolationMode(job.envelope.isolationMode);
  }
  return normalizeIsolationMode(job ? job.isolationMode : undefined);
}

/**
 * Normalize a workload kind. A missing value is the js-module default; an
 * unknown value is NOT coerced into a runnable kind (the job is refused).
 *
 * @param {*} value
 * @returns {string|null} 'js-module' | 'wasm-abi' | null (unknown)
 */
export function normalizeWorkloadKind(value) {
  if (value === undefined || value === null) return DEFAULT_WORKLOAD_KIND;
  const kind = typeof value === 'string' ? value.trim() : '';
  return WORKLOAD_KINDS.includes(kind) ? kind : null;
}

/**
 * Resolve the workload kind of a job. It comes from the signed envelope, then
 * the page config the parent sent, then the js-module default. The envelope is
 * authoritative because the kind selects the ABI the module bytes are held to.
 *
 * @param {Object} job
 * @returns {{ kind: string|null, source: string }} kind null = unsupported
 */
export function resolveWorkloadKind(job) {
  const declared = job && job.envelope && job.envelope.workload
    ? job.envelope.workload.kind
    : undefined;
  if (declared !== undefined) return { kind: normalizeWorkloadKind(declared), source: 'envelope' };
  if (job && job.workloadKind !== undefined) {
    return { kind: normalizeWorkloadKind(job.workloadKind), source: 'job' };
  }
  return { kind: DEFAULT_WORKLOAD_KIND, source: 'default' };
}

/**
 * Normalize the wasm-abi memory cap from a page/job value in PAGES.
 *
 * @param {*} value
 * @returns {number} a whole page count in [1, MAX_WASM_MEMORY_PAGES]
 */
export function normalizeWasmMaxPages(value) {
  if (value === undefined || value === null || value === '') return DEFAULT_WASM_MAX_MEMORY_PAGES;
  const pages = Number(value);
  if (!Number.isFinite(pages) || !Number.isInteger(pages) || pages < 1) {
    return DEFAULT_WASM_MAX_MEMORY_PAGES;
  }
  return Math.min(pages, MAX_WASM_MEMORY_PAGES);
}

/**
 * Resolve the wasm-abi memory cap of a job: the signed envelope wins, then the
 * page config, then the default. A hostile value can only lower the cap.
 *
 * @param {Object} job
 * @returns {number} pages
 */
export function resolveWasmMaxPages(job) {
  if (job && job.envelope && job.envelope.workload && job.envelope.workload.maxMemoryPages !== undefined) {
    return normalizeWasmMaxPages(job.envelope.workload.maxMemoryPages);
  }
  return normalizeWasmMaxPages(job ? job.wasmMaxPages : undefined);
}

/**
 * Classify a wasm-abi module BEFORE instantiation.
 *
 * The rule of D-W10-10 is "no imports": a module that declares any is refused,
 * because an import is a host capability the platform never grants. The one
 * exception is `env.abort`, which the kernel supplies as a stub. Memory is
 * required and must be a module-defined memory (an imported memory is an import).
 *
 * Self-contained: the kernel carries this function as SOURCE TEXT, so it may
 * reference no module-level binding (its arguments carry everything it needs).
 *
 * @param {WebAssembly.Module} module
 * @param {number} maxPages the declared maximum the module may ask for
 * @param {string[]} [exports] the required exported function names
 * @param {string[]} [allowedImports] the tolerated imports
 * @returns {{ valid: boolean, error: string|null }}
 */
export function classifyWasmAbiModule(module, maxPages, exports, allowedImports) {
  const required = Array.isArray(exports) ? exports : ['ccgsc_alloc', 'ccgsc_run', 'ccgsc_out'];
  const allowed = Array.isArray(allowedImports) ? allowedImports : ['env.abort'];
  if (!Number.isFinite(maxPages) || maxPages < 1) return { valid: false, error: 'wasm_abi_invalid_memory_cap' };
  let declaredImports = [];
  let declaredExports = [];
  try {
    declaredImports = WebAssembly.Module.imports(module);
    declaredExports = WebAssembly.Module.exports(module);
  } catch (e) {
    return { valid: false, error: 'wasm_abi_invalid_module' };
  }
  for (const imp of declaredImports) {
    if (!allowed.includes(imp.module + '.' + imp.name)) {
      return { valid: false, error: 'wasm_abi_import_not_allowed' };
    }
  }
  for (const name of required) {
    if (!declaredExports.some((ex) => ex.name === name && ex.kind === 'function')) {
      return { valid: false, error: 'wasm_abi_missing_export' };
    }
  }
  if (!declaredExports.some((ex) => ex.kind === 'memory')) {
    return { valid: false, error: 'wasm_abi_missing_memory' };
  }
  // The declared MAXIMUM is not exposed by the JS API: the bound is enforced on
  // the real memory before and after the run (a growing module cannot exceed it
  // silently, it fails the task).
  return { valid: true, error: null };
}

/**
 * The sandboxed runtime of a wasm-abi workload. It runs INSIDE the kernel realm
 * (its source travels as text, like the realm lock), so the module never shares a
 * realm with the shell that hashes the output.
 *
 * Contract (D-W10-10):
 *   - instantiate with no imports (only an env.abort stub that throws);
 *   - copy the task input bytes into the module memory at ccgsc_alloc(inLen);
 *   - call ccgsc_run(inPtr, inLen); a negative return is an error code;
 *   - read outLen bytes from ccgsc_out().
 *
 * Self-contained: the kernel carries this function as SOURCE TEXT, so it may
 * reference no module-level binding (its arguments carry everything it needs).
 *
 * @param {Uint8Array} inputBytes
 * @param {ArrayBuffer} wasmBytes
 * @param {Object} limits { maxPages, maxOutputBytes }
 * @returns {Promise<{ outputBytes: Uint8Array, outLen: number }>}
 */
export function wasmAbiRuntime(inputBytes, wasmBytes, limits) {
  const maxPages = limits && limits.maxPages;
  const maxOutputBytes = limits && limits.maxOutputBytes;
  const memoryCap = maxPages * 65536;
  const checkMemory = (memory) => {
    if (!(memory instanceof WebAssembly.Memory)) throw new Error('wasm_abi_missing_memory');
    if (memory.buffer.byteLength > memoryCap) throw new Error('wasm_abi_memory_cap');
  };
  // A wasm-abi module imports NOTHING. The one tolerated import is the abort
  // stub: a program abort is a task failure, never a way back into the host.
  const abort = () => {
    throw new Error('wasm_abi_abort');
  };
  return WebAssembly.instantiate(wasmBytes, { env: { abort } }).then((instance) => {
    const exported = (instance && instance.exports) || (instance && instance.instance && instance.instance.exports) || {};
    const memory = exported.memory;
    checkMemory(memory);
    for (const name of ['ccgsc_alloc', 'ccgsc_run', 'ccgsc_out']) {
      if (typeof exported[name] !== 'function') throw new Error('wasm_abi_missing_export');
    }
    const inLen = inputBytes.length;
    const inPtr = exported.ccgsc_alloc(inLen);
    if (!Number.isInteger(inPtr) || inPtr < 0 || inPtr + inLen > memory.buffer.byteLength) {
      throw new Error('wasm_abi_alloc_out_of_range');
    }
    new Uint8Array(memory.buffer, inPtr, inLen).set(inputBytes);
    const outLen = exported.ccgsc_run(inPtr, inLen);
    if (!Number.isInteger(outLen)) throw new Error('wasm_abi_bad_return');
    // A negative return is the module's own error code; it is never a length.
    if (outLen < 0) throw new Error('wasm_abi_error_code:' + outLen);
    if (outLen > maxOutputBytes) throw new Error('output_too_large');
    // The run may have grown the memory; the cap holds after it too.
    checkMemory(memory);
    const outPtr = exported.ccgsc_out();
    if (!Number.isInteger(outPtr) || outPtr < 0 || outPtr + outLen > memory.buffer.byteLength) {
      throw new Error('wasm_abi_out_of_range');
    }
    return { outputBytes: new Uint8Array(memory.buffer, outPtr, outLen).slice(), outLen };
  });
}

/**
 * Pin the verified URLs into the shell. Both travel as JSON string literals, so
 * neither can break out of an import or a Worker constructor.
 *
 * @param {string} workloadUrl blob: URL of the verified workload module
 * @param {Object} [options]
 * @param {string} [options.mode] isolation mode (default separate+lock)
 * @param {string} [options.kernelUrl] blob: URL of the kernel Worker script
 * @returns {string} the module Worker script
 */
export function buildModuleWorkerSource(workloadUrl, options = {}) {
  return WORKER_SRC
    .replace('__REALM_LOCK__', () => realmLockSource)
    .replace('__EGRESS_IMPL__', () => egressRealmSource)
    .replace('__HASH_IMPL__', () => hashSource)
    .replace('__ISOLATION_MODE__', () => JSON.stringify(normalizeIsolationMode(options.mode)))
    .replace('__KERNEL_URL__', () => JSON.stringify(options.kernelUrl ? String(options.kernelUrl) : ''))
    .replace('__WORKLOAD_URL__', () => JSON.stringify(String(workloadUrl)));
}

/**
 * Create the blob URL of the kernel Worker script.
 *
 * @param {string} workloadUrl blob: URL of the verified workload module
 * @returns {string} blob: URL of the kernel script
 */
export function createKernelBlobUrl(workloadUrl) {
  return URL.createObjectURL(new Blob([buildKernelSource(workloadUrl)], { type: 'text/javascript' }));
}

/**
 * Create the blob URL of the verified workload source. The workload keeps its
 * ES module syntax; the platform never rewrites, strips or inlines it (D-W10-4).
 *
 * @param {string} workloadSource the verified workload module source
 * @returns {string} blob: URL of the workload module
 */
export function createWorkloadBlobUrl(workloadSource) {
  return URL.createObjectURL(new Blob([workloadSource], { type: 'text/javascript' }));
}

/**
 * Sanitize workload-controlled meta into the allowlisted result shape. A
 * workload can never write identity, verification status or any other control
 * field into the platform result (ChatGPT HIGH: no spread of workload objects).
 * Only primitive facts pass; every unknown key, object and non-finite number
 * is dropped.
 *
 * @param {Object} gpu the workload's meta.gpu
 * @returns {Object|null} the allowlisted gpu meta, or null
 */
export function sanitizeGpuMeta(_workloadReportedGpu, trustedLane = null) {
  // A workload's claimed GPU timings/adapter are unverified and must NEVER
  // become platform evidence. A platform-allocated GPU lane proves only that
  // an adapter was assigned, not that the workload actually used it.
  if (!trustedLane || trustedLane.kind !== 'gpu') return null;
  return { laneAssigned: true };
}

// ---------------------------------------------------------------- Inline Generation

// The marker that opens the generated inline block of runner.html.
export const RUNNER_INLINE_BEGIN = '/* INLINED FROM js/runner.js - generated, do not edit */';

/**
 * Build the inline script of runner.html from the source of js/runner.js.
 *
 * js/runner.js is the single source of the runner. runner.html inlines it, because
 * the sandbox has an opaque origin and a module fetch of js/runner.js would be
 * blocked by CORS. The export keywords are stripped, because the inline copy is a
 * plain module script and does not export anything.
 *
 * @param {string} source the text of js/runner.js
 * @returns {string} the exact text of the inline block
 */
export function buildInlineRunnerScript(source) {
  const text = String(source == null ? '' : source);
  const stripped = text
    .replace(/^import \{ createGPUPicker, createLanePool \} from '\.\/gpu_picker_T83\.js';$/m, () => createGPUPicker.toString() + '\n' + createLanePool.toString())
    .replace(/^import \{ sha256Hex, sha256Pure, toBytes \} from '\.\/platform\/hash\.js';$/m,
      () => [toBytes, sha256Pure, sha256Hex].map(fn => fn.toString()).join('\n'))
    .replace(/^export\s+(?=(?:async\s+function|function|const|let|var|class)\b)/gm, '')
    .replace(/^export\s*\{[^}]*\}\s*;?\s*$/gm, '')
    .replace(/\s+$/, '');
  // T91 gate repair | Codex | 2026-11-20: deterministic LF output on Windows,
  // including factory source read through Function.toString(). No runtime change.
  return RUNNER_INLINE_BEGIN + '\n' + stripped.replace(/\r\n?/g, '\n') + '\n';
}

// ---------------------------------------------------------------- Runner State & Bootstrap Protocol

let bootstrapped = false;
let bootstrapRejected = 0;
let port = null;
let sessionNonce = null;
let lanePoolPromise;
let gpuPaused = false;
let gpuSupported = null;
const egressRefreshers = new Map();

export function refuseBootstrap(reason) {
  bootstrapRejected++;
  // A refusal must be visible, not only counted.
  try {
    console.warn('[runner] bootstrap refused: ' + reason);
  } catch (_) {}
  if (port) {
    port.postMessage({ type: 'check', name: 'bootstrap_refused', status: 'FAIL', detail: reason });
  }
}

/**
 * Handle and validate the MessageChannel handshake bootstrap message.
 *
 * The trust anchor is ev.source === parent. The document referrer is deliberately
 * NOT consulted: in an opaque sandbox the referrer is empty or unrelated, so the
 * half check it produced was skipped exactly when it mattered.
 *
 * @param {Object} ev
 * @param {Object} [state]
 * @returns {{ accepted: boolean, reason?: string, port?: MessagePort, nonce?: string }}
 */
export function handleBootstrapMessage(ev, state = {}) {
  if (!ev) {
    return { accepted: false, reason: 'null_event' };
  }

  const expectedParent = state.parent !== undefined ? state.parent : (typeof parent !== 'undefined' ? parent : null);
  if (ev.source !== expectedParent) {
    return { accepted: false, reason: 'source_not_parent' };
  }

  const isBootstrapped = state.bootstrapped !== undefined ? state.bootstrapped : bootstrapped;
  if (isBootstrapped) {
    return { accepted: false, reason: 'already_bootstrapped' };
  }

  const d = ev.data;
  if (!d || d.type !== 'init' || typeof d.nonce !== 'string' || !d.nonce) {
    return { accepted: false, reason: 'invalid_init_message' };
  }

  const transferredPort = ev.ports && ev.ports[0];
  if (!transferredPort) {
    return { accepted: false, reason: 'no_port_transferred' };
  }

  return { accepted: true, port: transferredPort, nonce: d.nonce };
}

// Exactly ONE bootstrap MessagePort is accepted (WWPP-R39.1). The listener is only
// installed inside the sandbox iframe: in a top level page parent === window.
if (typeof window !== 'undefined' && typeof window.parent !== 'undefined' && window.parent !== window) {
  window.addEventListener('message', (ev) => {
    const res = handleBootstrapMessage(ev, {
      bootstrapped,
      parent: typeof parent !== 'undefined' ? parent : null
    });

    if (!res.accepted) {
      refuseBootstrap(res.reason);
      return;
    }

    bootstrapped = true;
    port = res.port;
    sessionNonce = res.nonce;
    port.onmessage = onPortMessage;

    // Ack with the nonce over the port (never over window.postMessage).
    port.postMessage({
      type: 'ready',
      nonce: res.nonce,
      selfOrigin: String(self.origin),
      hw: (typeof navigator !== 'undefined' && navigator.hardwareConcurrency) || 2,
      buildId: RUNNER_BUILD_ID,
      // R10-FIX owner D-W10-9: the runner self-reports the pin the generated page
      // baked in, so the parent never has to guess which runner it is driving.
      runnerPin: runnerSelfPin()
    });

    // The capability contract advertises WebGPU: the runner states the truth.
    probeWebGPU().then((gpu) => {
      if (port) {
        port.postMessage({
          type: 'check',
          name: 'webgpu',
          status: gpu.supported ? 'PASS' : 'SKIP',
          detail: gpu.detail
        });
      }
    });
  });
}

/**
 * Report whether this runner can execute a GPU task. The capability contract
 * advertises WebGPU, so the runner probes instead of guessing.
 *
 * @returns {Promise<{ supported: boolean, detail: string }>}
 */
export async function probeWebGPU() {
  try {
    if (typeof navigator === 'undefined' || !navigator.gpu || typeof navigator.gpu.requestAdapter !== 'function') {
      return { supported: false, detail: 'navigator.gpu is unavailable' };
    }
    const adapter = await navigator.gpu.requestAdapter();
    if (!adapter) {
      return { supported: false, detail: 'no WebGPU adapter' };
    }
    return { supported: true, detail: 'WebGPU adapter acquired' };
  } catch (e) {
    return { supported: false, detail: 'WebGPU probe failed: ' + ((e && e.message) || e) };
  }
}

async function ensureGpuProbe() {
  if (gpuSupported === null) {
    gpuSupported = (await probeWebGPU()).supported;
  }
  return gpuSupported;
}

/**
 * Handle one message of the port. Every message is validated against the protocol
 * schema before it is acted on: an invalid message is dropped, never executed.
 *
 * @param {Object} ev the MessageEvent of the port
 * @param {MessagePort} [customPort] the port to answer on (default: the bound port)
 */
export function onPortMessage(ev, customPort = null) {
  const d = ev && ev.data;
  if (!d || typeof d !== 'object') return;
  const targetPort = customPort || port;

  // Runtime schema validation: an invalid message is dropped, never executed.
  const schema = validateMessageSchema(d);
  if (!schema.valid) {
    try {
      console.warn('[runner] dropped invalid port message: ' + schema.error);
    } catch (_) {}
    if (targetPort) {
      targetPort.postMessage({ type: 'check', name: 'message_schema', status: 'FAIL', detail: schema.error });
    }
    return;
  }

  if (d.type === 'ping') {
    if (targetPort) targetPort.postMessage({ type: 'pong', seq: d.seq });
    return;
  }

  if (d.type === 'gpu-guard') {
    gpuPaused = !d.allowed;
    lanePoolPromise?.then(lanes => lanes.pause(gpuPaused));
    return;
  }
  if (d.type === 'egress-refresh') {
    if (d.nonce === sessionNonce) egressRefreshers.get(JSON.stringify([d.jobId,d.taskId]))?.(d.egress);
    return;
  }
  if (d.type === 'job') {
    handleJob(d, targetPort);
    return;
  }
}

/**
 * Run one job. Only one job runs at a time: a second job is refused, so the
 * runner can never exceed its own worker cap.
 *
 * @param {Object} job
 * @param {MessagePort} [customPort]
 * @param {Object} [options]
 * @param {Object} [options.keyRing] the pinned operator key ring (default OPERATOR_KEYS)
 * @returns {Promise<Object>}
 */
export async function handleJob(job, customPort = null, options = {}) {
  const targetPort = customPort || port;
  if (!lanePoolPromise) lanePoolPromise = createGPUPicker().enumerate().then(gpus =>
    createLanePool(computeWorkerCap(globalThis.navigator?.hardwareConcurrency), gpus));
  const lanes = await lanePoolPromise;
  lanes.pause(gpuPaused || job.gpuAllowed === false);
  try {
    return await lanes.run(job.needs || 'cpu', async lane => {
      const result = await runJob(job, null, {...options, lane});
      if (lane.kind === 'gpu' && result.verifyPass && !result.ok) throw Error(result.error);
      if (targetPort) targetPort.postMessage(result);
      return result;
    });
  } catch (err) {
    const result = {type:'result', jobId:job.jobId, taskId:job.taskId, ok:false,
      verifyPass:true, error:String(err.message || err), timings:{computeMs:0}};
    if (targetPort) targetPort.postMessage(result);
    return result;
  }
}

async function runJob(job, targetPort, options) {
  const timings = {};
  const wasmBytes = job.wasm; // Transferred ArrayBuffer
  const fail = (error, verifyPass, extra) => {
    if (timings.computeMs === undefined) timings.computeMs = 0;
    const payload = Object.assign({
      type: 'result',
      jobId: job.jobId,
      taskId: job.taskId,
      ok: false,
      verifyPass: verifyPass,
      error: error,
      workers: 0,
      timings: timings
    }, extra || {});
    if (targetPort) {
      targetPort.postMessage(payload);
    }
    return payload;
  };

  // 0. The job must belong to this session.
  if (sessionNonce && job.nonce && job.nonce !== sessionNonce) {
    return fail('nonce_mismatch', false);
  }

  // 1. Pre-execution verification gate (R37.1) against the PINNED operator key.
  const verifyResult = await verifyEnvelope({
    envelope: job.envelope,
    envSig: job.envSig,
    wasmBytes: wasmBytes,
    keyRing: options.keyRing
  });
  timings.verifyMs = verifyResult.verifyMs;

  if (!verifyResult.valid) {
    return fail(verifyResult.error, false);
  }

  // 1b. D-W10-8: every declared workload file must arrive with the digest the
  //     signed envelope pins, and no undeclared file may travel with the job.
  //     The entry module is job.wasm (verified above by its own digest); every
  //     other declared file travels in job.files. This runs before any import,
  //     so a bad file is declined, never loaded.
  if (job.envelope.workload !== undefined) {
    const declared = job.envelope.workload.files;
    const delivered = job.files && typeof job.files === 'object' ? job.files : {};
    for (const path of Object.keys(delivered)) {
      if (!declared.some(f => f.path === path)) {
        return fail('workload_file_not_declared', false);
      }
    }
    for (const file of declared) {
      if (file.sha256 === job.envelope.artifactSha256) continue;
      const bytes = delivered[file.path];
      if (!bytes) {
        return fail('workload_file_missing', false);
      }
      let actual;
      try {
        actual = await sha256Hex(toBytes(bytes));
      } catch (_) {
        return fail('workload_file_invalid', false);
      }
      if (actual !== file.sha256) {
        return fail('workload_file_sha256_mismatch', false);
      }
    }
  } else if (job.files && Object.keys(job.files).length > 0) {
    // Extra files without a signed per-file digest have no authority at all.
    return fail('workload_file_not_declared', false);
  }

  // 2. The declared digest of the job must match the bytes that arrived.
  if (typeof job.wasmSha256 === 'string' && job.wasmSha256) {
    const actual = await sha256Hex(wasmBytes);
    if (actual !== job.wasmSha256) {
      return fail('job_wasm_sha256_mismatch', true);
    }
  }

  // 3. The signed input of the task must equal the bytes that arrived, and the
  //    lease identity the server signed must be the one the tab claims (G3).
  //    The exact signed inputJSON bytes are hashed - JSON re-serialization is
  //    never accepted (ChatGPT HIGH: generic gate before parse/worker).
  let input;
  try {
    if (typeof job.inputJSON === 'string' && utf8ByteLength(job.inputJSON) > MAX_INPUT_JSON_BYTES) {
      return fail('task_input_too_large', false);
    }
    if (job.jobId !== job.envelope.jobId || job.taskId !== job.envelope.taskId ||
        (job.leaseToken !== undefined && job.envelope.leaseToken !== undefined && job.leaseToken !== job.envelope.leaseToken) ||
        (job.workerId !== undefined && job.envelope.workerId !== undefined && job.workerId !== job.envelope.workerId) ||
        typeof job.inputJSON !== 'string' ||
        await sha256Hex(new TextEncoder().encode(job.inputJSON)) !== job.envelope.inputSha256) {
      return fail('task_input_binding', false);
    }
    const raw = JSON.parse(job.inputJSON);
    input = raw.json || raw;
  } catch (err) {
    return fail('task_input_binding', false);
  }

  // 4. Execution: a Worker from verified blobs is mandatory (D-W10-4). There is
  //    no main thread fallback, because synchronous work cannot be preempted by
  //    the timeout timer. The KIND selects WHICH generic shell runs (D-W10-10):
  //    a js-module shell that imports `run(input, ctx)`, or a wasm-abi shell
  //    that holds the verified bytes and calls the C ABI. Neither knows a
  //    workload. A platform without module workers declines the task.
  const cap = computeWorkerCap(typeof navigator !== 'undefined' ? navigator.hardwareConcurrency : 2);
  const workersRequested = Math.max(1, Number(job.workersRequested) || 1);
  const workersToSpawn = 1;

  // D-W10-10: the kind is part of the signed envelope when it is declared. An
  // unknown kind is refused, never coerced into the js-module default.
  const workloadKind = resolveWorkloadKind(job);
  if (workloadKind.kind === null) {
    return fail('unsupported_workload_kind', false);
  }
  timings.workloadKind = workloadKind.kind;

  // D-W10-8: every extra workload file arrives as verified bytes. A job without
  // an envelope file list carries exactly the one entry artifact.
  const files = job.files && typeof job.files === 'object' ? job.files : {};
  const filePaths = Object.keys(files);
  if (filePaths.length > MAX_WORKLOAD_FILES) {
    return fail('workload_too_many_files', false);
  }
  // The byte ABI has no channel for companion files: refusing is honest, where
  // ignoring them would silently drop signed bytes.
  if (workloadKind.kind === 'wasm-abi' && filePaths.length > 0) {
    return fail('wasm_abi_files_not_supported', false);
  }
  let filesBytes = 0;
  for (const path of filePaths) {
    let byteLength = 0;
    try {
      byteLength = toBytes(files[path]).byteLength;
    } catch (_) {
      return fail('workload_file_invalid', false);
    }
    filesBytes += byteLength;
  }
  if (filesBytes + toBytes(wasmBytes).byteLength > MAX_WASM_BYTES) {
    return fail('workload_too_large', false);
  }

  // D-W10-10: the input of a byte-ABI task is `{"bytesBase64": "..."}` inside the
  // SIGNED input JSON. Decoding it after the binding check means the bytes the
  // module reads are exactly the bytes the envelope authorizes; a side channel
  // for binary input would be unsigned.
  let inputBytes = null;
  if (workloadKind.kind === 'wasm-abi') {
    const encoded = input && typeof input === 'object' && typeof input.bytesBase64 === 'string'
      ? input.bytesBase64
      : null;
    if (encoded === null) {
      return fail('wasm_abi_input_invalid', false);
    }
    try {
      inputBytes = bytesOfBase64(encoded);
    } catch (_) {
      return fail('wasm_abi_input_invalid', false);
    }
    if (inputBytes.byteLength > MAX_WASM_BYTES) {
      return fail('wasm_abi_input_too_large', false);
    }
  }

  const comp0 = nowMs();
  const timeoutMs = (typeof job.timeoutMs === 'number' && job.timeoutMs > 0)
    ? job.timeoutMs
    : ((job.task && typeof job.task.timeoutMs === 'number' && job.task.timeoutMs > 0) ? job.task.timeoutMs : 30000);
  const isolationMode = resolveIsolationMode(job);
  timings.isolationMode = isolationMode;
  const maxPages = resolveWasmMaxPages(job);

  let timedOut = false;
  let timerId = null;
  const workers = [];
  let workloadUrl = null;
  let shellUrl = null;
  let kernelUrl = null;
  let moduleReady = false;
  const refreshKey = JSON.stringify([job.jobId,job.taskId]);

  try {
    if (typeof Worker === 'undefined') {
      return fail('task_failed: worker_unavailable', true);
    }

    let shellSource;
    if (workloadKind.kind === 'wasm-abi') {
      // The module is classified BEFORE instantiation: a failed classification
      // declines the task (it is a malformed workload, not a compute failure).
      let abiModule;
      try {
        abiModule = new WebAssembly.Module(toBytes(wasmBytes));
      } catch (_) {
        return fail('task_declined: wasm_abi_invalid_module', true, { workers: 0 });
      }
      const verdict = classifyWasmAbiModule(abiModule, maxPages);
      if (!verdict.valid) {
        return fail('task_declined: ' + verdict.error, true, { workers: 0 });
      }
      // The wasm realm is always a separate Worker: the shell holds the digest.
      kernelUrl = createWasmAbiKernelBlobUrl({ maxPages, maxOutputBytes: MAX_OUTPUT_BYTES });
      shellSource = buildWasmAbiWorkerSource({ kernelUrl });
    } else {
      const workloadSource = new TextDecoder().decode(toBytes(wasmBytes));
      if (workloadImports(workloadSource)) {
        // Design section 2: a workload imports nothing. The shell's own workload
        // import is injected by buildModuleWorkerSource, never part of the bytes.
        // The ban covers the static, dynamic and re-export forms (R10-FIX row 4).
        throw new Error('workload_must_not_import');
      }
      workloadUrl = createWorkloadBlobUrl(workloadSource);
      // D-W10-6: separate+lock builds the kernel script too; the shell then never
      // imports the workload, so the bytes live only in the kernel realm.
      if (isolationMode === 'separate+lock') {
        kernelUrl = createKernelBlobUrl(workloadUrl);
      }
      shellSource = buildModuleWorkerSource(workloadUrl, { mode: isolationMode, kernelUrl });
    }
    shellUrl = URL.createObjectURL(new Blob([shellSource], { type: 'text/javascript' }));
    let worker;
    try {
      worker = new Worker(shellUrl, { type: 'module' });
    } catch (e) {
      // D-W10-4: no module-worker support = task declined, not failed.
      return fail('task_declined: module_worker_unsupported', true, { workers: 0 });
    }
    workers.push(worker);
    if (job.egress && workloadKind.kind !== 'wasm-abi') {
      egressRefreshers.set(refreshKey, egress => worker.postMessage({type:'egress_refresh',egress}));
    }

    const lane = options.lane || {};
    const gpuLane = lane.kind === 'gpu';
    const ctx = {
      lane: lane.kind || null,
      gpuLane,
      gpuId: gpuLane ? lane.id : undefined,
      requireGPU: gpuLane,
      timeoutMs,
      files
    };
    // Each kind has its own task message; neither is sent to the other's shell.
    const computeMessage = workloadKind.kind === 'wasm-abi'
      ? { type: 'compute_task', inputBytes }
      : { type: 'compute_task', input, ctx };
    // The wasm kind hands the verified bytes over at init; the module kind
    // imports them itself through the pinned blob URL.
    const initMessage = workloadKind.kind === 'wasm-abi'
      ? { type: 'init_workload', wasmBytes: toBytes(wasmBytes) }
      : { type: 'init_workload', egress:job.egress || null };

    const computePromise = new Promise((resolve, reject) => {
      worker.onmessage = (msgEv) => {
        const m = msgEv.data;
        if (!m || typeof m !== 'object') return;
        if (m.type === 'module_ready') {
          moduleReady = true;
          if (m.entry === false) {
            reject(new Error(m.error || 'unknown_workload: the module exports no run function'));
            return;
          }
          worker.postMessage(computeMessage);
        } else if (m.type === 'task_done') {
          // D-W10-6: the shell already hashed the raw bytes in its own thread.
          // The runner never trusts a workload-claimed digest: it is recorded
          // only to prove the shell computed one, and it must match.
          resolve({ result: m.result, shellSha256: m.shellSha256 });
        } else if (m.type === 'task_error') {
          reject(new Error(m.error));
        }
      };
      worker.onerror = (err) => {
        if (!moduleReady) {
          // The module worker never came up: declined, not failed (D-W10-4).
          reject(new Error('__module_worker_load_failed__'));
          return;
        }
        reject(err instanceof Error ? err : new Error(String((err && err.message) || err)));
      };
      worker.postMessage(initMessage);
    });

    const timeoutPromise = new Promise((_, reject) => {
      timerId = setTimeout(() => {
        timedOut = true;
        reject(new Error('task_timeout'));
      }, timeoutMs);
    });

    const settled = await Promise.race([computePromise, timeoutPromise]);
    const result = settled ? settled.result : null;
    timings.computeMs = nowMs() - comp0;

    // 5. The platform owns the output hash; the workload only returns bytes/meta.
    //    Two separate digest domains (W10b): output.sha256 hashes the RAW output
    //    bytes; the settlement JSON digest is computed by the platform submit
    //    path (contribute.js) over the result JSON. Workload-controlled meta is
    //    sanitized into the allowlisted shape - no spread, no control fields.
    const output = result && result.output !== undefined ? result.output : null;
    const metaOut = sanitizeGpuMeta(result?.meta?.gpu, options.lane);
    let outputDigest = null;
    let outputN = null;
    if (output === null || !output || typeof output !== 'object' || !output.bytes) {
      return fail('task_failed: workload_output_invalid', true, { workers: workersToSpawn });
    }
    {
      // Generic shape validation: when input declares a numerical dimension,
      // the output cannot silently claim a different dimension.
      if (Number.isInteger(input?.n) && output.n !== input.n) {
        return fail('task_failed: workload_output_invalid', true, { workers: workersToSpawn });
      }
      let outputBytes;
      try {
        outputBytes = toBytes(output.bytes);
      } catch (e) {
        return fail('task_failed: workload_output_invalid', true, { workers: workersToSpawn });
      }
      if (outputBytes.length > MAX_OUTPUT_BYTES) {
        return fail('output_too_large', true, { workers: workersToSpawn });
      }
      outputDigest = await sha256Hex(outputBytes);
      outputN = Number.isFinite(output.n) ? output.n : null;
      // D-W10-6: the shell hashed the same bytes in its own thread. A missing or
      // divergent shell digest means the work did not come through the shell.
      if (settled && settled.shellSha256 !== outputDigest) {
        return fail('task_failed: shell_hash_mismatch', true, { workers: workersToSpawn });
      }
    }
    const resultPayload = {
      type: 'result',
      jobId: job.jobId,
      taskId: job.taskId,
      ok: true,
      verifyPass: true,
      output: outputDigest !== null ? { n: outputN, sha256: outputDigest } : null,
      ...(metaOut ? { gpu: metaOut } : {}),
      workers: workersToSpawn,
      workerCap: cap,
      workersRequested: workersRequested,
      timings: timings
    };
    if (targetPort) {
      targetPort.postMessage(resultPayload);
    }
    return resultPayload;
  } catch (execErr) {
    timings.computeMs = nowMs() - comp0;
    // Only the timedOut flag decides the timeout verdict, never the error text.
    if (timedOut) {
      return fail('task_timeout', true, { workers: workersToSpawn });
    }
    const msg = String((execErr && execErr.message) || execErr);
    if (msg === '__module_worker_load_failed__') {
      return fail('task_declined: module_worker_load_failed', true, { workers: workersToSpawn });
    }
    return fail('task_failed: ' + msg, true, { workers: workersToSpawn });
  } finally {
    egressRefreshers.delete(refreshKey);
    if (timerId) clearTimeout(timerId);
    for (const w of workers) {
      try { w.terminate(); } catch (_) {}
    }
    if (typeof URL !== 'undefined' && URL.revokeObjectURL) {
      if (workloadUrl) URL.revokeObjectURL(workloadUrl);
      if (shellUrl) URL.revokeObjectURL(shellUrl);
      if (kernelUrl) URL.revokeObjectURL(kernelUrl);
    }
  }
}

// END OF runner.js


