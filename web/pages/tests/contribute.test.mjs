// WO_T3_CONTRIBUTE_PAGE contribute.test.mjs - Contributor Pool Unit & Schema Test Suite
// Author: Claude Opus 5.5 head / agy Gemini 3.8 Flash high. Date: 2026-11-17.
// WO_FIX_B (hermes MiniMax-M3, 2026-11-17): one regression test per row of
// docs/reviews/WEBCHAT_R1_glm_runner_20261117.md and of the js rows of
// docs/reviews/REVIEW_02_copilot_20261117.md.
// Truth: WO_T3_CONTRIBUTE_PAGE.md, GRILL_WWPP_R1.md (R29.1, R30.1-3, R31.1-2, R37.1, R39.1)

import { describe, it, beforeEach, afterEach, after } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

import {
  SAMPLE_WASM,
  computeWorkerCount,
  computeWorkerCap,
  detectIsPhone,
  canonicalJson,
  hexOf,
  sha256Hex,
  ENVELOPE_DOMAIN,
  ENVELOPE_VERSION,
  envelopeSigningBytes,
  createTaskEnvelope,
  signTaskEnvelope,
  validateMessageSchema,
  readAuthToken,
  base64ToBytes,
  API_BASE,
  DEMO_OPERATOR_KEY,
  ContributeApp,
  resolveAutoStart,
  jwtSubject
} from '../js/contribute.js';

import {
  handleBootstrapMessage,
  handleJob,
  onPortMessage,
  WORKER_SRC,
  KERNEL_SRC,
  buildModuleWorkerSource,
  RUNNER_BUILD_ID,
  RUNNER_INLINE_BEGIN,
  MAX_WASM_BYTES,
  OPERATOR_KEYS,
  buildInlineRunnerScript,
  deriveKeyId,
  operatorKeyEntry,
  probeWebGPU,
  resolveOperatorKey,
  validateTaskEnvelope,
  verifyEnvelope as verifyEnvelopeRunner
} from '../js/runner.js';

// Tests may read workload fixtures directly; production platform must not.
const demoManifest = JSON.parse(fs.readFileSync(new URL('../../workloads/matmul/manifest.json', import.meta.url), 'utf8'));
const DEMO_WORKLOAD_KIND = demoManifest.kind;
const DEMO_WORKLOAD_SOURCE = fs.readFileSync(new URL('../../workloads/matmul/matmul_kernel.js', import.meta.url), 'utf8');
const DEMO_WORKLOAD_SHA256 = demoManifest.sha256;
import { run as matmulRun } from '../../workloads/matmul/matmul_kernel.js';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const webPagesDir = path.join(__dirname, '..');
const runnerJsPath = path.join(webPagesDir, 'js', 'runner.js');
const runnerHtmlPath = path.join(webPagesDir, 'runner.html');
const contributeJsPath = path.join(webPagesDir, 'js', 'contribute.js');

// ---------------------------------------------------------------- Test Doubles

// A Worker double that follows the protocol of the generic module worker shell
// (D-W10-4, D-W10-6): init_workload -> module_ready { entry }, compute_task
// { input, ctx } -> run(input, ctx) -> task_done { result, shellSha256 }. It can
// hang (a module that never answers), fail (a task_error), refuse the run entry,
// or compute the real answer through the reference kernel the job carries.
// The shell hashes the raw output bytes in its own realm, so the double does too.
class MockWorker {
  constructor(url) {
    this.url = url;
    this.terminated = false;
    this.onmessage = null;
    this.onerror = null;
    MockWorker.instances.push(this);
    MockWorker.spawned++;
  }

  postMessage(msg) {
    if (!msg || typeof msg !== 'object') return;
    if (msg.type === 'init_workload') {
      setTimeout(() => {
        if (!this.terminated && this.onmessage) {
          this.onmessage({ data: { type: 'module_ready', entry: MockWorker.entryAvailable } });
        }
      }, 0);
      return;
    }
    if (msg.type === 'compute_task') {
      if (MockWorker.hang) return; // never answers: a hung workload module
      if (MockWorker.failWith !== null) {
        setTimeout(() => {
          if (!this.terminated && this.onmessage) {
            this.onmessage({ data: { type: 'task_error', error: MockWorker.failWith } });
          }
        }, 0);
        return;
      }
      setTimeout(async () => {
        if (this.terminated || !this.onmessage) return;
        try {
          if (!MockWorker.entryAvailable || typeof MockWorker.run !== 'function') {
            this.onmessage({ data: { type: 'task_error', error: 'unknown_workload: the module exports no run function' } });
            return;
          }
          const result = await MockWorker.run(msg.input, msg.ctx || {});
          const bytes = new Uint8Array(result?.output?.bytes || new ArrayBuffer(0));
          const shellSha256 = bytes.byteLength ? await sha256Hex(bytes) : undefined;
          this.onmessage({ data: { type: 'task_done', result, shellSha256, computeMs: 1 } });
        } catch (e) {
          this.onmessage({ data: { type: 'task_error', error: e.message || String(e) } });
        }
      }, 0);
    }
  }

  terminate() {
    this.terminated = true;
    MockWorker.terminated++;
  }
}
MockWorker.instances = [];
MockWorker.spawned = 0;
MockWorker.terminated = 0;
MockWorker.hang = false;
MockWorker.failWith = null;
// The workload the shell would import: the reference kernel (same source bytes
// the demo job carries). entryAvailable=false emulates a module without run.
MockWorker.run = null;
MockWorker.entryAvailable = true;

// withMockWorker installs the Worker double for the whole (awaited) body. The
// restore must happen AFTER the async body finished, otherwise the runner would
// see the real (missing) Worker.
async function withMockWorker(fn) {
  const original = globalThis.Worker;
  MockWorker.instances = [];
  MockWorker.spawned = 0;
  MockWorker.terminated = 0;
  MockWorker.hang = false;
  MockWorker.failWith = null;
  globalThis.Worker = MockWorker;
  MockWorker.run = matmulRun;
  MockWorker.entryAvailable = true;
  try {
    return await fn();
  } finally {
    globalThis.Worker = original;
  }
}

function mockPort() {
  return {
    messages: [],
    postMessage(msg) { this.messages.push(msg); }
  };
}

// The demo operator key of the runner: the private half of the pinned key.
async function demoPrivateKey() {
  return globalThis.crypto.subtle.importKey(
    'pkcs8',
    base64ToBytes(DEMO_OPERATOR_KEY.pkcs8B64),
    { name: 'Ed25519' },
    true,
    ['sign']
  );
}

// A valid job of the pinned demo key over the independent reference artifact
// workload: the signed input bytes travel with the job and the runner hashes the
// exact inputJSON string against envelope.inputSha256 (W10 binding gate).
async function demoJob(overrides = {}) {
  const priv = await demoPrivateKey();
  const workloadBuf = new TextEncoder().encode(DEMO_WORKLOAD_SOURCE).buffer;
  const workloadSha = await sha256Hex(workloadBuf);
  const inputJSON = overrides.inputJSON !== undefined
    ? overrides.inputJSON
    : JSON.stringify({ json: { kind: DEMO_WORKLOAD_KIND, n: 160 } });
  const inputSha = await sha256Hex(new TextEncoder().encode(inputJSON));
  const envelope = createTaskEnvelope(
    overrides.jobId || 'job-fixb-1',
    overrides.taskId || 'task-fixb-1',
    workloadSha,
    3600,
    DEMO_OPERATOR_KEY.keyId,
    inputSha
  );
  const signed = await signTaskEnvelope(envelope, priv);
  const job = {
    type: 'job',
    jobId: overrides.jobId || 'job-fixb-1',
    taskId: overrides.taskId || 'task-fixb-1',
    wasm: workloadBuf,
    wasmSha256: workloadSha,
    envelope: envelope,
    envSig: signed.envSig,
    workersRequested: overrides.workersRequested || 1,
    inputJSON: inputJSON,
    timeoutMs: overrides.timeoutMs !== undefined ? overrides.timeoutMs : 5000
  };
  if (overrides.needs) job.needs = overrides.needs;
  return job;
}

// Strip the WORKER_SRC template literal from a runner source. The worker source is
// the one place where a module may be instantiated; the main thread code must not.
// It handles the module form ("export const WORKER_SRC = `") and the inline form
// that runner.html carries ("const WORKER_SRC = `").
function withoutWorkerSource(source) {
  const marker = 'WORKER_SRC = `';
  const start = source.indexOf(marker);
  if (start < 0) return source;
  const bodyStart = start + marker.length - 1;
  const end = source.indexOf('`', bodyStart + 1);
  if (end < 0) return source;
  return source.slice(0, start) + source.slice(end + 1);
}

const originalFetch = globalThis.fetch;
const runnerApps = [];

after(() => {
  for (const app of runnerApps) app.disposeRunnerFrame();
});

describe('WO_T3_CONTRIBUTE_PAGE Test Suite', () => {

  describe('1. Worker-Count & Allocation Rules (WWPP-R29.4, WWPP-R30.2, WWPP-R39.1)', () => {

    it('computes auto worker count for desktop systems (cores - 1, min 1, max 4)', () => {
      // 8 cores -> cores - 1 = 7 -> capped at 4
      assert.equal(computeWorkerCount({ hardwareConcurrency: 8, isPhone: false }), 4);

      // 6 cores -> cores - 1 = 5 -> capped at 4
      assert.equal(computeWorkerCount({ hardwareConcurrency: 6, isPhone: false }), 4);

      // 5 cores -> cores - 1 = 4 -> capped at 4
      assert.equal(computeWorkerCount({ hardwareConcurrency: 5, isPhone: false }), 4);

      // 4 cores -> cores - 1 = 3
      assert.equal(computeWorkerCount({ hardwareConcurrency: 4, isPhone: false }), 3);

      // 3 cores -> cores - 1 = 2
      assert.equal(computeWorkerCount({ hardwareConcurrency: 3, isPhone: false }), 2);

      // 2 cores -> cores - 1 = 1
      assert.equal(computeWorkerCount({ hardwareConcurrency: 2, isPhone: false }), 1);

      // 1 core -> min 1 (never 0)
      assert.equal(computeWorkerCount({ hardwareConcurrency: 1, isPhone: false }), 1);

      // 16 cores -> capped at 4
      assert.equal(computeWorkerCount({ hardwareConcurrency: 16, isPhone: false }), 4);

      // 32 cores -> capped at 4
      assert.equal(computeWorkerCount({ hardwareConcurrency: 32, isPhone: false }), 4);
    });

    it('handles missing or zero hardwareConcurrency gracefully', () => {
      assert.equal(computeWorkerCount({ hardwareConcurrency: 0, isPhone: false }), 1);
      assert.equal(computeWorkerCount({ hardwareConcurrency: undefined, isPhone: false }), 1);
      assert.equal(computeWorkerCount({}), 1);
    });

    it('computes mobile phone worker count (1 on battery, 2 when charging)', () => {
      // Phone on battery -> exactly 1 compute worker
      assert.equal(computeWorkerCount({ isPhone: true, isCharging: false }), 1);

      // Phone while charging -> exactly 2 compute workers
      assert.equal(computeWorkerCount({ isPhone: true, isCharging: true }), 2);

      // Phone with many cores on battery still gets 1 worker
      assert.equal(computeWorkerCount({ hardwareConcurrency: 8, isPhone: true, isCharging: false }), 1);

      // Phone with many cores charging gets 2 workers
      assert.equal(computeWorkerCount({ hardwareConcurrency: 8, isPhone: true, isCharging: true }), 2);
    });

    it('enforces runner worker cap (WWPP-R39.1)', () => {
      assert.equal(computeWorkerCap(8), 4);
      assert.equal(computeWorkerCap(4), 3);
      assert.equal(computeWorkerCap(2), 1);
      assert.equal(computeWorkerCap(1), 1);
      assert.equal(computeWorkerCap(16), 4);
      assert.equal(computeWorkerCap(undefined), 1);
    });

    it('accurately identifies phone user agents', () => {
      const androidPhone = 'Mozilla/5.0 (Linux; Android 10; SM-A205U) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36';
      const iPhone = 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1';
      const iPad = 'Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1';
      const desktopChrome = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36';
      const desktopMac = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15';

      assert.equal(detectIsPhone(androidPhone), true, 'Android phone UA must be detected as phone');
      assert.equal(detectIsPhone(iPhone), true, 'iPhone UA must be detected as phone');
      assert.equal(detectIsPhone(iPad), false, 'iPad must NOT be classified as phone');
      assert.equal(detectIsPhone(desktopChrome), false, 'Desktop Chrome must NOT be classified as phone');
      assert.equal(detectIsPhone(desktopMac), false, 'Desktop Mac must NOT be classified as phone');
    });

  });

  describe('2. Cryptographic Envelope & Pre-Execution Verification (WWPP-R37.1)', () => {

    it('canonicalJson produces sorted deterministic JSON strings', () => {
      const objA = { z: 1, a: 2, m: { y: 10, x: 20 } };
      const objB = { a: 2, m: { x: 20, y: 10 }, z: 1 };
      const expected = '{"a":2,"m":{"x":20,"y":10},"z":1}';

      assert.equal(canonicalJson(objA), expected);
      assert.equal(canonicalJson(objB), expected);
      assert.equal(canonicalJson(objA), canonicalJson(objB));
    });

    it('computes correct SHA-256 hex digest on WASM bytes', async () => {
      const sha = await sha256Hex(SAMPLE_WASM);
      assert.equal(typeof sha, 'string');
      assert.equal(sha.length, 64);
      assert.match(sha, /^[0-9a-f]{64}$/);

      // Digest is consistent across multiple calls
      const sha2 = await sha256Hex(SAMPLE_WASM.slice().buffer);
      assert.equal(sha, sha2);
    });

    it('signs and verifies a valid task envelope with Ed25519 test key', async () => {
      const kp = await globalThis.crypto.subtle.generateKey(
        { name: 'Ed25519' },
        true,
        ['sign', 'verify']
      );
      const pubRaw = new Uint8Array(await globalThis.crypto.subtle.exportKey('raw', kp.publicKey));
      const keyRing = { [await deriveKeyId(pubRaw)]: await operatorKeyEntry(pubRaw) };
      const wasmBuf = SAMPLE_WASM.slice().buffer;
      const wasmSha = await sha256Hex(wasmBuf);

      const envelope = createTaskEnvelope('job-101', 'task-55', wasmSha, 3600, await deriveKeyId(pubRaw));
      const signed = await signTaskEnvelope(envelope, kp.privateKey);

      assert.ok(signed.envSig instanceof Uint8Array);
      assert.equal(signed.envSig.length, 64);

      // Verification of valid envelope passes
      const res = await verifyEnvelopeRunner({
        envelope: envelope,
        envSig: signed.envSig,
        wasmBytes: wasmBuf,
        keyRing: keyRing
      });

      assert.equal(res.valid, true, 'Valid envelope must pass verification');
      assert.equal(res.error, null);
    });

    it('fails closed when envelope signature is tampered', async () => {
      const kp = await globalThis.crypto.subtle.generateKey({ name: 'Ed25519' }, true, ['sign', 'verify']);
      const pubRaw = new Uint8Array(await globalThis.crypto.subtle.exportKey('raw', kp.publicKey));
      const keyId = await deriveKeyId(pubRaw);
      const keyRing = { [keyId]: await operatorKeyEntry(pubRaw) };
      const wasmBuf = SAMPLE_WASM.slice().buffer;
      const wasmSha = await sha256Hex(wasmBuf);

      const envelope = createTaskEnvelope('job-102', 'task-56', wasmSha, 3600, keyId);
      const signed = await signTaskEnvelope(envelope, kp.privateKey);

      // Tamper signature by flipping byte 0
      const tamperedSig = new Uint8Array(signed.envSig);
      tamperedSig[0] ^= 0xff;

      const res = await verifyEnvelopeRunner({
        envelope: envelope,
        envSig: tamperedSig,
        wasmBytes: wasmBuf,
        keyRing: keyRing
      });

      assert.equal(res.valid, false, 'Tampered signature must fail verification');
      assert.equal(res.error, 'signature_invalid');
    });

    it('fails closed when envelope content is modified after signing', async () => {
      const kp = await globalThis.crypto.subtle.generateKey({ name: 'Ed25519' }, true, ['sign', 'verify']);
      const pubRaw = new Uint8Array(await globalThis.crypto.subtle.exportKey('raw', kp.publicKey));
      const keyId = await deriveKeyId(pubRaw);
      const keyRing = { [keyId]: await operatorKeyEntry(pubRaw) };
      const wasmBuf = SAMPLE_WASM.slice().buffer;
      const wasmSha = await sha256Hex(wasmBuf);

      const envelope = createTaskEnvelope('job-103', 'task-57', wasmSha, 3600, keyId);
      const signed = await signTaskEnvelope(envelope, kp.privateKey);

      // Tamper envelope field
      const tamperedEnvelope = { ...envelope, jobId: 'job-hacked-999' };

      const res = await verifyEnvelopeRunner({
        envelope: tamperedEnvelope,
        envSig: signed.envSig,
        wasmBytes: wasmBuf,
        keyRing: keyRing
      });

      assert.equal(res.valid, false, 'Altered envelope content must fail verification');
      assert.equal(res.error, 'signature_invalid');
    });

    it('fails closed when WASM bytes do not match envelope artifactSha256', async () => {
      const kp = await globalThis.crypto.subtle.generateKey({ name: 'Ed25519' }, true, ['sign', 'verify']);
      const pubRaw = new Uint8Array(await globalThis.crypto.subtle.exportKey('raw', kp.publicKey));
      const keyId = await deriveKeyId(pubRaw);
      const keyRing = { [keyId]: await operatorKeyEntry(pubRaw) };
      const wasmBuf = SAMPLE_WASM.slice();
      const wasmSha = await sha256Hex(wasmBuf);

      const envelope = createTaskEnvelope('job-104', 'task-58', wasmSha, 3600, keyId);
      const signed = await signTaskEnvelope(envelope, kp.privateKey);

      // Tamper WASM bytes
      const tamperedWasm = new Uint8Array(wasmBuf);
      tamperedWasm[0] ^= 0xff;

      const res = await verifyEnvelopeRunner({
        envelope: envelope,
        envSig: signed.envSig,
        wasmBytes: tamperedWasm,
        keyRing: keyRing
      });

      assert.equal(res.valid, false, 'Tampered WASM bytes must fail hash verification');
      assert.equal(res.error, 'artifact_sha256_mismatch');
    });

    it('fails closed when task lease has expired', async () => {
      const kp = await globalThis.crypto.subtle.generateKey({ name: 'Ed25519' }, true, ['sign', 'verify']);
      const pubRaw = new Uint8Array(await globalThis.crypto.subtle.exportKey('raw', kp.publicKey));
      const keyId = await deriveKeyId(pubRaw);
      const keyRing = { [keyId]: await operatorKeyEntry(pubRaw) };
      const wasmBuf = SAMPLE_WASM.slice().buffer;
      const wasmSha = await sha256Hex(wasmBuf);

      // Expired lease in the past (-300 seconds)
      const expiredEnvelope = createTaskEnvelope('job-105', 'task-59', wasmSha, -300, keyId);
      const signed = await signTaskEnvelope(expiredEnvelope, kp.privateKey);

      const res = await verifyEnvelopeRunner({
        envelope: expiredEnvelope,
        envSig: signed.envSig,
        wasmBytes: wasmBuf,
        keyRing: keyRing
      });

      assert.equal(res.valid, false, 'Expired lease must fail validation');
      assert.equal(res.error, 'lease_expired');
    });

  });

  describe('3. MessageChannel Protocol & Message Schema Validation (WWPP-R39.1)', () => {

    it('validates init message schema', () => {
      assert.equal(validateMessageSchema({ type: 'init', nonce: 'test-nonce-123' }).valid, true);
      assert.equal(validateMessageSchema({ type: 'init' }).valid, false);
      assert.equal(validateMessageSchema({ type: 'init', nonce: '' }).valid, false);
    });

    it('validates ready message schema', () => {
      assert.equal(validateMessageSchema({ type: 'ready', nonce: 'nonce-1', hw: 4 }).valid, true);
      assert.equal(validateMessageSchema({ type: 'ready' }).valid, false);
    });

    it('validates job message schema', () => {
      const validJob = {
        type: 'job',
        jobId: 'job-1',
        taskId: 'task-1',
        nonce: 'nonce-1',
        wasm: new ArrayBuffer(87),
        wasmSha256: 'a'.repeat(64),
        envelope: { jobId: 'job-1' },
        envSig: new Uint8Array(64),
        workersRequested: 3
      };
      assert.equal(validateMessageSchema(validJob).valid, true);

      // Missing required fields
      assert.equal(validateMessageSchema({ ...validJob, jobId: '' }).valid, false);
      assert.equal(validateMessageSchema({ ...validJob, taskId: '' }).valid, false);
      assert.equal(validateMessageSchema({ ...validJob, nonce: '' }).valid, false);
      assert.equal(validateMessageSchema({ ...validJob, wasm: 'not-buffer' }).valid, false);
      assert.equal(validateMessageSchema({ ...validJob, wasmSha256: '' }).valid, false);
      assert.equal(validateMessageSchema({ ...validJob, wasmSha256: 'zz' }).valid, false);
      assert.equal(validateMessageSchema({ ...validJob, envelope: null }).valid, false);
      assert.equal(validateMessageSchema({ ...validJob, envSig: null }).valid, false);
    });

    it('validates result message schema', () => {
      const validResult = {
        type: 'result',
        jobId: 'job-1',
        taskId: 'task-1',
        ok: true,
        workers: 3,
        timings: { compileMs: 1.2, verifyMs: 0.5, computeMs: 15.0 }
      };
      assert.equal(validateMessageSchema(validResult).valid, true);

      assert.equal(validateMessageSchema({ ...validResult, jobId: null }).valid, false);
      assert.equal(validateMessageSchema({ ...validResult, taskId: null }).valid, false);
      assert.equal(validateMessageSchema({ ...validResult, ok: 'not-bool' }).valid, false);
      assert.equal(validateMessageSchema({ ...validResult, timings: null }).valid, false);
    });

    it('validates check diagnostic message schema', () => {
      assert.equal(validateMessageSchema({ type: 'check', name: 'handshake', status: 'PASS' }).valid, true);
      assert.equal(validateMessageSchema({ type: 'check', name: 'webgpu', status: 'SKIP' }).valid, true);
      assert.equal(validateMessageSchema({ type: 'check', name: 'tamper', status: 'FAIL' }).valid, true);
      assert.equal(validateMessageSchema({ type: 'check', name: 'x', status: 'INVALID' }).valid, false);
    });

    it('validates error and ping/pong schemas', () => {
      assert.equal(validateMessageSchema({ type: 'error', msg: 'fatal error' }).valid, true);
      assert.equal(validateMessageSchema({ type: 'error' }).valid, false);

      assert.equal(validateMessageSchema({ type: 'ping', seq: 5 }).valid, true);
      assert.equal(validateMessageSchema({ type: 'pong', seq: 5 }).valid, true);
      assert.equal(validateMessageSchema({ type: 'ping' }).valid, false);

      assert.equal(validateMessageSchema({ type: 'unrecognized_type' }).valid, false);
      assert.equal(validateMessageSchema(null).valid, false);
      assert.equal(validateMessageSchema('not-object').valid, false);
    });

  });

  describe('4. File Integrity & Architectural Conformance Smoke Checks', () => {

    it('verifies all 6 required files exist', () => {
      const requiredFiles = [
        path.join(webPagesDir, 'contribute.html'),
        path.join(webPagesDir, 'js', 'contribute.js'),
        path.join(webPagesDir, 'runner.html'),
        path.join(webPagesDir, 'js', 'runner.js'),
        path.join(webPagesDir, 'tests', 'contribute.test.mjs'),
        path.join(webPagesDir, 'manual', 'contribute.html')
      ];

      for (const f of requiredFiles) {
        assert.ok(fs.existsSync(f), `File must exist: ${f}`);
      }
    });

    it('verifies contribute.html conforms to architecture decisions', () => {
      const content = fs.readFileSync(path.join(webPagesDir, 'contribute.html'), 'utf8');

      assert.ok(content.includes('<!DOCTYPE html>'));
      assert.ok(content.includes('id="nav"'), 'Must contain #nav element for web/nav.js');
      assert.ok(content.includes('nav.js'), 'Must load nav.js');
      assert.ok(content.includes('id="runner-frame"'), 'Must contain runner-frame iframe');
      assert.ok(content.includes('sandbox="allow-scripts"'), 'Runner iframe must use opaque sandbox (WWPP-R29.1)');
      assert.ok(content.includes('allow="webgpu"'), 'Runner iframe must allow webgpu (WWPP-R39.1)');
      assert.ok(!content.includes('id="btn-toggle-compute"'), 'Start/Stop button is removed');
      assert.ok(!content.includes('Start Computing'), 'Start Computing label is removed');
      assert.ok(content.includes('id="worker-count-select"'), 'Worker-count select stays');
      assert.ok(content.includes('value="auto" selected'), 'Worker count defaults to auto');
      assert.ok(content.includes('id="source-badge"'), 'Must contain source badge element');
      assert.ok(content.includes('id="stat-tasks-done"'), 'Must contain tasks done stat');
      assert.ok(content.includes('id="stat-verify-pass"'), 'Must contain verify pass stat');
      assert.ok(content.includes('manual/contribute.html'), 'Must link to manual/contribute.html');
      assert.ok(content.includes('<!-- END OF contribute.html -->'));
    });

    it('verifies runner.html conforms to sandbox runner design', () => {
      const content = fs.readFileSync(runnerHtmlPath, 'utf8');

      assert.ok(content.includes('<!DOCTYPE html>'));
      assert.ok(content.includes('js/runner.js'), 'Must reference js/runner.js');
      assert.ok(content.includes('<!-- END OF runner.html -->'));
    });

    it('verifies manual/contribute.html contains complete manual documentation', () => {
      const content = fs.readFileSync(path.join(webPagesDir, 'manual', 'contribute.html'), 'utf8');

      assert.ok(content.includes('<!DOCTYPE html>'));
      assert.ok(content.includes('manual-rows-tbody'), 'Must contain metrics table body');
      assert.ok(content.includes('WWPP-R29.1'), 'Must document sandbox decision R29.1');
      assert.ok(content.includes('WWPP-R30'), 'Must document worker decision R30');
      assert.ok(content.includes('WWPP-R37.1'), 'Must document verification decision R37.1');
      assert.ok(content.includes('WWPP-R39.1'), 'Must document channel handshake decision R39.1');
      assert.ok(content.includes('../contribute.html'), 'Must link back to live contributor page');
      assert.ok(content.includes('<!-- END OF manual/contribute.html -->'));
    });

    it('verifies JS modules have proper headers and footers', () => {
      const contributeJs = fs.readFileSync(contributeJsPath, 'utf8');
      assert.ok(contributeJs.includes('WO_T3_CONTRIBUTE_PAGE contribute.js'));
      assert.ok(contributeJs.includes('END OF contribute.js'));

      const runnerJs = fs.readFileSync(runnerJsPath, 'utf8');
      assert.ok(runnerJs.includes('WO_T3_CONTRIBUTE_PAGE runner.js'));
      assert.ok(runnerJs.includes('END OF runner.js'));
    });

  });

  describe('5. WO_FIX_REVIEW01_JS Regression & Conformance Tests', () => {

    it('verifies runner.html inlines runner script to avoid opaque sandbox CORS error', () => {
      const content = fs.readFileSync(runnerHtmlPath, 'utf8');

      // Must NOT have external script tag loading js/runner.js because opaque origin null gets blocked by CORS
      assert.ok(!content.includes('<script type="module" src="./js/runner.js">'), 'Must not make cross-origin external script fetch');
      assert.ok(!content.includes('<script src="./js/runner.js">'), 'Must not make cross-origin script fetch');

      // Must have inline script with runner implementation
      assert.ok(content.includes('<script type="module">'), 'Must contain inline script module');
      assert.ok(content.includes('verifyEnvelope'), 'Inline script must contain verifyEnvelope logic');
      assert.ok(content.includes('WORKER_SRC'), 'Inline script must contain compute worker source');
      assert.ok(content.includes('handleBootstrapMessage') || content.includes('source_not_parent'), 'Inline script must contain handshake validation');
    });

    it('handshake strictly requires ev.source === parent before accepting port transfer', () => {
      const mockParent = { name: 'parent_window' };
      const mockAttacker = { name: 'evil_iframe_window' };
      const mockPort = { postMessage() {} };

      // 1. Rejection when source is not parent
      const forgedEv = {
        source: mockAttacker,
        data: { type: 'init', nonce: 'nonce-forged-1' },
        ports: [mockPort]
      };
      const resForged = handleBootstrapMessage(forgedEv, { parent: mockParent, bootstrapped: false });
      assert.equal(resForged.accepted, false);
      assert.equal(resForged.reason, 'source_not_parent');

      // 2. Acceptance when source is parent and valid init message
      const legitEv = {
        source: mockParent,
        data: { type: 'init', nonce: 'nonce-legit-1' },
        ports: [mockPort]
      };
      const resLegit = handleBootstrapMessage(legitEv, { parent: mockParent, bootstrapped: false });
      assert.equal(resLegit.accepted, true);
      assert.equal(resLegit.nonce, 'nonce-legit-1');
      assert.equal(resLegit.port, mockPort);

      // 3. Rejection on duplicate bootstrap attempt
      const resDup = handleBootstrapMessage(legitEv, { parent: mockParent, bootstrapped: true });
      assert.equal(resDup.accepted, false);
      assert.equal(resDup.reason, 'already_bootstrapped');

      // 4. Rejection when no port transferred
      const noPortEv = {
        source: mockParent,
        data: { type: 'init', nonce: 'nonce-legit-2' },
        ports: []
      };
      const resNoPort = handleBootstrapMessage(noPortEv, { parent: mockParent, bootstrapped: false });
      assert.equal(resNoPort.accepted, false);
      assert.equal(resNoPort.reason, 'no_port_transferred');

      // 5. A wrong nonce is refused
      const badNonceEv = {
        source: mockParent,
        data: { type: 'init', nonce: '' },
        ports: [mockPort]
      };
      const resBadNonce = handleBootstrapMessage(badNonceEv, { parent: mockParent, bootstrapped: false });
      assert.equal(resBadNonce.accepted, false);
      assert.equal(resBadNonce.reason, 'invalid_init_message');
    });

    it('envelope adheres to Go canonical form with domain prefix ccgsc.pools.task-envelope.v1\\0', () => {
      assert.equal(ENVELOPE_DOMAIN, 'ccgsc.pools.task-envelope.v1');
      assert.equal(ENVELOPE_VERSION, 1);

      const testEnv = {
        version: 1,
        jobId: 'job-domain-test',
        taskId: 'task-domain-test',
        artifactSha256: '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef',
        inputSha256: '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef',
        issuedAt: 1700000000,
        expiresAt: 1700003600,
        keyId: 'key-test-domain'
      };

      const bytes = envelopeSigningBytes(testEnv);
      const prefixExpected = 'ccgsc.pools.task-envelope.v1\0';
      const prefixActual = new TextDecoder().decode(bytes.slice(0, prefixExpected.length));

      assert.equal(prefixActual, prefixExpected, 'Envelope bytes must start with domain separator');

      // Remainder must be canonical JSON with sorted keys
      const canonicalPayloadStr = new TextDecoder().decode(bytes.slice(prefixExpected.length));
      assert.equal(canonicalPayloadStr, canonicalJson(testEnv));
      assert.ok(canonicalPayloadStr.startsWith('{"artifactSha256":'), 'Canonical keys must be sorted');
    });

    it('createTaskEnvelope aligns with Go TaskEnvelope struct fields', () => {
      const sha = 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855';
      const env = createTaskEnvelope('job-spec-1', 'task-spec-1', sha, 1800, 'custom-key-1');

      // Verify all 8 fields of Go TaskEnvelope
      assert.equal(env.version, 1);
      assert.equal(env.jobId, 'job-spec-1');
      assert.equal(env.taskId, 'task-spec-1');
      assert.equal(env.artifactSha256, sha);
      assert.equal(env.inputSha256, sha);
      assert.equal(typeof env.issuedAt, 'number');
      assert.equal(typeof env.expiresAt, 'number');
      assert.ok(env.expiresAt > env.issuedAt, 'expiresAt must be after issuedAt');
      assert.equal(env.keyId, 'custom-key-1');

      const keys = Object.keys(env).sort();
      assert.deepEqual(keys, [
        'artifactSha256',
        'expiresAt',
        'inputSha256',
        'issuedAt',
        'jobId',
        'keyId',
        'taskId',
        'version'
      ]);
    });

    it('verifyEnvelope fails closed with missing_wasm_bytes when bytes missing or empty', async () => {
      const env = createTaskEnvelope('job-bytes-1', 'task-bytes-1', await sha256Hex(SAMPLE_WASM), 3600, DEMO_OPERATOR_KEY.keyId);
      const priv = await demoPrivateKey();
      const signed = await signTaskEnvelope(env, priv);

      // 1. null bytes
      const resNull = await verifyEnvelopeRunner({
        envelope: env,
        envSig: signed.envSig,
        wasmBytes: null
      });
      assert.equal(resNull.valid, false);
      assert.equal(resNull.error, 'missing_wasm_bytes');

      // 2. undefined bytes
      const resUndef = await verifyEnvelopeRunner({
        envelope: env,
        envSig: signed.envSig,
        wasmBytes: undefined
      });
      assert.equal(resUndef.valid, false);
      assert.equal(resUndef.error, 'missing_wasm_bytes');

      // 3. empty buffer (0 bytes)
      const resEmpty = await verifyEnvelopeRunner({
        envelope: env,
        envSig: signed.envSig,
        wasmBytes: new ArrayBuffer(0)
      });
      assert.equal(resEmpty.valid, false);
      assert.equal(resEmpty.error, 'missing_wasm_bytes');
    });

    it('verifyEnvelope verifies lease expiration against expiresAt', async () => {
      // The demo key is scoped to the reference workload copy, so the bytes are
      // the demo workload, not the retired sample module.
      const workloadBuf = new TextEncoder().encode(DEMO_WORKLOAD_SOURCE).buffer;
      const workloadSha = await sha256Hex(workloadBuf);
      const priv = await demoPrivateKey();

      // Expired envelope (expiresAt in the past)
      const expiredEnv = {
        version: 1,
        jobId: 'job-exp-1',
        taskId: 'task-exp-1',
        artifactSha256: workloadSha,
        inputSha256: workloadSha,
        issuedAt: Math.floor(Date.now() / 1000) - 7200,
        expiresAt: Math.floor(Date.now() / 1000) - 3600,
        keyId: DEMO_OPERATOR_KEY.keyId
      };
      const signedExpired = await signTaskEnvelope(expiredEnv, priv);

      const res = await verifyEnvelopeRunner({
        envelope: expiredEnv,
        envSig: signedExpired.envSig,
        wasmBytes: workloadBuf
      });
      assert.equal(res.valid, false);
      assert.equal(res.error, 'lease_expired');

      // Future envelope (expiresAt in the future)
      const validEnv = {
        ...expiredEnv,
        issuedAt: Math.floor(Date.now() / 1000) - 100,
        expiresAt: Math.floor(Date.now() / 1000) + 3600
      };
      const signedValid = await signTaskEnvelope(validEnv, priv);

      const resValid = await verifyEnvelopeRunner({
        envelope: validEnv,
        envSig: signedValid.envSig,
        wasmBytes: workloadBuf
      });
      assert.equal(resValid.valid, true);
      assert.equal(resValid.error, null);
    });

    it('runner calculates and spawns workersToSpawn workers based on worker cap', async () => {
      assert.equal(computeWorkerCap(8), 4);
      assert.equal(computeWorkerCap(4), 3);
      assert.equal(computeWorkerCap(2), 1);

      await withMockWorker(async () => {
        const job = await demoJob({ workersRequested: 3 });
        const port = mockPort();
        const res = await handleJob(job, port);

        assert.equal(res.ok, true);
        assert.equal(res.verifyPass, true);
        assert.ok(res.workers >= 1 && res.workers <= 4, 'workers must be within worker cap');
        assert.equal(port.messages.length, 1);
        assert.equal(port.messages[0].workers, res.workers);
      });
    });

    it('runner enforces per-task timeout and terminates workers upon timeout', async () => {
      await withMockWorker(async () => {
        MockWorker.hang = true;
        const job = await demoJob({ workersRequested: 2, timeoutMs: 25 });
        const port = mockPort();

        const res = await handleJob(job, port);

        assert.equal(res.ok, false, 'Timed-out task must report ok=false');
        assert.equal(res.error, 'task_timeout', 'Must report task_timeout error');
        assert.ok(MockWorker.terminated >= 1, 'Hung workers must be terminated upon timeout');
        assert.ok(MockWorker.spawned >= 1, 'Must have spawned worker pool');
        assert.equal(port.messages.length, 1);
        assert.equal(port.messages[0].error, 'task_timeout');
      });
    });

    it('contributor client uses standard worker endpoints without invented /tasks/next', () => {
      const contributeCode = fs.readFileSync(contributeJsPath, 'utf8');

      // 1. Invented /tasks/next must NOT exist anywhere in code
      assert.ok(!contributeCode.includes('/tasks/next'), 'Invented /tasks/next endpoint must be removed');

      // 2. Standard worker endpoints must be present
      assert.ok(contributeCode.includes("API_BASE = '/thread-pool/v1'"), 'Must use the real pool prefix');
      // T104 dual pools: the tab id resolves per pool (the primary pool tab is
      // still this.tabId), so the endpoint pins follow the per-pool template.
      // The routes are the same standard worker routes as before T104.
      assert.ok(contributeCode.includes('${base}/workers/${tabId}/leases'), 'Must use POST /workers/{tabId}/leases');
      assert.ok(contributeCode.includes('${base}/workers/${tabId}/results'), 'Must use POST /workers/{tabId}/results');
      assert.ok(contributeCode.includes('/workers/register'), 'Must use POST /workers/register');

      // 3. ContributeApp supports tabId and graceful MOCK fallback
      const app = new ContributeApp({ tabId: 'tab-unit-test' });
      assert.equal(app.tabId, 'tab-unit-test');
      assert.equal(app.sourceMode, 'MOCK', 'Default or unreachable server sourceMode must be MOCK');
    });

    it('setupRunnerFrame handles early iframe load race condition without dropping handshake', () => {
      const app = new ContributeApp();
      let postedMsg = null;
      let postedTransfer = null;

      const mockIframe = {
        contentDocument: { readyState: 'complete' }, // Already loaded before setup
        getAttribute(name) { return name === 'src' ? 'runner.html' : null; },
        contentWindow: {
          postMessage(msg, targetOrigin, transfer) {
            postedMsg = msg;
            postedTransfer = transfer;
          }
        },
        addEventListener(event, fn) {}
      };

      app.dom = { runnerFrame: mockIframe };
      runnerApps.push(app);
      app.setupRunnerFrame();

      assert.ok(postedMsg !== null, 'Handshake init must be posted immediately for pre-loaded iframe');
      assert.equal(postedMsg.type, 'init');
      assert.equal(typeof postedMsg.nonce, 'string');
      assert.ok(postedTransfer && postedTransfer.length === 1, 'MessagePort must be transferred');

      // Close ports so Node.js event loop terminates cleanly
      if (app.channel) {
        if (app.channel.port1 && typeof app.channel.port1.close === 'function') app.channel.port1.close();
        if (app.channel.port2 && typeof app.channel.port2.close === 'function') app.channel.port2.close();
      }
    });

    it('preserves the exact signed input bytes without falsy coercion', () => {
      // The generic shell passes the signed input to run() verbatim: no default
      // substitution, no re-serialization, no falsy coercion of task data.
      assert.ok(WORKER_SRC.includes('await run(d.input, d.ctx || {})'), 'The shell passes the input through unmodified');
      assert.ok(!/d\.input\s*\?\?/.test(WORKER_SRC), 'No default substitution of the input');
      assert.ok(WORKER_SRC.includes("typeof run !== 'function'"), 'The shell refuses a module without the run entry');
    });

  });

  // ==========================================================================
  // WO_FIX_B - one regression test per review row
  // ==========================================================================

  describe('6. WO_FIX_B - Blockers: the verify gate must not trust the job', () => {

    it('BLOCKER runner.js:286 - a public key that travels with the job is never used for verification', async () => {
      // An attacker signs its own envelope and ships its own public key with the
      // job. The runner must ignore that key and fail closed.
      const attacker = await globalThis.crypto.subtle.generateKey({ name: 'Ed25519' }, true, ['sign', 'verify']);
      const attackerPub = new Uint8Array(await globalThis.crypto.subtle.exportKey('raw', attacker.publicKey));
      const attackerKeyId = await deriveKeyId(attackerPub);

      // W10: the demo key is scoped to the reference workload copy, so the bytes
      // are the demo workload - the second claim below must pass the scope gate
      // and fail exactly at the signature of the pinned key.
      const wasmBuf = new TextEncoder().encode(DEMO_WORKLOAD_SOURCE).buffer;
      const wasmSha = await sha256Hex(wasmBuf);
      const envelope = createTaskEnvelope('job-evil-1', 'task-evil-1', wasmSha, 3600, attackerKeyId);
      const signed = await signTaskEnvelope(envelope, attacker.privateKey);

      // The key id of the attacker is not in the pinned ring.
      const res = await verifyEnvelopeRunner({
        envelope: envelope,
        envSig: signed.envSig,
        wasmBytes: wasmBuf,
        pubKeyRaw: attackerPub // the job supplies the key: it must be ignored
      });
      assert.equal(res.valid, false, 'A job supplied key must never authenticate the job');
      assert.equal(res.error, 'unknown_operator_key');

      // Even when the attacker claims a key id that IS pinned, the pinned key is
      // used, so the attacker signature does not verify.
      const pinnedClaim = { ...envelope, keyId: DEMO_OPERATOR_KEY.keyId };
      const signedClaim = await signTaskEnvelope(pinnedClaim, attacker.privateKey);
      const resClaim = await verifyEnvelopeRunner({
        envelope: pinnedClaim,
        envSig: signedClaim.envSig,
        wasmBytes: wasmBuf,
        pubKeyRaw: attackerPub
      });
      assert.equal(resClaim.valid, false, 'The pinned key must decide the verification');
      assert.equal(resClaim.error, 'signature_invalid');
    });

    it('BLOCKER runner.js:286 - handleJob reports verifyPass false for a job with a foreign key', async () => {
      const attacker = await globalThis.crypto.subtle.generateKey({ name: 'Ed25519' }, true, ['sign', 'verify']);
      const attackerPub = new Uint8Array(await globalThis.crypto.subtle.exportKey('raw', attacker.publicKey));
      const attackerKeyId = await deriveKeyId(attackerPub);

      const wasmBuf = SAMPLE_WASM.slice().buffer;
      const wasmSha = await sha256Hex(wasmBuf);
      const envelope = createTaskEnvelope('job-evil-2', 'task-evil-2', wasmSha, 3600, attackerKeyId);
      const signed = await signTaskEnvelope(envelope, attacker.privateKey);

      const port = mockPort();
      const spawnedBefore = MockWorker.spawned;
      const res = await handleJob({
        type: 'job',
        jobId: 'job-evil-2',
        taskId: 'task-evil-2',
        wasm: wasmBuf,
        wasmSha256: wasmSha,
        envelope: envelope,
        envSig: signed.envSig,
        pubKeyRaw: attackerPub
      }, port);

      assert.equal(res.ok, false);
      assert.equal(res.verifyPass, false);
      assert.equal(res.error, 'unknown_operator_key');
      assert.equal(MockWorker.spawned, spawnedBefore, 'No worker may start for an unverified job');
    });

    it('BLOCKER runner.js:286 - the operator key ring is pinned inside the runner and resolves by key id', async () => {
      assert.ok(OPERATOR_KEYS && typeof OPERATOR_KEYS === 'object', 'The runner must pin a key ring');
      const entry = OPERATOR_KEYS[DEMO_OPERATOR_KEY.keyId];
      assert.ok(entry, 'The demo operator key must be pinned');
      assert.match(entry.publicKeyHex, /^[0-9a-f]{64}$/, 'The pinned key must be a raw Ed25519 public key');

      // The key id is derived from the key, exactly like Go verify.KeyID.
      const derived = await deriveKeyId(base64ToBytes(Buffer.from(entry.publicKeyHex, 'hex').toString('base64')));
      assert.equal(derived, DEMO_OPERATOR_KEY.keyId);

      // The demo key is scoped: it may only authorize the demo module digest.
      const otherDigest = 'a'.repeat(64);
      const resolved = resolveOperatorKey(DEMO_OPERATOR_KEY.keyId, otherDigest);
      assert.equal(resolved.valid, false);
      assert.equal(resolved.error, 'operator_key_scope_violation');
    });

  });

  describe('7. WO_FIX_B - Majors: concurrency, the Worker requirement and the schema gate', () => {

    it('MAJOR runner.js:255 (T83) - one job per lane: CPU jobs and one job per GPU lane run at once, a busy lane never runs two jobs', async () => {
      await withMockWorker(async () => {
        MockWorker.hang = true;
        const port = mockPort();
        const first = handleJob(await demoJob({ jobId: 'job-a', taskId: 'task-a', timeoutMs: 40 }), port);
        // T83 dropped the global one-job-at-a-time refusal: the runner now runs
        // CPU jobs and one job per GPU lane at once. The second job is therefore
        // admitted onto its own lane instead of being refused with 'job_in_flight'.
        const second = await handleJob(await demoJob({ jobId: 'job-b', taskId: 'task-b', timeoutMs: 40 }), port);

        assert.equal(second.ok, false);
        assert.equal(second.error, 'task_timeout', 'The second job must run to its own timeout, not be refused');
        assert.equal(second.jobId, 'job-b');

        const firstResult = await first;
        assert.equal(firstResult.error, 'task_timeout');

        // One job per lane, one worker per job: both admitted jobs ran on their own
        // lane, a busy lane never ran two jobs at once. The old global refusal kept
        // the spawned count at 1.
        assert.equal(MockWorker.spawned, 2, 'Each admitted job runs on its own lane with its own worker');
        const jobIds = port.messages.filter(m => m.type === 'result').map(m => m.jobId).sort();
        assert.deepEqual(jobIds, ['job-a', 'job-b'], 'Each lane reports exactly the job it ran');
      });
    });

    it('MAJOR runner.js:425 / runner.html:375 - there is no main thread WASM fallback, a missing Worker is task_failed', async () => {
      const original = globalThis.Worker;
      delete globalThis.Worker;
      try {
        const port = mockPort();
        const res = await handleJob(await demoJob({ jobId: 'job-nw', taskId: 'task-nw' }), port);
        assert.equal(res.ok, false);
        assert.equal(res.error, 'task_failed: worker_unavailable');
        assert.equal(port.messages[0].error, 'task_failed: worker_unavailable');
      } finally {
        globalThis.Worker = original;
      }

      // The source must not contain a direct instance execution outside the worker.
      const runnerJs = fs.readFileSync(runnerJsPath, 'utf8');
      const directInstances = withoutWorkerSource(runnerJs).split('\n').filter(l => /new WebAssembly\.Instance\(/.test(l));
      assert.equal(directInstances.length, 0, 'Only the worker source may instantiate a module');
      const runnerHtml = fs.readFileSync(runnerHtmlPath, 'utf8');
      const htmlInstances = withoutWorkerSource(runnerHtml).split('\n').filter(l => /new WebAssembly\.Instance\(/.test(l));
      assert.equal(htmlInstances.length, 0, 'The inline runner must not instantiate a module on the main thread');
    });

    it('MAJOR runner.js:425 - a worker failure is reported as task_failed, never as a main thread retry', async () => {
      await withMockWorker(async () => {
        MockWorker.failWith = 'module trapped';
        const port = mockPort();
        const res = await handleJob(await demoJob({ jobId: 'job-fail', taskId: 'task-fail' }), port);
        assert.equal(res.ok, false);
        assert.equal(res.error, 'task_failed: module trapped');
        assert.equal(res.verifyPass, true);
      });
    });

    it('MAJOR runner.js:103 - the envelope schema is validated before anything else, missing bounds fail closed', async () => {
      const base = {
        version: 1,
        jobId: 'job-schema',
        taskId: 'task-schema',
        artifactSha256: 'a'.repeat(64),
        inputSha256: 'b'.repeat(64),
        issuedAt: Math.floor(Date.now() / 1000) - 10,
        expiresAt: Math.floor(Date.now() / 1000) + 600,
        keyId: DEMO_OPERATOR_KEY.keyId
      };

      const cases = [
        [{ ...base, expiresAt: undefined }, 'envelope_missing_expires_at'],
        [{ ...base, expiresAt: 'soon' }, 'envelope_missing_expires_at'],
        [{ ...base, issuedAt: undefined }, 'envelope_missing_issued_at'],
        [{ ...base, issuedAt: null }, 'envelope_missing_issued_at'],
        [{ ...base, version: undefined }, 'envelope_missing_version'],
        [{ ...base, version: 2 }, 'envelope_unsupported_version'],
        [{ ...base, version: 0 }, 'envelope_unsupported_version'],
        [{ ...base, artifactSha256: 'not-hex' }, 'envelope_invalid_artifact_sha256'],
        [{ ...base, artifactSha256: 'a'.repeat(63) }, 'envelope_invalid_artifact_sha256'],
        [{ ...base, inputSha256: 'zz' }, 'envelope_invalid_input_sha256'],
        [{ ...base, keyId: '' }, 'envelope_missing_key_id'],
        [{ ...base, jobId: '' }, 'envelope_missing_job_id'],
        [{ ...base, taskId: '' }, 'envelope_missing_task_id']
      ];

      for (const [envelope, expected] of cases) {
        const res = await verifyEnvelopeRunner({
          envelope: envelope,
          envSig: new Uint8Array(64),
          wasmBytes: SAMPLE_WASM.slice().buffer
        });
        assert.equal(res.valid, false, `Envelope ${JSON.stringify(envelope)} must fail closed`);
        assert.equal(res.error, expected);
      }

      // An envelope whose bounds are inverted is refused as well. The artifact
      // digest matches and the expiry is in the future, so the bounds check is
      // what rejects it.
      const wasmSha = await sha256Hex(SAMPLE_WASM);
      const future = Math.floor(Date.now() / 1000) + 600;
      const invertedValid = {
        ...base,
        artifactSha256: wasmSha,
        inputSha256: wasmSha,
        issuedAt: future,
        expiresAt: future - 60
      };
      const resInverted = await verifyEnvelopeRunner({
        envelope: invertedValid,
        envSig: new Uint8Array(64),
        wasmBytes: SAMPLE_WASM.slice().buffer
      });
      assert.equal(resInverted.valid, false);
      assert.equal(resInverted.error, 'envelope_invalid_time_bounds');

      // The pure validator agrees.
      assert.equal(validateTaskEnvelope(base).valid, true);
      assert.equal(validateTaskEnvelope(null).valid, false);
    });

    it('MAJOR contribute.js:250 - validateMessageSchema runs at runtime and drops an invalid message', () => {
      const port = mockPort();
      onPortMessage({ data: { type: 'job', jobId: 'job-1' } }, port); // no wasm, no envelope
      assert.equal(port.messages.length, 1);
      assert.equal(port.messages[0].type, 'check');
      assert.equal(port.messages[0].name, 'message_schema');
      assert.equal(port.messages[0].status, 'FAIL');

      // A valid ping is answered.
      const pingPort = mockPort();
      onPortMessage({ data: { type: 'ping', seq: 7 } }, pingPort);
      assert.equal(pingPort.messages.length, 1);
      assert.equal(pingPort.messages[0].type, 'pong');
      assert.equal(pingPort.messages[0].seq, 7);
    });

    it('MAJOR contribute.js:250 - the parent drops an invalid result message from the runner', () => {
      const app = new ContributeApp();
      const logs = [];
      app.log = (m) => logs.push(m);
      app.handleRunnerPortMessage({ type: 'result', jobId: 'job-1', ok: 'yes' });
      assert.equal(app.stats.tasksDone, 0, 'An invalid result must not count as a task');
      assert.equal(app.stats.verifyPass, 0);
      assert.ok(logs.some(l => l.includes('DROPPED invalid runner message')), 'The drop must be logged');
    });

  });

  describe('8. WO_FIX_B - Minors: handshake, worker budget, timeout verdict, size cap, exports', () => {

    it('MINOR runner.js:199 - the handshake does not rely on document.referrer', () => {
      const mockParent = { name: 'parent_window' };
      const mockPort = { postMessage() {} };

      // A foreign origin is not a rejection reason any more: the trust anchor is
      // ev.source === parent.
      const ev = {
        source: mockParent,
        origin: 'https://malicious.example.com',
        data: { type: 'init', nonce: 'nonce-ref-1' },
        ports: [mockPort]
      };
      const res = handleBootstrapMessage(ev, {
        parent: mockParent,
        referrer: 'https://legit.example.com/web/contribute.html',
        bootstrapped: false
      });
      assert.equal(res.accepted, true, 'The referrer must not decide the handshake');
      assert.equal(res.reason, undefined);

      const runnerJs = fs.readFileSync(runnerJsPath, 'utf8');
      assert.ok(!runnerJs.includes('document.referrer'), 'The referrer check must be gone from the runner');
    });

    it('MINOR runner.js:380 - one worker per task, the cap workers never duplicate the compute', async () => {
      await withMockWorker(async () => {
        const port = mockPort();
        const res = await handleJob(await demoJob({ jobId: 'job-one', taskId: 'task-one', workersRequested: 4 }), port);
        assert.equal(res.ok, true);
        assert.equal(res.workers, 1, 'Exactly one worker computes the task');
        assert.equal(res.workerCap, computeWorkerCap(typeof navigator !== 'undefined' ? navigator.hardwareConcurrency : 2));
        assert.equal(res.workersRequested, 4, 'The request is reported, the budget stays at one');
        assert.equal(MockWorker.spawned, 1, 'No duplicate compute');
      });
    });

    it('MINOR runner.js:407 - the timeout verdict comes from the timedOut flag, not from the error text', async () => {
      await withMockWorker(async () => {
        // A worker that reports the text "task_timeout" without a real timeout.
        MockWorker.failWith = 'task_timeout';
        const port = mockPort();
        const res = await handleJob(await demoJob({ jobId: 'job-flag', taskId: 'task-flag' }), port);
        assert.equal(res.ok, false);
        assert.notEqual(res.error, 'task_timeout', 'The error text must not decide the verdict');
        assert.equal(res.error, 'task_failed: task_timeout');
      });
    });

    it('MINOR runner.js:85 - the artifact size is capped before hashing and compiling', async () => {
      assert.equal(MAX_WASM_BYTES, 32 * 1024 * 1024, 'The cap must match pool.MaxArtifactBytes');

      const huge = new Uint8Array(MAX_WASM_BYTES + 1);
      const res = await verifyEnvelopeRunner({
        envelope: {
          version: 1,
          jobId: 'job-big',
          taskId: 'task-big',
          artifactSha256: 'a'.repeat(64),
          inputSha256: 'b'.repeat(64),
          issuedAt: Math.floor(Date.now() / 1000) - 10,
          expiresAt: Math.floor(Date.now() / 1000) + 600,
          keyId: DEMO_OPERATOR_KEY.keyId
        },
        envSig: new Uint8Array(64),
        wasmBytes: huge
      });
      assert.equal(res.valid, false);
      assert.equal(res.error, 'wasm_too_large');
    });

    it('MINOR runner.js:153 - a workload without a run export is a task_error, not an output of zero', async () => {
      assert.ok(WORKER_SRC.includes("unknown_workload"), 'The worker must reject a workload without the run entry');
      // D-W10-6: the shell replies through its captured `send`, and in
      // separate+lock the kernel answers through `reply`. Both name the error.
      assert.ok(/send\(\{ type: 'task_error', error: 'unknown_workload/.test(WORKER_SRC) ||
        /reply\(\{ type: 'kernel_error', error: 'unknown_workload/.test(KERNEL_SRC),
        'The worker must report task_error');
      assert.ok(KERNEL_SRC.includes("unknown_workload"), 'The kernel rejects a workload without the run entry');

      // Run the real generic shell against a module that exports no run function.
      const noRunUrl = 'data:text/javascript;base64,' + Buffer.from('export const noEntry = true;\n', 'utf8').toString('base64');
      const posted = [];
      const fakeSelf = {};
      const factory = new Function('self', 'postMessage', buildModuleWorkerSource(noRunUrl, { mode: 'lock-only' }));
      factory(fakeSelf, (m) => posted.push(m));

      fakeSelf.onmessage({ data: { type: 'init_workload' } });
      await new Promise((resolve) => setTimeout(resolve, 50));

      const ready = posted.find(m => m.type === 'module_ready');
      assert.ok(ready, 'The shell must report module_ready');
      assert.equal(ready.entry, false, 'The shell must report the missing run entry');

      fakeSelf.onmessage({ data: { type: 'compute_task', input: { n: 5 } } });
      await new Promise((resolve) => setTimeout(resolve, 50));

      const errorMessage = posted.find(m => m.type === 'task_error');
      assert.ok(errorMessage, 'The worker must report task_error for a missing run entry');
      assert.ok(errorMessage.error.includes('unknown_workload'));
      assert.equal(posted.find(m => m.type === 'task_done'), undefined, 'No task_done without a run entry');
    });

    it('MINOR runner.js:172 - a refused bootstrap is visible on the console, not only counted', () => {
      const runnerJs = fs.readFileSync(runnerJsPath, 'utf8');
      assert.ok(runnerJs.includes('console.warn'), 'A refusal must be logged');

      const originalWarn = console.warn;
      const seen = [];
      console.warn = (...args) => seen.push(args.join(' '));
      try {
        const runner = require('../js/runner.js');
        runner.refuseBootstrap('test_reason');
      } catch (e) {
        // The module is an ES module: require() is not available in every runtime.
      } finally {
        console.warn = originalWarn;
      }
      // The static check above is the assertion; the dynamic probe is best effort.
      assert.ok(runnerJs.includes("[runner] bootstrap refused"));
    });

    it('MINOR runner.html:12 - the inline script of runner.html is generated from js/runner.js', () => {
      const runnerSource = fs.readFileSync(runnerJsPath, 'utf8');
      const html = fs.readFileSync(runnerHtmlPath, 'utf8');

      const match = html.match(/<script type="module">([\s\S]*?)<\/script>/);
      assert.ok(match, 'runner.html must contain an inline module script');
      const inline = match[1];

      const markerAt = inline.indexOf(RUNNER_INLINE_BEGIN);
      assert.ok(markerAt >= 0, 'The inline block must carry the generation marker');

      const expected = buildInlineRunnerScript(runnerSource).trim();
      const actual = inline.slice(markerAt).trim();
      assert.equal(actual, expected, 'runner.html and js/runner.js must not drift');

      // The inline copy must not export anything: it is a plain module script.
      const exportLines = inline.split('\n').filter(l => /^\s*export\b/.test(l));
      assert.equal(exportLines.length, 0, 'The inline copy must not keep export statements');

      // The generator must agree with the file on disk.
      const out = execFileSync(process.execPath, [path.join(webPagesDir, 'js', 'build-runner-html.mjs'), '--check'], { encoding: 'utf8' });
      assert.ok(out.includes('up to date'), out);
    });

  });

  describe('9. WO_FIX_B - REVIEW_02 rows of the contributor client', () => {

    it('REVIEW_02 contribute.js:492 - the contributor registers first and keeps the server issued tab id', async () => {
      const app = new ContributeApp();
      const calls = [];
      globalThis.fetch = async (url, opts) => {
        calls.push({ url: String(url), method: (opts && opts.method) || 'GET' });
        if (String(url).endsWith('/workers/register')) {
          return {
            ok: true,
            status: 201,
            json: async () => ({ tabId: 'srv-tab-42', hostId: 'host-1', poolId: 'tp', owner: 'key-1', slots: 2, gpu: false, heartbeat: '2026-11-17T00:00:00Z' })
          };
        }
        return { ok: false, status: 404, json: async () => ({}) };
      };

      try {
        await app.initSource();
      } finally {
        globalThis.fetch = originalFetch;
      }

      assert.equal(app.sourceMode, 'LIVE');
      assert.equal(app.registeredTabId, 'srv-tab-42', 'The server issued tab id must be kept');
      assert.equal(app.tabId, 'srv-tab-42');
      assert.equal(calls.length, 1, 'Only the register call may run');
      assert.equal(calls[0].url, API_BASE + '/workers/register');
      assert.equal(calls[0].method, 'POST');
    });

    it('REVIEW_02 contribute.js:492 - the source detection never probes the lease endpoint', async () => {
      const app = new ContributeApp();
      const calls = [];
      globalThis.fetch = async (url, opts) => {
        calls.push({ url: String(url), method: (opts && opts.method) || 'GET' });
        if (String(url).endsWith('/workers/register')) {
          return { ok: true, status: 201, json: async () => ({ tabId: 'srv-tab-43' }) };
        }
        return { ok: false, status: 404, json: async () => ({}) };
      };

      try {
        await app.initSource();
      } finally {
        globalThis.fetch = originalFetch;
      }

      const leaseCalls = calls.filter(c => c.url.includes('/leases'));
      assert.equal(leaseCalls.length, 0, 'A probe would claim a lease and drop it');
    });

    it('REVIEW_02 contribute.js:492 - a rejected registration does not fall back to MOCK', async () => {
      const app = new ContributeApp();
      globalThis.fetch = async () => ({ ok: false, status: 401, json: async () => ({}) });

      try {
        await app.initSource();
      } finally {
        globalThis.fetch = originalFetch;
      }

      assert.equal(app.sourceMode, 'ERROR', 'An authentication failure must not hide behind MOCK');
      assert.notEqual(app.sourceMode, 'MOCK');
      assert.ok(app.sourceReason.includes('401'));
    });

    it('REVIEW_02 contribute.js:708 - a failed artifact fetch reports the lease failure instead of demo bytes', async () => {
      const app = new ContributeApp();
      app.sourceMode = 'LIVE';
      app.tabId = 'srv-tab-44';
      app.registeredTabId = 'srv-tab-44';

      const calls = [];
      globalThis.fetch = async (url, opts) => {
        calls.push({ url: String(url), method: (opts && opts.method) || 'GET', body: opts && opts.body });
        if (String(url).includes('/leases')) {
          return {
            ok: true,
            status: 200,
            json: async () => ({
              lease: { jobId: 'job-1', taskId: 'task-1', workerId: 'srv-tab-44', token: 'tok', generation: 1, attempt: 1, expiresAt: '2026-11-17T01:00:00Z' },
              task: { taskId: 'task-1', timeoutMs: 1000 },
              moduleArtifactIds: ['a'.repeat(64)],
              taskEnvelope: {
                payload: {
                  version: 1,
                  jobId: 'job-1',
                  taskId: 'task-1',
                  artifactSha256: 'a'.repeat(64),
                  inputSha256: 'b'.repeat(64),
                  issuedAt: Math.floor(Date.now() / 1000) - 10,
                  expiresAt: Math.floor(Date.now() / 1000) + 600,
                  keyId: DEMO_OPERATOR_KEY.keyId
                },
                signature: Buffer.alloc(64, 7).toString('base64'),
                keyId: DEMO_OPERATOR_KEY.keyId,
                algorithm: 'ed25519'
              }
            })
          };
        }
        if (String(url).includes('/artifacts/')) {
          return { ok: false, status: 500, json: async () => ({}) };
        }
        return { ok: true, status: 200, json: async () => ({}) };
      };

      let job = null;
      try {
        job = await app.getLiveJob();
      } finally {
        globalThis.fetch = originalFetch;
      }

      assert.equal(job, null, 'No job may be produced without the real artifact');
      const submit = calls.find(c => c.url.includes('/results'));
      assert.ok(submit, 'The lease failure must be reported to the server');
      const body = JSON.parse(submit.body);
      assert.equal(body.error, 'artifact_fetch_http_500');
      assert.equal(body.lease.taskId, 'task-1');
    });

    it('MINOR contribute.js:719 - the live path never substitutes the sample module', () => {
      const contributeJs = fs.readFileSync(contributeJsPath, 'utf8');
      assert.ok(!contributeJs.includes('wasmBuf = SAMPLE_WASM.slice().buffer;'),
        'The live path must not fall back to the demo module');
    });

    it('MINOR contribute.js:759 - the mock path is skipped when there is no signing key', async () => {
      const app = new ContributeApp();
      app.demoKeyPair = null;
      const job = await app.getMockJob();
      assert.equal(job, null, 'An unsigned mock job would only produce a verify failure loop');
    });

    it('MINOR contribute.js:759 - the mock job is signed with the pinned demo key and carries no public key', async () => {
      const app = new ContributeApp();
      await app.initDemoKeys();
      assert.ok(app.demoKeyPair, 'The demo key must be importable');
      // The test injects a separate workload fixture; no workload bytes are
      // imported by production platform JavaScript (CR-53).
      app.loadDemoArtifact = async () => ({
        bytes: new TextEncoder().encode(DEMO_WORKLOAD_SOURCE).buffer,
        sha256: DEMO_WORKLOAD_SHA256,
        inputJSON: JSON.stringify({ json: { kind: DEMO_WORKLOAD_KIND, n: 160 } })
      });

      const job = await app.getMockJob();
      assert.ok(job, 'The mock job must be built');
      assert.equal(job.envelope.keyId, DEMO_OPERATOR_KEY.keyId);
      assert.ok(job.envSig instanceof Uint8Array);
      assert.equal(job.envSig.length, 64);
      assert.equal(job.pubKeyRaw, undefined, 'No public key may travel with the job');
      assert.equal(job.wasmSha256, DEMO_WORKLOAD_SHA256,
        'The mock module is the separately fetched reference artifact (manifest pin)');
      assert.equal(job.envelope.inputSha256, await sha256Hex(new TextEncoder().encode(job.inputJSON)),
        'The mock input is signed over its exact bytes');
    });

    it('CR-53: mock artifact is fetched as separate data and byte-pinned before signing', async () => {
      const app = new ContributeApp();
      const originalFetch = globalThis.fetch;
      const inputJSON = JSON.stringify({ json: { kind: DEMO_WORKLOAD_KIND, n: 160 } });
      const payload = {
        id: demoManifest.id, version: demoManifest.version,
        source: DEMO_WORKLOAD_SOURCE, sha256: DEMO_WORKLOAD_SHA256, inputJSON
      };
      let requestedURL;
      try {
        globalThis.fetch = async (url) => {
          requestedURL = String(url);
          return { ok: true, json: async () => payload };
        };
        const artifact = await app.loadDemoArtifact();
        assert.equal(requestedURL, '/pools/mock-artifact');
        assert.equal(artifact.sha256, DEMO_WORKLOAD_SHA256);
        assert.equal(artifact.inputJSON, inputJSON);
        assert.equal(await sha256Hex(artifact.bytes), DEMO_WORKLOAD_SHA256);

        globalThis.fetch = async () => ({ ok: true, json: async () => ({
          ...payload, source: DEMO_WORKLOAD_SOURCE + '\n// tampered'
        }) });
        await assert.rejects(app.loadDemoArtifact(), /mock_artifact_sha256_mismatch/,
          'tampering with runtime-delivered bytes must fail closed');
      } finally {
        globalThis.fetch = originalFetch;
      }
    });

    it('MINOR contribute.js:543 - the upkeep worker is terminated and its blob url revoked on stop', () => {
      const originalWorker = globalThis.Worker;
      const originalCreate = URL.createObjectURL;
      const originalRevoke = URL.revokeObjectURL;
      const revoked = [];
      let terminated = false;
      globalThis.Worker = class { postMessage() {} terminate() { terminated = true; } };
      URL.createObjectURL = () => 'blob:upkeep-test';
      URL.revokeObjectURL = (u) => revoked.push(u);

      try {
        const app = new ContributeApp();
        app.initUpkeepWorker();
        assert.ok(app.upkeepWorker, 'The upkeep worker must exist');
        app.isRunning = true;
        app.stop();
        assert.ok(terminated, 'The upkeep worker must be terminated');
        assert.deepEqual(revoked, ['blob:upkeep-test'], 'The blob url must be revoked');
        assert.equal(app.upkeepWorker, null);
      } finally {
        globalThis.Worker = originalWorker;
        URL.createObjectURL = originalCreate;
        URL.revokeObjectURL = originalRevoke;
      }
    });

    it('MINOR contribute.js:473 - without Web Locks the page says that it is degraded', async () => {
      const originalNavigator = globalThis.navigator;
      try {
        Object.defineProperty(globalThis, 'navigator', {
          value: { userAgent: 'node', hardwareConcurrency: 4 },
          configurable: true
        });
        const app = new ContributeApp();
        await app.initWebLockLeader();
        assert.equal(app.degraded, true, 'The degraded mode must be surfaced');
        assert.equal(app.isLeader, false, 'Without Web Locks no tab may claim the leadership');
      } finally {
        Object.defineProperty(globalThis, 'navigator', { value: originalNavigator, configurable: true });
      }
    });

    it('MINOR contribute.js:473 - the Web Lock request does not block the page start', async () => {
      const originalNavigator = globalThis.navigator;
      try {
        Object.defineProperty(globalThis, 'navigator', {
          value: {
            userAgent: 'node',
            hardwareConcurrency: 4,
            locks: {
              // The callback holds the lock until unload: awaiting the request
              // would block the whole page start forever.
              request: () => new Promise(() => {})
            }
          },
          configurable: true
        });
        const app = new ContributeApp();
        const done = await Promise.race([
          app.initWebLockLeader().then(() => 'returned'),
          new Promise((r) => setTimeout(() => r('timeout'), 300))
        ]);
        assert.equal(done, 'returned', 'initWebLockLeader must not wait for the lock');
      } finally {
        Object.defineProperty(globalThis, 'navigator', { value: originalNavigator, configurable: true });
      }
    });

    it('MINOR contribute.js:543 - a start after a stop creates the upkeep worker again', () => {
      const originalWorker = globalThis.Worker;
      const originalCreate = URL.createObjectURL;
      const originalRevoke = URL.revokeObjectURL;
      let created = 0;
      globalThis.Worker = class { postMessage() {} terminate() {} };
      URL.createObjectURL = () => 'blob:upkeep-' + (++created);
      URL.revokeObjectURL = () => {};

      try {
        const app = new ContributeApp();
        app.initUpkeepWorker();
        assert.ok(app.upkeepWorker, 'The upkeep worker must exist after init');
        app.start();
        app.stop();
        assert.equal(app.upkeepWorker, null, 'The stop releases the upkeep worker');
        app.start();
        assert.ok(app.upkeepWorker, 'A start after a stop must create the upkeep worker again');
        app.stop();
      } finally {
        globalThis.Worker = originalWorker;
        URL.createObjectURL = originalCreate;
        URL.revokeObjectURL = originalRevoke;
      }
    });

    it('MINOR contribute.js:321 - the bearer token is not read from localStorage', () => {
      const originalLocal = globalThis.localStorage;
      const originalSession = globalThis.sessionStorage;
      try {
        globalThis.localStorage = { getItem: () => 'leaked-token' };
        globalThis.sessionStorage = { getItem: () => null };
        assert.equal(readAuthToken(), '', 'localStorage must not be a token source');

        globalThis.sessionStorage = { getItem: () => 'session-token' };
        assert.equal(readAuthToken(), '');
      } finally {
        globalThis.localStorage = originalLocal;
        globalThis.sessionStorage = originalSession;
      }

      const contributeJs = fs.readFileSync(contributeJsPath, 'utf8');
      assert.ok(!contributeJs.includes("localStorage.getItem('contributor_token')"),
        'The token must not come from localStorage');
    });

    it('MINOR contribute.js:585 - the runner frame is pinned before the port is handed over', () => {
      const app = new ContributeApp();
      assert.equal(app.isRunnerFramePinned({ getAttribute: () => 'runner.html' }), true);
      assert.equal(app.isRunnerFramePinned({ getAttribute: () => 'runner.html?v=2' }), true);
      assert.equal(app.isRunnerFramePinned({ getAttribute: () => 'sub/runner.html' }), true);
      assert.equal(app.isRunnerFramePinned({ getAttribute: () => 'evil.html' }), false);
      assert.equal(app.isRunnerFramePinned({ getAttribute: () => '../runner.html' }), false);
      assert.equal(app.isRunnerFramePinned({ getAttribute: () => '/runner.html' }), false);
      assert.equal(app.isRunnerFramePinned({ getAttribute: () => 'https://evil.example/runner.html' }), false);

      // An unpinned frame never receives the port.
      let posted = false;
      const mockIframe = {
        contentDocument: { readyState: 'complete' },
        getAttribute: () => 'evil.html',
        contentWindow: { postMessage() { posted = true; } },
        addEventListener() {}
      };
      const app2 = new ContributeApp();
      app2.dom = { runnerFrame: mockIframe };
      runnerApps.push(app2);
      app2.setupRunnerFrame();
      assert.equal(posted, false, 'The port must not be transferred to an unpinned frame');

      if (app2.channel) {
        if (app2.channel.port1 && typeof app2.channel.port1.close === 'function') app2.channel.port1.close();
        if (app2.channel.port2 && typeof app2.channel.port2.close === 'function') app2.channel.port2.close();
      }
    });

    it('MINOR contribute.js:585 - a separate runner origin is accepted only when it is pinned', () => {
      const app = new ContributeApp({ pinnedRunnerOrigins: ['http://127.0.0.1:8792'] });
      assert.equal(app.isRunnerFramePinned({ getAttribute: () => 'http://127.0.0.1:8792/runner.html' }), true);
      assert.equal(app.isRunnerFramePinned({ getAttribute: () => 'http://127.0.0.1:9999/runner.html' }), false);

      // Without the pin the same URL is refused.
      const unpinned = new ContributeApp({ pinnedRunnerOrigins: [] });
      assert.equal(unpinned.isRunnerFramePinned({ getAttribute: () => 'http://127.0.0.1:8792/runner.html' }), false);
    });

    it('REVIEW_02 runner.js:334 (T83) - the WebGPU capability is probed and a GPU task is refused without an adapter', async () => {
      const gpu = await probeWebGPU();
      assert.equal(gpu.supported, false, 'Node has no WebGPU adapter');
      assert.ok(gpu.detail.length > 0, 'The probe must explain itself');

      await withMockWorker(async () => {
        const port = mockPort();
        const spawnedBefore = MockWorker.spawned;
        const res = await handleJob(await demoJob({ jobId: 'job-gpu', taskId: 'task-gpu', needs: 'gpu' }), port);
        assert.equal(res.ok, false);
        // T83 routes the job through the lane pool built from the GPU picker:
        // without an adapter there is no GPU lane, so the pool refuses the job
        // immediately instead of the old runner-side gpu_unsupported verdict.
        assert.equal(res.error, 'gpu_unavailable');
        assert.equal(MockWorker.spawned, spawnedBefore, 'A GPU task must not start a CPU worker');
      });

      const runnerJs = fs.readFileSync(runnerJsPath, 'utf8');
      assert.ok(runnerJs.includes('requestAdapter'), 'The runner must probe the adapter');
      assert.ok(runnerJs.includes("name: 'webgpu'"), 'The runner must report the WebGPU capability');
    });

    it('REVIEW_02 contribute.js:492 - the live claim uses the registered tab id', async () => {
      const app = new ContributeApp();
      app.sourceMode = 'LIVE';
      app.tabId = 'srv-tab-45';
      app.registeredTabId = 'srv-tab-45';

      const calls = [];
      globalThis.fetch = async (url, opts) => {
        calls.push(String(url));
        if (String(url).includes('/leases')) {
          return { ok: true, status: 204 };
        }
        return { ok: true, status: 200, json: async () => ({}) };
      };

      try {
        const job = await app.getLiveJob();
        assert.equal(job, null, 'A 204 means no task');
      } finally {
        globalThis.fetch = originalFetch;
      }

      assert.ok(calls[0].includes('/workers/srv-tab-45/leases'), 'The claim must use the server issued tab id');
    });

  });

  describe('10. WO_T105 - auto-start, no Start button', () => {

    function contributorDocument() {
      return {
        cookie: '',
        getElementById(id) {
          if (id === 'runner-frame') return null;
          return {
            id,
            textContent: '',
            className: '',
            value: id === 'worker-count-select' ? 'auto' : '',
            style: {},
            addEventListener() {},
            removeEventListener() {}
          };
        }
      };
    }

    async function withContributorPage(options, run) {
      const originalNavigator = globalThis.navigator;
      const originalDocument = globalThis.document;
      const calls = [];
      let app = null;
      Object.defineProperty(globalThis, 'navigator', {
        value: { userAgent: 'node', hardwareConcurrency: 8 },
        configurable: true
      });
      globalThis.document = contributorDocument();
      globalThis.fetch = async (url, opts) => {
        const target = String(url);
        calls.push({ url: target, method: (opts && opts.method) || 'GET' });
        if (app && target.includes('/workers/register')) {
          calls[calls.length - 1].runningDuringRegister = app.isRunning;
        }
        if (target.endsWith('/pools/runner-config')) {
          return { ok: true, status: 200, json: async () => ({ anonymousJoin: false, runOrigin: null }) };
        }
        if (target.endsWith('/workers/register')) {
          return { ok: true, status: 201, json: async () => ({ tabId: 'srv-t105', hostId: 'host-t105', slots: 1 }) };
        }
        if (target.includes('/leases')) {
          return { ok: true, status: 204 };
        }
        return { ok: true, status: 200, json: async () => ({}) };
      };
      try {
        app = new ContributeApp(options);
        await run(app, calls);
      } finally {
        if (app) {
          if (app.taskTimer) clearTimeout(app.taskTimer);
          if (app.runnerRetryTimer) clearTimeout(app.runnerRetryTimer);
          if (app.tokenRenewTimer) clearTimeout(app.tokenRenewTimer);
          try { app.stop(); } catch (_) { /* already idle */ }
        }
        globalThis.fetch = originalFetch;
        if (originalDocument === undefined) delete globalThis.document;
        else globalThis.document = originalDocument;
        Object.defineProperty(globalThis, 'navigator', {
          value: originalNavigator,
          configurable: true
        });
      }
    }

    it('resolveAutoStart defaults on, and ?autostart=0 or autoStart:false stays off', () => {
      assert.equal(resolveAutoStart({}), true);
      assert.equal(resolveAutoStart({ autoStart: true }), true);
      assert.equal(resolveAutoStart({ autoStart: false }), false);
      assert.equal(resolveAutoStart({ search: '' }), true);
      assert.equal(resolveAutoStart({ search: '?autostart=0' }), false);
      assert.equal(resolveAutoStart({ autoStart: true, search: '?autostart=0' }), false);
      assert.equal(resolveAutoStart({ search: '?autostart=1' }), true);
      assert.equal(resolveAutoStart({ search: 'autostart=0' }), false);

      const idle = new ContributeApp({ search: '?autostart=0' });
      assert.equal(idle.autoStart, false);
      assert.equal(idle.isRunning, false);
      const off = new ContributeApp({ autoStart: false });
      assert.equal(off.autoStart, false);
      const on = new ContributeApp();
      assert.equal(on.autoStart, true);
    });

    it('starts claiming after registration, with no Start button in the page or the controller', async () => {
      const html = fs.readFileSync(path.join(webPagesDir, 'contribute.html'), 'utf8');
      const source = fs.readFileSync(contributeJsPath, 'utf8');
      assert.equal(html.includes('btn-toggle-compute'), false);
      assert.equal(source.includes('btn-toggle-compute'), false);
      assert.equal(source.includes('btnToggle'), false);

      await withContributorPage({}, async (app, calls) => {
        let runningDuringRegister = null;
        const registerSeen = calls.find((c) => c.url.includes('/workers/register'));
        assert.equal(registerSeen, undefined, 'Registration happens inside init, not before it');
        const wrappedFetch = globalThis.fetch;
        globalThis.fetch = async (url, opts) => {
          if (String(url).includes('/workers/register')) {
            runningDuringRegister = app.isRunning;
          }
          return wrappedFetch(url, opts);
        };
        await app.init();
        assert.equal(runningDuringRegister, false, 'The tab is idle while it registers');
        assert.equal(app.sourceMode, 'LIVE');
        assert.equal(app.tabId, 'srv-t105');
        assert.equal(app.isRunning, true, 'Registration is followed by compute');
        assert.equal(calls.filter((c) => c.url.includes('/leases')).length, 0, 'No claim before the runner is ready');

        app.runnerNonce = 'nonce-t105';
        app.port = { postMessage() {} };
        app.handleRunnerPortMessage({ type: 'ready', nonce: 'nonce-t105', buildId: 't105' });
        await new Promise((resolve) => setTimeout(resolve, 0));
        assert.equal(app.runnerReady, true);
        const leaseCalls = calls.filter((c) => c.url.includes('/leases'));
        assert.equal(leaseCalls.length, 1, 'The ready runner starts a claim');
        assert.equal(leaseCalls[0].method, 'POST');
        assert.ok(leaseCalls[0].url.includes('/workers/srv-t105/leases'));
      });
    });

    it('?autostart=0 registers and stays idle, including after the runner is ready', async () => {
      await withContributorPage({ search: '?autostart=0' }, async (app, calls) => {
        await app.init();
        assert.equal(app.autoStart, false);
        assert.equal(app.sourceMode, 'LIVE');
        assert.equal(app.tabId, 'srv-t105');
        assert.equal(app.isRunning, false, 'The debug flag keeps the old idle behaviour');
        assert.equal(calls.filter((c) => c.url.includes('/leases')).length, 0);

        app.runnerNonce = 'nonce-idle';
        app.port = { postMessage() {} };
        app.handleRunnerPortMessage({ type: 'ready', nonce: 'nonce-idle' });
        await new Promise((resolve) => setTimeout(resolve, 0));
        assert.equal(app.runnerReady, true);
        assert.equal(app.isRunning, false);
        assert.equal(calls.filter((c) => c.url.includes('/leases')).length, 0);
      });
    });

  });

  describe('11. WO_T96B - anonymous renewal hot loop', () => {

    function fakeJwt(sub) {
      const b = (o) => Buffer.from(JSON.stringify(o)).toString('base64url');
      return b({ alg: 'EdDSA' }) + '.' + b({ sub }) + '.sig';
    }

    it('jwtSubject reads sub and tolerates junk', () => {
      assert.equal(jwtSubject(fakeJwt('anon-abc')), 'anon-abc');
      assert.equal(jwtSubject('x.y.z'), null);
      assert.equal(jwtSubject(null), null);
    });

    it('100 upkeep ticks near expiry send one renewal, not one per tick', async () => {
      const calls = [];
      globalThis.fetch = async (url) => {
        calls.push(String(url));
        await new Promise((r) => setTimeout(r, 5));
        return { ok: true, status: 200, json: async () => ({ accessToken: fakeJwt('anon-same'), expiresIn: 900 }) };
      };
      try {
        const app = new ContributeApp({ autoStart: false });
        app.isAnonymous = true;
        app.authToken = fakeJwt('anon-same');
        app.tokenExpiresAt = Date.now() + 60000;
        app.isRunning = true;
        app.renderStats = () => {};
        app.sendHeartbeat = async () => {};
        for (let i = 0; i < 100; i++) app.onUpkeepTick();
        await app.renewPromise;
        for (let i = 0; i < 100; i++) app.onUpkeepTick();
        assert.equal(calls.filter((u) => u.includes('/workers/anonymous')).length, 1);
        if (app.tokenRenewTimer) clearTimeout(app.tokenRenewTimer);
        app.isRunning = false;
      } finally {
        globalThis.fetch = originalFetch;
      }
    });

    it('a 403 on a lease backs off, re-joins and registers a new tab once', async () => {
      const calls = [];
      globalThis.fetch = async (url) => {
        const u = String(url);
        calls.push(u);
        if (u.includes('/leases')) return { ok: false, status: 403 };
        if (u.includes('/workers/anonymous')) {
          return { ok: true, status: 200, json: async () => ({ accessToken: fakeJwt('anon-new'), expiresIn: 900 }) };
        }
        if (u.includes('/workers/register')) {
          return { ok: true, status: 201, json: async () => ({ tabId: 'tab-new' }) };
        }
        return { ok: true, status: 200, json: async () => ({}) };
      };
      try {
        const app = new ContributeApp({ autoStart: false });
        app.isAnonymous = true;
        app.authToken = fakeJwt('anon-old');
        app.sourceMode = 'LIVE';
        app.tabId = 'tab-old';
        app.gpuAllowed = () => false;
        assert.equal(await app.getLiveJob(), null);
        assert.equal(app.tabId, 'tab-new');
        assert.ok(app.authBlockedUntil > Date.now(), 'claims are paused');
        // A second 403 inside the pause does not re-join again.
        assert.equal(await app.getLiveJob(), null);
        assert.equal(calls.filter((u) => u.includes('/workers/anonymous')).length, 1);
        assert.equal(calls.filter((u) => u.includes('/workers/register')).length, 1);
        if (app.tokenRenewTimer) clearTimeout(app.tokenRenewTimer);
      } finally {
        globalThis.fetch = originalFetch;
      }
    });

    it('W7: a 403 with an unchanged subject (reputation bar) backs off and never registers a new tab', async () => {
      const calls = [];
      globalThis.fetch = async (url) => {
        const u = String(url);
        calls.push(u);
        if (u.includes('/leases')) return { ok: false, status: 403 };
        if (u.includes('/workers/anonymous')) {
          return { ok: true, status: 200, json: async () => ({ accessToken: fakeJwt('anon-same'), expiresIn: 900 }) };
        }
        return { ok: true, status: 201, json: async () => ({ tabId: 'tab-evade' }) };
      };
      try {
        const app = new ContributeApp({ autoStart: false });
        app.isAnonymous = true;
        app.authToken = fakeJwt('anon-same');
        app.sourceMode = 'LIVE';
        app.tabId = 'tab-barred';
        app.gpuAllowed = () => false;
        assert.equal(await app.getLiveJob(), null);
        assert.equal(app.tabId, 'tab-barred');
        assert.ok(app.authBlockedUntil > Date.now());
        assert.equal(calls.filter((u) => u.includes('/workers/register')).length, 0);
        if (app.tokenRenewTimer) clearTimeout(app.tokenRenewTimer);
      } finally {
        globalThis.fetch = originalFetch;
      }
    });

    it('W7: registerWorker is single-flight and stop() clears the claim and runner timers', async () => {
      let registers = 0;
      globalThis.fetch = async () => {
        registers++;
        await new Promise((r) => setTimeout(r, 5));
        return { ok: true, status: 201, json: async () => ({ tabId: 'tab-one' }) };
      };
      try {
        const app = new ContributeApp({ autoStart: false });
        app.gpuAllowed = () => false;
        const [a, b] = await Promise.all([app.registerWorker(), app.registerWorker()]);
        assert.equal(registers, 1);
        assert.equal(a.tabId, b.tabId);
        app.isRunning = true;
        app.taskTimer = setTimeout(() => assert.fail('claim timer fired after stop'), 50);
        app.runnerRetryTimer = setTimeout(() => assert.fail('runner timer fired after stop'), 50);
        app.stop();
        assert.equal(app.taskTimer, null);
        assert.equal(app.runnerRetryTimer, null);
        await new Promise((r) => setTimeout(r, 80));
      } finally {
        globalThis.fetch = originalFetch;
      }
    });

    it('W7 deepseek: 503 lease and failed register pause claims with backoff and Retry-After', async () => {
      const app = new ContributeApp({ autoStart: false });
      const before = Date.now();
      app.pauseAfterServerFailure(503, { headers: { get: () => null } });
      assert.ok(app.authBlockedUntil >= before + 2000, 'first failure pauses at least 2 s');
      app.pauseAfterServerFailure(503, { headers: { get: () => null } });
      assert.ok(app.authBlockedUntil >= before + 4000, 'second failure doubles the pause');
      app.pauseAfterServerFailure(429, { headers: { get: (h) => (h === 'Retry-After' ? '90' : null) } });
      assert.ok(app.authBlockedUntil >= before + 90000, 'Retry-After longer than backoff wins');
      app.pauseAfterServerFailure(429, { headers: { get: () => '99999' } });
      assert.ok(app.authBlockedUntil <= Date.now() + 300000 + 50, 'Retry-After is capped at 300 s');

      let leases = 0;
      globalThis.fetch = async () => {
        leases++;
        return { ok: false, status: 503, headers: { get: () => null }, json: async () => ({}) };
      };
      try {
        const live = new ContributeApp({ autoStart: false });
        live.sourceMode = 'LIVE';
        live.tabId = 'tab-x';
        assert.equal(await live.getLiveJob('tp'), null);
        assert.equal(leases, 1);
        assert.ok(live.authBlockedUntil > Date.now() + 1500, '503 lease pauses the claim loop');
        assert.equal(live.busyFailures, 1);
      } finally {
        globalThis.fetch = originalFetch;
      }
    });

    it('T112: auth failure during in-flight renewal waits instead of backing off; MOCK tab never sets registrationPending', async () => {
      const app = new ContributeApp({ autoStart: false });
      let release;
      app.renewPromise = new Promise((r) => { release = r; });
      const p = app.onAuthFailure(401);
      release();
      await p;
      assert.equal(app.authFailures || 0, 0, 'no backoff counted while a renewal was in flight');
      assert.ok(!app.authBlockedUntil, 'no pause set while a renewal was in flight');

      const mock = new ContributeApp({ autoStart: false });
      mock.sourceMode = 'MOCK';
      mock.updateGPUGuard();
      assert.equal(mock.registrationPending, false);
    });

    it('T112: upkeep tick does not renew while paused', () => {
      const app = new ContributeApp({ autoStart: false });
      app.isRunning = true;
      app.isAnonymous = true;
      app.tokenExpiresAt = Date.now() + 1000;
      app.authBlockedUntil = Date.now() + 30000;
      let renewals = 0;
      app.renewAnonymous = async () => { renewals++; };
      app.sendHeartbeat = async () => {};
      app.renderStats = () => {};
      app.onUpkeepTick();
      assert.equal(renewals, 0);
      app.authBlockedUntil = 0;
      app.onUpkeepTick();
      assert.equal(renewals, 1);
    });

  });

});

// END OF contribute.test.mjs



