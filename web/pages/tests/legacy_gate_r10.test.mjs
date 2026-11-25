// R10-FIX (minimax MINOR): a hidden legacy area must not take slots. The gate
// released the slots it held when the area was hidden, but claimSlots() ignored
// the gate afterwards, so the hidden lane immediately took them again and the
// visible area ran short.
import test from 'node:test';
import assert from 'node:assert/strict';

import { createLegacyLaneAdapter } from '../js/legacy_lane_adapter_T104.js';
import { WorkerBudget } from '../js/worker_budget_T104.js';
import { createStatusLog } from '../js/status_log_T104.js';

test('a closed gate refuses the claim and grants nothing', () => {
  const budget = new WorkerBudget({ total: 4 });
  const log = createStatusLog();
  const adapter = createLegacyLaneAdapter({ laneMode: 'matrix', budget, statusLog: log });
  assert.equal(adapter.claimSlots(2), 2);

  adapter.setEnabled(false);
  assert.equal(budget.usedBy('legacy:matrix'), 0, 'hiding the area releases its slots');
  assert.equal(adapter.claimSlots(2), 0, 'a hidden lane took slots back from the visible area');
  assert.equal(budget.usedBy('legacy:matrix'), 0);
  assert.equal(budget.remaining(), 4, 'a hidden lane holds nothing');

  adapter.setEnabled(true);
  assert.equal(adapter.claimSlots(2), 2, 'showing the area restores its claim');
});

test('a lane created hidden claims nothing until the page shows it', () => {
  const budget = new WorkerBudget({ total: 2 });
  const adapter = createLegacyLaneAdapter({ laneMode: 'p2p', budget, enabled: false });
  assert.equal(adapter.claimSlots(2), 0, 'a lane created hidden may not claim');
  assert.equal(budget.usedBy('legacy:p2p'), 0);
  adapter.setEnabled(true);
  assert.equal(adapter.claimSlots(2), 2, 'the ceiling is intact after the pause');
});

test('without a budget the hidden gate still answers the ask as zero', () => {
  const adapter = createLegacyLaneAdapter({ laneMode: 'solo' });
  assert.equal(adapter.claimSlots(3), 3, 'no budget: unconstrained while enabled');
  adapter.setEnabled(false);
  assert.equal(adapter.claimSlots(3), 0, 'a hidden lane without a budget also refuses');
});
