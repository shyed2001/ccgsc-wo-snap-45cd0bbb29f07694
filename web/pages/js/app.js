// WO-HC-WEB-01 app.js - Universal Page Controller, Form Generator & SSE Live Metric Binder
// Author: hermes MiniMax-M3 for Claude Opus 5.5 | 2026-11-17 (WO-T2.2: canonical nav path,
//   live/mock badge, one nav per page).
// Original author: Claude Opus 5.5 head / agy Gemini 3.8 Flash high. Date: 2026-11-17.
// Truth: F:/tmp/hcwork/ADMIN_MANUALS_SOURCE.md + ctx_rows_R37_R40.md

import { PAGE_SCHEMAS, validateRowValue, isIsolationField, isContributorControlsField, isDewWindowField } from './schema.js';
import { getMetrics, getSessionState, request, markLive, getAuditLog, connectEvents, displayError, clearError, renderDataSourceBadge, isMockData, esc } from './api.js';
import { mountAdminControls, mountContributorCounters, mountTabGpuPanel, saveAdminConfig } from './admin_controls_MVPW.js';
// T92 | Codex | 2026-11-20: additive legacy panel below the GPU reports.
import { mountLegacyPoolPanel } from './legacy_pool_T92.js';
// T91 / R42.8 | Codex | 2026-11-20: Redis controls and observed status.
import { REDIS_PAGES, redisApplyNote, redisStatusText } from './redis_admin_T91.js';

// coerceRowValue converts one form value into the JSON type its schema row declares
// (row.jsonType). The T83 GPU controls must reach /admin/v1/config/{page} as real JSON
// booleans and integers, because pool.ValidateGPUAdminConfig (internal/pool/policy_F5.go)
// rejects the 'on'/'off' strings of the plain toggle inputs with invalid-config.
// Rows without a jsonType keep the historical wire format untouched.
export function coerceRowValue(row, value) {
  if (!row || !row.jsonType) return value;
  if (row.jsonType === 'boolean') {
    if (typeof value === 'boolean') return value;
    const text = String(value).toLowerCase();
    if (text === 'on' || text === 'true' || text === '1') return true;
    if (text === 'off' || text === 'false' || text === '0' || text === '') return false;
    return value;
  }
  if (row.jsonType === 'integer') {
    if (value === '' || value === undefined || value === null) return value;
    const num = Number(value);
    // Redis validation must see fractional input and reject it, never round it.
    if (row.id?.startsWith('redis.')) return Number.isFinite(num) ? num : value;
    return Number.isFinite(num) ? Math.trunc(num) : value;
  }
  return value;
}

// Detailed anchor map linking monitor brief numbers to exact control sections (WWPP-R40.1)
export const DETAIL_ANCHOR_MAP = {
  'mon.cap.contributors': 'pool-monitor.html#devices',
  'mon.cap.tabs': 'pool-monitor.html#devices',
  'mon.cap.cpu_workers': 'thread-pool-admin.html#workers',
  'mon.cap.gpu_workers': 'thread-pool-admin.html#gpu',
  'mon.cap.free_slots_thread': 'thread-pool-admin.html#scheduling',
  'mon.cap.free_slots_process': 'process-pool-admin.html#scheduling',
  'mon.work.running': 'pool-monitor.html#work',
  'mon.work.queued': 'pool-monitor.html#queue',
  'mon.work.failed': 'pool-monitor.html#errors',
  'mon.work.tasks_per_s': 'pool-monitor.html#work',
  'mon.work.avg_task_time': 'pool-monitor.html#work',
  'mon.trust.hash_fails': 'thread-pool-admin.html#verification',
  'mon.trust.rerun_mismatch': 'thread-pool-admin.html#verification',
  'mon.trust.canary_fails': 'thread-pool-admin.html#verification',
  'mon.trust.quarantined': 'thread-pool-admin.html#dew',
  'mon.trust.banned': 'thread-pool-admin.html#verification',
  'mon.trust.window_hours': 'thread-pool-admin.html#verification',
  'mon.api.req_per_min': 'api-monitor.html#traffic',
  'mon.api.4xx': 'api-monitor.html#errors',
  'mon.api.5xx': 'api-monitor.html#errors',
  'mon.api.429': 'thread-api-admin.html#quotas',
  'mon.api.p95': 'api-monitor.html#traffic',
  'mon.sys.cpu': 'api-monitor.html#system',
  'mon.sys.ram': 'api-monitor.html#system',
  'mon.sys.disk': 'api-monitor.html#system',
  'mon.sys.pg_conns': 'api-monitor.html#system',
  'mon.sys.sse_conns': 'api-monitor.html#system',
  'mon.alert.level': 'pool-monitor.html#errors',
  'mon.alert.refresh': 'pool-monitor.html#capacity',
  'mon.alert.auto': 'pool-monitor.html#capacity',
  'mon.link.numbers': 'pool-monitor.html#control',
  'mon.link.target': 'pool-monitor.html#control',
  'mon.p95.window': 'api-monitor.html#traffic',
  'mon.metrics.read': 'thread-pool-admin.html#metrics'
};

export class CCGSCPageApp {
  constructor(pageId) {
    this.pageId = pageId;
    this.schema = PAGE_SCHEMAS[pageId];
    this.configData = null;
    this.eventSource = null;
    this.currentSettings = {};
    this.errorBanner = null;
    this.successBanner = null;
  }

  async init() {
    if (!this.schema) {
      console.error(`Unknown page schema: ${this.pageId}`);
      return;
    }

    this.errorBanner = document.getElementById('error-banner');
    this.successBanner = document.getElementById('success-banner');

    // Initialize Shared Nav Bar if available
    this.initNavBar();

    // Render static page skeleton, jump bar, and manual footer
    this.renderHeader();
    this.renderSectionNav();
    this.renderManualSection();

    // Differentiate between Monitor pages and Admin Config pages
    if (this.schema.tier === 1 || this.schema.tier === 2) {
      await this.initMonitorPage();
    } else {
      await this.initAdminPage();
    }

    // The badge is drawn last, so it also covers values that arrived over SSE.
    this.showDataSourceBadge();
  }

  initNavBar() {
    // Derive nav roles from real login state hook (window.CCGSC_NAV_ROLES or nav.js getNavRoles).
    // Never force admin roles for anonymous visitors.
    const roles = (typeof window !== 'undefined' && Array.isArray(window.CCGSC_NAV_ROLES))
      ? window.CCGSC_NAV_ROLES
      : ((typeof window !== 'undefined' && window.CCGSC_NAV && typeof window.CCGSC_NAV.getNavRoles === 'function')
          ? window.CCGSC_NAV.getNavRoles()
          : []);
    const navEl = (typeof document !== 'undefined') ? document.getElementById('ccgsc-nav') : null;
    if (navEl && window.CCGSC_NAV) {
      // One nav for every page: the canonical web/nav.js served at /nav/nav.js.
      window.CCGSC_NAV.initNav({
        container: navEl,
        url: '/nav/nav.json',
        roles
      }).catch(console.warn);
    }
  }

  // showDataSourceBadge renders the small MOCK DATA badge when a value came from mock.js.
  showDataSourceBadge() {
    renderDataSourceBadge();
    return isMockData();
  }

  showSuccess(msg) {
    if (!this.successBanner) return;
    this.successBanner.textContent = msg;
    this.successBanner.classList.add('visible');
    setTimeout(() => {
      this.successBanner.classList.remove('visible');
    }, 4000);
  }

  renderHeader() {
    const el = document.getElementById('page-header-container');
    if (!el) return;
    el.innerHTML = `
      <div class="page-header">
        <div>
          <h1 class="page-title">
            ${this.schema.title}
            <span class="tier-badge tier-${this.schema.tier}">Tier ${this.schema.tier}</span>
          </h1>
          <p class="page-desc">${this.schema.purpose}</p>
        </div>
        <div>
          <a href="${this.schema.manualHref}" class="btn btn-secondary" target="_blank" rel="noopener">
            📖 View Full Manual Page
          </a>
        </div>
      </div>
    `;
  }

  renderSectionNav() {
    const el = document.getElementById('section-nav-container');
    if (!el || !this.schema.sections) return;
    const linksHtml = this.schema.sections
      .map(s => `<a href="#${s.id}">#${s.id} (${s.label})</a>`)
      .join('');
    el.innerHTML = `<nav class="section-nav" aria-label="Section navigation">${linksHtml}</nav>`;
  }

  renderManualSection() {
    const el = document.getElementById('manual');
    if (!el) return;
    el.innerHTML = `
      <div class="manual-footer-section">
        <h3>
          <span>#manual — ${this.schema.title} Manual Reference</span>
          <a href="${this.schema.manualHref}" class="btn btn-secondary" style="font-size: 0.8rem;">Full Manual Page &rarr;</a>
        </h3>
        <p>${this.schema.purpose}</p>
        <p style="margin-top: 8px; font-size: 0.85rem; color: var(--text-dim);">
          Every setting on this page is versioned in <code>pool_config_version</code> with JSON Schema validation and audit trail (WWPP-R38.6, WWPP-R40.2).
          Hover over any <strong>(?)</strong> tooltip to inspect parameter definitions.
        </p>
      </div>
    `;
  }

  // --- MONITOR PAGES ---
  async initMonitorPage() {
    const content = document.getElementById('main-content');
    if (!content) return;

    // Build metric cards grouped by sections
    let html = '';
    for (const section of this.schema.sections) {
      if (section.id === 'manual') continue;
      // D-W10-6/D-W10-11: the isolation mode and the contributor controls have
      // their own Director panels; rendering them in the generic form too would
      // create a second, possibly disagreeing control.
      const sectionRows = this.schema.rows.filter(r =>
        r.section === section.id && !isIsolationField(r.id) && !isContributorControlsField(r.id) && !isDewWindowField(r.id));
      if (sectionRows.length === 0) continue;

      html += `
        <section id="${section.id}" class="admin-section">
          <div class="admin-section-header">
            <h2 class="admin-section-title">#${section.id} ${section.label}</h2>
            ${section.detailHref ? `<a href="${section.detailHref}" class="btn btn-secondary" style="font-size: 0.8rem;">View Detail &rarr;</a>` : ''}
          </div>
          <div class="admin-section-body">
            <div class="metrics-grid">
              ${sectionRows.map(r => this.renderMetricCard(r, section.detailHref)).join('')}
            </div>
          </div>
        </section>
      `;
    }
    content.innerHTML = html;
	content.insertAdjacentHTML('beforeend', '<p id="redis-current-status" role="status">Redis status unavailable.</p>');
	await this.refreshRedisStatus();

    // T83 (round 8): read-only contributor CPU limits and the GPU reports each tab
    // gave at registration; absent reports stay absent, never zero.
    mountContributorCounters(content);
    mountTabGpuPanel(content);
    if (this.pageId === 'monitor') mountLegacyPoolPanel(content);

    // Fill the cards once from the live-first metrics endpoint, so the badge is
    // already correct before the stream starts.
    await this.loadInitialMetrics();

    // Connect to live SSE numbers
    this.connectLiveMetrics();
  }

  // loadInitialMetrics reads the metrics once; getMetrics() prefers the real
  // pools-core endpoints and falls back to mock.js, which raises the badge.
  async loadInitialMetrics() {
    try {
      const metrics = await getMetrics();
      this.updateLiveMetrics(metrics);
    } catch (err) {
      displayError(this.errorBanner, err);
    }
    this.showDataSourceBadge();
  }

  async refreshRedisStatus() {
    try {
      this.redisStatus = await request('/admin/v1/redis/status');
    } catch (_) {
      this.redisStatus = null;
    }
    const el = document.getElementById('redis-current-status');
    if (el) el.textContent = redisStatusText(this.redisStatus);
  }

  renderMetricCard(row, defaultDetailHref) {
    const targetHref = DETAIL_ANCHOR_MAP[row.id] || defaultDetailHref || '#';
    const rowKey = row.id.replace(/\./g, '-');
    const tipId = `tip-mon-${rowKey}`;
    return `
      <div class="metric-card" id="card-${rowKey}">
        <div class="metric-card-header">
          <span class="metric-card-label">
            <a href="${esc(targetHref)}" class="metric-link" aria-describedby="${tipId}">${esc(row.label)}</a>
            <span class="tooltip-icon" id="${tipId}" tabindex="0" role="tooltip" aria-label="${esc(row.legend)}" title="${esc(row.legend)}">(?)</span>
          </span>
          <span class="range-badge">${esc(row.type)}</span>
        </div>
        <a href="${esc(targetHref)}" class="metric-card-value-link" style="text-decoration: none; color: inherit;">
          <div class="metric-card-value" id="val-${rowKey}">—</div>
        </a>
        <div class="metric-card-sub">
          ${esc(row.wwppId)} &bull; ${esc(row.whoMayChange)} &bull; <a href="${esc(targetHref)}" style="color: inherit;">&rarr; ${targetHref.split('#')[1] ? '#' + esc(targetHref.split('#')[1]) : ''}</a>
        </div>
      </div>
    `;
  }

  connectLiveMetrics() {
    try {
      let pool = 'both';
      if (this.pageId.includes('process')) pool = 'process-pool';
      else if (this.pageId.includes('thread')) pool = 'thread-pool';
      this.eventSource = connectEvents(
        (data) => this.updateLiveMetrics(data),
        (err) => console.warn('SSE notification:', err),
        { pool }
      );
    } catch (err) {
      displayError(this.errorBanner, err);
    }
    // The stream may have fallen back to mock data; keep the badge in step.
    this.showDataSourceBadge();
  }

  updateLiveMetrics(metrics) {
    if (!metrics) return;
    for (const [key, val] of Object.entries(metrics)) {
      const valEl = document.getElementById(`val-${key.replace(/\./g, '-')}`);
      if (valEl) {
        valEl.textContent = val;
      }
    }
  }

  // --- ADMIN PAGES ---
  async initAdminPage() {
    const content = document.getElementById('main-content');
    if (!content) return;

    const session = await getSessionState();
    const roles = session?.user?.roles;
    const canAdmin = session?.authenticated === true && Array.isArray(roles) &&
      roles.some(role => role === 'admin' || role === 'director');
    this.canWriteConfig = false;
    this.configData = null;
    this.currentSettings = {};
    try {
      if (!canAdmin) throw new Error('An admin or director session is required.');
      this.configData = await request(`/admin/v1/config/${encodeURIComponent(this.pageId)}`);
      markLive(`pools-core served /admin/v1/config/${this.pageId}`);
      this.currentSettings = { ...(this.configData.settings || {}) };
      this.canWriteConfig = true;
    } catch (err) {
      displayError(this.errorBanner, err);
    }
    this.redisStatus = null;
    if (canAdmin && REDIS_PAGES.includes(this.pageId)) {
      await this.refreshRedisStatus();
      // Both pages display the one active server policy, even if their own
      // historical version last changed a different group of settings.
      if (this.redisStatus?.enabled && this.redisStatus.desired) {
        Object.assign(this.currentSettings, this.redisStatus.desired);
      }
    }
    this.renderAdminForms();
    this.renderConfigVersionTable();
    if (!this.canWriteConfig) {
      content.querySelectorAll('input, select, textarea, button').forEach(el => { el.disabled = true; });
    }
    mountAdminControls(content, canAdmin, err => displayError(this.errorBanner, err), msg => this.showSuccess(msg),
      { pageId: this.pageId, settings: this.currentSettings });
  }

  renderAdminForms() {
    const content = document.getElementById('main-content');
    if (!content) return;

    let html = '';
    for (const section of this.schema.sections) {
      if (section.id === 'manual') continue;
      // D-W10-6/D-W10-11: the isolation mode and the contributor controls have
      // their own Director panels; rendering them in the generic form too would
      // create a second, possibly disagreeing control.
      const sectionRows = this.schema.rows.filter(r =>
        r.section === section.id && !isIsolationField(r.id) && !isContributorControlsField(r.id) && !isDewWindowField(r.id));
      if (sectionRows.length === 0) continue;

      html += `
        <section id="${section.id}" class="admin-section">
          <div class="admin-section-header">
            <h2 class="admin-section-title">#${section.id} ${section.label}</h2>
            <span class="range-badge">${sectionRows.length} settings</span>
          </div>
          <div class="admin-section-body">
            ${section.id === 'redis' ? `<p id="redis-current-status" role="status">${esc(redisStatusText(this.redisStatus))}</p><p id="redis-apply-note">${esc(redisApplyNote(this.redisStatus))}</p>` : ''}
            <div class="form-grid">
              ${sectionRows.map(r => this.renderFormField(r)).join('')}
            </div>
          </div>
        </section>
      `;
    }

    // Config Version & Save Bar
    html += `
      <section id="config-actions-panel" class="config-panel">
        <h2 style="font-size: 1.2rem; color: #fff; margin-bottom: 8px;">Apply Versioned Configuration (WWPP-R38.6)</h2>
        <p style="color: var(--text-muted); font-size: 0.88rem; margin-bottom: 16px;">
          Every Save creates a new immutable version in PostgreSQL. New jobs adopt it instantly; running jobs keep their pinned version.
        </p>

        <div class="form-grid" style="grid-template-columns: 2fr 1fr;">
          <div class="form-group">
            <label class="form-label" for="save-reason">Changelog Reason (Required by Audit Policy)*</label>
            <input type="text" id="save-reason" placeholder="e.g. Adjusted worker threshold for thesis benchmark run" required>
          </div>
          <div class="form-group">
            <label class="form-label" for="pin-name">Pin Name (Optional, e.g. thesis run)</label>
            <input type="text" id="pin-name" placeholder="e.g. thesis-run-01">
          </div>
        </div>

        <div class="config-actions">
          <button type="button" id="btn-save-config" class="btn btn-primary">
            💾 Save Version ${((this.configData?.version ?? 0) + 1)}
          </button>
          <span style="font-size: 0.85rem; color: var(--text-muted);">
            Active Version: <strong>${this.configData ? `v${this.configData.version}` : 'unavailable'}</strong>
          </span>
        </div>
      </section>

      <section id="versions-history-panel" class="config-panel">
        <h3 style="font-size: 1.1rem; color: #fff; margin-bottom: 12px;">Configuration Version History & One-Click Rollback</h3>
        <div id="version-table-container" class="table-responsive"></div>
      </section>
    `;

    content.innerHTML = html;

    // Attach event listeners for inputs and save
    this.attachFormListeners();
  }

  renderFormField(row) {
    // With no admin applier, show the observed read-only line, never pretend
    // saved/default desired values are the live Redis configuration.
    if (row.id.startsWith('redis.') && !this.redisStatus?.enabled) return '';
    const rawVal = this.currentSettings[row.id] !== undefined ? this.currentSettings[row.id] : (row.default === 'TBD' ? '' : row.default);
    const rowKey = row.id.replace(/\./g, '-');
    const fieldId = `field-${rowKey}`;
    const tipId = `tip-${rowKey}`;
    const errId = `err-${rowKey}`;
    let controlHtml = '';

    if (row.type === 'toggle') {
      const isChecked = rawVal === 'on' || rawVal === true || rawVal === 'true';
      controlHtml = `
        <label class="toggle-wrapper" for="${fieldId}">
          <span class="toggle-switch">
            <input type="checkbox" id="${fieldId}" data-row-id="${esc(row.id)}" aria-describedby="${tipId} ${errId}" ${isChecked ? 'checked' : ''}>
            <span class="toggle-slider"></span>
          </span>
          <span id="label-state-${rowKey}" style="font-size: 0.85rem; font-family: monospace;">${isChecked ? 'ON' : 'OFF'}</span>
        </label>
      `;
    } else if (row.type === 'button') {
      controlHtml = `
        <button type="button" class="btn btn-secondary" id="btn-action-${rowKey}" aria-describedby="${tipId}" style="margin-top: 6px;">
          ${esc(row.label)}
        </button>
      `;
    } else if (row.type === 'multi-select') {
      const allowed = row.range.split('/').map(s => s.trim());
      const selected = Array.isArray(rawVal)
        ? rawVal.map(String)
        : (typeof rawVal === 'string' && rawVal ? rawVal.split(',').map(s => s.trim()) : []);
      const checkboxes = allowed.map(opt => {
        const isChecked = selected.includes(opt);
        return `
          <label style="display: inline-flex; align-items: center; margin-right: 12px; margin-bottom: 4px; gap: 4px; font-size: 0.88rem;">
            <input type="checkbox" name="${fieldId}" value="${esc(opt)}" data-row-id="${esc(row.id)}" aria-describedby="${tipId} ${errId}" ${isChecked ? 'checked' : ''}>
            <span>${esc(opt)}</span>
          </label>
        `;
      }).join('');
      controlHtml = `
        <div id="${fieldId}" class="multi-select-group" data-row-id="${esc(row.id)}" style="display: flex; flex-wrap: wrap; margin-top: 4px;">
          ${checkboxes}
        </div>
      `;
    } else if (row.type === 'enum' || (row.range.includes('/') && !/\d+-\d+/.test(row.range))) {
      const allowed = row.range.split('/').map(s => s.trim());
      const hasMatch = allowed.includes(String(rawVal));
      const placeholderHtml = !hasMatch
        ? `<option value="" disabled ${rawVal === '' ? 'selected' : ''}>-- Select --</option>`
        : '';
      const options = allowed.map(opt => {
        const selected = (hasMatch && String(rawVal) === opt) ? 'selected' : '';
        return `<option value="${esc(opt)}" ${selected}>${esc(opt)}</option>`;
      }).join('');
      controlHtml = `
        <select id="${fieldId}" data-row-id="${esc(row.id)}" aria-describedby="${tipId} ${errId}">
          ${placeholderHtml}
          ${options}
        </select>
      `;
    } else {
      const isNum = row.type.includes('number') || row.type.includes('percent') || row.type.includes('hours') || row.type.includes('minutes');
      controlHtml = `
        <input type="${isNum ? 'number' : 'text'}" 
               id="${fieldId}" 
               data-row-id="${esc(row.id)}" 
               value="${esc(rawVal)}" 
               placeholder="${esc(row.range)}"
               aria-describedby="${tipId} ${errId}">
      `;
    }

    return `
      <div class="form-group" id="group-${rowKey}">
        <div class="form-label-row">
          <label class="form-label" for="${fieldId}">
            ${esc(row.label)}
            <span class="tooltip-icon" id="${tipId}" tabindex="0" role="tooltip" title="${esc(row.legend)}" aria-label="${esc(row.legend)}">(?)</span>
          </label>
          <span class="range-badge">${esc(row.range)}</span>
        </div>
        ${controlHtml}
        <div class="field-error-msg" id="${errId}" role="alert"></div>
      </div>
    `;
  }

  attachFormListeners() {
    // Input validation on change
    for (const row of this.schema.rows) {
      if (row.id.startsWith('redis.') && !this.redisStatus?.enabled) continue;
      if (row.type === 'multi-select') {
        const cbs = document.querySelectorAll(`input[name="field-${row.id.replace(/\./g, '-')}"]`);
        for (const cb of cbs) {
          cb.addEventListener('change', () => {
            const checked = Array.from(document.querySelectorAll(`input[name="field-${row.id.replace(/\./g, '-')}"]:checked`)).map(c => c.value);
            const res = validateRowValue(row, checked);
            const errEl = document.getElementById(`err-${row.id.replace(/\./g, '-')}`);
            for (const box of cbs) {
              if (!res.valid) {
                box.setAttribute('aria-invalid', 'true');
              } else {
                box.removeAttribute('aria-invalid');
              }
            }
            if (!res.valid) {
              if (errEl) errEl.textContent = res.error;
            } else {
              if (errEl) errEl.textContent = '';
              this.currentSettings[row.id] = checked;
            }
          });
        }
        continue;
      }

      const el = document.getElementById(`field-${row.id.replace(/\./g, '-')}`);
      if (!el) continue;

      el.addEventListener('change', (e) => {
        let newVal = e.target.type === 'checkbox' ? (e.target.checked ? 'on' : 'off') : e.target.value;
        // T83 GPU controls are stored as JSON booleans/integers, never 'on'/'off' strings.
        newVal = coerceRowValue(row, newVal);
        const res = validateRowValue(row, newVal);
        const errEl = document.getElementById(`err-${row.id.replace(/\./g, '-')}`);

        if (!res.valid) {
          el.classList.add('form-control-invalid');
          el.setAttribute('aria-invalid', 'true');
          if (errEl) errEl.textContent = res.error;
        } else {
          el.classList.remove('form-control-invalid');
          el.removeAttribute('aria-invalid');
          if (errEl) errEl.textContent = '';
          this.currentSettings[row.id] = newVal;
        }

        // Toggle text update
        const labelState = document.getElementById(`label-state-${row.id.replace(/\./g, '-')}`);
        if (labelState && e.target.type === 'checkbox') {
          labelState.textContent = e.target.checked ? 'ON' : 'OFF';
        }
      });

      // Tooltip visible-on-focus behavior
      const tip = document.getElementById(`tip-${row.id.replace(/\./g, '-')}`);
      if (tip) {
        tip.addEventListener('focus', () => tip.classList.add('tooltip-visible'));
        tip.addEventListener('blur', () => tip.classList.remove('tooltip-visible'));
      }
    }

    // Save button with error blocking and double-submit prevention
    const saveBtn = document.getElementById('btn-save-config');
    if (saveBtn) {
      saveBtn.addEventListener('click', async () => {
        if (saveBtn.disabled) return;
        clearError(this.errorBanner);

        // Check for visible field errors
        const errEls = document.querySelectorAll('[id^="err-"]');
        const hasVisibleErrors = Array.from(errEls).some(el => el.textContent && el.textContent.trim().length > 0);
        if (hasVisibleErrors) {
          displayError(this.errorBanner, {
            title: 'Validation Error',
            status: 400,
            detail: 'Cannot save while fields have active validation errors.'
          });
          return;
        }

        // Validate all current settings against schema
        for (const row of this.schema.rows) {
          if (row.section === 'manual' || row.type === 'button') continue;
          // D-W10-6: the Director panel already normalized this field and has no
          // err-* element in the generic form.
          if (isIsolationField(row.id) || isDewWindowField(row.id)) continue;
          if (row.id.startsWith('redis.') && !this.redisStatus?.enabled) continue;
          let val = this.currentSettings[row.id];
          if (val !== undefined && val !== null && val !== '') {
            // Typed rows (T83 GPU) are coerced here too, so a value loaded from an
            // older 'on'/'off' save still reaches the server as the right JSON type.
            val = coerceRowValue(row, val);
            this.currentSettings[row.id] = val;
            const res = validateRowValue(row, val);
            if (!res.valid) {
              const errEl = document.getElementById(`err-${row.id.replace(/\./g, '-')}`);
              if (errEl) errEl.textContent = res.error;
              displayError(this.errorBanner, {
                title: 'Validation Error',
                status: 400,
                detail: `Invalid value for ${row.label}: ${res.error}`
              });
              return;
            }
          }
        }

        const reason = document.getElementById('save-reason')?.value || '';
        const pinName = document.getElementById('pin-name')?.value || '';

        saveBtn.disabled = true;
        try {
          const res = await saveAdminConfig(this.pageId, this.currentSettings, reason, pinName, this.configData?.version);
          this.showSuccess(`Configuration v${res.version} saved successfully!${res.redis ? ` Redis: ${res.redis.message}.` : ''}`);
          await this.initAdminPage(); // Reload versions and active state
        } catch (err) {
          displayError(this.errorBanner, err);
        } finally {
          saveBtn.disabled = false;
        }
      });
    }
  }

  renderConfigVersionTable() {
    const el = document.getElementById('version-table-container');
    if (!el || !this.configData?.versions) return;

    const rowsHtml = this.configData.versions.map(v => `
      <tr>
        <td><strong>v${v.version}</strong> ${v.version === this.configData.version ? '<span class="tier-badge tier-3">Active</span>' : ''}</td>
        <td><code>${esc(v.pinnedName || '—')}</code></td>
        <td>${esc(v.reason)}</td>
        <td>${esc(v.author)}</td>
        <td>${new Date(v.createdAt).toLocaleTimeString()}</td>
        <td>${v.inUseByJobs} jobs</td>
        <td>
          ${v.version !== this.configData.version ? `
            <button type="button" class="btn btn-secondary btn-sm btn-rollback" data-rollback-version="${v.version}">
              ⏮ Rollback to v${v.version}
            </button>
          ` : '—'}
        </td>
      </tr>
    `).join('');

    el.innerHTML = `
      <table class="data-table">
        <thead>
          <tr>
            <th>Version</th>
            <th>Pin Name</th>
            <th>Reason</th>
            <th>Author</th>
            <th>Created</th>
            <th>In-Use</th>
            <th>Rollback</th>
          </tr>
        </thead>
        <tbody>${rowsHtml}</tbody>
      </table>
    `;

    // Attach click listeners for rollback (no inline onclick, CSP-safe)
    el.querySelectorAll('.btn-rollback').forEach(btn => {
      btn.addEventListener('click', () => {
        const ver = parseInt(btn.getAttribute('data-rollback-version'), 10);
        if (!isNaN(ver)) {
          this.handleRollback(ver);
        }
      });
    });
  }

  async handleRollback(targetVersion) {
    clearError(this.errorBanner);
    const reason = prompt(`Enter reason for rollback to version ${targetVersion}:`, `Rolling back to audited version ${targetVersion}`);
    if (!reason) return;

    try {
      const res = await request(`/admin/v1/config/${encodeURIComponent(this.pageId)}`, {
        method: 'POST',
        body: JSON.stringify({ rollbackVersion: targetVersion, reason, expectedVersion: this.configData.version })
      });
      this.showSuccess(`Rollback complete: created new audited version v${res.version}!`);
      await this.initAdminPage();
    } catch (err) {
      displayError(this.errorBanner, err);
    }
  }
}

// END OF app.js

