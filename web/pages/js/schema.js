// T91 / R42.8 | Codex | 2026-11-20: typed shared Redis controls on both Director pages.
// WO-HC-WEB-01 schema.js - CCGSC Pool Admin & Monitor Page Schemas (291 Settings/Metrics Rows)
// Author: Claude Opus 5.5 head / agy Gemini 3.8 Flash high. Date: 2026-11-17.
// Truth: F:/tmp/hcwork/ADMIN_MANUALS_SOURCE.md (every page, section, row = truth)
// W10c (2026-11-21, D-W10-6): the kernel isolation mode is a Director setting on
// both pool admin pages. The value travels to the runner with the job; the
// server only stores and serves the field (W10C-SERVER).
import { ISOLATION_MODES, DEFAULT_ISOLATION_MODE } from './runner.js';

// The isolation-mode setting id of one admin page. Unknown pages answer the
// thread pool field, which is the primary pool of a tab.
export const ISOLATION_FIELD_BY_PAGE = Object.freeze({
  'thread-pool-admin': 'tp.ver.isolation',
  'process-pool-admin': 'pp.ver.isolation'
});

/**
 * The schema row id that stores the isolation mode of one admin page.
 *
 * @param {string} pageId
 * @returns {string}
 */
export function isolationFieldFor(pageId) {
  return ISOLATION_FIELD_BY_PAGE[pageId] || ISOLATION_FIELD_BY_PAGE['thread-pool-admin'];
}

/**
 * Read the isolation mode out of a settings map. A missing, unknown or empty
 * value answers the strict default.
 *
 * @param {Object} settings
 * @param {string} [pageId]
 * @returns {string}
 */
export function readIsolationMode(settings, pageId = 'thread-pool-admin') {
  const value = settings ? settings[isolationFieldFor(pageId)] : undefined;
  return ISOLATION_MODES.includes(String(value)) ? String(value) : DEFAULT_ISOLATION_MODE;
}

/**
 * Whether a row id is the isolation-mode field of any admin page. That field is
 * rendered by the Director panel (admin_controls_MVPW.js), not by the generic
 * form, so exactly one control edits it.
 *
 * @param {string} rowId
 * @returns {boolean}
 */
export function isIsolationField(rowId) {
  return Object.values(ISOLATION_FIELD_BY_PAGE).includes(String(rowId));
}

// One row per pool admin page, appended to the verification section.
function isolationRow(prefix) {
  return {
    id: `${prefix}.ver.isolation`, label: 'Kernel isolation mode',
    legend: 'separate+lock runs the workload in a second, empty worker while the shell keeps the hash and the port; lock-only keeps one worker and the same capability lock',
    type: 'enum', default: DEFAULT_ISOLATION_MODE, range: ISOLATION_MODES.join('/'),
    whoMayChange: 'admin', wwppId: 'D-W10-6', section: 'verification'
  };
}

// W10D row 2 (D-W10-11, CR-51): the contributor controls are a Director setting
// on both pool admin pages. The value lives in the pool settings endpoint
// (same expectedVersion rule as verifyMode/isolationMode), not in the page
// config version, so the select saves itself; schema.js keeps the row for the
// manual, validation and the field ids.
export const CONTRIBUTOR_CONTROLS_MODES = Object.freeze(['off', 'pauseResume', 'startStopOptIn']);
export const DEFAULT_CONTRIBUTOR_CONTROLS = 'off';
export const CONTRIBUTOR_CONTROLS_FIELD_BY_PAGE = Object.freeze({
  'thread-pool-admin': 'tp.gen.contributor_controls',
  'process-pool-admin': 'pp.gen.contributor_controls'
});

/**
 * The schema row id that documents the contributor controls of one admin page.
 *
 * @param {string} pageId
 * @returns {string}
 */
export function contributorControlsFieldFor(pageId) {
  return CONTRIBUTOR_CONTROLS_FIELD_BY_PAGE[pageId] || CONTRIBUTOR_CONTROLS_FIELD_BY_PAGE['thread-pool-admin'];
}

/**
 * Read the contributor controls out of a settings map or a pool settings reply.
 * A missing, unknown or empty value answers the strict default, off.
 *
 * @param {Object} settings
 * @param {string} [pageId]
 * @returns {string}
 */
export function readContributorControls(settings, pageId = 'thread-pool-admin') {
  if (!settings || typeof settings !== 'object') return DEFAULT_CONTRIBUTOR_CONTROLS;
  const value = settings.contributorControls !== undefined
    ? settings.contributorControls
    : settings[contributorControlsFieldFor(pageId)];
  return CONTRIBUTOR_CONTROLS_MODES.includes(String(value)) ? String(value) : DEFAULT_CONTRIBUTOR_CONTROLS;
}

/**
 * Whether a row id is the contributor-controls field of any admin page. That
 * field is rendered by the Director panel (admin_controls_MVPW.js), not by the
 * generic form, so exactly one control edits it.
 *
 * @param {string} rowId
 * @returns {boolean}
 */
export function isContributorControlsField(rowId) {
  return Object.values(CONTRIBUTOR_CONTROLS_FIELD_BY_PAGE).includes(String(rowId));
}

/**
 * Whether a row id is the Dew receipt window of any admin page. The Director
 * panel (dew_admin_F12.js) is the live control, in minutes. The generic form
 * must not render a second one.
 *
 * @param {string} rowId
 * @returns {boolean}
 */
export function isDewWindowField(rowId) {
  return rowId === 'tp.dew.window' || rowId === 'pp.dew.window';
}

// One row per pool admin page, appended to the general section.
function contributorControlsRow(prefix) {
  return {
    id: `${prefix}.gen.contributor_controls`, label: 'Contributor controls',
    legend: 'off (default): no buttons, connect and compute at once; pauseResume: Pause/Resume plus a capacity slider; startStopOptIn: the page opens stopped with Start/Stop plus a capacity slider',
    type: 'enum', default: DEFAULT_CONTRIBUTOR_CONTROLS, range: CONTRIBUTOR_CONTROLS_MODES.join('/'),
    whoMayChange: 'admin', wwppId: 'D-W10-11', section: 'general'
  };
}

export const PAGE_SCHEMAS = {
  "monitor": {
    pageId: "monitor",
    title: "Monitor",
    subTitle: "Combined Brief Live Health Overview (Tier 1)",
    tier: 1,
    manualHref: "manual/monitor.html",
    purpose: "One very brief live page that shows both pools and both APIs at a glance with simple error and warning lines only. Every number links to its matching settings section on the control pages.",
    sections: [
      { id: "capacity", label: "Capacity", detailHref: "pool-monitor.html#capacity" },
      { id: "work", label: "Work", detailHref: "pool-monitor.html#work" },
      { id: "trust", label: "Trust", detailHref: "thread-pool-admin.html#verification" },
      { id: "api-system", label: "API + System in Brief", detailHref: "api-monitor.html#traffic" },
      { id: "alerts", label: "Alerts & Settings", detailHref: "pool-monitor.html#errors" },
      { id: "devices-gpu", label: "Devices & GPU Reports", detailHref: "pool-monitor.html#devices" },
      { id: "manual", label: "Manual Section", detailHref: "manual/monitor.html" }
    ],
    rows: [
      { id: "mon.cap.contributors", label: "Contributors", legend: "Registered contributor devices available to both pools", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "capacity" },
      { id: "mon.cap.tabs", label: "Tabs", legend: "Registered tabs acting as wwHosts", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "capacity" },
      { id: "mon.cap.cpu_workers", label: "CPU workers", legend: "Free compute web worker threads available in the thread pool", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "capacity" },
      { id: "mon.cap.gpu_workers", label: "GPU workers", legend: "Tabs reporting a usable WebGPU adapter", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "capacity" },
      { id: "mon.cap.free_slots_thread", label: "Free slots — thread pool", legend: "Unleased task slots in the Web Thread Pool", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "capacity" },
      { id: "mon.cap.free_slots_process", label: "Free slots — process pool", legend: "Unleased tab slots in the Web Process Pool", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "capacity" },
      { id: "mon.work.running", label: "Jobs running", legend: "Jobs currently leased and not finished", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "work" },
      { id: "mon.work.queued", label: "Jobs queued", legend: "Jobs admitted and waiting for a slot", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "work" },
      { id: "mon.work.failed", label: "Jobs failed", legend: "Jobs that ended in failure state", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "work" },
      { id: "mon.work.tasks_per_s", label: "Tasks/s", legend: "Settled tasks per second across both pools", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "work" },
      { id: "mon.work.avg_task_time", label: "Avg task time", legend: "Mean duration of settled tasks in the shown window", type: "duration (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "work" },
      { id: "mon.trust.hash_fails", label: "Hash fails", legend: "Results rejected because the recomputed SHA-256 differed", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "trust" },
      { id: "mon.trust.rerun_mismatch", label: "Re-run mismatches", legend: "Sampled re-runs whose digests disagreed", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "trust" },
      { id: "mon.trust.canary_fails", label: "Canary fails", legend: "Failed hidden known-answer tasks", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "trust" },
      { id: "mon.trust.quarantined", label: "Quarantined", legend: "Tasks held in the poison-task quarantine", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "trust" },
      { id: "mon.trust.banned", label: "Banned", legend: "Workers banned by the reputation score", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "trust" },
      { id: "mon.trust.window_hours", label: "Trust window", legend: "Rolling window used by every trust counter on this page", type: "number (hours)", default: 24, range: "1-168", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "trust" },
      { id: "mon.api.req_per_min", label: "req/min", legend: "API requests per minute across both pool APIs", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "api-system" },
      { id: "mon.api.4xx", label: "4xx", legend: "Client-error responses in the shown window", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "api-system" },
      { id: "mon.api.5xx", label: "5xx", legend: "Server-error responses in the shown window", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "api-system" },
      { id: "mon.api.429", label: "429", legend: "Rate-limit rejections, each carrying Retry-After", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "api-system" },
      { id: "mon.api.p95", label: "p95", legend: "95th percentile request latency over the chosen window", type: "duration (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "api-system" },
      { id: "mon.sys.cpu", label: "Server CPU", legend: "CPU load of the pool server", type: "percent (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "api-system" },
      { id: "mon.sys.ram", label: "Server RAM", legend: "Memory use of the pool server", type: "percent (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "api-system" },
      { id: "mon.sys.disk", label: "Server disk", legend: "Disk use of the pool server", type: "percent (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "api-system" },
      { id: "mon.sys.pg_conns", label: "PG connections", legend: "Open PostgreSQL connections", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "api-system" },
      { id: "mon.sys.sse_conns", label: "SSE connections", legend: "Open server-sent event streams", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "api-system" },
      { id: "mon.alert.level", label: "Warning level", legend: "Lowest severity that still prints an error or warning line", type: "enum", default: "warn", range: "info/warn/error", whoMayChange: "admin", wwppId: "WWPP-R38.5b", section: "alerts" },
      { id: "mon.alert.refresh", label: "Refresh interval", legend: "Seconds between automatic refreshes of the brief numbers", type: "number (s)", default: 10, range: "5-300", whoMayChange: "admin", wwppId: "WWPP-R38.5b", section: "alerts" },
      { id: "mon.alert.auto", label: "Auto refresh", legend: "Keeps the brief page current; off leaves the page manual", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R38.5b", section: "alerts" },
      { id: "mon.link.numbers", label: "Number links", legend: "Every displayed number links to its matching settings section", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "alerts" },
      { id: "mon.link.target", label: "Link target", legend: "Where a linked number opens the matching section", type: "enum", default: "same-tab", range: "same-tab/new-tab", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "alerts" },
      { id: "mon.p95.window", label: "p95 window", legend: "Window used for the p95 latency number", type: "enum", default: "5m", range: "1m/5m/15m", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "alerts" },
      { id: "mon.metrics.read", label: "Metrics read", legend: "The brief page reads the light metrics table and never writes measurements", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R37.2", section: "alerts" }
    ]
  },

  "pool-monitor": {
    pageId: "pool-monitor",
    title: "Pool Monitor",
    subTitle: "Medium Monitor for Both Pools (Tier 2)",
    tier: 2,
    manualHref: "manual/pool-monitor.html",
    purpose: "The medium monitor for both pools: bigger error messages, dashboards and control options in one place. Every section links to its matching section on the pool admin pages.",
    sections: [
      { id: "capacity", label: "Capacity Dashboard", detailHref: "thread-pool-admin.html#workers" },
      { id: "work", label: "Work Dashboard", detailHref: "thread-pool-admin.html#scheduling" },
      { id: "trust", label: "Trust Dashboard", detailHref: "thread-pool-admin.html#verification" },
      { id: "devices", label: "Devices and Workers", detailHref: "thread-pool-admin.html#guards" },
      { id: "queue", label: "Queue and Backlog", detailHref: "thread-pool-admin.html#scheduling" },
      { id: "errors", label: "Error Board", detailHref: "thread-pool-admin.html#metrics" },
      { id: "control", label: "Control Options", detailHref: "thread-pool-admin.html#general" },
      { id: "manual", label: "Manual Section", detailHref: "manual/pool-monitor.html" }
    ],
    rows: [
      { id: "pm.cap.refresh", label: "Capacity refresh", legend: "Seconds between refreshes of the capacity dashboard", type: "number (s)", default: 10, range: "5-300", whoMayChange: "admin", wwppId: "WWPP-R38.5b", section: "capacity" },
      { id: "pm.cap.slot_detail", label: "Slot detail", legend: "Shows free and busy slots per pool, not only the totals", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R38.5b", section: "capacity" },
      { id: "pm.work.window", label: "Work window", legend: "Time window used by the work dashboard charts", type: "enum", default: "1h", range: "15m/1h/24h/7d", whoMayChange: "admin", wwppId: "WWPP-R38.5b", section: "work" },
      { id: "pm.work.tasks_per_s", label: "Tasks/s", legend: "Settled tasks per second over the chosen window", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "work" },
      { id: "pm.work.avg_task_time", label: "Avg task time", legend: "Mean task duration over the chosen window", type: "duration (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "work" },
      { id: "pm.trust.hash_fails", label: "Hash fails", legend: "Rejected results whose recomputed digest differed", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "trust" },
      { id: "pm.trust.rerun_mismatch", label: "Re-run mismatches", legend: "Sampled re-runs whose digests disagreed", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "trust" },
      { id: "pm.trust.canary_fails", label: "Canary fails", legend: "Failed hidden known-answer tasks", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "trust" },
      { id: "pm.trust.banned", label: "Banned", legend: "Workers banned by the reputation score", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "trust" },
      { id: "pm.dev.workers_per_tab", label: "Workers per tab shown", legend: "Shows the worker count each registered tab reports", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R29.4", section: "devices" },
      { id: "pm.dev.device_class", label: "Device class shown", legend: "Groups tabs by phone, laptop, desktop or owner node", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R10.4", section: "devices" },
      { id: "pm.dev.gpu_reported", label: "GPU capable tabs", legend: "Shows tabs that report a usable WebGPU adapter", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R33.1", section: "devices" },
      { id: "pm.queue.depth", label: "Queue depth", legend: "Tasks waiting for a slot in the shown pool", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "queue" },
      { id: "pm.queue.rejected", label: "Rejected", legend: "Tasks rejected by the bounded-queue policy", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "queue" },
      { id: "pm.err.banner", label: "Error banner size", legend: "Medium tier prints larger error messages than the brief page", type: "enum", default: "large", range: "compact/large", whoMayChange: "admin", wwppId: "WWPP-R38.5b", section: "errors" },
      { id: "pm.err.leases_expiring", label: "Leases expiring", legend: "Shows leases close to expiry and their renewal state", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R18.2", section: "errors" },
      { id: "pm.ctl.pause_scheduling", label: "Pause scheduling", legend: "Stops handing out new tasks; running jobs keep their leases", type: "toggle", default: "off", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R27.1", section: "control" },
      { id: "pm.ctl.requeue_failed", label: "Requeue failed tasks", legend: "Re-queues failed tasks that are still inside the retry policy", type: "toggle", default: "off", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.9", section: "control" },
      { id: "pm.ctl.clear_quarantine", label: "Clear quarantine", legend: "Releases a quarantined poison task after admin review", type: "button", default: "—", range: "—", whoMayChange: "admin", wwppId: "WWPP-R32.9", section: "control" },
      { id: "pm.ctl.requests", label: "Developer requests", legend: "Shows per-job developer requests filed against the pool defaults", type: "toggle", default: "on", range: "on/off", whoMayChange: "dev-request", wwppId: "WWPP-R23.1", section: "control" }
    ]
  },

  "api-monitor": {
    pageId: "api-monitor",
    title: "API Monitor",
    subTitle: "Medium Monitor for Both APIs (Tier 2)",
    tier: 2,
    manualHref: "manual/api-monitor.html",
    purpose: "The medium monitor for the two pool APIs together: bigger error messages, dashboards and control options for traffic, keys, quotas and sessions.",
    sections: [
      { id: "traffic", label: "Traffic", detailHref: "thread-api-admin.html#quotas" },
      { id: "errors", label: "Error Board", detailHref: "thread-api-admin.html#errors" },
      { id: "keys", label: "Key Activity", detailHref: "thread-api-admin.html#keys" },
      { id: "quotas", label: "Quota Usage", detailHref: "thread-api-admin.html#quotas" },
      { id: "sessions", label: "Sessions", detailHref: "process-api-admin.html#sessions" },
      { id: "system", label: "Server and Database", detailHref: "monitor.html#api-system" },
      { id: "control", label: "Control Options", detailHref: "thread-api-admin.html#keys" },
      { id: "manual", label: "Manual Section", detailHref: "manual/api-monitor.html" }
    ],
    rows: [
      { id: "am.scope", label: "API scope", legend: "Which of the two pool APIs the monitor follows", type: "enum", default: "both", range: "thread/process/both", whoMayChange: "admin", wwppId: "WWPP-R38.5b", section: "traffic" },
      { id: "am.traffic.refresh", label: "Traffic refresh", legend: "Seconds between refreshes of the traffic cards", type: "number (s)", default: 10, range: "5-300", whoMayChange: "admin", wwppId: "WWPP-R38.5b", section: "traffic" },
      { id: "am.traffic.req_per_min", label: "req/min", legend: "Requests per minute per API", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "traffic" },
      { id: "am.traffic.p95", label: "p95", legend: "95th percentile request latency over the chosen window", type: "duration (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "traffic" },
      { id: "am.traffic.window", label: "p95 window", legend: "Window used for the latency card", type: "enum", default: "5m", range: "1m/5m/15m", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "traffic" },
      { id: "am.err.banner", label: "Error banner size", legend: "Medium tier prints larger error messages than the brief page", type: "enum", default: "large", range: "compact/large", whoMayChange: "admin", wwppId: "WWPP-R38.5b", section: "errors" },
      { id: "am.err.4xx", label: "4xx", legend: "Client-error responses in the window", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "errors" },
      { id: "am.err.5xx", label: "5xx", legend: "Server-error responses in the window", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "errors" },
      { id: "am.err.429", label: "429", legend: "Rate-limit rejections with Retry-After", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "errors" },
      { id: "am.err.codes", label: "Error codes", legend: "Shows the RFC 9457 code list with counts per code", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.8", section: "errors" },
      { id: "am.keys.activity", label: "Key activity", legend: "Shows recent issuance, approval and revocation of API keys", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R38.3", section: "keys" },
      { id: "am.keys.revoked", label: "Revoked keys", legend: "Shows keys that are revoked and their deny-list state", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.4", section: "keys" },
      { id: "am.quota.usage", label: "Quota usage", legend: "Shows per-key running-job, queued-task and request-per-minute usage", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "quotas" },
      { id: "am.quota.warn", label: "Quota warnings", legend: "Warns when a key approaches one of its quotas", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "quotas" },
      { id: "am.quota.credits", label: "Credit metering", legend: "Shows metered CPU-seconds and GPU-seconds per key", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "quotas" },
      { id: "am.sessions.list", label: "Session monitor", legend: "Shows process-pool sessions, leased tabs and expiry", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R38.5b", section: "sessions" },
      { id: "am.sys.card", label: "System card", legend: "Shows server CPU, RAM, disk, PG connections and SSE connections", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "system" },
      { id: "am.sys.pg_conns", label: "PG connections", legend: "Open PostgreSQL connections", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "system" },
      { id: "am.sys.sse_conns", label: "SSE connections", legend: "Open server-sent event streams", type: "number (live)", default: "live", range: "live", whoMayChange: "admin", wwppId: "WWPP-R40.1", section: "system" },
      { id: "am.ctl.pause_ingress", label: "Pause ingress", legend: "Stops accepting new job submissions while live streams stay open", type: "toggle", default: "off", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "control" },
      { id: "am.ctl.revoke_key", label: "Revoke key", legend: "Revokes a key from the key activity card", type: "button", default: "—", range: "—", whoMayChange: "admin", wwppId: "WWPP-R32.4", section: "control" }
    ]
  },

  "thread-pool-admin": {
    pageId: "thread-pool-admin",
    title: "Thread (WW) Pool Admin",
    subTitle: "Web Thread Pool Detailed Control & Settings (Tier 3)",
    tier: 3,
    manualHref: "manual/thread-pool-admin.html",
    purpose: "Full detail and every setting of the Web Thread Pool, where the unit of work is one task on any web worker thread of any registered tab. The Pool Admin allows options, sets limits and sets the pool default; developers only request inside the allowance.",
    sections: [
      { id: "redis", label: "Shared Redis Server", detailHref: "thread-pool-admin.html#redis" },
      { id: "general", label: "General" },
      { id: "workers", label: "Workers per Tab" },
      { id: "guards", label: "Device Guards" },
      { id: "gpu", label: "GPU Settings" },
      { id: "scheduling", label: "Scheduling & Queue" },
      { id: "verification", label: "Verification & Tamper" },
      { id: "p2p", label: "P2P Sharing" },
      { id: "dew", label: "Dew & Late Results" },
      { id: "metrics", label: "Metrics & Thesis" },
      { id: "config", label: "Config Versions & Audit" },
      { id: "manual", label: "Manual Section", detailHref: "manual/thread-pool-admin.html" }
    ],
    rows: [
      {"id": "redis.memory.mode", "label": "Redis memory cap mode", "legend": "T91 / R42.8: shared Redis cap mode; always noeviction.", "type": "enum", "default": "fixed", "range": "fixed/dynamic", "jsonType": "string", "whoMayChange": "admin", "wwppId": "WWPP-R42.8", "section": "redis"},
      {"id": "redis.memory.fixed_gb", "label": "Redis fixed cap (GiB)", "legend": "Fixed cap in binary GB (GiB).", "type": "number", "default": 1, "range": "1-4", "jsonType": "integer", "whoMayChange": "admin", "wwppId": "WWPP-R42.8", "section": "redis"},
      {"id": "redis.memory.dynamic_percent", "label": "Redis free RAM percent", "legend": "Percent of Linux MemAvailable, clamped to 1-4 GiB.", "type": "number", "default": 10, "range": "10-40", "jsonType": "integer", "whoMayChange": "admin", "wwppId": "WWPP-R42.8", "section": "redis"},
      {"id": "redis.memory.interval_min", "label": "Redis recompute interval (min)", "legend": "Dynamic recomputation interval in minutes.", "type": "number", "default": 5, "range": "1-60", "jsonType": "integer", "whoMayChange": "admin", "wwppId": "WWPP-R42.8", "section": "redis"},
      {"id": "redis.persistence.mode", "label": "Redis persistence mode", "legend": "AOF every second or existing Redis snapshot save rules only.", "type": "enum", "default": "aof_everysec", "range": "aof_everysec/snapshot", "jsonType": "string", "whoMayChange": "admin", "wwppId": "WWPP-R42.8", "section": "redis"},
      { id: "tp.gen.enabled", label: "Pool enabled", legend: "Master switch; off stops new task leasing in this pool", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R27.1", section: "general" },
      { id: "tp.gen.name", label: "Pool name", legend: "Display name shown in nav, monitors and API responses", type: "text", default: "Web Thread Pool", range: "1-64 chars", whoMayChange: "admin", wwppId: "WWPP-R38.5", section: "general" },
      { id: "tp.gen.fetch_mode", label: "Same-device fetch mode", legend: "Leader tab per device or every tab fetches on its own; inefficient use always warns", type: "enum", default: "leader", range: "leader/each-tab", whoMayChange: "dev-request", wwppId: "WWPP-R22.1", section: "general" },
      { id: "tp.gen.shareable", label: "Shareable outputs", legend: "Lets a developer mark outputs shareable; the admin may disable it for the pool", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R23.4", section: "general" },
      { id: "tp.gen.group_by", label: "groupBy default", legend: "Default task partitioning key applied when the developer gives none", type: "enum", default: "none", range: "none/label/device-class", whoMayChange: "admin", wwppId: "WWPP-R32.2", section: "general" },
      contributorControlsRow('tp'),
      { id: "tp.wrk.per_tab", label: "Workers per tab", legend: "Compute web worker threads per tab; auto means logical cores minus one", type: "enum", default: "auto", range: "auto/1/2/3/4", whoMayChange: "admin", wwppId: "WWPP-R29.4", section: "workers" },
      { id: "tp.wrk.min", label: "Worker range minimum", legend: "Lowest number of compute workers this pool allows per tab", type: "number", default: 1, range: "1-4", whoMayChange: "admin", wwppId: "WWPP-R29.4", section: "workers" },
      { id: "tp.wrk.max", label: "Worker range maximum", legend: "Highest number of compute workers per tab; the server admin may raise it where more cores exist", type: "number", default: 4, range: "1-32", whoMayChange: "admin", wwppId: "WWPP-R29.4", section: "workers" },
      { id: "tp.wrk.phones", label: "Phone worker count", legend: "Workers per tab on phones", type: "number", default: 1, range: "1-2", whoMayChange: "admin", wwppId: "WWPP-R10.4", section: "workers" },
      { id: "tp.wrk.laptops", label: "Laptop worker count", legend: "Workers per tab on laptops", type: "number", default: 2, range: "1-4", whoMayChange: "admin", wwppId: "WWPP-R10.4", section: "workers" },
      { id: "tp.wrk.desktops", label: "Desktop worker count", legend: "Workers per tab on desktops", type: "number", default: 4, range: "1-8", whoMayChange: "admin", wwppId: "WWPP-R10.4", section: "workers" },
      { id: "tp.wrk.owner", label: "Owner node worker count", legend: "Workers per tab on owner nodes", type: "enum", default: "auto", range: "auto/1-32", whoMayChange: "admin", wwppId: "WWPP-R10.4", section: "workers" },
      { id: "tp.wrk.phone_battery", label: "Phone workers on battery", legend: "Workers per tab while the phone runs on battery", type: "number", default: 1, range: "1-2", whoMayChange: "admin", wwppId: "WWPP-R30.2", section: "workers" },
      { id: "tp.wrk.phone_charging", label: "Phone workers charging", legend: "Workers per tab while charging with the screen on", type: "number", default: 2, range: "1-4", whoMayChange: "admin", wwppId: "WWPP-R30.2", section: "workers" },
      { id: "tp.wrk.phone_stop", label: "Stop below battery", legend: "Work stops below this battery level", type: "percent", default: 20, range: "5-50", whoMayChange: "admin", wwppId: "WWPP-R30.2", section: "workers" },
      { id: "tp.gd.cpu", label: "CPU profile", legend: "Contributor CPU effort profile the server asks for", type: "enum", default: "medium", range: "low/medium/full", whoMayChange: "admin", wwppId: "WWPP-R30.1", section: "guards" },
      { id: "tp.gd.backoff", label: "Auto back-off", legend: "Drops worker effort only when the device is very very busy", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R30.1", section: "guards" },
      { id: "tp.gd.battery", label: "Battery rule", legend: "Applies the phone battery and heat rules where the device supports them", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R30.1", section: "guards" },
      { id: "tp.gd.memory", label: "Memory cap per worker", legend: "Upper memory bound asked of each worker", type: "number (MiB)", default: "TBD", range: "1-4096", whoMayChange: "admin", wwppId: "WWPP-R30.1", section: "guards" },
      { id: "tp.gd.hidden_tab", label: "Pause on hidden tab", legend: "Deliberately off: hidden and background tabs must keep working", type: "toggle", default: "off", range: "off only", whoMayChange: "admin", wwppId: "WWPP-R30.1", section: "guards" },
      { id: "tp.gpu.enabled", label: "GPU tasks", legend: "Allows jobs to declare cpu, gpu or gpu-preferred tasks in this pool", type: "toggle", default: "on", range: "on/off", jsonType: "boolean", whoMayChange: "admin", wwppId: "WWPP-R33.1", section: "gpu" },
      { id: "tp.gpu.max_slots_per_tab", label: "Max GPU slots per tab", legend: "Maximum concurrent GPU task slots one tab may hold; one task per GPU, at most two GPUs per tab", type: "number", default: 2, range: "1-4", jsonType: "integer", whoMayChange: "admin", wwppId: "WWPP-R33.1", section: "gpu" },
      { id: "tp.gpu.on_battery_allowed", label: "GPU on battery", legend: "Allows GPU leases on phone and laptop tabs while they run on battery; default off", type: "toggle", default: "off", range: "on/off", jsonType: "boolean", whoMayChange: "admin", wwppId: "WWPP-R33.1", section: "gpu" },
      { id: "tp.gpu.time_limit", label: "GPU task time limit", legend: "Maximum runtime of one GPU task", type: "number (s)", default: "TBD", range: "1-3600", whoMayChange: "admin", wwppId: "WWPP-R33.1", section: "gpu" },
      { id: "tp.gpu.memory_limit", label: "GPU memory limit", legend: "Maximum GPU memory one task may request", type: "number (MiB)", default: "TBD", range: "1-8192", whoMayChange: "admin", wwppId: "WWPP-R33.1", section: "gpu" },
      { id: "tp.gpu.charging_only", label: "GPU only while charging", legend: "GPU tasks are eligible only when the device is charging", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R33.1", section: "gpu" },
      { id: "tp.sch.bounded_queue", label: "Bounded queue", legend: "Caps the admitted queue depth; the queue never grows without limit", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "scheduling" },
      { id: "tp.sch.rejection", label: "Queue rejection policy", legend: "What happens when the bounded queue is full", type: "enum", default: "TBD", range: "reject-429/block-timeout/drop-oldest/caller-runs", whoMayChange: "admin", wwppId: "WWPP-R25.3", section: "scheduling" },
      { id: "tp.sch.priority_high", label: "High priority allowed", legend: "Lets the admin allow the high priority class in this pool", type: "toggle", default: "off", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R25.2", section: "scheduling" },
      { id: "tp.sch.fair_share", label: "Fair share", legend: "Shares pool capacity fairly between developers", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "scheduling" },
      { id: "tp.sch.batch_size", label: "Batch size per device class", legend: "Default task batch size handed to a device class", type: "number", default: "TBD", range: "1-1000", whoMayChange: "admin", wwppId: "WWPP-R34.1", section: "scheduling" },
      { id: "tp.sch.small_task", label: "Small-task warning", legend: "Tasks below this size raise an efficiency warning on all four surfaces", type: "number (tasks)", default: "TBD", range: "1-100", whoMayChange: "admin", wwppId: "WWPP-R24.4", section: "scheduling" },
      { id: "tp.ver.hash_check", label: "Hash check on every load", legend: "Verifies artifact and input SHA-256 on every load, including IndexedDB cache hits", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R37.1", section: "verification" },
      { id: "tp.ver.rerun_percent", label: "Result re-run rate", legend: "Percentage of accepted results re-run on another worker; exact hashes only", type: "percent", default: 1, range: "0-100", whoMayChange: "dev-request", wwppId: "WWPP-R35.1", section: "verification" },
      { id: "tp.ver.signed_envelope", label: "Signed task envelope", legend: "Server signs each envelope with Ed25519 over job and task ids, artifact and input hashes and the lease", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R37.1", section: "verification" },
      { id: "tp.ver.canary", label: "Canary known-answer tasks", legend: "Schedules hidden known-answer tasks to test worker trustworthiness", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R37.1", section: "verification" },
      { id: "tp.ver.canary_warn", label: "Canary warn threshold", legend: "Reputation score at or below which the worker is warned", type: "number", default: 75, range: "0-100", whoMayChange: "admin", wwppId: "WWPP-R37.1", section: "verification" },
      { id: "tp.ver.canary_bar", label: "Canary bar threshold", legend: "Reputation score at or below which the worker is barred from tasks", type: "number", default: 50, range: "0-100", whoMayChange: "admin", wwppId: "WWPP-R37.1", section: "verification" },
      { id: "tp.ver.canary_ban", label: "Canary ban threshold", legend: "Reputation score at or below which the worker is banned", type: "number", default: 0, range: "0-100", whoMayChange: "admin", wwppId: "WWPP-R37.1", section: "verification" },
      { id: "tp.ver.reputation_recovery", label: "Reputation recovery", legend: "A ban is lifted by admin review; there is no automatic recovery", type: "enum", default: "manual-review", range: "manual-review/auto", whoMayChange: "admin", wwppId: "WWPP-R37.1", section: "verification" },
      { id: "tp.ver.mode", label: "Verification mode", legend: "(a) server mechanisms plus developer policy, or (b) developer only with results labelled unverified", type: "enum", default: "a", range: "a/b", whoMayChange: "admin", wwppId: "WWPP-R24.1", section: "verification" },
      { id: "tp.ver.k_of_n", label: "k-of-n checks", legend: "Independent checks that must agree before a result counts as verified", type: "number", default: "TBD", range: "1-5", whoMayChange: "admin", wwppId: "WWPP-R24.1", section: "verification" },
      { id: "tp.ver.local_after_download", label: "Verify after download", legend: "Lets developers verify results locally after download", type: "toggle", default: "on", range: "on/off", whoMayChange: "dev-request", wwppId: "WWPP-R24.1", section: "verification" },
      { id: "tp.ver.evidence", label: "Tamper evidence", legend: "(a) server minimal, security log plus SHA-256 of every accepted result, or (b) everything opt-in", type: "enum", default: "a", range: "a/b", whoMayChange: "admin", wwppId: "WWPP-R35.2", section: "verification" },
      { id: "tp.ver.chain", label: "Audit hash chain", legend: "Per-job hash chain written by the server; built and tested even while off", type: "toggle", default: "off", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R26.2", section: "verification" },
      { id: "tp.ver.merkle", label: "Global Merkle checkpoints", legend: "Adds periodic Merkle roots over the tips of job chains", type: "toggle", default: "off", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R22.3", section: "verification" },
      { id: "tp.ver.chain_authority", label: "Chain authority", legend: "(a) admin global Merkle on/off with developer raise-only, or (b) developer free choice inside the allowance", type: "enum", default: "TBD", range: "a/b", whoMayChange: "admin", wwppId: "WWPP-R23.3", section: "verification" },
      isolationRow('tp'),
      { id: "tp.p2p.mode", label: "P2P artifact sharing", legend: "Peers only between tabs locked to the same-app; module artifacts are SHA-256 checked with one HTTP fallback; results and receipts always go to the server. lan = LAN host candidates only (no STUN at all); stun = self-hosted STUN only (no public STUN, no TURN); off = disabled (default per admin)", type: "enum", default: "off", range: "stun/lan/off", whoMayChange: "admin", wwppId: "WWPP-R36.2", section: "p2p" },
      { id: "tp.p2p.peer_scope", label: "Peer selection scope", legend: "Superseded by tp.p2p.mode: peers are same-app tabs and the admin mode decides reachability. Kept as a stored setting; no runtime effect", type: "enum", default: "same-lan", range: "same-device/same-phone/same-lan/near", whoMayChange: "admin", wwppId: "WWPP-R9.1", section: "p2p" },
      { id: "tp.p2p.task_handoff", label: "Task hand-off", legend: "Historical P2P scope, kept as a setting; not an active peer data flow", type: "toggle", default: "off", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R9.1", section: "p2p" },
      { id: "tp.p2p.rtt_probe", label: "RTT probe step", legend: "Uses round-trip time in peer ordering once the probe exists", type: "toggle", default: "off", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R14.1", section: "p2p" },
      { id: "tp.p2p.fallback", label: "CDN fallback", legend: "Falls back to server or CDN when no peer can serve the artifact", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R36.2", section: "p2p" },
      { id: "tp.dew.enabled", label: "Dew", legend: "Offline leases with signed receipts so a tab may submit results later", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R36.2", section: "dew" },
      { id: "tp.dew.window", label: "Dew receipt window", legend: "How long a signed dew receipt stays valid. The Director panel saves this in minutes (1-30, default 10).", type: "minutes", default: 10, range: "1-30", whoMayChange: "admin", wwppId: "WWPP-R36.2", section: "dew" },
      { id: "tp.dew.late_policy", label: "Late-result policy", legend: "(a) archived lease id and attempt match, (b) accept only if no other attempt reported, (c) drop after re-lease", type: "enum", default: "a", range: "a/b/c", whoMayChange: "admin", wwppId: "WWPP-R13.2", section: "dew" },
      { id: "tp.dew.max_attempts", label: "Max attempts", legend: "Attempts allowed for one task before it fails", type: "number", default: 3, range: "1-10", whoMayChange: "admin", wwppId: "WWPP-R32.9", section: "dew" },
      { id: "tp.dew.job_policy", label: "Job failure policy", legend: "What the job does when a task cannot complete", type: "enum", default: "partial", range: "partial/fail-fast/threshold", whoMayChange: "admin", wwppId: "WWPP-R32.9", section: "dew" },
      { id: "tp.dew.quarantine", label: "Quarantine threshold", legend: "Worker crashes before a poison task is quarantined", type: "number", default: 3, range: "1-10", whoMayChange: "admin", wwppId: "WWPP-R32.9", section: "dew" },
      { id: "tp.dew.dead_letter", label: "Dead-letter store", legend: "Keeps late results that arrive after the job ended, with no second credit", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.9", section: "dew" },
      { id: "tp.met.light", label: "Light metrics", legend: "Per-feature timings and counters in one PostgreSQL metrics table; disabled intervals are recorded, never fabricated", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R37.2", section: "metrics" },
      { id: "tp.met.detail", label: "Metrics detail", legend: "How much per-feature detail the metrics table keeps", type: "enum", default: "TBD", range: "light/standard/full", whoMayChange: "admin", wwppId: "WWPP-R37.2", section: "metrics" },
      { id: "tp.met.benchmark", label: "Thesis benchmark harness", legend: "Heavy harness for owner-started thesis runs beside the MVP", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R37.2", section: "metrics" },
      { id: "tp.cfg.retention", label: "Version retention", legend: "Number of config versions kept for this admin page", type: "number", default: 10, range: "1-100", whoMayChange: "admin", wwppId: "WWPP-R38.7", section: "config" },
      { id: "tp.cfg.pin", label: "Pin version", legend: "Admin-named pins, for example a thesis run, keep a version from pruning", type: "text", default: "—", range: "1-64 chars", whoMayChange: "admin", wwppId: "WWPP-R38.7", section: "config" },
      { id: "tp.cfg.reason", label: "Reason required", legend: "Each Save records who, when, what changed and why", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R38.6", section: "config" },
      { id: "tp.cfg.rollback", label: "Rollback", legend: "One-click rollback copies an earlier version into a new audited version", type: "button", default: "—", range: "—", whoMayChange: "admin", wwppId: "WWPP-R38.6", section: "config" }
    ]
  },

  "process-pool-admin": {
    pageId: "process-pool-admin",
    title: "Process (wwHost) Pool Admin",
    subTitle: "Web Process Pool Detailed Control & Settings (Tier 3)",
    tier: 3,
    manualHref: "manual/process-pool-admin.html",
    purpose: "Full detail and every setting of the Web Process Pool, where the unit of work is one tab acting as a wwHost holding many worker threads. The Pool Admin sets pool defaults and limits, developers only request inside the allowance.",
    sections: [
      { id: "redis", label: "Shared Redis Server", detailHref: "process-pool-admin.html#redis" },
      { id: "general", label: "General" },
      { id: "shape", label: "Pool Shapes & Sessions" },
      { id: "workers", label: "Workers and Hosts" },
      { id: "guards", label: "Device Guards" },
      { id: "gpu", label: "GPU Settings" },
      { id: "scheduling", label: "Scheduling & Queue" },
      { id: "verification", label: "Verification & Tamper" },
      { id: "p2p", label: "P2P Sharing" },
      { id: "dew", label: "Dew & Late Results" },
      { id: "metrics", label: "Metrics & Thesis" },
      { id: "config", label: "Config Versions & Audit" },
      { id: "manual", label: "Manual Section", detailHref: "manual/process-pool-admin.html" }
    ],
    rows: [
      {"id": "redis.memory.mode", "label": "Redis memory cap mode", "legend": "T91 / R42.8: shared Redis cap mode; always noeviction.", "type": "enum", "default": "fixed", "range": "fixed/dynamic", "jsonType": "string", "whoMayChange": "admin", "wwppId": "WWPP-R42.8", "section": "redis"},
      {"id": "redis.memory.fixed_gb", "label": "Redis fixed cap (GiB)", "legend": "Fixed cap in binary GB (GiB).", "type": "number", "default": 1, "range": "1-4", "jsonType": "integer", "whoMayChange": "admin", "wwppId": "WWPP-R42.8", "section": "redis"},
      {"id": "redis.memory.dynamic_percent", "label": "Redis free RAM percent", "legend": "Percent of Linux MemAvailable, clamped to 1-4 GiB.", "type": "number", "default": 10, "range": "10-40", "jsonType": "integer", "whoMayChange": "admin", "wwppId": "WWPP-R42.8", "section": "redis"},
      {"id": "redis.memory.interval_min", "label": "Redis recompute interval (min)", "legend": "Dynamic recomputation interval in minutes.", "type": "number", "default": 5, "range": "1-60", "jsonType": "integer", "whoMayChange": "admin", "wwppId": "WWPP-R42.8", "section": "redis"},
      {"id": "redis.persistence.mode", "label": "Redis persistence mode", "legend": "AOF every second or existing Redis snapshot save rules only.", "type": "enum", "default": "aof_everysec", "range": "aof_everysec/snapshot", "jsonType": "string", "whoMayChange": "admin", "wwppId": "WWPP-R42.8", "section": "redis"},
      { id: "pp.gen.enabled", label: "Pool enabled", legend: "Master switch; off stops new session and job leasing in this pool", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R27.1", section: "general" },
      { id: "pp.gen.name", label: "Pool name", legend: "Display name shown in nav, monitors and API responses", type: "text", default: "Web Process Pool", range: "1-64 chars", whoMayChange: "admin", wwppId: "WWPP-R38.5", section: "general" },
      { id: "pp.gen.fetch_mode", label: "Same-device fetch mode", legend: "Leader tab per device or every tab fetches on its own; inefficient use always warns", type: "enum", default: "leader", range: "leader/each-tab", whoMayChange: "dev-request", wwppId: "WWPP-R22.1", section: "general" },
      { id: "pp.gen.shareable", label: "Shareable outputs", legend: "Lets a developer mark outputs shareable; the admin may disable it for the pool", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R23.4", section: "general" },
      contributorControlsRow('pp'),
      { id: "pp.shape.default", label: "Default pool shape", legend: "(a) tab session lease, or (b) task-group pinning to one tab", type: "enum", default: "TBD", range: "a/b", whoMayChange: "dev-request", wwppId: "WWPP-R19.1", section: "shape" },
      { id: "pp.shape.session_minutes", label: "Session lease duration", legend: "Minutes a leased session stays warm before it is released", type: "number (min)", default: "TBD", range: "1-1440", whoMayChange: "admin", wwppId: "WWPP-R19.1", section: "shape" },
      { id: "pp.shape.tabs_per_session", label: "Tabs per session", legend: "Tabs leased for one session", type: "number", default: "TBD", range: "1-64", whoMayChange: "admin", wwppId: "WWPP-R19.1", section: "shape" },
      { id: "pp.shape.hosts_per_session", label: "Hosts per session", legend: "Distinct registered devices per session; one up to the tab count", type: "number", default: "TBD", range: "1-64", whoMayChange: "admin", wwppId: "WWPP-R39.1", section: "shape" },
      { id: "pp.shape.warm_initializer", label: "Warm initializer", legend: "Artifact preloaded into the session tab before tasks arrive", type: "toggle", default: "off", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R19.1", section: "shape" },
      { id: "pp.shape.initializer_state", label: "Initializer state", legend: "Default state of the warm initializer between sessions", type: "enum", default: "TBD", range: "keep/discard", whoMayChange: "admin", wwppId: "WWPP-R25.3", section: "shape" },
      { id: "pp.shape.close_policy", label: "Session close", legend: "What happens to the tabs on close, expiry or error", type: "enum", default: "release", range: "release/drain", whoMayChange: "admin", wwppId: "WWPP-R19.1", section: "shape" },
      { id: "pp.shape.lease_store", label: "Lease store", legend: "PG leases, or Redis lease plus PG settle; PG is proposed and the default is not decided", type: "enum", default: "TBD", range: "pg/redis+pg", whoMayChange: "admin", wwppId: "WWPP-R18.4", section: "shape" },
      { id: "pp.wrk.per_tab", label: "Workers per tab", legend: "Compute web worker threads inside each wwHost tab", type: "enum", default: "auto", range: "auto/1/2/3/4", whoMayChange: "admin", wwppId: "WWPP-R29.4", section: "workers" },
      { id: "pp.wrk.min", label: "Worker range minimum", legend: "Lowest number of compute workers this pool allows per tab", type: "number", default: 1, range: "1-4", whoMayChange: "admin", wwppId: "WWPP-R29.4", section: "workers" },
      { id: "pp.wrk.max", label: "Worker range maximum", legend: "Highest number of compute workers per tab; the server admin may raise it where more cores exist", type: "number", default: 4, range: "1-32", whoMayChange: "admin", wwppId: "WWPP-R29.4", section: "workers" },
      { id: "pp.wrk.phones", label: "Phone worker count", legend: "Workers per tab on phones", type: "number", default: 1, range: "1-2", whoMayChange: "admin", wwppId: "WWPP-R10.4", section: "workers" },
      { id: "pp.wrk.laptops", label: "Laptop worker count", legend: "Workers per tab on laptops", type: "number", default: 2, range: "1-4", whoMayChange: "admin", wwppId: "WWPP-R10.4", section: "workers" },
      { id: "pp.wrk.desktops", label: "Desktop worker count", legend: "Workers per tab on desktops", type: "number", default: 4, range: "1-8", whoMayChange: "admin", wwppId: "WWPP-R10.4", section: "workers" },
      { id: "pp.wrk.owner", label: "Owner node worker count", legend: "Workers per tab on owner nodes", type: "enum", default: "auto", range: "auto/1-32", whoMayChange: "admin", wwppId: "WWPP-R10.4", section: "workers" },
      { id: "pp.wrk.phone_battery", label: "Phone workers on battery", legend: "Workers per tab while the phone runs on battery", type: "number", default: 1, range: "1-2", whoMayChange: "admin", wwppId: "WWPP-R30.2", section: "workers" },
      { id: "pp.wrk.phone_charging", label: "Phone workers charging", legend: "Workers per tab while charging with the screen on", type: "number", default: 2, range: "1-4", whoMayChange: "admin", wwppId: "WWPP-R30.2", section: "workers" },
      { id: "pp.wrk.phone_stop", label: "Stop below battery", legend: "Work stops below this battery level", type: "percent", default: 20, range: "5-50", whoMayChange: "admin", wwppId: "WWPP-R30.2", section: "workers" },
      { id: "pp.gd.cpu", label: "CPU profile", legend: "Contributor CPU effort profile the server asks for", type: "enum", default: "medium", range: "low/medium/full", whoMayChange: "admin", wwppId: "WWPP-R30.1", section: "guards" },
      { id: "pp.gd.backoff", label: "Auto back-off", legend: "Drops worker effort only when the device is very very busy", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R30.1", section: "guards" },
      { id: "pp.gd.battery", label: "Battery rule", legend: "Applies the phone battery and heat rules where the device supports them", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R30.1", section: "guards" },
      { id: "pp.gd.memory", label: "Memory cap per worker", legend: "Upper memory bound asked of each worker", type: "number (MiB)", default: "TBD", range: "1-4096", whoMayChange: "admin", wwppId: "WWPP-R30.1", section: "guards" },
      { id: "pp.gd.hidden_tab", label: "Pause on hidden tab", legend: "Deliberately off: hidden and background tabs must keep working", type: "toggle", default: "off", range: "off only", whoMayChange: "admin", wwppId: "WWPP-R30.1", section: "guards" },
      { id: "pp.gpu.enabled", label: "GPU tasks", legend: "Allows jobs to declare cpu, gpu or gpu-preferred tasks in this pool", type: "toggle", default: "on", range: "on/off", jsonType: "boolean", whoMayChange: "admin", wwppId: "WWPP-R33.1", section: "gpu" },
      { id: "pp.gpu.max_slots_per_tab", label: "Max GPU slots per tab", legend: "Maximum concurrent GPU task slots one tab may hold; one task per GPU, at most two GPUs per tab", type: "number", default: 2, range: "1-4", jsonType: "integer", whoMayChange: "admin", wwppId: "WWPP-R33.1", section: "gpu" },
      { id: "pp.gpu.on_battery_allowed", label: "GPU on battery", legend: "Allows GPU leases on phone and laptop tabs while they run on battery; default off", type: "toggle", default: "off", range: "on/off", jsonType: "boolean", whoMayChange: "admin", wwppId: "WWPP-R33.1", section: "gpu" },
      { id: "pp.gpu.time_limit", label: "GPU task time limit", legend: "Maximum runtime of one GPU task", type: "number (s)", default: "TBD", range: "1-3600", whoMayChange: "admin", wwppId: "WWPP-R33.1", section: "gpu" },
      { id: "pp.gpu.memory_limit", label: "GPU memory limit", legend: "Maximum GPU memory one task may request", type: "number (MiB)", default: "TBD", range: "1-8192", whoMayChange: "admin", wwppId: "WWPP-R33.1", section: "gpu" },
      { id: "pp.gpu.charging_only", label: "GPU only while charging", legend: "GPU tasks are eligible only when the device is charging", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R33.1", section: "gpu" },
      { id: "pp.sch.bounded_queue", label: "Bounded queue", legend: "Caps the admitted queue depth; the queue never grows without limit", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "scheduling" },
      { id: "pp.sch.rejection", label: "Queue rejection policy", legend: "What happens when the bounded queue is full", type: "enum", default: "TBD", range: "reject-429/block-timeout/drop-oldest/caller-runs", whoMayChange: "admin", wwppId: "WWPP-R25.3", section: "scheduling" },
      { id: "pp.sch.priority_high", label: "High priority allowed", legend: "Lets the admin allow the high priority class in this pool", type: "toggle", default: "off", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R25.2", section: "scheduling" },
      { id: "pp.sch.fair_share", label: "Fair share", legend: "Shares pool capacity fairly between developers", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "scheduling" },
      { id: "pp.sch.small_task", label: "Small-task warning", legend: "Tasks below this size raise an efficiency warning on all four surfaces", type: "number (tasks)", default: "TBD", range: "1-100", whoMayChange: "admin", wwppId: "WWPP-R24.4", section: "scheduling" },
      { id: "pp.sch.batch_size", label: "Batch size per device class", legend: "Default task batch size handed to a device class", type: "number", default: "TBD", range: "1-1000", whoMayChange: "admin", wwppId: "WWPP-R34.1", section: "scheduling" },
      { id: "pp.ver.hash_check", label: "Hash check on every load", legend: "Verifies artifact and input SHA-256 on every load, including IndexedDB cache hits", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R37.1", section: "verification" },
      { id: "pp.ver.rerun_percent", label: "Result re-run rate", legend: "Percentage of accepted results re-run on another worker; exact hashes only", type: "percent", default: 1, range: "0-100", whoMayChange: "dev-request", wwppId: "WWPP-R35.1", section: "verification" },
      { id: "pp.ver.signed_envelope", label: "Signed task envelope", legend: "Server signs each envelope with Ed25519 over job and task ids, artifact and input hashes and the lease", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R37.1", section: "verification" },
      { id: "pp.ver.canary", label: "Canary known-answer tasks", legend: "Schedules hidden known-answer tasks to test worker trustworthiness", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R37.1", section: "verification" },
      { id: "pp.ver.canary_warn", label: "Canary warn threshold", legend: "Reputation score at or below which the worker is warned", type: "number", default: 75, range: "0-100", whoMayChange: "admin", wwppId: "WWPP-R37.1", section: "verification" },
      { id: "pp.ver.canary_bar", label: "Canary bar threshold", legend: "Reputation score at or below which the worker is barred from tasks", type: "number", default: 50, range: "0-100", whoMayChange: "admin", wwppId: "WWPP-R37.1", section: "verification" },
      { id: "pp.ver.canary_ban", label: "Canary ban threshold", legend: "Reputation score at or below which the worker is banned", type: "number", default: 0, range: "0-100", whoMayChange: "admin", wwppId: "WWPP-R37.1", section: "verification" },
      { id: "pp.ver.reputation_recovery", label: "Reputation recovery", legend: "A ban is lifted by admin review; there is no automatic recovery", type: "enum", default: "manual-review", range: "manual-review/auto", whoMayChange: "admin", wwppId: "WWPP-R37.1", section: "verification" },
      { id: "pp.ver.mode", label: "Verification mode", legend: "(a) server mechanisms plus developer policy, or (b) developer only with results labelled unverified", type: "enum", default: "a", range: "a/b", whoMayChange: "admin", wwppId: "WWPP-R24.1", section: "verification" },
      { id: "pp.ver.k_of_n", label: "k-of-n checks", legend: "Independent checks that must agree before a result counts as verified", type: "number", default: "TBD", range: "1-5", whoMayChange: "admin", wwppId: "WWPP-R24.1", section: "verification" },
      { id: "pp.ver.local_after_download", label: "Verify after download", legend: "Lets developers verify results locally after download", type: "toggle", default: "on", range: "on/off", whoMayChange: "dev-request", wwppId: "WWPP-R24.1", section: "verification" },
      { id: "pp.ver.evidence", label: "Tamper evidence", legend: "(a) server minimal, security log plus SHA-256 of every accepted result, or (b) everything opt-in", type: "enum", default: "a", range: "a/b", whoMayChange: "admin", wwppId: "WWPP-R35.2", section: "verification" },
      { id: "pp.ver.chain", label: "Audit hash chain", legend: "Per-job hash chain written by the server; built and tested even while off", type: "toggle", default: "off", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R26.2", section: "verification" },
      { id: "pp.ver.merkle", label: "Global Merkle checkpoints", legend: "Adds periodic Merkle roots over the tips of job chains", type: "toggle", default: "off", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R22.3", section: "verification" },
      { id: "pp.ver.chain_authority", label: "Chain authority", legend: "(a) admin global Merkle on/off with developer raise-only, or (b) developer free choice inside the allowance", type: "enum", default: "TBD", range: "a/b", whoMayChange: "admin", wwppId: "WWPP-R23.3", section: "verification" },
      isolationRow('pp'),
      { id: "pp.p2p.mode", label: "P2P artifact sharing", legend: "Peers only between tabs locked to the same-app; module artifacts are SHA-256 checked with one HTTP fallback; results and receipts always go to the server. lan = LAN host candidates only (no STUN at all); stun = self-hosted STUN only (no public STUN, no TURN); off = disabled (default per admin)", type: "enum", default: "off", range: "stun/lan/off", whoMayChange: "admin", wwppId: "WWPP-R36.2", section: "p2p" },
      { id: "pp.p2p.peer_scope", label: "Peer selection scope", legend: "Superseded by pp.p2p.mode: peers are same-app tabs and the admin mode decides reachability. Kept as a stored setting; no runtime effect", type: "enum", default: "same-lan", range: "same-device/same-phone/same-lan/near", whoMayChange: "admin", wwppId: "WWPP-R9.1", section: "p2p" },
      { id: "pp.p2p.task_handoff", label: "Task hand-off", legend: "Historical P2P scope, kept as a setting; not an active peer data flow", type: "toggle", default: "off", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R9.1", section: "p2p" },
      { id: "pp.p2p.rtt_probe", label: "RTT probe step", legend: "Uses round-trip time in peer ordering once the probe exists", type: "toggle", default: "off", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R14.1", section: "p2p" },
      { id: "pp.p2p.fallback", label: "CDN fallback", legend: "Falls back to server or CDN when no peer can serve the artifact", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R36.2", section: "p2p" },
      { id: "pp.dew.enabled", label: "Dew", legend: "Offline leases with signed receipts so a tab may submit results later", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R36.2", section: "dew" },
      { id: "pp.dew.window", label: "Dew receipt window", legend: "How long a signed dew receipt stays valid. The Director panel saves this in minutes (1-30, default 10).", type: "minutes", default: 10, range: "1-30", whoMayChange: "admin", wwppId: "WWPP-R36.2", section: "dew" },
      { id: "pp.dew.late_policy", label: "Late-result policy", legend: "(a) archived lease id and attempt match, (b) accept only if no other attempt reported, (c) drop after re-lease", type: "enum", default: "a", range: "a/b/c", whoMayChange: "admin", wwppId: "WWPP-R13.2", section: "dew" },
      { id: "pp.dew.max_attempts", label: "Max attempts", legend: "Attempts allowed for one task before it fails", type: "number", default: 3, range: "1-10", whoMayChange: "admin", wwppId: "WWPP-R32.9", section: "dew" },
      { id: "pp.dew.job_policy", label: "Job failure policy", legend: "What the job does when a task cannot complete", type: "enum", default: "partial", range: "partial/fail-fast/threshold", whoMayChange: "admin", wwppId: "WWPP-R32.9", section: "dew" },
      { id: "pp.dew.quarantine", label: "Quarantine threshold", legend: "Worker crashes before a poison task is quarantined", type: "number", default: 3, range: "1-10", whoMayChange: "admin", wwppId: "WWPP-R32.9", section: "dew" },
      { id: "pp.dew.dead_letter", label: "Dead-letter store", legend: "Keeps late results that arrive after the job ended, with no second credit", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.9", section: "dew" },
      { id: "pp.met.light", label: "Light metrics", legend: "Per-feature timings and counters in one PostgreSQL metrics table; disabled intervals are recorded, never fabricated", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R37.2", section: "metrics" },
      { id: "pp.met.benchmark", label: "Thesis benchmark harness", legend: "Heavy harness for owner-started thesis runs beside the MVP", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R37.2", section: "metrics" },
      { id: "pp.cfg.retention", label: "Version retention", legend: "Number of config versions kept for this admin page", type: "number", default: 10, range: "1-100", whoMayChange: "admin", wwppId: "WWPP-R38.7", section: "config" },
      { id: "pp.cfg.pin", label: "Pin version", legend: "Admin-named pins, for example a thesis run, keep a version from pruning", type: "text", default: "—", range: "1-64 chars", whoMayChange: "admin", wwppId: "WWPP-R38.7", section: "config" },
      { id: "pp.cfg.reason", label: "Reason required", legend: "Each Save records who, when, what changed and why", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R38.6", section: "config" },
      { id: "pp.cfg.rollback", label: "Rollback", legend: "One-click rollback copies an earlier version into a new audited version", type: "button", default: "—", range: "—", whoMayChange: "admin", wwppId: "WWPP-R38.6", section: "config" }
    ]
  },

  "thread-api-admin": {
    pageId: "thread-api-admin",
    title: "Thread Pool API Admin",
    subTitle: "API Keys, Quotas & Settings for /thread-pool/v1 (Tier 3)",
    tier: 3,
    manualHref: "manual/thread-api-admin.html",
    purpose: "Full detail and every setting of the Thread Pool API: keys, quotas, metering, queue, error model, versioning and upload checks. API key issuance and approval belong to the admin account store on the director host.",
    sections: [
      { id: "keys", label: "Keys and Tokens" },
      { id: "quotas", label: "Quotas" },
      { id: "fairness", label: "Fairness, Priority & Metering" },
      { id: "queue", label: "Queue Settings" },
      { id: "errors", label: "Errors & Contracts" },
      { id: "upload", label: "Upload Checks" },
      { id: "versions", label: "API Versioning" },
      { id: "config", label: "Config Versions & Audit" },
      { id: "manual", label: "Manual Section", detailHref: "manual/thread-api-admin.html" }
    ],
    rows: [
      { id: "ta.keys.issuance", label: "Key issuance mode", legend: "Manual issue by the admin, or developer self-service", type: "enum", default: "manual", range: "manual/self-service", whoMayChange: "admin", wwppId: "WWPP-R20.4", section: "keys" },
      { id: "ta.keys.approval", label: "Key approval mode", legend: "Approve each key request, or auto-approve all", type: "enum", default: "approve-each", range: "approve-each/auto-approve-all", whoMayChange: "admin", wwppId: "WWPP-R20.4", section: "keys" },
      { id: "ta.keys.lifetime", label: "Key lifetime", legend: "How long an issued key stays usable; the key is shown once and stored hashed", type: "duration", default: "TBD", range: "1h-365d", whoMayChange: "admin", wwppId: "WWPP-R32.4", section: "keys" },
      { id: "ta.keys.scope", label: "Key scope", legend: "Permissions bound to a key, such as pool, artifacts, quota or events", type: "multi-select", default: "TBD", range: "pool/artifacts/quota/events", whoMayChange: "admin", wwppId: "WWPP-R32.4", section: "keys" },
      { id: "ta.keys.jwt_ttl", label: "JWT lifetime", legend: "Lifetime of the short token exchanged from the key", type: "minutes", default: 15, range: "1-60", whoMayChange: "admin", wwppId: "WWPP-R32.4", section: "keys" },
      { id: "ta.keys.oauth2", label: "OAuth2 client credentials", legend: "Optional token endpoint for future SSO", type: "toggle", default: "off", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.4", section: "keys" },
      { id: "ta.keys.revoke", label: "Key revocation", legend: "Revokes a key at once; revoked authority cannot mint or renew leases", type: "button", default: "—", range: "—", whoMayChange: "admin", wwppId: "WWPP-R25.1", section: "keys" },
      { id: "ta.q.running_jobs", label: "Running jobs quota", legend: "Jobs that may run at the same time on one key", type: "number", default: "TBD", range: "1-1000", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "quotas" },
      { id: "ta.q.queued_tasks", label: "Queued tasks quota", legend: "Tasks that may wait in the queue on one key", type: "number", default: "TBD", range: "1-100000", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "quotas" },
      { id: "ta.q.task_size", label: "Task size quota", legend: "Maximum size of one task payload", type: "number (KiB)", default: "TBD", range: "1-1048576", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "quotas" },
      { id: "ta.q.task_time", label: "Task time quota", legend: "Maximum time one task may run", type: "number (s)", default: "TBD", range: "1-86400", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "quotas" },
      { id: "ta.q.req_per_min", label: "Requests per minute", legend: "Maximum API requests per minute on one key", type: "number", default: "TBD", range: "1-100000", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "quotas" },
      { id: "ta.q.retry_after", label: "429 Retry-After", legend: "Sends Retry-After with every rate-limit rejection", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "quotas" },
      { id: "ta.fair.share", label: "Fair-share scheduler", legend: "Shares capacity fairly between developers", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "fairness" },
      { id: "ta.fair.priority_high", label: "High priority class", legend: "Allows the high priority class when the admin permits it", type: "toggle", default: "off", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R25.2", section: "fairness" },
      { id: "ta.fair.credits", label: "Credits and metering", legend: "Meters CPU-seconds and GPU-seconds per key and reports them", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "fairness" },
      { id: "ta.fair.rewards", label: "Reports and rewards", legend: "Issues reports and rewards from metered credits; no payment now", type: "toggle", default: "off", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "fairness" },
      { id: "ta.queue.bounded", label: "Bounded queue", legend: "Caps the admitted queue depth", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R25.3", section: "queue" },
      { id: "ta.queue.rejection", label: "Rejection policy", legend: "What happens when the queue is full", type: "enum", default: "TBD", range: "reject-429/block-timeout/drop-oldest/caller-runs", whoMayChange: "admin", wwppId: "WWPP-R25.3", section: "queue" },
      { id: "ta.err.problem_json", label: "RFC 9457 problem+json", legend: "Same error envelope and code list on REST, SSE and gRPC", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.8", section: "errors" },
      { id: "ta.err.idempotency", label: "Idempotency keys", legend: "Same key and body replays; a different body is a conflict", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.8", section: "errors" },
      { id: "ta.err.traceparent", label: "traceparent support", legend: "Accepts W3C traceparent on submit and echoes it into the audit chain", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.8", section: "errors" },
      { id: "ta.err.cursor", label: "Cursor paging", legend: "Opaque cursor paging with limit on list routes", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.8", section: "errors" },
      { id: "ta.upload.format", label: "Format check", legend: "Checks the uploaded module and data format", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R36.1", section: "upload" },
      { id: "ta.upload.size", label: "Size check", legend: "Enforces the maximum upload size", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R36.1", section: "upload" },
      { id: "ta.upload.hash", label: "Hash check", legend: "Checks uploaded bytes against the declared SHA-256", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R36.1", section: "upload" },
      { id: "ta.upload.scan", label: "Import and permission scan", legend: "Scans imports and permissions in the uploaded code", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R36.1", section: "upload" },
      { id: "ta.upload.review_gate", label: "Review gate", legend: "A new code upload waits for admin review", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R36.1", section: "upload" },
      { id: "ta.upload.malware", label: "Malware and abuse scan", legend: "Runs the malware and abuse scan on uploads", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R36.1", section: "upload" },
      { id: "ta.ver.major", label: "URL major version", legend: "Major version segment of this route family", type: "text", default: "v1", range: "v1", whoMayChange: "admin", wwppId: "WWPP-R32.6", section: "versions" },
      { id: "ta.ver.notice", label: "Deprecation notice", legend: "Notice period before the next major version", type: "months", default: 6, range: "3-12", whoMayChange: "admin", wwppId: "WWPP-R32.6", section: "versions" },
      { id: "ta.ver.contract_tests", label: "Contract tests", legend: "Contract tests generated from OpenAPI run on every change", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.6", section: "versions" },
      { id: "ta.cfg.retention", label: "Version retention", legend: "Number of config versions kept for this admin page", type: "number", default: 10, range: "1-100", whoMayChange: "admin", wwppId: "WWPP-R38.7", section: "config" },
      { id: "ta.cfg.pin", label: "Pin version", legend: "Admin-named pins keep a version from pruning", type: "text", default: "—", range: "1-64 chars", whoMayChange: "admin", wwppId: "WWPP-R38.7", section: "config" },
      { id: "ta.cfg.reason", label: "Reason required", legend: "Each Save records who, when, what changed and why", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R38.6", section: "config" },
      { id: "ta.cfg.rollback", label: "Rollback", legend: "One-click rollback copies an earlier version into a new audited version", type: "button", default: "—", range: "—", whoMayChange: "admin", wwppId: "WWPP-R38.6", section: "config" }
    ]
  },

  "process-api-admin": {
    pageId: "process-api-admin",
    title: "Process Pool API Admin",
    subTitle: "API Keys, Quotas & Session Settings for /process-pool/v1 (Tier 3)",
    tier: 3,
    manualHref: "manual/process-api-admin.html",
    purpose: "Full detail and every setting of the Process Pool API: keys, quotas, metering, sessions, queue, error model, versioning and upload checks. It mirrors the Thread Pool API admin page and adds the session settings that only exist for the process pool.",
    sections: [
      { id: "keys", label: "Keys and Tokens" },
      { id: "quotas", label: "Quotas" },
      { id: "fairness", label: "Fairness, Priority & Metering" },
      { id: "sessions", label: "Sessions" },
      { id: "queue", label: "Queue Settings" },
      { id: "errors", label: "Errors & Contracts" },
      { id: "upload", label: "Upload Checks" },
      { id: "versions", label: "API Versioning" },
      { id: "config", label: "Config Versions & Audit" },
      { id: "manual", label: "Manual Section", detailHref: "manual/process-api-admin.html" }
    ],
    rows: [
      { id: "pa.keys.issuance", label: "Key issuance mode", legend: "Manual issue by the admin, or developer self-service", type: "enum", default: "manual", range: "manual/self-service", whoMayChange: "admin", wwppId: "WWPP-R20.4", section: "keys" },
      { id: "pa.keys.approval", label: "Key approval mode", legend: "Approve each key request, or auto-approve all", type: "enum", default: "approve-each", range: "approve-each/auto-approve-all", whoMayChange: "admin", wwppId: "WWPP-R20.4", section: "keys" },
      { id: "pa.keys.lifetime", label: "Key lifetime", legend: "How long an issued key stays usable; the key is shown once and stored hashed", type: "duration", default: "TBD", range: "1h-365d", whoMayChange: "admin", wwppId: "WWPP-R32.4", section: "keys" },
      { id: "pa.keys.scope", label: "Key scope", legend: "Permissions bound to a key, such as pool, artifacts, quota or events", type: "multi-select", default: "TBD", range: "pool/artifacts/quota/events", whoMayChange: "admin", wwppId: "WWPP-R32.4", section: "keys" },
      { id: "pa.keys.jwt_ttl", label: "JWT lifetime", legend: "Lifetime of the short token exchanged from the key", type: "minutes", default: 15, range: "1-60", whoMayChange: "admin", wwppId: "WWPP-R32.4", section: "keys" },
      { id: "pa.keys.oauth2", label: "OAuth2 client credentials", legend: "Optional token endpoint for future SSO", type: "toggle", default: "off", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.4", section: "keys" },
      { id: "pa.keys.revoke", label: "Key revocation", legend: "Revokes a key at once; revoked authority cannot mint or renew leases", type: "button", default: "—", range: "—", whoMayChange: "admin", wwppId: "WWPP-R25.1", section: "keys" },
      { id: "pa.q.running_jobs", label: "Running jobs quota", legend: "Jobs that may run at the same time on one key", type: "number", default: "TBD", range: "1-1000", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "quotas" },
      { id: "pa.q.queued_tasks", label: "Queued tasks quota", legend: "Tasks that may wait in the queue on one key", type: "number", default: "TBD", range: "1-100000", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "quotas" },
      { id: "pa.q.task_size", label: "Task size quota", legend: "Maximum size of one task payload", type: "number (KiB)", default: "TBD", range: "1-1048576", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "quotas" },
      { id: "pa.q.task_time", label: "Task time quota", legend: "Maximum time one task may run", type: "number (s)", default: "TBD", range: "1-86400", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "quotas" },
      { id: "pa.q.req_per_min", label: "Requests per minute", legend: "Maximum API requests per minute on one key", type: "number", default: "TBD", range: "1-100000", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "quotas" },
      { id: "pa.q.retry_after", label: "429 Retry-After", legend: "Sends Retry-After with every rate-limit rejection", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "quotas" },
      { id: "pa.fair.share", label: "Fair-share scheduler", legend: "Shares capacity fairly between developers", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "fairness" },
      { id: "pa.fair.priority_high", label: "High priority class", legend: "Allows the high priority class when the admin permits it", type: "toggle", default: "off", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R25.2", section: "fairness" },
      { id: "pa.fair.credits", label: "Credits and metering", legend: "Meters CPU-seconds and GPU-seconds per key and reports them", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "fairness" },
      { id: "pa.fair.rewards", label: "Reports and rewards", legend: "Issues reports and rewards from metered credits; no payment now", type: "toggle", default: "off", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.5", section: "fairness" },
      { id: "pa.ses.enabled", label: "Session API", legend: "Enables the explicit process-pool session API beside plain jobs", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.2", section: "sessions" },
      { id: "pa.ses.shape", label: "Session shape", legend: "(a) tab session lease, or (b) task-group pinning", type: "enum", default: "TBD", range: "a/b", whoMayChange: "dev-request", wwppId: "WWPP-R19.1", section: "sessions" },
      { id: "pa.ses.lease_minutes", label: "Session lease duration", legend: "Minutes a leased session stays warm before release", type: "number (min)", default: "TBD", range: "1-1440", whoMayChange: "admin", wwppId: "WWPP-R19.1", section: "sessions" },
      { id: "pa.ses.tab_count", label: "Tabs per session", legend: "Tabs leased for one session", type: "number", default: "TBD", range: "1-64", whoMayChange: "admin", wwppId: "WWPP-R19.1", section: "sessions" },
      { id: "pa.ses.host_count", label: "Hosts per session", legend: "Distinct registered devices per session; one up to the tab count", type: "number", default: "TBD", range: "1-64", whoMayChange: "admin", wwppId: "WWPP-R39.1", section: "sessions" },
      { id: "pa.ses.warm_initializer", label: "Warm initializer", legend: "Artifact preloaded into the session tab before tasks arrive", type: "toggle", default: "off", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R19.1", section: "sessions" },
      { id: "pa.ses.initializer_state", label: "Initializer state", legend: "Default state of the warm initializer between sessions", type: "enum", default: "TBD", range: "keep/discard", whoMayChange: "admin", wwppId: "WWPP-R25.3", section: "sessions" },
      { id: "pa.ses.close_policy", label: "Session close", legend: "What happens to the tabs on close, expiry or error", type: "enum", default: "release", range: "release/drain", whoMayChange: "admin", wwppId: "WWPP-R19.1", section: "sessions" },
      { id: "pa.queue.bounded", label: "Bounded queue", legend: "Caps the admitted queue depth", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R25.3", section: "queue" },
      { id: "pa.queue.rejection", label: "Rejection policy", legend: "What happens when the queue is full", type: "enum", default: "TBD", range: "reject-429/block-timeout/drop-oldest/caller-runs", whoMayChange: "admin", wwppId: "WWPP-R25.3", section: "queue" },
      { id: "pa.err.problem_json", label: "RFC 9457 problem+json", legend: "Same error envelope and code list on REST, SSE and gRPC", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.8", section: "errors" },
      { id: "pa.err.idempotency", label: "Idempotency keys", legend: "Same key and body replays; a different body is a conflict", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.8", section: "errors" },
      { id: "pa.err.traceparent", label: "traceparent support", legend: "Accepts W3C traceparent on submit and echoes it into the audit chain", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.8", section: "errors" },
      { id: "pa.err.cursor", label: "Cursor paging", legend: "Opaque cursor paging with limit on list routes", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.8", section: "errors" },
      { id: "pa.upload.format", label: "Format check", legend: "Checks the uploaded module and data format", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R36.1", section: "upload" },
      { id: "pa.upload.size", label: "Size check", legend: "Enforces the maximum upload size", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R36.1", section: "upload" },
      { id: "pa.upload.hash", label: "Hash check", legend: "Checks uploaded bytes against the declared SHA-256", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R36.1", section: "upload" },
      { id: "pa.upload.scan", label: "Import and permission scan", legend: "Scans imports and permissions in the uploaded code", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R36.1", section: "upload" },
      { id: "pa.upload.review_gate", label: "Review gate", legend: "A new code upload waits for admin review", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R36.1", section: "upload" },
      { id: "pa.upload.malware", label: "Malware and abuse scan", legend: "Runs the malware and abuse scan on uploads", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R36.1", section: "upload" },
      { id: "pa.ver.major", label: "URL major version", legend: "Major version segment of this route family", type: "text", default: "v1", range: "v1", whoMayChange: "admin", wwppId: "WWPP-R32.6", section: "versions" },
      { id: "pa.ver.notice", label: "Deprecation notice", legend: "Notice period before the next major version", type: "months", default: 6, range: "3-12", whoMayChange: "admin", wwppId: "WWPP-R32.6", section: "versions" },
      { id: "pa.ver.contract_tests", label: "Contract tests", legend: "Contract tests generated from OpenAPI run on every change", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R32.6", section: "versions" },
      { id: "pa.cfg.retention", label: "Version retention", legend: "Number of config versions kept for this admin page", type: "number", default: 10, range: "1-100", whoMayChange: "admin", wwppId: "WWPP-R38.7", section: "config" },
      { id: "pa.cfg.pin", label: "Pin version", legend: "Admin-named pins keep a version from pruning", type: "text", default: "—", range: "1-64 chars", whoMayChange: "admin", wwppId: "WWPP-R38.7", section: "config" },
      { id: "pa.cfg.reason", label: "Reason required", legend: "Each Save records who, when, what changed and why", type: "toggle", default: "on", range: "on/off", whoMayChange: "admin", wwppId: "WWPP-R38.6", section: "config" },
      { id: "pa.cfg.rollback", label: "Rollback", legend: "One-click rollback copies an earlier version into a new audited version", type: "button", default: "—", range: "—", whoMayChange: "admin", wwppId: "WWPP-R38.6", section: "config" }
    ]
  }
};

// Validation function for checking input against row ranges
export function validateRowValue(row, value) {
  if (!row) return { valid: false, error: "Invalid row definition" };
  if (value === undefined || value === null || value === "") {
    if (row.default === "TBD") return { valid: true };
    return { valid: false, error: "Value cannot be empty" };
  }

  const range = row.range;
  if (!range || range === "—" || range === "live") return { valid: true };

  // Specific exact value (e.g. 'off only' or 'v1') - checked BEFORE toggle branch so 'off only' rejects 'on'
  if (range === "off only") {
    const s = String(value).toLowerCase();
    if (s === "off" || s === "false") return { valid: true };
    return { valid: false, error: "Must be 'off'" };
  }
  if (range === "v1" && String(value) !== "v1") return { valid: false, error: "Must be 'v1'" };

  // Multi-select validation (e.g. pool/artifacts/quota/events)
  if (row.type === "multi-select") {
    const allowed = range.split("/").map(s => s.trim());
    let items = [];
    if (Array.isArray(value)) {
      items = value;
    } else if (typeof value === "string") {
      if (value.startsWith("[") && value.endsWith("]")) {
        try { items = JSON.parse(value); } catch (_) { items = value.split(","); }
      } else {
        items = value.split(",").map(s => s.trim()).filter(Boolean);
      }
    } else {
      return { valid: false, error: "Must be a selection of allowed options" };
    }
    if (items.length === 0) {
      if (row.default === "TBD") return { valid: true };
      return { valid: false, error: "At least one option must be selected" };
    }
    const invalid = items.filter(v => !allowed.includes(String(v).trim()));
    if (invalid.length > 0) {
      return { valid: false, error: `Invalid selection: ${invalid.join(", ")}. Allowed: ${allowed.join(", ")}` };
    }
    return { valid: true };
  }

  // Duration validation (e.g. '1h-365d')
  if (range === "1h-365d" || row.type === "duration") {
    const match = String(value).trim().match(/^(\d+)\s*(h|d|m|s)$/i);
    if (!match) return { valid: false, error: "Must be a valid duration (e.g. 1h, 24h, 7d, 365d)" };
    const num = parseInt(match[1], 10);
    const unit = match[2].toLowerCase();
    let hours = num;
    if (unit === "d") hours = num * 24;
    else if (unit === "m") hours = num / 60;
    else if (unit === "s") hours = num / 3600;
    if (range === "1h-365d") {
      if (hours < 1 || hours > 365 * 24) {
        return { valid: false, error: "Duration must be between 1h and 365d" };
      }
    }
    return { valid: true };
  }

  // Toggle boolean / on/off validation
  if (row.type === "toggle" || range === "on/off") {
    const s = String(value).toLowerCase();
    if (s === "on" || s === "off" || s === "true" || s === "false") return { valid: true };
    return { valid: false, error: "Must be 'on' or 'off'" };
  }

  // Mixed range e.g. "auto/1-32"
  if (range === "auto/1-32") {
    if (String(value) === "auto") return { valid: true };
    const num = Number(value);
    if (!isNaN(num) && Number.isInteger(num) && num >= 1 && num <= 32) return { valid: true };
    return { valid: false, error: "Must be 'auto' or an integer between 1 and 32" };
  }

  // Enum validation (e.g. leader/each-tab, info/warn/error, a/b)
  if (range.includes("/")) {
    const allowed = range.split("/").map(s => s.trim());
    if (allowed.includes(String(value))) return { valid: true };
    return { valid: false, error: `Must be one of: ${allowed.join(", ")}` };
  }

  // String length validation (e.g. 1-64 chars)
  const charMatch = range.match(/^(\d+)-(\d+)\s*chars$/);
  if (charMatch) {
    const min = parseInt(charMatch[1], 10);
    const max = parseInt(charMatch[2], 10);
    const len = String(value).length;
    if (len >= min && len <= max) return { valid: true };
    return { valid: false, error: `Length must be between ${min} and ${max} characters` };
  }

  // Numeric range validation (e.g. 1-4, 5-300, 1-100000)
  const numMatch = range.match(/^(\d+)-(\d+)$/);
  if (numMatch) {
    const min = parseInt(numMatch[1], 10);
    const max = parseInt(numMatch[2], 10);
    const num = Number(value);
    if (isNaN(num)) return { valid: false, error: "Must be a valid number" };
    const isCountOrInteger = !row.type || !row.type.includes("percent");
    if (isCountOrInteger && !Number.isInteger(num)) {
      return { valid: false, error: "Must be a whole number (integer)" };
    }
    if (num < min || num > max) return { valid: false, error: `Number must be between ${min} and ${max}` };
    return { valid: true };
  }

  return { valid: true };
}

// END OF schema.js

