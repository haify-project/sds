import { type HealthInfo } from '@/services/api';
import { type StatusTone } from '@/components/status';

export function formatLastSeen(lastSeen: string): string {
  const ts = Number(lastSeen);
  if (!ts) return '-';
  return new Date(ts * 1000).toLocaleString();
}

/** "41s ago" scans in a column; a locale timestamp does not. The absolute
 *  value stays reachable as the cell's title and in the expanded facts. */
export function formatAge(lastSeen: string): string {
  const ts = Number(lastSeen);
  if (!ts) return '-';
  const secs = Math.floor(Date.now() / 1000 - ts);
  if (secs < 0) return 'now';
  if (secs < 5) return 'now';
  if (secs < 60) return `${secs}s ago`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
  return `${Math.floor(secs / 86400)}d ago`;
}

export type Readiness = {
  dots: { label: string; tone: StatusTone; word: string }[];
  word: string;
  tone: StatusTone;
};

/**
 * What the promoter's start chain needs, reduced to three dots and one word.
 * The word is not optional: the dots repeat it, they never carry it alone.
 * Ordering is worst-first — a missing DRBD is the answer even if the reactor
 * is also stopped.
 */
export function readinessOf(h: HealthInfo): Readiness {
  // Each dot states its own condition. The summary word below is worst-first,
  // so on a node with two problems it names only one — the other would exist
  // as a coloured pixel and nothing else without these.
  const dots: Readiness['dots'] = [
    {
      label: 'DRBD',
      tone: h.drbdInstalled ? 'ok' : 'bad',
      word: h.drbdInstalled ? 'installed' : 'missing',
    },
    {
      label: 'drbd-reactor',
      tone: !h.drbdReactorInstalled ? 'bad' : h.drbdReactorRunning ? 'ok' : 'warn',
      word: !h.drbdReactorInstalled ? 'missing' : h.drbdReactorRunning ? 'running' : 'stopped',
    },
    {
      label: 'resource agents',
      tone: h.resourceAgentsInstalled ? 'ok' : 'bad',
      word: h.resourceAgentsInstalled ? 'installed' : 'missing',
    },
  ];
  if (!h.drbdInstalled) return { dots, word: 'DRBD missing', tone: 'bad' };
  if (!h.drbdReactorInstalled) return { dots, word: 'reactor missing', tone: 'bad' };
  if (!h.resourceAgentsInstalled) return { dots, word: 'agents missing', tone: 'bad' };
  if (!h.drbdReactorRunning) return { dots, word: 'reactor stopped', tone: 'warn' };
  return { dots, word: 'ready', tone: 'ok' };
}

/** A checked node's result plus when it was taken — the expanded row states the
 *  time, because a health check is a reading, not a live property. */
export type HealthResult = { info: HealthInfo; at: number; stale?: boolean };
