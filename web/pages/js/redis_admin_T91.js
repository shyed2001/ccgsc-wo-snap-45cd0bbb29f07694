// Path: web/pages/js/redis_admin_T91.js | Author: Codex | 2026-11-20
// T91: observed Redis state stays separate from desired, versioned settings.
export const REDIS_PAGES = ['thread-pool-admin', 'process-pool-admin'];

// Owner MCQ 2026-11-20 (R42.8): no Redis admin user yet, so the page only stores the
// choice; this text tells the operator how to apply it on the VPS by hand.
export const REDIS_OPS_HELP = 'Read-only mode: Save stores your choice only; Redis is changed by hand on the VPS (root). ' +
  'Run: export REDISCLI_AUTH=$(cat /root/ccgsc_secrets/redis_default.pw); then ' +
  'redis-cli --no-auth-warning CONFIG SET maxmemory 2gb (fixed cap, 1gb..4gb; dynamic = 10..40 % of MemAvailable, re-run by hand or cron); ' +
  'AOF: CONFIG SET appendonly yes and CONFIG SET appendfsync everysec; snapshots only: CONFIG SET appendonly no. ' +
  'Policy stays noeviction. Then set the same lines (maxmemory, appendonly, appendfsync) at the end of /etc/redis/redis.conf so a restart keeps them.';

export function redisApplyNote(status) {
  return status && status.enabled
    ? 'Shared by both pools and Node apps. Saving applies server-wide immediately; snapshot keeps existing save rules.'
    : REDIS_OPS_HELP;
}

export function redisStatusText(status) {
  if (!status) return 'Redis status unavailable; controls are read-only.';
  const ownership = status.enabled ? status.message : 'applied by ops, not by pools-core';
  const current = status.current;
  if (!current) return `${ownership}. Current Redis cap and persistence unavailable.`;
  return `${ownership}. Current Redis cap: ${current.maxMemoryBytes} bytes (${(current.maxMemoryBytes / 2 ** 30).toFixed(2)} GiB); persistence: ${current.persistence}; policy: ${current.policy}.`;
}
