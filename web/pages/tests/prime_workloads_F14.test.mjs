// F14, 2026-11-24: workload correctness uses independent trial division.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { createHash } from 'node:crypto';
import { run as isprime } from '../../workloads/isprime/entry.js';
import { run as ssprime } from '../../workloads/ssprime/entry.js';

const hash = b => createHash('sha256').update(b).digest('hex');
const decode = r => JSON.parse(new TextDecoder().decode(r.output.bytes));
const wasm = name => ({ files: { [name === 'isprime' ? 'prime_library.wasm' : 'segment.wasm']:
  fs.readFileSync(new URL(`../../workloads/${name}/${name === 'isprime' ? 'prime_library.wasm' : 'segment.wasm'}`, import.meta.url)) } });
// Deliberately no wheel, sieve, Miller-Rabin or workload helper in the reference.
function trial(n) {
  if (n < 2) return 0;
  for (let d = 2; d * d <= n; d++) if (n % d === 0) return 0;
  return 1;
}
function referenceCount(start, end) { let count = 0; for (let n = start; n < end; n++) count += trial(n); return count; }

test('F14 IsPrime: independent exhaustive reference and WASM parity', async () => {
  for (let start = 0; start < 2048; start += 256) {
    const values = Array.from({ length: 256 }, (_, i) => start + i);
    const expected = { v: 1, kind: 'isprime', values: values.map(String), prime: values.map(trial) };
    for (const ctx of [{}, wasm('isprime')]) {
      const r = await isprime({ values }, ctx);
      assert.deepEqual(decode(r), expected);
      assert.equal(hash(r.output.bytes), hash(Buffer.from(JSON.stringify(expected))));
      assert.equal(r.meta.backend, ctx.files ? 'wasm-trial+js-mr64' : 'js-mr64');
    }
  }
});

test('F14 IsPrime: uint64, pseudoprimes, Carmichael and exact decimal wire', async () => {
  const values = ['1','2','3','561','1105','1729','2465','2821','6601','3215031751',
    '341550071728321','3825123056546413051','18446744073709551615','18446744073709551557',
    '2305843009213693951','9007199254740993'];
  for (const ctx of [{}, wasm('isprime')]) {
    assert.deepEqual(decode(await isprime({ values }, ctx)).prime,
      [0,1,1,0,0,0,0,0,0,0,0,0,0,1,1,0]);
  }
  assert.deepEqual(decode(await isprime({ start: 0, end: 6 })).prime, [0,0,1,1,0,1]);
  assert.deepEqual(decode(await isprime({ start: 2, end: 2 })).prime, []);
});

test('F14 SSPrime: tiny wheel boundaries, offsets, empty and high ranges', async () => {
  const ranges = [[0,0],[0,1],[1,2],[0,6],[2,3],[3,4],[4,5],[5,6],[6,7],[28,62],
    [48,50],[120,123],[65520,65560],[9999999000,10000000000]];
  for (let start = 0; start < 90; start++) ranges.push([start, start + 47]);
  for (const [start,end] of ranges) {
    const expected = { v: 1, kind: 'ssprime', start: String(start), end: String(end), count: referenceCount(start,end) };
    for (const ctx of [{}, wasm('ssprime')]) {
      const r = await ssprime({ start, end, segmentSize: 31 }, ctx);
      assert.deepEqual(decode(r), expected);
      assert.equal(hash(r.output.bytes), hash(Buffer.from(JSON.stringify(expected))));
      assert.equal(r.meta.backend, ctx.files ? 'wasm-wheel30' : 'js-wheel30');
    }
  }
});

test('F14 SSPrime: known pi(10^n), segment partition invariance', async () => {
  for (const [n,count] of [[10,4],[100,25],[1000,168],[10000,1229],[100000,9592],[1000000,78498]]) {
    for (const ctx of [{}, wasm('ssprime')]) assert.equal(decode(await ssprime({ start: 0, end: n + 1 }, ctx)).count, count);
  }
  const a = await ssprime({ start: 0, end: 10001, segmentSize: 30 });
  const b = await ssprime({ start: '0', end: '10001', segmentSize: 65536 });
  assert.deepEqual(a.output.bytes, b.output.bytes);
});

test('F14 rejects malformed, rounded, negative and unbounded inputs', async () => {
  for (const n of [-1,-0,1.5,NaN,Infinity,9007199254740992,9007199254740993,'-1','01',' 2','2.0','1e3','18446744073709551616',true,null,{},2n]) {
    await assert.rejects(isprime({ values: [n] }));
    await assert.rejects(ssprime({ start: n, end: 100 }));
  }
  for (const x of [null,[],{}, {values: []}, {values: Array(1025).fill(2)}, {values:[2],start:0,end:2},
    {start:3,end:2}, {start:0,end:1025}, {values:[2], typo:1}]) await assert.rejects(isprime(x));
  for (const x of [null,[],{}, {start:2,end:1}, {start:0,end:2000001}, {start:9999999999,end:10000000001},
    {start:0,end:10,segmentSize:0}, {start:0,end:10,segmentSize:65537}, {start:0,end:10,segmentSize:1.1},
    {start:0,end:10,typo:1}]) await assert.rejects(ssprime(x));
});

test('F14 cancellation/deadline: preflight and during CPU work', async () => {
  for (const [run,input] of [[isprime,{values:[2]}],[ssprime,{start:0,end:10}]]) {
    await assert.rejects(run(input,{signal:{aborted:true}}), /cancelled/);
    await assert.rejects(run(input,{deadlineMs:Date.now()-1}), /deadline/);
    await assert.rejects(run(input,{timeoutMs:0}), /timeout/);
  }
  for (const [run,input] of [[isprime,{values:Array(1024).fill('18446744073709551557')}],
    [ssprime,{start:9998000000,end:10000000000}]]) {
    const controller = new AbortController();
    const timer = setTimeout(()=>controller.abort(),0);
    await assert.rejects(run(input,{signal:controller.signal}), /cancelled/);
    clearTimeout(timer);
    await assert.rejects(run(input,{timeoutMs:0.01}), /deadline/);
  }
});

test('F14 manifest pins and invalid companion fail closed', async () => {
  for (const [name,run,input,file] of [['isprime',isprime,{values:[2]},'prime_library.wasm'],['ssprime',ssprime,{start:0,end:10},'segment.wasm']]) {
    const manifest = JSON.parse(fs.readFileSync(new URL(`../../workloads/${name}/manifest.json`,import.meta.url)));
    for (const f of manifest.files) assert.equal(hash(fs.readFileSync(new URL(`../../workloads/${name}/${f.path}`,import.meta.url))), f.sha256);
    await assert.rejects(run(input,{files:{[file]:new Uint8Array([0,1,2])}}));
  }
});
