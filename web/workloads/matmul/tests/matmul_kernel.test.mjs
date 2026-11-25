// W10 design section 6: matmul_kernel.test.mjs
// What/why: the reference workload must keep the published hashes
// (160^3 = 1a5a6c39…c2d2, 240^3 = 049db7c3…320c) through the new
// `run(input, ctx) -> { output, meta }` ABI. The test hashes the output with
// platform/hash.js, exactly like the runner does. This file lives with the
// workload on purpose: it is workload + platform/hash.js, nothing else.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { run, reset } from '../matmul_kernel.js';
import { sha256Hex } from '../../../pages/js/platform/hash.js';

const here = path.dirname(fileURLToPath(import.meta.url));
const workloadDir = path.join(here, '..');
const pages = path.join(here, '..', '..', '..', 'pages');

const REFERENCE = {
  160: '1a5a6c39c56eada3b4ca01eaed913972ed5e48d1318884748e1abf3646abc2d2',
  240: '049db7c32a56cc68f03c9bff543f27f7733d1510104782ccc4510e5754cb320c'
};

// The independent scalar reference of the exact small integer operands.
function scalar(n, row, col) {
  let sum = 0;
  for (let k = 0; k < n; k++) sum += ((row*n+k)%7-3) * ((k*n+col)%5-2);
  return sum;
}

for (const n of [160, 240]) {
  test(`matmul workload ${n}^3 output hashes to the published reference`, async () => {
    const result = await run({ kind: 'matmul-f32-gpu', n }, { gpu: null, now: null });
    assert.equal(result.output.n, n);
    assert.equal(result.output.bytes.length, n*n*4);
    assert.deepEqual(result.meta.gpu, { adapterDetected: false });
    assert.equal(await sha256Hex(result.output.bytes), REFERENCE[n], `sha256 of the ${n}^3 bytes`);
  });

  test(`matmul workload ${n}^3 bytes match an independent scalar reference`, async () => {
    const { output } = await run({ kind: 'matmul-f32-gpu', n }, { gpu: null, now: null });
    const values = new Float32Array(output.bytes.buffer, output.bytes.byteOffset, output.bytes.length / 4);
    for (const [row, col] of [[0, 0], [n-1, n-1], [31, 57]]) {
      assert.equal(values[row*n+col], scalar(n, row, col), `cell (${row},${col})`);
    }
  });
}

test('matmul workload rejects every size and input shape outside its manifest', async () => {
  await assert.rejects(run({ kind: 'matmul-f32-gpu', n: 159 }), /matmul_size_must_be_160_or_240/);
  await assert.rejects(run({ kind: 'matmul-f32-gpu', n: 160, a: [1] }), /unsupported_matmul_input/);
  await assert.rejects(run({ kind: 'other', n: 160 }), /invalid_matmul_kind/);
  await assert.rejects(run(null), /invalid_matmul_kind/);
});

test('matmul workload follows the GPU order strongest, weaker, default, then CPU', async () => {
  reset();
  const asked = [];
  const allFail = {
    async requestAdapter(o) {
      asked.push(o?.powerPreference ?? 'default');
      return { async requestDevice() { throw new Error('device lost'); } };
    }
  };
  const first = await run({ kind: 'matmul-f32-gpu', n: 160 }, { gpu: allFail, now: null });
  assert.deepEqual(asked, ['high-performance', 'low-power', 'default']);
  assert.equal(first.meta.gpu.adapterDetected, false);
  assert.equal(await sha256Hex(first.output.bytes), REFERENCE[160]);
});

test('matmul workload hashes the same on a mock GPU and on the CPU path', async () => {
  reset();
  const n = 160;
  const cpu = await run({ kind: 'matmul-f32-gpu', n }, { gpu: null, now: null });
  // The CPU run cached "no adapter" for this module instance; a Worker gets a
  // fresh instance, so the test starts the GPU case from a cold module too.
  reset();
  const buffers = [];
  let pipelines = 0, dispatches = [];
  const device = {
    createShaderModule({ code }) { assert.match(code, /@workgroup_size\(8, 8\)/); return {}; },
    async createComputePipelineAsync() { pipelines++; return { getBindGroupLayout() { return {}; } }; },
    createBuffer({ size }) {
      const b = { data: new ArrayBuffer(size), mapState: 'unmapped', async mapAsync() { this.mapState = 'mapped'; }, getMappedRange() { return this.data; }, unmap() { this.mapState = 'unmapped'; }, destroy() {} };
      buffers.push(b);
      return b;
    },
    createBindGroup({ entries }) { return entries; },
    createCommandEncoder() {
      let group;
      return {
        beginComputePass() { return { setPipeline() {}, setBindGroup(_, g) { group = g; }, dispatchWorkgroups(...dims) { dispatches.push(dims); new Uint8Array(group[2].resource.buffer.data).set(cpu.output.bytes); }, end() {} }; },
        copyBufferToBuffer(src, _, dst) { new Uint8Array(dst.data).set(new Uint8Array(src.data)); },
        finish() { return {}; }
      };
    },
    queue: {
      writeBuffer(b, _, data) { new Uint8Array(b.data).set(new Uint8Array(data.buffer, data.byteOffset, data.byteLength)); },
      submit() {}
    }
  };
  const gpu = { async requestAdapter() { return { async requestDevice() { return device; } }; } };
  const first = await run({ kind: 'matmul-f32-gpu', n }, { gpu, now: null });
  assert.equal(first.meta.gpu.adapterDetected, true);
  assert.equal(pipelines, 1);
  assert.deepEqual(dispatches, [[20, 20, 1]]);
  assert.equal(await sha256Hex(first.output.bytes), REFERENCE[n]);
  assert.deepEqual(first.output, cpu.output, 'the marked result is identical on both paths');
});

test('matmul manifest pins the entry digest and the published references', () => {
  const manifestPath = path.join(workloadDir, 'manifest.json');
  const manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'));
  assert.equal(manifest.id, 'matmul');
  assert.equal(manifest.version, 1);
  assert.equal(manifest.kind, 'matmul-f32-gpu');
  assert.equal(manifest.entry, 'matmul_kernel.js');
  assert.match(manifest.sha256, /^[0-9a-f]{64}$/);
  assert.deepEqual(manifest.reference, REFERENCE);
  // The pin is the entry file's own digest, so the runner can verify it.
  return import('node:crypto').then(({ createHash }) => {
    const digest = createHash('sha256').update(fs.readFileSync(path.join(workloadDir, manifest.entry))).digest('hex');
    assert.equal(digest, manifest.sha256, 'manifest.sha256 must be the entry digest');
  });
});

test('the workload imports nothing and never hashes or fetches', () => {
  const source = fs.readFileSync(path.join(workloadDir, 'matmul_kernel.js'), 'utf8');
  assert.doesNotMatch(source, /^\s*import\b/m, 'the workload imports nothing');
  assert.doesNotMatch(source, /\bfetch\s*\(/, 'the workload never fetches');
  assert.doesNotMatch(source, /crypto\.subtle|\bsha256/, 'the workload never hashes (the platform does)');
  assert.doesNotMatch(source, /\bdocument\b/, 'the workload never touches the DOM');
  assert.match(source, /export async function run\(input, ctx/, 'the ABI is run(input, ctx)');
  // The kernel is driven from the platform tree only through the generic shell.
  assert.ok(!fs.existsSync(path.join(pages, 'js', 'matmul_MVPW.js')), 'the old platform workload file is gone');
});
