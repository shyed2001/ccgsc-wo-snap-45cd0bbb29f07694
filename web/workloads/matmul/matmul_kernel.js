// W10 workload kernel - matmul f32. Workload layer: imports nothing.
// The ABI is `run(input, ctx) -> { output, meta }` (design section 4). This file
// is uploaded as the module artifact of the job; the runner starts a Worker from
// the verified bytes and calls `run`. No platform import, no fetch, no DOM, no
// hash: the platform hashes the returned output.
//
// Version 1 uses deterministic small integer f32 operands. All products and sums
// are exactly representable, avoiding GPU FMA/rounding hash divergence. Explicit
// custom matrices are rejected rather than silently ignored.
const KIND = 'matmul-f32-gpu';

const WGSL = `
struct Dims { n: u32 }
@group(0) @binding(0) var<storage, read> a: array<f32>;
@group(0) @binding(1) var<storage, read> b: array<f32>;
@group(0) @binding(2) var<storage, read_write> c: array<f32>;
@group(0) @binding(3) var<uniform> dims: Dims;
@compute @workgroup_size(8, 8)
fn main(@builtin(global_invocation_id) id: vec3<u32>) {
  if (id.x >= dims.n || id.y >= dims.n) { return; }
  var sum: f32 = 0.0;
  for (var k: u32 = 0u; k < dims.n; k = k + 1u) {
    let product: f32 = a[id.y * dims.n + k] * b[k * dims.n + id.x];
    sum = sum + product;
  }
  c[id.y * dims.n + id.x] = sum;
}`;

function size(n) {
  if (n !== 160 && n !== 240) throw new Error('matmul_size_must_be_160_or_240');
  return n;
}

function workgroups(n) {
  size(n);
  return [Math.ceil(n / 8), Math.ceil(n / 8), 1];
}

function operands(input) {
  if (!input || input.kind !== KIND) throw new Error('invalid_matmul_kind');
  if (Object.keys(input).some(k => k !== 'kind' && k !== 'n')) throw new Error('unsupported_matmul_input');
  const n = size(input.n);
  const a = new Float32Array(n * n), b = new Float32Array(n * n);
  for (let i = 0; i < n * n; i++) { a[i] = i % 7 - 3; b[i] = i % 5 - 2; }
  return { n, a, b };
}

function cpu(input) {
  const { n, a, b } = operands(input);
  const c = new Float32Array(n * n);
  for (let row = 0; row < n; row++) {
    for (let col = 0; col < n; col++) {
      let sum = 0;
      for (let k = 0; k < n; k++) sum = Math.fround(sum + Math.fround(a[row*n+k] * b[k*n+col]));
      c[row*n+col] = sum;
    }
  }
  return c;
}

function bytes(values) {
  const out = new Uint8Array(values.length * 4), view = new DataView(out.buffer);
  for (let i = 0; i < values.length; i++) view.setFloat32(i * 4, values[i], true);
  return out;
}

// setup and compute mirror the CPU/GPU split of the former matmul runtime: the
// GPU path is used only when an adapter and a device come back, so the result
// hash is the same on both paths (the operands are exact small integers).
// setup caches the compiled pipeline for the lifetime of the Worker, so a second
// task of the same tab reuses the shader module (only the first one reports the
// compile time) - the T83 orchestration the old runtime did per instance.
let pipelinePromise;
let compileReported = false;
async function setupGPU(gpu, now, options) {
  if (!gpu?.requestAdapter) {
    if (options.requireGPU) throw new Error('gpu_unavailable');
    return { adapterDetected: false };
  }
  const seen = new Set();
  const adapters = [];
  for (const preference of ['high-performance', 'low-power', 'default']) {
    try {
      const adapter = await gpu.requestAdapter(preference === 'default' ? undefined : { powerPreference: preference });
      if (!adapter) continue;
      const info = adapter.info || {};
      const id = JSON.stringify(['vendor', 'architecture', 'device', 'description'].map(k => String(info[k] || '')));
      if (seen.has(id)) continue;
      seen.add(id);
      const discrete = /discrete|nvidia|radeon rx/i.test(Object.values(info).join(' '));
      adapters.push({ id, discrete, createDevice: () => adapter.requestDevice() });
    } catch { /* unavailable preference */ }
  }
  adapters.sort((a, b) => Number(b.discrete) - Number(a.discrete));
  for (const adapter of adapters) {
    if (options.gpuId && adapter.id !== options.gpuId) continue;
    let device;
    try {
      device = await adapter.createDevice();
      const start = now?.();
      const module = device.createShaderModule({ code: WGSL });
      const pipeline = await device.createComputePipelineAsync({ layout: 'auto', compute: { module, entryPoint: 'main' } });
      return { adapterDetected: true, device, pipeline, compileMs: start === undefined ? undefined : now() - start };
    } catch (err) {
      device?.destroy?.();
      if (options.requireGPU) throw err;
    }
  }
  if (options.requireGPU) throw new Error('gpu_unavailable');
  return { adapterDetected: false };
}

async function stateFor(gpu, now, options) {
  if (!pipelinePromise) {
    pipelinePromise = setupGPU(gpu, now, options).catch(err => { pipelinePromise = undefined; throw err; });
  }
  return pipelinePromise;
}

// reset drops the cached pipeline. A Worker owns one module instance, so the
// cache lives exactly as long as one worker does; tests that drive run() with a
// different mock device reuse this to start from a cold module.
export function reset() {
  pipelinePromise = undefined;
  compileReported = false;
}

export async function run(input, ctx = {}) {
  const { n, a, b } = operands(input);
  const now = ctx.now === undefined ? (globalThis.performance ? () => performance.now() : null) : ctx.now;
  const gpuAPI = ctx.gpu === undefined ? globalThis.navigator?.gpu : ctx.gpu;
  const state = await stateFor(gpuAPI, now, { gpuId: ctx.gpuId, requireGPU: ctx.requireGPU });
  const gpu = { adapterDetected: state.adapterDetected };
  let values;
  if (!state.adapterDetected) {
    values = cpu(input);
  } else {
    const { device, pipeline } = state;
    if (!compileReported && state.compileMs !== undefined) gpu.compileMs = state.compileMs;
    compileReported = true;
    // GPUBufferUsage: MAP_READ=1, COPY_SRC=4, COPY_DST=8, UNIFORM=64, STORAGE=128.
    const buffers = [];
    const buffer = (length, usage) => { const b = device.createBuffer({ size: length, usage }); buffers.push(b); return b; };
    let readback;
    try {
      const aBuf = buffer(a.byteLength, 128 | 8), bBuf = buffer(b.byteLength, 128 | 8);
      const cBuf = buffer(a.byteLength, 128 | 4), dims = buffer(16, 64 | 8);
      readback = buffer(a.byteLength, 1 | 8);
      device.queue.writeBuffer(aBuf, 0, a);
      device.queue.writeBuffer(bBuf, 0, b);
      device.queue.writeBuffer(dims, 0, new Uint32Array([n, 0, 0, 0]));
      const group = device.createBindGroup({ layout: pipeline.getBindGroupLayout(0), entries: [aBuf, bBuf, cBuf, dims].map((b, binding) => ({ binding, resource: { buffer: b } })) });
      const start = now?.();
      const encoder = device.createCommandEncoder();
      const pass = encoder.beginComputePass();
      pass.setPipeline(pipeline); pass.setBindGroup(0, group);
      pass.dispatchWorkgroups(...workgroups(n)); pass.end();
      encoder.copyBufferToBuffer(cBuf, 0, readback, 0, a.byteLength);
      device.queue.submit([encoder.finish()]);
      await Promise.race([readback.mapAsync(1), device.lost?.then(() => { throw Error('device_lost'); }) || new Promise(() => {})]);
      // dispatchMs is wall time through queue completion/readback, not GPU timestamp-query time.
      if (start !== undefined) gpu.dispatchMs = now() - start;
      values = new Float32Array(readback.getMappedRange().slice(0));
    } finally {
      if (readback?.mapState === 'mapped') readback.unmap();
      for (const b of buffers) b.destroy();
    }
  }
  return { output: { n, bytes: bytes(values) }, meta: { gpu } };
}
