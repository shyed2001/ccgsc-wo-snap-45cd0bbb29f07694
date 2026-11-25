// Path: web/pages/js/caps_admin_R15.js | Author: Hermes (MiniMax-M3) | 2026-11-22
// What/why: R15-FIX-CAPS Addendum (G-R15-3) - the resource caps of the server in the
// admin panel (the server pools settings section). Until now the caps were only
// readable; the admin may now edit them.
//
// The rules of the panel:
//   - only an admin may edit (the server refuses every other role anyway),
//   - maxProcs and memLimitMB apply LIVE (the server calls runtime.GOMAXPROCS and
//     debug.SetMemoryLimit),
//   - workerPool and dbMaxOpenConns apply after a restart: the panel says so,
//   - the memory limit is a SOFT GC target, not a hard RSS cap: the panel says so
//     next to the field.
import { request, markLive, getCSRFHeaders } from './api.js';

// The id of the caps panel.
export const CAPS_PANEL_ID = 'caps-panel';
// The id of the status line of the caps panel.
export const CAPS_STATUS_ID = 'caps-status';

// CAPS_FIELDS are the four knobs of the panel: the name the server uses, the label,
// the unit and whether the change applies live.
export const CAPS_FIELDS = Object.freeze([
  { name: 'maxProcs', label: 'GOMAXPROCS (CPU cores)', unit: 'cores', live: true },
  { name: 'memLimitMB', label: 'Memory limit', unit: 'MB', live: true },
  { name: 'workerPool', label: 'Worker pool slots', unit: 'slots', live: false },
  { name: 'dbMaxOpenConns', label: 'Database connections', unit: 'connections', live: false }
]);

// CAPS_LIVE_FIELDS are the fields that apply without a restart of the server.
export const CAPS_LIVE_FIELDS = Object.freeze(CAPS_FIELDS.filter(f => f.live).map(f => f.name));

// CAPS_RESTART_FIELDS are the fields that need a restart of the server.
export const CAPS_RESTART_FIELDS = Object.freeze(CAPS_FIELDS.filter(f => !f.live).map(f => f.name));

// readCaps reads the effective caps of the server.
export async function readCaps() {
  const caps = await request('/admin/v1/caps');
  markLive('pools-core served the resource caps');
  return caps;
}

// writeCaps writes the caps of the body. The server validates, clamps and applies
// them; the reply carries the effective values and the fields that need a restart.
export async function writeCaps(changes) {
  const saved = await request('/admin/v1/caps', {
    method: 'PUT',
    headers: getCSRFHeaders(),
    body: JSON.stringify(changes || {})
  });
  markLive('pools-core saved the resource caps');
  return saved;
}

// capsFieldValue reads one knob out of a caps reply. A missing value answers 0
// (auto), never a wrong number.
export function capsFieldValue(caps, name) {
  const value = caps ? caps[name] : undefined;
  return Number.isFinite(Number(value)) ? Number(value) : 0;
}

// capsPendingRestart reads the fields that need a restart out of a write reply.
// A reply without the field answers the documented list.
export function capsPendingRestart(saved) {
  const list = saved && Array.isArray(saved.pendingRestart) ? saved.pendingRestart : null;
  return list && list.length ? list.slice() : CAPS_RESTART_FIELDS.slice();
}

// capsSoftLimitNote is the sentence under the memory field: the limit is a soft GC
// target, not a hard RSS cap.
export function capsSoftLimitNote(caps) {
  if (caps && typeof caps.softLimitExplain === 'string' && caps.softLimitExplain.trim()) {
    return caps.softLimitExplain.trim();
  }
  return 'the memory limit is a soft GC target, not a hard limit on the RSS of the process.';
}

// renderCapsPanel renders the caps form of the admin panel. `current` is a caps
// reply; the form is disabled without an admin session.
export function renderCapsPanel(content, canAdmin, current) {
  if (!content || typeof content.insertAdjacentHTML !== 'function') return null;
  const maxProcs = capsFieldValue(current, 'maxProcs');
  const memLimitMB = capsFieldValue(current, 'memLimitMB');
  const workerPool = capsFieldValue(current, 'workerPool');
  const dbMaxOpenConns = capsFieldValue(current, 'dbMaxOpenConns');
  const fields = CAPS_FIELDS.map(field => {
    const value = field.name === 'maxProcs' ? maxProcs
      : field.name === 'memLimitMB' ? memLimitMB
        : field.name === 'workerPool' ? workerPool : dbMaxOpenConns;
    const when = field.live ? 'applies immediately' : 'applies after restart';
    return `
      <div class="form-group" data-caps-field="${field.name}">
        <label class="form-label" for="caps-${field.name}">${field.label} (${field.unit})</label>
        <input id="caps-${field.name}" name="${field.name}" type="number" min="0" step="1" value="${value}">
        <p class="field-help">0 = auto. This change ${when}.</p>
      </div>`;
  }).join('');
  content.insertAdjacentHTML('beforeend', `
    <section class="config-panel" id="${CAPS_PANEL_ID}">
      <h2>Server resource caps</h2>
      <p class="field-help">
        The caps of the server process. GOMAXPROCS and the memory limit apply immediately;
        the worker pool and the database connection cap apply after a restart of the server.
      </p>
      <p class="field-help" id="caps-soft-limit-note">${capsSoftLimitNote(current)}</p>
      ${fields}
      <div class="form-group">
        <button id="caps-save" type="button">Save the caps</button>
      </div>
      <p class="field-help" id="${CAPS_STATUS_ID}" role="status"></p>
    </section>
  `);
  const panel = typeof content.querySelector === 'function' ? content.querySelector(`#${CAPS_PANEL_ID}`) : null;
  if (panel) {
    // The panel of a page without a querySelectorAll (a test double) keeps its
    // fields enabled; the server refuses every write of a non-admin anyway.
    if (typeof panel.querySelectorAll === 'function') {
      panel.querySelectorAll('input, button').forEach(el => { el.disabled = !canAdmin; });
    }
  }
  return { panel, status: panel && typeof panel.querySelector === 'function' ? panel.querySelector(`#${CAPS_STATUS_ID}`) : null };
}

// capsBodyFromForm reads the four knobs out of the form. A field the admin left
// empty is not sent, so a write never resets a field by accident.
export function capsBodyFromForm(form) {
  const body = {};
  if (!form) return body;
  for (const field of CAPS_FIELDS) {
    const el = form.querySelector(`#caps-${field.name}`);
    if (!el) continue;
    const raw = String(el.value === undefined ? '' : el.value).trim();
    if (raw === '') continue;
    const value = Number(raw);
    if (!Number.isFinite(value)) continue;
    body[field.name] = Math.trunc(value);
  }
  return body;
}

// capsStatusText is the one line the panel shows after a write: what applied now
// and what waits for a restart.
export function capsStatusText(saved) {
  const pending = capsPendingRestart(saved);
  if (!pending.length) return 'The caps are applied.';
  return `Saved. ${pending.join(' and ')} apply after a restart of the server.`;
}
