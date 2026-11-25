// W10d D-W10-10: matmul_wasm, the example workload of the generic WASM ABI.
// What/why: this is WORKLOAD-LAYER evidence, not platform evidence. The workload
// tree owns its kernel tests: the module must be byte-exact against the frozen
// reference hashes for 160^3 and 240^3 in the SCALAR variant, the SIMD variant
// must reproduce the same bytes, the ABI must be the only surface, and the
// arithmetic must be a pure function of its input (two runs, one digest).
//
// The reference hashes below belong to the WORKLOAD (id + version + variant),
// never to the platform. The agreement between the scalar module and the
// JavaScript kernel is MEASURED and recorded; nothing here forces it.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const dir = path.join(here, '..');
const manifest = JSON.parse(fs.readFileSync(path.join(dir, 'manifest.json'), 'utf8'));
const sha = (buf) => crypto.createHash('sha256').update(buf).digest('hex');

const kernels = manifest.kernels;
const load = (kernel) => fs.readFileSync(path.join(dir, kernel.artifact));

// The input of one task, byte for byte: little-endian u32 n, A, B. The operand
// generator mirrors the JavaScript kernel's deterministic small integers so the
// two kernels are comparable at all - the values are exactly representable, so
// no FMA or rounding difference can hide in them.
function buildInput(n) {
  const cells = n * n;
  const bytes = new Uint8Array(4 + cells * 8);
  const view = new DataView(bytes.buffer);
  view.setUint32(0, n, true);
  let offset = 4;
  for (let i = 0; i < cells; i++) { view.setFloat32(offset, i % 7 - 3, true); offset += 4; }
  for (let i = 0; i < cells; i++) { view.setFloat32(offset, i % 5 - 2, true); offset += 4; }
  return bytes;
}

async function runKernel(kernel, input) {
  const { instance } = await WebAssembly.instantiate(load(kernel), {});
  const memory = instance.exports.memory;
  const ptr = instance.exports.ccgsc_alloc(input.length);
  assert.ok(ptr > 0 && ptr + input.length <= memory.buffer.byteLength, 'ccgsc_alloc answered a usable pointer');
  new Uint8Array(memory.buffer, ptr, input.length).set(input);
  const outLen = instance.exports.ccgsc_run(ptr, input.length);
  assert.ok(outLen >= 0, 'ccgsc_run answered a length, not an error code');
  const outPtr = instance.exports.ccgsc_out();
  assert.ok(outPtr > 0 && outPtr + outLen <= memory.buffer.byteLength, 'ccgsc_out answered a usable pointer');
  return new Uint8Array(memory.buffer, outPtr, outLen).slice();
}

// ---------------------------------------------------------------- Manifest

test('the manifest pins both kernel variants by their own artifact digest', () => {
  assert.equal(manifest.id, 'matmul');
  assert.equal(manifest.version, 1);
  assert.equal(manifest.kind, 'wasm-abi', 'the workload declares the byte ABI kind');
  assert.equal(manifest.runtime, 'wasm32');
  assert.equal(kernels.length, 2, 'scalar and SIMD are two kernels');
  for (const kernel of kernels) {
    const bytes = load(kernel);
    assert.equal(sha(bytes), kernel.sha256, `${kernel.id} artifact digest`);
    assert.equal(bytes.length, kernel.bytes, `${kernel.id} artifact size`);
    // D-W10-10: the identity of a reference is id + version + variant.
    assert.equal(kernel.id, `${manifest.id}@${manifest.version}:${kernel.variant}`);
  }
  const entry = kernels.find((k) => k.artifact === manifest.entry);
  assert.equal(entry.sha256, manifest.sha256, 'the entry pin is the entry kernel pin');
});

test('the module imports nothing and exports exactly the ABI', () => {
  for (const kernel of kernels) {
    const module = new WebAssembly.Module(load(kernel));
    assert.deepEqual(WebAssembly.Module.imports(module), [], `${kernel.id} imports nothing`);
    assert.deepEqual(WebAssembly.Module.exports(module).map((e) => e.name).sort(),
      ['ccgsc_alloc', 'ccgsc_out', 'ccgsc_run', 'memory'], `${kernel.id} exports exactly the ABI`);
  }
});

test('the SIMD twin declares the simd128 target feature, the scalar twin does not', () => {
  // A structural read of the target_features custom section, not a string search.
  const features = (file) => {
    const bytes = fs.readFileSync(path.join(dir, file));
    const out = new Set();
    let pos = 8;
    const uleb = () => {
      let v = 0; let shift = 0;
      for (;;) { const b = bytes[pos++]; v |= (b & 0x7f) << shift; if ((b & 0x80) === 0) return v; shift += 7; }
    };
    while (pos < bytes.length) {
      const id = bytes[pos++];
      const size = uleb();
      const end = pos + size;
      if (id === 0) {
        const nameLen = uleb();
        const name = bytes.subarray(pos, pos + nameLen).toString('utf8');
        pos += nameLen;
        if (name === 'target_features') {
          const count = uleb();
          for (let i = 0; i < count; i++) {
            const prefix = String.fromCharCode(bytes[pos++]);
            const len = uleb();
            out.add(prefix + bytes.subarray(pos, pos + len).toString('utf8'));
            pos += len;
          }
        }
      }
      pos = end;
    }
    return out;
  };
  for (const kernel of kernels) {
    const declared = features(kernel.artifact);
    if (kernel.variant === 'simd') assert.ok(declared.has('+simd128'), 'the SIMD twin declares +simd128');
    else assert.equal(declared.has('+simd128'), false, 'the scalar twin does not declare +simd128');
  }
});

// ---------------------------------------------------------------- Reference hashes

test('every kernel reproduces its recorded reference hash at 160^3 and 240^3', async () => {
  for (const kernel of kernels) {
    for (const n of [160, 240]) {
      const input = buildInput(n);
      const output = await runKernel(kernel, input);
      assert.equal(output.length, n * n * 4, `${kernel.id} ${n}^3 output bytes`);
      assert.equal(await shaOf(output), kernel.reference[String(n)], `${kernel.id} ${n}^3 reference hash`);
    }
  }
});

test('both variants emit IDENTICAL bytes (the SIMD twin is not a different answer)', async () => {
  for (const n of [160, 240]) {
    const input = buildInput(n);
    const scalar = await runKernel(kernels.find((k) => k.variant === 'scalar'), input);
    const simd = await runKernel(kernels.find((k) => k.variant === 'simd'), input);
    assert.deepEqual(Buffer.from(simd), Buffer.from(scalar), `${n}^3 scalar == simd`);
  }
});

test('the kernel is a pure function of its input: two runs, one digest', async () => {
  const kernel = kernels.find((k) => k.variant === 'scalar');
  const first = await runKernel(kernel, buildInput(160));
  const second = await runKernel(kernel, buildInput(160));
  assert.equal(await shaOf(first), await shaOf(second));
});

// ---------------------------------------------------------------- Input contract

test('an input outside the contract is an error code, never a partial answer', async () => {
  const kernel = kernels.find((k) => k.variant === 'scalar');
  const { instance } = await WebAssembly.instantiate(load(kernel), {});
  const e = instance.exports;
  const cases = [
    { label: 'empty', bytes: new Uint8Array([]), code: -1 },
    { label: 'n = 0', bytes: (() => { const b = new Uint8Array(4); return b; })(), code: -2 },
    { label: 'n out of range', bytes: (() => { const b = new Uint8Array(4); new DataView(b.buffer).setUint32(0, 241, true); return b; })(), code: -2 },
    { label: 'truncated operands', bytes: (() => {
      const b = buildInput(160).slice(0, 4 + 160 * 160 * 4);
      return b;
    })(), code: -3 }
  ];
  for (const { label, bytes, code } of cases) {
    const ptr = e.ccgsc_alloc(bytes.length) || 1024;
    new Uint8Array(e.memory.buffer, ptr, bytes.length).set(bytes);
    assert.equal(e.ccgsc_run(ptr, bytes.length), code, `${label} is error code ${code}`);
  }
});

// Node's crypto is the independent digest for the hook above.
async function shaOf(bytes) {
  return crypto.createHash('sha256').update(bytes).digest('hex');
}
