// R10-FIX (minimax MINOR x2): the artifact cache.
//   1. A blocked IndexedDB was a NO-OP cache, not the in-memory fallback the page
//      promises: every lease refetched the artifact.
//   2. loadArtifact reported the REQUESTED digest for a hit it never re-hashed, so
//      poisoned bytes were reported as the artifact the lease authorized.
import test from 'node:test';
import assert from 'node:assert/strict';

import {
  createArtifactCache,
  createMemoryArtifactCache,
  loadArtifact
} from '../js/artifact_cache_T104.js';
import { sha256Hex } from '../js/contribute.js';

const GOOD = new TextEncoder().encode('fresh');
const EVIL = new TextEncoder().encode('evil');

test('a blocked IndexedDB falls back to the in-memory cache', async () => {
  const digest = await sha256Hex(GOOD);
  const broken = { open() { throw new Error('blocked'); } };
  const cache = createArtifactCache({ indexedDB: broken, sha256Hex });
  assert.equal(cache.kind, 'indexeddb', 'the store is still the primary');
  assert.equal(await cache.put(digest, GOOD), true, 'a blocked store stores nothing at all');
  const hit = await cache.get(digest);
  assert.ok(hit && hit.byteLength === GOOD.byteLength, 'the in-memory fallback is missing');
  assert.deepEqual(new Uint8Array(hit), GOOD);
  assert.equal(await cache.size(), 1, 'the fallback reports its own entries');
  assert.equal(await cache.clear(), 1, 'the fallback can be cleared');
  assert.equal(await cache.get(digest), null);
});

test('an IndexedDB that rejects every operation also falls back', async () => {
  const digest = await sha256Hex(GOOD);
  // A store whose transactions abort: the IndexedDB cache stays fail-closed (it
  // must), so the page-level factory is what promises the fallback.
  const dead = {
    open() {
      const req = {};
      setTimeout(() => {
        req.result = { transaction() { throw new Error('store closed'); }, objectStoreNames: { contains: () => true } };
        if (req.onsuccess) req.onsuccess();
      }, 0);
      return req;
    }
  };
  const cache = createArtifactCache({ indexedDB: dead, sha256Hex });
  assert.equal(await cache.put(digest, GOOD), true, 'a store that cannot transact loses only the speedup');
  const hit = await cache.get(digest);
  assert.ok(hit && hit.byteLength === GOOD.byteLength, 'the memory fallback did not take over');
  assert.deepEqual(new Uint8Array(hit), GOOD);
});

test('loadArtifact re-hashes a hit and never reports a digest it did not verify', async () => {
  const requested = await sha256Hex(GOOD);
  const cache = createMemoryArtifactCache(); // no digest impl: the store itself cannot re-hash
  await cache.put(requested, EVIL.slice().buffer);
  let fetched = 0;
  const out = await loadArtifact({
    cache,
    digest: requested,
    sha256Hex,
    fetchBytes: async () => { fetched += 1; return GOOD.slice().buffer; }
  });
  assert.equal(fetched, 1, 'a poisoned hit was served instead of the fetch');
  assert.equal(out.sha256, requested, 'the returned digest is the digest of the bytes');
  assert.equal(out.cached, false, 'bytes that did not hash to the key are not a hit');
  assert.deepEqual(new Uint8Array(out.bytes), GOOD);
});

test('loadArtifact serves a genuine hit and says so', async () => {
  const digest = await sha256Hex(GOOD);
  const cache = createMemoryArtifactCache();
  await cache.put(digest, GOOD.slice().buffer);
  let fetched = 0;
  const out = await loadArtifact({
    cache, digest, sha256Hex,
    fetchBytes: async () => { fetched += 1; return GOOD.slice().buffer; }
  });
  assert.equal(fetched, 0, 'a verified hit must skip the fetch');
  assert.equal(out.sha256, digest);
  assert.equal(out.cached, true);
});

test('a cache without a digest implementation is never trusted for a hit', async () => {
  const requested = await sha256Hex(GOOD);
  const cache = createMemoryArtifactCache();
  await cache.put(requested, EVIL.slice().buffer);
  // loadArtifact is the last line of defence: it re-hashes even when the store
  // cannot. A mismatch must fall through to the fetch.
  const out = await loadArtifact({
    cache, digest: requested, sha256Hex,
    fetchBytes: async () => GOOD.slice().buffer
  });
  assert.deepEqual(new Uint8Array(out.bytes), GOOD);
  assert.equal(out.cached, false);
});
