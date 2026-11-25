import { mountAppEgressPanel } from './app_egress_admin_F10b.js';
import { mountDewPanel } from './dew_admin_F12.js';
// Path: web/pages/js/admin_controls_MVPW.js | Author: GLM for Claude Opus 5.5 | 2026-11-19
// What/why: shared admin widgets (task-key rotation, measured metrics panel) plus the
// round-8 T83 additions: the read-only contributor devices panel for the monitor page
// (decided CPU limits + the GPU reports each tab gave) and the /admin/v1/workers reader.
import { request, markMock, getPoolSettings, savePoolSettings } from './api.js';
import { isMockMode, getMockData } from './mock.js';
import { ISOLATION_MODES, DEFAULT_ISOLATION_MODE } from './runner.js';
// R15-FIX-CAPS Addendum (G-R15-3): the editable resource caps of the server.
import {
  CAPS_FIELDS,
  capsBodyFromForm,
  capsFieldValue,
  capsPendingRestart,
  capsSoftLimitNote,
  capsStatusText,
  readCaps,
  renderCapsPanel,
  writeCaps
} from './caps_admin_R15.js';
import { mountRecomputePanel } from './recompute_admin_R17.js';
import {
  CONTRIBUTOR_CONTROLS_MODES,
  DEFAULT_CONTRIBUTOR_CONTROLS,
  contributorControlsFieldFor,
  isolationFieldFor,
  readContributorControls,
  readIsolationMode
} from './schema.js';

// Expected metric names of the admin metrics panel, in panel order. Unmeasured
// names render as "not measured" and never as zero. The gpu_* names are the
// thesis GPU metrics (docs/THESIS_METRICS_PLAN.md GPU rows) recorded on
// settled results: gpu_vs_cpu carries the task kind (gpu vs cpu) in the
// sample source label.
export const METRIC_PANEL_NAMES = [
  'lease_issued', 'lease_expired', 'lease_settled', 'task_latency_ms',
  'canary_pass', 'canary_fail', 'webhook_outbox_delivered', 'webhook_outbox_failed',
  'gpu_adapter_detected', 'gpu_compile_ms', 'gpu_dispatch_ms', 'gpu_vs_cpu'
];

export async function saveAdminConfig(page, settings, reason, pinName, expectedVersion) {
  if (!reason.trim()) throw new Error('A changelog reason is required.');
  return request(`/admin/v1/config/${encodeURIComponent(page)}`, {
    method: 'POST',
    body: JSON.stringify({ settings, reason, pinName: pinName || null, expectedVersion })
  });
}

export async function rotateTaskKey(overlapMinutes) {
  if (!Number.isSafeInteger(overlapMinutes) || overlapMinutes < 0) {
    throw new Error('Overlap must be a non-negative whole number of minutes.');
  }
  return request('/admin/v1/task-keys:rotate', {
    method: 'POST',
    body: JSON.stringify({ overlapMinutes })
  });
}

export async function getAdminMetrics() {
  const data = await request('/admin/v1/metrics');
  const samples = Array.isArray(data) ? data : data?.samples ?? data?.items ?? data?.metrics;
  if (!Array.isArray(samples)) throw new Error('The metrics response does not contain a sample array.');
  return samples;
}

export function metricRows(samples, names) {
  const rows = names.map(name => {
    const sample = samples.filter(item => item && item.name === name).at(-1);
    return { name, value: sample?.value ?? 'not measured', unit: sample?.unit ?? '' };
  });
  for (const sample of samples) {
    if (sample?.name && !names.includes(sample.name)) {
      rows.push({ name: sample.name, value: sample.value ?? 'not measured', unit: sample.unit ?? '' });
    }
  }
  return rows;
}

// --- D-W10-6: the kernel isolation mode, a Director control ---------------------

// ISOLATION_SELECT_ID is the panel select. The value is the page-config field of
// the Director's settings map; saving it writes the field, and every job the tab
// claims from that pool carries the mode to the runner (W10C-SERVER stores it).
export const ISOLATION_SELECT_ID = 'kernel-isolation-mode';

/**
 * The Director-facing label of one isolation mode.
 *
 * @param {string} mode
 * @returns {string}
 */
export function isolationModeLabel(mode) {
  return mode === 'lock-only'
    ? 'lock-only - one worker, capability lock inside it'
    : 'separate+lock - workload in its own worker, shell keeps hash and port (recommended)';
}

/**
 * Normalize a select value. Unknown values answer the strict default.
 *
 * @param {*} value
 * @returns {string}
 */
export function normalizeIsolationChoice(value) {
  const mode = typeof value === 'string' ? value.trim() : '';
  return ISOLATION_MODES.includes(mode) ? mode : DEFAULT_ISOLATION_MODE;
}

// renderIsolationModePanel renders the read-only display plus the Director select.
// The select is disabled without an admin/director session, and it never invents
// a value: an absent setting shows the default it will actually run with.
export function renderIsolationModePanel(content, canAdmin, settings, pageId) {
  if (!content || typeof content.insertAdjacentHTML !== 'function') return null;
  const current = readIsolationMode(settings, pageId);
  const options = ISOLATION_MODES.map(mode =>
    `<option value="${mode}"${mode === current ? ' selected' : ''}>${isolationModeLabel(mode)}</option>`
  ).join('');
  content.insertAdjacentHTML('beforeend', `
    <section class="config-panel" id="kernel-isolation-panel">
      <h2>Kernel isolation mode (D-W10-6)</h2>
      <div class="form-group">
        <label class="form-label" for="${ISOLATION_SELECT_ID}">How the workload runs inside the sandbox</label>
        <select id="${ISOLATION_SELECT_ID}" data-row-id="${isolationFieldFor(pageId)}">
          ${options}
        </select>
        <p class="field-help" id="kernel-isolation-help" role="status">
          The runner applies this to every job of this pool. Unknown or missing values run the default (${DEFAULT_ISOLATION_MODE}).
        </p>
      </div>
    </section>
  `);
  const select = typeof content.querySelector === 'function' ? content.querySelector(`#${ISOLATION_SELECT_ID}`) : null;
  if (select) select.disabled = !canAdmin;
  return { mode: current, select };
}

// --- W10D row 2 (D-W10-11): the contributor controls select ---------------------

// The Director select of the contributor controls. The value is stored by the
// pool settings endpoint with the same expectedVersion rule as verifyMode and
// isolationMode; the contributor tabs read it from their register and heartbeat
// replies.
export const CONTRIBUTOR_CONTROLS_SELECT_ID = 'contributor-controls-mode';
export const CONTRIBUTOR_CONTROLS_STATUS_ID = 'contributor-controls-status';

/**
 * The Director-facing label of one contributor-control mode.
 *
 * @param {string} mode
 * @returns {string}
 */
export function contributorControlsLabel(mode) {
  switch (mode) {
    case 'pauseResume':
      return 'pauseResume - Pause/Resume plus a capacity slider';
    case 'startStopOptIn':
      return 'startStopOptIn - page opens stopped, Start/Stop plus a capacity slider';
    default:
      return 'off - no buttons, connect and compute at once (default)';
  }
}

/**
 * Normalize a select value. Unknown values answer the strict default, off.
 *
 * @param {*} value
 * @returns {string}
 */
export function normalizeContributorControlsChoice(value) {
  const mode = typeof value === 'string' ? value.trim() : '';
  return CONTRIBUTOR_CONTROLS_MODES.includes(mode) ? mode : DEFAULT_CONTRIBUTOR_CONTROLS;
}

// renderContributorControlsPanel renders the Director select. `current` may be a
// pool settings reply ({contributorControls, version}) or a settings map; a
// missing value shows the default the tab will actually run with. The select is
// disabled without an admin/director session.
export function renderContributorControlsPanel(content, canAdmin, current, pageId) {
  if (!content || typeof content.insertAdjacentHTML !== 'function') return null;
  const mode = normalizeContributorControlsChoice(readContributorControls(current, pageId));
  const options = CONTRIBUTOR_CONTROLS_MODES.map(item =>
    `<option value="${item}"${item === mode ? ' selected' : ''}>${contributorControlsLabel(item)}</option>`
  ).join('');
  content.insertAdjacentHTML('beforeend', `
    <section class="config-panel" id="contributor-controls-panel">
      <h2>Contributor controls (D-W10-11)</h2>
      <div class="form-group">
        <label class="form-label" for="${CONTRIBUTOR_CONTROLS_SELECT_ID}">What a contributor tab shows</label>
        <select id="${CONTRIBUTOR_CONTROLS_SELECT_ID}" data-row-id="${contributorControlsFieldFor(pageId)}">
          ${options}
        </select>
        <p class="field-help" id="${CONTRIBUTOR_CONTROLS_STATUS_ID}" role="status">
          Stored by the pool settings endpoint. off (default) is today's page: no buttons, connect and compute at once.
        </p>
      </div>
    </section>
  `);
  const select = typeof content.querySelector === 'function'
    ? content.querySelector(`#${CONTRIBUTOR_CONTROLS_SELECT_ID}`)
    : null;
  if (select) select.disabled = !canAdmin;
  return { mode, select };
}

// The contributor CPU limits the owner already decided (WWPP-R29.4). This is decided
// policy, not a measured metric, so it is shown as static read-only text.
export const CONTRIBUTOR_CPU_LIMITS_TEXT = 'Contributor CPU limits (decided, WWPP-R29.4): workers per tab auto = logical cores - 1, min 1, max 4.';

// getWorkerReports reads the server worker records (T83: gpuSlots + gpus list).
// Mock mode answers from mock.js; every other failure propagates so the caller can
// show 'not measured' instead of inventing data (unmeasured metrics are absent, never zero).
export async function getWorkerReports() {
  if (isMockMode()) {
    markMock('mock mode requested');
    const mocked = getMockData('workers');
    const mockedWorkers = Array.isArray(mocked) ? mocked : mocked?.workers;
    if (!Array.isArray(mockedWorkers)) {
      throw new Error('The mock workers response does not contain a worker array.');
    }
    return mockedWorkers;
  }
  const data = await request('/admin/v1/workers');
  const workers = Array.isArray(data) ? data : data?.workers;
  if (!Array.isArray(workers)) {
    throw new Error('The workers response does not contain a worker array.');
  }
  return workers;
}

// formatReportedGpus renders the gpus list of one worker record; a tab that never
// reported GPUs stays absent (em dash), never zero.
export function formatReportedGpus(worker) {
  const gpus = Array.isArray(worker?.gpus) ? worker.gpus : [];
  if (gpus.length === 0) return '\u2014';
  return gpus.map(gpu => {
    const vendor = gpu?.vendor || 'unknown vendor';
    const architecture = gpu?.architecture || 'unknown architecture';
    return `${vendor} / ${architecture}`;
  }).join('; ');
}

// renderWorkerGpuRows fills the GPU report table body from worker records using
// textContent only, so server data is never interpreted as HTML (FIX-F XSS rule).
// Returns the number of data rows rendered.
export function renderWorkerGpuRows(tbody, workers) {
  if (!tbody || !Array.isArray(workers)) return 0;
  let count = 0;
  for (const worker of workers) {
    if (!worker || typeof worker !== 'object') continue;
    const row = document.createElement('tr');
    const cells = [
      worker.tabId || '\u2014',
      worker.poolId || '\u2014',
      worker.gpuSlots > 0 ? String(worker.gpuSlots) : '\u2014',
      formatReportedGpus(worker)
    ];
    for (const text of cells) {
      const cell = document.createElement('td');
      cell.textContent = text;
      row.appendChild(cell);
    }
    tbody.appendChild(row);
    count++;
  }
  return count;
}

// mountTabGpuPanel appends the read-only devices section to a monitor page. It never
// touches the global error banner: an unauthenticated visitor simply reads 'not measured'.
export function mountTabGpuPanel(content) {
  if (!content || typeof content.insertAdjacentHTML !== 'function') return null;
  content.insertAdjacentHTML('beforeend', `
    <section id="devices-gpu" class="admin-section">
      <div class="admin-section-header">
        <h2 class="admin-section-title">#devices-gpu Contributor devices &amp; GPU reports</h2>
        <span class="range-badge">read-only</span>
      </div>
      <div class="admin-section-body">
        <p id="tab-cpu-limits">${CONTRIBUTOR_CPU_LIMITS_TEXT}</p>
        <p id="tab-gpu-status" role="status">Loading worker GPU reports...</p>
        <div class="table-responsive">
          <table class="data-table" id="tab-gpu-table">
            <thead>
              <tr><th>Tab</th><th>Pool</th><th>GPU slots</th><th>GPUs reported</th></tr>
            </thead>
            <tbody id="tab-gpu-tbody"></tbody>
          </table>
        </div>
      </div>
    </section>
  `);
  const tbody = content.querySelector('#tab-gpu-tbody');
  const status = content.querySelector('#tab-gpu-status');
  return getWorkerReports().then(workers => {
    if (!status) return null;
    if (!workers.length) {
      status.textContent = 'No tabs registered yet: GPU reports are absent until a tab reports them, not zero.';
      return null;
    }
    status.textContent = 'Read-only: from the server worker record gpuSlots + gpus list (T83).';
    renderWorkerGpuRows(tbody, workers);
    return workers.length;
  }).catch(err => {
    if (status) {
      status.textContent = `Not measured: worker GPU reports unavailable (${err?.message || 'admin session required'}).`;
    }
    return null;
  });
}

export function mountAdminControls(content, canAdmin, showError, showSuccess, options = {}) {
  content.insertAdjacentHTML('beforeend', `
    <section class="config-panel" id="task-key-rotation-panel">
      <h2>Task-signing key rotation</h2>
      <label for="task-key-overlap">Overlap minutes</label>
      <input id="task-key-overlap" type="number" min="0" step="1" value="60">
      <button id="task-key-rotate" type="button" class="btn btn-primary">Rotate task-signing key</button>
    </section>
    <section class="config-panel" id="admin-metrics-panel">
      <h2>Measured metrics</h2>
      <p id="admin-metrics-status" role="status">Loading metrics...</p>
      <ul id="admin-metrics-list"></ul>
    </section>
  `);
  const overlap = content.querySelector('#task-key-overlap');
  const button = content.querySelector('#task-key-rotate');
  const status = content.querySelector('#admin-metrics-status');
  const list = content.querySelector('#admin-metrics-list');
  overlap.disabled = !canAdmin;
  button.disabled = !canAdmin;
  button.addEventListener('click', async () => {
    if (button.disabled) return;
    button.disabled = true;
    try {
      if (overlap.value.trim() === '') throw new Error('Overlap minutes are required.');
      await rotateTaskKey(Number(overlap.value));
      showSuccess('Task-signing key rotated.');
    } catch (err) {
      showError(err);
    } finally {
      button.disabled = !canAdmin;
    }
  });
  // D-W10-6: the Director sets the isolation mode here. R11-M6/M7: the pool
  // settings endpoint is its only home. Writing the page config map would only
  // change a mount-time snapshot that the next reload throws away, and without a
  // pageId the old code wrote the field under the literal key "undefined" while
  // acknowledging a save that never happened.
  const pageId = typeof options.pageId === 'string' && options.pageId ? options.pageId : '';
  const isolation = renderIsolationModePanel(content, canAdmin && pageId !== '', options.settings, pageId || undefined);
  const isolationSelect = isolation ? isolation.select : null;
  if (isolationSelect && typeof isolationSelect.addEventListener === 'function') {
    isolationSelect.addEventListener('change', async () => {
      const mode = normalizeIsolationChoice(isolationSelect.value);
      isolationSelect.value = mode;
      if (!pageId) {
        // There is no pool to write to: refuse instead of acknowledging.
        isolationSelect.value = isolation ? isolation.mode : isolationSelect.value;
        if (showError) showError(new Error('Kernel isolation mode: no page id, nothing was saved.'));
        return;
      }
      isolationSelect.disabled = true;
      try {
        const saved = await savePoolSettings(pageId, { isolationMode: mode });
        if (showSuccess) showSuccess(`Kernel isolation mode: ${mode} (version ${saved && saved.version}).`);
      } catch (err) {
        isolationSelect.value = isolation ? isolation.mode : isolationSelect.value;
        if (showError) showError(err);
      } finally {
        isolationSelect.disabled = !canAdmin;
      }
    });
  }
  // W10D row 2 (D-W10-11): the contributor controls live in the pool settings
  // endpoint, so this select loads and saves itself instead of waiting for a
  // config Save. A non-admin sees the value but cannot change it.
  const contributorControls = renderContributorControlsPanel(
    content, canAdmin, options.poolSettings || options.settings, options.pageId);
  const contributorSelect = contributorControls ? contributorControls.select : null;
  const contributorStatus = typeof content.querySelector === 'function'
    ? content.querySelector(`#${CONTRIBUTOR_CONTROLS_STATUS_ID}`)
    : null;
  if (contributorSelect) {
    let contributorVersion = options.poolSettings && Number.isInteger(options.poolSettings.version)
      ? options.poolSettings.version
      : null;
    let contributorCurrent = contributorControls.mode;
    if (canAdmin && !options.poolSettings) {
      Promise.resolve()
        .then(() => getPoolSettings(options.pageId))
        .then((reply) => {
          const mode = normalizeContributorControlsChoice(reply && reply.contributorControls);
          contributorSelect.value = mode;
          contributorCurrent = mode;
          contributorVersion = reply && Number.isInteger(reply.version) ? reply.version : null;
          if (contributorStatus) contributorStatus.textContent = `Active: ${mode} (version ${contributorVersion ?? '?'}).`;
        })
        .catch((err) => {
          if (contributorStatus) contributorStatus.textContent = `Active mode unavailable: ${err && err.message ? err.message : err}.`;
        });
    }
    if (typeof contributorSelect.addEventListener === 'function') {
      contributorSelect.addEventListener('change', async () => {
        const previous = contributorCurrent;
        const mode = normalizeContributorControlsChoice(contributorSelect.value);
        contributorSelect.value = mode;
        contributorSelect.disabled = true;
        try {
          const saved = typeof options.saveContributorControls === 'function'
            ? await options.saveContributorControls(mode, contributorVersion)
            : await savePoolSettings(options.pageId, { contributorControls: mode }, contributorVersion);
          contributorCurrent = mode;
          contributorVersion = saved && Number.isInteger(saved.version) ? saved.version : contributorVersion;
          if (contributorStatus) contributorStatus.textContent = `Saved: ${mode} (version ${contributorVersion ?? '?'}).`;
          if (showSuccess) showSuccess(`Contributor controls: ${mode}.`);
        } catch (err) {
          contributorSelect.value = previous;
          if (contributorStatus) contributorStatus.textContent = `Save failed: ${err && err.message ? err.message : err}.`;
          if (showError) showError(err);
        } finally {
          contributorSelect.disabled = !canAdmin;
        }
      });
    }
  }
  // R17: per-pool reference recompute mode is a separate durable admin setting.
  // The panel reads the effective mode/version and saves with optimistic locking.
  mountRecomputePanel(content, canAdmin, pageId, showError, showSuccess);
  mountAppEgressPanel(content, canAdmin, pageId, showError, showSuccess);
  mountDewPanel(content, canAdmin, pageId, showError, showSuccess);
  // R15-FIX-CAPS Addendum (G-R15-3): the resource caps of the server are editable
  // here, in the server pools settings section. The panel loads the effective caps
  // and saves the four knobs; maxProcs and memLimitMB apply immediately, the
  // worker pool and the connection cap apply after a restart.
  const capsPanel = renderCapsPanel(content, canAdmin, options.caps);
  const capsStatus = capsPanel ? capsPanel.status : null;
  const capsButton = typeof content.querySelector === 'function'
    ? content.querySelector('#caps-save')
    : null;
  if (capsPanel && capsButton) {
    // The panel shows the effective caps of the server when the caller did not
    // pass them. Only an admin reads them: the caps view is behind the admin
    // gate, so a non-admin mount makes no request at all.
    if (!options.caps && canAdmin) {
      Promise.resolve()
        .then(() => readCaps())
        .then((reply) => {
          const panel = capsPanel.panel;
          if (!panel || typeof panel.querySelector !== 'function') return;
          for (const field of CAPS_FIELDS) {
            const el = panel.querySelector(`#caps-${field.name}`);
            if (el) el.value = String(capsFieldValue(reply, field.name));
          }
          const note = panel.querySelector('#caps-soft-limit-note');
          if (note) note.textContent = capsSoftLimitNote(reply);
        })
        .catch((err) => {
          if (capsStatus) capsStatus.textContent = `Caps unavailable: ${err && err.message ? err.message : err}.`;
        });
    }
    capsButton.addEventListener('click', async () => {
      if (capsButton.disabled) return;
      capsButton.disabled = true;
      try {
        const body = capsBodyFromForm(capsPanel.panel);
        if (!Object.keys(body).length) throw new Error('No cap value was given.');
        const saved = await writeCaps(body);
        if (capsStatus) capsStatus.textContent = capsStatusText(saved);
        if (showSuccess) showSuccess(capsStatusText(saved));
      } catch (err) {
        if (capsStatus) capsStatus.textContent = `Save failed: ${err && err.message ? err.message : err}.`;
        if (showError) showError(err);
      } finally {
        capsButton.disabled = !canAdmin;
      }
    });
  }
  if (!canAdmin) {
    status.textContent = 'Admin role required; metrics not measured.';
    return;
  }
  const names = METRIC_PANEL_NAMES;
  getAdminMetrics().then(samples => {
    status.textContent = samples.length ? 'Recorded samples (expected names may differ):' : 'No samples: not measured.';
    for (const row of metricRows(samples, names)) {
      const item = document.createElement('li');
      item.textContent = `${row.name}: ${row.value}${row.unit ? ` ${row.unit}` : ''}`;
      list.appendChild(item);
    }
  }).catch(err => {
    status.textContent = `Metrics unavailable: ${err.message}. Values are not measured.`;
  });
}

// mountContributorCounters shows the monitor totals: accepted tasks and compute
// milliseconds across contributors. A refused read stays unmeasured.
export async function mountContributorCounters(content) {
  if (!content || typeof content.insertAdjacentHTML !== 'function') return null;
  content.insertAdjacentHTML('beforeend', `
    <section id="contributor-counters" class="admin-section">
      <div class="admin-section-header">
        <h2 class="admin-section-title">#contributor-counters Contributor counters</h2>
        <span class="range-badge">read-only</span>
      </div>
      <div class="admin-section-body">
        <p id="contributor-counter-status" role="status">Loading contributor counters...</p>
        <p>Accepted tasks <span id="contributor-counter-tasks">not measured</span></p>
        <p>Compute time <span id="contributor-counter-ms">not measured</span></p>
      </div>
    </section>
  `);
  const status = typeof content.querySelector === 'function' ? content.querySelector('#contributor-counter-status') : null;
  const tasks = typeof content.querySelector === 'function' ? content.querySelector('#contributor-counter-tasks') : null;
  const compute = typeof content.querySelector === 'function' ? content.querySelector('#contributor-counter-ms') : null;
  const unmeasured = () => {
    if (status) status.textContent = 'Contributor totals are not measured.';
    if (tasks) tasks.textContent = 'not measured';
    if (compute) compute.textContent = 'not measured';
  };
  try {
    const data = await request('/admin/v1/contributor-counters');
    const accepted = data && typeof data.acceptedTasks === 'number' && Number.isFinite(data.acceptedTasks);
    const ms = data && typeof data.computeMs === 'number' && Number.isFinite(data.computeMs);
    if (!accepted || !ms) {
      unmeasured();
      return null;
    }
    if (status) status.textContent = 'Totals across contributors.';
    if (tasks) tasks.textContent = String(data.acceptedTasks);
    if (compute) compute.textContent = `${data.computeMs} ms`;
    return data;
  } catch (err) {
    unmeasured();
    return null;
  }
}

// END OF admin_controls_MVPW.js

