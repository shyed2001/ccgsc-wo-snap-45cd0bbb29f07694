// Path: web/pages/tests/status_log_T104.test.mjs | Author: GLM (Z.ai) for Claude Opus 5.5 head | 2026-11-21
// What/why: T104 step 2 - the ONE status/log area of the integrated page.
// Ring buffer, per-source status chips, attach/adopt semantics, textContent-only
// rendering (never innerHTML), and subscriber isolation. No network.

import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { createStatusLog, MAX_LOG_LINES_DEFAULT } from '../js/status_log_T104.js';

function element() {
  return {
    textContent: '',
    set innerHTML(_) { throw new Error('innerHTML is never used by the status/log area'); },
    get innerHTML() { return ''; }
  };
}

describe('T104 status/log area', () => {

  it('buffers tagged lines newest first, without needing a DOM', () => {
    const log = createStatusLog();
    log.log('pools', 'first');
    log.log('legacy:matrix', 'second');
    const lines = log.lines();
    assert.equal(lines.length, 2);
    assert.equal(lines[0].source, 'legacy:matrix');
    assert.equal(lines[0].message, 'second');
    assert.equal(lines[1].source, 'pools');
    assert.ok(lines[0].at instanceof Date);
  });

  it('trims the ring buffer at maxLines', () => {
    const log = createStatusLog({ maxLines: 3 });
    for (let i = 0; i < 10; i++) log.log('pools', 'line ' + i);
    const lines = log.lines();
    assert.equal(lines.length, 3);
    assert.equal(lines[0].message, 'line 9');
    assert.equal(lines[2].message, 'line 7');
    assert.equal(MAX_LOG_LINES_DEFAULT, 200);
  });

  it('renders into the attached element with textContent only', () => {
    const el = element();
    const log = createStatusLog();
    log.attach(el);
    log.log('pools', 'hello');
    assert.ok(el.textContent.includes('[pools] hello'), el.textContent);
    assert.ok(!el.textContent.includes('legacy'));
  });

  it('keeps per-source status chips on one [status] line and clears them', () => {
    const el = element();
    const log = createStatusLog();
    log.attach(el);
    log.setStatus('pools', 'COMPUTING');
    log.setStatus('legacy:matrix', 'IDLE');
    assert.ok(el.textContent.startsWith('[status] pools=COMPUTING • legacy:matrix=IDLE'), el.textContent);
    assert.deepEqual(log.statuses(), { pools: 'COMPUTING', 'legacy:matrix': 'IDLE' });
    log.setStatus('legacy:matrix', '');
    assert.deepEqual(log.statuses(), { pools: 'COMPUTING' });
    assert.ok(!el.textContent.includes('legacy:matrix'));
  });

  it('attach adopts lines an app already wrote directly, but not the placeholder', () => {
    const el = element();
    el.textContent = '[System ready. Idle.]';
    const log = createStatusLog();
    log.attach(el);
    assert.equal(log.lines().length, 0, 'the shipped placeholder is not history');

    el.textContent = '[09:00:00] Racer setup\n[09:00:01] lane started';
    log.attach(el);
    const lines = log.lines();
    assert.equal(lines.length, 2);
    assert.equal(lines[0].source, 'page');
    assert.equal(lines[0].message, '[09:00:01] lane started');
    assert.ok(el.textContent.includes('[page] [09:00:01] lane started'), el.textContent);
    assert.ok(el.textContent.includes('[page] [09:00:00] Racer setup'), el.textContent);
  });

  it('detach stops rendering; the buffer survives', () => {
    const el = element();
    const log = createStatusLog();
    log.attach(el);
    log.log('pools', 'before');
    log.detach();
    assert.equal(log.attached(), false);
    const before = el.textContent;
    log.log('pools', 'after');
    assert.equal(el.textContent, before, 'nothing is rendered after detach');
    assert.equal(log.lines().length, 2);
    assert.equal(log.lines()[0].message, 'after');
  });

  it('subscribe notifies on lines and statuses and can unsubscribe', () => {
    const log = createStatusLog();
    const seen = [];
    const off = log.subscribe((entry) => seen.push(entry));
    log.log('pools', 'one');
    log.setStatus('pools', 'IDLE');
    off();
    log.log('pools', 'two');
    assert.equal(seen.length, 2);
    assert.equal(seen[0].message, 'one');
    assert.equal(seen[1].status, true);
  });

  it('a throwing subscriber never breaks the log', () => {
    const log = createStatusLog();
    log.subscribe(() => { throw new Error('boom'); });
    const entry = log.log('pools', 'still logged');
    assert.equal(entry.message, 'still logged');
    assert.equal(log.lines().length, 1);
  });

  it('logging without an element works and never touches innerHTML', () => {
    const log = createStatusLog();
    log.log('pools', 'no element'); // must not throw
    assert.equal(log.attached(), false);
    log.render(); // manual render without an element is a no-op
  });

  it('sanitises empty sources and null messages', () => {
    const log = createStatusLog();
    const entry = log.log('', null);
    assert.equal(entry.source, 'app');
    assert.equal(entry.message, '');
  });

});

// END OF status_log_T104.test.mjs
