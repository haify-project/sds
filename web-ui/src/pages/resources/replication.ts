import { Resource, ResourceStatus, NodeResourceState } from '../../services/api';
import { type StatusTone } from '@/components/status';

// DRBD replication states that mean an active (or paused) resync is underway.
const SYNC_REPLICATION_STATES = new Set([
  'SyncSource',
  'SyncTarget',
  'PausedSyncS',
  'PausedSyncT',
  'StartingSyncS',
  'StartingSyncT',
  'WFBitMapS',
  'WFBitMapT',
]);

// csiManagedLabel is stamped on every resource the Kubernetes CSI driver
// provisions (see pkg/csi CreateVolume). Such a volume's lifecycle belongs to
// Kubernetes: deleting it here strands the PersistentVolume that still
// references it, so the UI marks it and warns before a manual delete.
const csiManagedLabel = 'sds.csi/managed-by';

export function isCsiManaged(resource: Resource): boolean {
  return resource.labels?.[csiManagedLabel] === 'csi';
}

// The text colour that pairs with each tone. `TONE_SOFT` carries a fill with it,
// which is too loud for a word sitting on the row's own background. Literal
// class strings so the Tailwind scanner still sees all four.
export const TONE_TEXT: Record<StatusTone, string> = {
  ok: 'text-status-ok-text',
  warn: 'text-status-warn-text',
  bad: 'text-status-bad-text',
  idle: 'text-muted-foreground',
};

// isPeerSyncing reports whether a peer node-state represents a resync in
// progress. The local node has no replication relationship (empty
// replicationState) so it never counts as syncing.
export function isPeerSyncing(state?: NodeResourceState): boolean {
  if (!state) return false;
  const rs = state.replicationState ?? '';
  if (SYNC_REPLICATION_STATES.has(rs)) return true;
  // Any non-idle replication state still short of 100% counts as syncing.
  if (rs && rs !== 'Established' && rs !== 'Off' && (state.syncPercent ?? 100) < 100)
    return true;
  return false;
}

// statusHasActiveSync is the adaptive-polling predicate: true while any peer of
// the resource is resyncing, false when everything is idle/UpToDate.
function statusHasActiveSync(status?: ResourceStatus): boolean {
  if (!status) return false;
  return Object.values(status.nodeStates || {}).some(isPeerSyncing);
}

// syncPollInterval returns 2000ms while a resource is resyncing and false
// (stop polling) once it settles, matching the design's smart-polling rule.
export function syncPollInterval(query: {
  state: { data?: unknown };
}): number | false {
  const status = (query.state.data as { status?: ResourceStatus } | undefined)
    ?.status;
  return statusHasActiveSync(status) ? 2000 : false;
}

/** What the Replication column says, and what the row's tick is coloured from. */
export type Replication = {
  tone: StatusTone;
  /** The word beside the bar. Tone never carries the meaning on its own. */
  label: string;
  percent: number;
  title?: string;
};

// replicationSummary condenses a resource's live status into the one line the
// table has room for.
//
// Every role, disk and replication state here comes from `resourceStatus`:
// ListResources deliberately does not run `drbdadm status` on every node, so a
// resource straight out of the list has no node states at all.
export function replicationSummary(status?: ResourceStatus): Replication {
  if (!status) return { tone: 'idle', label: 'unknown', percent: 0 };
  const states = Object.entries(status.nodeStates ?? {});

  const syncing = states.find(([, st]) => isPeerSyncing(st));
  if (syncing) {
    const [host, st] = syncing;
    const pct = Math.min(100, Math.max(0, st.syncPercent ?? 0));
    return {
      tone: 'warn',
      label: `syncing ${pct.toFixed(0)}%`,
      percent: pct,
      title: `${st.replicationState} with ${st.node || host}`,
    };
  }

  if (status.quorum && !status.quorum.hasQuorum)
    return { tone: 'bad', label: 'no quorum', percent: 100 };

  // A tiebreaker or data client is *meant* to be Diskless; only a replica that
  // is not UpToDate is a degradation, so Diskless is not read as one here.
  const degraded = states.find(
    ([, st]) => st.diskState && st.diskState !== 'UpToDate' && st.diskState !== 'Diskless',
  );
  if (degraded)
    return {
      tone: 'bad',
      label: degraded[1].diskState,
      percent: 100,
      title: `on ${degraded[1].node || degraded[0]}`,
    };

  if (states.length === 0) return { tone: 'idle', label: 'unknown', percent: 0 };
  return { tone: 'ok', label: 'UpToDate', percent: 100 };
}

/** What a resource reads as before its status query has answered. */
export const UNKNOWN_REPLICATION: Replication = { tone: 'idle', label: 'unknown', percent: 0 };

// sizeGb crosses the wire as a proto int64, i.e. a JSON *string*; summing it
// without Number() concatenates instead of adding.
export function totalGbOf(resource: Resource): number {
  return resource.volumes.reduce((n, v) => n + Number(v.sizeGb), 0);
}
