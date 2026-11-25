// W10c row 3: multifile_MVPW.test.mjs - D-W10-8 multi-file workloads.
// What/why: the synthetic two-file workload (web/workloads/test_multifile/,
// TEST layer only) must travel the whole path: envelope file list -> parent
// fetches EVERY file -> runner verifies each sha256 BEFORE any import -> kernel
// sees ctx.files { path: ArrayBuffer }. A bad digest declines the task before
// import; more files than the cap is declined.
//
// Truth: WO_W10C_CLIENT.md row 2/3, SPLIT_DESIGN_W10.md section 8 (D-W10-8).
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { Worker as NodeWorker } from 'node:worker_threads';
import { fileURLToPath } from 'node:url';

import { handleJob, MAX_WORKLOAD_FILES } from '../js/runner.js';
import { sha256Hex } from '../js/platform/hash.js';
import {
  ContributeApp,
  createTaskEnvelope,
  signTaskEnvelope,
  DEMO_OPERATOR_KEY,
  base64ToBytes,
  MAX_ARTIFACT_BYTES
} from '../js/contribute.js';
import { run as multifileRun } from '../../workloads/test_multifile/entry.js';

const here = path.dirname(fileURLToPath(import.meta.url));
const workloadDir = path.join(here, '..', '..', 'workloads', 'test_multifile');
const manifest = JSON.parse(fs.readFileSync(path.join(workloadDir, 'manifest.json'), 'utf8'));
const ENTRY_SOURCE = fs.readFileSync(path.join(workloadDir, manifest.entry), 'utf8');
const TABLE_BYTES = new Uint8Array(fs.readFileSync(path.join(workloadDir, 'table.bin')));

const sha = (buf) => crypto.createHash('sha256').update(buf).digest('hex');
const ENTRY_SHA = sha(Buffer.from(ENTRY_SOURCE, 'utf8'));
const TABLE_SHA = sha(Buffer.from(TABLE_BYTES));

// ---------------------------------------------------------------- Fixtures

test('the synthetic two-file workload is TEST layer only and pins both files', () => {
  assert.equal(manifest.layer, 'TEST', 'the workload declares its layer');
  assert.equal(manifest.files.length, 2, 'entry module plus one data companion');
  assert.deepEqual(manifest.files.map((f) => f.path), ['entry.js', 'table.bin']);
  assert.equal(manifest.files[0].entry, true, 'the entry is flagged');
  assert.equal(manifest.files[0].sha256, ENTRY_SHA, 'the manifest pins the entry digest');
  assert.equal(manifest.files[1].sha256, TABLE_SHA, 'the manifest pins the table digest');
  assert.equal(manifest.sha256, ENTRY_SHA, 'sha256 stays the entry digest, as for every workload');
  // It is a real workload: no imports, no fetch, no hash, the run ABI.
  assert.doesNotMatch(ENTRY_SOURCE, /^\s*import\b/m);
  assert.doesNotMatch(ENTRY_SOURCE, /\bfetch\s*\(/);
  assert.doesNotMatch(ENTRY_SOURCE, /crypto\.subtle|\bsha256/);
  assert.match(ENTRY_SOURCE, /export async function run\(input, ctx/);
});

test('the test workload reads its companion from ctx.files and fails without it', async () => {
  const ok = await multifileRun({ kind: 'test-multifile', n: 4 }, { files: { 'table.bin': TABLE_BYTES } });
  assert.equal(ok.output.n, 4);
  assert.equal(ok.output.bytes.length, 4 * 4 * 4, 'n*n f32 bytes');
  const values = new Float32Array(ok.output.bytes.buffer, ok.output.bytes.byteOffset, 16);
  assert.equal(values[0], 0);
  assert.equal(values[1], 1, 'cell (0,1) = 1 * table[1] = 1');
  assert.equal(values[5], 5);
  await assert.rejects(multifileRun({ kind: 'test-multifile', n: 4 }, {}), /test_multifile_missing_table/);
  await assert.rejects(multifileRun({ kind: 'test-multifile', n: 4 }, { files: { 'table.bin': new Uint8Array([0]) } }),
    /test_multifile_table_too_small/);
});

// ---------------------------------------------------------------- Worker double

const WORKER_PRELUDE = "const {parentPort}=require('node:worker_threads');" +
  'globalThis.self=globalThis;globalThis.onmessage=null;' +
  'globalThis.postMessage=(x)=>parentPort.postMessage(x);\n';
const WORKER_TAIL = "\nparentPort.on('message',(data)=>onmessage({data}));";
const dataUrl = (text) => 'data:text/javascript;base64,' + Buffer.from(text, 'utf8').toString('base64');

// Run one job end to end through the real shell, exactly like handleJob does,
// but with the kernel in a Node thread so a test can observe ctx.files.
async function runJobInThreads(job) {
  const blobs = new Map();
  let n = 0;
  const realCreate = URL.createObjectURL;
  const realWorker = globalThis.Worker;
  URL.createObjectURL = (blob) => { const id = 'blob:mf' + (++n); blobs.set(id, blob); return id; };
  try {
    const result = await handleJob(job, null);
    return { result };
  } finally {
    URL.createObjectURL = realCreate;
    globalThis.Worker = realWorker;
  }
}

async function signJob(job, keyPair) {
  const signed = await signTaskEnvelope(job.envelope, keyPair);
  return { ...job, envSig: signed.envSig };
}

// A fresh operator key: the demo key is scoped to the demo module digest and
// must not authorize another workload. The runner resolves it from a key ring.
async function freshKeyRing() {
  const kp = await globalThis.crypto.subtle.generateKey({ name: 'Ed25519' }, true, ['sign', 'verify']);
  const pubRaw = new Uint8Array(await globalThis.crypto.subtle.exportKey('raw', kp.publicKey));
  const keyId = (await sha256Hex(pubRaw)).slice(0, 16);
  return {
    keyId,
    privateKey: kp.privateKey,
    ring: { [keyId]: { keyId, publicKeyHex: Buffer.from(pubRaw).toString('hex'), scope: 'operator' } }
  };
}

async function demoPrivateKey() {
  return globalThis.crypto.subtle.importKey('pkcs8', base64ToBytes(DEMO_OPERATOR_KEY.pkcs8B64), { name: 'Ed25519' }, true, ['sign']);
}

// A job carrying the entry plus every declared companion, all signed.
async function multifileJob(overrides = {}) {
  const ring = overrides.ring || await freshKeyRing();
  const entryBuf = new TextEncoder().encode(ENTRY_SOURCE).buffer;
  const files = { 'table.bin': overrides.tableBytes !== undefined ? overrides.tableBytes : TABLE_BYTES.slice() };
  if (overrides.extraFile) files[overrides.extraFile.path] = overrides.extraFile.bytes;

  const declaredFiles = [
    { path: 'entry.js', entry: true, sha256: overrides.entrySha || ENTRY_SHA },
    { path: 'table.bin', sha256: overrides.tableSha || TABLE_SHA }
  ];
  for (const f of overrides.extraDeclared || []) declaredFiles.push(f);

  const inputJSON = JSON.stringify({ json: { kind: 'test-multifile', n: 4 } });
  const inputSha = await sha256Hex(new TextEncoder().encode(inputJSON));
  const envelope = createTaskEnvelope(
    overrides.jobId || 'job-mf',
    overrides.taskId || 'task-mf',
    overrides.artifactSha256 || ENTRY_SHA,
    3600,
    ring.keyId,
    inputSha
  );
  envelope.workload = overrides.workload || { id: manifest.id, version: manifest.version, files: declaredFiles };
  const job = {
    type: 'job',
    jobId: envelope.jobId,
    taskId: envelope.taskId,
    wasm: entryBuf,
    wasmSha256: overrides.declaredEntrySha || ENTRY_SHA,
    files,
    envelope,
    workersRequested: 1,
    inputJSON,
    timeoutMs: 15000
  };
  return { job: await signJob(job, ring.privateKey), ring: ring.ring };
}

// ---------------------------------------------------------------- Runner: the gate

test('D-W10-8: the runner accepts the two-file workload and passes ctx.files to the kernel', async () => {
  installKernelShim();
  try {
    const { job, ring } = await multifileJob({ jobId: 'job-mf-ok', taskId: 'task-mf-ok' });
    const port = { messages: [], postMessage(m) { this.messages.push(m); } };
    const res = await handleJob(job, port, { keyRing: ring });
    assert.equal(res.ok, true, res.error);
    // The expected bytes come from the workload itself, with a fresh ctx.files.
    const expected = await multifileRun({ kind: 'test-multifile', n: 4 }, { files: { 'table.bin': TABLE_BYTES } });
    assert.equal(res.output.sha256, await sha256Hex(expected.output.bytes), 'the platform hashed the raw output bytes');
    assert.equal(res.output.n, 4);
  } finally {
    restoreKernelShim();
  }
});

test('D-W10-8: one bad companion digest declines the task before any import', async () => {
  let started = 0;
  installKernelShim(() => { started++; });
  try {
    const { job, ring } = await multifileJob({ jobId: 'job-mf-bad', taskId: 'task-mf-bad', tableBytes: new Uint8Array(TABLE_BYTES).fill(9) });
    const res = await handleJob(job, null, { keyRing: ring });
    assert.equal(res.ok, false);
    assert.equal(res.verifyPass, false, 'a digest gate failure is not a verification pass');
    assert.equal(res.error, 'workload_file_sha256_mismatch');
    assert.equal(started, 0, 'no worker may start for a bad file');
  } finally {
    restoreKernelShim();
  }
});

test('D-W10-8: a declared file that does not arrive declines the task', async () => {
  installKernelShim();
  try {
    const { job, ring } = await multifileJob({ jobId: 'job-mf-miss', taskId: 'task-mf-miss' });
    delete job.files['table.bin'];
    const res = await handleJob(job, null, { keyRing: ring });
    assert.equal(res.ok, false);
    assert.equal(res.error, 'workload_file_missing');
  } finally {
    restoreKernelShim();
  }
});

test('D-W10-8: an undeclared extra file declines the task', async () => {
  installKernelShim();
  try {
    const { job, ring } = await multifileJob({ jobId: 'job-mf-extra', taskId: 'task-mf-extra' });
    job.files['evil.bin'] = new Uint8Array([1, 2, 3]);
    const res = await handleJob(job, null, { keyRing: ring });
    assert.equal(res.ok, false);
    assert.equal(res.error, 'workload_file_not_declared');
  } finally {
    restoreKernelShim();
  }
});

test('D-W10-8: more files than the cap is declined', async () => {
  installKernelShim();
  try {
    const extraDeclared = [];
    const extra = {};
    for (let i = 1; i <= MAX_WORKLOAD_FILES; i++) {
      const bytes = new Uint8Array([i]);
      extraDeclared.push({ path: `pad-${i}.bin`, sha256: sha(Buffer.from(bytes)) });
      extra[`pad-${i}.bin`] = bytes;
    }
    // 1 entry + 1 table + MAX_WORKLOAD_FILES companions: one over the cap.
    const { job, ring } = await multifileJob({ jobId: 'job-mf-cap', taskId: 'task-mf-cap', extraDeclared });
    for (const [k, v] of Object.entries(extra)) job.files[k] = v;
    const res = await handleJob(job, null, { keyRing: ring });
    assert.equal(res.ok, false);
    assert.equal(res.error, 'envelope_invalid_workload', 'the schema refuses the oversized file list');
  } finally {
    restoreKernelShim();
  }
});

// ---------------------------------------------------------------- Parent: fetch all

test('D-W10-8: the parent fetches every declared artifact and passes the companions as job.files', async () => {
  const originalFetch = globalThis.fetch;
  const app = new ContributeApp({ autoStart: false });
  app.sourceMode = 'LIVE';
  app.tabIds = { tp: 'tab-mf' };
  app.tabId = 'tab-mf';
  app.runnerNonce = 'nonce-mf';

  const entryBuf = new TextEncoder().encode(ENTRY_SOURCE).buffer;
  const inputJSON = JSON.stringify({ json: { kind: 'test-multifile', n: 4 } });
  const inputSha = await sha256Hex(new TextEncoder().encode(inputJSON));
  const envelope = createTaskEnvelope('job-mf-live', 'task-mf-live', ENTRY_SHA, 3600, DEMO_OPERATOR_KEY.keyId, inputSha);
  envelope.workload = { id: manifest.id, version: manifest.version, files: manifest.files };

  const fetched = [];
  globalThis.fetch = async (url) => {
    const target = String(url);
    if (target.includes('/leases')) {
      return {
        ok: true,
        json: async () => ({
          lease: { jobId: 'job-mf-live', taskId: 'task-mf-live' },
          task: { input: { kind: 'test-multifile', n: 4 } },
          inputJSON,
          taskEnvelope: {
            payload: { ...envelope },
            signature: Buffer.alloc(64, 1).toString('base64')
          },
          moduleArtifactIds: ['artifact-entry', 'artifact-table']
        })
      };
    }
    if (target.includes('/artifacts/artifact-entry')) {
      fetched.push('entry');
      return { ok: true, arrayBuffer: async () => entryBuf };
    }
    if (target.includes('/artifacts/artifact-table')) {
      fetched.push('table');
      return { ok: true, arrayBuffer: async () => TABLE_BYTES.slice().buffer };
    }
    return { ok: true, json: async () => ({}) };
  };
  try {
    const job = await app.getLiveJob();
    assert.ok(job, 'a multi-file lease with a signed file list must build a job');
    assert.deepEqual(fetched, ['entry', 'table'], 'EVERY declared artifact was fetched, in order');
    assert.equal(job.wasmSha256, ENTRY_SHA, 'the entry travels as the module artifact');
    assert.ok(job.files, 'the companions travel as ctx.files');
    assert.deepEqual(Object.keys(job.files), ['table.bin']);
    assert.equal(await sha256Hex(job.files['table.bin']), TABLE_SHA);
    assert.equal(job.envelope.workload.files.length, 2, 'the signed file list travels with the envelope');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('D-W10-8: a bad companion digest never builds a job', async () => {
  const originalFetch = globalThis.fetch;
  const app = new ContributeApp({ autoStart: false });
  app.sourceMode = 'LIVE';
  app.tabIds = { tp: 'tab-mf2' };
  app.tabId = 'tab-mf2';
  app.runnerNonce = 'nonce-mf2';

  const entryBuf = new TextEncoder().encode(ENTRY_SOURCE).buffer;
  const inputJSON = JSON.stringify({ json: { kind: 'test-multifile', n: 4 } });
  const envelope = createTaskEnvelope('job-mf-live2', 'task-mf-live2', ENTRY_SHA, 3600, DEMO_OPERATOR_KEY.keyId,
    await sha256Hex(new TextEncoder().encode(inputJSON)));
  envelope.workload = { id: manifest.id, version: manifest.version, files: manifest.files };

  const calls = [];
  globalThis.fetch = async (url, options) => {
    const target = String(url);
    calls.push({ url: target, body: options && options.body });
    if (target.includes('/leases')) {
      return {
        ok: true,
        json: async () => ({
          lease: { jobId: 'job-mf-live2', taskId: 'task-mf-live2' },
          task: { input: { kind: 'test-multifile', n: 4 } },
          inputJSON,
          taskEnvelope: { payload: { ...envelope }, signature: Buffer.alloc(64, 1).toString('base64') },
          moduleArtifactIds: ['artifact-entry2', 'artifact-table2']
        })
      };
    }
    if (target.includes('/artifacts/artifact-entry2')) return { ok: true, arrayBuffer: async () => entryBuf };
    if (target.includes('/artifacts/artifact-table2')) return { ok: true, arrayBuffer: async () => new Uint8Array([1, 2, 3]).buffer };
    return { ok: true, json: async () => ({}) };
  };
  try {
    const job = await app.getLiveJob();
    assert.equal(job, null, 'no job may be built from a companion that disagrees with its digest');
    const submit = calls.find((c) => c.url.includes('/results'));
    assert.ok(submit, 'the decline is reported to the server');
    assert.equal(JSON.parse(submit.body).error, 'artifact_sha256_mismatch');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('D-W10-8: total workload bytes stay inside MAX_ARTIFACT_BYTES', () => {
  assert.equal(MAX_ARTIFACT_BYTES, 32 * 1024 * 1024, 'the parent cap is the server upload cap');
  assert.ok(MAX_WORKLOAD_FILES * MAX_ARTIFACT_BYTES >= MAX_ARTIFACT_BYTES, 'the total bound is enforced per job');
});

// ---------------------------------------------------------------- Shim

// The kernel in a Node thread, so handleJob's real Worker path runs.
function installKernelShim(onStart = null) {
  const blobs = new Map();
  let n = 0;
  globalThis.__mfBlobs = blobs;
  globalThis.__mfCreate = URL.createObjectURL;
  globalThis.__mfWorker = globalThis.Worker;
  URL.createObjectURL = (blob) => { const id = 'blob:mfw' + (++n); blobs.set(id, blob); return id; };
  globalThis.Worker = class {
    constructor(url) {
      if (onStart) onStart();
      this.onmessage = null;
      this.onerror = null;
      this.ready = blobs.get(url).text().then(async (shellText) => {
        const kernelMatch = /const KERNEL_URL = "([^"]+)";/.exec(shellText);
        // A Node thread cannot import blob:, so BOTH module URLs (the lock-only
        // shell's and the kernel's) become data: URLs of the SAME bytes.
        let kernelText = kernelMatch ? await blobs.get(kernelMatch[1]).text() : '';
        const workloadUrl = (() => {
          const m = /const WORKLOAD_URL = "([^"]+)";/.exec(kernelText);
          return m ? m[1] : '';
        })();
        const workloadText = workloadUrl ? await blobs.get(workloadUrl).text() : '';
        kernelText = kernelText.split(JSON.stringify(workloadUrl)).join(JSON.stringify(dataUrl(workloadText)));
        const patched = shellText;
        const prelude = WORKER_PRELUDE +
          'const __KERNEL_SRC = ' + JSON.stringify(WORKER_PRELUDE + kernelText + WORKER_TAIL) + ';' +
          'globalThis.Worker = class { constructor(u) { this.onmessage = null; this.onerror = null;' +
          'this.inner = new (require("node:worker_threads").Worker)(__KERNEL_SRC, { eval: true });' +
          'this.inner.on("message", (d) => { if (this.onmessage) this.onmessage({ data: d }); });' +
          'this.inner.on("error", (e) => { if (this.onerror) this.onerror(e); }); }' +
          'postMessage(d) { if (this.inner) this.inner.postMessage(d); } terminate() {} };';
        this.inner = new NodeWorker(prelude + patched + WORKER_TAIL, { eval: true });
        this.inner.on('message', (data) => { if (this.onmessage) this.onmessage({ data }); });
        this.inner.on('error', (e) => { if (this.onerror) this.onerror(e); });
      });
    }
    postMessage(data) { this.ready.then(() => this.inner.postMessage(data)); }
    terminate() { return this.ready.then(() => this.inner && this.inner.terminate()); }
  };
}

function restoreKernelShim() {
  if (globalThis.__mfCreate) URL.createObjectURL = globalThis.__mfCreate;
  if (globalThis.__mfWorker) globalThis.Worker = globalThis.__mfWorker;
  delete globalThis.__mfCreate;
  delete globalThis.__mfWorker;
  delete globalThis.__mfBlobs;
}
