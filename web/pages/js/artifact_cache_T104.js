// Path: web/pages/js/artifact_cache_T104.js | Author: Claude Opus 5.5 head | 2026-11-21
// What/why: T104 step 6 - the artifact cache keyed by the sha256 of the leased
// artifact. Every lease names its module artifact by id and authorizes exactly
// one digest (task envelope artifactSha256); the cache stores the bytes under
// that digest, so a repeat lease of the same module skips the fetch.
//
// The cache is never the authority: the runner still verifies the signature,
// validates the envelope and hashes the bytes it received before compile
// (WWPP-R37.1, runner.js verifyEnvelope). A digest that is already in the store
// is hashed again on read, and a store entry whose bytes do not hash to its key
// is dropped - a corrupted or hostile store cannot make a wrong artifact pass.
//
// There is no memory sharing (R10.0): the cached bytes are cloned into a fresh
// ArrayBuffer for every job, exactly like a fetched body.
//
// Truth: docs/T104_INTEGRATED_PAGE_PLAN.md step 6, GRILL_WWPP_R1.md (R10.0,
// R37.1), owner T104 rules (issue #117).

'use strict';

export const ARTIFACT_CACHE_DB = 'ccgsc-t104-artifacts';
export const ARTIFACT_CACHE_TABLE = 'artifacts';
export const ARTIFACT_CACHE_MAX_ENTRIES = 32;
const HEX64 = /^[0-9a-f]{64}$/;

/**
 * Whether a digest has the shape of an artifact sha256. Anything else is never
 * used as a cache key.
 *
 * @param {string} digest
 * @returns {boolean}
 */
export function isArtifactDigest(digest) {
  return typeof digest === 'string' && HEX64.test(digest);
}

/**
 * Copy bytes into a fresh ArrayBuffer. The cached value is never handed out, so
 * no two lanes can alias one buffer (R10.0).
 *
 * @param {ArrayBuffer|Uint8Array} bytes
 * @returns {ArrayBuffer}
 */
export function cloneArtifactBytes(bytes) {
  if (bytes instanceof ArrayBuffer) return bytes.slice(0);
  const view = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes || 0);
  return view.slice().buffer;
}

/**
 * In-memory artifact cache. Same promise-based API as the IndexedDB store, so a
 * page that has no storage (private mode, blocked IndexedDB) uses it unchanged.
 *
 * @param {Object} [options]
 * @param {number} [options.maxEntries]
 * @param {Function} [options.sha256Hex] digest implementation (injected in tests)
 * @returns {Object} cache
 */
export function createMemoryArtifactCache(options = {}) {
  const maxEntries = Math.max(1, Math.floor(Number(options.maxEntries)) || ARTIFACT_CACHE_MAX_ENTRIES);
  const sha256 = typeof options.sha256Hex === 'function' ? options.sha256Hex : null;
  const entries = new Map(); // digest -> ArrayBuffer
  let hits = 0;
  let misses = 0;

  return {
    kind: 'memory',

    /**
     * Read the bytes of one digest. Bytes that do not hash to their key are
     * dropped (the entry is corrupt, the server is the authority).
     *
     * @param {string} digest
     * @returns {Promise<ArrayBuffer|null>}
     */
    async get(digest) {
      if (!isArtifactDigest(digest)) return null;
      const hit = entries.get(digest);
      if (!hit) {
        misses += 1;
        return null;
      }
      if (sha256) {
        const actual = await sha256(cloneArtifactBytes(hit));
        if (actual !== digest) {
          entries.delete(digest);
          misses += 1;
          return null;
        }
      }
      hits += 1;
      // Re-insert so the Map keeps a least-recently-used order.
      entries.delete(digest);
      entries.set(digest, hit);
      return cloneArtifactBytes(hit);
    },

    /**
     * Store the bytes of one digest. The digest of an entry is never recomputed
     * here: the caller hashed the bytes it is about to store, and the read path
     * re-hashes.
     *
     * @param {string} digest
     * @param {ArrayBuffer|Uint8Array} bytes
     * @returns {Promise<boolean>} whether the entry was stored
     */
    async put(digest, bytes) {
      if (!isArtifactDigest(digest)) return false;
      const copy = cloneArtifactBytes(bytes);
      if (!copy || copy.byteLength === 0) return false;
      entries.delete(digest);
      entries.set(digest, copy);
      while (entries.size > maxEntries) {
        const oldest = entries.keys().next();
        if (oldest.done) break;
        entries.delete(oldest.value);
      }
      return true;
    },

    /** @returns {Promise<number>} entries dropped or removed */
    async clear() {
      const count = entries.size;
      entries.clear();
      return count;
    },

    /** @returns {number} stored entries */
    size() { return entries.size; },

    /** @returns {{hits: number, misses: number, entries: number}} */
    stats() { return { hits, misses, entries: entries.size }; }
  };
}

function openIndexedDB(factory, name) {
  return new Promise((resolve, reject) => {
    let request;
    try {
      request = factory.open(name, 1);
    } catch (err) {
      reject(err);
      return;
    }
    request.onupgradeneeded = () => {
      const db = request.result;
      try {
        if (!db.objectStoreNames.contains(ARTIFACT_CACHE_TABLE)) db.createObjectStore(ARTIFACT_CACHE_TABLE);
      } catch (_) { /* a raced upgrade keeps the existing table */ }
    };
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error || new Error('indexeddb_open_failed'));
    request.onblocked = () => reject(new Error('indexeddb_blocked'));
  });
}

/**
 * IndexedDB-backed artifact cache. One database, one object store, the digest as
 * the key and `{bytes, stamp}` as the value. The stamp is the LRU order: a put
 * and a hit both write a fresh one, eviction takes the oldest, and a plain
 * ArrayBuffer value of the first T104 build (stamp 0, the oldest) keeps working
 * until its next read stamps it. Every call fails closed (resolves null / false)
 * when the store is unavailable, so a blocked IndexedDB only costs the speedup
 * and never a task.
 *
 * @param {Object} [options]
 * @param {Object} [options.indexedDB] injectable implementation
 * @param {string} [options.dbName]
 * @param {number} [options.maxEntries]
 * @param {Function} [options.sha256Hex]
 * @returns {Object} cache
 */
export function createIndexedDbArtifactCache(options = {}) {
  const factory = options.indexedDB || (typeof globalThis !== 'undefined' ? globalThis.indexedDB : null);
  const dbName = String(options.dbName || ARTIFACT_CACHE_DB);
  const maxEntries = Math.max(1, Math.floor(Number(options.maxEntries)) || ARTIFACT_CACHE_MAX_ENTRIES);
  const sha256 = typeof options.sha256Hex === 'function' ? options.sha256Hex : null;
  let dbPromise = null;
  let hits = 0;
  let misses = 0;
  let stampClock = 0;

  /**
   * A strictly increasing stamp for the LRU order. Two writes in one
   * millisecond still get different stamps, and a reload keeps its order
   * because Date.now() only grows.
   *
   * @returns {number}
   */
  function nextStamp() {
    stampClock = Math.max(Date.now(), stampClock + 1);
    return stampClock;
  }

  /**
   * Read one stored value. An entry of the first T104 build is a plain
   * ArrayBuffer; it is the oldest possible entry and is rewritten with a stamp
   * on its next successful read.
   *
   * @param {any} stored
   * @returns {{bytes: any, stamp: number}|null}
   */
  function entryOf(stored) {
    if (!stored) return null;
    if (stored.bytes) return { bytes: stored.bytes, stamp: Number(stored.stamp) || 0 };
    return { bytes: stored, stamp: 0 };
  }

  function db() {
    if (!factory || typeof factory.open !== 'function') return Promise.resolve(null);
    if (!dbPromise) {
      dbPromise = openIndexedDB(factory, dbName).catch(() => null);
    }
    return dbPromise;
  }

  function withStore(mode, run) {
    return db().then((handle) => {
      if (!handle) return null;
      return new Promise((resolve) => {
        let tx;
        try {
          tx = handle.transaction(ARTIFACT_CACHE_TABLE, mode);
        } catch (_) {
          resolve(null);
          return;
        }
        let result = null;
        tx.oncomplete = () => resolve(result);
        tx.onerror = () => resolve(null);
        tx.onabort = () => resolve(null);
        try {
          const store = tx.objectStore(ARTIFACT_CACHE_TABLE);
          const request = run(store);
          if (request) request.onsuccess = () => { result = request.result === undefined ? null : request.result; };
        } catch (_) {
          resolve(null);
        }
      });
    }).catch(() => null);
  }

  return {
    kind: 'indexeddb',
    dbName,

    async get(digest) {
      if (!isArtifactDigest(digest)) return null;
      const stored = await withStore('readonly', (store) => store.get(digest));
      const entry = entryOf(stored);
      if (!entry) {
        misses += 1;
        return null;
      }
      const copy = cloneArtifactBytes(entry.bytes);
      if (sha256) {
        const actual = await sha256(cloneArtifactBytes(copy));
        if (actual !== digest) {
          await withStore('readwrite', (store) => store.delete(digest));
          misses += 1;
          return null;
        }
      }
      hits += 1;
      // Touch the entry so eviction follows use, not the key order. A failed
      // touch only leaves the old stamp; the bytes are already in hand.
      await withStore('readwrite', (store) => store.put({ bytes: entry.bytes, stamp: nextStamp() }, digest));
      return copy;
    },

    async put(digest, bytes) {
      if (!isArtifactDigest(digest)) return false;
      const copy = cloneArtifactBytes(bytes);
      if (!copy || copy.byteLength === 0) return false;
      if (!(await db())) return false;
      await withStore('readwrite', (store) => store.put({ bytes: copy, stamp: nextStamp() }, digest));
      // Trim from the key list as it is AFTER the write: overwriting an existing
      // digest neither grows the store nor makes that digest a candidate for its
      // own eviction pass, and the digest just written is never dropped. Among
      // the other keys the oldest stamp goes first, so this cache is a real LRU
      // like the in-memory one. The reads cost little next to a network fetch.
      const keys = await withStore('readonly', (store) => store.getAllKeys());
      const others = (Array.isArray(keys) ? keys : []).filter((key) => key !== digest);
      if (others.length + 1 <= maxEntries) return true;
      const ages = [];
      for (const key of others) {
        const entry = entryOf(await withStore('readonly', (store) => store.get(key)));
        ages.push({ key, stamp: entry ? entry.stamp : 0 });
      }
      ages.sort((a, b) => a.stamp - b.stamp);
      const drop = ages.slice(0, others.length + 1 - maxEntries).map((age) => age.key);
      if (drop.length) {
        await withStore('readwrite', (store) => {
          for (const key of drop) store.delete(key);
          return null;
        });
      }
      return true;
    },

    async clear() {
      const keys = await withStore('readonly', (store) => store.getAllKeys());
      await withStore('readwrite', (store) => store.clear());
      return Array.isArray(keys) ? keys.length : 0;
    },

    /** @returns {Promise<number>} stored entries (0 when the store is unavailable) */
    async size() {
      const keys = await withStore('readonly', (store) => store.getAllKeys());
      return Array.isArray(keys) ? keys.length : 0;
    },

    /** @returns {{hits: number, misses: number}} */
    stats() { return { hits, misses }; }
  };
}

/**
 * The artifact cache of this page: IndexedDB when the browser offers it, the
 * in-memory cache otherwise. Both answer the same promise API.
 *
 * @param {Object} [options] see createIndexedDbArtifactCache
 * @returns {Object} cache
 */
export function createArtifactCache(options = {}) {
  const factory = options.indexedDB !== undefined
    ? options.indexedDB
    : (typeof globalThis !== 'undefined' ? globalThis.indexedDB : null);
  if (factory && typeof factory.open === 'function') {
    return createFallbackArtifactCache(createIndexedDbArtifactCache({ ...options, indexedDB: factory }), options);
  }
  return createMemoryArtifactCache(options);
}

/**
 * Wrap an IndexedDB cache so a store that cannot be opened or cannot transact
 * still caches - in memory.
 *
 * R10-FIX (minimax MINOR): a blocked IndexedDB (private mode, a denied origin)
 * used to make the whole cache a NO-OP: get() answered null, put() answered
 * false, and every lease refetched the artifact although the page promises the
 * in-memory fallback. The store stays the primary; the memory cache only answers
 * what the store could not. The bytes are verified by the same digest rule either
 * way, so the fallback never weakens the authority of the server.
 *
 * @param {Object} store an IndexedDB-backed cache
 * @param {Object} [options] see createMemoryArtifactCache
 * @returns {Object} cache
 */
export function createFallbackArtifactCache(store, options = {}) {
  const memory = createMemoryArtifactCache(options);
  return {
    kind: store.kind,
    dbName: store.dbName,

    async get(digest) {
      const hit = await store.get(digest);
      if (hit) return hit;
      return memory.get(digest);
    },

    async put(digest, bytes) {
      const stored = await store.put(digest, bytes);
      // A store that refuses the write holds nothing; the memory cache keeps it,
      // so the next lease of the same digest is still a hit.
      const kept = await memory.put(digest, bytes);
      return stored || kept;
    },

    async clear() {
      const fromStore = await store.clear();
      const fromMemory = await memory.clear();
      return fromStore + fromMemory;
    },

    async size() {
      return (await store.size()) + memory.size();
    },

    stats() {
      const s = typeof store.stats === 'function' ? store.stats() : { hits: 0, misses: 0 };
      const m = memory.stats();
      return { hits: s.hits + m.hits, misses: s.misses + m.misses };
    }
  };
}

/**
 * Read one artifact through the cache: a hit returns the stored bytes, a miss
 * fetches them with `fetchBytes`, hashes the body with `sha256Hex` and stores it
 * under the computed digest. The returned digest is what the caller sends as the
 * job's wasmSha256, so the runner hashes the very bytes this cache keyed.
 *
 * The server stays the authority: a caller compares the returned digest with the
 * digest the signed envelope authorizes and refuses the job on a mismatch.
 *
 * @param {Object} params
 * @param {Object} params.cache a cache from createArtifactCache
 * @param {string} params.digest the digest the lease expects
 * @param {Function} params.sha256Hex
 * @param {Function} params.fetchBytes async () => ArrayBuffer
 * @returns {Promise<{bytes: ArrayBuffer, sha256: string, cached: boolean}|null>}
 */
export async function loadArtifact({ cache, digest, sha256Hex: sha256, fetchBytes }) {
  if (!cache || typeof cache.get !== 'function' || typeof fetchBytes !== 'function') return null;
  if (isArtifactDigest(digest)) {
    const hit = await cache.get(digest);
    if (hit && hit.byteLength > 0) {
      // R10-FIX (minimax MINOR): the digest of the bytes that are returned is
      // reported, never the requested one. A hit that does not hash to its key is
      // not trusted: the fetch path runs and its real digest is reported.
      const actual = await sha256(hit);
      if (actual === digest) return { bytes: hit, sha256: actual, cached: true };
    }
  }
  const fetched = await fetchBytes();
  if (!fetched || !fetched.byteLength) return null;
  const actual = await sha256(fetched);
  if (typeof cache.put === 'function') {
    await cache.put(actual, cloneArtifactBytes(fetched));
  }
  return { bytes: fetched, sha256: actual, cached: false };
}

// END OF artifact_cache_T104.js
