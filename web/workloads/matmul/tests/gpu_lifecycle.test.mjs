// Moved from web/pages/tests/matmul_MVPW.test.mjs (WO_SPLIT_W10_RESUME step 1:
// the kernel tests live with the workload). The GPU orchestration rows (pipeline
// cache, readback, buffer release, compile/dispatch timing) and the WGSL/workgroup
// math rows, on the new ABI run(input, ctx) -> { output, meta }.
import test from 'node:test';
import assert from 'node:assert/strict';
import { run, reset } from '../matmul_kernel.js';
import { sha256Hex } from '../../../pages/js/platform/hash.js';

const REFERENCE_160 = '1a5a6c39c56eada3b4ca01eaed913972ed5e48d1318884748e1abf3646abc2d2';
const REFERENCE_240 = '049db7c32a56cc68f03c9bff543f27f7733d1510104782ccc4510e5754cb320c';

// The exact small integer operands, computed independently of the kernel.
function cpuBytes(n) {
  const a = new Float32Array(n * n), b = new Float32Array(n * n);
  for (let i = 0; i < n * n; i++) { a[i] = i % 7 - 3; b[i] = i % 5 - 2; }
  const c = new Float32Array(n * n);
  for (let row = 0; row < n; row++) {
    for (let col = 0; col < n; col++) {
      let sum = 0;
      for (let k = 0; k < n; k++) sum = Math.fround(sum + Math.fround(a[row * n + k] * b[k * n + col]));
      c[row * n + col] = sum;
    }
  }
  const out = new Uint8Array(c.length * 4), view = new DataView(out.buffer);
  for (let i = 0; i < c.length; i++) view.setFloat32(i * 4, c[i], true);
  return out;
}

// A WebGPU device double with counters, mirroring the old MVPW mock.
function mockDevice(expected, counters) {
  return {
    createShaderModule({ code }) { counters.wgsl = code; return {}; },
    async createComputePipelineAsync() { counters.pipelines++; return { getBindGroupLayout() { return {}; } }; },
    createBuffer({ size }) {
      const b = {
        data: new ArrayBuffer(size),
        mapState: 'unmapped',
        async mapAsync() { this.mapState = 'mapped'; },
        getMappedRange() { return this.data; },
        unmap() { this.mapState = 'unmapped'; },
        destroy() { counters.destroyed++; }
      };
      counters.buffers.push(b);
      return b;
    },
    createBindGroup({ entries }) { return entries; },
    createCommandEncoder() {
      let group;
      return {
        beginComputePass() {
          return {
            setPipeline() {},
            setBindGroup(_, g) { group = g; },
            dispatchWorkgroups(...dims) {
              counters.dispatches.push(dims);
              new Uint8Array(group[2].resource.buffer.data).set(expected);
            },
            end() {}
          };
        },
        copyBufferToBuffer(src, _, dst) { new Uint8Array(dst.data).set(new Uint8Array(src.data)); },
        finish() { return {}; }
      };
    },
    queue: {
      writeBuffer(b, _, data) { new Uint8Array(b.data).set(new Uint8Array(data.buffer, data.byteOffset, data.byteLength)); },
      submit() {}
    }
  };
}

test('GPU orchestration caches pipeline, reads back and releases buffers', async () => {
  reset();
  const input = { kind: 'matmul-f32-gpu', n: 160 };
  const expected = cpuBytes(160);
  const counters = { pipelines: 0, destroyed: 0, dispatches: [], buffers: [], wgsl: null };
  const asked = [];
  const gpu = {
    async requestAdapter(options) {
      asked.push(options?.powerPreference ?? 'default');
      return { async requestDevice() { return mockDevice(expected, counters); } };
    }
  };
  let clock = 0;
  const first = await run(input, { gpu, now: () => ++clock });
  const second = await run(input, { gpu, now: () => ++clock });

  assert.deepEqual(asked, ['high-performance', 'low-power', 'default']);
  assert.equal(counters.pipelines, 1, 'the pipeline is compiled once and cached for the worker lifetime');
  assert.deepEqual(counters.dispatches, [[20, 20, 1], [20, 20, 1]]);
  assert.equal(counters.destroyed, 10, 'every buffer of both dispatches is destroyed');
  assert.ok(counters.buffers.every((b) => b.mapState === 'unmapped'), 'readback buffers are unmapped');
  assert.equal(first.meta.gpu.adapterDetected, true);
  assert.equal(first.meta.gpu.compileMs, 1, 'only the first task reports the compile time');
  assert.equal(first.meta.gpu.dispatchMs, 1);
  assert.equal('compileMs' in second.meta.gpu, false, 'the cached pipeline does not re-report compile');
  assert.equal(await sha256Hex(first.output.bytes), REFERENCE_160);
  assert.deepEqual(first.output, second.output, 'both GPU dispatches produce the same bytes');

  assert.match(counters.wgsl, /@workgroup_size\(8, 8\)/);
  assert.match(counters.wgsl, /id\.x >= dims\.n \|\| id\.y >= dims\.n/);
  assert.match(counters.wgsl, /a\[id\.y \* dims\.n \+ k\]/);
  assert.match(counters.wgsl, /b\[k \* dims\.n \+ id\.x\]/);
});

test('workgroup math dispatches ceil(n/8) x ceil(n/8) x 1 for both reference sizes', async () => {
  for (const [n, dims] of [[160, [20, 20, 1]], [240, [30, 30, 1]]]) {
    reset();
    const expected = cpuBytes(n);
    const counters = { pipelines: 0, destroyed: 0, dispatches: [], buffers: [], wgsl: null };
    const gpu = {
      async requestAdapter() { return { async requestDevice() { return mockDevice(expected, counters); } }; }
    };
    const result = await run({ kind: 'matmul-f32-gpu', n: n }, { gpu, now: null });
    assert.deepEqual(counters.dispatches, [dims], `workgroups(${n}) = [${dims}]`);
    assert.equal(result.output.bytes.length, n * n * 4);
    assert.equal(await sha256Hex(result.output.bytes), n === 160 ? REFERENCE_160 : REFERENCE_240);
  }
});
