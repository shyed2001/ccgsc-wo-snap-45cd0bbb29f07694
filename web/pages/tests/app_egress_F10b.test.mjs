import {test} from 'node:test';
import assert from 'node:assert/strict';
import {createLeaseEgress} from '../js/runner.js';
test('F10b HTTPS allowlist, bounded request, renewal and expiry', async()=>{
 const old=globalThis.fetch, calls=[];
 globalThis.fetch=async (url,options)=>{calls.push({url,options});return new Response('{"ok":true}',{status:200});};
 try{
 const cap=createLeaseEgress({endpoints:['https://api.example.com'],token:'lease',expiresAt:Math.floor(Date.now()/1000)+90});
 assert.equal((await cap.api.fetch('https://api.example.com/work',{method:'POST',body:'x'})).json().ok,true);
 assert.equal(calls[0].options.headers.get('Authorization'),'Bearer lease');
 assert.equal(calls[0].options.credentials,'omit');
 assert.equal(calls[0].options.redirect,'error');
 await assert.rejects(cap.api.fetch('https://other.example.com/'),/endpoint_denied/);
 await assert.rejects(cap.api.fetch('https://api.example.com/',{method:'POST',body:'a'.repeat(262145)}),/body_limit/);
 assert.equal(calls.length,1);
 cap.refresh({token:'new',expiresAt:Math.floor(Date.now()/1000)+90});
 await cap.api.fetch('https://api.example.com/');
 assert.equal(calls[1].options.headers.get('Authorization'),'Bearer new');
 const expired=createLeaseEgress({endpoints:['https://api.example.com'],token:'old',expiresAt:1});
 await assert.rejects(expired.api.fetch('https://api.example.com/'),/expired/);
 for(let i=0;i<55;i++) await cap.api.fetch('https://api.example.com/');
 await cap.api.fetch('https://api.example.com/');
 await assert.rejects(cap.api.fetch('https://api.example.com/'),/rate_limit/);
 }finally{globalThis.fetch=old;}
});
test('F10b WSS authentication before send and connection close on renewal',()=>{
 const old=globalThis.WebSocket;
 let ws;
 globalThis.WebSocket=class{constructor(url){this.url=url;this.sent=[];ws=this;} send(x){this.sent.push(x);} close(){this.closed=true;this.onclose?.();}};
 try{
 const cap=createLeaseEgress({endpoints:['wss://socket.example.com'],token:'lease',expiresAt:Math.floor(Date.now()/1000)+90});
 assert.throws(()=>cap.api.connect('wss://other.example.com/'),/endpoint_denied/);
 let ready=false;
 const c=cap.api.connect('wss://socket.example.com/work',{onReady(){ready=true;}});
 assert.throws(()=>c.send('x'),/auth_required/);
 ws.onopen();
 assert.deepEqual(JSON.parse(ws.sent[0]),{type:'auth',token:'lease'});
 ws.onmessage({data:'{"type":"auth-ok"}'});
 assert.equal(ready,true);c.send('hello');assert.equal(ws.sent[1],'hello');
 cap.refresh({token:'new',expiresAt:Math.floor(Date.now()/1000)+90});
 assert.equal(ws.closed,true);
 }finally{if(ws&&!ws.closed)ws.close();globalThis.WebSocket=old;}
});
