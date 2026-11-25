// Finish Dash 2026-11-24: deterministic regressions against the R14 draft.
import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { createP2pArtifactCache } from '../js/platform/p2p/cache.js';
import { chunkPlan, fallbackFSM, MAX_TRANSFER_BYTES } from '../js/platform/p2p/rules.js';

const sha = async bytes => createHash('sha256').update(bytes).digest('hex');
const good = new Uint8Array([1, 2, 3, 4]);
const digest = await sha(good);
function fixture(options = {}) {
  const map = new Map();
  const store = { get: async k => map.get(k), put: async (k, v) => map.set(k, v), delete: async k => map.delete(k) };
  return { map, cache: createP2pArtifactCache({ cache: store, hash: sha, ...options }) };
}

test('R14 cache owns write and read buffers; callers cannot mutate verified entries', async () => {
  const { cache } = fixture();
  const input = good.slice();
  assert.equal((await cache.putVerified(digest, input)).ok, true);
  input.fill(9);
  const first = await cache.get(digest);
  assert.deepEqual(first, good);
  first.fill(8);
  assert.deepEqual(await cache.get(digest), good);
});

test('R14 put snapshots bytes before an asynchronous hash', async () => {
  let release;
  const gate = new Promise(resolve => { release = resolve; });
  const { cache } = fixture({ hash: async b => { const h = await sha(b); await gate; return h; } });
  const input = good.slice();
  const pending = cache.putVerified(digest, input);
  input.fill(9);
  release();
  assert.equal((await pending).ok, true);
  assert.deepEqual(await cache.get(digest), good);
});

test('R14 get snapshots stored bytes before asynchronous verification', async () => {
  let release;
  const gate = new Promise(resolve => { release = resolve; });
  const { cache, map } = fixture({ hash: async b => { const h = await sha(b); await gate; return h; } });
  const stored = good.slice();
  map.set(digest, { bytes: stored, sha256: digest, provenance: 'server' });
  const pending = cache.get(digest);
  await Promise.resolve();
  stored.fill(9);
  release();
  assert.deepEqual(await pending, good);
});

test('R14 cache rejects fake byte objects before invoking even an injectable hash', async () => {
  let hashes = 0;
  const { cache } = fixture({ hash: async () => { hashes++; return digest; } });
  assert.equal((await cache.putVerified(digest, { byteLength: 4 })).ok, false);
  assert.equal(hashes, 0);
});

test('R14 cache and chunk options cannot raise the 32 MiB hard cap', () => {
  const { cache } = fixture({ maxBytes: MAX_TRANSFER_BYTES * 2 });
  assert.equal(cache.maxBytes, MAX_TRANSFER_BYTES);
  assert.equal(chunkPlan(MAX_TRANSFER_BYTES + 1, { maxBytes: MAX_TRANSFER_BYTES * 2 }).ok, false);
});

test('R14 fallback rejects inherited property names as reasons or states', () => {
  for (const name of ['constructor', 'toString', '__proto__']) {
    assert.deepEqual(fallbackFSM('open', 'abort', name), { state: 'fallback', reason: 'channel' });
    assert.equal(fallbackFSM(name, 'offer').state, 'fallback');
  }
});

test('R14 asynchronous eviction failure remains a clean cache miss', async () => {
  const cache = createP2pArtifactCache({
    hash: sha,
    cache: {
      get: async () => ({ bytes: good, sha256: '0'.repeat(64), provenance: 'server' }),
      put: async () => {},
      delete: async () => { throw new Error('eviction unavailable'); }
    }
  });
  assert.equal(await cache.get(digest), null);
  await new Promise(resolve => setImmediate(resolve));
});

test('R14 store false is a failed write, not successful provenance', async () => {
  const { cache } = fixture({ cache: { get: async () => null, put: async () => false } });
  assert.equal((await cache.putVerified(digest, good)).ok, false);
});

test('R14 subarrays hash only their visible bytes', async () => {
  const { cache } = fixture();
  const backing = new Uint8Array([99, ...good, 99]);
  assert.equal((await cache.putVerified(digest, backing.subarray(1, 5))).ok, true);
  assert.deepEqual(await cache.get(digest), good);
});
