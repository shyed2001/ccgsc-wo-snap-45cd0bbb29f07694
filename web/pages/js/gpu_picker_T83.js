// T83-S1: self-contained factories for module, opaque runner and blob workers.
export function createGPUPicker() {
  async function enumerate(gpu = globalThis.navigator?.gpu) {
    if (!gpu?.requestAdapter) return [];
    const seen = new Set(), found = [];
    for (const preference of ['high-performance', 'low-power', 'default']) {
      try {
        const adapter = await gpu.requestAdapter(preference === 'default' ? undefined : {powerPreference: preference});
        if (!adapter) continue;
        const info = adapter.info || {};
        const id = JSON.stringify(['vendor','architecture','device','description'].map(k => String(info[k] || '')));
        if (seen.has(id)) continue;
        seen.add(id);
        const discrete = /discrete|nvidia|radeon rx/i.test(Object.values(info).join(' '));
        found.push({id, vendor:String(info.vendor || '').slice(0,64), architecture:String(info.architecture || '').slice(0,64), preference,
          discrete, createDevice: () => adapter.requestDevice()});
      } catch { /* unavailable preference */ }
    }
    return found.sort((a,b) => Number(b.discrete)-Number(a.discrete));
  }
  return {enumerate};
}

export function createLanePool(cpuSlots, gpus = []) {
  const cpu = Array.from({length:Math.max(1,Math.min(4,cpuSlots || 1))}, (_,i)=>({kind:'cpu',id:`cpu-${i}`}));
  const gpu = gpus.slice(0,2).map(g=>({...g,kind:'gpu'}));
  const waiters = new Set();
  let paused = false;
  const wake = () => { for(const resolve of waiters) resolve(); waiters.clear(); };
  async function run(needs, execute) {
    for (;;) {
      const available = gpu.filter(g=>!g.failed && !paused);
      let lane = needs !== 'cpu' && available.find(g=>!g.busy);
      if (!lane && needs !== 'gpu') lane = cpu.find(c=>!c.busy);
      if (!lane) {
        if(needs === 'gpu' && !available.length) throw Error('gpu_unavailable');
        await new Promise(r=>waiters.add(r)); continue;
      }
      lane.busy=true;
      try { return await execute(lane); }
      catch(err) { if(lane.kind !== 'gpu') throw err; lane.failed=true; }
      finally {lane.busy=false; wake();}
    }
  }
  return {run, gpu, cpu, pause(value) {paused=Boolean(value); wake();}};
}
