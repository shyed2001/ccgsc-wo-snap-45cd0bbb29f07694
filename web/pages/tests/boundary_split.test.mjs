// W10 design section 6: boundary_split.test.mjs
// Owner hard rule CR-53: platform code, native apps, tests/test workloads, thesis
// evidence and benchmarks are separate; the platform never imports or inlines any
// of the others. ChatGPT gate G1: the platform dependency graph excludes all
// workloads and runner.html is unchanged by workload-only edits. Gate G5 (static
// part): the generated runner.html carries the strict sandbox CSP of D-W10-5.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

import { buildInlineRunnerScript, WORKER_SRC, buildModuleWorkerSource } from '../js/runner.js';

const here = path.dirname(fileURLToPath(import.meta.url));
const pagesDir = path.join(here, '..');
const repoWebDir = path.join(pagesDir, '..');

// This build-time gate validates the separate workload manifest only. It must
// never generate workload source into platform JS, even for offline MOCK paths.
const BUILD_BRIDGE = 'build-demo-workload.mjs';
const GENERATED_DEMO = 'demo_workload.js';

function platformJsFiles() {
  const out = [];
  const walk = (dir) => {
    for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
      const full = path.join(dir, entry.name);
      if (entry.isDirectory()) {
        walk(full);
        continue;
      }
      if (/\.(js|mjs)$/.test(entry.name)) out.push(full);
    }
  };
  walk(path.join(pagesDir, 'js'));
  return out;
}

test('CR-53: no platform js file imports a workload, a test, a native app, thesis evidence or a benchmark', () => {
  for (const file of platformJsFiles()) {
    const rel = path.relative(repoWebDir, file);
    const source = fs.readFileSync(file, 'utf8');
    const specs = [...source.matchAll(/^\s*import\s[^'"]*['"]([^'"]+)['"]/gm)].map(m => m[1]);
    for (const spec of specs) {
      assert.doesNotMatch(spec, /workloads?\//, `${rel}: platform imports a workload (${spec})`);
      assert.doesNotMatch(spec, /(^|[./])tests?\//, `${rel}: platform imports a test (${spec})`);
      assert.doesNotMatch(spec, /AI_C_CPP|CCGS_WASM/, `${rel}: platform imports a native app tree (${spec})`);
      assert.doesNotMatch(spec, /thesis|benchmark|evidence/i, `${rel}: platform imports thesis/benchmark/evidence (${spec})`);
      if (spec.startsWith('.')) {
        const target = path.resolve(path.dirname(file), spec);
        assert.ok(target.startsWith(pagesDir + path.sep),
          `${rel}: relative import escapes the platform page tree (${spec})`);
      }
    }
  }
});

test('CR-53: the hand-written platform carries no workload code or kind fingerprints', () => {
  const runnerJs = fs.readFileSync(path.join(pagesDir, 'js', 'runner.js'), 'utf8');
  assert.doesNotMatch(runnerJs, /matmul/i, 'runner.js knows no workload');
  assert.doesNotMatch(runnerJs, /createMatmulRuntime/, 'runner.js must not reference the old runtime');
  const contributeJs = fs.readFileSync(path.join(pagesDir, 'js', 'contribute.js'), 'utf8');
  assert.doesNotMatch(contributeJs, /matmul/i, 'contribute.js knows no workload');
  assert.doesNotMatch(contributeJs, /web\/workloads/, 'contribute.js must not reference the workload tree');
});

test('CR-53: the platform contains NO copied/inlined demo workload', () => {
  assert.equal(fs.existsSync(path.join(pagesDir, 'js', GENERATED_DEMO)), false,
    'a generated workload implementation must not exist inside platform/js');
  const contributor = fs.readFileSync(path.join(pagesDir, 'js', 'contribute.js'), 'utf8');
  assert.ok(!contributor.includes('DEMO_WORKLOAD_SOURCE') &&
    !contributor.includes("from './demo_workload.js'"),
    'the platform may not import workload source');
  assert.ok(contributor.includes("fetch('/pools/mock-artifact'"),
    'mock bytes are loaded via a separate runtime artifact endpoint');
  for (const file of platformJsFiles()) {
    const name = path.basename(file);
    if (name === BUILD_BRIDGE) continue;
    const source = fs.readFileSync(file, 'utf8');
    assert.ok(!source.includes('from ' + "'../../workloads") && !source.includes('DEMO_WORKLOAD_SOURCE'),
      path.relative(repoWebDir, file) + ' contains a forbidden workload import or source');
  }
  const check = execFileSync(process.execPath, [path.join(pagesDir, 'js', BUILD_BRIDGE), '--check'], { encoding: 'utf8' });
  assert.ok(check.includes('manifest pin verified'), 'the independent artifact hash must match its workload manifest');
});

test('CR-53 / G1: runner.html is built from platform sources only, with no workload fingerprint', () => {
  const html = fs.readFileSync(path.join(pagesDir, 'runner.html'), 'utf8');
  assert.doesNotMatch(html, /matmul/i, 'runner.html contains no workload code');
  assert.doesNotMatch(html, /createMatmulRuntime/, 'runner.html contains no old runtime');
  // The builder reads js/runner.js only - a workload edit cannot touch the page.
  const buildSrc = fs.readFileSync(path.join(pagesDir, 'js', 'build-runner-html.mjs'), 'utf8');
  assert.ok(buildSrc.includes('readFileSync(runnerJsPath'), 'the builder reads the runner source');
  assert.doesNotMatch(buildSrc, /workloads/, 'the builder never reads the workload tree');
  const out = execFileSync(process.execPath, [path.join(pagesDir, 'js', 'build-runner-html.mjs'), '--check'], { encoding: 'utf8' });
  assert.ok(out.includes('up to date'), 'runner.html is in sync with js/runner.js: ' + out);
});

test('G1: rebuilding the inline runner from platform sources is byte-identical, workload state untouched', () => {
  const runnerSource = fs.readFileSync(path.join(pagesDir, 'js', 'runner.js'), 'utf8');
  const html = fs.readFileSync(path.join(pagesDir, 'runner.html'), 'utf8');
  const inline = buildInlineRunnerScript(runnerSource);
  const markerAt = html.indexOf('/* INLINED FROM js/runner.js - generated, do not edit */');
  assert.ok(markerAt > 0, 'the inline block carries the generation marker');
  const scriptEnd = html.indexOf('</script>', markerAt);
  // The template emits `${inline}  </script>`: the fresh platform-only build must
  // be byte-identical to the block on disk (a workload edit cannot appear here).
  assert.equal(html.slice(scriptEnd - 2, scriptEnd), '  ');
  assert.equal(html.slice(markerAt, scriptEnd - 2), inline, 'the inline block equals a fresh platform-only build');
});

test('G5 (static): runner.html carries the strict sandbox CSP of D-W10-5', () => {
  const html = fs.readFileSync(path.join(pagesDir, 'runner.html'), 'utf8');
  const meta = html.match(/<meta http-equiv="Content-Security-Policy" content="([^"]+)">/);
  assert.ok(meta, 'runner.html must carry a CSP meta');
  const csp = meta[1];
  assert.ok(csp.includes("default-src 'none'"), 'default-src none');
  assert.ok(/script-src[^;]*'sha256-[A-Za-z0-9+/=]+'/.test(csp), 'script-src pins the inline runner hash');
  assert.ok(/script-src[^;]*blob:/.test(csp), 'script-src allows the verified blob: module Workers');
  assert.ok(csp.includes('worker-src blob:'), 'worker-src blob:');
  assert.ok(csp.includes("connect-src 'none'"), "connect-src 'none': the sandbox has no network egress");
  // R10-FIX row 1: the pinned hash is the hash of the EXACT element text the
  // browser hashes - the builder's template padding included.
  const script = html.match(/<script type="module">([\s\S]*?)<\/script>/);
  assert.ok(script, 'runner.html carries the inline module script');
  const hash = crypto.createHash('sha256').update(script[1], 'utf8').digest('base64');
  assert.ok(csp.includes("'sha256-" + hash + "'"), 'the CSP hash matches the inline runner element text');
  const inline = buildInlineRunnerScript(fs.readFileSync(path.join(pagesDir, 'js', 'runner.js'), 'utf8'));
  assert.equal(script[1], '\n' + inline + '  ', 'the element text is the generated body plus its template padding');
});

test('G5 (static): the worker shell fetches nothing and imports only the verified workload URL', () => {
  // D-W10-6: the shell names the denied egress APIs (it locks them), but it never
  // CALLS one. Assert calls, and assert the lock is present.
  const shell = buildModuleWorkerSource('blob:test-entry');
  assert.doesNotMatch(shell, /\bfetch\s*\(/, 'the shell never fetches');
  assert.doesNotMatch(shell, /new\s+(XMLHttpRequest|WebSocket)\s*\(/, 'the shell opens no other egress API');
  assert.ok(shell.includes('lockWorkloadRealm'), 'the shell carries the capability lock');
  assert.ok(shell.includes("'XMLHttpRequest'") && shell.includes("'WebSocket'"),
    'the lock denies the egress APIs by name');
  // lock-only imports the workload; separate+lock routes to the kernel instead.
  assert.equal(shell.match(/\bimport\(/g).length, 1, 'the shell has exactly one workload import (lock-only mode)');
  assert.ok(shell.includes('await import(WORKLOAD_URL)'), 'the import is the pinned workload URL');
  assert.ok(shell.includes('new Worker(KERNEL_URL'), 'separate+lock starts the kernel Worker');
  assert.doesNotMatch(WORKER_SRC, /^\s*import\b/m, 'the shell template itself has no static import');
});
