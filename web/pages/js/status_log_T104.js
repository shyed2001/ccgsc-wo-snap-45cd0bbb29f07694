// Path: web/pages/js/status_log_T104.js | Author: GLM (Z.ai) for Claude Opus 5.5 head | 2026-11-21
// What/why: T104 step 2 - the ONE status/log area of the integrated contributor
// page. The new pools (ContributeApp, source "pools") and the old lanes (through
// the LegacyLaneAdapter, source "legacy:<lane>") write tagged lines into one
// buffer that renders into one element with textContent only - never innerHTML,
// so a hostile lane name or message cannot become markup. attach() adopts the
// lines an app already wrote directly into the element, so wiring can start
// before or after the app without losing a line.
//
// Truth: docs/T104_INTEGRATED_PAGE_PLAN.md, owner T104 rules (issue #117).

'use strict';

export const MAX_LOG_LINES_DEFAULT = 200;

// The placeholder the shipped pages put inside the log box. Adopted lines that
// match it are dropped instead of becoming fake history.
const PLACEHOLDER_PREFIX = '[System ready. Idle.]';

/**
 * Create one status/log area.
 *
 * Lines are kept newest-first, mirroring ContributeApp.log. The rendered text is
 * `[HH:MM:SS] [source] message` per line, optionally preceded by one
 * `[status] source=text • source=text` line. Without an attached element the
 * buffer still works, so callers and tests never need a DOM.
 *
 * @param {Object} [options]
 * @param {number} [options.maxLines] ring buffer size, default 200
 * @param {Function} [options.now] injectable clock returning a Date
 * @returns {Object} the status/log area API
 */
export function createStatusLog(options = {}) {
  const maxLines = Math.max(1, Math.floor(Number(options.maxLines)) || MAX_LOG_LINES_DEFAULT);
  const now = typeof options.now === 'function' ? options.now : () => new Date();
  const lines = []; // newest first: {at: Date|null, source, message}
  const statuses = new Map(); // source -> text
  let element = null;
  const subscribers = new Set();

  function notify(entry) {
    for (const fn of subscribers) {
      try { fn(entry); } catch (_) { /* a broken subscriber must not break the log */ }
    }
  }

  function formatLine(entry) {
    const prefix = entry.at instanceof Date ? `[${entry.at.toLocaleTimeString()}] ` : '';
    return `${prefix}[${entry.source}] ${entry.message}`;
  }

  function render() {
    if (!element) return;
    const chunks = [];
    if (statuses.size > 0) {
      const parts = [];
      for (const [source, text] of statuses.entries()) parts.push(`${source}=${text}`);
      chunks.push(`[status] ${parts.join(' • ')}`);
    }
    for (const entry of lines) chunks.push(formatLine(entry));
    element.textContent = chunks.length > 0 ? chunks.join('\n') : '';
  }

  return {
    /**
     * Append one log line for one source. Newest line first.
     *
     * @param {string} source
     * @param {string} message
     * @returns {{at: Date, source: string, message: string}} the stored entry
     */
    log(source, message) {
      const entry = {
        at: now(),
        source: String(source == null || source === '' ? 'app' : source),
        message: String(message == null ? '' : message)
      };
      lines.unshift(entry);
      if (lines.length > maxLines) lines.length = maxLines;
      render();
      notify(entry);
      return entry;
    },

    /**
     * Set or clear the status chip of one source. An empty text clears it.
     *
     * @param {string} source
     * @param {string} text
     */
    setStatus(source, text) {
      const key = String(source == null || source === '' ? 'app' : source);
      const value = String(text == null ? '' : text);
      if (value === '') statuses.delete(key);
      else statuses.set(key, value);
      render();
      notify({ status: true, source: key, text: value });
    },

    /** @returns {Object<string, string>} current per-source statuses */
    statuses() {
      return Object.fromEntries(statuses.entries());
    },

    /** @returns {Array<{at: (Date|null), source: string, message: string}>} copy of the buffer */
    lines() {
      return lines.map((entry) => ({ ...entry }));
    },

    /**
     * Attach the one element this area renders into. Lines already written
     * directly into the element (an app that started before the wiring) are
     * adopted into the buffer so nothing is lost; the shipped placeholder is
     * not adopted as history.
     *
     * @param {{textContent: string}} el element-like target
     */
    attach(el) {
      if (!el || typeof el.textContent !== 'string') return;
      for (const raw of el.textContent.split('\n')) {
        const message = raw.trim();
        if (!message || message.startsWith(PLACEHOLDER_PREFIX)) continue;
        lines.unshift({ at: null, source: 'page', message: raw });
      }
      if (lines.length > maxLines) lines.length = maxLines;
      element = el;
      render();
    },

    /** Stop rendering (the buffer and the statuses stay). */
    detach() {
      element = null;
    },

    /** @returns {boolean} whether an element is attached */
    attached() {
      return element !== null;
    },

    /**
     * Subscribe to every appended line and status change. Returns the
     * unsubscribe function. A throwing subscriber is skipped.
     *
     * @param {Function} fn
     * @returns {Function}
     */
    subscribe(fn) {
      if (typeof fn !== 'function') return () => {};
      subscribers.add(fn);
      return () => subscribers.delete(fn);
    },

    /** Render now (the API renders on every change by itself). */
    render
  };
}

// END OF status_log_T104.js
