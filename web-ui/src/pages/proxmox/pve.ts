import { type Resource } from '../../services/api';

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
  for (const g of byVm.values()) g.disks.sort((a, b) => a.volume.localeCompare(b.volume, undefined, { numeric: true }));
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
