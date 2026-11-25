// Run explicitly: node web/pages/tests/p2p_browser_F11.mjs
// F11, 2026-11-24. TWO real Chromium tabs, real RTCDataChannel and loopback HTTP.
// CDP exchanges SDP only in this test. Synthetic grants are NOT production auth.
// No public ICE/TURN, user profile, package installs or pools runtime is used.
import { createServer } from 'node:http';
import { readFile, mkdtemp } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import assert from 'node:assert/strict';

const payload = Buffer.alloc(2 * 1024 * 1024);
for (let i = 0; i < payload.length; i++) payload[i] = i % 251;
const sha = createHash('sha256').update(payload).digest('hex');
const files = new Map(await Promise.all(['transport.js', 'rules.js'].map(async n =>
  ['/' + n, await readFile(new URL('../js/platform/p2p/' + n, import.meta.url))])));
const html = `<!doctype html><meta charset="utf-8"><title>F11 isolated RTC test</title>
<script type="module">
import { createArtifactPeerClient } from '/transport.js';
const sha = '${sha}';
window.setup = async (donor, mode) => {
  window.pc?.close(); window.client?.clear();
  window.calls = 0; window.served = null; window.channel = null;
  window.scope = { poolId:'tp', ownerId:'synthetic-owner', appId:'synthetic-app',
    artifactId:sha, workerId:donor?'donor':'receiver', leaseId:donor?'donor-lease':'receiver-lease',
    kind:'module', expiresAt:Date.now()+60000 };
  window.client = createArtifactPeerClient({ origin:'https://fixture.invalid', enabled:true,
    consent:()=>true, peerTimeoutMs:mode==='timeout'?300:3000,
    authorize:async s=>s.ownerId==='synthetic-owner' && s.appId==='synthetic-app',
    fetch:async (url, init)=>{
      if (url !== 'https://fixture.invalid/thread-pool/v1/artifacts/'+sha) throw Error('wrong URL');
      window.calls++;
      // Test-only URL adapter: actual loopback HTTP, fixed synthetic fixture.
      return fetch('/fixture.bin'+(!donor && mode==='bad-http'?'?bad=1':''), init);
    }
  });
  if(donor) await client.get(scope,{token:'f11-fixture-only'});
  window.pc = new RTCPeerConnection({iceServers:[]});
  pc.ondatachannel = event => {
    window.channel = event.channel; channel.binaryType='arraybuffer';
    channel.onopen = () => {
      if(mode==='hit' || mode==='head-unavailable') {
        window.served = client.serve(scope,channel).then(x=>({ok:true,...x}),e=>({error:e.message}));
      } else channel.onmessage = event => {
        if(JSON.parse(event.data).type!=='get') return;
        if(mode==='timeout') return;
        if(mode==='drop' || mode==='bad-http') { pc.close(); return; }
        channel.send(JSON.stringify({v:1,type:'artifact',sha256:sha,size:mode==='oversize'?33554433:1}));
        channel.send(new Uint8Array([9]).buffer); channel.send(JSON.stringify({v:1,type:'end'}));
      };
    };
  };
};
window.ice = async () => {
  if(pc.iceGatheringState!=='complete') await new Promise((resolve,reject)=>{
    const timer=setTimeout(()=>reject(Error('ICE timeout')),5000);
    pc.onicegatheringstatechange=()=>{ if(pc.iceGatheringState==='complete'){clearTimeout(timer);resolve();} };
  });
  return pc.localDescription.toJSON();
};
window.offer = async () => {
  window.channel=pc.createDataChannel('f11-artifact',{ordered:true});
  await pc.setLocalDescription(await pc.createOffer()); return ice();
};
window.answer = async offer => { await pc.setRemoteDescription(offer); await pc.setLocalDescription(await pc.createAnswer()); return ice(); };
window.run = async mode => {
  const started=performance.now();
  try {
    const result=await client.get(scope,{token:'f11-fixture-only',openPeer:async()=>{
      if(channel.readyState!=='open') throw Error('head-unavailable'); return channel;
    }});
    const digest=Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',result.bytes)),b=>b.toString(16).padStart(2,'0')).join('');
    return {mode,source:result.source,fallbackReason:result.fallbackReason,size:result.bytes.length,digest,httpCalls:calls,ms:performance.now()-started};
  } catch(e) { return {mode,error:e.message,httpCalls:calls,ms:performance.now()-started}; }
};
window.ready=true;
</script>`;
const server = createServer((req, res) => {
  if (req.url.startsWith('/fixture.bin')) {
    if (req.headers.authorization !== 'Bearer f11-fixture-only') { res.writeHead(403).end(); return; }
    const data = req.url.includes('?bad=1') ? Buffer.from('corrupt-http') : payload;
    res.writeHead(200, { 'Content-Type': 'application/octet-stream', 'Content-Length': data.length,
      'X-Artifact-Sha256': sha, 'Cache-Control': 'no-store' }); res.end(data); return;
  }
  if (req.url === '/') { res.writeHead(200, { 'Content-Type': 'text/html' }).end(html); return; }
  if (files.has(req.url)) { res.writeHead(200, { 'Content-Type': 'text/javascript' }).end(files.get(req.url)); return; }
  res.writeHead(404).end();
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
const url = 'http://127.0.0.1:' + server.address().port + '/';
const profile = await mkdtemp(join(tmpdir(), 'f11-rtc-'));
const browser = process.env.F11_BROWSER || 'C:/Program Files/Google/Chrome/Application/chrome.exe';
const child = spawn(browser, ['--headless=new', '--remote-debugging-port=0', '--remote-debugging-address=127.0.0.1',
  '--user-data-dir=' + profile, '--no-first-run', '--no-default-browser-check', '--disable-background-networking',
  '--disable-extensions', 'about:blank'], { stdio: 'ignore', windowsHide: true });
let spawnError; child.on('error', e => { spawnError = e; });
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
let ws, send;
try {
  let port;
  for (let i = 0; i < 80; i++) {
    if (spawnError) throw spawnError;
    try { port = (await readFile(join(profile, 'DevToolsActivePort'), 'utf8')).split('\n')[0]; break; }
    catch { await delay(100); }
  }
  if (!port) throw Error('CDP startup unavailable');
  const version = await (await fetch('http://127.0.0.1:' + port + '/json/version')).json();
  ws = new WebSocket(version.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => { ws.onopen = resolve; ws.onerror = reject; });
  let serial = 0; const pending = new Map();
  ws.onmessage = event => {
    const msg = JSON.parse(event.data), waiter = pending.get(msg.id);
    if (waiter) { pending.delete(msg.id); clearTimeout(waiter.timer); msg.error ? waiter.reject(Error(JSON.stringify(msg.error))) : waiter.resolve(msg.result); }
  };
  send = (method, params = {}, sessionId) => new Promise((resolve, reject) => {
    const id = ++serial;
    const timer = setTimeout(() => { pending.delete(id); reject(Error('CDP timeout: ' + method)); }, 15000);
    pending.set(id, { resolve, reject, timer }); ws.send(JSON.stringify({ id, method, params, sessionId }));
  });
  const evaluate = async (session, expression) => {
    const r = await send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true }, session);
    if (r.exceptionDetails) throw Error(JSON.stringify(r.exceptionDetails)); return r.result.value;
  };
  const sessions = [];
  for (let i = 0; i < 2; i++) {
    const target = await send('Target.createTarget', { url });
    const { sessionId } = await send('Target.attachToTarget', { targetId: target.targetId, flatten: true }); sessions.push(sessionId);
    let ready = false;
    for (let j = 0; j < 100; j++) { if (await evaluate(sessionId, 'window.ready===true')) { ready = true; break; } await delay(50); }
    assert.ok(ready, 'fixture loaded');
  }
  const [donor, receiver] = sessions; const results = [];
  for (const mode of ['hit', 'corrupt', 'oversize', 'timeout', 'drop', 'head-unavailable', 'bad-http']) {
    await evaluate(donor, 'setup(true,' + JSON.stringify(mode) + ')');
    await evaluate(receiver, 'setup(false,' + JSON.stringify(mode) + ')');
    const offer = await evaluate(receiver, 'offer()');
    const answer = await evaluate(donor, 'answer(' + JSON.stringify(offer) + ')');
    await evaluate(receiver, 'pc.setRemoteDescription(' + JSON.stringify(answer) + ')');
    let opened = false;
    for (let j = 0; j < 100; j++) { if (await evaluate(receiver, 'channel.readyState==="open"')) { opened = true; break; } await delay(50); }
    assert.ok(opened, 'real RTCDataChannel opened: ' + mode);
    if (mode === 'head-unavailable') {
      await evaluate(donor, 'pc.close()');
      for (let j = 0; j < 100; j++) { if (await evaluate(receiver, 'channel.readyState!=="open"')) break; await delay(20); }
    }
    const result = await evaluate(receiver, 'run(' + JSON.stringify(mode) + ')'); results.push(result);
    console.log(JSON.stringify(result));
    if (mode === 'bad-http') { assert.equal(result.error, 'hash'); assert.equal(result.httpCalls, 1); }
    else {
      assert.equal(result.digest, sha); assert.equal(result.size, payload.length);
      assert.equal(result.source, mode === 'hit' ? 'peer' : 'server'); assert.equal(result.httpCalls, mode === 'hit' ? 0 : 1);
      if (mode === 'hit') assert.equal((await evaluate(donor, 'served')).ok, true);
    }
  }
  console.log(JSON.stringify({ status: 'PASS', qualification: 'ISOLATED_TRANSPORT_ONLY', browser: version.Browser,
    tabs: 2, cases: results.length, productionSignaling: false, turn: false, profile }));
} finally {
  if (send && ws?.readyState === WebSocket.OPEN) { try { await send('Browser.close'); } catch {} }
  ws?.close(); child.kill(); await new Promise(resolve => server.close(resolve));
}
