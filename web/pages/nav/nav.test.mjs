// WO-HC-NAV-01 nav.test.mjs - compatibility shim
// Author: hermes MiniMax-M3 for Claude Opus 5.5 | 2026-11-17 (WO-T2.2)
// What/why: one nav means one test suite. The canonical suite now lives at
//   web/nav.test.mjs and runs against the canonical web/nav.js and web/nav.json.
//   This file keeps the documented `node --test web/pages/nav/nav.test.mjs` path
//   working by re-running that one suite.
import '../../nav.test.mjs';
