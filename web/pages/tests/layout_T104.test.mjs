// Path: web/pages/tests/layout_T104.test.mjs | Author: Claude Opus 5.5 head | 2026-11-21
// What/why: T104 owner decision 2026-11-21 (layout = "All") - the three layouts
// of the root page and their selection: ?layout=top|tabs|pools, else localStorage
// ccgsc_layout, else top, unknown value = top. Plus the run state per layout
// (which area runs heavy work) and the switch against a fake DOM: tab
// visibility, tab clicks and the collapsible legacy section. No network.

import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import {
  LAYOUTS,
  DEFAULT_LAYOUT,
  LAYOUT_STORAGE_KEY,
  TAB_POOLS,
  TAB_LEGACY,
  normalizeLayout,
  readStoredLayout,
  resolveLayout,
  persistLayout,
  computeAreaRunState,
  createLayoutSwitch
} from '../js/layout_T104.js';

function mapStorage(initial = {}) {
  const store = new Map(Object.entries(initial));
  return {
    getItem: (key) => (store.has(key) ? store.get(key) : null),
    setItem: (key, value) => { store.set(key, String(value)); },
    removeItem: (key) => { store.delete(key); },
    dump: () => Object.fromEntries(store.entries())
  };
}

function classList() {
  const set = new Set();
  return {
    toggle(name, on) { if (on) set.add(name); else set.delete(name); },
    contains: (name) => set.has(name),
    values: () => Array.from(set)
  };
}

function fakeElement(extra = {}) {
  const el = {
    hidden: false,
    style: {},
    attributes: {},
    listeners: {},
    classList: classList(),
    setAttribute(name, value) { this.attributes[name] = String(value); },
    addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
    removeEventListener(type, fn) {
      this.listeners[type] = (this.listeners[type] || []).filter((f) => f !== fn);
    },
    fire(type) { for (const fn of this.listeners[type] || []) fn({ type }); }
  };
  return Object.assign(el, extra);
}

function fakeDoc(nodes = {}) {
  const byId = new Map(Object.entries(nodes));
  return {
    body: { attributes: {}, setAttribute(name, value) { this.attributes[name] = value; } },
    getElementById: (id) => byId.get(id) || null,
    nodes: byId
  };
}

// The element set of the root page.
function rootNodes(layout = {}) {
  const legacy = fakeElement({ open: true, ...(layout.legacy || {}) });
  return {
    nodes: {
      't103-pools-panel': fakeElement(),
      't104-legacy-section': legacy,
      't104-legacy-summary': fakeElement(),
      't104-layout-tabs': fakeElement(),
      't104-tab-pools': fakeElement(),
      't104-tab-legacy': fakeElement(),
      't104-layout-btn-top': fakeElement(),
      't104-layout-btn-tabs': fakeElement(),
      't104-layout-btn-pools': fakeElement()
    },
    legacy
  };
}

describe('T104 layout selection', () => {

  it('knows exactly the three layouts and defaults to top', () => {
    assert.deepEqual(LAYOUTS, ['top', 'tabs', 'pools']);
    assert.equal(DEFAULT_LAYOUT, 'top');
    assert.equal(LAYOUT_STORAGE_KEY, 'ccgsc_layout');
    assert.equal(normalizeLayout('top'), 'top');
    assert.equal(normalizeLayout(' TABS '), 'tabs');
    assert.equal(normalizeLayout('Pools'), 'pools');
    assert.equal(normalizeLayout(''), null);
    assert.equal(normalizeLayout(null), null);
    assert.equal(normalizeLayout('bogus'), null);
    assert.equal(normalizeLayout(7), null);
  });

  it('reads the stored layout and ignores junk', () => {
    assert.equal(readStoredLayout(mapStorage({ [LAYOUT_STORAGE_KEY]: 'tabs' })), 'tabs');
    assert.equal(readStoredLayout(mapStorage({ [LAYOUT_STORAGE_KEY]: 'nope' })), null);
    assert.equal(readStoredLayout(mapStorage()), null);
    assert.equal(readStoredLayout(null), null, 'no storage at all');
    assert.equal(readStoredLayout({ getItem: () => { throw new Error('blocked'); } }), null);
  });

  it('the query wins, then the stored value, then top', () => {
    const stored = mapStorage({ [LAYOUT_STORAGE_KEY]: 'tabs' });
    assert.equal(resolveLayout('?layout=pools', stored), 'pools');
    assert.equal(resolveLayout('?layout=tabs', stored), 'tabs');
    assert.equal(resolveLayout('', stored), 'tabs', 'no query: the remembered layout');
    assert.equal(resolveLayout('?other=1', mapStorage()), 'top');
    assert.equal(resolveLayout(undefined, mapStorage()), 'top');
    assert.equal(resolveLayout('?layout=TOP', mapStorage()), 'top', 'the query is case-insensitive');
    assert.equal(resolveLayout('?layout=', mapStorage()), 'top', 'empty value = default');
  });

  it('an unknown ?layout value means top, not the stored layout', () => {
    const stored = mapStorage({ [LAYOUT_STORAGE_KEY]: 'pools' });
    assert.equal(resolveLayout('?layout=bogus', stored), 'top', 'the owner rule: unknown = top');
    assert.equal(resolveLayout('?layout=', stored), 'top', 'an empty value is unknown too');
    assert.equal(resolveLayout('?layout=%20', stored), 'top', 'a blank value is unknown too');
    assert.equal(resolveLayout('?layout', stored), 'top', 'a bare key is unknown too');
    assert.equal(resolveLayout('?layout=nope', mapStorage()), 'top', 'with no stored value either');
    // The stored value still drives a visit without a layout query at all.
    assert.equal(resolveLayout('', stored), 'pools');
    assert.equal(resolveLayout('?other=1', stored), 'pools');
  });

  it('a query value wins even when it equals the default', () => {
    const stored = mapStorage({ [LAYOUT_STORAGE_KEY]: 'pools' });
    assert.equal(resolveLayout('?layout=top', stored), 'top', 'an explicit top is a choice, not a fallback');
  });

  it('persists only known layouts, and only when the value changes', () => {
    const store = mapStorage();
    assert.equal(persistLayout('tabs', store), 'tabs');
    assert.equal(store.dump()[LAYOUT_STORAGE_KEY], 'tabs');
    const writes = [];
    const counting = {
      getItem: (key) => store.getItem(key),
      setItem: (key, value) => { writes.push(value); store.setItem(key, value); }
    };
    assert.equal(persistLayout('tabs', counting), 'tabs');
    assert.equal(writes.length, 0, 'the same value is not written again');
    assert.equal(persistLayout('bogus', counting), 'top', 'an unknown layout falls back to the default');
    assert.deepEqual(writes, ['top']);
    assert.equal(persistLayout('pools', null), 'pools', 'no storage still answers the effective layout');
  });

});

describe('T104 layout run state (only the visible area runs heavy work)', () => {

  it('top: new pools first, legacy in a collapsible section (open by default)', () => {
    const state = computeAreaRunState({ layout: 'top' });
    assert.equal(state.layout, 'top');
    assert.equal(state.pools, true);
    assert.equal(state.legacy, true);
    assert.equal(state.poolsVisible, true);
    assert.equal(state.legacyVisible, true);
    assert.equal(state.tabsVisible, false);
    assert.equal(state.legacyCollapsible, true);
  });

  it('top with the section collapsed runs no legacy work', () => {
    const state = computeAreaRunState({ layout: 'top', legacyOpen: false });
    assert.equal(state.legacyVisible, true, 'the summary stays reachable');
    assert.equal(state.legacy, false, 'a collapsed section runs no heavy work');
    assert.equal(state.pools, true);
  });

  it('tabs: exactly one tab runs heavy work', () => {
    const pools = computeAreaRunState({ layout: 'tabs', activeTab: TAB_POOLS });
    assert.equal(pools.pools, true);
    assert.equal(pools.legacy, false);
    assert.equal(pools.poolsVisible, true);
    assert.equal(pools.legacyVisible, false);
    assert.equal(pools.tabsVisible, true);
    assert.equal(pools.legacyCollapsible, false);

    const legacy = computeAreaRunState({ layout: 'tabs', activeTab: TAB_LEGACY });
    assert.equal(legacy.pools, false);
    assert.equal(legacy.legacy, true);
    assert.equal(legacy.poolsVisible, false);
    assert.equal(legacy.legacyVisible, true);
  });

  it('pools: the legacy lanes are hidden and never run', () => {
    const state = computeAreaRunState({ layout: 'pools' });
    assert.equal(state.pools, true);
    assert.equal(state.legacy, false);
    assert.equal(state.poolsVisible, true);
    assert.equal(state.legacyVisible, false);
    assert.equal(state.tabsVisible, false);
    assert.equal(state.legacyCollapsible, false);
  });

  it('unknown layouts and tabs fall back to the default', () => {
    assert.equal(computeAreaRunState({ layout: 'bogus' }).layout, 'top');
    assert.equal(computeAreaRunState({ layout: 'bogus' }).legacy, true);
    assert.equal(computeAreaRunState({ layout: 'tabs', activeTab: 'bogus' }).activeTab, TAB_POOLS);
  });

});

describe('T104 layout switch (fake DOM)', () => {

  it('renders the visibility of all three layouts', () => {
    const { nodes } = rootNodes();
    const doc = fakeDoc(nodes);
    const sw = createLayoutSwitch({ doc, layout: 'tabs', emitInitial: true });
    assert.equal(nodes['t103-pools-panel'].hidden, false);
    assert.equal(nodes['t104-legacy-section'].hidden, true, 'the legacy tab is not visible');
    assert.equal(nodes['t104-layout-tabs'].hidden, false, 'the tab bar is visible');
    assert.equal(nodes['t104-tab-pools'].attributes['aria-selected'], 'true');
    assert.equal(nodes['t104-tab-legacy'].attributes['aria-selected'], 'false');

    sw.setLayout('pools');
    assert.equal(nodes['t103-pools-panel'].hidden, false);
    assert.equal(nodes['t104-legacy-section'].hidden, true);
    assert.equal(nodes['t104-layout-tabs'].hidden, true, 'the tab bar belongs to the tabs layout');
    assert.equal(doc.body.attributes['data-t104-layout'], 'pools');
    assert.equal(nodes['t104-layout-btn-pools'].attributes['aria-pressed'], 'true');
    assert.equal(nodes['t104-layout-btn-top'].attributes['aria-pressed'], 'false');

    sw.setLayout('top');
    assert.equal(nodes['t104-legacy-section'].hidden, false);
    assert.equal(nodes['t104-legacy-summary'].style.display, '', 'the summary of the collapsible section');
  });

  it('a tab click reports the run state; the hidden tab runs nothing', () => {
    const { nodes } = rootNodes();
    const doc = fakeDoc(nodes);
    const seen = [];
    const sw = createLayoutSwitch({
      doc,
      layout: 'tabs',
      onRunStateChange: (next) => seen.push(next)
    });
    assert.equal(seen.length, 1, 'the mount applies the initial state');
    assert.equal(seen[0].pools, true);
    assert.equal(seen[0].legacy, false);

    nodes['t104-tab-legacy'].fire('click');
    assert.equal(sw.getActiveTab(), TAB_LEGACY);
    assert.equal(seen.length, 2);
    assert.equal(seen[1].pools, false, 'the pools tab is hidden now');
    assert.equal(seen[1].legacy, true);
    assert.equal(nodes['t103-pools-panel'].hidden, true);
    assert.equal(nodes['t104-legacy-section'].hidden, false);

    nodes['t104-tab-pools'].fire('click');
    assert.equal(seen.length, 3);
    assert.equal(seen[2].pools, true);
    assert.equal(seen[2].legacy, false);
    assert.equal(sw.getActiveTab(), TAB_POOLS);
  });

  it('two areas never run at once, whatever the click order', () => {
    const { nodes } = rootNodes();
    const sw = createLayoutSwitch({ doc: fakeDoc(nodes), layout: 'tabs' });
    for (const tab of [TAB_LEGACY, TAB_LEGACY, TAB_POOLS, TAB_LEGACY, TAB_POOLS]) {
      const state = sw.setActiveTab(tab);
      assert.notEqual(state.pools && state.legacy, true, `${tab}: both areas ran at once`);
      assert.equal(state.pools || state.legacy, true, 'one area always runs');
    }
  });

  it('the collapsible legacy section of the top layout gates its own work', () => {
    const { nodes, legacy } = rootNodes();
    const seen = [];
    const sw = createLayoutSwitch({
      doc: fakeDoc(nodes),
      layout: 'top',
      onRunStateChange: (next) => seen.push(next)
    });
    assert.equal(seen[0].legacy, true, 'the shipped section is open, so it runs');

    legacy.open = false;
    legacy.fire('toggle');
    assert.equal(sw.getLegacyOpen(), false);
    assert.equal(seen[seen.length - 1].legacy, false, 'collapsed: no legacy work');
    assert.equal(seen[seen.length - 1].pools, true, 'the new pools are unaffected');

    sw.setLegacyOpen(true);
    assert.equal(legacy.open, true);
    assert.equal(seen[seen.length - 1].legacy, true);
  });

  it('the layout buttons switch the layout and remember it', () => {
    const { nodes } = rootNodes();
    const doc = fakeDoc(nodes);
    const store = mapStorage();
    const changes = [];
    const sw = createLayoutSwitch({
      doc,
      layout: 'top',
      emitInitial: false,
      onLayoutChange: (next, previous) => changes.push([previous, next])
    });
    nodes['t104-layout-btn-tabs'].fire('click');
    assert.equal(sw.getLayout(), 'tabs');
    assert.deepEqual(changes, [['top', 'tabs']]);
    nodes['t104-layout-btn-pools'].fire('click');
    assert.equal(sw.getLayout(), 'pools');
    assert.deepEqual(changes, [['top', 'tabs'], ['tabs', 'pools']]);

    sw.setLayout('tabs', store);
    assert.equal(store.dump()[LAYOUT_STORAGE_KEY], 'tabs', 'the visitor choice is remembered');
    assert.equal(resolveLayout('', store), 'tabs', 'and used by the next visit');
  });

  it('a missing page or broken nodes never throw', () => {
    const sw = createLayoutSwitch({ doc: null });
    assert.equal(sw.getLayout(), 'top');
    assert.equal(sw.runState().pools, true);
    const partial = fakeDoc({ 't103-pools-panel': fakeElement() });
    const sw2 = createLayoutSwitch({ doc: partial, layout: 'tabs' });
    assert.equal(sw2.getLayout(), 'tabs');
    assert.equal(sw2.setActiveTab('legacy').legacy, true);
    const throwing = { getElementById: () => { throw new Error('boom'); } };
    assert.equal(createLayoutSwitch({ doc: throwing }).getLayout(), 'top');
  });

  it('dispose removes every listener', () => {
    const { nodes, legacy } = rootNodes();
    const sw = createLayoutSwitch({ doc: fakeDoc(nodes), layout: 'tabs' });
    sw.dispose();
    const seen = [];
    sw.setActiveTab(TAB_LEGACY);
    nodes['t104-tab-pools'].fire('click');
    assert.equal(nodes['t104-tab-pools'].listeners.click.length, 0);
    assert.equal(nodes['t104-tab-legacy'].listeners.click.length, 0);
    assert.equal(legacy.listeners.toggle.length, 0);
    assert.equal(seen.length, 0);
  });

  it('emitInitial false reports no mount event, the state is still served', () => {
    const { nodes } = rootNodes();
    const seen = [];
    const sw = createLayoutSwitch({ doc: fakeDoc(nodes), layout: 'tabs', emitInitial: false, onRunStateChange: (n) => seen.push(n) });
    assert.equal(seen.length, 0);
    assert.deepEqual(sw.initial, sw.runState());
    sw.setActiveTab(TAB_LEGACY);
    assert.equal(seen.length, 1, 'the first explicit change is reported once');
  });

});
