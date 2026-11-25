import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const pages = path.join(here, '..');

describe('MVPW-E sandbox wiring', () => {
  it('keeps the contributor iframe opaque and obtains its source from server config', () => {
    const html = fs.readFileSync(path.join(pages, 'contribute.html'), 'utf8');
    assert.match(html, /sandbox="allow-scripts"/);
    assert.doesNotMatch(html, /allow-same-origin/);
    assert.doesNotMatch(html, /src="runner\.html"/);
  });

  it('pins the fetched run origin before assigning runner.html', () => {
    const source = fs.readFileSync(path.join(pages, 'js', 'contribute.js'), 'utf8');
    assert.match(source, /fetch\('\/pools\/runner-config'/);
    assert.match(source, /frame\.src = `\$\{pinnedOrigin\}\/runner\.html`/);
    assert.match(source, /origin\.origin === location\.origin/);
  });
});
