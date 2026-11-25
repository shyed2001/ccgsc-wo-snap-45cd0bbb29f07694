// File: F:\tmp\wwpp\spike_sandbox2\pw_run.mjs | Author: Claude Opus 5.5 for owner | 2026-11-17
// What/why: run spike2 parent.html in Playwright chromium, firefox, webkit on a lab node; print CHECK lines.
// Run: NODE_PATH=~/ccgsc_rc_lf/node_modules node pw_run.mjs   (serve.py must run on 8711/8712)
import { createRequire } from 'module';
const require = createRequire(process.env.HOME + '/ccgsc_rc_lf/');
const pw = require('playwright');
for (const name of ['chromium', 'firefox', 'webkit']) {
  let b;
  try {
    const home = process.env.HOME + '/.cache/ms-playwright/';
    const exe = { firefox: home + 'firefox-1543/firefox/firefox', webkit: home + 'webkit-2359/pw_run.sh' }[name];
    b = await pw[name].launch(exe ? { headless: true, executablePath: exe } : { headless: true });
    const p = await b.newPage();
    await p.goto('http://127.0.0.1:8711/parent.html');
    await p.waitForFunction(() => /SPIKE2 (PASS|FAIL)/.test(document.title), null, { timeout: 60000 });
    const log = await p.evaluate(() => document.getElementById('log').textContent);
    console.log('== ' + name + ' ' + (await p.title()));
    console.log(log.split('\n').filter((l) => /^CHECK|SPIKE2/.test(l)).join('\n'));
  } catch (e) { console.log('== ' + name + ' ERROR ' + e.message.split('\n')[0]); }
  finally { if (b) await b.close(); }
}
// END OF FILE pw_run.mjs
