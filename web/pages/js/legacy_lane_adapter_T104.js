// Path: web/pages/js/legacy_lane_adapter_T104.js | Author: GLM (Z.ai) for Claude Opus 5.5 head | 2026-11-21
// What/why: T104 step 5 - the LegacyLaneAdapter (Gemini W7 target, step 3 of the
// applied plan). The old Node lane router (startWorkerLanes / worker_lane_multi.mjs,
// prime repo) keeps its own WebSocket transport, lane modules and lifecycle. The
// adapter gives it exactly the two things the integrated page shares:
//   1. slots of the one WorkerBudget, claimed under owner "legacy:<lane>",
//   2. lines and a status chip in the one status/log area.
// Nothing here touches the transport of the old lanes: a lane that never calls the
// adapter behaves exactly as before, and an adapter without a budget degrades to
// unconstrained with one honest line instead of failing the lane.
//
// Truth: docs/T104_INTEGRATED_PAGE_PLAN.md, owner T104 rules (issue #117),
// GRILL_WWPP_R1.md (R29.4, R30.2).

'use strict';

// Owner prefix of every legacy lane inside the shared budget.
export const LEGACY_OWNER_PREFIX = 'legacy:';

/**
 * Create one legacy lane adapter.
 *
 * @param {Object} [options]
 * @param {string} [options.laneMode] lane label, default "generic"; also the
 *   budget owner suffix and the status/log source ("legacy:<laneMode>")
 * @param {Object} [options.budget] a WorkerBudget from worker_budget_T104.js;
 *   without one the lane runs unconstrained and says so once
 * @param {Object} [options.statusLog] a status/log area from status_log_T104.js
 * @returns {Object} the adapter
 */
export function createLegacyLaneAdapter(options = {}) {
  const lane = String(options.laneMode || 'generic') || 'generic';
  const owner = LEGACY_OWNER_PREFIX + lane;
  const budget = options.budget || null;
  const statusLog = options.statusLog || null;
  const inFlight = new Map(); // tag -> true
  let warnedNoBudget = false;
  // T104 layout rule: only the visible area runs heavy work. The page flips this
  // gate when the visitor hides the legacy area; the lane router of the prime
  // repo reads it (plan step 9) before starting or continuing a lane. Without a
  // consumer the gate only holds the adapter's own slots.
  let enabled = options.enabled !== false;

  return {
    /** Budget owner of this lane, e.g. "legacy:matrix". */
    owner,
    /** The lane label the adapter was created with. */
    laneMode: lane,

    /**
     * Whether this lane may run heavy work. The page turns it off when the
     * legacy area is hidden (tabs layout, pools layout) and on again when it is
     * shown. The router polls it before starting a task or claiming slots (see
     * setEnabled); a router that does not read the gate behaves as before.
     *
     * @returns {boolean}
     */
    isEnabled() { return enabled; },

    /**
     * Open or close the heavy-work gate of this lane.
     *
     * Contract when closing:
     *   - the adapter releases every slot the lane holds at once (grant release),
     *     so the shared budget already reflects the hidden area;
     *   - it does NOT cancel or pause work that is already running: the adapter
     *     has no handle on the router's tasks, so those finish (or fail) under
     *     their own timeout;
     *   - the router must therefore POLL the gate - `isEnabled()` here or
     *     `ccgscLegacyLaneEnabled()` on the page - before claiming slots and
     *     between tasks, and stop claiming while it answers false. A router that
     *     never reads the gate keeps its pre-T104 behaviour; only the slots are
     *     given back.
     *
     * @param {boolean} value
     * @returns {boolean} the gate after the call
     */
    setEnabled(value) {
      const next = value !== false;
      if (next === enabled) return enabled;
      enabled = next;
      if (!enabled) budget && budget.releaseAll(owner);
      if (statusLog) statusLog.log(owner, enabled ? 'lane enabled' : 'lane paused (area hidden)');
      return enabled;
    },

    /**
     * Claim CPU worker slots for this lane. Returns the granted count, which may
     * be lower than the ask when the new pools or another lane hold slots. A
     * partial grant is shared budgeting, not an error; the adapter says so once
     * per ask through the status/log area.
     *
     * @param {number} want
     * @returns {number} granted slots (== want when no budget is attached)
     */
    claimSlots(want) {
      const n = Math.max(0, Math.floor(Number(want)) || 0);
      // R10-FIX (minimax MINOR): a hidden area runs no heavy work, so a closed
      // gate refuses the claim instead of taking back the slots setEnabled() just
      // released. Without this the hidden lane competed with the visible one.
      if (!enabled) return 0;
      if (!budget) {
        if (!warnedNoBudget) {
          warnedNoBudget = true;
          if (statusLog) statusLog.log(owner, 'no shared worker budget attached; lane runs unconstrained');
        }
        return n;
      }
      const granted = budget.claim(owner, n);
      if (granted < n && statusLog) {
        statusLog.log(owner, `worker budget: granted ${granted} of ${n} slots`);
      }
      return granted;
    },

    /**
     * Release slots. Never frees more than this lane holds.
     *
     * @param {number} want
     * @returns {number} released slots (== want when no budget is attached)
     */
    releaseSlots(want) {
      const n = Math.max(0, Math.floor(Number(want)) || 0);
      if (!budget) return n;
      return budget.release(owner, n);
    },

    /** Release every slot this lane holds. @returns {number} freed slots */
    releaseAllSlots() {
      if (!budget) return 0;
      return budget.releaseAll(owner);
    },

    /** @returns {number} slots this lane currently holds */
    heldSlots() {
      if (!budget) return 0;
      return budget.usedBy(owner);
    },

    /**
     * Mark one lane task as in flight (tag is any key, usually a task id).
     *
     * @param {string|number} tag
     * @returns {number} in-flight count after the call
     */
    beginTask(tag) {
      if (tag !== null && tag !== undefined && tag !== '') inFlight.set(String(tag), true);
      return inFlight.size;
    },

    /**
     * Mark one lane task as finished. Unknown tags are ignored.
     *
     * @param {string|number} tag
     * @returns {number} in-flight count after the call
     */
    endTask(tag) {
      if (tag !== null && tag !== undefined && tag !== '') inFlight.delete(String(tag));
      return inFlight.size;
    },

    /** @returns {number} lane tasks currently in flight */
    activeTasks() {
      return inFlight.size;
    },

    /** @returns {string[]} tags currently in flight (for diagnostics) */
    activeTags() {
      return Array.from(inFlight.keys());
    },

    /**
     * Write one line into the shared status/log area under "legacy:<lane>".
     *
     * @param {string} message
     */
    log(message) {
      if (statusLog) statusLog.log(owner, String(message == null ? '' : message));
    },

    /**
     * Set the status chip of this lane ("legacy:<lane>=<text>").
     *
     * @param {string} text
     */
    setLaneStatus(text) {
      if (statusLog) statusLog.setStatus(owner, String(text == null ? '' : text));
    },

    /**
     * Expose the adapter on a global target (window / globalThis). The safe
     * refresh handler of the root page reads ccgscLegacyLaneActiveTasks; the
     * lane router of the prime repo reads ccgscLegacyLaneAdapter (T104 plan
     * step 9).
     *
     * @param {Object} target
     * @returns {Object} the adapter (chainable)
     */
    exposeOn(target) {
      if (!target || typeof target !== 'object') return this;
      target.ccgscLegacyLaneAdapter = this;
      target.ccgscLegacyLaneActiveTasks = () => this.activeTasks();
      // T104 layout rule: the lane router reads the gate before it starts or
      // continues heavy work (plan step 9); a router that never reads it keeps
      // its old behaviour.
      target.ccgscLegacyLaneEnabled = () => this.isEnabled();
      return this;
    }
  };
}

// END OF legacy_lane_adapter_T104.js
