import { type Resource } from '../../services/api';
import { type Guest } from './placement';

// libvirt guests on Haify volumes. The libvirt hook (deploy/libvirt) labels
// every resource a guest's disks are on with the guest's name when it starts
// the guest, and `ha create --vm` does when it hands one to drbd-reactor.
export const libvirtDomainLabel = 'haify.libvirt/domain';

export function libvirtDomainOf(resource: Resource): string | undefined {
  return resource.labels?.[libvirtDomainLabel] || undefined;
}

/** One entry per guest, by name; ha names the resources with an HA config. */
export function libvirtGuestsOf(resources: Resource[], ha: Set<string>): Guest[] {
  const byDomain = new Map<string, Resource[]>();
  for (const r of resources) {
    const d = libvirtDomainOf(r);
    if (d) byDomain.set(d, [...(byDomain.get(d) ?? []), r]);
  }
  return [...byDomain.entries()]
    .sort(([a], [b]) => a.localeCompare(b, undefined, { numeric: true }))
    .map(([domain, rs]) => ({
      key: domain,
      title: domain,
      template: false,
      ha: rs.some((r) => ha.has(r.name)),
      disks: rs
        .sort((a, b) => a.name.localeCompare(b.name, undefined, { numeric: true }))
        .map((resource) => ({ resource, label: resource.name, data: true })),
    }));
}
