// F14 (2026-11-24). WORKLOAD-owned port of segment30_wheel.cpp/.hpp.
// Mod-30 candidate mapping and tiny-prime adjustment retained; [start,end).
// No imports: each entry can be uploaded and executed independently as a Blob.
const RESIDUES = [1,7,11,13,17,19,23,29];
const SLOT = Array.from({length:30},(_,r)=>RESIDUES.indexOf(r));

function integer(value) {
  if (typeof value === 'number') {
    if (!Number.isSafeInteger(value) || value < 0 || Object.is(value,-0)) throw Error('invalid_integer');
  } else {
    if (typeof value !== 'string' || value.length > 11 || !/^(0|[1-9][0-9]*)$/.test(value)) throw Error('invalid_integer');
    value = Number(value);
  }
  if (value > 10000000000) throw Error('end_limit');
  return value;
}

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
  return {check,async yield() { check(); await new Promise(resolve=>setTimeout(resolve,0)); check(); }};
}

async function basePrimes(limit,stop) {
  const composite = new Uint8Array(limit+1), primes = [];
  for (let p=2;p<=limit;p++) {
    if (p%8192 === 0) await stop.yield();
    if (composite[p]) continue;
    primes.push(p);
    for (let m=p*p;m<=limit;m+=p) composite[m]=1;
  }
  return Uint32Array.from(primes);
}

function wheelCount(lo,hi,base,stop) {
  if (hi < lo) return 0;
  const origin = Math.floor(lo/30)*30;
  const bits = new Uint8Array(Math.floor((hi-origin)/30)+1);
  for (const p of base) {
    if (p < 7) continue;
    if (p*p > hi) break;
    stop.check();
    for (let m=Math.max(p*p,Math.ceil(lo/p)*p);m<=hi;m+=p) {
      const slot = SLOT[m%30];
      if (slot >= 0) bits[Math.floor((m-origin)/30)] |= 1<<slot;
    }
  }
  let count=0;
  for (const p of [2,3,5]) if (lo<=p && p<=hi) count++;
  for (let block=0;block<bits.length;block++) {
    if (block%256 === 0) stop.check();
    for (let i=0;i<8;i++) {
      const n=origin+block*30+RESIDUES[i];
      if (n>=Math.max(2,lo) && n<=hi && !(bits[block] & (1<<i))) count++;
    }
  }
  return count;
}

export async function run(input,ctx={}) {
  const stop=guard(ctx);
  if (!input || typeof input!=='object' || Array.isArray(input) || Object.keys(input).some(k=>!['start','end','segmentSize'].includes(k))) throw Error('invalid_input');
  const start=integer(input.start),end=integer(input.end);
  const size=input.segmentSize ?? 65536;
  if (end<start || end-start>2000000) throw Error('range_limit');
  if (!Number.isInteger(size) || size<1 || size>65536) throw Error('segment_limit');
  // A tiny segment over a huge span must not create unbounded yield/scan work.
  if (Math.ceil((end-start)/size)>4096) throw Error('segment_count_limit');
  let count=0,ex=null,ptr=0;
  const base=await basePrimes(Math.floor(Math.sqrt(Math.max(0,end-1))),stop);
  const raw=ctx.files?.['segment.wasm'];
  if (raw!==undefined) {
    if (!(raw instanceof ArrayBuffer || raw instanceof Uint8Array) || raw.byteLength!==7771) throw Error('invalid_segment_wasm');
    // Original Emscripten imports: abort + resize_heap. Growth is refused;
    // fixed input limits fit the existing 258-page (~16.13 MiB) memory.
    const {instance}=await WebAssembly.instantiate(raw,{a:{a:()=>{throw Error('wasm_abort');},b:()=>0}});
    ex=instance.exports;
    if (!ex.c || ex.c.buffer.byteLength>32*1024*1024 || !['d','e','f','g'].every(k=>typeof ex[k]==='function')) throw Error('invalid_segment_wasm_abi');
    ex.d();
    ptr=ex.f(Math.max(4,base.byteLength));
    if (!ptr) throw Error('wasm_allocation');
    new Uint32Array(ex.c.buffer,ptr,base.length).set(base);
  }
  try {
    // Clamp to 2: the original wheel counts candidate 1 unless caller excludes it.
    for (let lo=Math.max(2,start);lo<end;lo+=size) {
      await stop.yield();
      const hi=Math.min(end,lo+size)-1;
      count+=ex ? ex.e(lo,hi,ptr,base.length) : wheelCount(lo,hi,base,stop);
    }
    stop.check();
  } finally { if (ptr) ex.g(ptr); }
  const result={v:1,kind:'ssprime',start:String(start),end:String(end),count};
  return {output:{bytes:new TextEncoder().encode(JSON.stringify(result)),format:'ccgsc-prime-json-v1'},
    meta:{backend:ex?'wasm-wheel30':'js-wheel30',range:'half-open',segmentSize:size}};
}
