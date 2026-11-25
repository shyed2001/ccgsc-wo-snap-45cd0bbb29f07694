// Finish Dash 2026-11-24: actual contributor path, both pools, fake fetch only.
import test from 'node:test';
import assert from 'node:assert/strict';
import { ContributeApp, POOL_BASES, sha256Hex } from '../js/contribute.js';

const good = new TextEncoder().encode('export function run() { return { output: null }; }');
const digest = await sha256Hex(good);
const bad = new TextEncoder().encode('corrupt cache');

async function claim(pool, hit, serverBytes = good, cachePut = async () => {}) {
  const originalFetch = globalThis.fetch;
  const calls = [];
  const app = new ContributeApp({ authToken: 'test-token', autoStart: false,
    artifactCache: { get: async () => { if (hit instanceof Error) throw hit; return hit; }, put: cachePut } });
  app.sourceMode = 'LIVE';
  app.tabIds = { [pool]: 'tab-test' };
  app.tabId = 'tab-test';
  const envelope = { version: 1, jobId: 'job-test', taskId: 'task-test',
    artifactSha256: digest, inputSha256: 'b'.repeat(64),
    issuedAt: 1, expiresAt: 4102444800, keyId: '0123456789abcdef' };
  globalThis.fetch = async (url, init = {}) => {
    calls.push({ url: String(url), init });
    if (String(url).endsWith('/leases')) return { ok: true, status: 200, json: async () => ({
      lease: { jobId: 'job-test', taskId: 'task-test', generation: 1, token: 'lease-test' },
      taskEnvelope: { payload: JSON.stringify(envelope), signature: 'c2ln' },
      moduleArtifactIds: [digest], task: { needs: 'cpu', timeoutMs: 30000 }, inputJSON: '{}'
    }) };
    if (String(url).includes('/artifacts/')) return { ok: true, status: 200, arrayBuffer: async () => serverBytes.slice().buffer };
    if (String(url).endsWith('/results')) return { ok: true, status: 200 };
    throw new Error('unexpected route: ' + url);
  };
  try { return { job: await app.getLiveJob(pool), calls }; }
  finally { globalThis.fetch = originalFetch; }
}

for (const pool of ['tp', 'pp']) {
  for (const [name, makeHit] of [
    ['corrupt', () => bad.slice().buffer],
    ['empty', () => new ArrayBuffer(0)],
    ['oversized', () => new ArrayBuffer(32 * 1024 * 1024 + 1)],
    ['unavailable', () => new Error('cache offline')],
    ['fake bytes', () => ({ byteLength: 4 })]
  ]) {
    test(`${pool}: ${name} cache falls back once to authenticated server artifact`, async () => {
      const { job, calls } = await claim(pool, makeHit());
      assert.ok(job, 'cache failure must not lose a valid lease');
      const fetches = calls.filter(c => c.url.includes('/artifacts/'));
      assert.equal(fetches.length, 1);
      assert.equal(fetches[0].url, `${POOL_BASES[pool]}/artifacts/${digest}`);
      assert.equal(fetches[0].init.headers.Authorization, 'Bearer test-token');
      assert.equal(calls.some(c => c.url.endsWith('/results')), false);
    });
  }
  test(`${pool}: genuine cache avoids server fetch`, async () => {
    const { job, calls } = await claim(pool, good.slice().buffer);
    assert.ok(job);
    assert.equal(calls.some(c => c.url.includes('/artifacts/')), false);
  });
  test(`${pool}: corrupt server fallback still fails closed and releases lease`, async () => {
    const { job, calls } = await claim(pool, bad.slice().buffer, bad);
    assert.equal(job, null);
    assert.equal(calls.filter(c => c.url.includes('/artifacts/')).length, 1);
    const failure = calls.find(c => c.url.endsWith('/results'));
    assert.ok(failure);
    assert.equal(JSON.parse(failure.init.body).error, 'artifact_digest_mismatch');
  });
  test(`${pool}: cache write failure does not prevent verified server execution`, async () => {
    const { job } = await claim(pool, null, good, async () => { throw new Error('full'); });
    assert.ok(job);
  });
}
