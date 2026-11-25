// Path: web/pages/tests/legacy_lane_adapter_T104.test.mjs | Author: GLM (Z.ai) for Claude Opus 5.5 head | 2026-11-21
// What/why: T104 step 5 - the LegacyLaneAdapter. The old Node lanes keep their
// WebSocket transport; the adapter only gives them slots of the one WorkerBudget
// and lines in the one status/log area, with an honest unconstrained fallback.

import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { createLegacyLaneAdapter, LEGACY_OWNER_PREFIX } from '../js/legacy_lane_adapter_T104.js';
import { WorkerBudget } from '../js/worker_budget_T104.js';
import { createStatusLog } from '../js/status_log_T104.js';

describe('T104 legacy lane adapter', () => {

  it('claims and releases slots under owner legacy:<lane>', () => {
    const budget = new WorkerBudget({ total: 4 });
    const adapter = createLegacyLaneAdapter({ laneMode: 'matrix', budget });
    assert.equal(adapter.owner, 'legacy:matrix');
    assert.equal(adapter.claimSlots(2), 2);
    assert.equal(budget.usedBy('legacy:matrix'), 2);
    assert.equal(adapter.heldSlots(), 2);
    // The new pools claim the rest first; a later legacy ask shrinks to zero.
    assert.equal(budget.claim('pools', 2), 2);
    assert.equal(adapter.claimSlots(2), 0);
    assert.equal(adapter.releaseSlots(1), 1);
    assert.equal(adapter.releaseAllSlots(), 1);
    assert.equal(budget.usedBy('legacy:matrix'), 0);
    assert.equal(adapter.heldSlots(), 0);
  });

  it('a partial grant is logged through the status/log area', () => {
    const budget = new WorkerBudget({ total: 2 });
    budget.claim('pools', 1);
    const log = createStatusLog();
    const adapter = createLegacyLaneAdapter({ laneMode: 'prime', budget, statusLog: log });
    assert.equal(adapter.claimSlots(3), 1);
    const lines = log.lines();
    assert.equal(lines[0].source, 'legacy:prime');
    assert.match(lines[0].message, /granted 1 of 3 slots/);
  });

  it('without a budget the lane runs unconstrained and says so once', () => {
    const log = createStatusLog();
    const adapter = createLegacyLaneAdapter({ statusLog: log });
    assert.equal(adapter.owner, LEGACY_OWNER_PREFIX + 'generic');
    assert.equal(adapter.claimSlots(3), 3);
    assert.equal(adapter.claimSlots(3), 3);
    const warns = log.lines().filter((line) => line.message.includes('no shared worker budget'));
    assert.equal(warns.length, 1, 'one honest line, not one per claim');
    assert.equal(adapter.releaseSlots(2), 2);
    assert.equal(adapter.releaseAllSlots(), 0);
    assert.equal(adapter.heldSlots(), 0);
  });

  it('beginTask/endTask count in-flight lane tasks; unknown ends are ignored', () => {
    const adapter = createLegacyLaneAdapter({});
    assert.equal(adapter.beginTask('t1'), 1);
    assert.equal(adapter.beginTask('t2'), 2);
    assert.equal(adapter.beginTask('t2'), 2, 'the same tag does not double count');
    assert.equal(adapter.activeTasks(), 2);
    assert.deepEqual(adapter.activeTags().sort(), ['t1', 't2']);
    assert.equal(adapter.endTask('t1'), 1);
    assert.equal(adapter.endTask('unknown'), 1);
    assert.equal(adapter.endTask(''), 1);
    assert.equal(adapter.endTask('t2'), 0);
    assert.equal(adapter.beginTask(null), 0);
  });

  it('log and setLaneStatus route into the shared status/log area', () => {
    const log = createStatusLog();
    const el = { textContent: '' };
    log.attach(el);
    const adapter = createLegacyLaneAdapter({ laneMode: 'matrix', statusLog: log });
    adapter.log('lane connected');
    adapter.setLaneStatus('RUNNING');
    assert.ok(el.textContent.includes('[legacy:matrix] lane connected'), el.textContent);
    assert.ok(el.textContent.includes('[status] legacy:matrix=RUNNING'), el.textContent);
    adapter.setLaneStatus('');
    assert.ok(!el.textContent.includes('legacy:matrix='));
  });

  it('without a statusLog every call stays silent and safe', () => {
    const adapter = createLegacyLaneAdapter({});
    adapter.log('nothing');
    adapter.setLaneStatus('nothing');
    assert.equal(adapter.claimSlots(1), 1);
    assert.equal(adapter.releaseSlots(1), 1);
    assert.equal(adapter.releaseAllSlots(), 0);
  });

  it('exposeOn wires the hooks the root page consumes', () => {
    const adapter = createLegacyLaneAdapter({ laneMode: 'matrix' });
    const target = {};
    assert.equal(adapter.exposeOn(target), adapter);
    assert.equal(target.ccgscLegacyLaneAdapter, adapter);
    assert.equal(typeof target.ccgscLegacyLaneActiveTasks, 'function');
    adapter.beginTask('x');
    assert.equal(target.ccgscLegacyLaneActiveTasks(), 1);
    adapter.endTask('x');
    assert.equal(target.ccgscLegacyLaneActiveTasks(), 0);
    // A null target is refused harmlessly.
    assert.equal(adapter.exposeOn(null), adapter);
  });

  it('the whole integrated page stays inside the tab budget', () => {
    // The acceptance rule of the owner: the sum of all lanes of the tab never
    // exceeds the budget (the auto default is navigator.hardwareConcurrency).
    const budget = new WorkerBudget({ total: 4 });
    const matrix = createLegacyLaneAdapter({ laneMode: 'matrix', budget });
    const prime = createLegacyLaneAdapter({ laneMode: 'generic', budget });
    assert.equal(matrix.claimSlots(2), 2);
    assert.equal(prime.claimSlots(1), 1);
    assert.equal(budget.claim('pools', 2), 1, 'only one slot is left for the new pools');
    assert.ok(budget.used() <= budget.total());
    assert.deepEqual(budget.snapshot().owners, { 'legacy:matrix': 2, 'legacy:generic': 1, pools: 1 });
  });

  it('the heavy-work gate of a hidden area releases the slots', () => {
    const budget = new WorkerBudget({ total: 4 });
    const log = createStatusLog();
    const adapter = createLegacyLaneAdapter({ laneMode: 'matrix', budget, statusLog: log });
    assert.equal(adapter.isEnabled(), true, 'visible by default');
    assert.equal(adapter.claimSlots(2), 2);
    assert.equal(budget.usedBy('legacy:matrix'), 2);

    // The visitor shows the pools tab: the legacy area is hidden.
    assert.equal(adapter.setEnabled(false), false);
    assert.equal(adapter.isEnabled(), false);
    assert.equal(budget.usedBy('legacy:matrix'), 0, 'a hidden lane holds no slot');
    assert.ok(log.lines()[0].message.includes('paused'));

    // R10-FIX: a closed gate refuses the claim (a hidden lane must not take the
    // slots it just released); showing the area restores it.
    assert.equal(adapter.claimSlots(1), 0, 'a hidden lane grants nothing');
    assert.equal(budget.usedBy('legacy:matrix'), 0);

    assert.equal(adapter.setEnabled(true), true);
    assert.ok(log.lines()[0].message.includes('enabled'));
    assert.equal(adapter.setEnabled(true), true, 'no change, no second line');
    assert.equal(log.lines().length, 2, 'one line for the pause, one for the resume');
    assert.equal(adapter.claimSlots(1), 1, 'the visible lane claims again');
  });

  it('a lane created disabled claims nothing until the page shows it', () => {
    const budget = new WorkerBudget({ total: 2 });
    const adapter = createLegacyLaneAdapter({ laneMode: 'p2p', budget, enabled: false });
    assert.equal(adapter.isEnabled(), false);
    assert.equal(adapter.heldSlots(), 0);
    adapter.setEnabled(true);
    assert.equal(adapter.claimSlots(2), 2, 'the ceiling is intact after the pause');
  });

  it('the page reads the gate through the exposed hooks', () => {
    const target = {};
    const adapter = createLegacyLaneAdapter({ laneMode: 'matrix' });
    adapter.exposeOn(target);
    assert.equal(typeof target.ccgscLegacyLaneEnabled, 'function');
    assert.equal(target.ccgscLegacyLaneEnabled(), true);
    adapter.setEnabled(false);
    assert.equal(target.ccgscLegacyLaneEnabled(), false);
  });

});

