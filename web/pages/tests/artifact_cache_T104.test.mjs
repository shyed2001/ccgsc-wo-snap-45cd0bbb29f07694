// Path: web/pages/tests/artifact_cache_T104.test.mjs | Author: Claude Opus 5.5 head | 2026-11-21
// What/why: T104 step 6 - the artifact cache keyed by the sha256 of the leased
// artifact. The digest is the key, the bytes are re-hashed on read, a corrupted
// or hostile store entry is dropped, and a blocked IndexedDB degrades to the
// in-memory cache instead of failing a task. Plus the live path of
// ContributeApp#getLiveJob: a repeat lease of the same digest skips the fetch,
// a cache miss stores what it fetched, and the wasmSha256 of the job is the
// digest of the bytes that actually travel to the runner (the existing verify
// gate stays the authority). No network.

import { describe, it, after } from 'node:test';
import assert from 'node:assert/strict';
import { webcrypto } from 'node:crypto';
import {
  ARTIFACT_CACHE_DB,
  ARTIFACT_CACHE_TABLE,
  ARTIFACT_CACHE_MAX_ENTRIES,
  isArtifactDigest,
  cloneArtifactBytes,
  createMemoryArtifactCache,
  createIndexedDbArtifactCache,
  createArtifactCache,
  loadArtifact
} from '../js/artifact_cache_T104.js';
import { ContributeApp, sha256Hex, hexOf } from '../js/contribute.js';

const originalFetch = globalThis.fetch;
const bytes = (arr) => new Uint8Array(arr).buffer;

after(() => {
  globalThis.fetch = originalFetch;
});

function jsonResponse(status, body) {
  return { ok: status >= 200 && status < 300, status, json: async () => body };
}

// ---------------------------------------------------------------- fake IndexedDB

/**
 * The smallest IndexedDB that the cache uses: open, transaction, objectStore,
 * get/put/delete/getAllKeys/clear. Every request is asynchronous like the real
 * one, so the cache's promise handling is exercised.
 */
function fakeIndexedDB(initial = {}, options = {}) {
  const stores = new Map(Object.entries(initial).map(([k, v]) => [k, v]));
  const state = { opened: 0, failOpen: Boolean(options.failOpen), failPut: Boolean(options.failPut) };
  const factory = {
    open(name) {
      state.opened += 1;
      const request = { result: null, error: null, onsuccess: null, onerror: null, onupgradeneeded: null, onblocked: null };
      setTimeout(() => {
        if (state.failOpen) {
          request.error = new Error('blocked');
          request.onerror && request.onerror();
          return;
        }
        const db = {
          name,
          objectStoreNames: { contains: (name2) => name2 === ARTIFACT_CACHE_TABLE },
          transaction(storeName) {
            const tx = { oncomplete: null, onerror: null, onabort: null };
            let pending = 0;
            let finished = false;
            const settle = () => {
              pending -= 1;
              if (finished || pending > 0) return;
              finished = true;
              setTimeout(() => tx.oncomplete && tx.oncomplete(), 0);
            };
            const track = () => { pending += 1; };
            const done = (fn) => () => { fn(); settle(); };
            tx.objectStore = () => ({
              get: (key) => {
                track();
                const req = { result: stores.get(key), onsuccess: null };
                setTimeout(() => { if (req.onsuccess) done(req.onsuccess)(); else settle(); }, 0);
                return req;
              },
              put: (value, key) => {
                track();
                const req = { onsuccess: null };
                setTimeout(() => {
                  if (!state.failPut) stores.set(key, value);
                  if (req.onsuccess) done(req.onsuccess)(); else settle();
                }, 0);
                return req;
              },
              delete: (key) => {
                track();
                const req = { onsuccess: null };
                setTimeout(() => { stores.delete(key); if (req.onsuccess) done(req.onsuccess)(); else settle(); }, 0);
                return req;
              },
              clear: () => {
                track();
                const req = { onsuccess: null };
                setTimeout(() => { stores.clear(); if (req.onsuccess) done(req.onsuccess)(); else settle(); }, 0);
                return req;
              },
              getAllKeys: () => {
                track();
                const req = { result: Array.from(stores.keys()), onsuccess: null };
                setTimeout(() => { if (req.onsuccess) done(req.onsuccess)(); else settle(); }, 0);
                return req;
              }
            });
            // A transaction with no request still completes.
            track();
            setTimeout(settle, 0);
            return tx;
          }
        };
        request.result = db;
        request.onsuccess && request.onsuccess();
      }, 0);
      return request;
    }
  };
  return { factory, stores, state };
}

// ---------------------------------------------------------------- the digest

const A_BYTES = bytes([0, 97, 115, 109, 1, 0, 0, 0]);
// The sha256 of A_BYTES (the SAMPLE_WASM digest of contribute.js).
const A_DIGEST = '93a44bbb96c751218e4c00d479e4c14358122a389acca16205b1e4d0dc5f9476';

describe('T104 artifact cache rules', () => {

  it('accepts only the sha256 shape as a key', () => {
    assert.equal(isArtifactDigest(A_DIGEST), true);
    assert.equal(isArtifactDigest('a'.repeat(64)), true);
    assert.equal(isArtifactDigest('a'.repeat(63)), false);
    assert.equal(isArtifactDigest('A'.repeat(64)), false, 'hex is lower case');
    assert.equal(isArtifactDigest('../etc/passwd'), false);
    assert.equal(isArtifactDigest(''), false);
    assert.equal(isArtifactDigest(null), false);
    assert.equal(isArtifactDigest(42), false);
  });

  it('clones its bytes, so no two lanes alias one buffer (R10.0)', () => {
    const source = new Uint8Array([1, 2, 3]);
    const copy = cloneArtifactBytes(source);
    assert.equal(copy.byteLength, 3);
    assert.notEqual(copy, source.buffer, 'a fresh ArrayBuffer');
    const first = new ArrayBuffer(4);
    assert.notEqual(cloneArtifactBytes(first), first);
    // Mutating the source does not reach the copy.
    source[0] = 99;
    assert.equal(new Uint8Array(copy)[0], 1);
  });

});

describe('T104 in-memory artifact cache', () => {

  it('stores and returns a verified digest, keyed by sha256', async () => {
    const cache = createMemoryArtifactCache({ sha256Hex });
    assert.equal(await cache.get(A_DIGEST), null, 'empty');
    assert.equal(await cache.put(A_DIGEST, A_BYTES), true);
    const hit = await cache.get(A_DIGEST);
    assert.ok(hit instanceof ArrayBuffer);
    assert.deepEqual(Array.from(new Uint8Array(hit)), [0, 97, 115, 109, 1, 0, 0, 0]);
    assert.deepEqual(cache.stats(), { hits: 1, misses: 1, entries: 1 });
  });

  it('refuses a junk key and empty bytes', async () => {
    const cache = createMemoryArtifactCache({ sha256Hex });
    assert.equal(await cache.put('nope', A_BYTES), false);
    assert.equal(await cache.put(A_DIGEST, new ArrayBuffer(0)), false);
    assert.equal(await cache.get('nope'), null);
    assert.equal(cache.size(), 0);
  });

  it('drops an entry whose bytes do not hash to its key', async () => {
    const sha = async (buf) => hexOf(new Uint8Array(await webcrypto.subtle.digest('SHA-256', buf)));
    const wrong = 'f'.repeat(64);
    assert.notEqual(await sha(bytes([1, 2, 3])), wrong);
    // Seed the store with bytes under a digest they do not have.
    const cache = createMemoryArtifactCache({ sha256Hex: sha });
    await cache.put(wrong, bytes([1, 2, 3]));
    const hit = await cache.get(wrong);
    assert.equal(hit, null, 'a corrupted entry never serves bytes');
    assert.equal(cache.size(), 0, 'and it is dropped');
    assert.equal(cache.stats().hits, 0);
  });

  it('keeps the newest maxEntries entries', async () => {
    const cache = createMemoryArtifactCache({ maxEntries: 2 });
    const key = (n) => String(n).padStart(64, '0');
    await cache.put(key(1), bytes([1]));
    await cache.put(key(2), bytes([2]));
    await cache.put(key(3), bytes([3]));
    assert.equal(cache.size(), 2);
    assert.equal(await cache.get(key(1)), null, 'the oldest entry was dropped');
    assert.ok(await cache.get(key(3)));
    assert.ok(ARTIFACT_CACHE_MAX_ENTRIES >= 1);
  });

  it('a read moves the entry to the newest position', async () => {
    const cache = createMemoryArtifactCache({ maxEntries: 2 });
    const key = (n) => String(n).padStart(64, '0');
    await cache.put(key(1), bytes([1]));
    await cache.put(key(2), bytes([2]));
    await cache.get(key(1));            // key(1) becomes newest
    await cache.put(key(3), bytes([3])); // key(2) is now the oldest
    assert.ok(await cache.get(key(1)));
    assert.equal(await cache.get(key(2)), null);
  });

  it('clear empties it', async () => {
    const cache = createMemoryArtifactCache();
    await cache.put(A_DIGEST, A_BYTES);
    assert.equal(await cache.clear(), 1);
    assert.equal(cache.size(), 0);
    assert.equal(await cache.clear(), 0);
  });

});

describe('T104 IndexedDB artifact cache', () => {

  it('stores and returns an ArrayBuffer by digest', async () => {
    const { factory, stores } = fakeIndexedDB();
    const cache = createIndexedDbArtifactCache({ indexedDB: factory, sha256Hex });
    assert.equal(await cache.put(A_DIGEST, A_BYTES), true);
    assert.ok(stores.has(A_DIGEST));
    const hit = await cache.get(A_DIGEST);
    assert.deepEqual(Array.from(new Uint8Array(hit)), [0, 97, 115, 109, 1, 0, 0, 0]);
    assert.equal(await cache.size(), 1);
    assert.equal(await cache.clear(), 1);
    assert.equal(await cache.get(A_DIGEST), null);
  });

  it('a cache entry that does not hash to its key is dropped', async () => {
    const wrong = 'e'.repeat(64);
    const { factory, stores } = fakeIndexedDB({ [wrong]: bytes([9, 9, 9]) });
    const cache = createIndexedDbArtifactCache({ indexedDB: factory, sha256Hex });
    assert.equal(await cache.get(wrong), null);
    assert.equal(stores.has(wrong), false, 'the corrupt entry is deleted from the store');
  });

  it('an overwrite at capacity keeps the digest it just wrote (no self-delete)', async () => {
    const { factory, stores } = fakeIndexedDB();
    const cache = createIndexedDbArtifactCache({ indexedDB: factory, maxEntries: 2 });
    const first = 'a'.repeat(64);
    const second = 'b'.repeat(64);
    assert.equal(await cache.put(first, bytes([1])), true);
    assert.equal(await cache.put(second, bytes([2])), true);
    assert.equal(await cache.size(), 2, 'the store is at capacity');
    // Overwrite the first key with new bytes. The trim used to run on a key list
    // read BEFORE the put, so the key just written could be dropped by its own
    // eviction pass.
    assert.equal(await cache.put(first, bytes([9, 9])), true);
    assert.equal(stores.has(first), true, 'the key just written survives its own put');
    assert.deepEqual(Array.from(new Uint8Array(await cache.get(first))), [9, 9], 'and holds the new bytes');
    assert.equal(await cache.size(), 2, 'an overwrite never grows the store');
    assert.equal(stores.has(second), true, 'the other key is untouched');
  });

  it('evicts the least recently used entry, not the smallest key', async () => {
    const { factory, stores } = fakeIndexedDB();
    const cache = createIndexedDbArtifactCache({ indexedDB: factory, maxEntries: 2 });
    const a = 'a'.repeat(64);
    const b = 'b'.repeat(64);
    const c = 'c'.repeat(64);
    await cache.put(a, bytes([1]));
    await cache.put(b, bytes([2]));
    await cache.get(a);              // a is now the most recently used entry
    await cache.put(c, bytes([3]));  // b is the least recently used one
    assert.equal(stores.has(b), false, 'the least recently used entry goes first');
    assert.equal(stores.has(a), true, 'the entry a read touched stays');
    assert.equal(stores.has(c), true, 'the entry just written stays');
    assert.equal(await cache.size(), 2);
  });

  it('reads an entry of the T104 build shape (a plain ArrayBuffer value)', async () => {
    const { factory } = fakeIndexedDB({ [A_DIGEST]: bytes([0, 97, 115, 109, 1, 0, 0, 0]) });
    const cache = createIndexedDbArtifactCache({ indexedDB: factory, sha256Hex });
    const hit = await cache.get(A_DIGEST);
    assert.ok(hit instanceof ArrayBuffer);
    assert.deepEqual(Array.from(new Uint8Array(hit)), [0, 97, 115, 109, 1, 0, 0, 0]);
  });

  it('a blocked or broken store degrades to a miss, never an error', async () => {
    const blocked = fakeIndexedDB({}, { failOpen: true });
    const cache = createIndexedDbArtifactCache({ indexedDB: blocked.factory, sha256Hex });
    assert.equal(await cache.get(A_DIGEST), null);
    assert.equal(await cache.get('junk'), null);
    assert.equal(await cache.put(A_DIGEST, A_BYTES), false);
    assert.equal(await cache.size(), 0);

    const failing = fakeIndexedDB({}, { failPut: true });
    const cache2 = createIndexedDbArtifactCache({ indexedDB: failing.factory, sha256Hex });
    assert.equal(await cache2.put(A_DIGEST, A_BYTES), true, 'a store that ignores a put is still a valid cache');
    assert.equal(await cache2.get(A_DIGEST), null);
  });

  it('without an implementation every call fails closed', async () => {
    const cache = createIndexedDbArtifactCache({ indexedDB: null });
    assert.equal(await cache.get(A_DIGEST), null);
    assert.equal(await cache.put(A_DIGEST, A_BYTES), false);
    assert.equal(await cache.size(), 0);
    assert.equal(await cache.clear(), 0);
  });

  it('createArtifactCache picks IndexedDB when the browser offers it', () => {
    const { factory } = fakeIndexedDB();
    assert.equal(createArtifactCache({ indexedDB: factory }).kind, 'indexeddb');
    assert.equal(createArtifactCache({ indexedDB: null }).kind, 'memory');
    assert.equal(ARTIFACT_CACHE_DB, 'ccgsc-t104-artifacts');
    assert.equal(ARTIFACT_CACHE_TABLE, 'artifacts');
  });

});

describe('T104 artifact load (cache hit / miss)', () => {

  it('a hit skips the fetch; a miss fetches, hashes and stores', async () => {
    const cache = createMemoryArtifactCache({ sha256Hex });
    let fetches = 0;
    const fetchBytes = async () => { fetches += 1; return bytes([0, 97, 115, 109, 1, 0, 0, 0]); };

    const miss = await loadArtifact({ cache, digest: A_DIGEST, sha256Hex, fetchBytes });
    assert.equal(fetches, 1);
    assert.equal(miss.cached, false);
    assert.equal(miss.sha256, A_DIGEST, 'the digest of the fetched bytes');

    const hit = await loadArtifact({ cache, digest: A_DIGEST, sha256Hex, fetchBytes });
    assert.equal(fetches, 1, 'the second read never touches the network');
    assert.equal(hit.cached, true);
    assert.equal(hit.sha256, A_DIGEST);

    // An unknown digest still fetches, and the answer is named by its own hash.
    const other = await loadArtifact({ cache, digest: 'b'.repeat(64), sha256Hex, fetchBytes });
    assert.equal(fetches, 2);
    assert.equal(other.sha256, A_DIGEST);
  });

  it('empty or missing bytes answer null', async () => {
    const cache = createMemoryArtifactCache({ sha256Hex });
    assert.equal(await loadArtifact({ cache, digest: A_DIGEST, sha256Hex, fetchBytes: async () => new ArrayBuffer(0) }), null);
    assert.equal(await loadArtifact({ cache: null, digest: A_DIGEST, sha256Hex, fetchBytes: async () => A_BYTES }), null);
  });

});

describe('T104 getLiveJob uses the artifact cache', () => {

  function assignment(digest, taskId) {
    return {
      lease: { jobId: 'job-1', taskId, generation: 1, token: 'tok-' + taskId },
      taskEnvelope: {
        payload: JSON.stringify({
          version: 1, jobId: 'job-1', taskId,
          artifactSha256: digest, inputSha256: 'b'.repeat(64),
          issuedAt: 1, expiresAt: 9999999999, keyId: 'k'
        }),
        signature: 'c2ln'
      },
      moduleArtifactIds: ['art-1'],
      task: { input: { kind: 'matmul-f32-gpu', n: 4 } }
    };
  }

  async function withLease(sha, run) {
    const calls = [];
    const realSha = await sha256Hex(bytes([0, 97, 115, 109, 1, 0, 0, 0]));
    globalThis.fetch = async (url, opts) => {
      const target = String(url);
      calls.push({ url: target, method: (opts && opts.method) || 'GET' });
      if (target.includes('/leases')) return jsonResponse(200, assignment(realSha, 'task-1'));
      if (target.includes('/artifacts/')) {
        return { ok: true, status: 200, arrayBuffer: async () => bytes([0, 97, 115, 109, 1, 0, 0, 0]) };
      }
      return jsonResponse(200, {});
    };
    try {
      const app = new ContributeApp({ autoStart: false, artifactCache: sha });
      app.sourceMode = 'LIVE';
      app.tabIds = { tp: 'tab-tp' };
      app.tabId = 'tab-tp';
      app.runnerNonce = 'nonce-cache';
      await run(app, calls);
    } finally {
      globalThis.fetch = originalFetch;
    }
  }

  it('a second lease of the same digest skips the artifact fetch', async () => {
    const cache = createMemoryArtifactCache({ sha256Hex });
    await withLease(cache, async (app, calls) => {
      const first = await app.getLiveJob();
      assert.ok(first, 'the first lease has a job');
      assert.equal(calls.filter((c) => c.url.includes('/artifacts/')).length, 1);
      assert.equal(first.wasmSha256, await sha256Hex(bytes([0, 97, 115, 109, 1, 0, 0, 0])));

      const second = await app.getLiveJob();
      assert.ok(second, 'the cached lease still has a job');
      assert.equal(calls.filter((c) => c.url.includes('/artifacts/')).length, 1, 'no second fetch');
      assert.equal(second.wasmSha256, first.wasmSha256);
      assert.deepEqual(Array.from(new Uint8Array(second.wasm)), Array.from(new Uint8Array(first.wasm)));
    });
  });

  it('without a cache every lease fetches, exactly as before', async () => {
    await withLease(null, async (app, calls) => {
      await app.getLiveJob();
      await app.getLiveJob();
      assert.equal(calls.filter((c) => c.url.includes('/artifacts/')).length, 2);
    });
  });

  it('a stored digest is never trusted over the hash of the bytes', async () => {
    // R10-FIX: the lease authorizes a digest that is not the fetched bytes. The
    // client compares them and fails closed instead of dispatching the mismatch:
    // the job is refused, the lease is released, and the real digest is never
    // reported as the authorized one.
    const cache = createMemoryArtifactCache({ sha256Hex });
    const calls = [];
    globalThis.fetch = async (url, opts) => {
      const target = String(url);
      calls.push({ url: target, method: (opts && opts.method) || 'GET' });
      if (target.includes('/leases')) return jsonResponse(200, assignment('c'.repeat(64), 'task-x'));
      if (target.includes('/artifacts/')) return { ok: true, status: 200, arrayBuffer: async () => bytes([1, 2, 3, 4]) };
      return jsonResponse(200, {});
    };
    try {
      const app = new ContributeApp({ autoStart: false, artifactCache: cache });
      app.sourceMode = 'LIVE';
      app.tabIds = { tp: 'tab-tp' };
      app.tabId = 'tab-tp';
      app.runnerNonce = 'nonce-bad';
      const job = await app.getLiveJob();
      assert.equal(job, null, 'a mismatching artifact is never dispatched');
      const release = calls.filter((c) => c.url.includes('/results')).pop();
      assert.ok(release, 'the lease of the refused job is released');
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

});
