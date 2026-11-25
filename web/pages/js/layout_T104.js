// Path: web/pages/js/layout_T104.js | Author: Claude Opus 5.5 head | 2026-11-21
// What/why: T104 owner decision 2026-11-21 (layout = "All") - the root page
// supports three switchable layouts:
//   top (default): the new pools panel first, one status/log area, one worker
//     budget, the legacy lanes in a collapsible section. Both areas run their
//     heavy work while the section is open (it ships open).
//   tabs: two tabs "Pools" and "Legacy lanes"; only the visible tab runs heavy
//     work, the one worker budget is shared by both.
//   pools: pools only; the legacy lanes are hidden and never started.
// Selection: `?layout=top|tabs|pools`, else localStorage `ccgsc_layout`, else
// `top`. An unknown value is `top` (T104c row 3: a query value that names no
// layout answers the default, it does not fall back to the stored one).
//
// This module only decides and renders; it never claims a worker slot and never
// starts a lane. The caller (contribute_page_T104.js) applies the run state to
// the consumers, so the rules stay testable without a DOM.
//
// Truth: WO_T104_STEPS_6_10.md, docs/T104_INTEGRATED_PAGE_PLAN.md,
// owner T104 rules (issue #117).

'use strict';

export const LAYOUTS = Object.freeze(['top', 'tabs', 'pools']);
export const DEFAULT_LAYOUT = 'top';
export const LAYOUT_STORAGE_KEY = 'ccgsc_layout';
export const TAB_POOLS = 'pools';
export const TAB_LEGACY = 'legacy';
export const TABS = Object.freeze([TAB_POOLS, TAB_LEGACY]);

/**
 * Normalise one layout name. Anything that is not one of the three layouts is
 * unknown, and an unknown value means the default.
 *
 * @param {string|null|undefined} value
 * @returns {'top'|'tabs'|'pools'|null} the name, or null when unknown
 */
export function normalizeLayout(value) {
  const text = String(value == null ? '' : value).trim().toLowerCase();
  return LAYOUTS.includes(text) ? text : null;
}

/**
 * Read the persisted layout. A missing store, a blocked store and a junk value
 * all answer null, so the caller falls back to the default.
 *
 * @param {Object} [storage] injectable storage; undefined means localStorage,
 *   null means "no storage at all"
 * @returns {'top'|'tabs'|'pools'|null}
 */
export function readStoredLayout(storage) {
  let store = storage;
  if (store === undefined) {
    try {
      if (typeof localStorage !== 'undefined') store = localStorage;
    } catch (_) { /* storage can throw in privacy modes */ }
  }
  if (!store || typeof store.getItem !== 'function') return null;
  try {
    return normalizeLayout(store.getItem(LAYOUT_STORAGE_KEY));
  } catch (_) {
    return null;
  }
}

/**
 * Resolve the layout of this visit: the query wins, then the persisted value,
 * then the default. An unknown value means `top` (owner rule of 2026-11-21,
 * T104c row 3): a page never runs a layout it cannot name, and an unusable
 * `?layout=` is an unknown effective value, not a silent fallback to the
 * remembered one.
 *
 * @param {string} [search] page query, e.g. "?layout=tabs"
 * @param {Object} [storage] injectable storage (see readStoredLayout)
 * @returns {'top'|'tabs'|'pools'}
 */
export function resolveLayout(search, storage) {
  let params = null;
  if (typeof search === 'string' && search) {
    try { params = new URLSearchParams(search); } catch (_) { params = null; }
  }
  const fromQuery = params ? params.get('layout') : null;
  if (fromQuery !== null && fromQuery !== undefined) {
    return normalizeLayout(fromQuery) || DEFAULT_LAYOUT;
  }
  return readStoredLayout(storage) || DEFAULT_LAYOUT;
}

/**
 * Remember a layout as the default for the next visit. Only a known layout is
 * written; an unknown one is never persisted, so a typo cannot pin the page to
 * a wrong layout.
 *
 * @param {string} layout
 * @param {Object} [storage] injectable storage (see readStoredLayout)
 * @returns {'top'|'tabs'|'pools'} the layout that is in effect
 */
export function persistLayout(layout, storage) {
  const value = normalizeLayout(layout) || DEFAULT_LAYOUT;
  let store = storage;
  if (store === undefined) {
    try {
      if (typeof localStorage !== 'undefined') store = localStorage;
    } catch (_) { /* storage can throw in privacy modes */ }
  }
  if (!store || typeof store.setItem !== 'function') return value;
  try {
    if (readStoredLayout(store) !== value) store.setItem(LAYOUT_STORAGE_KEY, value);
  } catch (_) { /* private mode: the visit still uses the value */ }
  return value;
}

/**
 * The run state of the two areas of the page. Two different questions, and the
 * fields are named for the question they answer:
 *   - `pools` / `legacy`: does that area run heavy work RIGHT NOW. This is the
 *     pair the page applies to the consumers (start/stop of the app, the lane
 *     gate); it is the only field the apply path reads.
 *   - `poolsVisible` / `legacyVisible` / `tabsVisible`: is that element part of
 *     the layout at all, i.e. must it not be `hidden`. A collapsed <details> is
 *     still visible - its summary stays reachable - while it runs nothing, so
 *     `legacyVisible: true, legacy: false` is the normal collapsed state of the
 *     `top` layout. `legacyCollapsible` says whether the legacy area is that
 *     kind of section rather than a tab panel.
 *
 * @param {Object} [state]
 * @param {string} [state.layout]
 * @param {'pools'|'legacy'} [state.activeTab]
 * @param {boolean} [state.legacyOpen] top layout: is the collapsible section open
 * @returns {{layout: string, activeTab: string, pools: boolean, legacy: boolean,
 *   poolsVisible: boolean, legacyVisible: boolean, tabsVisible: boolean,
 *   legacyCollapsible: boolean}}
 */
export function computeAreaRunState(state = {}) {
  const layout = normalizeLayout(state.layout) || DEFAULT_LAYOUT;
  const activeTab = TABS.includes(state.activeTab) ? state.activeTab : TAB_POOLS;
  const legacyOpen = state.legacyOpen !== false;

  if (layout === 'pools') {
    return {
      layout, activeTab,
      pools: true, legacy: false,                 // legacy: no run, and not shown below
      poolsVisible: true, legacyVisible: false,
      tabsVisible: false, legacyCollapsible: false
    };
  }
  if (layout === 'tabs') {
    const onLegacy = activeTab === TAB_LEGACY;
    return {
      layout, activeTab,
      pools: !onLegacy, legacy: onLegacy,         // exactly the visible tab runs
      poolsVisible: !onLegacy, legacyVisible: onLegacy,
      tabsVisible: true, legacyCollapsible: false
    };
  }
  return {
    layout: DEFAULT_LAYOUT, activeTab,
    pools: true, legacy: legacyOpen,            // shown either way; a closed section runs nothing
    poolsVisible: true, legacyVisible: true,
    tabsVisible: false, legacyCollapsible: true
  };
}

function elementById(doc, id) {
  if (!doc || typeof doc.getElementById !== 'function') return null;
  try { return doc.getElementById(id) || null; } catch (_) { return null; }
}

function setHidden(el, hidden) {
  if (!el) return;
  el.hidden = Boolean(hidden);
}

function setClass(el, name, on) {
  if (!el || !el.classList || typeof el.classList.toggle !== 'function') return;
  try { el.classList.toggle(name, Boolean(on)); } catch (_) { /* fake nodes */ }
}

/**
 * Mount the layout switch on the page. Missing nodes are tolerated, so the same
 * wiring runs on the root page (full layout) and on /pools/contribute.html
 * (pools only).
 *
 * @param {Object} [options]
 * @param {Document|Object|null} [options.doc]
 * @param {string} [options.layout]
 * @param {'pools'|'legacy'} [options.activeTab]
 * @param {boolean} [options.legacyOpen]
 * @param {Function} [options.onRunStateChange] called with (next, previous)
 *   whenever the heavy-work state changes, including the first apply unless
 *   options.emitInitial === false (a page that wires the consumers after the
 *   mount applies the initial state itself)
 * @param {boolean} [options.emitInitial] default true
 * @param {Function} [options.onLayoutChange] called with (next, previous) when
 *   the visitor picks another layout through the layout buttons
 * @returns {Object} the switch controller
 */
export function createLayoutSwitch(options = {}) {
  const doc = options.doc !== undefined
    ? options.doc
    : (typeof document !== 'undefined' ? document : null);
  const emitInitial = options.emitInitial !== false;
  let layout = normalizeLayout(options.layout) || DEFAULT_LAYOUT;
  let activeTab = TABS.includes(options.activeTab) ? options.activeTab : TAB_POOLS;
  let legacyOpen = options.legacyOpen !== false;

  const nodes = {
    tabs: elementById(doc, 't104-layout-tabs'),
    tabPools: elementById(doc, 't104-tab-pools'),
    tabLegacy: elementById(doc, 't104-tab-legacy'),
    pools: elementById(doc, 't103-pools-panel'),
    legacy: elementById(doc, 't104-legacy-section'),
    legacySummary: elementById(doc, 't104-legacy-summary'),
    layoutButtons: LAYOUTS.map((name) => elementById(doc, 't104-layout-btn-' + name))
  };

  function runState() {
    return computeAreaRunState({ layout, activeTab, legacyOpen });
  }

  function render(next) {
    if (doc && doc.body && typeof doc.body.setAttribute === 'function') {
      doc.body.setAttribute('data-t104-layout', next.layout);
    }
    setHidden(nodes.tabs, !next.tabsVisible);
    setHidden(nodes.pools, !next.poolsVisible);
    setHidden(nodes.legacy, !next.legacyVisible);

    for (const [tab, el] of [[TAB_POOLS, nodes.tabPools], [TAB_LEGACY, nodes.tabLegacy]]) {
      if (!el) continue;
      if (typeof el.setAttribute === 'function') el.setAttribute('aria-selected', tab === next.activeTab ? 'true' : 'false');
      setClass(el, 't104-tab-active', tab === next.activeTab);
    }

    for (let i = 0; i < LAYOUTS.length; i++) {
      const el = nodes.layoutButtons[i];
      if (!el) continue;
      if (typeof el.setAttribute === 'function') el.setAttribute('aria-pressed', LAYOUTS[i] === next.layout ? 'true' : 'false');
      setClass(el, 't104-layout-active', LAYOUTS[i] === next.layout);
    }

    if (nodes.legacySummary && nodes.legacySummary.style) {
      nodes.legacySummary.style.display = next.legacyCollapsible ? '' : 'none';
    }
    if (nodes.legacy && 'open' in nodes.legacy) {
      // A collapsible section follows its own open state; a tab panel is always
      // open (its visibility is the gate).
      nodes.legacy.open = next.legacyCollapsible ? legacyOpen : true;
    }
  }

  // The last state this switch applied (null before the mount). Every mutation
  // compares against it, so a change is reported exactly once.
  let lastState = null;

  function apply() {
    const next = runState();
    const previous = lastState;
    lastState = next;
    render(next);
    const changed = !previous
      || previous.pools !== next.pools
      || previous.legacy !== next.legacy
      || previous.poolsVisible !== next.poolsVisible
      || previous.legacyVisible !== next.legacyVisible
      || previous.layout !== next.layout;
    if (changed && (previous || emitInitial) && typeof options.onRunStateChange === 'function') {
      try { options.onRunStateChange(next, previous || null); } catch (_) { /* a broken consumer never breaks the page */ }
    }
    return next;
  }

  function onTabClick(tab) {
    if (activeTab === tab) return runState();
    activeTab = tab;
    return apply();
  }

  const onPoolsClick = () => onTabClick(TAB_POOLS);
  const onLegacyClick = () => onTabClick(TAB_LEGACY);
  const onLegacyToggle = () => {
    if (nodes.legacy && 'open' in nodes.legacy) legacyOpen = Boolean(nodes.legacy.open);
    return apply();
  };
  const onLayoutButtons = LAYOUTS.map((name) => () => selectLayout(name));
  if (nodes.tabPools && typeof nodes.tabPools.addEventListener === 'function') {
    nodes.tabPools.addEventListener('click', onPoolsClick);
  }
  if (nodes.tabLegacy && typeof nodes.tabLegacy.addEventListener === 'function') {
    nodes.tabLegacy.addEventListener('click', onLegacyClick);
  }
  if (nodes.legacy && typeof nodes.legacy.addEventListener === 'function') {
    nodes.legacy.addEventListener('toggle', onLegacyToggle);
  }
  for (let i = 0; i < LAYOUTS.length; i++) {
    const el = nodes.layoutButtons[i];
    if (el && typeof el.addEventListener === 'function') el.addEventListener('click', onLayoutButtons[i]);
  }

  function selectLayout(next) {
    const previous = layout;
    layout = normalizeLayout(next) || DEFAULT_LAYOUT;
    const state = apply();
    if (previous !== layout && typeof options.onLayoutChange === 'function') {
      try { options.onLayoutChange(layout, previous); } catch (_) { /* a broken consumer never breaks the page */ }
    }
    return state;
  }

  const initial = apply();

  return {
    /** @returns {'top'|'tabs'|'pools'} */
    getLayout() { return layout; },
    /** @returns {'pools'|'legacy'} */
    getActiveTab() { return activeTab; },
    /** @returns {boolean} */
    getLegacyOpen() { return legacyOpen; },
    /** @returns {Object} the current area run state */
    runState,
    /** The state that was applied on mount. */
    initial,
    /**
     * Show one tab. Only meaningful in the tabs layout; the run state is
     * reported either way.
     *
     * @param {'pools'|'legacy'} tab
     */
    setActiveTab(tab) {
      if (!TABS.includes(tab)) return runState();
      return onTabClick(tab);
    },
    /**
     * Open or close the collapsible legacy section of the top layout.
     *
     * @param {boolean} open
     */
    setLegacyOpen(open) {
      legacyOpen = open !== false;
      if (nodes.legacy && 'open' in nodes.legacy) nodes.legacy.open = legacyOpen;
      return apply();
    },
    /**
     * Switch the layout of this visit and remember the choice.
     *
     * @param {string} next
     * @param {Object} [storage] injectable storage (see persistLayout)
     */
    setLayout(next, storage) {
      persistLayout(next, storage);
      return selectLayout(next);
    },
    /** Apply the current state again (after the DOM changed). */
    refresh() { return apply(); },
    /** Remove the listeners of this switch. */
    dispose() {
      if (nodes.tabPools && typeof nodes.tabPools.removeEventListener === 'function') {
        nodes.tabPools.removeEventListener('click', onPoolsClick);
      }
      if (nodes.tabLegacy && typeof nodes.tabLegacy.removeEventListener === 'function') {
        nodes.tabLegacy.removeEventListener('click', onLegacyClick);
      }
      if (nodes.legacy && typeof nodes.legacy.removeEventListener === 'function') {
        nodes.legacy.removeEventListener('toggle', onLegacyToggle);
      }
      for (let i = 0; i < LAYOUTS.length; i++) {
        const el = nodes.layoutButtons[i];
        if (el && typeof el.removeEventListener === 'function') el.removeEventListener('click', onLayoutButtons[i]);
      }
    },
    nodes
  };
}

// END OF layout_T104.js
