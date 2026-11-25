// W10c D-W10-8: the synthetic two-file workload's own test (TEST layer only).
// What/why: the workload tree owns its kernel tests. This suite keeps the
// two-file ABI honest - the entry reads `ctx.files`, the table is what the
// manifest pins, and the output is deterministic.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { fileURLToPath } from 'node:url';

import { run } from '../entry.js';
import { sha256Hex } from '../../../pages/js/platform/hash.js';

const here = path.dirname(fileURLToPath(import.meta.url));
const dir = path.join(here, '..');
const manifest = JSON.parse(fs.readFileSync(path.join(dir, 'manifest.json'), 'utf8'));
const table = new Uint8Array(fs.readFileSync(path.join(dir, 'table.bin')));
const sha = (buf) => crypto.createHash('sha256').update(buf).digest('hex');

test('the entry module imports nothing, fetches nothing and hashes nothing', () => {
  const source = fs.readFileSync(path.join(dir, manifest.entry), 'utf8');
  assert.doesNotMatch(source, /^\s*import\b/m, 'a workload imports nothing');
  assert.doesNotMatch(source, /\bfetch\s*\(/, 'a workload never fetches');
  assert.doesNotMatch(source, /crypto\.subtle|\bsha256/, 'the platform hashes, not the workload');
  assert.doesNotMatch(source, /\bdocument\b/, 'a workload never touches the DOM');
});

test('the manifest pins exactly the two files it ships', () => {
  assert.equal(manifest.layer, 'TEST');
  assert.equal(manifest.entry, 'entry.js');
  assert.deepEqual(manifest.files.map((f) => f.path), ['entry.js', 'table.bin']);
  assert.equal(manifest.sha256, sha(fs.readFileSync(path.join(dir, 'entry.js'))));
  assert.equal(manifest.files[1].sha256, sha(Buffer.from(table)));
});

test('the workload scales each cell by its companion table', async () => {
  const { output } = await run({ kind: 'test-multifile', n: 4 }, { files: { 'table.bin': table } });
  assert.equal(output.n, 4);
  assert.equal(output.bytes.length, 64);
  const values = new Float32Array(output.bytes.buffer, output.bytes.byteOffset, 16);
  for (let i = 0; i < 16; i++) assert.equal(values[i], i, `cell ${i}`);
  // The same input and table always produce the same bytes (no clock, no RNG).
  const again = await run({ kind: 'test-multifile', n: 4 }, { files: { 'table.bin': table } });
  assert.equal(await sha256Hex(again.output.bytes), await sha256Hex(output.bytes));
});

test('every input outside the manifest is rejected', async () => {
  const ctx = { files: { 'table.bin': table } };
  await assert.rejects(run({ kind: 'other', n: 4 }, ctx), /invalid_multifile_kind/);
  await assert.rejects(run({ kind: 'test-multifile', n: 0 }, ctx), /invalid_multifile_size/);
  await assert.rejects(run({ kind: 'test-multifile', n: 65 }, ctx), /invalid_multifile_size/);
  await assert.rejects(run({ kind: 'test-multifile', n: 1.5 }, ctx), /invalid_multifile_size/);
  await assert.rejects(run(null, ctx), /invalid_multifile_kind/);
});

test('a missing, truncated or inconsistent table fails the task', async () => {
  await assert.rejects(run({ kind: 'test-multifile', n: 4 }, {}), /test_multifile_missing_table/);
  await assert.rejects(run({ kind: 'test-multifile', n: 4 }, { files: { 'table.bin': new Uint8Array([]) } }),
    /test_multifile_table_too_small/);
  await assert.rejects(run({ kind: 'test-multifile', n: 4 }, { files: { 'table.bin': new Uint8Array(12) } }),
    /test_multifile_table_invalid/);
  const zeroColumns = new Uint8Array(4);
  await assert.rejects(run({ kind: 'test-multifile', n: 4 }, { files: { 'table.bin': zeroColumns } }),
    /test_multifile_table_invalid/);
});

test('the workload is NOT embedded in the platform (CR-53)', () => {
  const embed = fs.readFileSync(path.join(here, '..', '..', '..', 'embed.go'), 'utf8');
  assert.doesNotMatch(embed, /test_multifile/, 'the test workload is never shipped in the server embed');
  const pages = path.join(here, '..', '..', '..', 'pages');
  for (const file of ['js/runner.js', 'js/contribute.js', 'js/app.js']) {
    const source = fs.readFileSync(path.join(pages, file), 'utf8');
    assert.doesNotMatch(source, /test_multifile/, `${file} must not know this workload`);
  }
});
