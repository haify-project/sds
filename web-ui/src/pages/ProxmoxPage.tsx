import { useQueries, useQuery } from '@tanstack/react-query';
import { api, type ResourceStatus } from '../services/api';
import { PageHeader } from '@/components/PageHeader';
import { Card, CardContent } from '@/components/ui/card';
import { Badge } from '@/components/ui/badge';
import { Skeleton } from '@/components/ui/skeleton';
import { toneOf, TONE_BG } from '@/components/status';
import { cn } from '@/lib/utils';
import { ExternalLink, Monitor } from 'lucide-react';
import { replicationSummary, syncPollInterval, TONE_TEXT } from './resources/replication';
import { diskBytes, formatGiB, guestsOf, pveGuestUrl, type PveDisk, type PveGuest } from './proxmox/pve';

// The disks Proxmox VE keeps on SDS, by guest: where each one is replicated,
// whether its copies are in step, and which node the guest runs on (the one
// holding its disks Primary).
export function ProxmoxPage() {
  const { data: resources, isLoading } = useQuery({
    queryKey: ['resources'],
    queryFn: () => api.getResources(),
  });
  const { data: nodes } = useQuery({ queryKey: ['nodes'], queryFn: () => api.getNodes() });

  const guests = guestsOf(resources?.resources ?? []);
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
  const addressOf = new Map((nodes?.nodes ?? []).map((n) => [n.name, n.address]));

  const totalBytes = disks.reduce((n, d) => n + diskBytes(d.resource), 0);
  const unsynced = disks.filter((d) => replicationSummary(statusOf.get(d.resource.name)).tone !== 'ok').length;

  return (
    <div>
      <PageHeader
        title="Proxmox VE"
        description={
          <>
            <span className="font-mono tabular-nums text-foreground">{guests.length}</span> guests ·{' '}
            <span className="font-mono tabular-nums text-foreground">{disks.length}</span> disks ·{' '}
            <span className="font-mono tabular-nums text-foreground">{formatGiB(totalBytes)}</span>
            {unsynced > 0 ? (
              <>
                {' '}
                · <span className="font-mono tabular-nums text-status-warn-text">{unsynced}</span> not in step
              </>
            ) : null}
          </>
        }
      />

      {isLoading ? (
        <div className="space-y-3">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-28 w-full" />
          ))}
        </div>
      ) : !guests.length ? (
        <Card>
          <CardContent className="flex flex-col items-center justify-center gap-3 py-16 text-center">
            <Monitor className="h-8 w-8 text-muted-foreground" />
            <p className="max-w-md text-sm text-muted-foreground">
              No Proxmox VE disks on SDS yet. Connect a PVE cluster by running this on one of its nodes, then
              create disks on the storage it adds:
            </p>
            <code className="rounded bg-muted px-3 py-2 font-mono text-xs">
              ./deploy/proxmox/bootstrap.sh --devices /dev/sdb --vip &lt;free address&gt;/24
            </code>
          </CardContent>
        </Card>
      ) : (
        <div className="space-y-3">
          {guests.map((g) => (
            <GuestCard key={g.vmid} guest={g} statusOf={statusOf} addressOf={addressOf} />
          ))}
        </div>
      )}
    </div>
  );
}

function GuestCard({
  guest,
  statusOf,
  addressOf,
}: {
  guest: PveGuest;
  statusOf: Map<string, ResourceStatus | undefined>;
  addressOf: Map<string, string>;
}) {
  // The node holding a disk Primary is the node running the guest.
  const runningOn = guest.disks
    .map((d) => primaryOf(statusOf.get(d.resource.name)))
    .find((n): n is string => !!n);
  const linkNode = runningOn ?? guest.disks[0]?.resource.nodes[0];
  const linkAddr = linkNode ? addressOf.get(linkNode) : undefined;

  return (
    <Card className="gap-0 py-0">
      <CardContent className="p-0">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b px-4 py-3">
          <span className="font-medium">
            {guest.template ? 'Template' : 'VM'} <span className="font-mono tabular-nums">{guest.vmid}</span>
          </span>
          <span className="text-sm text-muted-foreground">
            {runningOn ? (
              <>
                running on <span className="text-foreground">{runningOn}</span>
              </>
            ) : guest.template ? (
              'not running'
            ) : (
              'stopped'
            )}
          </span>
          {linkAddr ? (
            <a
              className="ml-auto inline-flex items-center gap-1 text-sm text-primary hover:underline"
              href={pveGuestUrl(linkAddr, guest.vmid)}
              target="_blank"
              rel="noreferrer"
            >
              Open in Proxmox VE
              <ExternalLink className="h-3.5 w-3.5" />
            </a>
          ) : null}
        </div>
        <ul className="divide-y">
          {guest.disks.map((d) => (
            <DiskRow key={d.resource.name} disk={d} status={statusOf.get(d.resource.name)} />
          ))}
        </ul>
      </CardContent>
    </Card>
  );
}

function DiskRow({ disk, status }: { disk: PveDisk; status?: ResourceStatus }) {
  const r = disk.resource;
  const replication = replicationSummary(status);
  const stateOf = (node: string) =>
    Object.entries(status?.nodeStates ?? {}).find(([host, st]) => (st.node || host) === node)?.[1];
  const diskless = [...(r.disklessNodes ?? []), ...(r.disklessClients ?? [])];

  return (
    <li className="grid gap-x-4 gap-y-1 px-4 py-2.5 text-sm sm:grid-cols-[minmax(11rem,1.2fr)_5rem_minmax(0,2fr)_8rem] sm:items-center">
      <div className="min-w-0">
        <div className="truncate font-mono text-[13px]">{disk.volume}</div>
        <div className="truncate font-mono text-xs text-muted-foreground">{r.name}</div>
      </div>
      <div className="font-mono tabular-nums text-muted-foreground">{formatGiB(diskBytes(r))}</div>
      <div className="flex flex-wrap items-center gap-1.5">
        {r.nodes.map((n) => {
          const st = stateOf(n);
          const tone = st ? toneOf(st.diskState) : 'idle';
          return (
            <Badge
              key={n}
              variant="outline"
              className="gap-1.5 bg-card font-normal"
              title={st ? `${st.diskState}${st.replicationState ? `, ${st.replicationState}` : ''}` : 'state not read yet'}
            >
              <span className={cn('h-1.5 w-1.5 rounded-full', TONE_BG[tone])} />
              {n}
              {st?.role === 'Primary' ? <span className="text-muted-foreground">Primary</span> : null}
            </Badge>
          );
        })}
        {diskless.length ? (
          <span className="text-xs text-muted-foreground" title="Reach the disk over the network, without a copy">
            + {diskless.join(', ')} without a copy
          </span>
        ) : null}
      </div>
      <div className={cn('text-sm', TONE_TEXT[replication.tone])} title={replication.title}>
        {replication.label}
      </div>
    </li>
  );
}

function primaryOf(status?: ResourceStatus): string | undefined {
  const hit = Object.entries(status?.nodeStates ?? {}).find(([, st]) => st.role === 'Primary');
  return hit ? hit[1].node || hit[0] : undefined;
}
