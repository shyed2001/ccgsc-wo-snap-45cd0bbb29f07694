// W10D row 3/4 (D-W10-11, CR-51): the contributor controls on the contributor
// page. The suite pins each mode's rendering, the off default (today's page, no
// control section at all), pause (no new leases, the in-flight result still
// submits, the tab keeps its worker id), resume, the startStopOptIn opt-in and
// the live change on a heartbeat. ?autostart=0 keeps its meaning in every mode.
//
// Truth: WO_W10D_CTL.md rows 3/4, SPLIT_DESIGN_W10.md section 8 (D-W10-11).
import test from 'node:test';
import assert from 'node:assert/strict';

import {
  ContributeApp,
  CONTRIBUTOR_CONTROLS_MODES,
  CONTRIBUTOR_CONTROLS_ID,
  CONTRIBUTOR_CONTROL_ACTION_ID,
  CONTRIBUTOR_CONTROL_CAPACITY_ID,
  CONTRIBUTOR_CONTROL_STATUS_ID,
  contributorControlView,
  normalizeContributorControls
} from '../js/contribute.js';
import {
  startContributorPage,
  resetContributorPageForTests
} from '../js/contribute_page_T104.js';

// ---------------------------------------------------------------- Fake DOM

function fakeElement(doc, tag = 'div') {
  const element = {
    tagName: tag,
    id: '',
    className: '',
    type: '',
    min: '',
    max: '',
    step: '',
    value: '',
    textContent: '',
    hidden: false,
    disabled: false,
    parentNode: null,
    children: [],
    listeners: {},
    attributes: {},
    appendChild(child) {
      child.parentNode = element;
      element.children.push(child);
      doc.register(child);
      return child;
    },
    insertBefore(child, before) {
      child.parentNode = element;
      const at = element.children.indexOf(before);
      if (at >= 0) element.children.splice(at, 0, child);
      else element.children.push(child);
      doc.register(child);
      return child;
    },
    removeChild(child) {
      const at = element.children.indexOf(child);
      if (at >= 0) element.children.splice(at, 1);
      child.parentNode = null;
      doc.unregister(child);
      return child;
    },
    addEventListener(type, fn) {
      (element.listeners[type] = element.listeners[type] || []).push(fn);
    },
    removeEventListener(type, fn) {
      element.listeners[type] = (element.listeners[type] || []).filter((item) => item !== fn);
    },
    fire(type) {
      for (const fn of element.listeners[type] || []) fn({ type, target: element });
    },
    setAttribute(name, value) {
      element.attributes[name] = String(value);
    },
    querySelector(selector) {
      const id = selector.startsWith('#') ? selector.slice(1) : selector;
      const find = (node) => {
        if (!node) return null;
        if (node.id === id) return node;
        for (const child of node.children || []) {
          const hit = find(child);
          if (hit) return hit;
        }
        return null;
      };
      return find(element);
    }
  };
  return element;
}

function fakeDocument(ids = []) {
  const byId = new Map();
  const doc = {
    body: null,
    register(element) {
      if (element.id) byId.set(element.id, element);
      return element;
    },
    unregister(element) {
      if (element.id && byId.get(element.id) === element) byId.delete(element.id);
    },
    createElement(tag) {
      return fakeElement(doc, tag);
    },
    getElementById(id) {
      return byId.get(id) || null;
    }
  };
  doc.body = doc.createElement('body');
  for (const id of ids) {
    const element = doc.createElement('div');
    element.id = id;
    doc.register(element);
    doc.body.appendChild(element);
  }
  return doc;
}

function clearAppTimers(app) {
  for (const key of ['taskTimer', 'runnerRetryTimer', 'tokenRenewTimer']) {
    if (app[key]) {
      clearTimeout(app[key]);
      app[key] = null;
    }
  }
}

function jsonResponse(body, status = 200) {
  return { ok: status < 400, status, json: async () => body };
}

// ---------------------------------------------------------------- The mode model

test('D-W10-11: the three documented modes, and everything else is off', () => {
  assert.deepEqual(Array.from(CONTRIBUTOR_CONTROLS_MODES), ['off', 'pauseResume', 'startStopOptIn']);
  for (const junk of [undefined, null, '', 'alwaysOn', 'OFF', 'pause', 7, {}, []]) {
    assert.equal(normalizeContributorControls(junk), 'off', `${String(junk)} answers off`);
  }
  for (const mode of CONTRIBUTOR_CONTROLS_MODES) {
    assert.equal(normalizeContributorControls(mode), mode);
  }
});

test('D-W10-11: the view of each mode offers the one documented action', () => {
  const off = contributorControlView({ mode: 'off', isRunning: true });
  assert.equal(off.visible, false);
  assert.equal(off.action, null);

  const computing = contributorControlView({
    mode: 'pauseResume', isRunning: true, workerCount: 9, capacityMax: 4
  });
  assert.equal(computing.action, 'pause');
  assert.equal(computing.actionLabel, 'Pause');
  assert.equal(computing.capacityValue, 4, 'the slider stays inside the capacity');
  assert.match(computing.status, /Computing/);

  const paused = contributorControlView({
    mode: 'pauseResume', isRunning: true, claimsPaused: true
  });
  assert.equal(paused.action, 'resume');
  assert.equal(paused.actionLabel, 'Resume');
  assert.match(paused.status, /no new leases/i);

  const idle = contributorControlView({ mode: 'pauseResume', isRunning: false });
  assert.equal(idle.action, 'resume', 'a tab that never started offers Resume');

  const stopped = contributorControlView({ mode: 'startStopOptIn', isRunning: false });
  assert.equal(stopped.action, 'start');
  assert.equal(stopped.actionLabel, 'Start');
  assert.match(stopped.status, /press Start/i);

  const running = contributorControlView({ mode: 'startStopOptIn', isRunning: true });
  assert.equal(running.action, 'stop');
  assert.equal(running.actionLabel, 'Stop');
});

// ---------------------------------------------------------------- Rendering

test('D-W10-11: off renders no control section at all', () => {
  const doc = fakeDocument(['log-output']);
  const app = new ContributeApp({ doc, autoStart: false });
  app.bindDOM();
  app.applyContributorControls('off');
  assert.equal(doc.getElementById(CONTRIBUTOR_CONTROLS_ID), null);
  assert.equal(doc.getElementById(CONTRIBUTOR_CONTROL_ACTION_ID), null);
  assert.equal(app.controlElements, null);
});

test('D-W10-11: pauseResume renders Pause/Resume plus a capacity slider inside the budget', () => {
  const doc = fakeDocument(['log-output']);
  const app = new ContributeApp({ doc, autoStart: false, contributorControls: 'pauseResume' });
  app.bindDOM();
  app.hwConcurrency = 8;
  app.workerCount = 4;
  app.isRunning = true;
  app.applyContributorControls('pauseResume');

  const section = doc.getElementById(CONTRIBUTOR_CONTROLS_ID);
  assert.ok(section, 'the section exists');
  assert.equal(section.hidden, false);
  const button = doc.getElementById(CONTRIBUTOR_CONTROL_ACTION_ID);
  assert.equal(button.textContent, 'Pause');
  const slider = doc.getElementById(CONTRIBUTOR_CONTROL_CAPACITY_ID);
  assert.equal(slider.max, '4', 'the device cap of 8 cores is 4 workers');
  assert.equal(slider.value, '4');
  assert.ok(doc.getElementById(CONTRIBUTOR_CONTROL_STATUS_ID).textContent.length > 0);

  // The shared tab budget bounds the slider, never the other way round.
  app.options.budget = {
    total: () => 3,
    claim: () => 0,
    release: () => 0,
    releaseAll: () => 0,
    usedBy: () => 0,
    remaining: () => 3
  };
  app.renderContributorControls();
  assert.equal(slider.max, '3', 'the budget of 3 slots wins over the device cap');
  assert.equal(slider.value, '3');
});

test('D-W10-11: startStopOptIn opens stopped with a Start button', () => {
  const doc = fakeDocument(['log-output']);
  const app = new ContributeApp({ doc, autoStart: false, contributorControls: 'startStopOptIn' });
  app.bindDOM();
  app.hwConcurrency = 8;
  app.workerCount = 2;
  app.applyContributorControls('startStopOptIn');
  const button = doc.getElementById(CONTRIBUTOR_CONTROL_ACTION_ID);
  assert.equal(button.textContent, 'Start');
  assert.match(doc.getElementById(CONTRIBUTOR_CONTROL_STATUS_ID).textContent, /press Start/i);
  assert.equal(doc.getElementById(CONTRIBUTOR_CONTROL_CAPACITY_ID).value, '2');
});

test('D-W10-11: the capacity slider sets the worker count and stays clamped', () => {
  const doc = fakeDocument(['log-output']);
  const app = new ContributeApp({ doc, autoStart: false, contributorControls: 'pauseResume' });
  app.bindDOM();
  app.hwConcurrency = 8;
  app.workerCount = 1;
  app.applyContributorControls('pauseResume');
  const slider = doc.getElementById(CONTRIBUTOR_CONTROL_CAPACITY_ID);
  slider.value = '3';
  slider.fire('input');
  assert.equal(app.workerCount, 3);
  assert.equal(app.manualOverride, true);
  slider.value = '99';
  slider.fire('change');
  assert.equal(app.workerCount, 4, 'a value above the cap is clamped');
  assert.equal(slider.value, '4');
});

// ---------------------------------------------------------------- Pause/resume semantics

test('D-W10-11: pause stops new claims, the in-flight result still submits, the tab keeps its id', async () => {
  const doc = fakeDocument(['log-output']);
  const app = new ContributeApp({ doc, autoStart: false, contributorControls: 'pauseResume' });
  app.bindDOM();
  app.sourceMode = 'LIVE';
  app.isRunning = true;
  app.tabIds = { tp: 'tab-pause' };
  app.tabId = 'tab-pause';
  app.port = { postMessage() {} };
  app.runnerReady = true;
  app.workerCount = 1;
  app.contributorControls = 'pauseResume';
  app.pauseContributorWork();
  assert.equal(app.claimsPaused, true);
  assert.equal(app.isRunning, true, 'pausing does not stop the run');
  assert.equal(app.tabId, 'tab-pause', 'the worker (tab) id is kept');
  assert.equal(doc.getElementById(CONTRIBUTOR_CONTROL_ACTION_ID).textContent, 'Resume');

  let claims = 0;
  app.getNextJob = async () => { claims++; return null; };
  await app.scheduleNextTask();
  assert.equal(claims, 0, 'a paused tab claims no new lease');

  // An in-flight task completes while paused: its result is still submitted.
  const lease = { jobId: 'job-pause', taskId: 'task-pause' };
  app.currentLease = lease;
  app.activeTasks.set('task-pause', { lease, pool: 'tp', tabId: 'tab-pause', taskEnvelope: {}, leaseReceipt: {} });
  const posts = [];
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (url, options) => {
    posts.push({ url: String(url), body: options && options.body ? JSON.parse(options.body) : null });
    return jsonResponse({}, 200);
  };
  try {
    app.onTaskResult({
      type: 'result',
      taskId: 'task-pause',
      ok: true,
      verifyPass: true,
      timings: { computeMs: 2 },
      output: { n: 1 }
    });
    const deadline = Date.now() + 1000;
    while (Date.now() < deadline) {
      const results = posts.filter((post) => post.url.includes('/results'));
      if (results.length === 1 && app.activeTasks.size === 0) break;
      await new Promise((resolve) => setTimeout(resolve, 5));
    }
  } finally {
    globalThis.fetch = originalFetch;
    clearAppTimers(app);
  }
  const results = posts.filter((post) => post.url.includes('/results'));
  assert.equal(results.length, 1, 'the in-flight result was submitted while paused');
  assert.equal(results[0].url.includes('/workers/tab-pause/results'), true);
  assert.equal(claims, 0, 'the paused tab still claims nothing');
  assert.equal(app.activeTasks.size, 0, 'the finished task left the active set');

  // Resume claims again on the same tab id.
  app.resumeContributorWork();
  assert.equal(app.claimsPaused, false);
  await app.scheduleNextTask();
  clearAppTimers(app);
  assert.equal(claims, 1, 'resume claims again');
  assert.equal(app.tabId, 'tab-pause');
});

// ---------------------------------------------------------------- Boot and heartbeats

test('D-W10-11: the register reply is read at boot, and startStopOptIn opens stopped', async () => {
  const doc = fakeDocument(['log-output']);
  const app = new ContributeApp({ doc, autoStart: true });
  app.bindDOM();
  app.sourceMode = 'LIVE';
  app.gpuAllowed = () => false;
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (url) => {
    if (String(url).includes('/workers/register')) {
      return jsonResponse({ tabId: 'tab-boot', contributorControls: 'startStopOptIn' }, 201);
    }
    return jsonResponse({}, 200);
  };
  try {
    await app.registerWorker();
  } finally {
    globalThis.fetch = originalFetch;
  }
  assert.equal(app.contributorControls, 'startStopOptIn');
  assert.equal(app.contributorOptInStopped, true, 'the tab must not auto-start');
  assert.equal(doc.getElementById(CONTRIBUTOR_CONTROL_ACTION_ID).textContent, 'Start');
});

test('D-W10-11: an init in startStopOptIn registers but never claims until Start', async () => {
  const doc = fakeDocument(['log-output']);
  const app = new ContributeApp({ doc });
  let leases = 0;
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (url) => {
    const target = String(url);
    if (target.endsWith('/pools/runner-config')) return jsonResponse({ anonymousJoin: false, runOrigin: null });
    if (target.includes('/workers/register')) {
      return jsonResponse({ tabId: 'tab-init', contributorControls: 'startStopOptIn' }, 201);
    }
    if (target.includes('/leases')) { leases++; return jsonResponse(null, 204); }
    return jsonResponse({}, 200);
  };
  try {
    await app.init();
    assert.equal(app.sourceMode, 'LIVE');
    assert.equal(app.tabId, 'tab-init');
    assert.equal(app.isRunning, false, 'the page opens stopped');
    assert.equal(app.autoStart, true, 'auto-start is on; the mode wins');
    assert.equal(leases, 0);

    doc.getElementById(CONTRIBUTOR_CONTROL_ACTION_ID).fire('click');
    assert.equal(app.isRunning, true, 'Start starts the tab');
    assert.equal(doc.getElementById(CONTRIBUTOR_CONTROL_ACTION_ID).textContent, 'Stop');
    assert.equal(app.tabId, 'tab-init');

    doc.getElementById(CONTRIBUTOR_CONTROL_ACTION_ID).fire('click');
    assert.equal(app.isRunning, false, 'Stop stops the tab');
    assert.equal(app.contributorOptInStopped, true, 'a stopped opt-in tab stays stopped');
    assert.equal(app.tabId, 'tab-init', 'the tab id survives Stop');
  } finally {
    clearAppTimers(app);
    app.stop();
    globalThis.fetch = originalFetch;
  }
});

test('D-W10-11: pauseResume auto-starts, but ?autostart=0 still wins', async () => {
  // Auto-start on: the tab computes without a click.
  {
    const doc = fakeDocument(['log-output']);
    const app = new ContributeApp({ doc });
    const originalFetch = globalThis.fetch;
    globalThis.fetch = async (url) => {
      const target = String(url);
      if (target.endsWith('/pools/runner-config')) return jsonResponse({ anonymousJoin: false, runOrigin: null });
      if (target.includes('/workers/register')) {
        return jsonResponse({ tabId: 'tab-auto', contributorControls: 'pauseResume' }, 201);
      }
      return jsonResponse({}, 200);
    };
    try {
      await app.init();
      assert.equal(app.contributorControls, 'pauseResume');
      assert.equal(app.isRunning, true, 'pauseResume auto-starts');
      assert.equal(doc.getElementById(CONTRIBUTOR_CONTROL_ACTION_ID).textContent, 'Pause');
    } finally {
      clearAppTimers(app);
      app.stop();
      globalThis.fetch = originalFetch;
    }
  }

  // ?autostart=0: registered, idle, and the Resume click is the explicit start.
  {
    const doc = fakeDocument(['log-output']);
    const app = new ContributeApp({ doc, search: '?autostart=0' });
    const originalFetch = globalThis.fetch;
    globalThis.fetch = async (url) => {
      const target = String(url);
      if (target.endsWith('/pools/runner-config')) return jsonResponse({ anonymousJoin: false, runOrigin: null });
      if (target.includes('/workers/register')) {
        return jsonResponse({ tabId: 'tab-idle', contributorControls: 'pauseResume' }, 201);
      }
      return jsonResponse({}, 200);
    };
    try {
      await app.init();
      assert.equal(app.autoStart, false);
      assert.equal(app.isRunning, false, '?autostart=0 keeps the tab idle');
      assert.equal(doc.getElementById(CONTRIBUTOR_CONTROL_ACTION_ID).textContent, 'Resume');
      doc.getElementById(CONTRIBUTOR_CONTROL_ACTION_ID).fire('click');
      assert.equal(app.isRunning, true, 'Resume is a user action and starts the tab');
    } finally {
      clearAppTimers(app);
      app.stop();
      globalThis.fetch = originalFetch;
    }
  }
});

test('D-W10-11: off renders nothing after an init and keeps compute at once', async () => {
  const doc = fakeDocument(['log-output']);
  const app = new ContributeApp({ doc });
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (url) => {
    const target = String(url);
    if (target.endsWith('/pools/runner-config')) return jsonResponse({ anonymousJoin: false, runOrigin: null });
    if (target.includes('/workers/register')) {
      return jsonResponse({ tabId: 'tab-off', contributorControls: 'off' }, 201);
    }
    return jsonResponse({}, 200);
  };
  try {
    await app.init();
    assert.equal(app.contributorControls, 'off');
    assert.equal(app.isRunning, true, 'off is today\'s page: connect and compute at once');
    assert.equal(doc.getElementById(CONTRIBUTOR_CONTROLS_ID), null);
    assert.equal(doc.getElementById(CONTRIBUTOR_CONTROL_ACTION_ID), null);
  } finally {
    clearAppTimers(app);
    app.stop();
    globalThis.fetch = originalFetch;
  }
});

test('D-W10-11: a setting change applies on the next heartbeat without interrupting the task', async () => {
  const doc = fakeDocument(['log-output']);
  const app = new ContributeApp({ doc, autoStart: false, contributorControls: 'pauseResume' });
  app.bindDOM();
  app.sourceMode = 'LIVE';
  app.tabIds = { tp: 'tab-hb' };
  app.tabId = 'tab-hb';
  app.isRunning = true;
  app.applyContributorControls('pauseResume');
  let mode = 'startStopOptIn';
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => jsonResponse({ tabId: 'tab-hb', contributorControls: mode }, 200);
  try {
    await app.sendHeartbeat();
    assert.equal(app.contributorControls, 'startStopOptIn', 'the heartbeat applied the new mode');
    assert.equal(app.isRunning, true, 'a live change never interrupts the run');
    assert.equal(doc.getElementById(CONTRIBUTOR_CONTROL_ACTION_ID).textContent, 'Stop');

    mode = 'off';
    await app.sendHeartbeat();
    assert.equal(app.contributorControls, 'off');
    assert.equal(doc.getElementById(CONTRIBUTOR_CONTROLS_ID), null, 'off removes the section again');
  } finally {
    globalThis.fetch = originalFetch;
    clearAppTimers(app);
  }
});

test('D-W10-11: a live change away from pauseResume lifts the pause and claims again', async () => {
  const doc = fakeDocument(['log-output']);
  const app = new ContributeApp({ doc, autoStart: false, contributorControls: 'pauseResume' });
  app.bindDOM();
  app.sourceMode = 'LIVE';
  app.tabIds = { tp: 'tab-lift' };
  app.tabId = 'tab-lift';
  app.port = { postMessage() {} };
  app.runnerReady = true;
  app.isRunning = true;
  app.contributorControls = 'pauseResume';
  app.applyContributorControls('pauseResume');
  app.pauseContributorWork();
  assert.equal(app.claimsPaused, true);

  let claims = 0;
  app.getNextJob = async () => { claims++; return null; };
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => jsonResponse({ tabId: 'tab-lift', contributorControls: 'startStopOptIn' }, 200);
  try {
    await app.sendHeartbeat();
    await new Promise((resolve) => setTimeout(resolve, 0));
    assert.equal(app.contributorControls, 'startStopOptIn');
    assert.equal(app.claimsPaused, false, 'the pause belongs to pauseResume only');
    assert.ok(claims >= 1, 'the tab claims again under the new mode');
    assert.equal(doc.getElementById(CONTRIBUTOR_CONTROL_ACTION_ID).textContent, 'Stop');
  } finally {
    globalThis.fetch = originalFetch;
    clearAppTimers(app);
  }
});

// ---------------------------------------------------------------- The page entry

test('D-W10-11: the page entry does not start a tab whose stop opt-in said stopped', async () => {
  resetContributorPageForTests();
  const calls = [];
  class FakeApp {
    constructor() {
      this.dom = null;
      this.isRunning = false;
      this.contributorOptInStopped = false;
      this.contributorControls = 'off';
      this.activeTasks = new Map();
      this.workerCount = 2;
      this.hwConcurrency = 4;
      this.gpus = [];
    }
    async init() {
      calls.push('init');
      this.dom = {};
      this.contributorOptInStopped = true;
      this.contributorControls = 'startStopOptIn';
    }
    async start() { calls.push('start'); this.isRunning = true; }
    stop() { calls.push('stop'); this.isRunning = false; }
    gpuAllowed() { return false; }
    applyBudgetClaim() { return 0; }
    applyGpuBudgetClaim() { return 0; }
    releaseBudgetClaim() { return 0; }
    releaseGpuBudgetClaim() { return 0; }
  }
  try {
    const result = await startContributorPage({
      doc: { getElementById: () => null, body: { setAttribute() {} } },
      globalTarget: {},
      navigator: { hardwareConcurrency: 8 },
      search: '',
      forcedLayout: 'pools',
      ContributeApp: FakeApp,
      importModule: async () => ({}),
      indexedDB: null
    });
    assert.equal(result.started, true);
    assert.deepEqual(calls, ['init'], 'the entry registered but did not start');
    assert.equal(result.contributorControls, 'startStopOptIn');
  } finally {
    resetContributorPageForTests();
  }
});
