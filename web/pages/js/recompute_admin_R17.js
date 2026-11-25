// R17 (G-R16-4): per-pool Director recompute mode, separate from sample rate.
// R18 (G-R17-1/2): per-pool recompute policies (busy and malformed) ride the
// same durable record. The backend is the authority; never show a guessed
// saved mode as confirmed.
import { request, getCSRFHeaders, markLive, poolSettingsId } from './api.js';

export const RECOMPUTE_MODES = Object.freeze(['registry', 'off']);
export const RECOMPUTE_BUSY_POLICIES = Object.freeze(['skip', 'defer']);
export const RECOMPUTE_MALFORMED_POLICIES = Object.freeze(['reject', 'fraud']);

export function recomputePath(pageOrPool) {
  const id = poolSettingsId(pageOrPool);
  if (!id) throw new Error('Unknown pool: no recompute endpoint for this page.');
  return `/admin/v1/pools/${id}/recompute`;
}

export async function readRecomputeMode(pageOrPool) {
  const reply = await request(recomputePath(pageOrPool));
  markLive('pools-core served the recompute mode');
  return reply;
}

export async function writeRecomputeMode(pageOrPool, mode, expectedVersion) {
  if (!RECOMPUTE_MODES.includes(mode)) throw new Error('Invalid recompute mode.');
  if (!Number.isSafeInteger(expectedVersion) || expectedVersion < 0) {
    throw new Error('Recompute version is unavailable; reload before saving.');
  }
  const saved = await request(recomputePath(pageOrPool), {
    method: 'PUT',
    headers: getCSRFHeaders(),
    body: JSON.stringify({ mode, expectedVersion })
  });
  markLive('pools-core saved the recompute mode');
  return saved;
}

export async function writeRecomputePolicies(pageOrPool, busyPolicy, malformedPolicy, expectedVersion) {
  if (!RECOMPUTE_BUSY_POLICIES.includes(busyPolicy)) throw new Error('Invalid recompute busy policy.');
  if (!RECOMPUTE_MALFORMED_POLICIES.includes(malformedPolicy)) throw new Error('Invalid recompute malformed policy.');
  if (!Number.isSafeInteger(expectedVersion) || expectedVersion < 0) {
    throw new Error('Recompute version is unavailable; reload before saving.');
  }
  const saved = await request(recomputePath(pageOrPool), {
    method: 'PUT',
    headers: getCSRFHeaders(),
    body: JSON.stringify({ busyPolicy, malformedPolicy, expectedVersion })
  });
  markLive('pools-core saved the recompute policies');
  return saved;
}

export function renderRecomputePanel(content, canAdmin, current = {}) {
  if (!content || typeof content.insertAdjacentHTML !== 'function') return null;
  const mode = RECOMPUTE_MODES.includes(current.mode) ? current.mode : null;
  const busy = RECOMPUTE_BUSY_POLICIES.includes(current.busyPolicy) ? current.busyPolicy : 'skip';
  const malformed = RECOMPUTE_MALFORMED_POLICIES.includes(current.malformedPolicy) ? current.malformedPolicy : 'reject';
  content.insertAdjacentHTML('beforeend', `
    <section class="config-panel" id="recompute-panel">
      <h2>Reference recompute mode (R17)</h2>
      <div class="form-group">
        <label class="form-label" for="recompute-mode">Per-pool sampled reference verification</label>
        <select id="recompute-mode">
          <option value="" disabled${mode === null ? ' selected' : ''}>Unknown — awaiting server configuration</option>
          <option value="registry"${mode === 'registry' ? ' selected' : ''}>registry - use registered reference verifiers</option>
          <option value="off"${mode === 'off' ? ' selected' : ''}>off - 2-of-N redundancy and canaries only (default)</option>
        </select>
        <p class="field-help">The sample rate is configured separately in Pool settings.
          registry recomputes only eligible sampled jobs with known module SHA-256.
          off disables reference recomputation but retains 2-of-N redundancy and canaries.</p>
        <p class="field-help" id="recompute-status" role="status">Loading server recompute mode...</p>
        <button type="button" id="recompute-confirm" hidden disabled>Confirm off mode for this pool</button>
      </div>
      <div class="form-group">
        <label class="form-label" for="recompute-busy-policy">Verifier-busy policy (R18)</label>
        <select id="recompute-busy-policy">
          <option value="skip"${busy === 'skip' ? ' selected' : ''}>skip - settle, accuse no one (default)</option>
          <option value="defer"${busy === 'defer' ? ' selected' : ''}>defer - hold the result, retry the recompute when a slot frees</option>
        </select>
        <p class="field-help">When the sampled recompute cannot run (verifier busy or a stale
          prepared verdict), skip settles the submission untouched; defer keeps the task
          pending and retries with bounded retries, then falls back to skip and logs.</p>
      </div>
      <div class="form-group">
        <label class="form-label" for="recompute-malformed-policy">Malformed-digest policy (R18)</label>
        <select id="recompute-malformed-policy">
          <option value="reject"${malformed === 'reject' ? ' selected' : ''}>reject - refuse the submission as invalid and re-lease (default)</option>
          <option value="fraud"${malformed === 'fraud' ? ' selected' : ''}>fraud - treat as a recompute mismatch with a fraud mark (pre-R18)</option>
        </select>
        <p class="field-help">A sampled submission with a malformed claimed digest is rejected
          by default without a fraud mark; the fraud option keeps the pre-R18 accusation.</p>
      </div>
    </section>
  `);
  const select = typeof content.querySelector === 'function' ? content.querySelector('#recompute-mode') : null;
  const status = typeof content.querySelector === 'function' ? content.querySelector('#recompute-status') : null;
  const busySelect = typeof content.querySelector === 'function' ? content.querySelector('#recompute-busy-policy') : null;
  const malformedSelect = typeof content.querySelector === 'function' ? content.querySelector('#recompute-malformed-policy') : null;
  if (select) {
    select.value = mode ?? '';
    select.disabled = !canAdmin || mode === null;
  }
  if (busySelect) {
    busySelect.value = busy;
    busySelect.disabled = !canAdmin || mode === null;
  }
  if (malformedSelect) {
    malformedSelect.value = malformed;
    malformedSelect.disabled = !canAdmin || mode === null;
  }
  return { mode, select, status, busySelect, malformedSelect };
}

// Load the durable mode/version before allowing writes. A failed read or
// rejected write stays visible; the Director never gets a false Saved message.
export function mountRecomputePanel(content, canAdmin, pageId, showError, showSuccess) {
  if (!poolSettingsId(pageId)) return null;
  const panel = renderRecomputePanel(content, canAdmin);
  if (!panel || !panel.select) return panel;
  const { select, status, busySelect, malformedSelect } = panel;
  const confirm = typeof content.querySelector === 'function' ? content.querySelector('#recompute-confirm') : null;
  if (confirm) { confirm.hidden = true; confirm.disabled = true; }
  select.disabled = true;
  if (busySelect) busySelect.disabled = true;
  if (malformedSelect) malformedSelect.disabled = true;
  if (!canAdmin) {
    if (status) status.textContent = 'Admin role required to read or edit the saved mode.';
    return panel;
  }
  let actualMode = null, actualBusy = 'skip', actualMalformed = 'reject', version = null, ready = false;
  readRecomputeMode(pageId).then(reply => {
    if (!RECOMPUTE_MODES.includes(reply?.mode) ||
        !Number.isSafeInteger(reply?.version) || reply.version < 0) {
      throw new Error('Invalid recompute settings returned by the server.');
    }
    actualMode = reply.mode;
    actualBusy = RECOMPUTE_BUSY_POLICIES.includes(reply?.busyPolicy) ? reply.busyPolicy : 'skip';
    actualMalformed = RECOMPUTE_MALFORMED_POLICIES.includes(reply?.malformedPolicy) ? reply.malformedPolicy : 'reject';
    version = reply.version;
    ready = true;
    select.value = actualMode;
    select.disabled = false;
    if (busySelect) { busySelect.value = actualBusy; busySelect.disabled = false; }
    if (malformedSelect) { malformedSelect.value = actualMalformed; malformedSelect.disabled = false; }
    if (confirm) {
      confirm.hidden = !(reply.upgradeReviewRequired && actualMode === 'off');
      confirm.disabled = confirm.hidden;
    }
    if (status) {
      const upgradeWarning = reply.upgradeReviewRequired
        ? ' Upgrade review required: previously configured sampling is now disabled by default; review the mode and explicitly save registry or off.'
        : '';
      status.textContent = `Active: ${actualMode} (version ${version}; ${reply.source || 'server'}).${upgradeWarning}`;
    }
  }).catch(err => {
    select.disabled = true;
    if (busySelect) busySelect.disabled = true;
    if (malformedSelect) malformedSelect.disabled = true;
    // An optional background read must not overwrite an unrelated panel's
    // success/error banner. Keep the failure visible in this panel only.
    if (status) status.textContent = `Recompute settings unavailable: ${err?.message || err}.`;
  });

  if (typeof select.addEventListener === 'function') {
    select.addEventListener('change', async () => {
      const next = select.value;
      if (!ready || !RECOMPUTE_MODES.includes(next)) {
        select.value = actualMode;
        return;
      }
      select.disabled = true;
      try {
        const saved = await writeRecomputeMode(pageId, next, version);
        if (!RECOMPUTE_MODES.includes(saved?.mode) || !Number.isSafeInteger(saved?.version)) {
          throw new Error('Invalid recompute save response.');
        }
        actualMode = saved.mode;
        version = saved.version;
        select.value = actualMode;
        if (status) status.textContent = `Saved: ${actualMode} (version ${version}).`;
        if (confirm) { confirm.hidden = true; confirm.disabled = true; }
        if (showSuccess) showSuccess(`Reference recompute: ${actualMode}.`);
      } catch (err) {
        select.value = actualMode;
        if (status) status.textContent = `Save failed; still ${actualMode}: ${err?.message || err}.`;
        if (showError) showError(err);
      } finally {
        select.disabled = !canAdmin;
      }
    });
  }
  // R18 (G-R17-1/2): one change handler for each policy select. Both values
  // travel together so the write stays a single versioned record.
  const wirePolicySelect = (policySelect, isBusy) => {
    if (!policySelect || typeof policySelect.addEventListener !== 'function') return;
    policySelect.addEventListener('change', async () => {
      const next = policySelect.value;
      const allowed = isBusy ? RECOMPUTE_BUSY_POLICIES : RECOMPUTE_MALFORMED_POLICIES;
      const currentValue = isBusy ? actualBusy : actualMalformed;
      if (!ready || !allowed.includes(next)) {
        policySelect.value = currentValue;
        return;
      }
      const busy = isBusy ? next : (busySelect ? busySelect.value : actualBusy);
      const malformed = isBusy ? (malformedSelect ? malformedSelect.value : actualMalformed) : next;
      policySelect.disabled = true;
      try {
        const saved = await writeRecomputePolicies(pageId, busy, malformed, version);
        if (!RECOMPUTE_BUSY_POLICIES.includes(saved?.busyPolicy) ||
            !RECOMPUTE_MALFORMED_POLICIES.includes(saved?.malformedPolicy) ||
            !Number.isSafeInteger(saved?.version)) {
          throw new Error('Invalid recompute policy save response.');
        }
        actualBusy = saved.busyPolicy;
        actualMalformed = saved.malformedPolicy;
        version = saved.version;
        if (busySelect) busySelect.value = actualBusy;
        if (malformedSelect) malformedSelect.value = actualMalformed;
        if (status) status.textContent = `Saved: ${actualMode} (version ${version}).`;
        if (showSuccess) showSuccess(`Recompute policy: busy=${actualBusy}, malformed=${actualMalformed}.`);
      } catch (err) {
        policySelect.value = currentValue;
        if (status) status.textContent = `Save failed; still ${actualMode}: ${err?.message || err}.`;
        if (showError) showError(err);
      } finally {
        policySelect.disabled = !canAdmin;
      }
    });
  };
  wirePolicySelect(busySelect, true);
  wirePolicySelect(malformedSelect, false);
  if (confirm && typeof confirm.addEventListener === 'function') {
    confirm.addEventListener('click', async () => {
      if (!ready || actualMode !== 'off' || confirm.hidden || confirm.disabled) return;
      confirm.disabled = true;
      try {
        const saved = await writeRecomputeMode(pageId, 'off', version);
        if (saved?.mode !== 'off' || !Number.isSafeInteger(saved.version) || saved.version <= version) {
          throw new Error('Invalid recompute confirmation response.');
        }
        version = saved.version;
        confirm.hidden = true;
        if (status) status.textContent = `Saved: off (version ${version}).`;
        if (showSuccess) showSuccess('Reference recompute: off confirmed.');
      } catch (err) {
        if (status) status.textContent = `Off confirmation failed; review still required: ${err?.message || err}.`;
        if (showError) showError(err);
        confirm.disabled = false;
      }
    });
  }
  return panel;
}
