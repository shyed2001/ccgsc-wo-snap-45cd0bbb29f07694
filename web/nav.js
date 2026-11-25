// WO-HC-NAV-01 nav.js - Shared Accessible Navigation Component (< 150 lines)
// Author: hermes MiniMax-M3 for Claude Opus 5.5 | 2026-11-17 (WO-T2.2: canonical copy at web/nav.js)
// Original author: Claude Opus 5.5 head / agy Gemini 3.8 Flash high. Date: 2026-11-17.
// Truth: ctx_rows_R37_R40.md (R38.2, R38.3, R38.5) + design doc sec 3/4/14. Zero cookies/storage.
// Served at /nav/nav.js (and /web/nav.js for old links); nav.json resolves next to this module.

let _cachedNav = null;

// Read login state hook from window.CCGSC_NAV_ROLES (array) else []
export function getNavRoles() {
  return (typeof window !== 'undefined' && Array.isArray(window.CCGSC_NAV_ROLES))
    ? window.CCGSC_NAV_ROLES
    : [];
}

// Filter links: public always visible + role links matching caller roles; unknown roles ignored
export function filterNavLinks(links, roles = []) {
  if (!Array.isArray(links)) return [];
  const activeRoles = new Set(Array.isArray(roles) ? roles : []);
  return links.filter(item => item && (item.visibility === 'public' || activeRoles.has(item.visibility)));
}

// Resolve nav.json next to this module. document.currentScript is null inside an
// ES module, so import.meta.url is the reliable anchor.
export function resolveNavUrl(url) {
  if (url) return url;
  try {
    if (import.meta && import.meta.url) return new URL('nav.json', import.meta.url).href;
  } catch (_) { /* not a module context */ }
  const src = (typeof document !== 'undefined' && document.currentScript) ? document.currentScript.src : '';
  return src ? new URL('nav.json', src).href : '/nav/nav.json';
}

// Fetch nav.json with in-memory caching across invocations; resolves relative to script host
export async function fetchNav(url) {
  if (_cachedNav) return _cachedNav;
  const targetUrl = resolveNavUrl(url);
  const res = await fetch(targetUrl);
  if (!res.ok) throw new Error(`HTTP ${res.status} loading ${targetUrl}`);
  const data = await res.json();
  _cachedNav = Array.isArray(data) ? data : (data.links || []);
  return _cachedNav;
}

// Clear or update cache (useful for testing or dynamic link injection)
export function setNavCache(links) {
  _cachedNav = links;
}

// Render accessible navigation bar into container
export function renderNav(container, links, options = {}) {
  const target = (typeof container === 'string' && typeof document !== 'undefined')
    ? document.querySelector(container)
    : container;
  if (!target || !Array.isArray(links)) return null;

  const roles = options.roles ?? getNavRoles();
  const visible = filterNavLinks(links, roles);
  const curHref = options.currentUrl ?? (typeof window !== 'undefined' ? window.location.href : '');
  const curHost = options.currentHost ?? (typeof window !== 'undefined' ? window.location.hostname : '');

  const doc = target.ownerDocument || (typeof document !== 'undefined' ? document : null);
  if (!doc) return null;

  const nav = doc.createElement('nav');
  nav.className = 'ccgsc-nav';
  nav.setAttribute('aria-label', options.ariaLabel || 'CCGSC Network Navigation');

  const list = doc.createElement('ul');
  list.className = 'ccgsc-nav-list';

  for (const item of visible) {
    const li = doc.createElement('li');
    li.className = 'ccgsc-nav-item';

    const a = doc.createElement('a');
    a.className = 'ccgsc-nav-link';
    a.href = item.href;
    a.textContent = item.label;
    if (item.legend) {
      a.title = item.legend;
      a.setAttribute('aria-description', item.legend);
    }

    // Current page and host highlighting
    const isPage = curHref && (a.href === curHref || (curHref.startsWith(item.href) && item.href.length > 25));
    const isHost = curHost && (item.host === curHost);

    if (isHost) {
      a.classList.add('nav-active-host');
    }
    if (isPage) {
      a.setAttribute('aria-current', 'page');
      a.classList.add('nav-active', 'nav-active-page');
    } else if (isHost) {
      a.setAttribute('aria-current', 'location');
    }

    li.appendChild(a);
    list.appendChild(li);
  }

  nav.appendChild(list);
  target.innerHTML = '';
  target.appendChild(nav);
  return nav;
}

// Auto-initialize into selector '#ccgsc-nav, [data-ccgsc-nav]'
export async function initNav(options = {}) {
  const sel = options.selector || '#ccgsc-nav, [data-ccgsc-nav]';
  const el = options.container || (typeof document !== 'undefined' ? document.querySelector(sel) : null);
  if (!el) return null;
  // One nav per page: never render a second bar into the same mount.
  const existing = typeof el.querySelector === 'function' ? el.querySelector('.ccgsc-nav') : null;
  if (existing) return existing;
  const links = options.links || await fetchNav(options.url);
  return renderNav(el, links, options);
}

// Update the optional service status line of a page, when the page has one.
export async function updateServiceStatus(url = '/healthz') {
  if (typeof document === 'undefined') return null;
  const status = document.getElementById('status');
  if (!status) return null;
  try {
    const res = await fetch(url);
    status.textContent = res.ok ? 'Service is running.' : 'Service health check failed.';
  } catch (_) {
    status.textContent = 'Service unavailable.';
  }
  return status;
}

// Global browser hook attachment and DOM auto-mount
if (typeof window !== 'undefined') {
  window.CCGSC_NAV = { getNavRoles, filterNavLinks, fetchNav, setNavCache, renderNav, initNav, updateServiceStatus };
  if (typeof document !== 'undefined' && !window.CCGSC_NAV_MANUAL_INIT) {
    const mount = () => { initNav().catch(console.error); updateServiceStatus().catch(() => {}); };
    if (document.readyState === 'loading' || document.readyState === 'interactive') {
      document.addEventListener('DOMContentLoaded', mount);
    } else {
      mount();
    }
  }
}

// END OF nav.js
