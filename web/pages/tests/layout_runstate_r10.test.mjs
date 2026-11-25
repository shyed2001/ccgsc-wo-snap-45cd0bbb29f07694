// R10-FIX (minimax NOT SURE): does the `top` layout keep the pools running while
// the collapsible legacy section is closed, and is that the documented contract?
// The answer is the run state, not a fix: this locks the contract down so a later
// change is a visible decision.
import test from 'node:test';
import assert from 'node:assert/strict';

import { computeAreaRunState } from '../js/layout_T104.js';

test('top: the pools run either way, the legacy area follows its open state', () => {
  const open = computeAreaRunState({ layout: 'top', legacyOpen: true });
  assert.equal(open.pools, true, 'the pools panel is the primary area of the top layout');
  assert.equal(open.legacy, true, 'an open section runs its heavy work');
  assert.equal(open.legacyVisible, true);
  assert.equal(open.legacyCollapsible, true);

  const closed = computeAreaRunState({ layout: 'top', legacyOpen: false });
  assert.equal(closed.pools, true, 'closing the legacy section does not stop the pools');
  assert.equal(closed.legacy, false, 'a closed section runs no heavy work');
  assert.equal(closed.legacyVisible, true, 'a closed <details> keeps its summary reachable');
});

test('tabs: exactly the visible tab runs', () => {
  const pools = computeAreaRunState({ layout: 'tabs', activeTab: 'pools' });
  assert.equal(pools.pools, true);
  assert.equal(pools.legacy, false);

  const legacy = computeAreaRunState({ layout: 'tabs', activeTab: 'legacy' });
  assert.equal(legacy.pools, false, 'a hidden tab starts nothing');
  assert.equal(legacy.legacy, true);
});

test('pools: the legacy area is neither shown nor started', () => {
  const state = computeAreaRunState({ layout: 'pools' });
  assert.equal(state.pools, true);
  assert.equal(state.legacy, false);
  assert.equal(state.legacyVisible, false);
});
