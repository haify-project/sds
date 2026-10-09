import { type Resource } from '../../services/api';
import { type Guest, type GuestDisk } from '../guests/placement';

// What the Proxmox VE storage plugin (deploy/proxmox) creates, read back from
// the resource list. Every resource it creates carries this label, and its
// name encodes the volume PVE knows it by (Haify/Naming.pm):
//
//   <prefix>-<vmid>-<n>          vm-<vmid>-disk-<n>
//   <prefix>-base-<vmid>-<n>     base-<vmid>-disk-<n>   (a template's disk)
//   <prefix>-<vmid>-<name>       vm-<vmid>-<name>       (cloudinit, state-…, fleece-…)
const pveManagedLabel = 'haify.pve/managed-by';

export function isPveManaged(resource: Resource): boolean {
  return resource.labels?.[pveManagedLabel] === 'pve';
}

interface PveDisk extends GuestDisk {
  vmid: number;
  template: boolean;
}

const pveName = /^[a-z0-9_]+-(base-)?(\d+)-([A-Za-z0-9_-]+)$/;

export function pveDiskOf(resource: Resource): PveDisk | null {
  const m = pveName.exec(resource.name);
  if (!m) return null;
  const template = !!m[1];
  const vmid = Number(m[2]);
  const rest = m[3];
  const label = /^\d+$/.test(rest) ? `${template ? 'base' : 'vm'}-${vmid}-disk-${rest}` : `vm-${vmid}-${rest}`;
  return { resource, vmid, label, template, data: /-disk-\d+$/.test(label) };
}

/** The guest's page in the Proxmox VE web interface on one of its nodes. */
export function pveGuestUrl(address: string, vmid: number): string {
  return `https://${address}:8006/#v1:0:=qemu%2F${vmid}`;
}

/** The plugin's disks, one entry per guest, guests and disks in order. */
export function guestsOf(resources: Resource[]): Guest[] {
  const byVm = new Map<number, { template: boolean; disks: PveDisk[] }>();
  for (const r of resources) {
    if (!isPveManaged(r)) continue;
    const disk = pveDiskOf(r);
    if (!disk) continue;
    const g = byVm.get(disk.vmid) ?? { template: false, disks: [] };
    g.template = g.template || disk.template;
    g.disks.push(disk);
    byVm.set(disk.vmid, g);
  }
  return [...byVm.entries()]
    .sort(([a], [b]) => a - b)
    .map(([vmid, g]) => ({
      key: String(vmid),
      title: `${g.template ? 'Template' : 'VM'} ${vmid}`,
      template: g.template,
      // Data disks first, then cloud-init and state volumes.
      disks: g.disks.sort(
        (a, b) => Number(b.data) - Number(a.data) || a.label.localeCompare(b.label, undefined, { numeric: true }),
      ),
      link: { label: 'Open in Proxmox VE', url: (address: string) => pveGuestUrl(address, vmid) },
    }));
}
