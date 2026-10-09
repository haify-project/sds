import { type NodeResourceState, type Resource, type ResourceStatus } from '../../services/api';
import { type StatusTone } from '@/components/status';
import { isPeerSyncing } from '../resources/replication';

// What the Proxmox VE storage plugin (deploy/proxmox) creates, read back from
// the resource list. Every resource it creates carries this label, and its
// name encodes the volume PVE knows it by (SDS/Naming.pm):
//
//   <prefix>-<vmid>-<n>          vm-<vmid>-disk-<n>
//   <prefix>-base-<vmid>-<n>     base-<vmid>-disk-<n>   (a template's disk)
//   <prefix>-<vmid>-<name>       vm-<vmid>-<name>       (cloudinit, state-…, fleece-…)
const pveManagedLabel = 'sds.pve/managed-by';

export function isPveManaged(resource: Resource): boolean {
  return resource.labels?.[pveManagedLabel] === 'pve';
}

export interface PveDisk {
  resource: Resource;
  vmid: number;
  /** The volume name PVE shows, e.g. vm-101-disk-0. */
  volume: string;
  template: boolean;
}

const pveName = /^[a-z0-9_]+-(base-)?(\d+)-([A-Za-z0-9_-]+)$/;

export function pveDiskOf(resource: Resource): PveDisk | null {
  const m = pveName.exec(resource.name);
  if (!m) return null;
  const template = !!m[1];
  const vmid = Number(m[2]);
  const rest = m[3];
  const volume = /^\d+$/.test(rest)
    ? `${template ? 'base' : 'vm'}-${vmid}-disk-${rest}`
    : `vm-${vmid}-${rest}`;
  return { resource, vmid, volume, template };
}

export interface PveGuest {
  vmid: number;
  template: boolean;
  disks: PveDisk[];
}

/** The plugin's disks, one entry per guest, guests and disks in order. */
export function guestsOf(resources: Resource[]): PveGuest[] {
  const byVm = new Map<number, PveGuest>();
  for (const r of resources) {
    if (!isPveManaged(r)) continue;
    const disk = pveDiskOf(r);
    if (!disk) continue;
    const g = byVm.get(disk.vmid) ?? { vmid: disk.vmid, template: false, disks: [] };
    g.template = g.template || disk.template;
    g.disks.push(disk);
    byVm.set(disk.vmid, g);
  }
  // Data disks first, then cloud-init and state volumes.
  const rank = (d: PveDisk) => (/-disk-\d+$/.test(d.volume) ? 0 : 1);
  for (const g of byVm.values())
    g.disks.sort((a, b) => rank(a) - rank(b) || a.volume.localeCompare(b.volume, undefined, { numeric: true }));
  return [...byVm.values()].sort((a, b) => a.vmid - b.vmid);
}

/** Bytes of a disk: the exact size when it has one, else its whole GiB. */
export function diskBytes(resource: Resource): number {
  const v = resource.volumes[0];
  if (!v) return 0;
  const exact = Number(v.sizeBytes ?? 0);
  return exact > 0 ? exact : Number(v.sizeGb) * 1024 ** 3;
}

export function formatGiB(bytes: number): string {
  const gib = bytes / 1024 ** 3;
  if (gib >= 100) return `${gib.toFixed(0)} GiB`;
  if (gib >= 1) return `${gib.toFixed(1).replace(/\.0$/, '')} GiB`;
  return `${(bytes / 1024 ** 2).toFixed(0)} MiB`;
}

/** The guest's page in the Proxmox VE web interface on one of its nodes. */
export function pveGuestUrl(address: string, vmid: number): string {
  return `https://${address}:8006/#v1:0:=qemu%2F${vmid}`;
}

// ---------------------------------------------------------------------------
// Placement: for each disk, what every node holds of it, and where the guest
// runs. The node holding a disk Primary is the node running the guest.
// ---------------------------------------------------------------------------

export type CellKind = 'replica' | 'diskless' | 'none';

export interface PlacementCell {
  kind: CellKind;
  /** For a replica: in step, syncing, behind or failed; idle until read. */
  tone: StatusTone;
  runsHere: boolean;
  /** What the cell means, in words, for its tooltip. */
  title: string;
}

/** The live state of `node` for a resource, matched by SDS node name. */
export function stateOn(status: ResourceStatus | undefined, node: string): NodeResourceState | undefined {
  return Object.entries(status?.nodeStates ?? {}).find(([host, st]) => (st.node || host) === node)?.[1];
}

export function placementCell(disk: PveDisk, status: ResourceStatus | undefined, node: string): PlacementCell {
  const r = disk.resource;
  const st = stateOn(status, node);
  const runsHere = st?.role === 'Primary';
  if (r.nodes.includes(node)) {
    if (!st) return { kind: 'replica', tone: 'idle', runsHere, title: 'Replica, state not read yet' };
    if (isPeerSyncing(st))
      return { kind: 'replica', tone: 'warn', runsHere, title: `Replica, syncing ${Math.round(st.syncPercent ?? 0)}%` };
    if (st.diskState === 'UpToDate') return { kind: 'replica', tone: 'ok', runsHere, title: 'Replica, in step' };
    if (st.diskState === 'Inconsistent')
      return { kind: 'replica', tone: 'warn', runsHere, title: 'Replica, Inconsistent until its sync ends' };
    return { kind: 'replica', tone: 'bad', runsHere, title: `Replica, ${st.diskState || 'unreachable'}` };
  }
  if ((r.disklessNodes ?? []).includes(node) || (r.disklessClients ?? []).includes(node) || st)
    return {
      kind: 'diskless',
      tone: 'idle',
      runsHere,
      title: runsHere ? 'No replica: the guest reads and writes over the network' : 'Diskless: a quorum vote, no replica',
    };
  return { kind: 'none', tone: 'idle', runsHere: false, title: 'Not on this node' };
}

/** A guest's data disks; cloud-init and state volumes are read once or rarely. */
export function dataDisks(guest: PveGuest): PveDisk[] {
  const data = guest.disks.filter((d) => /-disk-\d+$/.test(d.volume));
  return data.length ? data : guest.disks;
}

export function runningOn(guest: PveGuest, statusOf: Map<string, ResourceStatus | undefined>): string | undefined {
  for (const d of guest.disks) {
    const hit = Object.entries(statusOf.get(d.resource.name)?.nodeStates ?? {}).find(([, st]) => st.role === 'Primary');
    if (hit) return hit[1].node || hit[0];
  }
  return undefined;
}

/** Whether a running guest's node holds a replica of every one of its data disks. */
export function runsOnReplica(guest: PveGuest, node: string | undefined): boolean {
  return !!node && dataDisks(guest).every((d) => d.resource.nodes.includes(node));
}

/** Every node any of these disks is on, replica or diskless, by name. */
export function nodeColumns(guests: PveGuest[]): string[] {
  const all = new Set<string>();
  for (const g of guests)
    for (const d of g.disks)
      for (const n of [...d.resource.nodes, ...(d.resource.disklessNodes ?? []), ...(d.resource.disklessClients ?? [])])
        all.add(n);
  return [...all].sort((a, b) => a.localeCompare(b, undefined, { numeric: true }));
}
