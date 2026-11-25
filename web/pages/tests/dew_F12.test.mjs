// F12 (G-F12-3): the contributor tab keeps its app lock and pauses idle while a
// Dew result is still outstanding. stop() with no open receipt still releases
// the budget and the upkeep worker.
import { describe, it } from 'node:test';
import assert from 'node:assert/strict';

import { ContributeApp } from '../js/contribute.js';

function budget() {
  const held = new Map();
  return {
    claim(owner, n) {
      held.set(owner, (held.get(owner) || 0) + n);
      return n;
    },
    release(owner, n) {
      held.set(owner, Math.max(0, (held.get(owner) || 0) - n));
    },
    releaseAll(owner) {
      const n = held.get(owner) || 0;
      held.set(owner, 0);
      return n;
    },
    usedBy(owner) {
      return held.get(owner) || 0;
    },
    total() {
      return 4;
    }
  };
}

function appWith(receipt) {
  const originalWorker = globalThis.Worker;
  const originalCreate = URL.createObjectURL;
  const originalRevoke = URL.revokeObjectURL;
  globalThis.Worker = class {
    postMessage() {}
    terminate() { this.terminated = true; }
  };
  URL.createObjectURL = () => 'blob:dew-f12';
  URL.revokeObjectURL = () => {};
  const app = new ContributeApp({ authToken: 'token', autoStart: false, budget: budget() });
  app.sourceMode = 'LIVE';
  app.isRunning = true;
  app.render = () => {};
  app.renderStats = () => {};
  app.log = () => {};
  app.port = { postMessage() {} };
  app.sendHeartbeat = async () => { app.heartbeats = (app.heartbeats || 0) + 1; };
  app.initUpkeepWorker();
  app.applyBudgetClaim();
  const lease = { jobId: 'j', taskId: 't', token: 'tok', generation: 1 };
  app.currentLease = lease;
  app.currentLeaseReceipt = receipt;
  app.activeTasks.set('t', { lease, leaseReceipt: receipt, pool: 'tp', tabId: 'tab' });
  app.pendingSettlement = { taskId: 't', ok: true, output: { n: 1 } };
  return {
    app,
    restore() {
      globalThis.Worker = originalWorker;
      URL.createObjectURL = originalCreate;
      URL.revokeObjectURL = originalRevoke;
    }
  };
}

describe('F12 Dew client lock', () => {
  it('stop() keeps the budget and the upkeep worker while a Dew receipt is open', () => {
    const deadline = Math.floor(Date.now() / 1000) + 600;
    const { app, restore } = appWith({ deadline, windowSeconds: 600 });
    try {
      const held = app.budgetRef().usedBy('pools');
      assert.ok(held > 0, 'the running tab claimed budget');
      assert.ok(app.upkeepWorker, 'upkeep exists before stop');
      app.stop();
      assert.equal(app.isRunning, false);
      assert.equal(app.dewIdlePaused, true);
      assert.equal(app.budgetRef().usedBy('pools'), held, 'budget stays while the receipt is open');
      assert.ok(app.upkeepWorker, 'upkeep stays so heartbeats continue');
      app.onUpkeepTick();
      assert.equal(app.heartbeats, 1, 'a stopped Dew hold still heartbeats');
    } finally {
      restore();
    }
  });

  it('stop() releases the budget and the upkeep worker when no Dew receipt is pending', () => {
    const { app, restore } = appWith({ r: 1 });
    try {
      app.pendingSettlement = null;
      app.stop();
      assert.equal(app.dewIdlePaused, false);
      assert.equal(app.budgetRef().usedBy('pools'), 0);
      assert.equal(app.upkeepWorker, null);
    } finally {
      restore();
    }
  });

  it('an expired Dew receipt does not pause idle', () => {
    const deadline = Math.floor(Date.now() / 1000) - 5;
    const { app, restore } = appWith({ deadline });
    try {
      app.stop();
      assert.equal(app.dewIdlePaused, false);
      assert.equal(app.budgetRef().usedBy('pools'), 0);
      assert.equal(app.upkeepWorker, null);
    } finally {
      restore();
    }
  });

  it('a successful settle releases the hold on a stopped tab', async () => {
    const deadline = Math.floor(Date.now() / 1000) + 600;
    const { app, restore } = appWith({ deadline, windowSeconds: 600 });
    try {
      app.stop();
      assert.equal(app.dewIdlePaused, true);
      app.submitResult = async () => true;
      await app.settlePendingResult();
      assert.equal(app.pendingSettlement, null);
      assert.equal(app.dewIdlePaused, false);
      assert.equal(app.budgetRef().usedBy('pools'), 0);
      assert.equal(app.upkeepWorker, null);
    } finally {
      restore();
    }
  });
});
