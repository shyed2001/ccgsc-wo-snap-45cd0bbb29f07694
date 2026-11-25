import { request, getCSRFHeaders, poolSettingsId } from './api.js';

// F12 (G-F12-1): the Dew window is 1-30 minutes, default 10. This panel is the
// live control. schema.js keeps the row for the manual and skips it in the
// generic form so there is only one editor.
export function mountDewPanel(content, canAdmin, pageId, showError, showSuccess) {
  const pool = poolSettingsId(pageId);
  if (!pool || !canAdmin || !content?.ownerDocument || typeof content.appendChild !== 'function') return;
  const doc = content.ownerDocument;
  const panel = doc.createElement('section');
  panel.id = 'dew';
  panel.className = 'admin-panel';
  panel.innerHTML = [
    '<h3>Dew receipt window</h3>',
    '<p>How long a signed Dew receipt stays valid. 1–30 minutes, default 10. A developer may only shorten this for one approved app.</p>',
    '<label>Window (minutes) <input data-dew-minutes type="number" min="1" max="30" value="10"></label> ',
    '<button type="button" data-dew-save disabled>Save Dew window</button>',
    '<p data-dew-status role="status">Loading...</p>',
    '<h4>Per-app override</h4>',
    '<label>Developer key owner <input data-dew-owner></label> ',
    '<label>App ID <input data-dew-app></label> ',
    '<label>Minutes (0 clears) <input data-dew-app-minutes type="number" min="0" max="30" value="0"></label> ',
    '<button type="button" data-dew-app-load>Load override</button> ',
    '<button type="button" data-dew-app-save>Save override</button>',
    '<p data-dew-app-status role="status"></p>'
  ].join('');
  content.appendChild(panel);
  const get = (name) => panel.querySelector('[data-' + name + ']');
  const minutes = get('dew-minutes');
  const save = get('dew-save');
  const status = get('dew-status');
  const route = '/admin/v1/pools/' + pool + '/dew';
  let version = 0;
  const render = (v) => {
    minutes.value = v.windowMinutes;
    version = Number(v.version) || 0;
    status.textContent = 'Active: ' + v.windowMinutes + ' min (' + v.windowSeconds + ' s), version ' + version + '.';
    save.disabled = false;
  };
  request(route).then(render).catch((err) => {
    status.textContent = 'Dew window unavailable: ' + (err && err.message ? err.message : err);
  });
  save.addEventListener('click', async () => {
    save.disabled = true;
    try {
      const saved = await request(route, {
        method: 'PUT',
        headers: getCSRFHeaders(),
        body: JSON.stringify({ windowMinutes: Number(minutes.value), expectedVersion: version })
      });
      render(saved);
      showSuccess?.('Dew window saved.');
    } catch (err) {
      status.textContent = 'Save failed: ' + (err && err.message ? err.message : err);
      showError?.(err);
      save.disabled = false;
    }
  });
  const appStatus = get('dew-app-status');
  let appVersion = 0;
  const appRoute = () => '/admin/v1/pools/' + pool + '/apps/' + encodeURIComponent(get('dew-app').value.trim()) + '/dew';
  get('dew-app-load').addEventListener('click', async () => {
    try {
      const v = await request(appRoute() + '?owner=' + encodeURIComponent(get('dew-owner').value.trim()));
      get('dew-app-minutes').value = v.windowMinutes;
      appVersion = Number(v.version) || 0;
      appStatus.textContent = 'Override loaded: ' + v.windowMinutes + ' min (admin max ' + v.adminMaxMinutes + ' min), version ' + appVersion + '.';
    } catch (err) {
      appStatus.textContent = 'Load failed: ' + (err && err.message ? err.message : err);
    }
  });
  get('dew-app-save').addEventListener('click', async () => {
    try {
      const saved = await request(appRoute(), {
        method: 'PUT',
        headers: getCSRFHeaders(),
        body: JSON.stringify({
          owner: get('dew-owner').value.trim(),
          windowMinutes: Number(get('dew-app-minutes').value),
          expectedVersion: appVersion
        })
      });
      appVersion = Number(saved.version) || 0;
      appStatus.textContent = 'Override saved: ' + saved.windowMinutes + ' min, version ' + appVersion + '.';
      showSuccess?.('Dew app override saved.');
    } catch (err) {
      appStatus.textContent = 'Save failed: ' + (err && err.message ? err.message : err);
      showError?.(err);
    }
  });
}
