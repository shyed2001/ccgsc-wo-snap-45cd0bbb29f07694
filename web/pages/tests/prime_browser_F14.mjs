// Explicit local qualification, not a deployment or server settlement test.
import {createServer} from 'node:http';
import {readFile,mkdtemp} from 'node:fs/promises';
import {resolve,join,extname} from 'node:path';
import {fileURLToPath} from 'node:url';
import {spawn} from 'node:child_process';
import {createHash} from 'node:crypto';
import assert from 'node:assert/strict';
const web=fileURLToPath(new URL('../../',import.meta.url));
const hash=x=>createHash('sha256').update(x).digest('hex');
const examples=[
  {name:'isprime',input:{values:[0,1,2,3,4,561,"18446744073709551557"]},expect:{v:1,kind:'isprime',values:['0','1','2','3','4','561','18446744073709551557'],prime:[0,0,1,1,0,0,1]}},
  {name:'ssprime',input:{start:0,end:100001},expect:{v:1,kind:'ssprime',start:'0',end:'100001',count:9592}},
];
const html='<!doctype html><meta charset="utf-8"><title>F14 runner qualification</title><script>window.ccgscPageBooted=true</script><script type="module">import {handleJob} from "/pages/js/runner.js";import {createTaskEnvelope,signTaskEnvelope} from "/pages/js/contribute.js";import {sha256Hex} from "/pages/js/platform/hash.js";window.run=async function(name,input,withWasm){const base="/workloads/"+name+"/";const m=await(await fetch(base+"manifest.json")).json();const wasm=await(await fetch(base+m.entry)).arrayBuffer();const files={};const declared=[];for(const f of m.files){if(f.entry||withWasm){const bytes=f.entry?wasm:await(await fetch(base+f.path)).arrayBuffer();if(await sha256Hex(bytes)!==f.sha256)throw Error("manifest mismatch");declared.push(f);if(!f.entry)files[f.path]=bytes;}}const kp=await crypto.subtle.generateKey({name:"Ed25519"},true,["sign","verify"]);const pub=new Uint8Array(await crypto.subtle.exportKey("raw",kp.publicKey));const keyId=(await sha256Hex(pub)).slice(0,16);const keyRing={[keyId]:{keyId,publicKeyHex:Array.from(pub,b=>b.toString(16).padStart(2,"0")).join(""),scope:"operator"}};const inputJSON=JSON.stringify({json:input});const env=createTaskEnvelope("f14-"+name,"f14-"+name+"-"+withWasm,m.sha256,3600,keyId,await sha256Hex(new TextEncoder().encode(inputJSON)));env.workload={id:name,version:m.version,files:declared,kind:"js-module"};const signed=await signTaskEnvelope(env,kp.privateKey);const job={type:"job",jobId:env.jobId,taskId:env.taskId,wasm,wasmSha256:m.sha256,files,envelope:env,envSig:signed.envSig,workersRequested:1,inputJSON,timeoutMs:30000,needs:"cpu"};return handleJob(job,{postMessage(){}},{keyRing});};window.ready=true;</script>';
const server=createServer(async(req,res)=>{
  try {
    if(req.url==='/'){res.writeHead(200,{'Content-Type':'text/html'}).end(html);return;}
    const pathname=new URL(req.url,'http://127.0.0.1').pathname;
    const file=resolve(web,'.'+pathname);
    if(!file.startsWith(resolve(web)+ (process.platform==='win32'?'\\':'/'))){res.writeHead(403).end();return;}
    const bytes=await readFile(file);const mime={'.js':'text/javascript','.json':'application/json','.wasm':'application/wasm'}[extname(file)];
    if(!mime){res.writeHead(403).end();return;}
    res.writeHead(200,{'Content-Type':mime,'Cache-Control':'no-store'}).end(bytes);
  } catch {res.writeHead(404).end();}
});
await new Promise(r=>server.listen(0,'127.0.0.1',r));
const url='http://127.0.0.1:'+server.address().port+'/';
const profile=await mkdtemp(join(process.env.F14_TMP||'F:/tmp','f14-chrome-'));
const child=spawn(process.env.F14_BROWSER||'C:/Program Files/Google/Chrome/Application/chrome.exe',
 ['--headless=new','--remote-debugging-port=0','--remote-debugging-address=127.0.0.1','--user-data-dir='+profile,'--no-first-run','--no-default-browser-check','--disable-background-networking','--disable-extensions','about:blank'],{stdio:'ignore',windowsHide:true});
let spawnError;child.on('error',e=>{spawnError=e;});
const delay=ms=>new Promise(r=>setTimeout(r,ms));
let ws,send;
try{
 let port;
 for(let i=0;i<100;i++){if(spawnError)throw spawnError;try{port=(await readFile(join(profile,'DevToolsActivePort'),'utf8')).split('\n')[0];break;}catch{await delay(100);}}
 assert.ok(port,'CDP port');
 const version=await(await fetch('http://127.0.0.1:'+port+'/json/version')).json();
 ws=new WebSocket(version.webSocketDebuggerUrl);await new Promise((r,j)=>{ws.onopen=r;ws.onerror=j;});
 let serial=0;const pending=new Map();ws.onmessage=e=>{const msg=JSON.parse(e.data),p=pending.get(msg.id);if(p){pending.delete(msg.id);clearTimeout(p.timer);msg.error?p.reject(Error(JSON.stringify(msg.error))):p.resolve(msg.result);}};
 send=(method,params={},sessionId)=>new Promise((resolve,reject)=>{const id=++serial;const timer=setTimeout(()=>{pending.delete(id);reject(Error('CDP timeout '+method));},45000);pending.set(id,{resolve,reject,timer});ws.send(JSON.stringify({id,method,params,sessionId}));});
 const target=await send('Target.createTarget',{url});
 const {sessionId}=await send('Target.attachToTarget',{targetId:target.targetId,flatten:true});
 const evalJS=async(expression)=>{const r=await send('Runtime.evaluate',{expression,awaitPromise:true,returnByValue:true},sessionId);if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value;};
 let ready=false;for(let i=0;i<100;i++){if(await evalJS('window.ready===true')){ready=true;break;}await delay(50);}assert.ok(ready,'runner imports ready');
 for(const {name,input,expect} of examples)for(const withWasm of [false,true]){
  const expected=hash(Buffer.from(JSON.stringify(expect)));
  const result=await evalJS('run('+JSON.stringify(name)+','+JSON.stringify(input)+','+withWasm+')');
  console.log(JSON.stringify({name,withWasm,expected,result}));
  assert.equal(result.ok,true,result.error);assert.equal(result.verifyPass,true);assert.equal(result.output.sha256,expected);
 }
 console.log(JSON.stringify({status:'PASS',cases:4,browser:version.Browser,qualification:'REAL_BROWSER_GENERIC_RUNNER_WITH_TEST_KEY',productionSettlement:false}));
}finally{
 if(send&&ws?.readyState===WebSocket.OPEN){try{await send('Browser.close');}catch{}}
 ws?.close();child.kill();await new Promise(r=>server.close(r));
}
