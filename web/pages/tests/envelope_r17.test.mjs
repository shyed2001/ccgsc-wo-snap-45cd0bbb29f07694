// WO R17-ENVELOPE: the pages read the additive message/hint of a problem body
// and fall back to detail when a server still sends only the old body.
import { describe, it, before, after } from 'node:test';
import assert from 'node:assert/strict';

import { ProblemError, displayError } from '../js/api.js';

// r17Element is the small DOM element displayError needs.
function r17Element(tagName = 'div') {
  const el = {
    tagName: tagName.toUpperCase(),
    children: [],
    style: {},
    textContent: '',
    innerHTML: '',
    className: '',
    _classes: new Set(),
    classList: {
      add(c) { el._classes.add(c); },
      remove(c) { el._classes.delete(c); },
      contains(c) { return el._classes.has(c); }
    },
    appendChild(child) { el.children.push(child); return child; }
  };
  return el;
}

// r17Text flattens the text of a rendered subtree.
function r17Text(node) {
  if (!node) return '';
  let text = node.textContent || '';
  for (const child of node.children || []) text += '\n' + r17Text(child);
  return text;
}

describe('R17 error envelope in the pages', () => {
  let previousDocument;
  before(() => {
    previousDocument = globalThis.document;
    globalThis.document = { createElement: (tag) => r17Element(tag) };
  });
  after(() => {
    globalThis.document = previousDocument;
  });

  it('ProblemError prefers message over detail and keeps hint/requestId/retryable', () => {
    const problem = new ProblemError({
      status: 400,
      title: 'Bad Request',
      detail: 'legacy detail',
      message: 'envelope message',
      hint: 'envelope hint',
      requestId: 'req-1',
      retryable: true
    });
    assert.equal(problem.message, 'envelope message');
    assert.equal(problem.detail, 'legacy detail');
    assert.equal(problem.hint, 'envelope hint');
    assert.equal(problem.requestId, 'req-1');
    assert.equal(problem.retryable, true);
  });

  it('ProblemError falls back to detail on an old body', () => {
    const problem = new ProblemError({ status: 400, title: 'Bad Request', detail: 'legacy detail' });
    assert.equal(problem.message, 'legacy detail');
    assert.equal(problem.hint, '');
  });

  it('displayError renders message and hint', () => {
    const container = r17Element('div');
    displayError(container, new ProblemError({
      status: 400,
      title: 'Bad Request',
      detail: 'legacy detail',
      message: 'envelope message',
      hint: 'envelope hint'
    }));
    const text = r17Text(container);
    assert.ok(text.includes('envelope message'), `rendered text lost the message: ${text}`);
    assert.ok(text.includes('envelope hint'), `rendered text lost the hint: ${text}`);
  });

  it('displayError renders detail when there is no message', () => {
    const container = r17Element('div');
    displayError(container, new ProblemError({ status: 400, title: 'Bad Request', detail: 'legacy detail' }));
    assert.ok(r17Text(container).includes('legacy detail'));
  });
});
