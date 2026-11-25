// Path: web/pages/tests/layout_pages_T104.test.mjs | Author: Claude Opus 5.5 head | 2026-11-21
// What/why: T104 steps 7/10 - the page wiring of the layout switch and the one
// entry, checked on the files themselves: the root page carries the three layout
// buttons, the two tabs, the collapsible legacy section and exactly one
// startContributorPage() script; the standalone page pins its layout and loads
// the same entry; the old unconditional lane-router script is gone and every
// module carries its header and footer. Source checks only - no browser.

import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const webPagesDir = path.join(here, '..');
// tmp lane copies keep a sibling prime_public/; the repo keeps the same file in
// prime_distributed_projectPrlCc1B_V5_10K/public/ (merge 2026-11-21).
const primeCandidates = [
  path.join(webPagesDir, '..', '..', '..', 'prime_public'),
  path.join(webPagesDir, '..', '..', '..', 'prime_distributed_projectPrlCc1B_V5_10K', 'public'),
];
const primeDir = primeCandidates.find((d) => fs.existsSync(path.join(d, 'worker.html'))) || primeCandidates[0];
const rootWorkerPath = path.join(primeDir, 'worker.html');

function read(file) {
  return fs.readFileSync(file, 'utf8');
}

describe('T104 page wiring', () => {

  it('the root page carries the layout bar, the two tabs and both areas', () => {
    const html = read(rootWorkerPath);
    assert.ok(html.includes('id="t104-layout-bar"'), 'the layout bar of the owner decision');
    for (const name of ['top', 'tabs', 'pools']) {
      assert.ok(html.includes(`id="t104-layout-btn-${name}"`), `the ${name} layout button`);
    }
    assert.ok(html.includes('id="t104-layout-tabs"'), 'the tab bar of the tabs layout');
    assert.ok(html.includes('id="t104-tab-pools"'), 'the Pools tab');
    assert.ok(html.includes('id="t104-tab-legacy"'), 'the Legacy lanes tab');
    // The new pools panel first, the legacy lanes below (markup, not CSS).
    const poolsPanel = html.indexOf('<details id="t103-pools-panel"');
    const legacyPanel = html.indexOf('<details id="t104-legacy-section"');
    assert.ok(poolsPanel > 0 && legacyPanel > 0);
    assert.ok(poolsPanel < legacyPanel, 'new pools panel first, legacy lanes below');
    assert.ok(/<details id="t104-legacy-section"[^>]*\bopen\b/.test(html), 'it ships open (top layout runs it)');
    assert.ok(html.includes('id="log-output"'), 'the one status/log area');
    assert.ok(html.includes('id="worker-count-select"'), 'the budget control stays');
  });

  it('the root page boots through exactly one entry and no old path', () => {
    const html = read(rootWorkerPath);
    assert.equal(html.split('await page.startContributorPage(').length - 1, 1, 'one startContributorPage call');
    assert.ok(html.includes('/pools/js/contribute_page_T104.js'), 'the one entry module');
    assert.equal(html.includes('startPoolsBridge('), false, 'the T103 boot path is retired');
    assert.equal(html.includes('./worker_lane_multi.mjs'), false, 'the router is no longer imported directly');
    assert.ok(html.includes('BEGIN CCGSC-R16A-LANE-ROUTER-20260802'), 'the old block stays documented');
    assert.equal(html.includes('id="btn-toggle-compute"'), false, 'no Start button (T105)');
    assert.ok(html.includes('optInGate: true'), 'the T103 opt-in survives the cutover');
    assert.ok(html.includes('window.CCGSC_IS_PHONE'), 'the phone rule of the budget receives its hint');
    assert.ok(html.includes('window.CCGSC_IS_PHONE = contributeMod.detectIsPhone()'), 'from the app module');
  });

  it('the standalone page pins the pools-only layout and loads the same entry', () => {
    const html = read(path.join(webPagesDir, 'contribute.html'));
    assert.ok(html.includes('startContributorPage({ forcedLayout: \'pools\''), 'pinned layout');
    assert.ok(html.includes('./js/contribute_page_T104.js'), 'the one entry module');
    assert.ok(html.includes('src="./js/contribute.js"'), 'the app module stays loaded');
    assert.ok(html.includes('window.CCGSC_IS_PHONE'), 'the phone rule of the budget receives its hint');
    assert.equal(html.includes('isPhone: false'), false, 'the page never pins the device type');
    assert.ok(html.includes('<!-- END OF contribute.html -->'));
  });

  it('every new module has a header and a footer', () => {
    const modules = [
      ['js', 'layout_T104.js'],
      ['js', 'contribute_page_T104.js'],
      ['js', 'artifact_cache_T104.js']
    ];
    for (const [dir, file] of modules) {
      const source = read(path.join(webPagesDir, dir, file));
      assert.ok(source.includes('// Path: web/pages/' + dir + '/' + file), `${file} header`);
      assert.ok(source.includes('END OF ' + file), `${file} footer`);
      assert.ok(source.includes('T104'), `${file} names its ticket`);
    }
  });

  it('the layout module owns the three names and nothing else', () => {
    const source = read(path.join(webPagesDir, 'js', 'layout_T104.js'));
    assert.ok(source.includes("export const LAYOUTS = Object.freeze(['top', 'tabs', 'pools'])"));
    assert.ok(source.includes("export const DEFAULT_LAYOUT = 'top'"));
    assert.ok(source.includes("export const LAYOUT_STORAGE_KEY = 'ccgsc_layout'"));
  });

  it('every layout name of the switch exists in the page markup', () => {
    const html = read(rootWorkerPath);
    // The button ids follow the LAYOUTS list of layout_T104.js: the markup and
    // the switch may never drift apart.
    for (const name of ['top', 'tabs', 'pools']) {
      assert.ok(html.includes(`id="t104-layout-btn-${name}"`), `the page carries the ${name} button`);
    }
  });

});
