// Invoked by internal/api/dew_F12_test.go against a real, disposable Go API.
// One JS process retains the tab state across server downtime, never a reload.
import assert from 'node:assert/strict';
import { ContributeApp } from '../../js/contribute.js';

let input = '';
for await (const chunk of process.stdin) input += chunk;
const cfg = JSON.parse(input);
const path = cfg.pool === 'pp' ? '/process-pool/v1' : '/thread-pool/v1';
let base = cfg.base + path;
const headers = { 'Content-Type': 'application/json', Authorization: `Bearer ${cfg.token}` };
async function post(url, body, auth = headers) {
  return fetch(url, { method: 'POST', headers: auth, body: JSON.stringify(body) });
}
let response = await post(base + '/workers/register', { hostId: 'f12-host', browserTabId: 'f12-tab', slots: 1 });
assert.equal(response.status, 201, 'register');
const worker = await response.json();
response = await post(base + `/workers/${worker.tabId}/leases`, {});
assert.equal(response.status, 200, 'claim');
const a = await response.json();
// F12 (G-F12-1): the default Dew window is 10 minutes; the retired 24 h
// default must never come back.
assert.equal(a.leaseReceipt.windowSeconds, 600);
const pool = cfg.pool;
const app = new ContributeApp({ authToken: cfg.token, autoStart: false });
app.sourceMode = 'LIVE';
app.poolBase = () => base;
app.tabIds = { [pool]: worker.tabId };
app.currentPool = pool;
app.currentLease = a.lease;
app.currentTaskEnvelope = a.taskEnvelope;
app.currentLeaseReceipt = a.leaseReceipt;
app.renderStats = () => {};
app.log = () => {};
app.activeTasks.set(a.lease.taskId, { lease: a.lease, taskEnvelope: a.taskEnvelope, leaseReceipt: a.leaseReceipt, pool, tabId: worker.tabId });
const result = { taskId: a.lease.taskId, ok: true, output: { answer: 42 } };
app.pendingSettlement = result;
assert.equal((await post(cfg.control + '/disconnect', {}, {})).status, 204);
await app.settlePendingResult(); // genuine connection failure on the stopped API
assert.equal(app.pendingSettlement, result, 'offline result retained');
assert.equal(app.activeTasks.size, 1, 'original context retained');
assert.equal(app.currentLease, a.lease, 'original lease retained');
const reconnect = await post(cfg.control + '/reconnect', {}, {});
assert.equal(reconnect.status, 200);
base = (await reconnect.json()).base + path;

// Authentication is still required even when a signed receipt is available.
const body = { lease: a.lease, taskEnvelope: a.taskEnvelope, leaseReceipt: a.leaseReceipt, output: result.output };
assert.equal((await post(base + `/workers/${worker.tabId}/results`, body, { 'Content-Type': 'application/json' })).status, 401);
await app.settlePendingResult();
assert.equal(app.pendingSettlement, null, 'reconnect accepted retained result');
assert.equal(app.activeTasks.size, 0);
assert.equal(app.currentLease, null);
const statuses = await Promise.all(Array.from({ length: 8 }, async () => (await post(base + `/workers/${worker.tabId}/results`, body)).status));
assert.deepEqual(statuses, Array(8).fill(409), 'concurrent replays refused');
console.log('PASS: retained contributor result, server/store restart, authenticated reconnect, 8 concurrent replays refused');
