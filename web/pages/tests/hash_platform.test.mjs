// W10 design section 6: hash_platform.test.mjs
// What/why: web/pages/js/platform/hash.js is the one SHA-256 of the platform. The
// pure FIPS 180-4 fallback (used inside the sandboxed Worker, which has no
// crypto.subtle) must equal Node's crypto for every message length, including the
// FIPS block boundaries. The crypto.subtle path must answer the same hex.
import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';

import { sha256Hex, sha256Pure, toBytes } from '../js/platform/hash.js';

// Deterministic bytes, so a failure is reproducible.
function sample(len) {
  const out = new Uint8Array(len);
  for (let i = 0; i < len; i++) out[i] = (i * 31 + 7) & 0xff;
  return out;
}

const LENGTHS = [0, 1, 55, 56, 63, 64, 65, 1000, 102400, 230400];

test('platform/hash.js pure fallback equals node crypto at every block boundary', () => {
  for (const len of LENGTHS) {
    const bytes = sample(len);
    const want = createHash('sha256').update(bytes).digest('hex');
    assert.equal(sha256Pure(bytes), want, `sha256Pure(${len} bytes)`);
  }
});

test('platform/hash.js sha256Hex equals node crypto on bytes, ArrayBuffer and typed views', async () => {
  for (const len of LENGTHS) {
    const bytes = sample(len);
    const want = createHash('sha256').update(bytes).digest('hex');
    assert.equal(await sha256Hex(bytes), want, `sha256Hex(Uint8Array ${len})`);
    assert.equal(await sha256Hex(bytes.buffer), want, `sha256Hex(ArrayBuffer ${len})`);
    if (len >= 8) {
      const view = new Uint8Array(bytes.buffer, 2, len - 4);
      const viewWant = createHash('sha256').update(view).digest('hex');
      assert.equal(await sha256Hex(view), viewWant, `sha256Hex(subview ${len})`);
    }
  }
});

test('platform/hash.js keeps the T115 fallback for a runtime without crypto.subtle', async () => {
  const original = globalThis.crypto;
  try {
    // A Worker in the opaque run origin has no crypto.subtle. Deleting it from a
    // stand-in crypto object is exactly what the fallback must survive.
    Object.defineProperty(globalThis, 'crypto', { value: {}, configurable: true });
    const bytes = sample(1000);
    const want = createHash('sha256').update(bytes).digest('hex');
    assert.equal(await sha256Hex(bytes), want);
  } finally {
    Object.defineProperty(globalThis, 'crypto', { value: original, configurable: true });
  }
});

test('platform/hash.js is platform only: no workload import, no DOM, no fetch', async () => {
  const fs = await import('node:fs');
  const path = await import('node:path');
  const { fileURLToPath } = await import('node:url');
  const here = path.dirname(fileURLToPath(import.meta.url));
  const source = fs.readFileSync(path.join(here, '..', 'js', 'platform', 'hash.js'), 'utf8');
  assert.doesNotMatch(source, /^\s*import\b/m, 'platform/hash.js imports nothing');
  assert.doesNotMatch(source, /\bfetch\s*\(/, 'the platform hash never fetches');
  assert.doesNotMatch(source, /\bdocument\b/, 'the platform hash never touches the DOM');
});

test('platform/hash.js rejects a non-byte input instead of hashing an encoding', async () => {
  assert.throws(() => sha256Pure('{"n":160}'), /sha256_input_must_be_bytes/);
  await assert.rejects(sha256Hex('{"n":160}'), /sha256_input_must_be_bytes/);
  assert.equal(toBytes(new Uint8Array([1, 2]).buffer).length, 2);
});
