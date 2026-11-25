// R10-FIX row 1 (qwen BLOCKER): the strict sandbox CSP must pin the hash of the
// EXACT text of the inline <script type="module"> element, not of a substring.
// The runner.html template emits "\n" + inline + "  ", so a hash over `inline`
// alone never matches the element the browser hashes and the runner never runs.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { fileURLToPath } from 'node:url';

import { buildInlineRunnerScript } from '../js/runner.js';

const pagesDir = path.join(path.dirname(fileURLToPath(import.meta.url)), '..');
const html = fs.readFileSync(path.join(pagesDir, 'runner.html'), 'utf8');

function sha256Base64(text) {
  return crypto.createHash('sha256').update(text, 'utf8').digest('base64');
}

test('runner.html pins the sha256 of the exact inline module script text', () => {
  const script = html.match(/<script type="module">([\s\S]*?)<\/script>/);
  assert.ok(script, 'the generated page carries the inline module script');
  const body = script[1];
  const hash = sha256Base64(body);

  const meta = html.match(/<meta http-equiv="Content-Security-Policy" content="([^"]+)">/);
  assert.ok(meta, 'runner.html must carry a CSP meta');
  const scriptSrc = meta[1].match(/script-src ([^;]+);/);
  assert.ok(scriptSrc, 'the CSP must carry a script-src');
  assert.ok(
    scriptSrc[1].includes(`'sha256-${hash}'`),
    `the CSP does not pin the real element text: element starts ${JSON.stringify(body.slice(0, 3))}, ` +
    `ends ${JSON.stringify(body.slice(-3))}, hashes to sha256-${hash}; CSP script-src is ${scriptSrc[1]}`
  );
});

test('the pinned hash covers the padding the template adds around the runner body', () => {
  const inline = buildInlineRunnerScript(fs.readFileSync(path.join(pagesDir, 'js', 'runner.js'), 'utf8'));
  const script = html.match(/<script type="module">([\s\S]*?)<\/script>/);
  assert.ok(script);
  const body = script[1];
  assert.equal(body, '\n' + inline + '  ', 'element text is the template body, padding included');
  const unpadded = sha256Base64(inline);
  const padded = sha256Base64(body);
  assert.notEqual(unpadded, padded,
    'this test is only meaningful while the template pads the body; hashing `inline` alone must differ');
});
