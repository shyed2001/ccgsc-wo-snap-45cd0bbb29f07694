// WO R14 step 3 (D-W10-14) - provenance-stamped wrapper over the injectable
// artifact cache of the contributor page (ContributeApp options.artifactCache).
// Author: GLM (Z.ai) for Claude Opus 5.5 head / owner. Date: 2026-11-21.
// Truth: WO_R13_WS3_P2P_CLIENT_DESIGN.md sections 2.5/4 (steps 2/3 of the
// atomic plan); owner decision D-W10-14.
//
// Rules (fail closed everywhere):
//  - an entry is stamped provenance='server' ONLY after its bytes hash to the
//    digest it is stored under;
//  - every get re-hashes the stored bytes and evicts on any mismatch, so a
//    tampered entry degrades into a clean miss (the caller falls back to the
//    server fetch path);
//  - entries that are not provenance='server' are NEVER served. That covers
//    provenance='peer' entries and unstamped legacy entries of an older round.
//  - a broken underlying cache is a lost speedup, never a thrown error.
//
// The wrapper must own its store: entries are objects ({bytes, sha256,
// provenance, storedAt}), not the raw buffers of earlier rounds. Wrap a
// dedicated store or a namespaced view of the shared one.

// Finish Dash 2026-11-24: bounded owned byte snapshots and awaited eviction.
// putVerified is SERVER-INGEST ONLY: hashing proves integrity, not origin or
// authorization. A trusted caller must supply only server-fetched module bytes
// pinned by a lease. Never call it for peer bytes, inputs, results or receipts.
// This dedicated object store is not the raw-buffer T104 cache. No bootstrap
// or transport is installed here; the runner remains the signature authority.
'use strict';

import { MAX_TRANSFER_BYTES } from './rules.js';

const SHA256_RE = /^[0-9a-f]{64}$/;

/**
 * Default hash: WebCrypto SHA-256 hex, available in browsers and in Node.
 *
 * @param {ArrayBuffer|Uint8Array} bytes
 * @returns {Promise<string>}
 */
async function defaultSha256Hex(bytes) {
  const subtle = globalThis.crypto && globalThis.crypto.subtle;
  if (!subtle) throw new Error('WebCrypto subtle digest unavailable');
  const view = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
  const digest = await subtle.digest('SHA-256', view);
  return Array.from(new Uint8Array(digest)).map(b => b.toString(16).padStart(2, '0')).join('');
}

/**
 * Build the provenance wrapper around an injectable cache.
 *
 * @param {Object} options
 * @param {Object} options.cache injectable store with async get(key) and
 *   put(key, value); an optional delete(key) enables eviction of tampered
 *   entries. Required.
 * @param {Function} [options.hash] async (bytes) => sha256 hex; defaults to
 *   WebCrypto SHA-256. Injectable so tests can pin deterministic digests.
 * @param {*} [options.maxBytes] put-time size cap; defaults to the
 *   MAX_TRANSFER_BYTES rule of platform/p2p/rules.js
 * @returns {{putVerified: Function, get: Function, peek: Function, maxBytes: number}}
 */
export function createP2pArtifactCache(options = {}) {
  const store = options.cache;
  if (!store || typeof store.get !== 'function' || typeof store.put !== 'function') {
    throw new TypeError('createP2pArtifactCache requires an injectable cache with get() and put()');
  }
  const hash = typeof options.hash === 'function' ? options.hash : defaultSha256Hex;
  const maxBytesOpt = Math.floor(Number(options.maxBytes));
  const maxBytes = Number.isSafeInteger(maxBytesOpt) && maxBytesOpt > 0 ? Math.min(maxBytesOpt, MAX_TRANSFER_BYTES) : MAX_TRANSFER_BYTES;
  const hasDelete = typeof store.delete === 'function';

  async function evict(key) {
    if (!hasDelete) return;
    try { await store.delete(key); } catch (_) { /* eviction is best effort */ }
  }

  function isBytes(value) {
    return value instanceof ArrayBuffer
      || (value instanceof Uint8Array && value.buffer instanceof ArrayBuffer);
  }

  function snapshot(bytes) {
    // Always allocate: Buffer.slice() aliases, unlike Uint8Array.slice().
    return new Uint8Array(bytes instanceof ArrayBuffer ? new Uint8Array(bytes) : bytes);
  }

  /**
   * Store bytes under `digest` after verifying sha256(bytes) === digest.
   * Only a verified store stamps provenance='server'; a mismatch, a bad
   * digest shape, an empty or oversized payload stores nothing.
   *
   * @param {*} digest 64 hex chars, the digest the signed lease pinned
   * @param {ArrayBuffer|Uint8Array} bytes
   * @returns {Promise<{ok: boolean, reason: string|null, sha256: string|null, byteLength: number}>}
   */
  async function putVerified(digest, bytes) {
    const key = String(digest === undefined || digest === null ? '' : digest).toLowerCase();
    if (!SHA256_RE.test(key)) return { ok: false, reason: 'digest', sha256: null, byteLength: 0 };
    if (!isBytes(bytes) || !(bytes.byteLength > 0)) {
      return { ok: false, reason: 'empty', sha256: null, byteLength: 0 };
    }
    if (bytes.byteLength > maxBytes) {
      return { ok: false, reason: 'size', sha256: null, byteLength: bytes.byteLength };
    }
    let computed;
    try {
      bytes = snapshot(bytes);
      computed = String(await hash(bytes)).toLowerCase();
    } catch (_) {
      return { ok: false, reason: 'hash', sha256: null, byteLength: bytes.byteLength };
    }
    if (computed !== key) {
      return { ok: false, reason: 'hash', sha256: computed, byteLength: bytes.byteLength };
    }
    const entry = { bytes, sha256: key, provenance: 'server', storedAt: Date.now() };
    try {
      if (await store.put(key, entry) === false) {
        return { ok: false, reason: 'store', sha256: key, byteLength: bytes.byteLength };
      }
    } catch (_) {
      return { ok: false, reason: 'store', sha256: key, byteLength: bytes.byteLength };
    }
    return { ok: true, reason: null, sha256: key, byteLength: bytes.byteLength };
  }

  /**
   * Verified read, both for the requester (before the MessagePort relay) and
   * for the serving peer (only provenance='server' bytes ever leave this tab).
   *
   * Answers null - never bytes - when the digest shape is wrong, the entry is
   * missing, its provenance is not 'server', it is empty or oversized, or the
   * re-hash of the stored bytes disagrees. A disagreeing entry is evicted so
   * the next read is a clean miss into the server fetch path.
   *
   * @param {*} digest
   * @returns {Promise<ArrayBuffer|Uint8Array|null>} the verified bytes
   */
  async function get(digest) {
    const key = String(digest === undefined || digest === null ? '' : digest).toLowerCase();
    if (!SHA256_RE.test(key)) return null;
    let entry;
    try {
      entry = await store.get(key);
    } catch (_) {
      return null;
    }
    if (!entry || typeof entry !== 'object') return null;
    if (entry.provenance !== 'server') return null;
    if (String(entry.sha256 === undefined || entry.sha256 === null ? '' : entry.sha256).toLowerCase() !== key) {
      await evict(key);
      return null;
    }
    let bytes = entry.bytes;
    if (!isBytes(bytes) || !(bytes.byteLength > 0) || bytes.byteLength > maxBytes) {
      await evict(key);
      return null;
    }
    let computed;
    try {
      bytes = snapshot(bytes);
      computed = String(await hash(bytes)).toLowerCase();
    } catch (_) {
      return null;
    }
    if (computed !== key) {
      await evict(key);
      return null;
    }
    return bytes;
  }

  /**
   * Introspection without serving: metadata of an entry, never bytes. Used by
   * tests and the monitor counters; provenance travels with it so a caller
   * can distinguish a peer-stamped entry from a verified one.
   *
   * @param {*} digest
   * @returns {Promise<{sha256: string, provenance: string, byteLength: number}|null>}
   */
  async function peek(digest) {
    const key = String(digest === undefined || digest === null ? '' : digest).toLowerCase();
    if (!SHA256_RE.test(key)) return null;
    let entry;
    try {
      entry = await store.get(key);
    } catch (_) {
      return null;
    }
    if (!entry || typeof entry !== 'object') return null;
    return {
      sha256: String(entry.sha256 || ''),
      provenance: String(entry.provenance || ''),
      byteLength: isBytes(entry.bytes) ? entry.bytes.byteLength : 0
    };
  }

  return { putVerified, get, peek, maxBytes };
}

// END OF cache.js
