// R10-FIX (minimax BLOCKER): the integrated root page dropped the ?pools= switch,
// so a visitor asking for ?pools=tp,pp was registered in the thread pool only and
// the process pool was never used on the root page. The standalone page already
// honoured the switch; the one entry did not.
import test from 'node:test';
import assert from 'node:assert/strict';

import {
  startContributorPage,
  resetContributorPageForTests,
  readPageSwitches
} from '../js/contribute_page_T104.js';

function fakeDoc() {
  return { getElementById: () => null, body: { setAttribute() {} } };
}

function recordingAppClass(seen) {
  return class FakeApp {
    constructor(options = {}) {
      seen.push(options);
      this.dom = null;
      this.isRunning = false;
      this.activeTasks = new Map();
      this.hostId = 'host-test';
    }
    async init() { this.dom = {}; }
    async start() { this.isRunning = true; }
    stop() { this.isRunning = false; }
  };
}

async function boot(search) {
  const seen = [];
  resetContributorPageForTests();
  try {
    const page = await startContributorPage({
      search,
      doc: fakeDoc(),
      globalTarget: {},
      storage: null,
      fetch: async () => ({ ok: true, status: 200, json: async () => ({ anonymousJoin: true }) }),
      importModule: async () => ({}),
      ContributeApp: recordingAppClass(seen)
    });
    return { page, seen };
  } finally {
    resetContributorPageForTests();
  }
}

test('the integrated root page registers in every pool of ?pools=tp,pp', async () => {
  const { page, seen } = await boot('?pools=tp,pp');
  assert.equal(page.started, true, page.reason);
  assert.equal(seen.length, 1, 'the page must construct the app');
  assert.deepEqual(seen[0].pools, ['tp', 'pp'],
    'the root page dropped the pools switch: the process pool is never used');
});

test('a single pool and an absent switch keep their old behaviour', async () => {
  const one = await boot('?pools=pp');
  assert.deepEqual(one.seen[0].pools, ['pp']);

  const none = await boot('');
  assert.equal(none.seen[0].pools, undefined, 'no switch: the app keeps its own default');

  const junk = await boot('?pools=xx,yy');
  assert.deepEqual(junk.seen[0].pools, ['xx', 'yy'], 'the app validates the names and falls back');
});

test('?pools=0 still turns the new pools off instead of naming a pool', () => {
  const off = readPageSwitches('?pools=0', null, 'pools');
  assert.equal(off.poolsOff, true);
  assert.equal(off.pools, undefined, 'a disabled switch names no pool');
  assert.equal(readPageSwitches('?pools=', null, 'pools').pools, undefined, 'an empty switch names no pool');
  assert.deepEqual(readPageSwitches('?pools=tp, pp', null, 'pools').pools, ['tp', 'pp']);
  assert.equal(readPageSwitches('', null, 'pools').pools, undefined, 'no switch keeps the app default');
});
