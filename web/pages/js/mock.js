// WO-HC-WEB-01 mock.js - In-Memory Mock Data & SSE Simulator (?mock=1 Debug Mode)
// Author: Claude Opus 5.5 head / agy Gemini 3.8 Flash high. Date: 2026-11-17.
// Truth: F:/tmp/hcwork/ctx_rows_R37_R40.md (R38.5b, R38.6, R40.1) + design doc sec 14

import { PAGE_SCHEMAS } from './schema.js';

export function isMockMode() {
  if (typeof window === 'undefined') return false;
  const params = new URLSearchParams(window.location.search);
  if (params.get('mock') === '1' || params.get('mock') === 'true') return true;
  if (typeof sessionStorage !== 'undefined' && sessionStorage.getItem('ccgsc_mock') === '1') return true;
  return false;
}

// Generate default mock config for each page from schema defaults
function buildDefaultSettings(pageId) {
  const schema = PAGE_SCHEMAS[pageId];
  if (!schema) return {};
  const settings = {};
  for (const row of schema.rows) {
    if (row.type !== 'button' && row.type !== 'number (live)' && row.type !== 'duration (live)' && row.type !== 'percent (live)') {
      settings[row.id] = row.default === 'TBD' ? '' : row.default;
    }
  }
  return settings;
}

// Mock State Stores
const mockConfigs = {
  'thread-pool-admin': {
    currentVersion: 2,
    activeSettings: buildDefaultSettings('thread-pool-admin'),
    versions: [
      {
        version: 1,
        createdAt: '2026-11-17T10:00:00Z',
        author: 'admin@digitalbd.org',
        reason: 'Initial baseline thread pool configuration',
        pinnedName: null,
        inUseByJobs: 0,
        settings: buildDefaultSettings('thread-pool-admin')
      },
      {
        version: 2,
        createdAt: '2026-11-17T14:30:00Z',
        author: 'admin@digitalbd.org',
        reason: 'Owner thesis run pin: tamper measures verified on all worker tabs',
        pinnedName: 'thesis-baseline-01',
        inUseByJobs: 2,
        settings: { ...buildDefaultSettings('thread-pool-admin'), 'tp.ver.rerun_percent': 2 }
      }
    ]
  },
  'process-pool-admin': {
    currentVersion: 1,
    activeSettings: buildDefaultSettings('process-pool-admin'),
    versions: [
      {
        version: 1,
        createdAt: '2026-11-17T11:00:00Z',
        author: 'admin@digitalbd.org',
        reason: 'Initial process pool config with wwHost session limits',
        pinnedName: null,
        inUseByJobs: 1,
        settings: buildDefaultSettings('process-pool-admin')
      }
    ]
  },
  'thread-api-admin': {
    currentVersion: 1,
    activeSettings: buildDefaultSettings('thread-api-admin'),
    versions: [
      {
        version: 1,
        createdAt: '2026-11-17T11:15:00Z',
        author: 'admin@digitalbd.org',
        reason: 'Thread pool API rate limit and quota defaults',
        pinnedName: null,
        inUseByJobs: 0,
        settings: buildDefaultSettings('thread-api-admin')
      }
    ]
  },
  'process-api-admin': {
    currentVersion: 1,
    activeSettings: buildDefaultSettings('process-api-admin'),
    versions: [
      {
        version: 1,
        createdAt: '2026-11-17T11:30:00Z',
        author: 'admin@digitalbd.org',
        reason: 'Process pool API session quotas and error envelope settings',
        pinnedName: null,
        inUseByJobs: 0,
        settings: buildDefaultSettings('process-api-admin')
      }
    ]
  }
};

// T83 (round 8) mock worker records for the read-only monitor devices panel.
// The desktop tab never reported GPUs, so its record carries no gpu/gpuSlots/gpus
// fields at all - absent, never zero (owner rule).
const mockWorkers = [
  {
    tabId: 'tab-laptop-01',
    hostId: 'host-01',
    owner: 'contributor-01',
    poolId: 'thread-pool',
    slots: 4,
    gpu: true,
    gpuSlots: 2,
    gpus: [{ vendor: 'amd', architecture: 'rdna3' }],
    deviceClass: 'laptop',
    charging: true,
    heartbeat: '2026-11-19T09:30:00Z'
  },
  {
    tabId: 'tab-desktop-02',
    hostId: 'host-01',
    owner: 'contributor-02',
    poolId: 'process-pool',
    slots: 4,
    deviceClass: 'desktop',
    heartbeat: '2026-11-19T09:31:00Z'
  }
];

const mockAuditLogs = [
  { id: 101, at: '2026-11-17T14:30:00Z', actor: 'admin@digitalbd.org', action: 'SAVE', page: 'thread-pool-admin', from_version: 1, to_version: 2, diff: { 'tp.ver.rerun_percent': [1, 2] } },
  { id: 100, at: '2026-11-17T11:30:00Z', actor: 'admin@digitalbd.org', action: 'INIT', page: 'process-api-admin', from_version: null, to_version: 1, diff: {} },
  { id: 99, at: '2026-11-17T11:15:00Z', actor: 'admin@digitalbd.org', action: 'INIT', page: 'thread-api-admin', from_version: null, to_version: 1, diff: {} },
  { id: 98, at: '2026-11-17T11:00:00Z', actor: 'admin@digitalbd.org', action: 'INIT', page: 'process-pool-admin', from_version: null, to_version: 1, diff: {} },
  { id: 97, at: '2026-11-17T10:00:00Z', actor: 'admin@digitalbd.org', action: 'INIT', page: 'thread-pool-admin', from_version: null, to_version: 1, diff: {} }
];

// Dynamic mock metrics generator
export function getMockLiveMetrics() {
  const t = Date.now() / 1000;
  return {
    timestamp: new Date().toISOString(),
    // Capacity
    'mon.cap.contributors': 184 + Math.floor(Math.sin(t / 10) * 8),
    'mon.cap.tabs': 212 + Math.floor(Math.cos(t / 10) * 12),
    'mon.cap.cpu_workers': 640 + Math.floor(Math.sin(t / 8) * 30),
    'mon.cap.gpu_workers': 42 + Math.floor(Math.cos(t / 15) * 4),
    'mon.cap.free_slots_thread': 190 + Math.floor(Math.sin(t / 5) * 40),
    'mon.cap.free_slots_process': 68 + Math.floor(Math.cos(t / 6) * 15),

    // Work
    'mon.work.running': 16 + Math.floor(Math.sin(t / 7) * 5),
    'mon.work.queued': 4 + Math.max(0, Math.floor(Math.cos(t / 9) * 4)),
    'mon.work.failed': 1,
    'mon.work.tasks_per_s': +(142.6 + Math.sin(t / 4) * 18.2).toFixed(1),
    'mon.work.avg_task_time': '58.4 ms',

    // Trust
    'mon.trust.hash_fails': 0,
    'mon.trust.rerun_mismatch': 0,
    'mon.trust.canary_fails': 0,
    'mon.trust.quarantined': 0,
    'mon.trust.banned': 0,

    // API & System
    'mon.api.req_per_min': 1420 + Math.floor(Math.sin(t / 3) * 120),
    'mon.api.4xx': 3,
    'mon.api.5xx': 0,
    'mon.api.429': 1,
    'mon.api.p95': '3.2 ms',
    'mon.sys.cpu': 24 + Math.floor(Math.sin(t / 8) * 6),
    'mon.sys.ram': 48,
    'mon.sys.disk': 37,
    'mon.sys.pg_conns': 22,
    'mon.sys.sse_conns': 38,

    // Pool monitor specific
    'pm.work.tasks_per_s': +(142.6 + Math.sin(t / 4) * 18.2).toFixed(1),
    'pm.work.avg_task_time': '58.4 ms',
    'pm.queue.depth': 4,
    'pm.queue.rejected': 0,
    'pm.trust.hash_fails': 0,
    'pm.trust.rerun_mismatch': 0,
    'pm.trust.canary_fails': 0,
    'pm.trust.banned': 0,

    // API monitor specific
    'am.traffic.req_per_min': 1420 + Math.floor(Math.sin(t / 3) * 120),
    'am.traffic.p95': '3.2 ms',
    'am.err.4xx': 3,
    'am.err.5xx': 0,
    'am.err.429': 1,
    'am.sys.pg_conns': 22,
    'am.sys.sse_conns': 38
  };
}

// Mock Data Router
export function getMockData(endpoint) {
  if (endpoint === 'metrics') {
    return getMockLiveMetrics();
  }
  if (endpoint.startsWith('config/')) {
    const pageId = endpoint.replace('config/', '');
    const entry = mockConfigs[pageId];
    if (entry) {
      return {
        page: pageId,
        version: entry.currentVersion,
        settings: entry.activeSettings,
        versions: entry.versions
      };
    }
    return {
      page: pageId,
      version: 1,
      settings: buildDefaultSettings(pageId),
      versions: [
        {
          version: 1,
          createdAt: new Date().toISOString(),
          author: 'admin@digitalbd.org',
          reason: 'Initial default config',
          pinnedName: null,
          inUseByJobs: 0,
          settings: buildDefaultSettings(pageId)
        }
      ]
    };
  }
  if (endpoint === 'audit') {
    return { logs: mockAuditLogs };
  }
  if (endpoint === 'workers') {
    return { workers: mockWorkers, count: mockWorkers.length };
  }
  // T92 | Codex | 2026-11-20: explicit mock-only legacy summary, matching Node.
  if (endpoint === 'legacy-workers') {
    return {
      available: true, source: 'node-pool-admin', fetchedAt: new Date().toISOString(),
      workers: [],
      detailsUnavailable: 'Node summary exposes cluster counts only; worker IDs and last-seen times are not provided.',
      raw: { totalWorkers: 3, liveWorkers: 2, staleWorkers: 1, clusterCount: 1,
        clusters: { 'old-node-pool': { total: 3, live: 2, stale: 1 } } }
    };
  }
  return { status: 'ok' };
}

// Mock POST handler for config save and rollback
export function handleMockPost(endpoint, payload) {
  if (endpoint.startsWith('config/') && endpoint.endsWith('/rollback')) {
    const pageId = endpoint.split('/')[1];
    const entry = mockConfigs[pageId];
    if (!entry) throw new Error('Page not found');

    const targetVer = payload.rollbackVersion;
    const targetObj = entry.versions.find(v => v.version === targetVer);
    if (!targetObj) throw new Error(`Target rollback version ${targetVer} not found`);

    const newVer = entry.currentVersion + 1;
    const newRecord = {
      version: newVer,
      createdAt: new Date().toISOString(),
      author: 'admin@digitalbd.org',
      reason: payload.reason || `Rollback to version ${targetVer}`,
      pinnedName: null,
      inUseByJobs: 0,
      settings: { ...targetObj.settings }
    };

    entry.currentVersion = newVer;
    entry.activeSettings = newRecord.settings;
    entry.versions.unshift(newRecord);

    mockAuditLogs.unshift({
      id: mockAuditLogs.length + 101,
      at: newRecord.createdAt,
      actor: 'admin@digitalbd.org',
      action: 'ROLLBACK',
      page: pageId,
      from_version: targetVer,
      to_version: newVer,
      diff: { rollbackFrom: targetVer }
    });

    return { success: true, version: newVer, settings: entry.activeSettings };
  }

  if (endpoint.startsWith('config/')) {
    const pageId = endpoint.replace('config/', '');
    let entry = mockConfigs[pageId];
    if (!entry) {
      entry = { currentVersion: 1, activeSettings: buildDefaultSettings(pageId), versions: [] };
      mockConfigs[pageId] = entry;
    }

    if (payload && payload.expectedVersion !== undefined && payload.expectedVersion !== null && payload.expectedVersion !== entry.currentVersion) {
      const err = new Error(`Configuration version conflict: expected v${payload.expectedVersion}, but current version is v${entry.currentVersion}. Please reload.`);
      err.name = 'ProblemError';
      err.status = 409;
      err.type = 'https://ccgsc.digitalbd.org/errors/version-conflict';
      err.title = 'Version Conflict';
      err.detail = err.message;
      throw err;
    }

    const newVer = entry.currentVersion + 1;
    const newRecord = {
      version: newVer,
      createdAt: new Date().toISOString(),
      author: 'admin@digitalbd.org',
      reason: payload.reason,
      pinnedName: payload.pinName || null,
      inUseByJobs: 0,
      settings: { ...payload.settings }
    };

    entry.currentVersion = newVer;
    entry.activeSettings = newRecord.settings;
    entry.versions.unshift(newRecord);

    mockAuditLogs.unshift({
      id: mockAuditLogs.length + 101,
      at: newRecord.createdAt,
      actor: 'admin@digitalbd.org',
      action: 'SAVE',
      page: pageId,
      from_version: entry.currentVersion - 1,
      to_version: newVer,
      diff: payload.settings
    });

    return { success: true, version: newVer, settings: entry.activeSettings };
  }

  return { success: true };
}

// Simulated SSE Event Stream
export function createMockEventStream(onMessage, onError) {
  let active = true;
  const timer = setInterval(() => {
    if (!active) return;
    try {
      if (onMessage) onMessage(getMockLiveMetrics());
    } catch (err) {
      if (onError) onError(err);
    }
  }, 2000);

  // Send initial data immediately
  setTimeout(() => {
    if (active && onMessage) onMessage(getMockLiveMetrics());
  }, 100);

  return {
    close() {
      active = false;
      clearInterval(timer);
    }
  };
}

// END OF mock.js

