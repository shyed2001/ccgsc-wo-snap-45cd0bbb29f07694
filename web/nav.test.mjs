// WO-HC-NAV-01 nav.test.mjs - Node:test and Bun-compatible Test Suite
// Author: hermes MiniMax-M3 for Claude Opus 5.5 | 2026-11-17 (WO-T2.2: canonical copy at web/nav.test.mjs)
// Original author: Claude Opus 5.5 head / agy Gemini 3.8 Flash high. Date: 2026-11-17.
// Truth: ctx_rows_R37_R40.md (R38.2, R38.3, R38.5) + design doc sec 3/4/14.
// One nav means one suite: this file tests the canonical web/nav.js and web/nav.json.
// web/pages/nav/nav.test.mjs is a shim that re-runs this suite on the old path.

import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { filterNavLinks, getNavRoles, setNavCache, fetchNav, renderNav } from './nav.js';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

// Load and parse nav.json
const navJsonRaw = fs.readFileSync(path.join(__dirname, 'nav.json'), 'utf8');
const navJson = JSON.parse(navJsonRaw);
const links = Array.isArray(navJson) ? navJson : (navJson.links || []);

describe('WO-HC-NAV-01 Navigation Test Suite', () => {

  describe('nav.json Schema & Structure', () => {
    it('contains a valid links array', () => {
      assert.ok(Array.isArray(links), 'links must be an array');
      assert.ok(links.length >= 10, `expected at least 10 links, got ${links.length}`);
    });

    it('ensures every link has required fields: id, label, href, host, visibility, group, legend', () => {
      const requiredFields = ['id', 'label', 'href', 'host', 'visibility', 'group', 'legend'];
      for (const item of links) {
        for (const field of requiredFields) {
          assert.ok(item[field] && typeof item[field] === 'string' && item[field].trim().length > 0,
            `link ${item.id || 'unnamed'} must have non-empty string '${field}'`);
        }
      }
    });

    it('validates allowed visibility levels (public|contributor|developer|admin|director)', () => {
      const allowedVisibilities = new Set(['public', 'contributor', 'developer', 'admin', 'director']);
      for (const item of links) {
        assert.ok(allowedVisibilities.has(item.visibility),
          `link ${item.id} has invalid visibility '${item.visibility}'`);
      }
    });

    it('validates allowed hosts', () => {
      const allowedHosts = new Set([
        'ccgsc.digitalbd.org',
        'ccgsc-demo.digitalbd.org',
        'ccgsc-director.digitalbd.org',
        'ccgsc-dev.digitalbd.org'
      ]);
      for (const item of links) {
        assert.ok(allowedHosts.has(item.host), `link ${item.id} has unexpected host '${item.host}'`);
      }
    });

    it('verifies all 8 pool admin pages on ccgsc-director are present', () => {
      const requiredAdminPages = [
        'monitor',
        'pool-monitor',
        'api-monitor',
        'thread-pool-admin',
        'process-pool-admin',
        'thread-api-admin',
        'process-api-admin',
        'manual'
      ];

      for (const pageName of requiredAdminPages) {
        const found = links.some(l =>
          l.host === 'ccgsc-director.digitalbd.org' &&
          l.visibility === 'admin' &&
          (l.id.includes(pageName) || l.href.includes(pageName))
        );
        assert.ok(found, `ccgsc-director must contain admin page for '${pageName}'`);
      }
    });

    it('ensures all links use absolute HTTPS URLs', () => {
      for (const item of links) {
        assert.ok(item.href.startsWith('https://'), `link ${item.id} must use absolute HTTPS URL, got ${item.href}`);
      }
    });
  });

  describe('Role Filtering & Public Always Shown', () => {
    const publicLinks = links.filter(l => l.visibility === 'public');
    const adminLinks = links.filter(l => l.visibility === 'admin');
    const devLinks = links.filter(l => l.visibility === 'developer');
    const contribLinks = links.filter(l => l.visibility === 'contributor');
    const directorLinks = links.filter(l => l.visibility === 'director');

    it('shows ONLY public links when roles array is empty []', () => {
      const filtered = filterNavLinks(links, []);
      assert.equal(filtered.length, publicLinks.length);
      for (const item of filtered) {
        assert.equal(item.visibility, 'public');
      }
    });

    it('always includes public links regardless of role', () => {
      const testRoleSets = [
        [],
        ['admin'],
        ['director'],
        ['contributor'],
        ['developer'],
        ['admin', 'director'],
        ['unknown_role']
      ];

      for (const r of testRoleSets) {
        const filtered = filterNavLinks(links, r);
        for (const pub of publicLinks) {
          assert.ok(filtered.some(l => l.id === pub.id), `public link ${pub.id} must be present for roles: ${JSON.stringify(r)}`);
        }
      }
    });

    it('shows public + admin links when roles is [admin]', () => {
      const filtered = filterNavLinks(links, ['admin']);
      assert.equal(filtered.length, publicLinks.length + adminLinks.length);
      for (const adminLink of adminLinks) {
        assert.ok(filtered.some(l => l.id === adminLink.id), `admin link ${adminLink.id} must be shown`);
      }
      for (const devLink of devLinks) {
        assert.ok(!filtered.some(l => l.id === devLink.id), `dev link ${devLink.id} should NOT be shown`);
      }
    });

    it('shows public + contributor links when roles is [contributor]', () => {
      const filtered = filterNavLinks(links, ['contributor']);
      assert.equal(filtered.length, publicLinks.length + contribLinks.length);
      for (const cl of contribLinks) {
        assert.ok(filtered.some(l => l.id === cl.id), `contributor link ${cl.id} must be shown`);
      }
    });

    it('shows public + developer links when roles is [developer]', () => {
      const filtered = filterNavLinks(links, ['developer']);
      assert.equal(filtered.length, publicLinks.length + devLinks.length);
      for (const dl of devLinks) {
        assert.ok(filtered.some(l => l.id === dl.id), `developer link ${dl.id} must be shown`);
      }
    });

    it('shows public + director links when roles is [director]', () => {
      const filtered = filterNavLinks(links, ['director']);
      assert.equal(filtered.length, publicLinks.length + directorLinks.length);
      for (const dl of directorLinks) {
        assert.ok(filtered.some(l => l.id === dl.id), `director link ${dl.id} must be shown`);
      }
    });

    it('shows public + admin + director links for multi-role [admin, director]', () => {
      const filtered = filterNavLinks(links, ['admin', 'director']);
      assert.equal(filtered.length, publicLinks.length + adminLinks.length + directorLinks.length);
    });

    it('ignores unknown roles and returns public links only', () => {
      const unknownRoles = ['superman', 'anonymous', 'guest', 'root', '', 999, null];
      const filtered = filterNavLinks(links, unknownRoles);
      assert.equal(filtered.length, publicLinks.length);
      for (const item of filtered) {
        assert.equal(item.visibility, 'public');
      }
    });

    it('handles null, undefined, or malformed inputs safely', () => {
      assert.deepEqual(filterNavLinks(null, ['admin']), []);
      assert.deepEqual(filterNavLinks(undefined, []), []);
      assert.deepEqual(filterNavLinks(links, null), publicLinks);
      assert.deepEqual(filterNavLinks(links, undefined), publicLinks);
    });
  });

  describe('Login State Hook & In-memory Cache', () => {
    it('getNavRoles reads window.CCGSC_NAV_ROLES if defined', () => {
      globalThis.window = { CCGSC_NAV_ROLES: ['admin', 'director'] };
      assert.deepEqual(getNavRoles(), ['admin', 'director']);

      globalThis.window = { CCGSC_NAV_ROLES: 'not-an-array' };
      assert.deepEqual(getNavRoles(), []);

      delete globalThis.window;
      assert.deepEqual(getNavRoles(), []);
    });

    it('fetchNav caches data in memory', async () => {
      const mockLinks = [{ id: 'mock', label: 'Mock', visibility: 'public' }];
      setNavCache(mockLinks);
      const cached = await fetchNav('dummy_url');
      assert.deepEqual(cached, mockLinks);
    });
  });

  describe('Accessible DOM Rendering & Highlighting', () => {
    function createMockElement(tagName = 'div') {
      return {
        tagName: tagName.toUpperCase(),
        className: '',
        attributes: {},
        children: [],
        innerHTML: '',
        textContent: '',
        classList: {
          classes: new Set(),
          add(...cls) { cls.forEach(c => this.classes.add(c)); },
          contains(c) { return this.classes.has(c); }
        },
        setAttribute(k, v) { this.attributes[k] = String(v); },
        getAttribute(k) { return this.attributes[k]; },
        appendChild(child) { this.children.push(child); return child; }
      };
    }

    const mockDoc = {
      createElement(tag) { return createMockElement(tag); }
    };

    it('renders accessible nav structure with aria-label', () => {
      const container = createMockElement('div');
      container.ownerDocument = mockDoc;

      const nav = renderNav(container, links, {
        roles: [],
        ariaLabel: 'Custom Test Navigation',
        currentHost: 'ccgsc.digitalbd.org',
        currentUrl: 'https://ccgsc.digitalbd.org/'
      });

      assert.ok(nav, 'renderNav must return root nav element');
      assert.equal(nav.getAttribute('aria-label'), 'Custom Test Navigation');
      assert.equal(container.children.length, 1);
      assert.equal(container.children[0], nav);

      const ul = nav.children[0];
      assert.equal(ul.tagName, 'UL');
      assert.ok(ul.children.length >= 3, 'guest nav should render public items');

      // Check current page aria-current="page"
      const firstA = ul.children[0].children[0];
      assert.equal(firstA.getAttribute('aria-current'), 'page');
      assert.ok(firstA.classList.contains('nav-active-page'));
      assert.ok(firstA.classList.contains('nav-active-host'));
    });

    it('highlights current host link with aria-current="location" when page does not match exactly', () => {
      const container = createMockElement('div');
      container.ownerDocument = mockDoc;

      const nav = renderNav(container, links, {
        roles: ['admin'],
        currentHost: 'ccgsc-director.digitalbd.org',
        currentUrl: 'https://ccgsc-director.digitalbd.org/pools/unlisted-custom-path'
      });

      const ul = nav.children[0];
      const adminItemA = ul.children.find(li => li.children[0].href.includes('thread-pool-admin'))?.children[0];
      assert.ok(adminItemA, 'admin link must be rendered');
      assert.equal(adminItemA.getAttribute('aria-current'), 'location');
      assert.ok(adminItemA.classList.contains('nav-active-host'));
    });
  });

  describe('nav.js Code Constraints & Guardrails', () => {
    const navJsPath = path.join(__dirname, 'nav.js');
    const navJsContent = fs.readFileSync(navJsPath, 'utf8');
    const navJsLines = navJsContent.split(/\r?\n/);

    it('nav.js must be strictly less than 150 lines', () => {
      assert.ok(navJsLines.length < 150, `nav.js has ${navJsLines.length} lines, must be < 150`);
    });

    it('nav.js must NOT read or access cookies or storage', () => {
      assert.ok(!navJsContent.includes('document.cookie'), 'no cookies access permitted');
      assert.ok(!navJsContent.includes('localStorage'), 'no localStorage permitted');
      assert.ok(!navJsContent.includes('sessionStorage'), 'no sessionStorage permitted');
      assert.ok(!navJsContent.includes('indexedDB'), 'no indexedDB permitted');
    });

    it('nav.js must contain Header and END footer comments', () => {
      assert.ok(navJsLines[0].startsWith('// WO-HC-NAV-01'), 'nav.js must start with Header comment');
      const lastNonEmpty = navJsLines.filter(l => l.trim().length > 0).pop();
      assert.ok(lastNonEmpty.includes('END OF nav.js'), `nav.js must end with END OF nav.js, got ${lastNonEmpty}`);
    });
  });
});

// END OF nav.test.mjs
