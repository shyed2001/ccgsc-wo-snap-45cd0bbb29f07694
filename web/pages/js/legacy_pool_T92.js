// Path: web/pages/js/legacy_pool_T92.js | Author: Codex | 2026-11-20
// What/why: read-only legacy counts and optional worker details, without invented data.
import { request, markMock } from './api.js';
import { isMockMode, getMockData } from './mock.js';

export async function getLegacyWorkers() {
  if (isMockMode()) {
    markMock('mock mode requested');
    return getMockData('legacy-workers');
  }
  return request('/admin/v1/legacy-workers');
}

function row(tbody, values) {
  const tr = document.createElement('tr');
  for (const value of values) {
    const td = document.createElement('td');
    td.textContent = value == null ? 'Not provided' : String(value);
    tr.appendChild(td);
  }
  tbody.appendChild(tr);
}

export function renderLegacyWorkers(content, data) {
  const status = content.querySelector('#legacy-pool-status');
  const counts = content.querySelector('#legacy-pool-counts');
  const clusters = content.querySelector('#legacy-pool-clusters');
  const workers = content.querySelector('#legacy-pool-workers');
  if (!status || !counts || !clusters || !workers) return;
  // Clear any prior snapshot, including on a failed refresh.
  counts.textContent = '';
  clusters.textContent = '';
  workers.textContent = '';
  if (data?.available !== true) {
    status.textContent = `Unavailable: ${data?.reason || 'legacy pool did not answer'}. Counts are not measured.`;
    return;
  }
  status.textContent = `${isMockMode() ? 'MOCK DATA. ' : ''}Read-only Node pool snapshot${data.fetchedAt ? ` fetched at ${data.fetchedAt}` : ''}. ${data.detailsUnavailable || ''}`;
  const labels = {
    totalWorkers: 'Total workers', liveWorkers: 'Live workers', staleWorkers: 'Stale workers',
    clusterCount: 'Cluster groups', distinctDevices: 'Distinct devices', liveTabs: 'Live tabs',
    realClusters: 'Named clusters', unclusteredLive: 'Unclustered live workers'
  };
  for (const [key, label] of Object.entries(labels)) {
    if (typeof data.raw?.[key] === 'number') row(counts, [label, data.raw[key]]);
  }
  for (const [tag, values] of Object.entries(data.raw?.clusters || {})) {
    row(clusters, [tag, values?.total, values?.live, values?.stale]);
  }
  for (const worker of Array.isArray(data.workers) ? data.workers : []) {
    if (!worker || typeof worker !== 'object' || Array.isArray(worker)) continue;
    row(workers, [worker.id, worker.lane, worker.pool, worker.lastSeen, worker.status]);
  }
}

export function mountLegacyPoolPanel(content) {
  if (!content || typeof content.insertAdjacentHTML !== 'function') return null;
  // Static markup only; every upstream string is rendered with textContent.
  content.insertAdjacentHTML('beforeend', `
    <section id="legacy-pool" class="admin-section">
      <div class="admin-section-header">
        <h2 class="admin-section-title">Old Node pool workers</h2>
        <span class="range-badge">read-only</span>
      </div>
      <div class="admin-section-body">
        <p id="legacy-pool-status" role="status">Loading old Node pool workers...</p>
        <div class="table-responsive">
          <table class="data-table"><caption>Reported counts</caption>
            <thead><tr><th>Metric</th><th>Count</th></tr></thead><tbody id="legacy-pool-counts"></tbody></table>
          <table class="data-table"><caption>Cluster / pool tags</caption>
            <thead><tr><th>Tag</th><th>Total</th><th>Live</th><th>Stale</th></tr></thead><tbody id="legacy-pool-clusters"></tbody></table>
          <table class="data-table"><caption>Worker details, when provided by Node</caption>
            <thead><tr><th>ID</th><th>Lane</th><th>Pool tag</th><th>Last seen</th><th>Status</th></tr></thead><tbody id="legacy-pool-workers"></tbody></table>
        </div>
      </div>
    </section>`);
  return getLegacyWorkers().then(data => renderLegacyWorkers(content, data)).catch(err => {
    renderLegacyWorkers(content, { available: false, reason: err?.message || 'admin session required' });
  });
}
