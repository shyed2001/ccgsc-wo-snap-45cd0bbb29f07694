// R10-FIX (chatgpt MAJOR): GPU-required work must never be computed on the CPU.
// The kernel's setupGPU() returned early when the platform had no navigator.gpu,
// so `requireGPU: true` produced a correct CPU result labelled GPU work.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { run, reset } from '../matmul_kernel.js';
import { sha256Hex } from '../../../pages/js/platform/hash.js';

const REFERENCE_160 = '1a5a6c39c56eada3b4ca01eaed913972ed5e48d1318884748e1abf3646abc2d2';

test('GPU-required work never falls back to the CPU when no GPU API exists', async () => {
  reset();
  for (const gpu of [null, undefined, {}, { requestAdapter: null }]) {
    await assert.rejects(
      run({ kind: 'matmul-f32-gpu', n: 160 }, { gpu, requireGPU: true, now: null }),
      /gpu_unavailable/,
      `gpu=${JSON.stringify(gpu)} produced a CPU fallback for GPU-required work`
    );
  }
});

test('GPU-required work never falls back when every adapter fails', async () => {
  reset();
  const noAdapters = { async requestAdapter() { return null; } };
  await assert.rejects(
    run({ kind: 'matmul-f32-gpu', n: 160 }, { gpu: noAdapters, requireGPU: true, now: null }),
    /gpu_unavailable/
  );
});

test('without requireGPU the CPU fallback keeps its published reference hash', async () => {
  reset();
  const result = await run({ kind: 'matmul-f32-gpu', n: 160 }, { gpu: null, now: null });
  assert.equal(result.meta.gpu.adapterDetected, false);
  assert.equal(await sha256Hex(result.output.bytes), REFERENCE_160,
    'the optional-GPU path still computes the reference matrix on the CPU');
});

test('the kernel is a workload-layer file: it imports nothing', () => {
  const file = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'matmul_kernel.js');
  const source = fs.readFileSync(file, 'utf8');
  assert.doesNotMatch(source, /^\s*import\b/m, 'CR-53: the kernel must not import platform code');
  assert.doesNotMatch(source, /\brequire\s*\(/, 'the kernel must not require modules');
});
