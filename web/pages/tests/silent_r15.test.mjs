// R15-FIX-SILENT: formerly silent browser failures are logged through the page's
// own this.log helper. registerWorkerOnce caught a failed adapter.createDevice()
// with an empty catch, so a discovered GPU adapter that could not open a device
// vanished without a trace and the tab registered fewer GPU lanes silently.
import test from 'node:test';
import assert from 'node:assert/strict';

import { ContributeApp } from '../js/contribute.js';

test('R15: a GPU adapter whose createDevice fails is logged, not dropped silently', async () => {
  const originalFetch = globalThis.fetch;
  const logs = [];
  const adapter = {
    info: { vendor: 'r15-test', architecture: 'mock' },
    requestDevice: async () => { throw new Error('device lost'); }
  };
  Object.defineProperty(globalThis.navigator, 'gpu', {
    value: { requestAdapter: async () => adapter },
    configurable: true
  });
  globalThis.fetch = async () => ({
    ok: true,
    status: 201,
    json: async () => ({ tabId: 'tab-r15' })
  });
  try {
    const app = new ContributeApp({ autoStart: false });
    app.log = (msg) => logs.push(String(msg));
    app.gpuAllowed = () => true;
    const reply = await app.registerWorkerOnce();
    assert.equal(reply.tabId, 'tab-r15');
    assert.equal(app.gpus.length, 0, 'the adapter that cannot open a device is not registered');
    assert.ok(
      logs.some((line) => line.includes('device lost')),
      `the failed adapter must be reported through this.log, got ${JSON.stringify(logs)}`
    );
  } finally {
    globalThis.fetch = originalFetch;
    delete globalThis.navigator.gpu;
  }
});
