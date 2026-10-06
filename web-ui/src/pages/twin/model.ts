import type {
  Gateway,
  Node,
  Pool,
  Resource,
  ResourceStatus,
  SelfHaStatus,
} from '@/services/api';
import { toneOf, type StatusTone } from '@/components/status';
import { isPeerSyncing, replicationSummary } from '../resources/replication';

// The 3D view's picture of the cluster, derived from the same API answers the
// other pages read. Everything the scene and the side panel draw comes from
// here, so the two cannot describe the cluster differently.

export type Health = StatusTone;

export interface TwinPool {
  name: string;
  /** 0..1 of the pool really in use: written data on a thin pool. */
  used: number;
  thin: boolean;
  sizeGb: number;
}

export interface TwinReplica {
  resource: string;
  health: Health;
  primary: boolean;
  syncing: boolean;
  /** What a person reads beside it: "UpToDate", "syncing 42%", "Inconsistent". */
  label: string;
}

export interface TwinNode {
  name: string;
  address: string;
  state: string;
  version: string;
  lastSeen: string;
  health: Health;
  controller: boolean;
  pools: TwinPool[];
  /** The fullest pool's fill, 0..1 — what decides whether the node takes writes. */
  fill: number;
  replicas: TwinReplica[];
  tiebreakerOf: string[];
  gateways: TwinGateway[];
  /** Position on the floor. */
  x: number;
  z: number;
}

export interface TwinLinkResource {
  name: string;
  health: Health;
  syncing: boolean;
  /** The node writes flow from, when one of the pair is Primary. */
  from?: string;
  label: string;
}

export interface TwinLink {
  key: string;
  a: string;
  b: string;
  health: Health;
  syncing: boolean;
  wan: boolean;
  resources: TwinLinkResource[];
}

export interface TwinGateway {
  id: string;
  type: string;
  state: string;
  node: string;
  resource: string;
  health: Health;
}

export interface TwinModel {
  nodes: TwinNode[];
  links: TwinLink[];
  tiebreakers: { key: string; node: string; peer: string; resource: string }[];
  gateways: TwinGateway[];
  controllerNode: string;
  vip: string;
  kpi: {
    nodesOnline: number;
    nodes: number;
    resources: number;
    healthy: number;
    syncing: number;
    degraded: number;
    primaries: number;
    gatewaysUp: number;
    gateways: number;
    fill: number;
  };
}

const RANK: Record<Health, number> = { ok: 0, idle: 1, warn: 2, bad: 3 };
export const worst = (a: Health, b: Health): Health => (RANK[b] > RANK[a] ? b : a);

/** A node's own state word as a health: maintenance is not an outage. */
function nodeHealth(state: string): Health {
  const s = state.toLowerCase();
  if (s === 'online') return 'ok';
  if (s === 'maintenance' || s === 'draining') return 'warn';
  if (s === 'offline' || s === 'evicted' || s === 'lost') return 'bad';
  return toneOf(s);
}

function poolFill(p: Pool): number {
  if (p.thinPoolLv && p.thinDataPercent !== undefined) return p.thinDataPercent / 100;
  const total = Number(p.totalBytes ?? 0) || Number(p.totalGb) * 2 ** 30;
  const free = Number(p.freeBytes ?? 0) || Number(p.freeGb) * 2 ** 30;
  return total > 0 ? Math.min(1, Math.max(0, (total - free) / total)) : 0;
}

/** One member's view of a resource: role, disk and whether it is copying. */
function replicaOf(resource: string, node: string, status?: ResourceStatus): TwinReplica {
  const st = Object.entries(status?.nodeStates ?? {}).find(
    ([host, s]) => (s.node || host) === node,
  )?.[1];
  if (!st) return { resource, health: 'idle', primary: false, syncing: false, label: 'unknown' };
  const syncing = isPeerSyncing(st);
  const disk = st.diskState || '';
  const conn = st.replicationState || '';
  let health: Health = 'ok';
  let label = disk || 'unknown';
  if (syncing) {
    health = 'warn';
    label = `syncing ${Math.round(st.syncPercent ?? 0)}%`;
  } else if (disk && disk !== 'UpToDate') {
    health = 'bad';
  } else if (conn && conn !== 'Established' && conn !== 'Off') {
    health = 'bad';
    label = conn;
  }
  return { resource, health, primary: st.role?.toLowerCase() === 'primary', syncing, label };
}

/**
 * Floor positions: a ring for a handful of nodes, rows of six beyond that. The
 * ring reads as "these replicate to each other"; a big cluster would make it
 * too wide to see, so it becomes a grid.
 */
export function layout(n: number): { x: number; z: number }[] {
  if (n === 0) return [];
  if (n === 1) return [{ x: 0, z: 0 }];
  if (n <= 7) {
    const r = Math.max(7.5, n * 2.4);
    return Array.from({ length: n }, (_, i) => {
      const a = Math.PI / 2 + (i * 2 * Math.PI) / n;
      return { x: Math.cos(a) * r * 1.15, z: -Math.sin(a) * r * 0.85 };
    });
  }
  const cols = 6;
  const rows = Math.ceil(n / cols);
  return Array.from({ length: n }, (_, i) => ({
    x: ((i % cols) - (Math.min(cols, n) - 1) / 2) * 12,
    z: (Math.floor(i / cols) - (rows - 1) / 2) * 12,
  }));
}

export function buildModel(
  nodes: Node[],
  pools: Pool[],
  resources: Resource[],
  statuses: Map<string, ResourceStatus | undefined>,
  gateways: Gateway[],
  selfHa?: SelfHaStatus,
): TwinModel {
  const sorted = [...nodes].sort((a, b) => a.name.localeCompare(b.name));
  const nameOf = new Map<string, string>();
  for (const n of sorted) {
    nameOf.set(n.name, n.name);
    nameOf.set(n.address, n.name);
    if (n.hostname) nameOf.set(n.hostname, n.name);
  }
  const controllerNode = selfHa?.enabled ? nameOf.get(selfHa.activeNode) ?? selfHa.activeNode : '';

  const gws: TwinGateway[] = gateways.map((g) => ({
    id: g.id,
    type: g.type.toLowerCase(),
    state: g.state,
    node: nameOf.get(g.node) ?? g.node,
    resource: g.resource,
    health: toneOf(g.state),
  }));

  const pos = layout(sorted.length);
  const twinNodes: TwinNode[] = sorted.map((n, i) => {
    const own = pools.filter((p) => (nameOf.get(p.node) ?? p.node) === n.name);
    const tp = own.map((p) => ({
      name: p.name,
      used: poolFill(p),
      thin: p.thin || Boolean(p.thinPoolLv),
      sizeGb: Number(p.totalGb),
    }));
    return {
      name: n.name,
      address: n.address,
      state: n.state,
      version: n.version,
      lastSeen: n.lastSeen,
      health: nodeHealth(n.state),
      controller: n.name === controllerNode,
      pools: tp,
      fill: tp.reduce((m, p) => Math.max(m, p.used), 0),
      replicas: [],
      tiebreakerOf: [],
      gateways: gws.filter((g) => g.node === n.name),
      x: pos[i].x,
      z: pos[i].z,
    };
  });
  const byName = new Map(twinNodes.map((n) => [n.name, n]));

  const links = new Map<string, TwinLink>();
  const tiebreakers: TwinModel['tiebreakers'] = [];
  let healthy = 0;
  let syncing = 0;
  let degraded = 0;
  let primaries = 0;
  for (const r of resources) {
    const status = statuses.get(r.name);
    const summary = replicationSummary(status);

    const members = r.nodes.filter((m) => byName.has(m));
    const reps = new Map(
      members.map((m) => {
        const rep = replicaOf(r.name, m, status);
        // "Offline" is the controller failing to reach the node over SSH; its
        // DRBD may be replicating perfectly well, and a peer's view of it says
        // so. Only a copy nobody can report on is taken to be down with it.
        const down = byName.get(m)!.health === 'bad' && rep.health === 'idle';
        return [m, down ? { ...rep, health: 'bad' as Health, label: 'node down' } : rep];
      }),
    );
    let resHealth: Health = summary.tone;
    for (const rep of reps.values()) resHealth = worst(resHealth, rep.health === 'idle' ? 'ok' : rep.health);
    for (const [m, rep] of reps) {
      byName.get(m)!.replicas.push(rep);
      if (rep.primary) primaries++;
    }
    if (resHealth === 'ok') healthy++;
    else if (resHealth === 'warn') syncing++;
    else if (resHealth === 'bad') degraded++;
    const primary = members.find((m) => reps.get(m)!.primary);
    for (let i = 0; i < members.length; i++) {
      for (let j = i + 1; j < members.length; j++) {
        const [a, b] = [members[i], members[j]].sort();
        const key = `${a}|${b}`;
        const link =
          links.get(key) ??
          { key, a, b, health: 'ok' as Health, syncing: false, wan: false, resources: [] };
        const ra = reps.get(a)!;
        const rb = reps.get(b)!;
        const health = worst(ra.health, rb.health);
        const sync = ra.syncing || rb.syncing;
        const wan = Boolean(r.wanMode) && (a === r.drNode || b === r.drNode);
        link.resources.push({
          name: r.name,
          health,
          syncing: sync,
          from: primary === a || primary === b ? primary : undefined,
          label: sync ? (ra.syncing ? ra.label : rb.label) : health === 'ok' ? 'in sync' : worseOf(ra, rb).label,
        });
        link.health = worst(link.health, health);
        link.syncing ||= sync;
        link.wan ||= wan;
        links.set(key, link);
      }
    }
    for (const t of r.disklessNodes ?? []) {
      const tn = byName.get(t);
      if (!tn) continue;
      tn.tiebreakerOf.push(r.name);
      for (const m of members) tiebreakers.push({ key: `${r.name}|${t}|${m}`, node: t, peer: m, resource: r.name });
    }
  }

  const online = twinNodes.filter((n) => n.health === 'ok').length;
  return {
    nodes: twinNodes,
    links: [...links.values()],
    tiebreakers,
    gateways: gws,
    controllerNode,
    vip: selfHa?.enabled ? selfHa.vip : '',
    kpi: {
      nodesOnline: online,
      nodes: twinNodes.length,
      resources: resources.length,
      healthy,
      syncing,
      degraded,
      primaries,
      gatewaysUp: gws.filter((g) => g.health === 'ok').length,
      gateways: gws.length,
      fill: twinNodes.reduce((m, n) => Math.max(m, n.fill), 0),
    },
  };
}

const worseOf = (a: TwinReplica, b: TwinReplica): TwinReplica => (RANK[a.health] >= RANK[b.health] ? a : b);
