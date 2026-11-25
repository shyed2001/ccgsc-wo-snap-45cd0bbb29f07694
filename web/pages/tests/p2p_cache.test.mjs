// WO R14 step 3 (D-W10-14): the provenance wrapper over the injectable
// artifact cache. The suite pins the cache write rule of design section 4:
// entries are stamped provenance=server only after sha256 verify, every get
// re-hashes, provenance=peer (and unstamped legacy) entries are never served,
// and a tampered entry degrades into a clean miss into the server fetch path.
//
// Truth: WO_R13_WS3_P2P_CLIENT_DESIGN.md sections 2.5/4; D-W10-14.
import test from 'node:test';
import assert from 'node:assert/strict';

import { createP2pArtifactCache } from '../js/platform/p2p/cache.js';
import { MAX_TRANSFER_BYTES } from '../js/platform/p2p/rules.js';

const MIB = 1024 * 1024;

// Deterministic non-cryptographic test double, NOT SHA-256. The real-hash
// test below and p2p_candidate.test.mjs cover SHA-256 separately.
function fakeHash(hexPrefix) {
  return async (bytes) => {
    const view = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
    let acc = 0x811c9dc5;
    for (const b of view) acc = ((acc ^ b) * 0x01000193) >>> 0;
    return (hexPrefix || acc.toString(16).padStart(8, '0')).padEnd(64, '0').slice(0, 64);
  };
}

function makeStore() {
  const map = new Map();
  const calls = { get: 0, put: 0, del: 0 };
  return {
    calls,
    map,
    async get(key) { calls.get++; return map.get(key) || null; },
    async put(key, value) { calls.put++; map.set(key, value); },
    async delete(key) { calls.del++; map.delete(key); }
  };
}

const BYTES_A = new Uint8Array([1, 2, 3, 4, 5, 6, 7, 8]);
const BYTES_B = new Uint8Array([9, 9, 9]);
const DIGEST_A = await fakeHash()(BYTES_A); // the digest fakeHash really computes for BYTES_A

// ---------------------------------------------------------------- The wrapper

test('cache wrapper requires an injectable store with get and put', () => {
  assert.throws(() => createP2pArtifactCache({}), /injectable cache/, 'no built-in store (design F3)');
  assert.throws(() => createP2pArtifactCache({ cache: { get: () => null } }), /injectable cache/);
  assert.throws(() => createP2pArtifactCache({ cache: null }), /injectable cache/);
});

test('putVerified stamps provenance=server only after sha256 verify', async () => {
  const store = makeStore();
  const cache = createP2pArtifactCache({ cache: store, hash: fakeHash() });
  const put = await cache.putVerified(DIGEST_A, BYTES_A);
  assert.deepEqual(put, { ok: true, reason: null, sha256: DIGEST_A, byteLength: 8 });
  const meta = await cache.peek(DIGEST_A);
  assert.equal(meta.provenance, 'server', 'the verified entry carries the server stamp');
  assert.equal(meta.sha256, DIGEST_A);
  assert.equal(meta.byteLength, 8);
});

test('putVerified refuses a digest the bytes do not hash to; nothing is stored', async () => {
  const store = makeStore();
  const cache = createP2pArtifactCache({ cache: store, hash: fakeHash() });
  const wrong = 'bb'.padEnd(64, '0');
  const put = await cache.putVerified(wrong, BYTES_A);
  assert.equal(put.ok, false);
  assert.equal(put.reason, 'hash');
  assert.equal(store.map.size, 0, 'no unverified entry exists');
  assert.equal(await cache.get(wrong), null);
});

test('putVerified refuses junk digest shapes before hashing anything', async () => {
  let hashed = 0;
  const countingHash = async (bytes) => { hashed++; return fakeHash()(bytes); };
  const cache = createP2pArtifactCache({ cache: makeStore(), hash: countingHash });
  for (const digest of ['', 'ZZZ', 'abc'.padEnd(63, '0'), 'abc'.padEnd(65, '0'), null, undefined, 42, {}]) {
    const put = await cache.putVerified(digest, BYTES_A);
    assert.equal(put.ok, false, `digest ${String(digest)} refused`);
    assert.equal(put.reason, 'digest');
  }
  assert.equal(hashed, 0, 'no hash call for a malformed digest');
});

test('putVerified refuses empty and oversized payloads (client refuses early)', async () => {
  const cache = createP2pArtifactCache({ cache: makeStore(), hash: fakeHash() });
  const empty = await cache.putVerified('cc'.padEnd(64, '0'), new Uint8Array(0));
  assert.equal(empty.reason, 'empty');
  const big = await cache.putVerified('dd'.padEnd(64, '0'), new Uint8Array(MAX_TRANSFER_BYTES + 1));
  assert.equal(big.reason, 'size');
  assert.equal(big.byteLength, MAX_TRANSFER_BYTES + 1);
  const exactBytes = new Uint8Array(MAX_TRANSFER_BYTES);
  const exactDigest = await fakeHash()(exactBytes);
  const exact = await cache.putVerified(exactDigest, exactBytes);
  assert.equal(exact.ok, true, 'a full 32 MiB artifact is inside the cap');
  const overBytes = new Uint8Array(MAX_TRANSFER_BYTES + 1);
  const over = await cache.putVerified(await fakeHash()(overBytes), overBytes);
  assert.equal(over.ok, false, 'one byte over the cap refuses before hashing');
  assert.equal(over.reason, 'size');
  assert.equal(cache.maxBytes, MAX_TRANSFER_BYTES);
  const small = createP2pArtifactCache({ cache: makeStore(), hash: fakeHash(), maxBytes: 4 });
  assert.equal((await small.putVerified('ff'.padEnd(64, '0'), BYTES_A)).reason, 'size', 'the pool knob caps earlier');
});

test('verified get re-hashes on every read and returns the bytes', async () => {
  const store = makeStore();
  let hashCalls = 0;
  const counting = async (bytes) => { hashCalls++; return fakeHash()(bytes); };
  const cache = createP2pArtifactCache({ cache: store, hash: counting });
  await cache.putVerified(DIGEST_A, BYTES_A);
  const first = hashCalls;
  const hit = await cache.get(DIGEST_A);
  assert.ok(hit && hit.byteLength === 8, 'the verified bytes come back');
  assert.ok(hashCalls > first, 'get re-hashed the stored bytes');
  const second = hashCalls;
  await cache.get(DIGEST_A);
  assert.ok(hashCalls > second, 'every get re-hashes, not only the first');
});

test('a tampered entry is rejected and evicted: the caller lands on the server fetch path', async () => {
  const store = makeStore();
  const cache = createP2pArtifactCache({ cache: store, hash: fakeHash() });
  await cache.putVerified(DIGEST_A, BYTES_A);
  // Poison the underlying store behind the wrapper's back.
  store.map.set(DIGEST_A, { ...store.map.get(DIGEST_A), bytes: BYTES_B });
  assert.equal(await cache.get(DIGEST_A), null, 'the re-hash gate rejects the tampered bytes');
  assert.equal(store.calls.del, 1, 'the poisoned entry was evicted');
  assert.equal(store.map.has(DIGEST_A), false);
  assert.equal(await cache.get(DIGEST_A), null, 'the next read is a clean miss');
});

test('provenance=peer entries are never served, even when the bytes verify', async () => {
  const store = makeStore();
  const cache = createP2pArtifactCache({ cache: store, hash: fakeHash() });
  const digestB = await fakeHash()(BYTES_B);
  store.map.set(digestB, { bytes: BYTES_B, sha256: digestB, provenance: 'peer', storedAt: 0 });
  assert.equal(await cache.get(digestB), null, 'peer-stamped bytes never leave the cache');
  assert.equal(store.calls.del, 0, 'a peer entry is refused, not evicted as tampered');
  const meta = await cache.peek(digestB);
  assert.equal(meta.provenance, 'peer', 'peek exposes the stamp without serving bytes');
});

test('unstamped legacy entries are never served (stamp only after verify)', async () => {
  const store = makeStore();
  const cache = createP2pArtifactCache({ cache: store, hash: fakeHash() });
  const digestB = await fakeHash()(BYTES_B);
  store.map.set(digestB, { bytes: BYTES_B, sha256: digestB, storedAt: 0 });
  assert.equal(await cache.get(digestB), null, 'an entry without provenance is an unverified entry');
});

test('a get with a junk digest shape is a miss, never a throw', async () => {
  const cache = createP2pArtifactCache({ cache: makeStore(), hash: fakeHash() });
  for (const digest of ['', 'ZZ', null, undefined, 42, {}]) {
    assert.equal(await cache.get(digest), null);
  }
});

test('a broken underlying cache is a lost speedup, never an error', async () => {
  const boom = {
    async get() { throw new Error('store exploded'); },
    async put() { throw new Error('store full'); },
    async delete() { throw new Error('store exploded'); }
  };
  const cache = createP2pArtifactCache({ cache: boom, hash: fakeHash() });
  const put = await cache.putVerified(DIGEST_A, BYTES_A);
  assert.equal(put.ok, false);
  assert.equal(put.reason, 'store', 'the verified bytes could not be kept');
  assert.equal(await cache.get(DIGEST_A), null, 'the requester falls back to the server fetch');
});

test('a broken hash fails closed at put and at get', async () => {
  const store = makeStore();
  const boomHash = async () => { throw new Error('no subtle crypto'); };
  const cache = createP2pArtifactCache({ cache: store, hash: boomHash });
  const put = await cache.putVerified(DIGEST_A, BYTES_A);
  assert.equal(put.ok, false);
  assert.equal(put.reason, 'hash');
  store.map.set(DIGEST_A, { bytes: BYTES_A, sha256: DIGEST_A, provenance: 'server', storedAt: 0 });
  assert.equal(await cache.get(DIGEST_A), null, 'no verified read without a working hash');
});

test('the default hash is the WebCrypto SHA-256 of the real bytes', async () => {
  const store = makeStore();
  const cache = createP2pArtifactCache({ cache: store });
  const bytes = new TextEncoder().encode('ccgsc p2p cache probe');
  const digest = await globalThis.crypto.subtle.digest('SHA-256', bytes)
    .then(d => Array.from(new Uint8Array(d)).map(b => b.toString(16).padStart(2, '0')).join(''));
  const put = await cache.putVerified(digest, bytes);
  assert.equal(put.ok, true, 'the default hash verifies the same digest WebCrypto computes');
  assert.equal(await cache.get(digest) === null, false);
  const wrong = await cache.putVerified('00'.padEnd(64, '0'), bytes);
  assert.equal(wrong.ok, false, 'and still refuses bytes under a foreign digest');
});

test('verified bytes are an owned snapshot the requester can relay', async () => {
  const cache = createP2pArtifactCache({ cache: makeStore(), hash: fakeHash() });
  await cache.putVerified(DIGEST_A, BYTES_A);
  const hit = await cache.get(DIGEST_A);
  assert.notEqual(hit, BYTES_A, 'callers cannot mutate the cache through a hit');
  assert.deepEqual(hit, BYTES_A, 'the byte content is unchanged');
  assert.equal(hit.byteLength > 0, true, 'matches the existing hit.byteLength check');
});
