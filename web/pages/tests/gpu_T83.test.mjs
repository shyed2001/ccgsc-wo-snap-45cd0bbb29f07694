import test from 'node:test';
import assert from 'node:assert/strict';
import { createGPUPicker, createLanePool } from '../js/gpu_picker_T83.js';
import { ContributeApp } from '../js/contribute.js';
const adapter = (device, description) => ({ info: {vendor:'v', architecture:'a', device, description}, requestDevice: async () => ({destroy(){}}) });
const fake = () => { const a=adapter('1','integrated'), b=adapter('2','discrete'); return {requestAdapter: async o => o?.powerPreference === 'low-power' ? b : a}; };
test('picker deduplicates identity and sorts discrete first', async () => {
  const list=await createGPUPicker().enumerate(fake());
  assert.equal(list.length,2); assert.equal(list[0].preference,'low-power');
  const a=adapter('1',''); assert.equal((await createGPUPicker().enumerate({requestAdapter:async()=>a})).length,1);
});
test('two GPU lanes and CPU overlap; failures retire GPUs then use CPU', async () => {
  const lanes=createLanePool(2,await createGPUPicker().enumerate(fake()));
  const spans=[]; let release; const barrier=new Promise(r=>release=r);
  const run=async lane=>{ const span={kind:lane.kind,start:performance.now()}; spans.push(span); await barrier; span.end=performance.now(); return lane.kind; };
  const work=[lanes.run('gpu',run),lanes.run('gpu',run),lanes.run('cpu',run),lanes.run('cpu',run)];
  await new Promise(r=>setTimeout(r,10)); assert.equal(spans.length,4); release(); await Promise.all(work);
  assert.ok(Math.max(...spans.map(s=>s.start))<=Math.min(...spans.map(s=>s.end)));
  const tried=[]; assert.equal(await lanes.run('gpu-preferred',async l=>{tried.push(l.kind); if(l.kind==='gpu')throw Error('device lost'); return 'cpu';}), 'cpu');
  assert.deepEqual(tried,['gpu','gpu','cpu']);
  await assert.rejects(lanes.run('gpu',run),/gpu_unavailable/);
});
test('battery and backoff registration guards and GPU slot fields', async () => {
  const old=globalThis.fetch; const app=new ContributeApp({gpu:fake()}); app.isCharging=true;
  let body; globalThis.fetch=async (_u,o)=>{body=JSON.parse(o.body);return {ok:true,json:async()=>({tabId:'t'})};};
  try {
    await app.registerWorker(); assert.equal(body.gpu,true); assert.equal(body.gpuSlots,2);
    app.hasBattery=true; app.isCharging=false; await app.registerWorker(); assert.equal(body.gpu,false); assert.equal(body.gpuSlots,0);
    app.isCharging=true; app.backoff=true; await app.registerWorker(); assert.equal(body.gpu,false);
  } finally {globalThis.fetch=old;}
});
