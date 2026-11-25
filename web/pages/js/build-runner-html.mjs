// Path: web/pages/js/build-runner-html.mjs | Author: hermes MiniMax-M3 for the owner | 2026-11-17
// What/why: generates web/pages/runner.html from web/pages/js/runner.js, so the inline
// copy of the runner in the sandbox page can never drift from the module source
// (WO_FIX_B_RUNNER_JS: single source plus a test that they match).
//
// W10b (2026-11-21, D-W10-5): the generated page carries a strict CSP - the
// sha256 hash of the exact inline runner plus blob: (the verified workload and
// shell module Workers), worker-src blob: and connect-src 'none'. The sandbox
// has no network egress: network access happens only through the platform parent.
//
// Usage: node web/pages/js/build-runner-html.mjs [--check]
//   --check  exits 1 when runner.html is out of date, without writing it.
//
// Go port: cmd/buildrunner (D-W10-12); keep both until owner retires this file.

import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { fileURLToPath } from 'node:url';

import { buildInlineRunnerScript, RUNNER_BUILD_ID, OPERATOR_KEYS } from './runner.js';

const here = path.dirname(fileURLToPath(import.meta.url));
const runnerJsPath = path.join(here, 'runner.js');
const runnerHtmlPath = path.join(here, '..', 'runner.html');

const runnerSource = fs.readFileSync(runnerJsPath, 'utf8');
const inline = buildInlineRunnerScript(runnerSource);

// R10-FIX row 1: the element text the browser hashes is the WHOLE text between
// <script type="module"> and </script>, including the template padding. Build it
// once here and use the same string for the hash and for the emitted element, so
// the pinned sha256 always covers exactly what the browser sees.
const scriptBody = '\n' + inline + '  ';

// D-W10-5: strict CSP. default-src 'none'; script-src = the hash of this exact
// inline runner + blob: (module import of the verified workload blob inside the
// Worker); worker-src blob:; connect-src 'none'. The hash is computed over the
// exact element text, so any runner change rebuilds the page and the check.
const scriptHash = crypto.createHash('sha256').update(scriptBody, 'utf8').digest('base64');
const csp = "default-src 'none'; script-src 'sha256-" + scriptHash + "' blob:; worker-src blob:; connect-src 'none'";

// R10-FIX owner D-W10-9 ("Runner self-reports"): the page bakes the identity of
// the runner it ships as RUNNER_PIN = "<sha256 of the runner body>:<keyId>". The
// lockstep rule is ONE pin: the hash is over the exact script element text
// (scriptBody, the same bytes the CSP hash covers), never over `inline` alone,
// so the pin the server compares and the CSP the browser enforces describe the
// same runner. The key id is the operator key the runner verifies against. The
// runner reads the pin back and reports it in its 'ready' message; the parent
// forwards it on register and heartbeat.
const runnerPinHash = crypto.createHash('sha256').update(scriptBody, 'utf8').digest('hex');
const runnerPinKeyId = Object.keys(OPERATOR_KEYS)[0] || '';
const runnerPin = `${runnerPinHash}:${runnerPinKeyId}`;

const html = `<!DOCTYPE html>
<!-- WO_T3_CONTRIBUTE_PAGE runner.html - Sandbox Runner (ccgsc-run origin) -->
<!-- Author: Claude Opus 5.5 head / agy Gemini 3.8 Flash high. Date: 2026-11-17 -->
<!-- Truth: WO_T3_CONTRIBUTE_PAGE.md, GRILL_WWPP_R1.md (R29.1, R37.1, R39.1), SPLIT_DESIGN_W10.md section 8 (D-W10-5) -->
<!-- GENERATED FILE: the script below is generated from js/runner.js by -->
<!-- web/pages/js/build-runner-html.mjs. Do not edit the inline script by hand. -->
<html lang="en" data-runner-pin="${runnerPin}">
<head>
  <meta charset="UTF-8">
  <!-- D-W10-5 strict sandbox CSP: no egress, no inline script beyond the pinned -->
  <!-- runner hash, Workers only from verified blob: URLs. -->
  <meta http-equiv="Content-Security-Policy" content="${csp}">
  <title>CCGSC Sandbox Runner</title>
  <!-- owner D-W10-9: the runner's own pin, read back by the runner off the
       document element and reported in its ready message. A page attribute keeps
       the CSP untouched (no second inline script) and the hash non-circular. -->
</head>
<body>
  <!-- Opaque execution sandbox. All communication strictly over transferred MessagePort -->
  <!-- The runner script is inlined from js/runner.js: the sandbox has an opaque origin -->
  <!-- and a module fetch of js/runner.js would be blocked by CORS. -->
  <script type="module">${scriptBody}</script>
</body>
</html>
<!-- END OF runner.html -->
<!-- build id: ${RUNNER_BUILD_ID} -->
`;

if (process.argv.includes('--check')) {
  const current = fs.existsSync(runnerHtmlPath) ? fs.readFileSync(runnerHtmlPath, 'utf8') : '';
  if (current !== html) {
    process.stderr.write('runner.html is out of date: run node web/pages/js/build-runner-html.mjs\n');
    process.exit(1);
  }
  process.stdout.write('runner.html is up to date\n');
} else {
  fs.writeFileSync(runnerHtmlPath, html, 'utf8');
  process.stdout.write('wrote ' + runnerHtmlPath + '\n');
}
