// W10d D-W10-10: the generic wasm-ABI tests (platform layer).
// What/why: the SECOND workload kind is an ABI, not a workload. The same generic
// path must run a tiny module that reverses bytes, honour the memory cap, refuse
// a module with imports or a missing export BEFORE any compute, and keep the
// shell as the only writer of the digest. The final case proves the js-module
// kind (W10c) still behaves exactly as before.
//
// Every fixture is compiled from tests/fixtures/*.c by
// tools/build-wasm-fixtures.mjs and inlined as bytes, so the module under test
// has a readable provenance, the suite needs no C toolchain at run time, and
// nothing is hand-assembled in a test.
import test from 'node:test';
import assert from 'node:assert/strict';
import { Worker as NodeWorker } from 'node:worker_threads';

import {
  buildWasmAbiKernelSource,
  buildWasmAbiWorkerSource,
  classifyWasmAbiModule,
  normalizeWasmMaxPages,
  resolveWasmMaxPages,
  normalizeWorkloadKind,
  resolveWorkloadKind,
  validateTaskEnvelope,
  bytesOfBase64,
  handleJob,
  WASM_ABI_EXPORTS,
  WORKLOAD_KINDS,
  DEFAULT_WORKLOAD_KIND,
  DEFAULT_WASM_MAX_MEMORY_PAGES,
  MAX_WASM_BYTES
} from '../js/runner.js';
import { sha256Hex } from '../js/platform/hash.js';
import {
  createTaskEnvelope,
  signTaskEnvelope
} from '../js/contribute.js';
import {
  WASM_ABI_REVERSE,
  WASM_ABI_IMPORT,
  WASM_ABI_BIG_MEM,
  WASM_ABI_NO_ENTRY
} from './wasm_abi_fixtures.mjs';

const REVERSE = new Uint8Array(WASM_ABI_REVERSE);
const HOSTILE_IMPORT = new Uint8Array(WASM_ABI_IMPORT);
const BIG_MEM = new Uint8Array(WASM_ABI_BIG_MEM);
const NO_ENTRY = new Uint8Array(WASM_ABI_NO_ENTRY);

// ---------------------------------------------------------------- Tables

test('D-W10-10: the two platform kinds are declared, js-module stays the default', () => {
  assert.deepEqual(WORKLOAD_KINDS.slice(), ['js-module', 'wasm-abi']);
  assert.equal(DEFAULT_WORKLOAD_KIND, 'js-module');
  assert.deepEqual(WASM_ABI_EXPORTS.slice(), ['ccgsc_alloc', 'ccgsc_run', 'ccgsc_out']);
  assert.equal(normalizeWorkloadKind(undefined), 'js-module');
  assert.equal(normalizeWorkloadKind('js-module'), 'js-module');
  assert.equal(normalizeWorkloadKind(' wasm-abi '), 'wasm-abi');
  for (const junk of ['', 'wasm', 'WASM-ABI', 7, {}, []]) {
    assert.equal(normalizeWorkloadKind(junk), null, `${JSON.stringify(junk)} is not a kind`);
  }
  assert.deepEqual(resolveWorkloadKind({ envelope: { workload: { kind: 'wasm-abi' } }, workloadKind: 'js-module' }),
    { kind: 'wasm-abi', source: 'envelope' });
  assert.deepEqual(resolveWorkloadKind({ workloadKind: 'wasm-abi' }), { kind: 'wasm-abi', source: 'job' });
  assert.deepEqual(resolveWorkloadKind({}), { kind: 'js-module', source: 'default' });
});

test('D-W10-10: the memory cap is pages, bounded, and a hostile value cannot raise it', () => {
  assert.equal(DEFAULT_WASM_MAX_MEMORY_PAGES, 4096, 'default 4096 pages = 256 MiB');
  assert.equal(normalizeWasmMaxPages(undefined), 4096);
  for (const junk of [0, -5, 1.5, 'many', NaN, Infinity, null]) {
    assert.equal(normalizeWasmMaxPages(junk), 4096, `${String(junk)} answers the default`);
  }
  assert.equal(normalizeWasmMaxPages(1), 1);
  assert.equal(normalizeWasmMaxPages(1e9), 65536, 'the hard ceiling holds');
  assert.equal(resolveWasmMaxPages({ envelope: { workload: { maxMemoryPages: 64 } }, wasmMaxPages: 4096 }), 64);
  assert.equal(resolveWasmMaxPages({ wasmMaxPages: 128 }), 128);
  assert.equal(resolveWasmMaxPages({}), 4096);
});

test('D-W10-10: an unknown kind is refused by the envelope schema, never coerced', () => {
  const base = {
    version: 1, jobId: 'j', taskId: 't', artifactSha256: 'a'.repeat(64), inputSha256: 'b'.repeat(64),
    issuedAt: 1, expiresAt: 2, keyId: 'k', workload: { id: 'w', version: 1, files: [{ path: 'x.wasm', sha256: 'c'.repeat(64) }] }
  };
  assert.equal(validateTaskEnvelope(base).valid, true, 'a kindless descriptor stays valid');
  assert.equal(validateTaskEnvelope({ ...base, workload: { ...base.workload, kind: 'wasm-abi' } }).valid, true);
  assert.equal(validateTaskEnvelope({ ...base, workload: { ...base.workload, kind: 'js-module' } }).valid, true);
  const bad = validateTaskEnvelope({ ...base, workload: { ...base.workload, kind: 'elf' } });
  assert.equal(bad.valid, false);
  assert.equal(bad.error, 'envelope_invalid_workload');
});

// ---------------------------------------------------------------- Classification

test('D-W10-10: the classifier requires memory + the three exports and no imports', () => {
  assert.deepEqual(classifyWasmAbiModule(new WebAssembly.Module(REVERSE), 4096), { valid: true, error: null });

  const reverseExports = WebAssembly.Module.exports(new WebAssembly.Module(REVERSE)).map((e) => e.name).sort();
  assert.deepEqual(reverseExports, ['ccgsc_alloc', 'ccgsc_out', 'ccgsc_run', 'memory'].sort(),
    'the positive fixture exports exactly the ABI');

  assert.equal(classifyWasmAbiModule(new WebAssembly.Module(NO_ENTRY), 4096).error, 'wasm_abi_missing_export');

  const imports = WebAssembly.Module.imports(new WebAssembly.Module(HOSTILE_IMPORT));
  assert.equal(imports.length, 1, 'the hostile fixture really declares one import');
  assert.equal(classifyWasmAbiModule(new WebAssembly.Module(HOSTILE_IMPORT), 4096).error,
    'wasm_abi_import_not_allowed');
});

test('D-W10-10: a malformed memory cap is refused, not silently defaulted', () => {
  const good = new WebAssembly.Module(REVERSE);
  assert.equal(classifyWasmAbiModule(good, 0).error, 'wasm_abi_invalid_memory_cap');
  assert.equal(classifyWasmAbiModule(good, -1).error, 'wasm_abi_invalid_memory_cap');
});

// ---------------------------------------------------------------- Live shells

const PRELUDE = "const {parentPort}=require('node:worker_threads');" +
  'globalThis.self=globalThis;globalThis.onmessage=null;' +
  'globalThis.postMessage=(x)=>parentPort.postMessage(x);\n';
const TAIL = "\nparentPort.on('message',(data)=>onmessage({data}));";

// Drive the real wasm-abi shell with a real nested kernel thread: both realms
// are real Workers, exactly as in the browser (D-W10-6).
async function runWasmAbi(wasmBytes, inputBytes, options = {}) {
  const kernelSrc = buildWasmAbiKernelSource(options);
  const shellSrc = buildWasmAbiWorkerSource({ kernelUrl: 'blob:kernel' });
  const prelude = PRELUDE +
    'const __KERNEL = ' + JSON.stringify(PRELUDE + kernelSrc + TAIL) + ';' +
    'globalThis.Worker = class { constructor(u) { this.onmessage = null; this.onerror = null;' +
    'this.inner = new (require("node:worker_threads").Worker)(__KERNEL, { eval: true });' +
    'this.inner.on("message", (d) => { if (this.onmessage) this.onmessage({ data: d }); });' +
    'this.inner.on("error", (e) => { if (this.onerror) this.onerror(e); }); }' +
    'postMessage(d) { if (this.inner) this.inner.postMessage(d); } terminate() {} };';
  const thread = new NodeWorker(prelude + shellSrc.replace('"blob:kernel"', '"k"') + TAIL, { eval: true });
  const posted = [];
  const done = new Promise((resolve) => {
    thread.on('message', (d) => {
      posted.push(d);
      if (d.type === 'module_ready') {
        if (d.entry === false) { resolve(); return; }
        thread.postMessage({ type: 'compute_task', inputBytes });
      }
      if (d.type === 'task_done' || d.type === 'task_error') resolve();
    });
    thread.on('error', () => resolve());
    setTimeout(resolve, 5000);
  });
  thread.postMessage({ type: 'init_workload', wasmBytes });
  await done;
  await thread.terminate();
  return {
    posted,
    ready: posted.find((m) => m.type === 'module_ready'),
    taskDone: posted.find((m) => m.type === 'task_done'),
    taskError: posted.find((m) => m.type === 'task_error')
  };
}

test('D-W10-10: the generic path copies the input in, reads the output out, and the SHELL hashes it', async () => {
  const input = new TextEncoder().encode('hello-wasm');
  const res = await runWasmAbi(REVERSE, input);
  assert.ok(res.ready, 'module_ready');
  assert.equal(res.ready.entry, true, 'the fixture carries the ABI');
  assert.ok(res.taskDone, res.taskError ? res.taskError.error : 'no task_done');
  const bytes = new Uint8Array(res.taskDone.result.output.bytes);
  assert.equal(new TextDecoder().decode(bytes), 'msaw-olleh', 'the module reversed the bytes');
  assert.equal(res.taskDone.shellSha256, await sha256Hex(bytes), 'the shell adopted its own digest');
  assert.equal(res.taskDone.result.output.sha256, undefined, 'the kernel claims no digest');
  // Exactly one task_done, written by the shell: the kernel cannot forge one.
  assert.equal(res.posted.filter((m) => m.type === 'task_done').length, 1);
});

test('D-W10-10: the same input always produces the same bytes (no clock, no RNG)', async () => {
  const input = new TextEncoder().encode('determinism');
  const first = await runWasmAbi(REVERSE, input);
  const second = await runWasmAbi(REVERSE, input);
  assert.ok(first.taskDone && second.taskDone);
  assert.equal(first.taskDone.shellSha256, second.taskDone.shellSha256);
});

test('D-W10-10: a module without the ABI is declined at init, never computed', async () => {
  for (const [label, bytes] of [['no entry', NO_ENTRY], ['hostile import', HOSTILE_IMPORT]]) {
    const res = await runWasmAbi(bytes, new Uint8Array([1]));
    assert.ok(res.ready, `module_ready (${label})`);
    assert.equal(res.ready.entry, false, `${label} is declined`);
    assert.ok(/^wasm_abi_(missing_export|import_not_allowed)$/.test(res.ready.error), res.ready.error);
    assert.equal(res.taskDone, undefined, `nothing ran (${label})`);
    assert.equal(res.taskError, undefined, `a decline is not a compute failure (${label})`);
  }
});

test('D-W10-10: the memory cap is enforced on the REAL memory, before any run', async () => {
  // The fixture allocates 8 pages of static data. With a 1-page cap the module
  // must be refused: a cap that is parsed and then ignored would let it run.
  const under = await runWasmAbi(BIG_MEM, new Uint8Array([1]), { maxPages: 16 });
  assert.ok(under.taskDone, 'with room the same module computes');

  const over = await runWasmAbi(BIG_MEM, new Uint8Array([1]), { maxPages: 1 });
  assert.equal(over.taskDone, undefined, 'a module above the cap must not produce output');
  assert.equal(over.ready && over.ready.entry, true, 'classification is not the cap');
  assert.ok(over.taskError, 'the cap fails the task');
  assert.equal(over.taskError.error, 'wasm_abi_memory_cap');
});

test('D-W10-10: the kernel realm still denies every escape hatch', () => {
  const kernel = buildWasmAbiKernelSource({});
  for (const name of ['eval', 'Function', 'importScripts', 'Worker', 'SharedWorker', 'Blob',
    'fetch', 'XMLHttpRequest', 'WebSocket', 'EventSource']) {
    assert.ok(kernel.includes(`'${name}'`), `the wasm kernel lock denies ${name}`);
  }
  assert.ok(kernel.indexOf('lockWorkloadRealm(self)') < kernel.indexOf('new WebAssembly.Module'),
    'the lock applies before any module is compiled');
  assert.doesNotMatch(kernel, /^\s*import\b/m, 'the kernel template has no static import');
  // The shell must NOT be locked: it needs Worker to start the kernel.
  const shell = buildWasmAbiWorkerSource({ kernelUrl: 'blob:k' });
  assert.ok(shell.includes('new Worker(KERNEL_URL'), 'the shell starts the kernel');
  assert.equal(shell.includes('lockWorkloadRealm(self)'), false, 'the shell holds no untrusted code');
});

test('D-W10-10: base64 input decodes to the exact bytes, and garbage is refused', () => {
  for (const text of ['', 'a', 'ab', 'abc', 'abcd', 'hello-wasm', '\u0000\u00ff\u0080']) {
    const bytes = new TextEncoder().encode(text);
    const b64 = Buffer.from(bytes).toString('base64');
    assert.deepEqual(Array.from(bytesOfBase64(b64)), Array.from(bytes), `round trip ${JSON.stringify(text)}`);
  }
  // Interaction is normal: whitespace inside a signed JSON value is tolerated.
  assert.deepEqual(Array.from(bytesOfBase64(' aGVs\nbG8= ')), Array.from(new TextEncoder().encode('hello')));
  for (const junk of ['!!!!', 'a===b', 'ab=c']) {
    assert.throws(() => bytesOfBase64(junk), /not a base64 string/, `${junk} is not base64`);
  }
});

test('D-W10-10: the artifact cap is unchanged and re-checked for the byte kind', () => {
  assert.equal(MAX_WASM_BYTES, 32 * 1024 * 1024, 'the platform artifact cap is unchanged');
  assert.ok(REVERSE.length < MAX_WASM_BYTES);
});

test('D-W10-10: the js-module templates are untouched by the new kind', async () => {
  const { WORKER_SRC, KERNEL_SRC, buildModuleWorkerSource } = await import('../js/runner.js');
  for (const source of [WORKER_SRC, KERNEL_SRC, buildModuleWorkerSource('blob:x')]) {
    assert.doesNotMatch(source, /wasm-abi|ccgsc_alloc|ccgsc_run|ccgsc_out/,
      'the js-module templates carry no wasm-ABI code');
  }
  assert.ok(WORKER_SRC.includes('await import(WORKLOAD_URL)'), 'the js-module shell still imports the module');
});

// ---------------------------------------------------------------- handleJob end-to-end

// The job shape of a signed wasm-abi task. The input BYTES travel base64 inside
// the signed input JSON, so the binding gate covers what the module reads. A
// fresh operator key is used because the pinned demo key is scoped to the demo
// module digest (W10) and must not authorize a second workload.
async function freshKeyRing() {
  const kp = await globalThis.crypto.subtle.generateKey({ name: 'Ed25519' }, true, ['sign', 'verify']);
  const pubRaw = new Uint8Array(await globalThis.crypto.subtle.exportKey('raw', kp.publicKey));
  const keyId = (await sha256Hex(pubRaw)).slice(0, 16);
  return {
    keyId,
    ring: { [keyId]: { keyId, publicKeyHex: Buffer.from(pubRaw).toString('hex'), scope: 'operator' } },
    async sign(job) {
      const signed = await signTaskEnvelope(job.envelope, kp.privateKey);
      return { ...job, envSig: signed.envSig };
    }
  };
}

async function wasmAbiJob(overrides = {}) {
  const ring = await freshKeyRing();
  const workloadBytes = overrides.wasm || REVERSE;
  const buffer = workloadBytes.slice().buffer;
  const workloadSha = await sha256Hex(buffer);
  const inputBytes = overrides.inputBytes || new TextEncoder().encode('hello-wasm');
  const inputJSON = JSON.stringify({ json: { bytesBase64: Buffer.from(inputBytes).toString('base64') } });
  const envelope = createTaskEnvelope(
    overrides.jobId || 'job-wasm',
    overrides.taskId || 'task-wasm',
    workloadSha,
    3600,
    ring.keyId,
    await sha256Hex(new TextEncoder().encode(inputJSON))
  );
  envelope.workload = {
    id: 'wasm-abi-fixture',
    version: 1,
    kind: 'wasm-abi',
    files: [{ path: 'fixture.wasm', sha256: workloadSha }]
  };
  if (overrides.envelopeFields) Object.assign(envelope, overrides.envelopeFields);
  const job = await ring.sign({
    type: 'job',
    jobId: envelope.jobId,
    taskId: envelope.taskId,
    wasm: buffer,
    wasmSha256: workloadSha,
    envelope,
    inputJSON,
    timeoutMs: 10000
  });
  job.ring = ring.ring;
  return job;
}

function mockPort() {
  return { messages: [], postMessage(msg) { this.messages.push(msg); } };
}

test('D-W10-10 end-to-end: handleJob runs a signed wasm-abi job through the generic gate', async () => {
  installBlobWorkerShim();
  try {
    const job = await wasmAbiJob();
    const res = await handleJob(job, mockPort(), { keyRing: job.ring });
    assert.equal(res.ok, true, res.error);
    assert.equal(res.verifyPass, true);
    const expected = await sha256Hex(new TextEncoder().encode('msaw-olleh'));
    assert.equal(res.output.sha256, expected, 'the platform hashes the module output bytes');
    assert.equal(res.timings.workloadKind, 'wasm-abi', 'the resolved kind is recorded');
    assert.equal(res.workers, 1);
  } finally {
    await restoreBlobWorkerShim();
  }
});

test('D-W10-10 end-to-end: the byte-ABI input is covered by the signed binding', async () => {
  installBlobWorkerShim();
  try {
    const job = await wasmAbiJob();
    // Substituting the bytes without re-signing must fail the binding gate, so
    // the bytes the module reads can only be the bytes the envelope authorized.
    const forged = JSON.stringify({ json: { bytesBase64: Buffer.from('other').toString('base64') } });
    job.inputJSON = forged;
    const res = await handleJob(job, mockPort(), { keyRing: job.ring });
    assert.equal(res.ok, false);
    assert.equal(res.verifyPass, false);
    assert.equal(res.error, 'task_input_binding');
    assert.equal(spawnedWorkers.length, 0, 'no worker may start for a substituted input');
  } finally {
    await restoreBlobWorkerShim();
  }
});

test('D-W10-10 end-to-end: a kind outside the platform ABI is refused', async () => {
  installBlobWorkerShim();
  try {
    const job = await wasmAbiJob();
    job.envelope.workload = { ...job.envelope.workload, kind: 'elf' };
    const res = await handleJob(job, mockPort(), { keyRing: job.ring });
    assert.equal(res.ok, false);
    assert.equal(res.verifyPass, false);
    // The schema gate is earlier and stricter than the signature gate: an
    // unknown kind fails closed before the signature is even consulted.
    assert.equal(res.error, 'envelope_invalid_workload');
    assert.equal(spawnedWorkers.length, 0, 'no worker may start for a tampered envelope');
  } finally {
    await restoreBlobWorkerShim();
  }
});

test('D-W10-10 end-to-end: a hostile module never reaches a compute', async () => {
  installBlobWorkerShim();
  try {
    const job = await wasmAbiJob({ wasm: HOSTILE_IMPORT });
    const res = await handleJob(job, mockPort(), { keyRing: job.ring });
    assert.equal(res.ok, false, 'an importing module cannot produce a result');
    assert.equal(res.verifyPass, true, 'the envelope and bytes were valid; the module is declined');
    assert.match(String(res.error), /wasm_abi_import_not_allowed/);
  } finally {
    await restoreBlobWorkerShim();
  }
});

// The runner creates blob: URLs; Node has none, so map each blob to a real Node
// worker thread over the same message protocol (the W10c shim, verbatim).
const blobs = new Map();
let spawnedWorkers = [];
const originalCreate = URL.createObjectURL;
const originalRevoke = URL.revokeObjectURL;
const originalWorker = globalThis.Worker;

const SHIM_PRELUDE = "const {parentPort}=require('node:worker_threads');" +
  'globalThis.self=globalThis;globalThis.onmessage=null;' +
  'globalThis.postMessage=(x)=>parentPort.postMessage(x);\n';
const SHIM_TAIL = "\nparentPort.on('message',(data)=>onmessage({data}));";
const dataUrl = (text) => 'data:text/javascript;base64,' + Buffer.from(text, 'utf8').toString('base64');

async function inlineBlobUrls(source) {
  let out = String(source);
  for (const match of out.match(/"(blob:[^"]*)"/g) || []) {
    const id = JSON.parse(match);
    const blob = blobs.get(id);
    if (!blob) continue;
    out = out.split(match).join(JSON.stringify(dataUrl(await blob.text())));
  }
  return out;
}

// A kernel script is the one that speaks kernel_* messages.
async function kernelSourcesFor(rawShell) {
  const out = [];
  for (const [id, blob] of blobs) {
    if (!rawShell.includes(JSON.stringify(id))) continue;
    const text = await blob.text();
    if (!text.includes('kernel_done')) continue;
    out.push([dataUrl(text), SHIM_PRELUDE + await inlineBlobUrls(text) + SHIM_TAIL]);
  }
  return out;
}

function installBlobWorkerShim() {
  blobs.clear();
  spawnedWorkers = [];
  URL.createObjectURL = (blob) => {
    const id = 'blob:test-' + (blobs.size + 1);
    blobs.set(id, blob);
    return id;
  };
  URL.revokeObjectURL = () => {};
  globalThis.Worker = class BlobWorkerShim {
    constructor(url) {
      this.terminated = false;
      this.onmessage = null;
      this.onerror = null;
      this.ready = (async () => {
        const rawShell = await blobs.get(url).text();
        const kernels = await kernelSourcesFor(rawShell);
        const shell = await inlineBlobUrls(rawShell);
        const prelude = SHIM_PRELUDE +
          'const __KERNELS = new Map(' + JSON.stringify(kernels) + ');' +
          'globalThis.Worker = class {' +
          'constructor(u) { this.onmessage = null; this.onerror = null;' +
          'const src = __KERNELS.get(u);' +
          'if (src === undefined) { setTimeout(() => { if (this.onerror) this.onerror(new Error("kernel_blob_missing")); }, 0); return; }' +
          'this.inner = new (require("node:worker_threads").Worker)(src, { eval: true });' +
          'this.inner.on("message", (d) => { if (this.onmessage) this.onmessage({ data: d }); });' +
          'this.inner.on("error", (e) => { if (this.onerror) this.onerror(e); }); }' +
          'postMessage(d) { if (this.inner) this.inner.postMessage(d); }' +
          'terminate() { return this.inner ? this.inner.terminate() : Promise.resolve(); }' +
          '};';
        this.inner = new NodeWorker(prelude + shell + SHIM_TAIL, { eval: true });
        spawnedWorkers.push(this.inner);
        this.inner.on('message', (data) => { if (this.onmessage) this.onmessage({ data }); });
        this.inner.on('error', (e) => { if (this.onerror) this.onerror(e); });
      })();
    }
    postMessage(data) { this.ready.then(() => { if (this.inner) this.inner.postMessage(data); }); }
    terminate() {
      this.terminated = true;
      return this.inner ? this.inner.terminate() : Promise.resolve();
    }
  };
}

async function restoreBlobWorkerShim() {
  globalThis.Worker = originalWorker;
  URL.createObjectURL = originalCreate;
  URL.revokeObjectURL = originalRevoke;
  for (const w of spawnedWorkers) {
    try { await w.terminate(); } catch (_) { /* already gone */ }
  }
  spawnedWorkers = [];
}
