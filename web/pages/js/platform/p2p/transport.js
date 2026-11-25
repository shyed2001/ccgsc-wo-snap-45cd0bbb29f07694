// F11, 2026-11-24: isolated artifact transport, NOT wired into contributor runtime.
// No signaling, peer discovery, TURN, result/receipt path or arbitrary URL API.
// The trusted host must implement server-backed authorize(scope, action, signal)
// and provide an already-authorized, reliable ordered channel bound to that scope.
// A digest, local cache hit or caller-supplied scope is NOT an authorization grant.
import { MAX_TRANSFER_BYTES, CHUNK_BYTES, BUFFER_HWM_BYTES, BUFFER_RESUME_BYTES } from './rules.js';

export const LIMITS = Object.freeze({ maxBytes: MAX_TRANSFER_BYTES, chunkBytes: CHUNK_BYTES,
  peerMs: 3000, httpMs: 10000, cacheMs: 60000, cacheEntries: 4,
  cacheBytes: MAX_TRANSFER_BYTES, serveBytesPerMinute: 64 * 1024 * 1024 });
const prefixes = Object.freeze({ tp: '/thread-pool/v1', pp: '/process-pool/v1' });
const hex = /^[0-9a-f]{64}$/;
const fail = reason => new Error(reason);
const close = channel => { try { channel?.close(); } catch { /* already gone */ } };
const capTime = (value, max) => Number.isSafeInteger(value) && value > 0 ? Math.min(value, max) : max;
const keyOf = s => JSON.stringify([s.poolId, s.ownerId, s.appId, s.artifactId]);
const validSize = n => Number.isSafeInteger(n) && n > 0 && n <= LIMITS.maxBytes;

function scopeOf(raw) {
  const s = Object.fromEntries(['poolId', 'ownerId', 'appId', 'artifactId', 'workerId', 'leaseId', 'kind', 'expiresAt']
    .map(k => [k, raw?.[k]]));
  if (!Object.hasOwn(prefixes, s.poolId) || s.kind !== 'module' || !hex.test(s.artifactId)
    || !Number.isSafeInteger(s.expiresAt) || s.expiresAt <= Date.now()
    || ['ownerId', 'appId', 'workerId', 'leaseId'].some(k => typeof s[k] !== 'string'
      || s[k].length < 1 || s[k].length > 256 || /[\x00-\x1f]/.test(s[k]))) throw fail('scope');
  return Object.freeze(s);
}
function live(s, signal) {
  if (signal?.aborted) throw fail('aborted');
  if (s.expiresAt <= Date.now()) throw fail('expired');
}
async function digest(bytes) {
  if (!globalThis.crypto?.subtle) throw fail('crypto-unavailable');
  return Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', bytes)), b => b.toString(16).padStart(2, '0')).join('');
}
function channelOK(c) {
  if (!c || c.readyState !== 'open' || c.ordered !== true || c.maxRetransmits != null
    || c.maxPacketLifeTime != null) throw fail('channel');
  c.binaryType = 'arraybuffer';
}
function control(data) {
  if (typeof data !== 'string' || data.length > 256) throw fail('protocol');
  let v; try { v = JSON.parse(data); } catch { throw fail('protocol'); }
  if (!v || v.v !== 1) throw fail('protocol');
  return v;
}
// Deadline also bounds trusted adapters; a late channel is closed by the caller.
async function bounded(ms, parent, work) {
  const ac = new AbortController();
  const abort = () => ac.abort();
  if (parent?.aborted) ac.abort(); else parent?.addEventListener('abort', abort, { once: true });
  let timer, onAbort;
  const stop = new Promise((_, reject) => {
    onAbort = () => reject(fail('aborted'));
    ac.signal.addEventListener('abort', onAbort, { once: true });
    timer = setTimeout(() => { reject(fail('timeout')); ac.abort(); }, ms);
  });
  try {
    if (ac.signal.aborted) throw fail('aborted');
    return await Promise.race([stop, work(ac.signal)]);
  } finally {
    clearTimeout(timer); parent?.removeEventListener('abort', abort);
    ac.signal.removeEventListener('abort', onAbort); ac.abort();
  }
}
// A single response on a dedicated channel. No peer metadata controls allocation
// until the header is validated. Exact chunk lengths bound both memory and events.
export async function receiveArtifact(channel, expected, { signal, timeoutMs } = {}) {
  try {
    channelOK(channel); if (!hex.test(expected)) throw fail('digest');
    return await bounded(capTime(timeoutMs, LIMITS.peerMs), signal, async signal => {
      const bytes = await new Promise((resolve, reject) => {
        let buffer, offset = 0, ended = false;
        const cleanup = () => {
          channel.removeEventListener('message', message); channel.removeEventListener('close', dropped);
          channel.removeEventListener('error', dropped); signal.removeEventListener('abort', aborted);
        };
        const done = (error, value) => { if (ended) return; ended = true; cleanup(); error ? reject(error) : resolve(value); };
        const dropped = () => done(fail('dropped-peer'));
        const aborted = () => done(fail('aborted'));
        const message = event => {
          try {
            if (event.data instanceof ArrayBuffer) {
              if (!buffer || event.data.byteLength !== Math.min(LIMITS.chunkBytes, buffer.length - offset)
                || event.data.byteLength === 0) throw fail('protocol');
              buffer.set(new Uint8Array(event.data), offset); offset += event.data.byteLength; return;
            }
            const m = control(event.data);
            if (!buffer && m.type === 'artifact' && m.sha256 === expected) {
              if (!validSize(m.size)) throw fail('size');
              buffer = new Uint8Array(m.size); return;
            }
            if (m.type === 'end' && buffer && offset === buffer.length) { done(null, buffer); return; }
            throw fail('protocol');
          } catch (error) { done(error); }
        };
        channel.addEventListener('message', message); channel.addEventListener('close', dropped);
        channel.addEventListener('error', dropped); signal.addEventListener('abort', aborted, { once: true });
        try { channel.send(JSON.stringify({ v: 1, type: 'get', sha256: expected })); } catch { dropped(); }
      });
      if (await digest(bytes) !== expected) throw fail('hash');
      if (signal.aborted) throw fail('aborted');
      channel.send(JSON.stringify({ v: 1, type: 'ack' }));
      return bytes;
    });
  } finally { close(channel); }
}

function waitControl(channel, type, signal, expected) {
  return new Promise((resolve, reject) => {
    const cleanup = () => {
      channel.removeEventListener('message', message); channel.removeEventListener('close', dropped);
      channel.removeEventListener('error', dropped); signal.removeEventListener('abort', dropped);
    };
    const done = error => { cleanup(); error ? reject(error) : resolve(); };
    const dropped = () => done(fail('dropped-peer'));
    const message = event => { try {
      const m = control(event.data);
      if (m.type !== type || (expected && m.sha256 !== expected)) throw fail('protocol');
      done();
    } catch (e) { done(e); } };
    channel.addEventListener('message', message); channel.addEventListener('close', dropped);
    channel.addEventListener('error', dropped); signal.addEventListener('abort', dropped, { once: true });
    if (signal.aborted || channel.readyState !== 'open') dropped();
  });
}
function drain(channel, signal) {
  if (channel.bufferedAmount <= BUFFER_RESUME_BYTES) return Promise.resolve();
  return new Promise((resolve, reject) => {
    const cleanup = () => {
      channel.removeEventListener('bufferedamountlow', low); channel.removeEventListener('close', stop);
      channel.removeEventListener('error', stop); signal.removeEventListener('abort', stop);
    };
    const low = () => { cleanup(); resolve(); };
    const stop = () => { cleanup(); reject(fail('dropped-peer')); };
    channel.bufferedAmountLowThreshold = BUFFER_RESUME_BYTES;
    channel.addEventListener('bufferedamountlow', low); channel.addEventListener('close', stop);
    channel.addEventListener('error', stop); signal.addEventListener('abort', stop, { once: true });
    if (signal.aborted || channel.readyState !== 'open') stop();
    else if (channel.bufferedAmount <= BUFFER_RESUME_BYTES) low();
  });
}

/** Inert unless explicitly enabled AND consent() === true. Default auth denies.
 * authorize is a trusted integration seam, not a substitute for server validation.
 * Both peers need separate grants; openPeer must bind the returned channel to the
 * authenticated pair + exact artifact. Tokens stay exclusively in the HTTP header.
 */
export function createArtifactPeerClient(options = {}) {
  const origin = new URL(options.origin || globalThis.location?.origin);
  if (origin.protocol !== 'https:' || origin.username || origin.password || origin.search || origin.hash
    || origin.pathname !== '/') throw fail('origin');
  const fetcher = options.fetch || globalThis.fetch;
  const authorize = options.authorize;
  const consent = () => options.enabled === true && options.consent?.() === true;
  const peerMs = capTime(options.peerTimeoutMs, LIMITS.peerMs);
  const httpMs = capTime(options.httpTimeoutMs, LIMITS.httpMs);
  const entries = new Map(); let stored = 0, busy = false, serveUsed = 0, windowStart = Date.now();
  function prune() {
    for (const [k, e] of entries) if (e.until <= Date.now()) { stored -= e.bytes.length; entries.delete(k); }
  }
  function remember(s, bytes) {
    prune(); const k = keyOf(s);
    if (entries.has(k)) { stored -= entries.get(k).bytes.length; entries.delete(k); }
    while (entries.size >= LIMITS.cacheEntries || stored + bytes.length > LIMITS.cacheBytes) {
      const oldest = entries.keys().next().value; stored -= entries.get(oldest).bytes.length; entries.delete(oldest);
    }
    entries.set(k, { bytes: bytes.slice(), until: Math.min(s.expiresAt, Date.now() + LIMITS.cacheMs) }); stored += bytes.length;
  }
  async function permit(s, action, signal) {
    live(s, signal);
    if (typeof authorize !== 'function' || await authorize(s, action, signal) !== true) throw fail('unauthorized');
    live(s, signal);
  }
  async function http(s, token, parent) {
    return bounded(httpMs, parent, async signal => {
      await permit(s, 'http', signal);
      if (typeof token !== 'string' || !token || /[\r\n]/.test(token)) throw fail('token');
      const response = await fetcher(origin.origin + prefixes[s.poolId] + '/artifacts/' + s.artifactId, {
        method: 'GET', headers: { Authorization: 'Bearer ' + token }, signal,
        credentials: 'omit', redirect: 'error', cache: 'no-store', referrerPolicy: 'no-referrer'
      });
      let reader;
      try {
        live(s, signal);
        if (!response.ok || response.redirected) throw fail('http');
        if (response.headers.get('Content-Type')?.split(';')[0].trim().toLowerCase() !== 'application/octet-stream') throw fail('mime');
        if (response.headers.get('X-Artifact-Sha256') !== s.artifactId) throw fail('server-digest');
        const length = response.headers.get('Content-Length');
        if (length !== null && (!/^\d+$/.test(length) || !validSize(Number(length)))) throw fail('size');
        reader = response.body?.getReader(); if (!reader) throw fail('stream');
        const abort = () => { void reader.cancel().catch(() => {}); };
        signal.addEventListener('abort', abort, { once: true });
        try {
          const chunks = []; let size = 0;
          while (true) {
            const next = await reader.read(); live(s, signal); if (next.done) break;
            if (!(next.value instanceof Uint8Array) || next.value.length === 0) throw fail('stream');
            size += next.value.length; if (!validSize(size)) throw fail('size');
            // Coalesce in a bounded buffer: no per-byte array-of-chunks overhead.
            if (chunks.length === 0) chunks.push(new Uint8Array(LIMITS.maxBytes));
            chunks[0].set(next.value, size - next.value.length);
          }
          if (!validSize(size) || (length !== null && size !== Number(length))) throw fail('size');
          const bytes = chunks[0].slice(0, size);
          if (await digest(bytes) !== s.artifactId) throw fail('hash');
          live(s, signal); remember(s, bytes); return bytes;
        } finally { signal.removeEventListener('abort', abort); }
      } finally {
        // Do not let a hostile stream's cancellation promise hold up fallback.
        if (reader) { void reader.cancel().catch(() => {}); }
        else { void response.body?.cancel().catch(() => {}); }
      }
    });
  }
  return Object.freeze({
    async get(raw, { token, openPeer, signal } = {}) {
      if (busy) throw fail('busy'); const s = scopeOf(raw); busy = true;
      let fallbackReason = 'disabled';
      try {
        live(s, signal);
        if (consent() && typeof openPeer === 'function') {
          let channel;
          try {
            const bytes = await bounded(peerMs, signal, async deadline => {
              await permit(s, 'receive', deadline);
              channel = await openPeer(s, deadline);
              if (deadline.aborted) { close(channel); throw fail('aborted'); }
              live(s, deadline);
              const bytes = await receiveArtifact(channel, s.artifactId, { signal: deadline, timeoutMs: peerMs });
              live(s, deadline); return bytes;
            });
            // Peer bytes are returned, NEVER admitted to the server provenance cache.
            return { bytes, source: 'peer', fallbackReason: null };
          } catch (e) { fallbackReason = ['timeout', 'hash', 'size', 'protocol', 'unauthorized', 'dropped-peer'].includes(e.message) ? e.message : 'peer-unavailable'; }
          finally { close(channel); }
        }
        live(s, signal);
        return { bytes: await http(s, token, signal), source: 'server', fallbackReason };
      } finally { busy = false; }
    },
    async serve(raw, channel, { signal } = {}) {
      let acquired = false;
      try {
        if (!consent()) throw fail('disabled');
        if (busy) throw fail('busy'); busy = true; acquired = true;
        const s = scopeOf(raw); channelOK(channel);
        return await bounded(peerMs, signal, async deadline => {
          // Install the request listener before async authorization to avoid races.
          const request = waitControl(channel, 'get', deadline, s.artifactId);
          void request.catch(() => {});
          await permit(s, 'serve', deadline); prune();
          const entry = entries.get(keyOf(s)); if (!entry) throw fail('cache-miss');
          const bytes = entry.bytes.slice();
          if (await digest(bytes) !== s.artifactId) throw fail('hash');
          await request; live(s, deadline); if (!consent()) throw fail('disabled');
          if (Date.now() - windowStart >= 60000) { windowStart = Date.now(); serveUsed = 0; }
          if (serveUsed + bytes.length > LIMITS.serveBytesPerMinute) throw fail('budget');
          serveUsed += bytes.length;
          const ack = waitControl(channel, 'ack', deadline); void ack.catch(() => {});
          channel.send(JSON.stringify({ v: 1, type: 'artifact', sha256: s.artifactId, size: bytes.length }));
          for (let offset = 0; offset < bytes.length; offset += LIMITS.chunkBytes) {
            live(s, deadline); if (!consent()) throw fail('disabled');
            if (channel.bufferedAmount + LIMITS.chunkBytes > BUFFER_HWM_BYTES) await drain(channel, deadline);
            live(s, deadline);
            channel.send(bytes.slice(offset, offset + LIMITS.chunkBytes).buffer);
          }
          channel.send(JSON.stringify({ v: 1, type: 'end' })); await ack;
          return { bytes: bytes.length };
        });
      } finally { close(channel); if (acquired) busy = false; }
    },
    clear() { entries.clear(); stored = 0; }
  });
}
