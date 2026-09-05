/**
 * Cluster status → tone, shared by every status-carrying component (badge,
 * tick, segment bar). One list, so "online" in a table and "online" in a stat
 * band cannot drift apart. DRBD and systemd hand us these words in mixed case,
 * hence the lowercasing.
 */
export type StatusTone = 'ok' | 'warn' | 'bad' | 'idle';

const GREEN = ['online', 'running', 'active', 'primary', 'uptodate', 'connected', 'healthy', 'enabled'];
const YELLOW = ['secondary', 'syncing', 'inconsistent', 'degraded', 'standby'];
const RED = ['offline', 'stopped', 'inactive', 'failed', 'error', 'diskless', 'disconnected'];

export function toneOf(status: string | undefined): StatusTone {
  const s = (status || '').toLowerCase();
  if (GREEN.includes(s)) return 'ok';
  if (YELLOW.includes(s)) return 'warn';
  if (RED.includes(s)) return 'bad';
  // Anything we do not recognise is "we don't know", not "bad" — an unknown
  // word must never render as a red alarm.
  return 'idle';
}
