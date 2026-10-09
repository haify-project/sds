import { useState } from 'react';
import { useQueries, useQuery } from '@tanstack/react-query';
import { api, type ResourceStatus } from '../../services/api';
import { StatBand, StatBandItem } from '@/components/StatBand';
import { SegmentBar } from '@/components/SegmentBar';
import { SegmentedFilter } from '@/components/SegmentedFilter';
import { replicationSummary, syncPollInterval } from '../resources/replication';
import { diskBytes, nodeColumns, runningOn, runsOnReplica, type Guest } from './placement';
import { PlacementMatrix } from './PlacementMatrix';

type Filter = 'all' | 'running' | 'attention' | 'templates' | 'ha';

// The disks a hypervisor keeps on Haify, by guest: which nodes hold a replica
// of each, whether the replicas are in step, and whether the guest runs on a
// node that holds its disks or reaches them over the network. The Proxmox VE
// and KVM pages differ only in how they find their guests.
export function GuestsView({ guests }: { guests: Guest[] }) {
  const [filter, setFilter] = useState<Filter>('all');
  const { data: nodes } = useQuery({ queryKey: ['nodes'], queryFn: () => api.getNodes() });
  const disks = guests.flatMap((g) => g.disks);

  // Same query keys as the Resources page, so the two share one cache.
  const statusQueries = useQueries({
    queries: disks.map((d) => ({
      queryKey: ['resource-status', d.resource.name],
      queryFn: () => api.resourceStatus(d.resource.name),
      refetchInterval: syncPollInterval,
    })),
  });
  const statusOf = new Map<string, ResourceStatus | undefined>(
    disks.map((d, i) => [d.resource.name, statusQueries[i]?.data?.status]),
  );
  const statusRead = statusQueries.every((q) => !q.isLoading);
  const addressOf = new Map((nodes?.nodes ?? []).map((n) => [n.name, n.address]));

  const toneOfDisk = (name: string) => replicationSummary(statusOf.get(name)).tone;
  const vms = guests.filter((g) => !g.template);
  const running = vms.filter((g) => runningOn(g, statusOf));
  const remote = running.filter((g) => !runsOnReplica(g, runningOn(g, statusOf)));
  const inStep = disks.filter((d) => toneOfDisk(d.resource.name) === 'ok').length;
  const needsAttention = (g: Guest) =>
    g.disks.some((d) => ['warn', 'bad'].includes(toneOfDisk(d.resource.name))) || remote.includes(g);

  const shown = guests.filter((g) => {
    if (filter === 'running') return running.includes(g);
    if (filter === 'attention') return needsAttention(g);
    if (filter === 'templates') return g.template;
    if (filter === 'ha') return !!g.ha;
    return true;
  });
  const [capacity, capacityUnit] = splitGiB(disks.reduce((n, d) => n + diskBytes(d.resource), 0));

  const ha = guests.filter((g) => g.ha).length;
  const templates = guests.length - vms.length;

  return (
    <div className="space-y-5">
      <StatBand className="flex-col divide-x-0 divide-y sm:flex-row sm:divide-x sm:divide-y-0">
        <StatBandItem
          label="Running"
          value={running.length}
          unit={`/${vms.length}`}
          loading={!statusRead}
          detail={[
            `${vms.length - running.length} stopped`,
            templates ? plural(templates, 'template') : '',
            ha ? `${ha} under HA` : '',
          ]
            .filter(Boolean)
            .join(' · ')}
        />
        <StatBandItem
          label="On a replica node"
          value={running.length - remote.length}
          unit={`/${running.length}`}
          loading={!statusRead}
          grow={1.3}
          detail={
            remote.length ? (
              <span className="text-status-warn-text">
                {remote.map((g) => g.title).join(', ')}{' '}
                {remote.length === 1 ? 'reads its disks' : 'read their disks'} over the network
              </span>
            ) : (
              'Every running guest has its disks locally'
            )
          }
        />
        <StatBandItem
          label="Replicas in step"
          value={inStep}
          unit={`/${disks.length}`}
          loading={!statusRead}
          grow={1.3}
          detail={<SegmentBar segments={disks.map((d) => toneOfDisk(d.resource.name))} />}
        />
        <StatBandItem
          label="Capacity"
          value={capacity}
          unit={`${capacityUnit} in ${plural(disks.length, 'disk')}`}
          grow={1.2}
        />
      </StatBand>

      <SegmentedFilter
        aria-label="Filter guests"
        className="max-w-full overflow-x-auto [&>button]:shrink-0 [&>button]:whitespace-nowrap"
        value={filter}
        onChange={setFilter}
        options={[
          { value: 'all', label: 'All', count: guests.length },
          { value: 'running', label: 'Running', count: running.length },
          { value: 'attention', label: 'Needs attention', count: guests.filter(needsAttention).length },
          ...(templates ? [{ value: 'templates' as const, label: 'Templates', count: templates }] : []),
          ...(ha ? [{ value: 'ha' as const, label: 'Under HA', count: ha }] : []),
        ]}
      />

      {shown.length ? (
        <PlacementMatrix guests={shown} nodes={nodeColumns(guests)} statusOf={statusOf} addressOf={addressOf} />
      ) : (
        <p className="rounded-lg border border-dashed border-border px-4 py-10 text-center text-sm text-muted-foreground">
          {
            {
              all: '',
              running: 'No guest is running.',
              attention: 'Every running guest is on a replica node and every replica is in step.',
              templates: 'No templates on Haify.',
              ha: 'No guest is under HA.',
            }[filter]
          }
        </p>
      )}
    </div>
  );
}

function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? '' : 's'}`;
}

function splitGiB(bytes: number): [string, string] {
  const gib = bytes / 1024 ** 3;
  if (gib >= 1024) return [(gib / 1024).toFixed(1).replace(/\.0$/, ''), 'TiB'];
  return [gib >= 100 ? gib.toFixed(0) : gib.toFixed(1).replace(/\.0$/, ''), 'GiB'];
}
