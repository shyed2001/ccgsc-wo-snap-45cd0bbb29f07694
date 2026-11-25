// File: F:\tmp\wwpp\spike_sandbox2\cdp_run.mjs
// Author: hermes MiniMax-M3 for Claude Opus 5.5. Created 2026-11-17 (Dhaka).
// What/why: run parent.html in headless Edge via CDP and print the spike log after real time
//  (no virtual time). WO-SPIKE2 harness driver - the parent URL port is 8711.
// Env: BROWSER=<edge path>  SPIKE_PORT=<port>  SPIKE_GPU=1 to keep the GPU stack on (SwiftShader).
import { spawn } from 'node:child_process';

const edge = process.env.BROWSER || 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe';
const PORT = Number(process.env.SPIKE_PORT || 8711);
const DEBUG_PORT = Number(process.env.CDP_PORT || 9334);
const URL = `http://127.0.0.1:${PORT}/parent.html`;
const GPU_FLAGS = process.env.SPIKE_GPU === '1'
  ? ['--enable-unsafe-swiftshader', '--use-angle=swiftshader']
  : ['--disable-gpu'];

const p = spawn(edge, ['--headless=new', ...GPU_FLAGS, `--remote-debugging-port=${DEBUG_PORT}`,
  `--user-data-dir=${process.env.TEMP}/spike2_edge`, 'about:blank'], { stdio: 'ignore' });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
console.log('spawned', edge, '->', URL);
let targets;
for (let i = 0; i < 40; i++) {
  try { targets = await (await fetch(`http://127.0.0.1:${DEBUG_PORT}/json`)).json(); break; }
  catch { await sleep(250); }
}
const page = targets && targets.find((t) => t.type === 'page');
if (!page) { console.log('no page target', JSON.stringify(targets).slice(0, 300)); p.kill(); process.exit(1); }
console.log('page', page.webSocketDebuggerUrl);
const ws = new WebSocket(page.webSocketDebuggerUrl);
let id = 0; const pend = new Map();
ws.onmessage = (m) => { const d = JSON.parse(m.data); if (d.id && pend.has(d.id)) { pend.get(d.id)(d); pend.delete(d.id); } };
await new Promise((r) => (ws.onopen = r));
const send = (method, params = {}) => new Promise((r) => { const i = ++id; pend.set(i, r); ws.send(JSON.stringify({ id: i, method, params })); });

await send('Page.navigate', { url: URL });
let out = '';
for (let i = 0; i < 40; i++) {
  await sleep(1000);
  const r = await send('Runtime.evaluate', {
    expression: "[document.title, document.getElementById('log').textContent].join(String.fromCharCode(10))",
    returnByValue: true
  });
  out = r.result?.result?.value || JSON.stringify(r).slice(0, 300);
  if (out.split('\n').some((l) => l.startsWith('SPIKE2 '))) break;
}
console.log(out);
ws.close(); p.kill();
// END OF FILE cdp_run.mjs
