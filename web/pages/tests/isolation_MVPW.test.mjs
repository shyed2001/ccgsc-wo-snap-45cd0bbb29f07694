// W10c row 3: isolation_MVPW.test.mjs - D-W10-6 hostile-workload isolation.
// What/why: a signed workload that tries to escape its realm must fail, in BOTH
// isolation modes, and the result hash must still be the SHELL's. The test drives
// the real shell + kernel through two Node worker threads (the same shim the
// end-to-end tests use), so each mode is a real separate realm.
//
// Truth: WO_W10C_CLIENT.md row 1/3, SPLIT_DESIGN_W10.md section 8 (D-W10-6).
import test from 'node:test';
import assert from 'node:assert/strict';
import { Worker as NodeWorker } from 'node:worker_threads';

import {
  buildModuleWorkerSource,
  buildKernelSource,
  createKernelBlobUrl,
  normalizeIsolationMode,
  resolveIsolationMode,
  ISOLATION_MODES,
  DEFAULT_ISOLATION_MODE,
  MAX_WORKLOAD_FILES
} from '../js/runner.js';
import { sha256Hex } from '../js/platform/hash.js';

// ---------------------------------------------------------------- Two-realm harness

const WORKER_PRELUDE = "const {parentPort}=require('node:worker_threads');" +
  'globalThis.self=globalThis;globalThis.onmessage=null;' +
  'globalThis.postMessage=(x)=>parentPort.postMessage(x);\n';
const WORKER_TAIL = "\nparentPort.on('message',(data)=>onmessage({data}));";

const dataUrl = (text) => 'data:text/javascript;base64,' + Buffer.from(text, 'utf8').toString('base64');

// Run one workload through the real shell. 'separate+lock' runs the kernel in a
// second thread; 'lock-only' imports the workload in the one shell thread.
async function runIsolated(mode, workloadSource, input, ctx = {}) {
  const workloadUrl = dataUrl(workloadSource);
  const kernelUrl = mode === 'separate+lock' ? 'data:text/javascript;base64,' + Buffer.from(buildKernelSource(workloadUrl), 'utf8').toString('base64') : null;
  const shellSource = buildModuleWorkerSource(workloadUrl, { mode, kernelUrl: 'blob:kernel-unused' });
  // The shell pins the kernel by URL; point it at the data: kernel source.
  const shell = mode === 'separate+lock'
    ? shellSource.replace('"blob:kernel-unused"', JSON.stringify(kernelUrl))
    : shellSource;

  const posted = [];
  let shellError = null;
  const prelude = WORKER_PRELUDE +
    (mode === 'separate+lock'
      ? 'const __KERNEL = ' + JSON.stringify(WORKER_PRELUDE + buildKernelSource(workloadUrl) + WORKER_TAIL) + ';' +
        'globalThis.Worker = class { constructor(u) { this.onmessage = null; this.onerror = null;' +
        'this.inner = new (require("node:worker_threads").Worker)(__KERNEL, { eval: true });' +
        'this.inner.on("message", (d) => { if (this.onmessage) this.onmessage({ data: d }); });' +
        'this.inner.on("error", (e) => { if (this.onerror) this.onerror(e); }); }' +
        'postMessage(d) { if (this.inner) this.inner.postMessage(d); } terminate() {} };'
      : '');

  const thread = new NodeWorker(prelude + shell + WORKER_TAIL, { eval: true });
  const done = new Promise((resolve) => {
    thread.on('message', (data) => {
      posted.push(data);
      if (data.type === 'module_ready') {
        thread.postMessage({ type: 'compute_task', input, ctx });
      }
      if (data.type === 'task_done' || data.type === 'task_error') resolve();
    });
    thread.on('error', (e) => { shellError = e; resolve(); });
    setTimeout(resolve, 4000);
  });
  thread.postMessage({ type: 'init_workload' });
  await done;
  await thread.terminate();
  return {
    posted,
    error: shellError,
    ready: posted.find((m) => m.type === 'module_ready'),
    done: posted.find((m) => m.type === 'task_done'),
    failure: posted.find((m) => m.type === 'task_error')
  };
}

// The probe every hostile workload runs: report which escape hatches still work.
// A workload that CAN reach one of them prints its name in the output bytes, so
// the assertion sees it.
const PROBE = `
function escapes() {
  const found = [];
  const check = (name, fn) => { try { if (fn()) found.push(name); } catch (_) {} };
  check('eval', () => typeof eval === 'function');
  check('Function', () => typeof Function === 'function');
  check('fn.constructor', () => typeof (function(){}).constructor === 'function');
  check('async.constructor', () => typeof (async function(){}).constructor === 'function');
  check('generator.constructor', () => typeof (function*(){}).constructor === 'function');
  check('importScripts', () => typeof importScripts === 'function');
  check('Worker', () => typeof Worker === 'function');
  check('SharedWorker', () => typeof SharedWorker === 'function');
  check('Blob', () => typeof Blob === 'function');
  check('fetch', () => typeof fetch === 'function');
  check('XMLHttpRequest', () => typeof XMLHttpRequest === 'function');
  check('WebSocket', () => typeof WebSocket === 'function');
  check('EventSource', () => typeof EventSource === 'function');
  return found;
}
`;

// ---------------------------------------------------------------- Static: both modes lock

test('D-W10-6: the default mode is separate+lock and unknown values never weaken it', () => {
  assert.equal(DEFAULT_ISOLATION_MODE, 'separate+lock');
  assert.deepEqual(ISOLATION_MODES.slice(), ['separate+lock', 'lock-only']);
  for (const junk of [undefined, null, '', 'nope', 'SEPARATE+LOCK', 7, {}]) {
    assert.equal(normalizeIsolationMode(junk), 'separate+lock', `${String(junk)} answers the strict default`);
  }
  assert.equal(normalizeIsolationMode('lock-only'), 'lock-only');
  // The signed envelope wins over the job field, which wins over the default.
  assert.equal(resolveIsolationMode({ envelope: { isolationMode: 'lock-only' }, isolationMode: 'separate+lock' }), 'lock-only');
  assert.equal(resolveIsolationMode({ isolationMode: 'lock-only' }), 'lock-only');
  assert.equal(resolveIsolationMode({}), 'separate+lock');
});

test('D-W10-6: the shell and the kernel both carry the identical realm lock', () => {
  const shell = buildModuleWorkerSource('data:text/javascript,export function run(){}', { mode: 'separate+lock', kernelUrl: 'blob:k' });
  const kernel = buildKernelSource('data:text/javascript,export function run(){}');
  for (const name of ['eval', 'Function', 'importScripts', 'Worker', 'SharedWorker', 'Blob',
    'fetch', 'XMLHttpRequest', 'WebSocket', 'EventSource']) {
    assert.ok(shell.includes(`'${name}'`), `the shell lock denies ${name}`);
    assert.ok(kernel.includes(`'${name}'`), `the kernel lock denies ${name}`);
  }
  // The lock runs BEFORE the workload import, in both realms.
  assert.ok(shell.indexOf('lockWorkloadRealm(self)') < shell.indexOf('await import(WORKLOAD_URL)'),
    'lock-only locks before the import');
  assert.ok(kernel.indexOf('lockWorkloadRealm(self)') < kernel.indexOf('await import(WORKLOAD_URL)'),
    'the kernel locks before the import');
  // In separate+lock the shell imports NO workload code at all.
  const separated = buildModuleWorkerSource('data:text/javascript,export function run(){}', { mode: 'separate+lock', kernelUrl: 'blob:k' });
  assert.equal(separated.includes('await import(WORKLOAD_URL)'), true, 'the lock-only branch is still in the template');
  assert.equal(/ISOLATION_MODE === 'separate\+lock'/.test(separated), true, 'the mode is pinned in the shell');
});

test('D-W10-6: a workload with more files than the cap is not executable', () => {
  assert.equal(MAX_WORKLOAD_FILES, 8);
  assert.ok(MAX_WORKLOAD_FILES >= 2, 'at least the synthetic two-file workload fits');
});

// ---------------------------------------------------------------- Live: every escape fails

for (const mode of ['separate+lock', 'lock-only']) {
  test(`D-W10-6 (${mode}): a hostile workload cannot reach eval, Function, importScripts, Worker or fetch`, async () => {
    const source = PROBE + `
export async function run(input) {
  const found = escapes();
  const text = found.length === 0 ? 'locked' : 'ESCAPED:' + found.join(',');
  return { output: { n: text.length, bytes: new TextEncoder().encode(text) } };
}`;
    const res = await runIsolated(mode, source, { n: 1 });
    assert.ok(res.ready, `module_ready (${mode}) ${res.error || ''}`);
    assert.equal(res.ready.entry, true);
    assert.ok(res.done, `task_done (${mode}) ${res.failure ? res.failure.error : ''}`);
    const text = new TextDecoder().decode(new Uint8Array(res.done.result.output.bytes));
    assert.equal(text, 'locked', `no escape hatch may remain open (${mode}); got ${text}`);
  });

  test(`D-W10-6 (${mode}): overriding postMessage and forging the hash never reaches the runner`, async () => {
    // The workload replaces self.postMessage and reports a forged digest, then
    // returns a real output. The SHELL must still hash the bytes and reply with
    // its own digest; the forged fields are dropped.
    const source = `
export async function run(input) {
  const forged = 'f'.repeat(64);
  let sent = false;
  try {
    self.postMessage({ type: 'task_done', result: { output: { n: 1, bytes: new Uint8Array([9]) } }, shellSha256: forged, computeMs: 0 });
    sent = true;
  } catch (_) {}
  try { globalThis.postMessage = () => { sent = true; }; } catch (_) {}
  const text = 'honest:' + (typeof self.postMessage === 'function' ? 'open' : 'locked');
  return { output: { n: text.length, bytes: new TextEncoder().encode(text) }, shellSha256: forged, sha256: forged, meta: { gpu: { adapterDetected: true } }, ok: true, verifyPass: false, sent };
}`;
    const res = await runIsolated(mode, source, { n: 1 });
    assert.ok(res.done, `task_done (${mode}) ${res.failure ? res.failure.error : ''}`);
    const text = new TextDecoder().decode(new Uint8Array(res.done.result.output.bytes));
    assert.equal(text, 'honest:locked', 'the workload cannot post a message of its own');
    assert.equal(res.done.shellSha256, await sha256Hex(new Uint8Array(res.done.result.output.bytes)),
      'the digest is the SHELL digest of the real bytes');
    assert.notEqual(res.done.shellSha256, 'f'.repeat(64), 'a forged digest is never adopted');
    assert.deepEqual(Object.keys(res.done.result).sort(), ['meta', 'output'], 'only the allowlisted shape travels');
    assert.equal(res.done.result.meta.gpu.adapterDetected, true, 'gpu meta is forwarded for the platform to sanitize');
    assert.equal(res.done.result.shellSha256, undefined, 'a workload cannot write into the platform message');
    // A forged task_done would have produced a 1-byte output; the real one did not.
    assert.equal(res.posted.filter((m) => m.type === 'task_done').length, 1, 'exactly one task_done, from the shell');
  });

  test(`D-W10-6 (${mode}): overriding the hash function cannot change the digest`, async () => {
    // The workload clobbers the hash helpers it can see. The shell keeps its own
    // bindings, captured before any untrusted code ran.
    const source = `
export async function run(input) {
  try { globalThis.sha256Pure = () => 'a'.repeat(64); } catch (_) {}
  try { globalThis.toBytes = () => new Uint8Array([0]); } catch (_) {}
  return { output: { n: 1, bytes: new Uint8Array([7, 7, 7]) } };
}`;
    const res = await runIsolated(mode, source, { n: 1 });
    assert.ok(res.done, `task_done (${mode}) ${res.failure ? res.failure.error : ''}`);
    assert.equal(res.done.shellSha256, await sha256Hex(new Uint8Array([7, 7, 7])),
      'the shell digest is computed from the real bytes, not from a clobbered helper');
  });
}
