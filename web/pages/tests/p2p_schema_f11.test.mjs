// F11, 2026-11-25: red->green tests for the pools admin panel P2P rows.
// RED on base 063bfac5: tp/pp.p2p.enabled defaults to "on" (contradicts
// G-F11-2 and the code-off rule) and peer_scope claims near proximity.
// GREEN with the F11 schema patch. Repo-side only (imports web/pages/js/schema.js).
import test from 'node:test';
import assert from 'node:assert/strict';
import { PAGE_SCHEMAS } from '../js/schema.js';

for (const [page, pool] of [['thread-pool-admin', 'tp'], ['process-pool-admin', 'pp']]) {
  test(page + ': P2P mode row is stun/lan/off, admin-only, default off', () => {
    const rows = PAGE_SCHEMAS[page].rows;
    const mode = rows.find(r => r.id === pool + '.p2p.mode');
    assert.ok(mode, pool + '.p2p.mode row missing');
    assert.equal(mode.type, 'enum');
    assert.equal(mode.default, 'off');
    assert.equal(mode.range, 'stun/lan/off');
    assert.equal(mode.whoMayChange, 'admin');
    assert.equal(mode.section, 'p2p');
    assert.match(mode.legend, /same-app/i);
    assert.match(mode.legend, /no public STUN/i);
    assert.match(mode.legend, /no TURN/i);
    assert.match(mode.legend, /SHA-256/i);
    assert.match(mode.legend, /HTTP fallback/i);
  });

  test(page + ': no enabled toggle row stays behind and peer_scope is superseded', () => {
    const rows = PAGE_SCHEMAS[page].rows;
    assert.equal(rows.find(r => r.id === pool + '.p2p.enabled'), undefined);
    const scope = rows.find(r => r.id === pool + '.p2p.peer_scope');
    assert.ok(scope, 'peer_scope row kept as a stored setting');
    assert.match(scope.legend, /Superseded by/i);
    assert.equal(scope.whoMayChange, 'admin');
  });
}

test('results and receipts never appear as a P2P capability row', () => {
  for (const page of ['thread-pool-admin', 'process-pool-admin']) {
    for (const row of PAGE_SCHEMAS[page].rows.filter(r => r.section === 'p2p')) {
      assert.doesNotMatch(row.id, /result|receipt/, row.id);
    }
  }
});
