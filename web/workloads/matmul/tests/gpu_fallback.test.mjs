// Moved from web/pages/tests/gpu_fallback_MVPW.test.mjs (WO_SPLIT_W10_RESUME
// step 1: the kernel tests live with the workload). Same assertions, new ABI:
// run(input, ctx) -> { output, meta } through web/workloads/matmul/matmul_kernel.js.
// Owner 2026-11-19: GPU order strongest -> weaker -> default -> CPU.
import test from 'node:test';
import assert from 'node:assert/strict';
import { run, reset } from '../matmul_kernel.js';
import { sha256Hex } from '../../../pages/js/platform/hash.js';

const REFERENCE_160 = '1a5a6c39c56eada3b4ca01eaed913972ed5e48d1318884748e1abf3646abc2d2';

test('GPU fallback: high-performance fails, low-power used, then CPU when all fail', async () => {
  reset();
  const input = { kind: 'matmul-f32-gpu', n: 160 };
  const asked = [];
  // Every adapter fails at device creation: the kernel must try all three
  // preferences in order and then fall back to the CPU path.
  const allFail = {
    async requestAdapter(o) {
      asked.push(o?.powerPreference ?? 'default');
      return { async requestDevice() { throw new Error('device lost'); } };
    }
  };
  const cpuRun = await run(input, { gpu: allFail, now: null });
  assert.deepEqual(asked, ['high-performance', 'low-power', 'default']);
  assert.equal(cpuRun.meta.gpu.adapterDetected, false);
  assert.equal(await sha256Hex(cpuRun.output.bytes), REFERENCE_160);

  // high-performance has no adapter: low-power is the next one asked for.
  reset();
  const order = [];
  const noStrong = {
    async requestAdapter(o) {
      order.push(o?.powerPreference ?? 'default');
      return o?.powerPreference === 'high-performance'
        ? null
        : { async requestDevice() { throw new Error('stop here'); } };
    }
  };
  await run(input, { gpu: noStrong, now: null });
  assert.deepEqual(order, ['high-performance', 'low-power', 'default']);
  const again = await run(input, { gpu: noStrong, now: null });
  assert.equal(await sha256Hex(again.output.bytes), REFERENCE_160,
    'the CPU fallback hash stays the published reference');
});
