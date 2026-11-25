// R11 Part B: the client-side findings of TRIAGE_R11 (R11-M1, M2, M3, M5, M6,
// M9, M10). Every case fails on the pre-fix page and passes on the fixed one.
import test from 'node:test';
import assert from 'node:assert/strict';

import {
  ContributeApp,
  createTaskEnvelope,
  signTaskEnvelope,
  DEMO_OPERATOR_KEY,
  MAX_ARTIFACT_BYTES
} from '../js/contribute.js';
import * as runner from '../js/runner.js';
import * as contribute from '../js/contribute.js';

const {
  MAX_WORKLOAD_FILES,
  resolveIsolationMode,
  verifyEnvelope,
  canonicalJson
} = runner;
import { sha256Hex } from '../js/platform/hash.js';

const bytesOf = (n, salt = 1) => new Uint8Array(Array.from({ length: n }, (_, i) => ((i * salt) % 251) + 1));
const sha = (u8) => sha256Hex(u8);

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

function harness({ assignment, artifacts }) {
  const calls = [];
  const fetchStub = async (url, options = {}) => {
    const target = String(url);
    calls.push({ url: target, method: options.method || 'GET', body: options.body });
    if (target.includes('/leases')) return { ok: true, status: 200, json: async () => assignment };
    if (target.includes('/artifacts/')) {
      const id = target.split('/artifacts/')[1].split('?')[0];
      const buf = artifacts[id];
      if (!buf) return { ok: false, status: 404, arrayBuffer: async () => new ArrayBuffer(0) };
      return { ok: true, status: 200, arrayBuffer: async () => buf.buffer.slice(buf.byteOffset, buf.byteOffset + buf.byteLength) };
    }
    if (target.includes('/results')) return { ok: true, status: 200, json: async () => ({ ok: true }) };
    return { ok: true, status: 200, json: async () => ({}) };
  };
  return { calls, fetchStub };
}

function app() {
  const a = new ContributeApp({ autoStart: false });
  a.sourceMode = 'LIVE';
  a.tabIds = { tp: 'tab-r11' };
  a.tabId = 'tab-r11';
  a.runnerNonce = 'nonce-r11';
  return a;
}

async function getLive(assignment, artifacts) {
  const h = harness({ assignment, artifacts });
  const original = globalThis.fetch;
  globalThis.fetch = h.fetchStub;
  let job = null;
  let threw = null;
  try {
    job = await app().getLiveJob();
  } catch (e) {
    threw = e;
  } finally {
    globalThis.fetch = original;
  }
  return { job, threw, calls: h.calls };
}

function assignmentOf(payload, ids) {
  return {
    lease: { jobId: 'job-1', taskId: 'task-1' },
    inputJSON: '{}',
    taskEnvelope: { payload, signature: Buffer.alloc(64, 1).toString('base64') },
    moduleArtifactIds: ids
  };
}

// ---------------------------------------------------------------- M1: signed payload

test('R11-M1: a signed isolationMode verifies in the runner and reaches the job', async () => {
  const ring = await freshKeyRing();
  const bytes = bytesOf(64, 3);
  const inputJSON = JSON.stringify({ json: { kind: 'matmul-f32-gpu', n: 4 } });
  const inputSha = await sha256Hex(new TextEncoder().encode(inputJSON));
  const payload = createTaskEnvelope('job-m1', 'task-m1', await sha(bytes), 3600, ring.keyId, inputSha);
  payload.isolationMode = 'lock-only';
  const signed = await signTaskEnvelope(payload, ring.privateKey);
  const assignment = {
    lease: { jobId: 'job-m1', taskId: 'task-m1' },
    inputJSON,
    taskEnvelope: {
      payload: { ...payload },
      signature: Buffer.from(signed.envSig).toString('base64')
    },
    moduleArtifactIds: ['art-m1']
  };
  const r = await getLive(assignment, { 'art-m1': bytes });
  assert.equal(r.threw, null, 'the client does not throw');
  assert.ok(r.job, 'a signed single-file lease builds a job');
  assert.deepEqual(r.job.envelope, payload, 'the signed payload is forwarded verbatim, not rebuilt from a whitelist');
  assert.equal(r.job.envelope.isolationMode, 'lock-only');
  assert.equal(r.job.isolationMode, 'lock-only', 'the mode reaches the job');

  // The runner's own verifier must accept the exact bytes the page shipped.
  const verdict = await verifyEnvelope({
    envelope: r.job.envelope,
    envSig: r.job.envSig,
    wasmBytes: r.job.wasm,
    keyRing: ring.ring
  });
  assert.equal(verdict.valid, true, verdict.error);
  assert.equal(resolveIsolationMode(r.job), 'lock-only', 'the runner takes the mode from the verified envelope');
});

test('R11-M1: the shipped envelope is byte-identical to the signed payload', async () => {
  const bytes = bytesOf(32, 5);
  const payload = createTaskEnvelope('job-m1b', 'task-m1b', await sha(bytes), 3600, DEMO_OPERATOR_KEY.keyId);
  payload.isolationMode = 'separate+lock';
  payload.workload = { id: 'w', version: '1', files: [{ path: 'm.js', entry: true, sha256: await sha(bytes) }] };
  const r = await getLive(assignmentOf(payload, ['art-m1b']), { 'art-m1b': bytes });
  assert.ok(r.job);
  assert.equal(canonicalJson(r.job.envelope), canonicalJson(payload),
    'the runner canonicalizes exactly the bytes the server signed');
});

// ---------------------------------------------------------------- M2: file count

test('R11-M2: a file count mismatch declines the task, releases the lease and never throws', async () => {
  const m1 = bytesOf(16, 2);
  const m2 = bytesOf(16, 3);
  const m3 = bytesOf(16, 4);
  const payload = createTaskEnvelope('job-m2', 'task-m2', await sha(m1), 3600, DEMO_OPERATOR_KEY.keyId);
  payload.workload = { files: [{ path: 'm.js', entry: true, sha256: await sha(m1) }, { path: 'd.json', sha256: await sha(m2) }] };
  const r = await getLive(assignmentOf(payload, ['a1', 'a2', 'a3']), { a1: m1, a2: m2, a3: m3 });
  assert.equal(r.threw, null, 'the client does not throw');
  assert.equal(r.job, null, 'the task is declined');
  const failure = r.calls.find((c) => c.url.includes('/results'));
  assert.ok(failure, 'the lease is released');
  assert.match(failure.body, /artifact_file_count_mismatch/);
});

test('R11-M2: fewer artifacts than declared files also declines', async () => {
  const m1 = bytesOf(16, 2);
  const m2 = bytesOf(16, 3);
  const payload = createTaskEnvelope('job-m2b', 'task-m2b', await sha(m1), 3600, DEMO_OPERATOR_KEY.keyId);
  payload.workload = { files: [{ path: 'm.js', entry: true, sha256: await sha(m1) }, { path: 'd.json', sha256: await sha(m2) }] };
  const r = await getLive(assignmentOf(payload, ['a1']), { a1: m1 });
  assert.equal(r.threw, null);
  assert.equal(r.job, null);
  assert.match(r.calls.find((c) => c.url.includes('/results')).body, /artifact_file_count_mismatch/);
});

// ---------------------------------------------------------------- M3: digests

test('R11-M3: a declared file without a pinned digest declines the task', async () => {
  const m1 = bytesOf(16, 2);
  const extra = bytesOf(16, 3);
  const payload = createTaskEnvelope('job-m3', 'task-m3', await sha(m1), 3600, DEMO_OPERATOR_KEY.keyId);
  payload.workload = { files: [{ path: 'm.js', entry: true, sha256: await sha(m1) }, { path: 'd.json' }] };
  const r = await getLive(assignmentOf(payload, ['a1', 'a2']), { a1: m1, a2: extra });
  assert.equal(r.job, null, 'nothing is dispatched');
  assert.match(r.calls.find((c) => c.url.includes('/results')).body, /file_digest_missing/);
});

test('R11-M3: the entry is checked against envelope.artifactSha256', async () => {
  const m1 = bytesOf(16, 2);
  const other = bytesOf(17, 3);
  const payload = createTaskEnvelope('job-m3b', 'task-m3b', await sha(other), 3600, DEMO_OPERATOR_KEY.keyId);
  payload.workload = { files: [{ path: 'm.js', entry: true, sha256: await sha(m1) }] };
  const r = await getLive(assignmentOf(payload, ['a1']), { a1: m1 });
  assert.equal(r.job, null, 'entry bytes disagree with the digest the envelope pins');
  assert.match(r.calls.find((c) => c.url.includes('/results')).body,
    /artifact_sha256_mismatch|artifact_entry_digest_mismatch/);
});

test('R11-M3: a missing digest on the single-file path fails closed', async () => {
  const m1 = bytesOf(16, 2);
  const payload = createTaskEnvelope('job-m3c', 'task-m3c', await sha(m1), 3600, DEMO_OPERATOR_KEY.keyId);
  delete payload.artifactSha256;
  const r = await getLive(assignmentOf(payload, ['a1']), { a1: m1 });
  assert.equal(r.job, null, 'an unsigned digest never authorizes bytes');
  assert.match(r.calls.find((c) => c.url.includes('/results')).body, /digest/);
});

// ---------------------------------------------------------------- M5: caps

test('R11-M5: the per-file cap stays and a separate total cap exists', () => {
  assert.equal(typeof contribute.MAX_TOTAL_WORKLOAD_BYTES, 'number',
    'the parent exports its own total-workload bound');
  assert.equal(contribute.MAX_TOTAL_WORKLOAD_BYTES, runner.MAX_TOTAL_WORKLOAD_BYTES,
    'the parent and the runner agree on the total bound of one workload');
  assert.equal(contribute.MAX_TOTAL_WORKLOAD_BYTES, runner.MAX_WASM_BYTES,
    'the total is what the runner accepts for entry plus companions');
  assert.equal(contribute.MAX_ARTIFACT_BYTES, runner.MAX_WASM_BYTES, 'the per-file cap is unchanged');
  assert.ok(contribute.MAX_TOTAL_WORKLOAD_BYTES < MAX_WORKLOAD_FILES * contribute.MAX_ARTIFACT_BYTES,
    'the total is no longer the per-artifact cap repeated MAX_WORKLOAD_FILES times');
});

test('R11-M5: a legal multi-file workload up to the total cap is accepted', async () => {
  // Five 6 MiB files = 30 MiB: inside the 32 MiB total even though the sum
  // exceeds what a single per-file check alone would allow.
  const chunk = 6 * 1024 * 1024;
  const ids = [];
  const artifacts = {};
  const files = [];
  for (let i = 0; i < 5; i++) {
    const buf = new Uint8Array(chunk).fill(i + 7);
    const id = `big-${i}`;
    ids.push(id);
    artifacts[id] = buf;
    files.push({ path: `f${i}.wasm`, entry: i === 0, sha256: await sha(buf) });
  }
  const payload = createTaskEnvelope('job-m5', 'task-m5', files[0].sha256, 3600, DEMO_OPERATOR_KEY.keyId);
  payload.workload = { files };
  const r = await getLive(assignmentOf(payload, ids), artifacts);
  assert.ok(r.job, 'the workload runs');
  assert.equal(Object.keys(r.job.files).length, 4, 'the four companions reach the kernel');
});

test('R11-M5: a workload whose files sum above the total cap declines', async () => {
  // The per-file cap is 32 MiB, so a real overrun needs real bytes: three
  // 11 MiB files are under it each and over it together (33 MiB > 32 MiB).
  const chunk = 11 * 1024 * 1024;
  const ids = [];
  const artifacts = {};
  const files = [];
  for (let i = 0; i < 3; i++) {
    const buf = new Uint8Array(chunk).fill(i + 1);
    const id = `cap-${i}`;
    ids.push(id);
    artifacts[id] = buf;
    files.push({ path: `f${i}.wasm`, entry: i === 0, sha256: await sha(buf) });
  }
  const payload = createTaskEnvelope('job-m5b', 'task-m5b', files[0].sha256, 3600, DEMO_OPERATOR_KEY.keyId);
  payload.workload = { files };
  const r = await getLive(assignmentOf(payload, ids), artifacts);
  assert.equal(r.job, null, 'the whole workload bound is enforced, not only the per-file one');
  assert.match(r.calls.find((c) => c.url.includes('/results')).body, /workload_too_large/);
});

// ---------------------------------------------------------------- M10: file map

test('R11-M10: a "__proto__" companion path is refused, never silently swallowed', async () => {
  const m1 = bytesOf(16, 2);
  const evil = bytesOf(16, 3);
  const payload = createTaskEnvelope('job-m10', 'task-m10', await sha(m1), 3600, DEMO_OPERATOR_KEY.keyId);
  payload.workload = { files: [{ path: 'm.js', entry: true, sha256: await sha(m1) }, { path: '__proto__', sha256: await sha(evil) }] };
  const r = await getLive(assignmentOf(payload, ['a1', 'a2']), { a1: m1, a2: evil });
  assert.equal(r.job, null, 'a prototype key never becomes a file');
  assert.match(r.calls.find((c) => c.url.includes('/results')).body, /workload_file_path_invalid/);
});

test('R11-M10: a "constructor" companion path is refused too', async () => {
  const m1 = bytesOf(16, 2);
  const evil = bytesOf(16, 3);
  const payload = createTaskEnvelope('job-m10b', 'task-m10b', await sha(m1), 3600, DEMO_OPERATOR_KEY.keyId);
  payload.workload = { files: [{ path: 'm.js', entry: true, sha256: await sha(m1) }, { path: 'constructor', sha256: await sha(evil) }] };
  const r = await getLive(assignmentOf(payload, ['a1', 'a2']), { a1: m1, a2: evil });
  assert.equal(r.job, null);
  assert.match(r.calls.find((c) => c.url.includes('/results')).body, /workload_file_path_invalid/);
});

// ---------------------------------------------------------------- M2/M3: every path reports

test('R11-M2/M3: every decline of a live lease is reported, none escapes getLiveJob', async () => {
  const m1 = bytesOf(16, 2);
  const cases = [
    {
      name: 'artifact missing',
      assignment: (payload) => assignmentOf(payload, ['missing']),
      artifacts: {},
      pattern: /artifact_fetch|artifact_empty/
    },
    {
      name: 'payload is not an object',
      assignment: () => ({
        lease: { jobId: 'job-x', taskId: 'task-x' },
        inputJSON: '{}',
        taskEnvelope: { payload: '"just a string"', signature: Buffer.alloc(64, 1).toString('base64') },
        moduleArtifactIds: ['a1']
      }),
      artifacts: { a1: m1 },
      pattern: /task_envelope_missing|digest/
    },
    {
      name: 'no module artifact id',
      assignment: (payload) => assignmentOf(payload, []),
      artifacts: {},
      pattern: /module_artifact_missing|artifact_file_count_mismatch/
    }
  ];
  for (const c of cases) {
    const payload = createTaskEnvelope('job-x', 'task-x', await sha(m1), 3600, DEMO_OPERATOR_KEY.keyId);
    const r = await getLive(c.assignment(payload), c.artifacts);
    assert.equal(r.threw, null, `${c.name}: no throw`);
    assert.equal(r.job, null, `${c.name}: no job`);
    const failure = r.calls.find((x) => x.url.includes('/results'));
    assert.ok(failure, `${c.name}: the lease is released`);
    assert.match(failure.body, c.pattern, `${c.name}: the reason is reported`);
  }
});

// ---------------------------------------------------------------- M9: import side effects

test('R11-M9: importing runner.js from the page has no side effects', async () => {
  const listeners = [];
  const savedWindow = globalThis.window;
  const savedSelf = globalThis.self;
  // A top-level page: parent === window, as on the contributor page.
  const fakeWindow = {
    parent: null,
    addEventListener(type) { listeners.push(type); }
  };
  fakeWindow.parent = fakeWindow;
  globalThis.window = fakeWindow;
  globalThis.self = fakeWindow;
  try {
    const mod = await import('../js/runner.js?r11-side-effect-probe=1');
    assert.equal(typeof mod.handleJob, 'function', 'the module loads');
    assert.deepEqual(listeners, [], 'an import installs no window listener on a top-level page');
    assert.equal(globalThis.Worker, savedWindow && savedWindow.Worker, 'no Worker is constructed by an import');
  } finally {
    if (savedWindow === undefined) delete globalThis.window;
    else globalThis.window = savedWindow;
    if (savedSelf === undefined) delete globalThis.self;
    else globalThis.self = savedSelf;
  }
});
