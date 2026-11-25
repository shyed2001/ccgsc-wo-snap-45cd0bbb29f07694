// W10 design section 6: runner_generic.test.mjs (+ W10b acceptance gates G2/G3/G4)
// What/why: the runner is generic - the SAME path (envelope verify, digest check,
// input binding, module Blob Worker) runs a dummy echo workload and the matmul
// reference workload; digest mismatches and input substitution are rejected before
// any worker starts; workload-controlled meta never reaches the platform result;
// input/output caps are enforced; the raw output hash and the settlement JSON hash
// stay two separate domains. Absorbs the handleJob end-to-end and the contributor
// envelope/receipt/input-bytes/metrics cases of the moved matmul_MVPW.test.mjs
// (WO_SPLIT_W10_RESUME step 1/3: no test deleted, only moved).
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { Worker as NodeWorker } from 'node:worker_threads';
import { fileURLToPath } from 'node:url';

import {
  handleJob,
  buildModuleWorkerSource,
  createKernelBlobUrl,
  WORKER_SRC
} from '../js/runner.js';
import { sha256Hex } from '../js/platform/hash.js';
import {
  ContributeApp,
  base64ToBytes,
  createTaskEnvelope,
  signTaskEnvelope,
  DEMO_OPERATOR_KEY
} from '../js/contribute.js';
// Test fixtures come from their separate workload tree; platform imports none.
const demoManifest = JSON.parse(fs.readFileSync(new URL('../../workloads/matmul/manifest.json', import.meta.url), 'utf8'));
const DEMO_WORKLOAD_KIND = demoManifest.kind;
const DEMO_WORKLOAD_SOURCE = fs.readFileSync(new URL('../../workloads/matmul/matmul_kernel.js', import.meta.url), 'utf8');
const DEMO_WORKLOAD_SHA256 = demoManifest.sha256;

const here = path.dirname(fileURLToPath(import.meta.url));
const pagesDir = path.join(here, '..');

const REFERENCE = {
  160: '1a5a6c39c56eada3b4ca01eaed913972ed5e48d1318884748e1abf3646abc2d2',
  240: '049db7c32a56cc68f03c9bff543f27f7733d1510104782ccc4510e5754cb320c'
};

// ---------------------------------------------------------------- Job Builders

async function demoPrivateKey() {
  return globalThis.crypto.subtle.importKey(
    'pkcs8',
    base64ToBytes(DEMO_OPERATOR_KEY.pkcs8B64),
    { name: 'Ed25519' },
    true,
    ['sign']
  );
}

async function signWithDemoKey(job) {
  const signed = await signTaskEnvelope(job.envelope, await demoPrivateKey());
  return { ...job, envSig: signed.envSig };
}

async function kernelJob(overrides = {}) {
  const workloadBuf = new TextEncoder().encode(DEMO_WORKLOAD_SOURCE).buffer;
  const workloadSha = await sha256Hex(workloadBuf);
  const inputJSON = overrides.inputJSON !== undefined
    ? overrides.inputJSON
    : JSON.stringify({ json: { kind: DEMO_WORKLOAD_KIND, n: overrides.n || 160 } });
  const inputSha = await sha256Hex(new TextEncoder().encode(inputJSON));
  const envelope = createTaskEnvelope(
    overrides.jobId || 'job-generic',
    overrides.taskId || 'task-generic',
    overrides.artifactSha256 !== undefined ? overrides.artifactSha256 : workloadSha,
    3600,
    DEMO_OPERATOR_KEY.keyId,
    inputSha
  );
  if (overrides.envelopeFields) Object.assign(envelope, overrides.envelopeFields);
  const job = {
    type: 'job',
    jobId: envelope.jobId,
    taskId: envelope.taskId,
    wasm: workloadBuf,
    wasmSha256: overrides.declaredSha256 !== undefined ? overrides.declaredSha256 : workloadSha,
    envelope: envelope,
    workersRequested: 1,
    inputJSON: inputJSON,
    timeoutMs: overrides.timeoutMs !== undefined ? overrides.timeoutMs : 30000
  };
  return signWithDemoKey(job);
}

// A fresh operator key ring for workloads outside the demo key scope.
async function freshKeyRing() {
  const kp = await globalThis.crypto.subtle.generateKey({ name: 'Ed25519' }, true, ['sign', 'verify']);
  const pubRaw = new Uint8Array(await globalThis.crypto.subtle.exportKey('raw', kp.publicKey));
  const keyId = (await sha256Hex(pubRaw)).slice(0, 16);
  return {
    keyId: keyId,
    ring: { [keyId]: { keyId: keyId, publicKeyHex: Buffer.from(pubRaw).toString('hex'), scope: 'operator' } },
    async sign(job) {
      const signed = await signTaskEnvelope(job.envelope, kp.privateKey);
      return { ...job, envSig: signed.envSig };
    }
  };
}

async function bytesJob(workloadSource, input, ring, overrides = {}) {
  const workloadBuf = new TextEncoder().encode(workloadSource).buffer;
  const workloadSha = await sha256Hex(workloadBuf);
  const inputJSON = JSON.stringify({ json: input });
  const envelope = createTaskEnvelope(
    overrides.jobId || 'job-echo',
    overrides.taskId || 'task-echo',
    workloadSha,
    3600,
    ring.keyId,
    await sha256Hex(new TextEncoder().encode(inputJSON))
  );
  if (overrides.envelopeFields) Object.assign(envelope, overrides.envelopeFields);
  const job = {
    type: 'job',
    jobId: envelope.jobId,
    taskId: envelope.taskId,
    wasm: workloadBuf,
    wasmSha256: workloadSha,
    envelope: envelope,
    workersRequested: 1,
    inputJSON: inputJSON,
    timeoutMs: overrides.timeoutMs || 30000
  };
  return ring.sign(job);
}

// ---------------------------------------------------------------- Worker Doubles

function mockPort() {
  return {
    messages: [],
    postMessage(msg) { this.messages.push(msg); }
  };
}

// Drive the real generic shell inside a Function sandbox; the workload import
// resolves through a data: URL (Node), exactly the module ABI the browser uses.
function moduleDataUrl(source) {
  return 'data:text/javascript;base64,' + Buffer.from(source, 'utf8').toString('base64');
}

// Drive the real generic shell through the SAME blob Worker shim the end-to-end
// tests use, so both entities are real realms in both isolation modes.
async function driveShell(workloadUrl, options = {}) {
  installBlobWorkerShim();
  const kernelUrl = options.mode === 'separate+lock' || options.mode === undefined
    ? createKernelBlobUrl(workloadUrl)
    : null;
  const shellSource = buildModuleWorkerSource(workloadUrl, { mode: options.mode, kernelUrl });
  const posted = [];
  const url = URL.createObjectURL(new Blob([shellSource], { type: 'text/javascript' }));
  const worker = new globalThis.Worker(url, { type: 'module' });
  worker.onmessage = (m) => posted.push(m.data);
  let error = null;
  worker.onerror = (e) => { error = e; };
  return {
    posted,
    get error() { return error; },
    send(data) { worker.postMessage(data); },
    async settle(ms = 300) {
      await new Promise((resolve) => setTimeout(resolve, ms));
      return posted;
    },
    async close() { await restoreBlobWorkerShim(); }
  };
}

// Readiness is message-driven. A fixed startup delay races module imports under
// concurrent full-gate load, especially with the lock-only shell.
async function awaitShellMessage(shell, type, deadlineMs = 3000) {
  const deadline = Date.now() + deadlineMs;
  for (;;) {
    const found = shell.posted.find((message) => message.type === type);
    if (found || shell.error || Date.now() >= deadline) return found;
    await shell.settle(Math.min(25, Math.max(1, deadline - Date.now())));
  }
}

// The blob Worker shim: the runner creates blob: URLs; Node has none, so the shim
// maps each blob to a Node worker thread over the same message protocol.
//
// D-W10-6: the default mode runs the workload in a SECOND, empty worker while the
// shell keeps the port and the digest. The shim therefore builds TWO real realms:
// the shell thread gets a nested `Worker` constructor that starts the kernel
// script in its own thread. Neither realm shares memory with the other or with
// the harness, so a forged postMessage or hash in the kernel cannot reach the
// shell, exactly as in a browser.
const blobs = new Map();
let spawnedWorkers = [];
const originalCreate = URL.createObjectURL;
const originalRevoke = URL.revokeObjectURL;
const originalWorker = globalThis.Worker;

const WORKER_PRELUDE = "const {parentPort}=require('node:worker_threads');" +
  'globalThis.self=globalThis;globalThis.onmessage=null;' +
  'globalThis.postMessage=(x)=>parentPort.postMessage(x);\n';
const WORKER_TAIL = "\nparentPort.on('message',(data)=>onmessage({data}));";

const dataUrl = (text) => 'data:text/javascript;base64,' + Buffer.from(text, 'utf8').toString('base64');

// Resolve one pinned blob: URL to a data: URL of the SAME bytes (Node cannot
// import blob:). Every URL literal in a script is rewritten this way before the
// thread starts, so no thread ever needs a blob registry of its own.
async function inlineBlobUrls(source) {
  let out = String(source);
  for (const match of out.match(/"blob:[^"]*"/g) || []) {
    const id = JSON.parse(match);
    const blob = blobs.get(id);
    if (!blob) continue;
    out = out.split(match).join(JSON.stringify(dataUrl(await blob.text())));
  }
  return out;
}

// The kernel sources a shell pins, resolved in the harness realm. A kernel
// script is the one that speaks kernel_* messages. The key is the data: URL the
// rewritten shell will hand to `new Worker(...)`.
async function kernelSourcesFor(rawShell) {
  const out = [];
  for (const [id, blob] of blobs) {
    if (!rawShell.includes(JSON.stringify(id))) continue;
    const text = await blob.text();
    if (!text.includes('kernel_done')) continue;
    // The kernel thread source is fully inlined: its own workload import is a
    // data: URL, so the thread never sees a blob: URL at all.
    out.push([dataUrl(text), WORKER_PRELUDE + await inlineBlobUrls(text) + WORKER_TAIL]);
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
        const prelude = WORKER_PRELUDE +
          'const __KERNELS = new Map(' + JSON.stringify(kernels) + ');' +
          'globalThis.Worker = class {' +
          'constructor(u) { this.onmessage = null; this.onerror = null;' +
          'const src = __KERNELS.get(u);' +
          'if (src === undefined) { setTimeout(() => { if (this.onerror) this.onerror(new Error("kernel_blob_missing")); }, 0); return; }' +
          'this.inner = new (require("node:worker_threads").Worker)(' +
          'src, { eval: true });' +
          'this.inner.on("message", (d) => { if (this.onmessage) this.onmessage({ data: d }); });' +
          'this.inner.on("error", (e) => { if (this.onerror) this.onerror(e); }); }' +
          'postMessage(d) { if (this.inner) this.inner.postMessage(d); }' +
          'terminate() { return this.inner ? this.inner.terminate() : Promise.resolve(); }' +
          '};';
        this.inner = new NodeWorker(prelude + shell + WORKER_TAIL, { eval: true });
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

// ---------------------------------------------------------------- Shell Tests (G4)

test('G4: the generic shell loads an echo workload module and calls run(input, ctx)', async () => {
  const ECHO = 'export async function run(input, ctx) {' +
    'const text = "echo:" + JSON.stringify(input);' +
    'return { output: { n: text.length, bytes: new TextEncoder().encode(text) }, meta: { gpu: { adapterDetected: false } } }; }';
  // D-W10-6: both isolation modes must produce the same shell contract.
  for (const mode of ['separate+lock', 'lock-only']) {
    const shell = await driveShell(moduleDataUrl(ECHO), { mode });
    try {
      shell.send({ type: 'init_workload' });
      const ready = await awaitShellMessage(shell, 'module_ready');
      assert.ok(ready, `module_ready (${mode}) ${shell.error || ''}`);
      assert.equal(ready.entry, true, `the workload keeps export function run and the shell imports it (${mode})`);
      // D-W10-6: separate+lock starts a kernel, lock-only imports the workload
      // in the one shell realm. A kernel that never came up would have answered
      // entry:false above, so entry:true proves the second realm ran it.
      if (mode === 'separate+lock') {
        assert.ok(spawnedWorkers.length >= 1, 'separate+lock runs the kernel in its own worker');
      }

      shell.send({ type: 'compute_task', input: { msg: 'hello' }, ctx: { timeoutMs: 1000 } });
      const done = await awaitShellMessage(shell, 'task_done');
      assert.ok(done, `task_done (${mode})`);
      const bytes = new Uint8Array(done.result.output.bytes);
      assert.equal(new TextDecoder().decode(bytes), 'echo:{"msg":"hello"}', `the shell passes the input through unmodified (${mode})`);
      assert.equal(await sha256Hex(bytes), await sha256Hex(new TextEncoder().encode('echo:{"msg":"hello"}')));
      assert.equal(done.shellSha256, await sha256Hex(bytes), `the shell hashes the output itself (${mode})`);
    } finally {
      await shell.close();
    }
  }
});

test('G4: the same generic path runs the matmul reference workload to the published hashes', async () => {
  for (const mode of ['separate+lock', 'lock-only']) {
    for (const n of [160, 240]) {
      const url = moduleDataUrl(DEMO_WORKLOAD_SOURCE);
      const shell = await driveShell(url, { mode });
      try {
        shell.send({ type: 'init_workload' });
        await shell.settle(mode === 'separate+lock' ? 500 : 150);
        shell.send({ type: 'compute_task', input: { kind: DEMO_WORKLOAD_KIND, n: n }, ctx: {} });
        const posted = await shell.settle(mode === 'separate+lock' ? 1500 : 600);
        const done = posted.find((m) => m.type === 'task_done');
        assert.ok(done, `task_done for ${n}^3 (${mode})`);
        assert.equal(await sha256Hex(new Uint8Array(done.result.output.bytes)), REFERENCE[n], `raw ${n}^3 matrix bytes (${mode})`);
        assert.equal(done.shellSha256, REFERENCE[n], `the shell digest is the reference digest (${mode})`);
        assert.equal(done.result.meta.gpu.adapterDetected, false);
      } finally {
        await shell.close();
      }
    }
  }
});

// ---------------------------------------------------------------- handleJob end-to-end

test('handleJob runs the real module worker end-to-end and rejects input substitution', async () => {
  installBlobWorkerShim();
  try {
    const port = mockPort();
    const job = await kernelJob({ jobId: 'mvpw-job', taskId: 'mvpw-task' });
    const result = await handleJob(job, port);
    assert.equal(result.ok, true, result.error);
    assert.equal(result.output.sha256, REFERENCE[160], 'the platform hashes the raw output bytes');
    assert.equal(result.gpu, undefined, 'CPU lane does not accept GPU claims from a workload');
    assert.equal(result.workers, 1);
    assert.equal(spawnedWorkers.length, 1, 'exactly one worker thread computed the task');

    // Changed signed input digits: the binding gate refuses before any worker.
    const tamperJob = await kernelJob({ jobId: 'mvpw-job-2', taskId: 'mvpw-task-2' });
    tamperJob.inputJSON = tamperJob.inputJSON.replace('160', '240');
    const tampered = await handleJob(tamperJob, port);
    assert.equal(tampered.ok, false);
    assert.equal(tampered.verifyPass, false);
    assert.equal(tampered.error, 'task_input_binding');
    assert.equal(spawnedWorkers.length, 1, 'no worker may start for a tampered input');
  } finally {
    await restoreBlobWorkerShim();
  }
});

test('the raw output hash and the settlement JSON hash stay two separate domains', async () => {
  installBlobWorkerShim();
  try {
    const port = mockPort();
    const result = await handleJob(await kernelJob({ jobId: 'job-domains', taskId: 'task-domains' }), port);
    assert.equal(result.ok, true);
    assert.equal(result.output.sha256, REFERENCE[160], 'output.sha256 = raw matrix bytes (workload data domain)');
    const settlement = await sha256Hex(new TextEncoder().encode(JSON.stringify(result.output)));
    assert.notEqual(settlement, result.output.sha256, 'the settlement JSON digest is a different domain');
    assert.equal(settlement, await sha256Hex(new TextEncoder().encode(JSON.stringify({ n: 160, sha256: REFERENCE[160] }))));
  } finally {
    await restoreBlobWorkerShim();
  }
});

test('handleJob rejects an artifact digest mismatch before any worker starts', async () => {
  installBlobWorkerShim();
  try {
    const port = mockPort();
    const wrong = await kernelJob({ jobId: 'job-wrong', taskId: 'task-wrong', artifactSha256: 'a'.repeat(64) });
    const resWrong = await handleJob(wrong, port);
    assert.equal(resWrong.ok, false);
    assert.equal(resWrong.verifyPass, false);
    assert.equal(resWrong.error, 'artifact_sha256_mismatch');
    assert.equal(spawnedWorkers.length, 0);

    const declared = await kernelJob({ jobId: 'job-declared', taskId: 'task-declared', declaredSha256: 'b'.repeat(64) });
    const resDeclared = await handleJob(declared, port);
    assert.equal(resDeclared.ok, false);
    assert.equal(resDeclared.verifyPass, true);
    assert.equal(resDeclared.error, 'job_wasm_sha256_mismatch');
    assert.equal(spawnedWorkers.length, 0, 'no worker for a declared-digest mismatch');
  } finally {
    await restoreBlobWorkerShim();
  }
});

// ---------------------------------------------------------------- D-W10-3 / G2

test('D-W10-3 / G2: the signed envelope workload file list is verified against the bytes', async () => {
  installBlobWorkerShim();
  try {
    const port = mockPort();
    const files = { workload: { id: 'matmul', version: 1, files: [{ path: 'matmul_kernel.js', sha256: DEMO_WORKLOAD_SHA256 }] } };

    const good = await kernelJob({ jobId: 'job-wl1', taskId: 'task-wl1', envelopeFields: files });
    const resGood = await handleJob(good, port);
    assert.equal(resGood.ok, true, resGood.error);
    assert.equal(resGood.output.sha256, REFERENCE[160]);

    const tamperJob = await kernelJob({ jobId: 'job-wl2', taskId: 'task-wl2', envelopeFields: files });
    tamperJob.envelope = {
      ...tamperJob.envelope,
      workload: { id: 'matmul', version: 1, files: [{ path: 'evil.js', sha256: DEMO_WORKLOAD_SHA256 }] }
    };
    const resTamper = await handleJob(tamperJob, port);
    assert.equal(resTamper.ok, false);
    assert.equal(resTamper.verifyPass, false);
    assert.equal(resTamper.error, 'signature_invalid', 'the file list is part of the signed bytes');

    const outsider = await kernelJob({
      jobId: 'job-wl3', taskId: 'task-wl3',
      envelopeFields: { workload: { id: 'matmul', version: 1, files: [{ path: 'other.js', sha256: 'c'.repeat(64) }] } }
    });
    const resOut = await handleJob(outsider, port);
    assert.equal(resOut.ok, false);
    assert.equal(resOut.verifyPass, false);
    assert.equal(resOut.error, 'artifact_not_in_workload_files');

    const bad = await kernelJob({ jobId: 'job-wl4', taskId: 'task-wl4', envelopeFields: { workload: { files: [] } } });
    const resBad = await handleJob(bad, port);
    assert.equal(resBad.ok, false);
    assert.equal(resBad.verifyPass, false);
    assert.equal(resBad.error, 'envelope_invalid_workload');

    assert.equal(spawnedWorkers.length, 1, 'only the valid workload envelope started a worker');
  } finally {
    await restoreBlobWorkerShim();
  }
});

// ---------------------------------------------------------------- G3: control fields, binding

test('G3: workload-controlled meta and output fields never reach the platform result', async () => {
  installBlobWorkerShim();
  try {
    const ring = await freshKeyRing();
    const SPOOF = 'export async function run(input) {' +
      'return { output: { n: input.n, bytes: new Uint8Array([1, 2, 3]), jobId: "spoof", ok: false, verifyPass: false },' +
      'meta: { ok: false, jobId: "spoof-job", taskId: "spoof-task", verifyPass: false, error: "spoof",' +
      'gpu: { adapterDetected: true, compileMs: 5, dispatchMs: NaN, evil: { deep: true } }, evil: "top" } }; }';
    const port = mockPort();
    const res = await handleJob(await bytesJob(SPOOF, { n: 3 }, ring), port, { keyRing: ring.ring });
    assert.equal(res.ok, true, res.error);
    assert.equal(res.jobId, 'job-echo', 'the platform owns identity');
    assert.equal(res.taskId, 'task-echo');
    assert.equal(res.verifyPass, true, 'the workload cannot flip verification');
    assert.equal(res.error, undefined);
    assert.equal(res.output.sha256, await sha256Hex(new Uint8Array([1, 2, 3])), 'the platform hashes the raw bytes');
    assert.deepEqual(res.output, { n: 3, sha256: await sha256Hex(new Uint8Array([1, 2, 3])) }, 'output carries only n and the platform digest');
    assert.equal(res.gpu, undefined, 'workload GPU claims are untrusted and must be dropped on CPU lanes');
    assert.deepEqual(Object.keys(res).sort(),
      ['jobId', 'ok', 'output', 'taskId', 'timings', 'type', 'verifyPass', 'workerCap', 'workers', 'workersRequested'],
      'no spoofed control field on the result');
  } finally {
    await restoreBlobWorkerShim();
  }
});

test('G3: lease identity and reordered JSON are binding failures', async () => {
  installBlobWorkerShim();
  try {
    const port = mockPort();
    const leaseJob = await kernelJob({
      jobId: 'job-lease', taskId: 'task-lease',
      envelopeFields: { leaseToken: 'lease-1', workerId: 'worker-1' }
    });
    leaseJob.leaseToken = 'lease-1';
    leaseJob.workerId = 'worker-1';
    const resLease = await handleJob(leaseJob, port);
    assert.equal(resLease.ok, true, resLease.error);

    const mismatch = await kernelJob({ jobId: 'job-lease2', taskId: 'task-lease2', envelopeFields: { leaseToken: 'lease-1' } });
    mismatch.leaseToken = 'lease-2';
    const resMis = await handleJob(mismatch, port);
    assert.equal(resMis.ok, false);
    assert.equal(resMis.verifyPass, false);
    assert.equal(resMis.error, 'task_input_binding');

    const reordered = await kernelJob({ jobId: 'job-lease3', taskId: 'task-lease3' });
    reordered.inputJSON = '{"json":{"n":160,"kind":"' + DEMO_WORKLOAD_KIND + '"}}';
    const resRe = await handleJob(reordered, port);
    assert.equal(resRe.ok, false);
    assert.equal(resRe.error, 'task_input_binding', 're-serialized JSON is not the signed input');
    assert.equal(spawnedWorkers.length, 1, 'only the bound job computed');
  } finally {
    await restoreBlobWorkerShim();
  }
});

// ---------------------------------------------------------------- Security regression: untrusted output + nested workers

test('G3: null output and a mismatched numerical dimension fail closed', async () => {
  installBlobWorkerShim();
  try {
    const ring = await freshKeyRing();
    for (const source of [
      'export function run() { return { output: null, meta: {} }; }',
      'export function run() { return { output: { n: 4, bytes: new Uint8Array([1]) } }; }'
    ]) {
      const res = await handleJob(await bytesJob(source, { n: 3 }, ring), mockPort(), { keyRing: ring.ring });
      assert.equal(res.ok, false, 'invalid output cannot succeed');
      assert.equal(res.error, 'task_failed: workload_output_invalid');
    }
  } finally {
    await restoreBlobWorkerShim();
  }
});

test('G5: imported workload cannot construct a nested Blob Worker', async () => {
  installBlobWorkerShim();
  try {
    const ring = await freshKeyRing();
    const source = 'export function run() {' +
      'const denied = typeof Worker === "undefined" && typeof SharedWorker === "undefined" && ' +
      'typeof Blob === "undefined" && typeof importScripts === "undefined" && ' +
      'typeof URL.createObjectURL === "undefined";' +
      'return { output: { n: 1, bytes: new Uint8Array([denied ? 1 : 0]) } }; }';
    const res = await handleJob(await bytesJob(source, { n: 1 }, ring), mockPort(), { keyRing: ring.ring });
    assert.equal(res.ok, true, res.error);
    assert.equal(res.output.sha256, await sha256Hex(new Uint8Array([1])),
      'nested-worker and Blob-URL capabilities are unavailable in the imported module');
    assert.equal(spawnedWorkers.length, 1, 'only the supervised worker runs');
  } finally {
    await restoreBlobWorkerShim();
  }
});

// ---------------------------------------------------------------- Caps (lifecycle)

test('input and output size caps are enforced around the compute', async () => {
  installBlobWorkerShim();
  try {
    const port = mockPort();
    const bigInput = '{"json":' + JSON.stringify({ kind: DEMO_WORKLOAD_KIND, n: 160, pad: 'x'.repeat(1024 * 1024) }) + '}';
    assert.ok(bigInput.length > 1024 * 1024, 'the oversized input is built as intended');
    const resBig = await handleJob(
      await kernelJob({ jobId: 'job-big-in', taskId: 'task-big-in', inputJSON: bigInput }), port);
    assert.equal(resBig.ok, false);
    assert.equal(resBig.verifyPass, false);
    assert.equal(resBig.error, 'task_input_too_large');
    assert.equal(spawnedWorkers.length, 0, 'an oversized input never reaches a worker');

    const ring = await freshKeyRing();
    const BIG = 'export async function run(input) {' +
      'return { output: { n: 1, bytes: new Uint8Array(' + (64 * 1024 * 1024 + 1) + ') }, meta: {} }; }';
    const resOut = await handleJob(await bytesJob(BIG, { n: 1 }, ring, { jobId: 'job-big-out', taskId: 'task-big-out' }), port, { keyRing: ring.ring });
    assert.equal(resOut.ok, false);
    assert.equal(resOut.verifyPass, true);
    assert.equal(resOut.error, 'output_too_large');
    assert.equal(spawnedWorkers.length, 1, 'the output cap hit after the real compute');
  } finally {
    await restoreBlobWorkerShim();
  }
});

// ---------------------------------------------------------------- D-W10-4: declined, not failed

test('the runner starts a module Blob Worker and declines without module support (D-W10-4)', async () => {
  const runnerJs = fs.readFileSync(path.join(pagesDir, 'js', 'runner.js'), 'utf8');
  assert.ok(runnerJs.includes("new Worker(shellUrl, { type: 'module' })"), 'module Blob Worker construction');
  assert.ok(runnerJs.includes('task_declined: module_worker_unsupported'), 'unsupported = declined');
  assert.ok(runnerJs.includes('task_declined: module_worker_load_failed'), 'a failed module load is also declined');
  const shell = buildModuleWorkerSource('blob:test-x', { mode: 'lock-only' });
  assert.ok(shell.includes('const WORKLOAD_URL = "blob:test-x"'), 'lock-only pins the workload URL');
  const separate = buildModuleWorkerSource('blob:test-x', { mode: 'separate+lock', kernelUrl: 'blob:kernel-x' });
  assert.ok(separate.includes('new Worker(KERNEL_URL, { type: \'module\' })'), 'separate+lock starts the kernel Worker');
  assert.ok(separate.includes('const KERNEL_URL = "blob:kernel-x"'), 'the kernel URL is pinned');
  assert.doesNotMatch(WORKER_SRC, /^\s*import\b/m, 'the shell template has no static import');

  const realCreate = URL.createObjectURL;
  const original = globalThis.Worker;
  URL.createObjectURL = () => 'blob:declined-test';
  globalThis.Worker = class {
    constructor() { throw new Error('module workers are not supported'); }
  };
  try {
    const port = mockPort();
    const res = await handleJob(await kernelJob({ jobId: 'job-declined', taskId: 'task-declined' }), port);
    assert.equal(res.ok, false);
    assert.equal(res.verifyPass, true, 'a declined task is not a verification failure');
    assert.equal(res.error, 'task_declined: module_worker_unsupported');
  } finally {
    globalThis.Worker = original;
    URL.createObjectURL = realCreate;
  }
});

// ---------------------------------------------------------------- Contributor cases (moved)

test('contributor retains the original envelope, receipt, input bytes and optional metrics', async () => {
  const originalFetch = globalThis.fetch;
  const app = new ContributeApp();
  app.sourceMode = 'LIVE';
  app.tabId = 'worker';
  // R11-M3: the envelope must name the digest of every artifact it authorizes;
  // a signed envelope without one is refused before anything is fetched.
  const moduleSha = await sha256Hex(new TextEncoder().encode(DEMO_WORKLOAD_SOURCE));
  const signed = {
    payload: {
      version: 1, jobId: 'job', taskId: 'task', artifactSha256: moduleSha,
      leaseToken: 'token', workerId: 'worker', keyId: 'key'
    },
    signature: Buffer.alloc(64).toString('base64')
  };
  const receipt = { receiptId: 'token' };
  const inputJSON = '{"json": {"kind":"matmul-f32-gpu","n":160}}';
  const assignment = {
    lease: { jobId: 'job', taskId: 'task' },
    task: { input: JSON.parse(inputJSON) },
    inputJSON: inputJSON,
    taskEnvelope: signed,
    leaseReceipt: receipt,
    moduleArtifactIds: ['module']
  };
  let submitted;
  globalThis.fetch = async (url, options) => {
    if (String(url).includes('/leases')) return { ok: true, json: async () => assignment };
    if (String(url).includes('/artifacts/')) {
      return { ok: true, arrayBuffer: async () => new TextEncoder().encode(DEMO_WORKLOAD_SOURCE).buffer };
    }
    submitted = JSON.parse(options.body);
    return { ok: true };
  };
  try {
    const job = await app.getLiveJob();
    // W10: the live job no longer names a workload kind - the runner reads the
    // signed input. The envelope, the receipt and the exact input bytes stay.
    assert.equal(job.kind, undefined);
    assert.equal(job.inputJSON, inputJSON);
    assert.equal(job.envelope.leaseToken, 'token');
    assert.equal(job.envelope.workerId, 'worker');
    await app.submitResult({ ok: true, output: { n: 160, sha256: 'digest' }, gpu: { adapterDetected: false } });
    assert.deepEqual(submitted.taskEnvelope, signed);
    assert.deepEqual(submitted.leaseReceipt, receipt);
    assert.deepEqual(submitted.gpu, { adapterDetected: false });
    assert.equal(submitted.sha256,
      await sha256Hex(new TextEncoder().encode(JSON.stringify(submitted.output))),
      'the settlement hash covers the result JSON (separate domain)');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('G2: a multi-artifact lease is declined until the envelope pins every file', async () => {
  const originalFetch = globalThis.fetch;
  const app = new ContributeApp();
  app.sourceMode = 'LIVE';
  app.tabId = 'srv-multi';
  app.registeredTabId = 'srv-multi';
  const calls = [];
  globalThis.fetch = async (url, options) => {
    calls.push({ url: String(url), body: options && options.body });
    if (String(url).includes('/leases')) {
      return {
        ok: true,
        json: async () => ({
          lease: { jobId: 'job-m', taskId: 'task-m' },
          task: { input: { kind: DEMO_WORKLOAD_KIND, n: 160 } },
          moduleArtifactIds: ['a'.repeat(64), 'b'.repeat(64)],
          taskEnvelope: {
            payload: {
              version: 1, jobId: 'job-m', taskId: 'task-m',
              artifactSha256: 'a'.repeat(64), inputSha256: 'b'.repeat(64),
              issuedAt: Math.floor(Date.now() / 1000) - 10,
              expiresAt: Math.floor(Date.now() / 1000) + 600,
              keyId: DEMO_OPERATOR_KEY.keyId
            },
            signature: Buffer.alloc(64, 1).toString('base64')
          }
        })
      };
    }
    return { ok: true };
  };
  try {
    const job = await app.getLiveJob();
    assert.equal(job, null, 'no job may be built from a multi-artifact lease without signed per-file digests');
    const submit = calls.find((c) => c.url.includes('/results'));
    assert.ok(submit, 'the decline is reported to the server');
    assert.equal(JSON.parse(submit.body).error, 'artifact_multi_file_requires_signed_envelope');
  } finally {
    globalThis.fetch = originalFetch;
  }
});
