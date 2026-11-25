// Path: web/pages/js/worker_budget_T104.js | Author: GLM (Z.ai) for Claude Opus 5.5 head | 2026-11-21
// What/why: T104 step 1 - the shared worker budget of the integrated contributor
// page. One tab, one budget: the CPU worker lanes of every consumer (the new pools
// ContributeApp under owner "pools", the old Node lanes through the
// LegacyLaneAdapter under owner "legacy:<lane>") claim slots here, and the sum of
// all claims never exceeds the tab budget. The auto default mirrors the
// computeWorkerCount rule of contribute.js (desktop cores-1 capped at 4, phone 1 on
// battery / 2 charging), so a tab with a single consumer keeps exactly today's
// worker count; sharing only starts when a second lane claims.
//
// GPU lanes are counted by computeTabBudget({gpuSlots}) and, since this round,
// are claimed against the same budget: the new pools claim them under owner
// "pools:gpu", the old lanes through their adapter. The battery guard
// (batteryAllowsGPU, no GPU under 20 %), the GPU-first picker and the adapter cap
// of 2 (T83) stay in force, so the total a lane can hold is bounded twice: by
// its own guard and by the tab ceiling. The budget coordinates lanes; it never
// shares memory (WWPP-R10.0).
//
// Truth: docs/T104_INTEGRATED_PAGE_PLAN.md, GRILL_WWPP_R1.md (R29.4, R30.2,
// R42.5, R42.6), owner T104 rules (issue #117).

'use strict';

// GPU lanes per tab stay capped by the picker rule of T83.
export const GPU_LANE_CAP = 2;

// Budget owners of the integrated page. The new pools claim their CPU slots as
// POOLS_BUDGET_OWNER and their GPU slots as POOLS_GPU_BUDGET_OWNER; every old
// lane claims as LEGACY_OWNER_PREFIX + lane (legacy_lane_adapter_T104.js).
export const POOLS_BUDGET_OWNER = 'pools';
export const POOLS_GPU_BUDGET_OWNER = 'pools:gpu';

// The share of battery charge under which a phone must not run GPU lanes.
// Owner rule (T104): no GPU under 20 % battery.
const GPU_BATTERY_FLOOR = 0.2;

/**
 * The GPU guard of the integrated page as a pure rule.
 *
 * Charging always allows GPU. With a battery present the level decides: no GPU
 * under 20 % battery (unknown level is treated conservatively as too low). With
 * no battery signal at all a phone stays conservative (the Battery API exists on
 * phones; its absence means it was blocked) and a desktop is allowed.
 *
 * @param {Object} [opts]
 * @param {boolean} [opts.hasBattery]
 * @param {number} [opts.level] 0..1
 * @param {boolean} [opts.isCharging]
 * @param {boolean} [opts.isPhone]
 * @returns {boolean}
 */
export function batteryAllowsGPU(opts = {}) {
  if (opts.isCharging === true) return true;
  if (opts.hasBattery === true) {
    return typeof opts.level === 'number' && opts.level >= GPU_BATTERY_FLOOR;
  }
  return opts.isPhone !== true;
}

/**
 * The tab budget as a pure rule (WWPP-R29.4, R30.2, owner T104 rules).
 *
 * CPU part: phone 1 on battery / 2 charging; desktop cores-1 capped at 4, at
 * least 1. Missing or invalid cores fall back to 2 cores, exactly like
 * computeWorkerCount of contribute.js. GPU part: up to 2 lanes when the battery
 * guard allows them. The total is the CPU + GPU ceiling of the whole tab; since
 * step 8 the WorkerBudget of the integrated page is created with `total`, so one
 * budget covers every lane of the tab (owner T104 rules: the sum of the worker
 * lanes of everything the tab runs never exceeds the tab ceiling).
 *
 * @param {Object} [opts]
 * @param {number} [opts.hardwareConcurrency]
 * @param {boolean} [opts.isPhone]
 * @param {boolean} [opts.isCharging]
 * @param {boolean} [opts.hasBattery]
 * @param {number} [opts.level]
 * @param {number} [opts.gpuSlots]
 * @returns {{cpu: number, gpu: number, total: number}}
 */
export function computeTabBudget(opts = {}) {
  const cores = typeof opts.hardwareConcurrency === 'number' && opts.hardwareConcurrency > 0
    ? opts.hardwareConcurrency
    : 2;
  const cpu = opts.isPhone
    ? (opts.isCharging ? 2 : 1)
    : Math.max(1, Math.min(4, Math.floor(cores - 1)));
  const gpu = batteryAllowsGPU(opts)
    ? Math.max(0, Math.min(GPU_LANE_CAP, Math.floor(Number(opts.gpuSlots)) || 0))
    : 0;
  return { cpu, gpu, total: cpu + gpu };
}

/**
 * One worker budget for one tab.
 *
 * Owners are labels ("pools", "legacy:matrix", ...). claim() grants at most the
 * remaining slots (a partial grant is normal sharing, never an error), release()
 * never frees more than the owner holds, and the sum of all held claims is
 * invariantly <= total. resize() moves the ceiling; if it drops below the held
 * sum, remaining() reports 0 until the owners release - the class never revokes
 * a running lane on its own.
 */
export class WorkerBudget {
  /**
   * @param {Object} [options]
   * @param {number} options.total integer >= 1
   * @param {Function} [options.now] injectable clock (unused by the math, kept
   *   for future telemetry so tests can stay deterministic)
   */
  constructor(options = {}) {
    const total = Math.floor(Number(options.total));
    if (!Number.isFinite(total) || total < 1) {
      throw new TypeError('WorkerBudget: total must be an integer >= 1');
    }
    this.totalSlots = total;
    this.now = typeof options.now === 'function' ? options.now : () => Date.now();
    this.claims = new Map(); // owner -> held slots (zero entries may linger)
  }

  /** The ceiling of the tab. @returns {number} */
  total() { return this.totalSlots; }

  /** Slots held by one owner. @returns {number} */
  usedBy(owner) { return this.claims.get(owner) || 0; }

  /** Slots held by every owner. @returns {number} */
  used() {
    let sum = 0;
    for (const held of this.claims.values()) sum += held;
    return sum;
  }

  /** Free slots. Never negative. @returns {number} */
  remaining() { return Math.max(0, this.totalSlots - this.used()); }

  /** Owners that hold at least one slot. @returns {string[]} */
  owners() {
    const out = [];
    for (const [owner, held] of this.claims.entries()) {
      if (held > 0) out.push(owner);
    }
    return out;
  }

  _checkOwner(owner) {
    if (typeof owner !== 'string' || owner.length === 0) {
      throw new TypeError('WorkerBudget: owner must be a non-empty string');
    }
  }

  _checkCount(value, what) {
    const n = Math.floor(Number(value));
    if (!Number.isFinite(n) || n < 0) {
      throw new TypeError(`WorkerBudget: ${what} must be an integer >= 0`);
    }
    return n;
  }

  /**
   * Claim slots for one owner. Grants at most the remaining slots and returns
   * the granted count (0..want). A want of 0 always grants 0.
   *
   * @param {string} owner
   * @param {number} want
   * @returns {number} granted slots
   */
  claim(owner, want) {
    this._checkOwner(owner);
    const n = this._checkCount(want, 'claim count');
    if (n === 0) return 0;
    const granted = Math.min(n, this.remaining());
    this.claims.set(owner, this.usedBy(owner) + granted);
    return granted;
  }

  /**
   * Release slots held by one owner. Never frees more than the owner holds and
   * returns the amount actually freed.
   *
   * @param {string} owner
   * @param {number} want
   * @returns {number} released slots
   */
  release(owner, want) {
    this._checkOwner(owner);
    const n = this._checkCount(want, 'release count');
    const held = this.usedBy(owner);
    const freed = Math.min(n, held);
    if (freed > 0) this.claims.set(owner, held - freed);
    return freed;
  }

  /**
   * Release every slot of one owner. Returns the amount freed.
   *
   * @param {string} owner
   * @returns {number}
   */
  releaseAll(owner) {
    this._checkOwner(owner);
    const held = this.usedBy(owner);
    if (held > 0) this.claims.set(owner, 0);
    return held;
  }

  /**
   * Move the ceiling. The value must stay >= 1; a ceiling below the held sum is
   * allowed and shows up as remaining() == 0 (no forced revoke).
   *
   * @param {number} total
   * @returns {number} the new total
   */
  resize(total) {
    const t = Math.floor(Number(total));
    if (!Number.isFinite(t) || t < 1) {
      throw new TypeError('WorkerBudget: total must be an integer >= 1');
    }
    this.totalSlots = t;
    return this.totalSlots;
  }

  /**
   * Point-in-time view for the status/log area and for tests.
   *
   * @returns {{total: number, used: number, remaining: number, owners: Object<string, number>}}
   */
  snapshot() {
    const owners = {};
    for (const [owner, held] of this.claims.entries()) {
      if (held > 0) owners[owner] = held;
    }
    return { total: this.totalSlots, used: this.used(), remaining: this.remaining(), owners };
  }
}

// END OF worker_budget_T104.js
