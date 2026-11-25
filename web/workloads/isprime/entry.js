// F14 (2026-11-24). WORKLOAD-owned; self-contained for the Blob module ABI.
// Legacy WASM: cpp_src/prime_worker.cpp + prime_library.h, unchanged artifact.
// uint64 MR bases: Forisek/Jancina, CEUR Vol-1326/020-Forisek.pdf, Theorem 3.
const MAX_U64 = (1n << 64n) - 1n;
const BASES = [2n,325n,9375n,28178n,450775n,9780504n,1795265022n];
const SMALL = [2n,3n,5n,7n,11n,13n,17n,19n,23n,29n,31n,37n];

function integer(value) {
  if (typeof value === 'number') {
    if (!Number.isSafeInteger(value) || value < 0 || Object.is(value,-0)) throw Error('invalid_integer: use canonical decimal strings for uint64');
    return BigInt(value);
  }
  if (typeof value !== 'string' || value.length > 20 || !/^(0|[1-9][0-9]*)$/.test(value)) throw Error('invalid_integer');
  const n = BigInt(value);
  if (n > MAX_U64) throw Error('uint64_overflow');
  return n;
}

// Runner cancellation is worker termination; direct callers may also pass a signal.
// timeoutMs is the existing runner field; deadlineMs is optional Unix epoch ms.
function guard(ctx) {
  const budget = ctx.timeoutMs === undefined ? 10000 : ctx.timeoutMs;
  if (!Number.isFinite(budget) || budget <= 0) throw Error('invalid_timeout');
  if (ctx.deadlineMs !== undefined && !Number.isFinite(ctx.deadlineMs)) throw Error('invalid_deadline');
  const start = performance.now();
  const check = () => {
    if (ctx.signal?.aborted) throw Error('task_cancelled');
    if (performance.now()-start >= Math.min(budget,10000) || Date.now() >= (ctx.deadlineMs ?? Infinity)) throw Error('task_deadline');
  };
  check();
  return { check, async yield() { check(); await new Promise(resolve=>setTimeout(resolve,0)); check(); } };
}

function powmod(a,d,n) {
  let r = 1n;
  while (d > 0n) { if (d & 1n) r = r*a%n; a = a*a%n; d >>= 1n; }
  return r;
}

function prime64(n,check) {
  if (n < 2n) return 0;
  for (const p of SMALL) { if (n === p) return 1; if (n%p === 0n) return 0; }
  let d = n-1n, s = 0;
  while ((d & 1n) === 0n) { d >>= 1n; s++; }
  for (const base of BASES) {
    check();
    const a = base%n;
    if (a === 0n) continue;
    let x = powmod(a,d,n);
    if (x === 1n || x === n-1n) continue;
    let passed = false;
    for (let r=1; r<s; r++) { x = x*x%n; if (x === n-1n) { passed=true; break; } }
    if (!passed) return 0;
  }
  return 1;
}

export async function run(input,ctx={}) {
  const stop = guard(ctx);
  if (!input || typeof input !== 'object' || Array.isArray(input) || Object.keys(input).some(k=>!['values','start','end'].includes(k))) throw Error('invalid_input');
  let values;
  if (Object.hasOwn(input,'values')) {
    if (Object.hasOwn(input,'start') || Object.hasOwn(input,'end') || !Array.isArray(input.values) || input.values.length < 1 || input.values.length > 1024) throw Error('invalid_values');
    // Array.from rejects sparse slots too, instead of silently emitting nulls.
    values = Array.from(input.values,integer);
  } else {
    const start = integer(input.start), end = integer(input.end);
    if (end < start || end-start > 1024n) throw Error('invalid_range');
    values = Array.from({length:Number(end-start)},(_,i)=>start+BigInt(i));
  }
  let kernel = null;
  const raw = ctx.files?.['prime_library.wasm'];
  if (raw !== undefined) {
    if (!(raw instanceof ArrayBuffer || raw instanceof Uint8Array) || raw.byteLength !== 243) throw Error('invalid_prime_wasm');
    const {instance} = await WebAssembly.instantiate(raw,{});
    kernel = instance.exports.c;
    if (typeof kernel !== 'function' || instance.exports.a.buffer.byteLength > 32*1024*1024) throw Error('invalid_prime_wasm_abi');
    instance.exports.b();
  }
  stop.check();
  const prime = [];
  for (let i=0; i<values.length; i++) {
    if (i%32 === 0) await stop.yield();
    const n = values[i];
    // Legacy trial division cannot be CPU-bounded at uint64 scale. A singleton
    // <= 1e10 needs at most ~16667 divisor pairs; larger inputs always use MR64.
    prime.push(kernel && n <= 10000000000n ? Number(kernel(n,n)) : prime64(n,stop.check));
  }
  stop.check();
  const result = {v:1,kind:'isprime',values:values.map(String),prime};
  return {output:{bytes:new TextEncoder().encode(JSON.stringify(result)),format:'ccgsc-prime-json-v1'},
    meta:{backend:kernel?'wasm-trial+js-mr64':'js-mr64',range:'half-open',maxItems:1024}};
}
