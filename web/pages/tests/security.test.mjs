// WO-HC-WEB-01 security.test.mjs - Security, XSS Defense, Accessibility and Concurrency Tests
// Author: Claude Opus 5.5 head / agy Gemini 3.8 Flash high. Date: 2026-11-18.
// Truth: WEBCHAT_R2_glm_pages_20261118.md (1 BLOCKER, 4 MAJOR, 17 MINOR) + WO_FIX_F_ADMIN_PAGES_JS.md

import { describe, it } from 'node:test';
import assert from 'node:assert/strict';

import { displayError, clearError, ProblemError, esc } from '../js/api.js';
import { CCGSCPageApp } from '../js/app.js';
import { PAGE_SCHEMAS } from '../js/schema.js';

// Minimal DOM mocking helper for browser-like testing in Node
function makeDomElement(tagName = 'div') {
  const el = {
    tagName: tagName.toUpperCase(),
    id: '',
    className: '',
    textContent: '',
    style: {},
    attributes: {},
    children: [],
    parentNode: null,
    disabled: false,
    value: '',
    checked: false,
    type: 'text',
    dataset: {},
    get href() { return this.attributes['href'] || ''; },
    set href(v) { this.setAttribute('href', v); },
    get target() { return this.attributes['target'] || ''; },
    set target(v) { this.setAttribute('target', v); },
    get rel() { return this.attributes['rel'] || ''; },
    set rel(v) { this.setAttribute('rel', v); },
    classList: {
      _classes: new Set(),
      add(c) { this._classes.add(c); el.className = Array.from(this._classes).join(' '); },
      remove(c) { this._classes.delete(c); el.className = Array.from(this._classes).join(' '); },
      contains(c) { return this._classes.has(c); }
    },
    setAttribute(k, v) {
      this.attributes[k] = String(v);
      if (k.startsWith('data-')) {
        const camel = k.slice(5).replace(/-([a-z])/g, (_, c) => c.toUpperCase());
        this.dataset[camel] = String(v);
      }
    },
    getAttribute(k) { return this.attributes[k] ?? null; },
    removeAttribute(k) { delete this.attributes[k]; },
    appendChild(c) {
      if (typeof c === 'string') {
        c = { tagName: '#text', textContent: c, parentNode: this };
      }
      this.children.push(c);
      c.parentNode = this;
      return c;
    },
    removeChild(c) {
      this.children = this.children.filter(x => x !== c);
      c.parentNode = null;
      return c;
    },
    querySelectorAll(sel) {
      const results = [];
      const match = (node) => {
        if (!node || !node.tagName) return;
        if (sel.startsWith('.')) {
          const cls = sel.slice(1);
          if (node.classList && node.classList.contains(cls)) results.push(node);
        } else if (sel.startsWith('#')) {
          if (node.id === sel.slice(1)) results.push(node);
        } else if (sel.startsWith('[') && sel.includes('^=')) {
          const m = sel.match(/\[([a-z0-9_-]+)\^="([^"]+)"\]/i);
          if (m && node.getAttribute && node.getAttribute(m[1]) && node.getAttribute(m[1]).startsWith(m[2])) {
            results.push(node);
          }
        } else if (sel.toLowerCase() === node.tagName.toLowerCase()) {
          results.push(node);
        }
        for (const ch of (node.children || [])) match(ch);
      };
      for (const ch of this.children) match(ch);
      return results;
    },
    querySelector(sel) {
      return this.querySelectorAll(sel)[0] || null;
    },
    addEventListener(evt, handler) {
      if (!this._listeners) this._listeners = {};
      if (!this._listeners[evt]) this._listeners[evt] = [];
      this._listeners[evt].push(handler);
    },
    dispatchEvent(evt) {
      const handlers = this._listeners?.[evt.type || evt] || [];
      for (const h of handlers) h(evt);
    }
  };

  Object.defineProperty(el, 'innerHTML', {
    get() {
      return this._innerHTML || '';
    },
    set(val) {
      this._innerHTML = val;
      this.children = [];
    }
  });

  return el;
}

function withDomContext(fn) {
  const previousDoc = globalThis.document;
  const previousWin = globalThis.window;
  const byId = new Map();

  const doc = {
    createElement: (tag) => {
      const el = makeDomElement(tag);
      return el;
    },
    createTextNode: (text) => ({
      tagName: '#text',
      textContent: text,
      parentNode: null
    }),
    getElementById: (id) => byId.get(id) || null,
    querySelectorAll: (sel) => {
      const res = [];
      for (const el of byId.values()) {
        if (sel.startsWith('[id^="') && el.id.startsWith(sel.slice(6, -2))) {
          res.push(el);
        }
      }
      return res;
    },
    body: makeDomElement('body')
  };

  doc.register = (id, el) => {
    el.id = id;
    byId.set(id, el);
    return el;
  };

  globalThis.document = doc;
  globalThis.window = {
    document: doc,
    location: { search: '' }
  };

  try {
    return fn(doc, byId);
  } finally {
    globalThis.document = previousDoc;
    globalThis.window = previousWin;
  }
}

describe('WO-HC-WEB-01 Security & Robustness Suite (FIX-F)', () => {

  describe('1. XSS Defense & Server Data Escaping', () => {
    it('esc() properly escapes HTML and attribute delimiter characters', () => {
      assert.equal(esc('<script>alert("xss")</script>'), '&lt;script&gt;alert(&quot;xss&quot;)&lt;/script&gt;');
      assert.equal(esc("' onfocus='alert(1)"), '&#39; onfocus=&#39;alert(1)');
      assert.equal(esc('a & b'), 'a &amp; b');
      assert.equal(esc(null), '');
      assert.equal(esc(undefined), '');
    });

    it('renderConfigVersionTable escapes author, pinnedName, and reason to prevent stored XSS (MAJOR)', () => {
      withDomContext((doc, byId) => {
        const container = doc.register('version-table-container', makeDomElement('div'));
        const app = new CCGSCPageApp('thread-pool-admin');
        app.configData = {
          version: 2,
          versions: [
            {
              version: 2,
              pinnedName: '<script>evil()</script>',
              reason: '<img src=x onerror=alert("reason_xss")>',
              author: '"><script>steal(sessionStorage)</script>',
              createdAt: new Date().toISOString(),
              inUseByJobs: 0
            }
          ]
        };

        app.renderConfigVersionTable();
        const html = container.innerHTML;

        assert.ok(!html.includes('<script>evil()</script>'), 'pinnedName script tag must not be injected raw');
        assert.ok(!html.includes('<img src=x onerror=alert("reason_xss")>'), 'reason img tag must not be injected raw');
        assert.ok(!html.includes('"><script>steal'), 'author script tag must not be injected raw');
        assert.ok(html.includes('&lt;script&gt;evil()&lt;/script&gt;'), 'pinnedName must be safely escaped');
        assert.ok(html.includes('&lt;img src=x onerror=alert(&quot;reason_xss&quot;)&gt;'), 'reason must be safely escaped');
      });
    });

    it('displayError uses DOM text nodes and whitelists only http(s) URLs for problem.type (MAJOR)', () => {
      withDomContext((doc) => {
        const container = makeDomElement('div');

        // Test with malicious javascript: URL in problem.type and HTML in detail/title/instance
        const evilProblem = new ProblemError({
          title: '<script>alert("title")</script>',
          status: 400,
          detail: '<b onmouseover="alert(1)">evil detail</b>',
          instance: '<iframe src="evil.html"></iframe>',
          type: 'javascript:alert(document.cookie)'
        });

        displayError(container, evilProblem);

        // Find any <a> links in container
        const links = container.querySelectorAll('a');
        assert.equal(links.length, 0, 'javascript: URL must NEVER be rendered as an <a> link');

        // Check text content is preserved without HTML interpretation
        function getAllText(node) {
          if (!node) return '';
          let txt = node.textContent || '';
          for (const ch of (node.children || [])) {
            txt += ' ' + getAllText(ch);
          }
          return txt;
        }
        const textDump = getAllText(container);
        assert.ok(textDump.includes('<script>alert("title")</script>'));
        assert.ok(textDump.includes('<b onmouseover="alert(1)">evil detail</b>'));
        assert.ok(textDump.includes('<iframe src="evil.html"></iframe>'));
        assert.ok(textDump.includes('javascript:alert(document.cookie)'));
        assert.ok(container.classList.contains('visible'));

        // Test that valid https URL IS rendered as an <a> link
        const safeContainer = makeDomElement('div');
        const safeProblem = new ProblemError({
          title: 'Bad Request',
          status: 400,
          detail: 'Parameter out of range',
          type: 'https://ccgsc.digitalbd.org/errors/out-of-range'
        });
        displayError(safeContainer, safeProblem);
        const safeLinks = safeContainer.querySelectorAll('a');
        assert.equal(safeLinks.length, 1);
        assert.equal(safeLinks[0].getAttribute('href'), 'https://ccgsc.digitalbd.org/errors/out-of-range');
        assert.equal(safeLinks[0].getAttribute('target'), '_blank');
        assert.equal(safeLinks[0].getAttribute('rel'), 'noopener noreferrer');
      });
    });

    it('renderFormField escapes values to prevent attribute breakout XSS (MAJOR)', () => {
      const app = new CCGSCPageApp('thread-pool-admin');
      const row = {
        id: 'tp.gen.name',
        label: 'Pool Name',
        legend: 'Human name for the thread pool',
        type: 'text',
        default: 'Web Thread Pool',
        range: '1-64 chars'
      };

      app.currentSettings[row.id] = '" onfocus="alert(1)" style="color:red" x="';
      const html = app.renderFormField(row);

      assert.ok(!html.includes('value="" onfocus="alert(1)"'), 'Must not allow unescaped double quotes to break out of attribute');
      assert.ok(html.includes('&quot; onfocus=&quot;alert(1)&quot;'), 'Quotes must be HTML-escaped inside attribute');
    });
  });

  describe('2. Accessibility & DOM Architecture', () => {
    it('renderMetricCard places tooltip outside the interactive anchor tag (WWPP-R40.1)', () => {
      const app = new CCGSCPageApp('monitor');
      const row = PAGE_SCHEMAS['monitor'].rows[0];
      const cardHtml = app.renderMetricCard(row, '#devices');

      // Check card element is not an <a> wrapping the tooltip
      assert.ok(!cardHtml.includes('<a href="#devices" class="metric-card"'), 'Card container should not be an <a> wrapping everything');
      assert.ok(cardHtml.includes('class="tooltip-icon"'), 'Tooltip icon must be present');
      assert.ok(cardHtml.includes('aria-describedby="tip-mon-'), 'Metric link must link to tooltip via aria-describedby');
    });

    it('renderFormField wires aria-describedby for tooltips and error messages', () => {
      const app = new CCGSCPageApp('thread-pool-admin');
      const row = {
        id: 'tp.wrk.max',
        label: 'Max Workers',
        legend: 'Maximum workers allowed',
        type: 'number',
        default: 4,
        range: '1-8'
      };

      const html = app.renderFormField(row);
      assert.ok(html.includes('aria-describedby="tip-tp-wrk-max err-tp-wrk-max"'), 'Input must have aria-describedby pointing to tip and err');
      assert.ok(html.includes('id="tip-tp-wrk-max"'), 'Tooltip must have expected id');
      assert.ok(html.includes('role="tooltip"'), 'Tooltip must have role="tooltip"');
      assert.ok(html.includes('id="err-tp-wrk-max" role="alert"'), 'Error element must have role="alert"');
    });

    it('renderConfigVersionTable has zero inline onclick attributes (CSP compliance)', () => {
      withDomContext((doc) => {
        const container = doc.register('version-table-container', makeDomElement('div'));
        const app = new CCGSCPageApp('thread-pool-admin');
        app.configData = {
          version: 2,
          versions: [
            { version: 2, reason: 'Current', author: 'admin', createdAt: new Date().toISOString(), inUseByJobs: 0 },
            { version: 1, reason: 'Initial', author: 'admin', createdAt: new Date().toISOString(), inUseByJobs: 0 }
          ]
        };

        app.renderConfigVersionTable();
        const html = container.innerHTML;

        assert.ok(!html.includes('onclick='), 'No inline onclick handler may be present');
        assert.ok(html.includes('data-rollback-version="1"'), 'Rollback button must use data-rollback-version');
      });
    });
  });

  describe('3. Form Controls & Concurrency Protection', () => {
    it('renderFormField renders enum placeholder when setting value is empty/TBD', () => {
      const app = new CCGSCPageApp('thread-pool-admin');
      const row = {
        id: 'tp.gen.fetch_mode',
        label: 'Fetch Mode',
        legend: 'How jobs are fetched',
        type: 'enum',
        default: 'TBD',
        range: 'leader/each-tab'
      };

      app.currentSettings[row.id] = '';
      const html = app.renderFormField(row);

      assert.ok(html.includes('<option value="" disabled selected>-- Select --</option>'),
        'Empty/TBD enum setting must render a disabled placeholder option so displayed matches stored');
    });

    it('renderFormField renders multi-select rows as multiple checkboxes', () => {
      const app = new CCGSCPageApp('thread-api-admin');
      const row = {
        id: 'ta.keys.scope',
        label: 'Key Scope',
        legend: 'Key permissions',
        type: 'multi-select',
        default: 'TBD',
        range: 'pool/artifacts/quota/events'
      };

      app.currentSettings[row.id] = ['pool', 'events'];
      const html = app.renderFormField(row);

      assert.ok(html.includes('value="pool"') && html.includes('checked'), 'pool checkbox must be checked');
      assert.ok(html.includes('value="events"') && html.includes('checked'), 'events checkbox must be checked');
      assert.ok(html.includes('value="artifacts"'), 'artifacts checkbox must be present');
    });

    it('blocks save and shows validation error when active field errors exist', async () => {
      await withDomContext(async (doc) => {
        const errorBanner = doc.register('error-banner', makeDomElement('div'));
        const saveBtn = doc.register('btn-save-config', makeDomElement('button'));
        const errField = doc.register('err-tp-wrk-max', makeDomElement('div'));
        errField.textContent = 'Number must be between 1 and 8'; // active error!

        const app = new CCGSCPageApp('thread-pool-admin');
        app.errorBanner = errorBanner;
        app.schema = { rows: [] };
        app.attachFormListeners();

        // Simulate save click
        saveBtn.dispatchEvent('click');

        // Error banner should be populated and visible
        assert.ok(errorBanner.classList.contains('visible'));
        const text = errorBanner.children.map(c => c.textContent).join(' ');
        assert.ok(text.includes('Validation Error') || text.includes('active validation errors'));
      });
    });

    it('prevents double-submit by disabling save button in flight', async () => {
      await withDomContext(async (doc) => {
        doc.register('error-banner', makeDomElement('div'));
        const saveBtn = doc.register('btn-save-config', makeDomElement('button'));
        const reasonInput = doc.register('save-reason', makeDomElement('input'));
        reasonInput.value = 'Valid reason for test';

        let callCount = 0;
        globalThis.fetch = async () => {
          callCount++;
          await new Promise(r => setTimeout(r, 40));
          return {
            ok: true,
            headers: { get: () => 'application/json' },
            json: async () => ({ success: true, version: 2, settings: {} })
          };
        };

        const app = new CCGSCPageApp('thread-pool-admin');
        app.schema = { rows: [] };
        app.attachFormListeners();

        // Click twice immediately
        saveBtn.dispatchEvent('click');
        saveBtn.dispatchEvent('click');

        await new Promise(r => setTimeout(r, 80));
        delete globalThis.fetch;

        assert.equal(callCount, 1, 'Only one save request may be issued when clicked rapidly');
      });
    });
  });

});

// END OF security.test.mjs
