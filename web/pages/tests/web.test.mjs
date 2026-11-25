// WO-HC-WEB-01 web.test.mjs - Node:test & Bun-compatible Test Suite for Web Pages
// Author: Claude Opus 5.5 head / agy Gemini 3.8 Flash high. Date: 2026-11-17.
// Truth: F:/tmp/hcwork/ADMIN_MANUALS_SOURCE.md + ctx_rows_R37_R40.md

import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { PAGE_SCHEMAS, validateRowValue } from '../js/schema.js';
import { isMockMode, getMockData, handleMockPost, createMockEventStream } from '../js/mock.js';
import { saveConfig, ProblemError } from '../js/api.js';
import { DETAIL_ANCHOR_MAP } from '../js/app.js';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const webRootDir = path.join(__dirname, '..');

describe('WO-HC-WEB-01 Test Suite', () => {

  describe('1. Schema Row Counts & Field Completeness (Truth: ADMIN_MANUALS_SOURCE.md)', () => {
    const expectedPageCounts = {
      'monitor': 34,
      'pool-monitor': 20,
      'api-monitor': 21,
      // W10D row 2 (D-W10-11): one contributor-controls row per pool admin page.
      'thread-pool-admin': 73,
      'process-pool-admin': 79,
      'thread-api-admin': 36,
      'process-api-admin': 44
    };

    it('verifies all 7 pages exist in PAGE_SCHEMAS with exact row counts', () => {
      let totalRows = 0;
      for (const [pageId, expectedCount] of Object.entries(expectedPageCounts)) {
        const schema = PAGE_SCHEMAS[pageId];
        assert.ok(schema, `Schema for page '${pageId}' must exist`);
        assert.equal(schema.rows.length, expectedCount,
          `Page '${pageId}' must have exactly ${expectedCount} rows, found ${schema.rows.length}`);
        totalRows += schema.rows.length;
      }
      assert.equal(totalRows, 307, `Total rows across all 7 pages must be exactly 307, found ${totalRows}`);
    });

    it('verifies every single row contains all mandatory schema attributes', () => {
      const requiredFields = ['id', 'label', 'legend', 'type', 'default', 'range', 'whoMayChange', 'wwppId', 'section'];
      for (const [pageId, schema] of Object.entries(PAGE_SCHEMAS)) {
        for (const row of schema.rows) {
          for (const field of requiredFields) {
            assert.ok(row[field] !== undefined && row[field] !== null,
              `Row '${row.id || 'unnamed'}' on page '${pageId}' missing mandatory field '${field}'`);
          }
        }
      }
    });

    it('verifies all 34 monitor metrics map to exact detail anchors (WWPP-R40.1)', () => {
      const monitorRows = PAGE_SCHEMAS['monitor'].rows;
      assert.equal(monitorRows.length, 34);
      for (const row of monitorRows) {
        const target = DETAIL_ANCHOR_MAP[row.id];
        assert.ok(target, `Metric ${row.id} must have a mapped detail anchor`);
        assert.ok(target.includes('.html#'), `Target '${target}' must include page and anchor (#)`);
      }
    });

    it('verifies every row has a one-line legend tooltip text', () => {
      for (const [pageId, schema] of Object.entries(PAGE_SCHEMAS)) {
        for (const row of schema.rows) {
          assert.ok(typeof row.legend === 'string' && row.legend.trim().length > 0,
            `Row '${row.id}' on page '${pageId}' must have non-empty tooltip legend text`);
          assert.ok(!row.legend.includes('\n'),
            `Row '${row.id}' legend must be a single line, found newline`);
        }
      }
    });
  });

  describe('2. Input Range Validation Engine', () => {
    it('validates numeric ranges accurately', () => {
      const row = { id: 'test.num', range: '1-4', type: 'number' };
      assert.ok(validateRowValue(row, 1).valid);
      assert.ok(validateRowValue(row, 4).valid);
      assert.ok(validateRowValue(row, '2').valid);
      assert.equal(validateRowValue(row, 0).valid, false);
      assert.equal(validateRowValue(row, 5).valid, false);
      assert.equal(validateRowValue(row, 'abc').valid, false);
    });

    it('validates percentage ranges accurately', () => {
      const row = { id: 'tp.wrk.phone_stop', range: '5-50', type: 'percent' };
      assert.ok(validateRowValue(row, 20).valid);
      assert.ok(validateRowValue(row, 5).valid);
      assert.ok(validateRowValue(row, 50).valid);
      assert.equal(validateRowValue(row, 4).valid, false);
      assert.equal(validateRowValue(row, 55).valid, false);
    });

    it('validates enum values strictly', () => {
      const row = { id: 'tp.gen.fetch_mode', range: 'leader/each-tab', type: 'enum' };
      assert.ok(validateRowValue(row, 'leader').valid);
      assert.ok(validateRowValue(row, 'each-tab').valid);
      assert.equal(validateRowValue(row, 'invalid-mode').valid, false);
    });

    it('validates toggle on/off states', () => {
      const row = { id: 'tp.gen.enabled', range: 'on/off', type: 'toggle' };
      assert.ok(validateRowValue(row, 'on').valid);
      assert.ok(validateRowValue(row, 'off').valid);
      assert.ok(validateRowValue(row, true).valid);
      assert.ok(validateRowValue(row, false).valid);
      assert.equal(validateRowValue(row, 'maybe').valid, false);
    });

    it('validates string character length bounds', () => {
      const row = { id: 'tp.gen.name', range: '1-64 chars', type: 'text' };
      assert.ok(validateRowValue(row, 'Web Thread Pool').valid);
      assert.equal(validateRowValue(row, '').valid, false);
      assert.equal(validateRowValue(row, 'a'.repeat(65)).valid, false);
    });

    it("validates 'off only' strictly and rejects 'on' even when type is toggle (WWPP-R30.1)", () => {
      const row = { id: 'tp.gd.hidden_tab', range: 'off only', type: 'toggle' };
      assert.ok(validateRowValue(row, 'off').valid);
      assert.ok(validateRowValue(row, false).valid);
      assert.equal(validateRowValue(row, 'on').valid, false);
      assert.equal(validateRowValue(row, true).valid, false);
    });

    it('validates duration ranges and rejects invalid patterns like banana', () => {
      const row = { id: 'ta.keys.lifetime', range: '1h-365d', type: 'duration' };
      assert.ok(validateRowValue(row, '1h').valid);
      assert.ok(validateRowValue(row, '24h').valid);
      assert.ok(validateRowValue(row, '7d').valid);
      assert.ok(validateRowValue(row, '365d').valid);
      assert.equal(validateRowValue(row, 'banana').valid, false);
      assert.equal(validateRowValue(row, '0h').valid, false);
      assert.equal(validateRowValue(row, '400d').valid, false);
    });

    it('validates integer worker counts and rejects fractional numbers', () => {
      const row = { id: 'tp.wrk.max', range: '1-4', type: 'number' };
      assert.ok(validateRowValue(row, 1).valid);
      assert.ok(validateRowValue(row, 4).valid);
      assert.equal(validateRowValue(row, 3.5).valid, false);
      assert.equal(validateRowValue(row, '3.5').valid, false);
    });

    it('validates multi-select arrays and comma-separated lists', () => {
      const row = { id: 'ta.keys.scope', range: 'pool/artifacts/quota/events', type: 'multi-select', default: 'TBD' };
      assert.ok(validateRowValue(row, ['pool', 'events']).valid);
      assert.ok(validateRowValue(row, 'pool,artifacts').valid);
      assert.equal(validateRowValue(row, ['pool', 'invalid_scope']).valid, false);
      assert.equal(validateRowValue(row, 'invalid_scope').valid, false);
    });
  });

  describe('3. Mock System & API Layer (?mock=1)', () => {
    it('generates complete mock metrics with capacity, work, trust, API and system counters', () => {
      const metrics = getMockData('metrics');
      assert.ok(metrics, 'Metrics object must be returned');
      assert.ok(typeof metrics['mon.cap.contributors'] === 'number');
      assert.ok(typeof metrics['mon.cap.tabs'] === 'number');
      assert.ok(typeof metrics['mon.work.running'] === 'number');
      assert.ok(typeof metrics['mon.work.tasks_per_s'] === 'number');
      assert.ok(typeof metrics['mon.api.req_per_min'] === 'number');
      assert.ok(typeof metrics['mon.sys.cpu'] === 'number');
    });

    it('saves a new configuration version with audit reason and increments version number', () => {
      const page = 'thread-pool-admin';
      const initial = getMockData(`config/${page}`);
      const initialVer = initial.version;

      const saveRes = handleMockPost(`config/${page}`, {
        settings: { ...initial.settings, 'tp.wrk.max': 8 },
        reason: 'Increased worker max for thesis testing',
        pinName: 'thesis-v3'
      });

      assert.ok(saveRes.success);
      assert.equal(saveRes.version, initialVer + 1);

      const updated = getMockData(`config/${page}`);
      assert.equal(updated.version, initialVer + 1);
      assert.equal(updated.versions[0].reason, 'Increased worker max for thesis testing');
      assert.equal(updated.versions[0].pinnedName, 'thesis-v3');
    });

    it('performs one-click rollback by creating a new audited version copying target settings', () => {
      const page = 'thread-pool-admin';
      const current = getMockData(`config/${page}`);
      const rollbackRes = handleMockPost(`config/${page}/rollback`, {
        rollbackVersion: 1,
        reason: 'Rolling back test'
      });

      assert.ok(rollbackRes.success);
      assert.equal(rollbackRes.version, current.version + 1);
    });

    it('starts and closes simulated SSE live metric event stream', async () => {
      let receivedCount = 0;
      const stream = createMockEventStream((data) => {
        if (data) receivedCount++;
      });
      assert.ok(stream && typeof stream.close === 'function');

      // Wait briefly for initial tick
      await new Promise(r => setTimeout(r, 200));
      assert.ok(receivedCount >= 1, 'Stream must emit at least one metric packet');
      stream.close();
    });

    it('enforces required changelog reason on saveConfig per WWPP-R38.6', async () => {
      globalThis.window = { location: { search: '?mock=1' } };
      await assert.rejects(async () => {
        await saveConfig('thread-pool-admin', {
          settings: {},
          reason: '   ' // empty reason
        });
      }, (err) => {
        assert.ok(err instanceof ProblemError);
        assert.equal(err.status, 400);
        return true;
      });
      delete globalThis.window;
    });

    it('honours expectedVersion and detects version conflicts in handleMockPost (WWPP-R38.6)', () => {
      const page = 'thread-pool-admin';
      const current = getMockData(`config/${page}`);
      
      // Matching version succeeds
      const resOk = handleMockPost(`config/${page}`, {
        settings: { ...current.settings },
        reason: 'Valid matching version save',
        expectedVersion: current.version
      });
      assert.ok(resOk.success);

      // Stale expectedVersion throws 409 conflict
      assert.throws(() => {
        handleMockPost(`config/${page}`, {
          settings: { ...current.settings },
          reason: 'Conflicting save attempt',
          expectedVersion: current.version // old version now stale
        });
      }, (err) => {
        assert.equal(err.status, 409);
        assert.ok(err.message.includes('conflict'));
        return true;
      });
    });
  });

  describe('4. Web Files Smoke Verification (String & Structure Checks)', () => {
    const requiredPages = [
      'monitor.html',
      'pool-monitor.html',
      'api-monitor.html',
      'thread-pool-admin.html',
      'process-pool-admin.html',
      'thread-api-admin.html',
      'process-api-admin.html'
    ];

    it('verifies all 7 main HTML pages exist and contain mandatory structural tags', () => {
      for (const file of requiredPages) {
        const filePath = path.join(webRootDir, file);
        assert.ok(fs.existsSync(filePath), `File ${file} must exist`);
        const content = fs.readFileSync(filePath, 'utf8');

        assert.ok(content.includes('<!DOCTYPE html>'), `${file} must contain DOCTYPE`);
        assert.ok(content.includes('id="ccgsc-nav"'), `${file} must contain #ccgsc-nav container`);
        assert.ok(content.includes('id="main-content"'), `${file} must contain #main-content container`);
        assert.ok(content.includes('id="manual"'), `${file} must contain #manual container`);
        assert.ok(content.includes('css/style.css'), `${file} must link shared style.css`);
        assert.ok(content.includes('js/app.js'), `${file} must load app.js`);
        assert.ok(content.includes(`<!-- WO-HC-WEB-01 ${file}`), `${file} must contain Header comment`);
        assert.ok(content.includes(`<!-- END OF ${file} -->`), `${file} must contain END footer comment`);
      }
    });

    it('verifies all 7 manual reference HTML pages exist and match schema rows', () => {
      for (const file of requiredPages) {
        const manualFile = path.join(webRootDir, 'manual', file);
        assert.ok(fs.existsSync(manualFile), `Manual file manual/${file} must exist`);
        const content = fs.readFileSync(manualFile, 'utf8');

        assert.ok(content.includes('<!DOCTYPE html>'), `manual/${file} must contain DOCTYPE`);
        assert.ok(content.includes('manual-rows-tbody'), `manual/${file} must contain table body`);
        assert.ok(content.includes('../css/style.css'), `manual/${file} must link style.css`);
        assert.ok(content.includes(`<!-- WO-HC-WEB-01 manual/${file}`), `manual/${file} must contain Header comment`);
        assert.ok(content.includes(`<!-- END OF manual/${file} -->`), `manual/${file} must contain END footer comment`);
      }
    });

    it('verifies shared CSS and JS files exist with required Header and END footers', () => {
      const codeFiles = [
        'css/style.css',
        'js/schema.js',
        'js/api.js',
        'js/mock.js',
        'js/app.js'
      ];
      for (const relPath of codeFiles) {
        const fullPath = path.join(webRootDir, relPath);
        assert.ok(fs.existsSync(fullPath), `${relPath} must exist`);
        const content = fs.readFileSync(fullPath, 'utf8');
        const fileName = path.basename(relPath);
        assert.ok(content.includes('WO-HC-WEB-01'), `${relPath} must contain WO-HC-WEB-01 Header`);
        assert.ok(content.includes(`END OF ${fileName}`), `${relPath} must contain END OF ${fileName} footer`);
      }
    });
  });

});

// END OF web.test.mjs

