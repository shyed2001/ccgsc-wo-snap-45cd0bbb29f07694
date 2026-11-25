// Path: web/pages/tests/worker_budget_T104.test.mjs | Author: GLM (Z.ai) for Claude Opus 5.5 head | 2026-11-21
// What/why: T104 step 1 - the shared worker budget of the integrated page.
// Pure rules (tab budget, battery guard) and the claim/release invariant of
// WorkerBudget, with no DOM and no network. Counts are integers; fractional
// values are floored, negative and non-finite values throw.

import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { batteryAllowsGPU, computeTabBudget, WorkerBudget, GPU_LANE_CAP, POOLS_BUDGET_OWNER, POOLS_GPU_BUDGET_OWNER } from '../js/worker_budget_T104.js';
import { LEGACY_OWNER_PREFIX } from '../js/legacy_lane_adapter_T104.js';

describe('T104 worker budget', () => {

  describe('batteryAllowsGPU (owner rule: no GPU under 20 % battery)', () => {
    it('charging always allows GPU', () => {
      assert.equal(batteryAllowsGPU({ isCharging: true, hasBattery: true, level: 0.05 }), true);
      assert.equal(batteryAllowsGPU({ isCharging: true, hasBattery: false, isPhone: true }), true);
    });

    it('blocks below 20 % and allows from 20 % up', () => {
      assert.equal(batteryAllowsGPU({ hasBattery: true, level: 0.19 }), false);
      assert.equal(batteryAllowsGPU({ hasBattery: true, level: 0.2 }), true);
      assert.equal(batteryAllowsGPU({ hasBattery: true, level: 1 }), true);
    });

    it('a battery without a readable level stays conservative', () => {
      assert.equal(batteryAllowsGPU({ hasBattery: true }), false);
    });

    it('no battery signal: desktop allowed, phone blocked', () => {
      assert.equal(batteryAllowsGPU({}), true);
      assert.equal(batteryAllowsGPU({ isPhone: true }), false);
    });
  });

  describe('computeTabBudget (auto default mirrors computeWorkerCount)', () => {
    it('desktop follows the cores-1 rule capped at 4', () => {
      assert.equal(computeTabBudget({ hardwareConcurrency: 8 }).cpu, 4);
      assert.equal(computeTabBudget({ hardwareConcurrency: 5 }).cpu, 4);
      assert.equal(computeTabBudget({ hardwareConcurrency: 3 }).cpu, 2);
      assert.equal(computeTabBudget({ hardwareConcurrency: 2 }).cpu, 1);
    });

    it('invalid cores fall back like computeWorkerCount (treated as 2)', () => {
      assert.equal(computeTabBudget({}).cpu, 1);
      assert.equal(computeTabBudget({ hardwareConcurrency: 0 }).cpu, 1);
      assert.equal(computeTabBudget({ hardwareConcurrency: -3 }).cpu, 1);
      assert.equal(computeTabBudget({ hardwareConcurrency: Number.NaN }).cpu, 1);
    });

    it('phone: 1 on battery, 2 charging', () => {
      assert.equal(computeTabBudget({ isPhone: true }).cpu, 1);
      assert.equal(computeTabBudget({ isPhone: true, isCharging: true }).cpu, 2);
    });

    it('gpu slots count only when the battery guard allows them, capped at 2', () => {
      assert.deepEqual(computeTabBudget({ hardwareConcurrency: 8, gpuSlots: 2 }), { cpu: 4, gpu: 2, total: 6 });
      assert.deepEqual(computeTabBudget({ hardwareConcurrency: 8, gpuSlots: 5 }), { cpu: 4, gpu: 2, total: 6 });
      assert.deepEqual(computeTabBudget({ hardwareConcurrency: 8, gpuSlots: 2, hasBattery: true, level: 0.1 }),
        { cpu: 4, gpu: 0, total: 4 });
      assert.deepEqual(computeTabBudget({ hardwareConcurrency: 8, gpuSlots: 2, hasBattery: true, level: 0.1, isCharging: true }),
        { cpu: 4, gpu: 2, total: 6 });
      assert.deepEqual(computeTabBudget({ hardwareConcurrency: 8 }), { cpu: 4, gpu: 0, total: 4 });
    });
  });

  describe('WorkerBudget (one tab, one budget)', () => {
    it('claims grant up to the remaining slots, a partial grant is normal', () => {
      const budget = new WorkerBudget({ total: 4 });
      assert.equal(budget.claim('pools', 3), 3);
      assert.equal(budget.remaining(), 1);
      assert.equal(budget.claim('legacy:matrix', 2), 1, 'the second lane gets what is left');
      assert.equal(budget.remaining(), 0);
      assert.equal(budget.claim('legacy:prime', 1), 0);
    });

    it('a want of 0 grants 0 and keeps the owner unlisted', () => {
      const budget = new WorkerBudget({ total: 2 });
      assert.equal(budget.claim('pools', 0), 0);
      assert.deepEqual(budget.owners(), []);
    });

    it('release never frees more than the owner holds', () => {
      const budget = new WorkerBudget({ total: 4 });
      budget.claim('pools', 2);
      assert.equal(budget.release('pools', 5), 2);
      assert.equal(budget.used(), 0);
      assert.equal(budget.release('pools', 1), 0);
    });

    it('releaseAll frees exactly the claim of one owner', () => {
      const budget = new WorkerBudget({ total: 4 });
      budget.claim('pools', 2);
      budget.claim('legacy:matrix', 1);
      assert.equal(budget.releaseAll('pools'), 2);
      assert.equal(budget.usedBy('pools'), 0);
      assert.equal(budget.usedBy('legacy:matrix'), 1);
      assert.equal(budget.releaseAll('unknown'), 0);
    });

    it('the sum of all claims never exceeds the total (randomised invariant)', () => {
      const budget = new WorkerBudget({ total: 6 });
      const owners = ['pools', 'legacy:matrix', 'legacy:prime', 'legacy:p2p'];
      let seed = 42;
      const rand = () => {
        seed = (seed * 1103515245 + 12345) % 2147483648;
        return seed / 2147483648;
      };
      for (let i = 0; i < 500; i++) {
        const owner = owners[Math.floor(rand() * owners.length)];
        if (rand() < 0.5) budget.claim(owner, Math.floor(rand() * 5));
        else budget.release(owner, Math.floor(rand() * 5));
        assert.ok(budget.used() <= budget.total(), `iteration ${i}: ${budget.used()} > ${budget.total()}`);
        assert.equal(budget.remaining(), Math.max(0, budget.total() - budget.used()));
      }
    });

    it('resize moves the ceiling without revoking held claims', () => {
      const budget = new WorkerBudget({ total: 2 });
      budget.claim('pools', 2);
      assert.equal(budget.resize(5), 5);
      assert.equal(budget.used(), 2);
      assert.equal(budget.remaining(), 3);
      assert.equal(budget.resize(1), 1);
      assert.equal(budget.remaining(), 0, 'over-commit shows as zero remaining');
    });

    it('snapshot reports the per-owner view and drops zero holders', () => {
      const budget = new WorkerBudget({ total: 4 });
      budget.claim('pools', 2);
      budget.claim('legacy:matrix', 1);
      budget.claim('legacy:prime', 0);
      assert.deepEqual(budget.snapshot(), {
        total: 4,
        used: 3,
        remaining: 1,
        owners: { pools: 2, 'legacy:matrix': 1 }
      });
    });

    it('fractional counts are floored, negative and junk throw', () => {
      const budget = new WorkerBudget({ total: 4 });
      assert.equal(budget.claim('pools', 1.9), 1);
      assert.equal(budget.release('pools', 0.5), 0);
      assert.throws(() => budget.claim('pools', -1), TypeError);
      assert.throws(() => budget.claim('pools', Number.NaN), TypeError);
      assert.throws(() => budget.release('pools', -2), TypeError);
      assert.throws(() => budget.claim('', 1), TypeError);
      assert.throws(() => budget.claim(null, 1), TypeError);
      assert.throws(() => budget.releaseAll(''), TypeError);
    });

    it('rejects an invalid ceiling', () => {
      assert.throws(() => new WorkerBudget({ total: 0 }), TypeError);
      assert.throws(() => new WorkerBudget({}), TypeError);
      const budget = new WorkerBudget({ total: 2 });
      assert.throws(() => budget.resize(0), TypeError);
    });

    it('accepts an injectable clock for future telemetry', () => {
      let tick = 0;
      const budget = new WorkerBudget({ total: 1, now: () => ++tick });
      assert.equal(budget.now(), 1);
      assert.equal(budget.now(), 2);
    });
  });

  describe('GPU lanes in the one budget (step 8)', () => {
    it('the owners of the integrated page are named and distinct', () => {
      assert.equal(POOLS_BUDGET_OWNER, 'pools');
      assert.equal(POOLS_GPU_BUDGET_OWNER, 'pools:gpu');
      assert.equal(GPU_LANE_CAP, 2);
      assert.equal(LEGACY_OWNER_PREFIX, 'legacy:');
    });

    it('the total of computeTabBudget is the ceiling of the whole tab', () => {
      const { cpu, gpu, total } = computeTabBudget({ hardwareConcurrency: 8, gpuSlots: 2 });
      assert.equal(cpu, 4);
      assert.equal(gpu, 2);
      assert.equal(total, 6);
      const budget = new WorkerBudget({ total });
      assert.equal(budget.claim(POOLS_BUDGET_OWNER, cpu), cpu);
      assert.equal(budget.claim(POOLS_GPU_BUDGET_OWNER, gpu), gpu);
      assert.equal(budget.remaining(), 0, 'CPU + GPU fills the tab exactly');
      assert.equal(budget.claim('legacy:matrix', 1), 0, 'no lane of the tab exceeds the ceiling');
    });

    it('a GPU lane never gets the CPU lanes ceiling on top of its own', () => {
      const budget = new WorkerBudget({ total: computeTabBudget({ hardwareConcurrency: 8, gpuSlots: 2 }).total });
      budget.claim(POOLS_BUDGET_OWNER, 4);
      assert.equal(budget.claim(POOLS_GPU_BUDGET_OWNER, 3), 2, 'the GPU cap of 2 is the ask, the ceiling the bound');
      assert.equal(budget.used(), 6);
    });

    it('the GPU part follows the 20 % battery rule of the same rule', () => {
      assert.equal(computeTabBudget({ hardwareConcurrency: 8, gpuSlots: 2, hasBattery: true, level: 0.19 }).total, 4);
      assert.equal(computeTabBudget({ hardwareConcurrency: 8, gpuSlots: 2, hasBattery: true, level: 0.2 }).total, 6);
      assert.equal(computeTabBudget({ hardwareConcurrency: 8, gpuSlots: 2, isPhone: true }).total, 1, 'phone on battery: no GPU');
      assert.equal(computeTabBudget({ hardwareConcurrency: 8, gpuSlots: 2, isPhone: true, isCharging: true }).total, 4);
    });

    it('a growing tab keeps the sum invariant across a resize', () => {
      const budget = new WorkerBudget({ total: 4 });
      budget.claim(POOLS_BUDGET_OWNER, 4);
      budget.resize(6);
      assert.equal(budget.claim(POOLS_GPU_BUDGET_OWNER, 2), 2);
      assert.equal(budget.used(), 6);
      assert.equal(budget.remaining(), 0);
      assert.equal(budget.resize(2), 2);
      assert.equal(budget.remaining(), 0, 'an over-committed tab reports no free slot');
      assert.equal(budget.used(), 6, 'a resize never revokes a running lane');
    });
  });

});

