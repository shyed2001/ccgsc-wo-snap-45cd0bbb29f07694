// R10-FIX row 4 (qwen MINOR x3) + owner D-W10-9: the runner's own guards.
//   - the demo scope pin must fail CLOSED: a demo key with a pinned
//     moduleSha256 may never authorize a job that names no artifact digest;
//   - the workload import ban must catch the dynamic `import(` form and
//     `export ... from` re-exports, not only static `import`;
//   - the signed input cap must count UTF-8 bytes, not UTF-16 code units.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { Worker as NodeWorker } from 'node:worker_threads';
import { fileURLToPath } from 'node:url';

import {
  resolveOperatorKey,
  OPERATOR_KEYS,
  handleJob,
  workloadImports,
  MAX_INPUT_JSON_BYTES
} from '../js/runner.js';
import { sha256Hex } from '../js/platform/hash.js';
import {
  base64ToBytes,
  createTaskEnvelope,
  signTaskEnvelope,
  DEMO_OPERATOR_KEY
} from '../js/contribute.js';
import { computeWorkerCap } from '../js/runner.js';

const pagesDir = path.join(path.dirname(fileURLToPath(import.meta.url)), '..');
const DEMO_KEY = OPERATOR_KEYS[DEMO_OPERATOR_KEY.keyId];
const DEMO_SHA = DEMO_KEY.moduleSha256;

// ---------------------------------------------------------------- demo scope

test('a demo key fails closed when the envelope names no artifact digest', () => {
  const missing = resolveOperatorKey(DEMO_OPERATOR_KEY.keyId, undefined);
  assert.equal(missing.valid, false, 'a falsy digest skipped the demo scope pin entirely');
  assert.equal(missing.error, 'operator_key_scope_violation');

  for (const falsy of [null, '', 0, false]) {
    const res = resolveOperatorKey(DEMO_OPERATOR_KEY.keyId, falsy);
    assert.equal(res.valid, false, `digest ${JSON.stringify(falsy)} must not pass the demo scope pin`);
    assert.equal(res.error, 'operator_key_scope_violation');
  }
});

test('a demo key still authorizes exactly its pinned digest and nothing else', () => {
  const ok = resolveOperatorKey(DEMO_OPERATOR_KEY.keyId, DEMO_SHA);
  assert.equal(ok.valid, true);
  assert.equal(ok.error, null);

  const other = resolveOperatorKey(DEMO_OPERATOR_KEY.keyId, 'f'.repeat(64));
  assert.equal(other.valid, false);
  assert.equal(other.error, 'operator_key_scope_violation');

  // An operator-scope key has no module pin and keeps answering as before.
  const opKeyId = '7dcc2218b5ba8363';
  assert.equal(resolveOperatorKey(opKeyId, undefined).valid, true,
    'only the demo scope carries the digest pin');
});

// ---------------------------------------------------------------- import ban

// The exact rule the runner applies to a workload's source before it becomes a
// blob module. Imported from runner.js so the ban cannot drift from the guard.
function workloadImportViolation(source) {
  return workloadImports(source);
}

test('the workload import ban catches every module-graph edge', () => {
  const banned = [
    "import x from './a.js';",
    "import './a.js';",
    "const m = await import('./a.js');",
    "const m = import('./a.js');",
    "export { a } from './a.js';",
    "export * from './a.js';",
    "export * as ns from './a.js';",
    "const s = import (`./a-${n}.js`);"
  ];
  for (const source of banned) {
    assert.equal(workloadImportViolation(source), true, `not banned: ${source}`);
  }
  assert.equal(workloadImportViolation(
    'export function run(input) { return { output: { n: 1, bytes: new Uint8Array([1]) } }; }'), false,
  'the plain ABI stays allowed');
  assert.equal(workloadImportViolation('const important = 1;'), false, '"important" is not an import');
  assert.equal(workloadImportViolation('const x = { import: 1 };'), false, 'a property named import is not an import');
  assert.equal(workloadImportViolation('const x = 1 / 2; // ratio'), false, 'division is not a regex literal');
});

// A fresh operator-scope key ring: the demo key may only sign the demo digest,
// and these cases exercise the guards, not the scope pin.
async function freshRing() {
  const pair = await globalThis.crypto.subtle.generateKey({ name: 'Ed25519' }, true, ['sign', 'verify']);
  const pubRaw = new Uint8Array(await globalThis.crypto.subtle.exportKey('raw', pair.publicKey));
  const keyId = (await sha256Hex(pubRaw)).slice(0, 16);
  return {
    keyId,
    privateKey: pair.privateKey,
    ring: { [keyId]: { keyId, publicKeyHex: Buffer.from(pubRaw).toString('hex'), scope: 'operator' } }
  };
}

// The merge of the W10d wasm-abi lane replaced this guard with an inline
// `/^\s*import\b/m` test on the js-module branch, which misses the dynamic and
// re-export forms. This test drives the real dispatch path so the ban cannot
// regress to the weaker regex again: every module-graph edge must be refused
// before a Worker is created.
test('handleJob refuses every import form on the js-module branch', async () => {
  const forms = [
    ['static', 'import x from "./other.js";\nexport function run() { return { output: null }; }'],
    ['dynamic', 'const m = await import("./other.js");\nexport function run() { return { output: null }; }'],
    ['reexport', 'export { run } from "./other.js";'],
    ['reexport-star', 'export * from "./other.js";']
  ];
  const keys = await freshRing();
  const originalWorker = globalThis.Worker;
  let constructed = 0;
  globalThis.Worker = class { constructor() { constructed += 1; throw new Error('unused'); } };
  try {
    for (const [label, source] of forms) {
      const buf = new TextEncoder().encode(source).buffer;
      const inputJSON = JSON.stringify({ json: { n: 1 } });
      const envelope = createTaskEnvelope(
        'job-ban-' + label, 'task-ban-' + label, await sha256Hex(buf), 3600, keys.keyId,
        await sha256Hex(new TextEncoder().encode(inputJSON)));
      // Two paths to the same branch: kind absent (the js-module default) and an
      // explicit js-module kind, which is what a signed W10c envelope carries.
      const sha = await sha256Hex(buf);
      for (const workload of [undefined, { id: 'w', version: 1, kind: 'js-module', files: [{ path: 'entry.js', sha256: sha }] }]) {
        const env = workload === undefined ? envelope : { ...envelope, workload };
        const signed = await signTaskEnvelope(env, keys.privateKey);
        const res = await handleJob({
          type: 'job', jobId: env.jobId, taskId: env.taskId,
          wasm: buf, wasmSha256: await sha256Hex(buf), envelope: env,
          envSig: signed.envSig, inputJSON, workersRequested: 1, timeoutMs: 5000
        }, null, { keyRing: keys.ring });
        assert.equal(res.ok, false, `${label} must not run`);
        assert.match(res.error, /workload_must_not_import/,
          `${label} (kind=${workload ? 'js-module' : 'default'}) must be refused by the ban`);
      }
    }
    assert.equal(constructed, 0, 'a banned workload never reaches Worker construction');
  } finally {
    globalThis.Worker = originalWorker;
  }
});

test('handleJob refuses a workload that imports through the dynamic form', async () => {
  const keys = await freshRing();
  const source = 'const m = await import("./other.js");\nexport function run() { return m; }';
  const buf = new TextEncoder().encode(source).buffer;
  const inputJSON = JSON.stringify({ json: { n: 1 } });
  const envelope = createTaskEnvelope(
    'job-import', 'task-import', await sha256Hex(buf), 3600, keys.keyId,
    await sha256Hex(new TextEncoder().encode(inputJSON)));
  const signed = await signTaskEnvelope(envelope, keys.privateKey);
  const originalWorker = globalThis.Worker;
  globalThis.Worker = class { constructor() { throw new Error('unused'); } };
  try {
    const res = await handleJob({
      type: 'job', jobId: envelope.jobId, taskId: envelope.taskId,
      wasm: buf, wasmSha256: await sha256Hex(buf), envelope,
      envSig: signed.envSig, inputJSON, workersRequested: 1, timeoutMs: 5000
    }, null, { keyRing: keys.ring });
    assert.equal(res.ok, false);
    assert.match(res.error, /workload_must_not_import/, 'the dynamic import form is never dispatched');
  } finally {
    globalThis.Worker = originalWorker;
  }
});

// ---------------------------------------------------------------- input cap

test('the signed input cap counts UTF-8 bytes, not UTF-16 code units', async () => {
  // "€" is 3 UTF-8 bytes and 1 UTF-16 code unit. The JSON wrapper adds 11 bytes.
  const prefix = '{"json":"';
  const suffix = '"}';
  const padChars = MAX_INPUT_JSON_BYTES - 1024;
  const inputJSON = prefix + '€'.repeat(padChars) + suffix;
  assert.ok(inputJSON.length < MAX_INPUT_JSON_BYTES,
    `the input is ${inputJSON.length} code units, under the cap`);
  assert.ok(new TextEncoder().encode(inputJSON).length > MAX_INPUT_JSON_BYTES,
    'but it is over the cap in bytes');

  const keys = await freshRing();
  const wasm = new TextEncoder().encode('export function run() { return { output: null }; }').buffer;
  const inputSha = await sha256Hex(new TextEncoder().encode(inputJSON));
  const envelope = createTaskEnvelope(
    'job-bytes', 'task-bytes', await sha256Hex(wasm), 3600, keys.keyId, inputSha);
  const signed = await signTaskEnvelope(envelope, keys.privateKey);
  const res = await handleJob({
    type: 'job', jobId: envelope.jobId, taskId: envelope.taskId,
    wasm, envelope, envSig: signed.envSig, inputJSON, workersRequested: 1, timeoutMs: 5000
  }, null, { keyRing: keys.ring });
  assert.equal(res.error, 'task_input_too_large', 'the cap must be byte-based');
});

test('an input just under the byte cap still passes the cap', async () => {
  const padChars = Math.floor((MAX_INPUT_JSON_BYTES - 64) / 3);
  const inputJSON = '{"json":"' + '€'.repeat(padChars) + '"}';
  const bytes = new TextEncoder().encode(inputJSON).length;
  assert.ok(bytes <= MAX_INPUT_JSON_BYTES, `built ${bytes} bytes`);
  const inputSha = await sha256Hex(new TextEncoder().encode(inputJSON));
  const keys = await freshRing();
  const wasm = new TextEncoder().encode('export function run() { return { output: null }; }').buffer;
  const envelope = createTaskEnvelope(
    'job-under', 'task-under', await sha256Hex(wasm), 3600, keys.keyId, inputSha);
  const signed = await signTaskEnvelope(envelope, keys.privateKey);
  const res = await handleJob({
    type: 'job', jobId: envelope.jobId, taskId: envelope.taskId,
    wasm, envelope, envSig: signed.envSig, inputJSON, workersRequested: 1, timeoutMs: 5000
  }, null, { keyRing: keys.ring });
  assert.notEqual(res.error, 'task_input_too_large', 'an input under the byte cap is not refused by the cap');
});
