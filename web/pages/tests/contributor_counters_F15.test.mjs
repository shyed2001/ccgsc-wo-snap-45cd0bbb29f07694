// F15: the contribute page and the monitor page show the durable counters.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { mountContributorCounters } from '../js/admin_controls_MVPW.js';
import {
  ContributeApp,
  applyContributorCounterText,
  contributorCounterURL,
  formatContributorCounters
} from '../js/contribute.js';

const pagesDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

function response(data, status = 200) {
  return {
    ok: status < 400,
    status,
    headers: { get: () => 'application/json' },
    json: async () => data
  };
}

test('counter text uses the server numbers and leaves gaps unmeasured', () => {
  assert.deepEqual(formatContributorCounters({ acceptedTasks: 2, computeMs: 1500 }), {
    acceptedText: '2',
    computeText: '1500 ms'
  });
  assert.deepEqual(formatContributorCounters({ acceptedTasks: 0, computeMs: 0 }), {
    acceptedText: '0',
    computeText: '0 ms'
  });
  assert.equal(formatContributorCounters({}).acceptedText, 'not measured');
  assert.equal(formatContributorCounters(null).computeText, 'not measured');
});

test('anonymous reads pass the contribute-page install id; signed-in reads do not', () => {
  assert.equal(contributorCounterURL('host-abc', false), '/contributor/v1/counters');
  assert.equal(contributorCounterURL('host-abc', true), '/contributor/v1/counters');
  assert.equal(contributorCounterURL('', true), '/contributor/v1/counters');
});

test('contribute.html shows the durable accepted-task and compute counters', () => {
  const html = fs.readFileSync(path.join(pagesDir, 'contribute.html'), 'utf8');
  assert.match(html, /id="stat-accepted-tasks"/);
  assert.match(html, /id="stat-compute-ms"/);
  assert.match(html, /not measured/);
});

test('the contribute page renders a loaded counter and an unmeasured one', () => {
  const accepted = { textContent: '' };
  const compute = { textContent: '' };
  const app = new ContributeApp({ autoStart: false, hostId: 'host-abc' });
  app.dom = { statAcceptedTasks: accepted, statComputeMs: compute };
  app.contributorCountersKnown = false;
  app.renderStats();
  assert.equal(accepted.textContent, 'not measured');
  assert.equal(compute.textContent, 'not measured');
  app.contributorCounters = { acceptedTasks: 4, computeMs: 20 };
  app.contributorCountersKnown = true;
  app.isAnonymous = true;
  app.renderStats();
  assert.equal(accepted.textContent, '4');
  assert.equal(compute.textContent, '20 ms');
  assert.equal(contributorCounterURL(app.resolveHostId(), app.isAnonymous), '/contributor/v1/counters');
});

test('applyContributorCounterText writes the elements the page owns', () => {
  const accepted = { textContent: 'stale' };
  const compute = { textContent: 'stale' };
  applyContributorCounterText({ accepted, compute }, null, true);
  assert.equal(accepted.textContent, 'not measured');
  applyContributorCounterText({ accepted, compute }, { acceptedTasks: 1, computeMs: 5 }, false);
  assert.equal(accepted.textContent, '1');
  assert.equal(compute.textContent, '5 ms');
});

test('the monitor page mounts contributor totals from the admin route', async () => {
  const inserted = [];
  const status = { textContent: '' };
  const tasks = { textContent: '' };
  const compute = { textContent: '' };
  const content = {
    insertAdjacentHTML(_pos, html) { inserted.push(html); },
    querySelector(selector) {
      if (selector === '#contributor-counter-status') return status;
      if (selector === '#contributor-counter-tasks') return tasks;
      if (selector === '#contributor-counter-ms') return compute;
      return null;
    }
  };
  globalThis.fetch = async (url) => {
    assert.equal(String(url), '/admin/v1/contributor-counters');
    return response({ acceptedTasks: 6, computeMs: 900 });
  };
  try {
    await mountContributorCounters(content);
    assert.match(inserted[0], /id="contributor-counters"/);
    assert.equal(status.textContent, 'Totals across contributors.');
    assert.equal(tasks.textContent, '6');
    assert.equal(compute.textContent, '900 ms');
  } finally {
    delete globalThis.fetch;
  }
});

test('the monitor page leaves totals unmeasured when the read is refused', async () => {
  const status = { textContent: '' };
  const tasks = { textContent: '0' };
  const compute = { textContent: '0 ms' };
  const content = {
    insertAdjacentHTML() {},
    querySelector(selector) {
      if (selector === '#contributor-counter-status') return status;
      if (selector === '#contributor-counter-tasks') return tasks;
      if (selector === '#contributor-counter-ms') return compute;
      return null;
    }
  };
  globalThis.fetch = async () => response({ title: 'Unauthorized' }, 401);
  try {
    await mountContributorCounters(content);
    assert.match(status.textContent, /not measured/);
    assert.equal(tasks.textContent, 'not measured');
    assert.equal(compute.textContent, 'not measured');
  } finally {
    delete globalThis.fetch;
  }
});

test('the monitor controller mounts the counter panel with the other monitor panels', () => {
  const app = fs.readFileSync(path.join(pagesDir, 'js', 'app.js'), 'utf8');
  assert.match(app, /mountContributorCounters\(content\)/);
});
