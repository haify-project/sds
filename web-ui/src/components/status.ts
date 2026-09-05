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

/**
 * Tone → class, defined once. The badge, the tick and the segment bar all drew
 * their own byte-identical copy of this map, which is three places for "warn"
 * to quietly become a different yellow. Class strings stay literal so the
 * Tailwind v4 scanner still sees every one of them.
 */
export const TONE_BG: Record<StatusTone, string> = {
  ok: 'bg-status-ok',
  warn: 'bg-status-warn',
  bad: 'bg-status-bad',
  idle: 'bg-status-idle',
};

/**
 * Tone → text colour alone. `TONE_SOFT` bundles a tinted fill with its readable
 * text colour; a status word rendered as bare text on a row needs the text half
 * on its own. Same `-text` tokens, so a word and a badge cannot drift apart.
 */
export const TONE_TEXT: Record<StatusTone, string> = {
  ok: 'text-status-ok-text',
  warn: 'text-status-warn-text',
  bad: 'text-status-bad-text',
  idle: 'text-status-idle-text',
};

/**
 * Tinted fill plus the text colour that is actually readable on it — the
 * `-text` tokens, not the fill colours, which fail AA over their own 12% tint
 * in light mode.
 */
export const TONE_SOFT: Record<StatusTone, string> = {
  ok: 'bg-status-ok-soft text-status-ok-text',
  warn: 'bg-status-warn-soft text-status-warn-text',
  bad: 'bg-status-bad-soft text-status-bad-text',
  idle: 'bg-status-idle-soft text-status-idle-text',
};
