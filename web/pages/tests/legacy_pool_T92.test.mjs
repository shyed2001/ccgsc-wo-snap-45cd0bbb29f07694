// Path: web/pages/tests/legacy_pool_T92.test.mjs | Author: Codex | 2026-11-20
// What/why: prove honest counts, unavailable states, placement and XSS-safe rendering.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { getLegacyWorkers, mountLegacyPoolPanel, renderLegacyWorkers } from '../js/legacy_pool_T92.js';

function element() {
  return {
    children: [], value: '',
    set textContent(value) { this.value = value; this.children = []; },
    get textContent() { return this.value; },
    set innerHTML(_) { throw new Error('Untrusted HTML assignment'); },
    appendChild(child) { this.children.push(child); }
  };
}
function harness(t) {
  const nodes = Object.fromEntries(['status', 'counts', 'clusters', 'workers'].map(id => [`#legacy-pool-${id}`, element()]));
  const inserted = [];
  const content = { querySelector: id => nodes[id], insertAdjacentHTML: (pos, html) => inserted.push({pos, html}) };
  globalThis.document = { createElement: element };
  t.after(() => { delete globalThis.document; delete globalThis.fetch; delete globalThis.window; });
  return { content, nodes, inserted };
}
const response = (body, status = 200) => ({ ok: status < 400, status, headers: { get: () => 'application/json' }, json: async () => body });
const cells = el => el.children.map(tr => tr.children.map(td => td.textContent));

test('T92 panel fetches read-only bridge and renders supplied zero and absent counts honestly', async t => {
  const { content, nodes, inserted } = harness(t);
  globalThis.fetch = async (url, options) => {
    assert.equal(url, '/admin/v1/legacy-workers');
    assert.equal(options.method || 'GET', 'GET');
    assert.equal(options.credentials, 'same-origin');
    return response({ available: true, fetchedAt: '2026-11-20T00:00:00Z', workers: [],
      detailsUnavailable: 'Worker IDs are not provided.', raw: { totalWorkers: 2, liveWorkers: 0, clusters: { alpha: { total: 2 } } } });
  };
  await mountLegacyPoolPanel(content);
  assert.equal(inserted.length, 1);
  assert.equal(inserted[0].pos, 'beforeend');
  assert.match(inserted[0].html, /Old Node pool workers/);
  assert.doesNotMatch(inserted[0].html, /<button|<input/);
  assert.deepEqual(cells(nodes['#legacy-pool-counts']), [['Total workers', '2'], ['Live workers', '0']]);
  assert.deepEqual(cells(nodes['#legacy-pool-clusters']), [['alpha', '2', 'Not provided', 'Not provided']]);
  assert.deepEqual(cells(nodes['#legacy-pool-workers']), []);
  assert.match(nodes['#legacy-pool-status'].textContent, /Worker IDs are not provided/);
});

test('T92 unavailable clears prior snapshot and never falls back to mock counts', async t => {
  const { content, nodes } = harness(t);
  renderLegacyWorkers(content, { available: true, raw: { totalWorkers: 5 }, workers: [{ id: 'old' }] });
  assert.equal(nodes['#legacy-pool-counts'].children.length, 1);
  globalThis.fetch = async () => response({ available: false, reason: 'upstream_unavailable', raw: { totalWorkers: 0 } });
  await mountLegacyPoolPanel(content);
  assert.match(nodes['#legacy-pool-status'].textContent, /Unavailable: upstream_unavailable/);
  for (const id of ['counts', 'clusters', 'workers']) assert.deepEqual(nodes[`#legacy-pool-${id}`].children, []);
  globalThis.fetch = async () => response({ title: 'Unauthorized', detail: 'admin session required' }, 401);
  await mountLegacyPoolPanel(content);
  assert.match(nodes['#legacy-pool-status'].textContent, /Unavailable:/);
  assert.equal(nodes['#legacy-pool-counts'].children.length, 0);
});

test('T92 worker fields, tags, reasons and timestamps are text, never HTML', t => {
  const { content, nodes } = harness(t);
  const attack = '<img src=x onerror=alert(1)>';
  renderLegacyWorkers(content, { available: true, fetchedAt: attack, detailsUnavailable: attack,
    raw: { clusters: { [attack]: { total: 1 } } },
    workers: [{ id: attack, lane: attack, pool: attack, lastSeen: attack, status: attack }, null] });
  assert.deepEqual(cells(nodes['#legacy-pool-workers']), [[attack, attack, attack, attack, attack]]);
  assert.equal(cells(nodes['#legacy-pool-clusters'])[0][0], attack);
  assert.ok(nodes['#legacy-pool-status'].textContent.includes(attack));
  renderLegacyWorkers(content, { available: false, reason: attack });
  assert.ok(nodes['#legacy-pool-status'].textContent.includes(attack));
});

test('T92 explicit mock mode uses mock.js and labels the panel without fetching', async t => {
  const { content, nodes } = harness(t);
  globalThis.window = { location: { search: '?mock=1' } };
  globalThis.fetch = async () => { throw new Error('mock must not dial'); };
  const data = await getLegacyWorkers();
  assert.equal(data.available, true);
  assert.equal(data.raw.totalWorkers, 3);
  assert.deepEqual(data.workers, []);
  await mountLegacyPoolPanel(content);
  assert.match(nodes['#legacy-pool-status'].textContent, /^MOCK DATA/);
  assert.equal(nodes['#legacy-pool-counts'].children.length, 4);
});

test('T92 combined monitor mounts the legacy panel immediately after T83 GPU panel', () => {
  const app = readFileSync(new URL('../js/app.js', import.meta.url), 'utf8');
  assert.match(app, /mountTabGpuPanel\(content\);\s*if \(this\.pageId === 'monitor'\) mountLegacyPoolPanel\(content\);/);
});
