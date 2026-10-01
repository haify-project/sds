import { useQuery } from '@tanstack/react-query';
import { api, Resource } from '../../services/api';
import { cn } from '@/lib/utils';
import { toneOf } from '@/components/status';
import { RoleChip } from '@/components/RoleChip';
import { ResourceTopology } from '@/components/ResourceTopology';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import { Plus, ArrowUpCircle, Camera } from 'lucide-react';
import { TONE_TEXT, isPeerSyncing, syncPollInterval } from './replication';
import { type RowDialog } from './types';
import { QuorumPanel, WANPanel } from './StatusPanels';

/** A sub-table inside the expanded panel: quieter rows than the main table. */
function SubTable({ head, children }: { head: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="overflow-hidden rounded-lg border border-border bg-card">
      <Table>
        <TableHeader>
          <TableRow>{head}</TableRow>
        </TableHeader>
        <TableBody>{children}</TableBody>
      </Table>
    </div>
  );
}

const subHead = 'px-3.5 pt-2.5 pb-2';

const subCell = 'h-10 px-3.5 py-0 text-[12.5px]';

/**
 * A sub-table row below `md`, where four columns inside a card that is itself
 * ~290px wide is not a table but a horizontal scroll. Same values, stacked:
 * the identifier on its own line, the rest as label/value pairs.
 */
function StackedRecord({
  title,
  facts,
}: {
  title: React.ReactNode;
  facts: { label: string; value: React.ReactNode }[];
}) {
  return (
    <div className="rounded-lg border border-border bg-card px-3.5 py-3">
      <div className="font-mono text-[13px] font-medium break-all">{title}</div>
      <dl className="mt-2 grid grid-cols-2 gap-x-4 gap-y-2">
        {facts.map((f) => (
          <div key={f.label} className="min-w-0">
            <dt className="eyebrow">{f.label}</dt>
            <dd className="mt-0.5 text-[12.5px] break-words">{f.value}</dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

/** The per-peer resync bar. It needs a floor to stay legible, and a lower one
 *  in a stacked fact than in the table column it was drawn for. */
function SyncBar({ percent }: { percent: number }) {
  return (
    <div className="flex min-w-[105px] items-center gap-2 md:min-w-[140px]">
      <div className="h-1 flex-1 overflow-hidden rounded-[2px] bg-muted">
        <div
          className="h-1 bg-status-warn transition-all"
          style={{ width: `${Math.min(100, Math.max(0, percent))}%` }}
        />
      </div>
      <span className="font-mono text-[11.5px] tabular-nums text-status-warn-text">
        {percent.toFixed(1)}%
      </span>
    </div>
  );
}

// ResourceDetail renders a resource's live status inline, under its row.
//
// It used to be a modal, which forced a choice the operator should not have to
// make: read one resource's detail, or see the list. Comparing two resources
// meant opening and closing dialogs and holding the first in your head. Expanded
// rows let several be open at once and keep every one in the context of the
// table it belongs to. Only the reading moved: every mutation is still a dialog
// with its own confirmation.
export function ResourceDetail({
  resource,
  onOpenDialog,
}: {
  resource: Resource;
  onOpenDialog: (d: RowDialog) => void;
}) {
  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['resource-status', resource.name],
    queryFn: () => api.resourceStatus(resource.name),
    refetchInterval: syncPollInterval,
  });
  const status = data?.status;

  if (isLoading)
    return (
      <div className="space-y-3 md:px-11 md:py-5">
        <Skeleton className="h-24 w-full" />
      </div>
    );
  if (isError)
    return (
      <p className="text-sm text-destructive md:px-11 md:py-5">{(error as Error).message}</p>
    );
  if (!status) return null;

  const nodeStates = Object.entries(status.nodeStates ?? {});
  // The status volumes carry the live device; the list volumes carry the
  // backing LV. Neither alone is the whole row.
  const backingOf = new Map(resource.volumes.map((v) => [v.volumeId, v]));
  const volumes = status.volumes?.length ? status.volumes : resource.volumes;

  // Derived once, rendered as a table at `md` and up and as stacks below it.
  // Two hand-written copies of these cells would eventually disagree about what
  // a Diskless peer or a missing backing volume reads as.
  const nodeRows = nodeStates.map(([host, st]) => {
    const node = st.node || host;
    return {
      key: host,
      node,
      role: (
        <RoleChip
          role={st.role}
          suffix={resource.wanMode && node === resource.drNode ? '· DR' : undefined}
        />
      ),
      disk: (
        <span className={cn('font-mono', TONE_TEXT[toneOf(st.diskState)])}>
          {st.diskState || '—'}
        </span>
      ),
      replication: !st.replicationState ? (
        <span className="text-muted-foreground">—</span>
      ) : isPeerSyncing(st) ? (
        <SyncBar percent={st.syncPercent ?? 0} />
      ) : (
        <span className="font-mono break-all text-muted-foreground">
          {st.replicationState}
        </span>
      ),
    };
  });

  const volumeRows = volumes.map((vol) => {
    const backing = backingOf.get(vol.volumeId) ?? vol;
    return {
      key: vol.volumeId,
      id: vol.volumeId,
      device: vol.device,
      backing:
        backing.pool && backing.backingVolume
          ? `${backing.pool}/${backing.backingVolume}`
          : backing.backingVolume || '—',
      size: `${vol.sizeGb} GB`,
    };
  });

  return (
    <div className="space-y-6 md:py-5 md:pr-6 md:pl-11">
      <div className="grid gap-6 lg:grid-cols-2">
        <section>
          <h4 className="eyebrow mb-2.5">Per-node state</h4>
          <div className="hidden md:block">
            <SubTable
              head={
                <>
                  <TableHead className={subHead}>Node</TableHead>
                  <TableHead className={subHead}>Role</TableHead>
                  <TableHead className={subHead}>Disk</TableHead>
                  <TableHead className={subHead}>Replication</TableHead>
                </>
              }
            >
              {nodeRows.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={4} className={cn(subCell, 'text-muted-foreground')}>
                    No node states reported.
                  </TableCell>
                </TableRow>
              ) : (
                nodeRows.map((r) => (
                  <TableRow key={r.key}>
                    <TableCell className={cn(subCell, 'font-mono')}>{r.node}</TableCell>
                    <TableCell className={subCell}>{r.role}</TableCell>
                    <TableCell className={subCell}>{r.disk}</TableCell>
                    <TableCell className={subCell}>{r.replication}</TableCell>
                  </TableRow>
                ))
              )}
            </SubTable>
          </div>
          <div className="space-y-2 md:hidden">
            {nodeRows.length === 0 ? (
              <p className="text-[12.5px] text-muted-foreground">
                No node states reported.
              </p>
            ) : (
              nodeRows.map((r) => (
                <StackedRecord
                  key={r.key}
                  title={r.node}
                  facts={[
                    { label: 'Role', value: r.role },
                    { label: 'Disk', value: r.disk },
                    { label: 'Replication', value: r.replication },
                  ]}
                />
              ))
            )}
          </div>
        </section>

        <section>
          <h4 className="eyebrow mb-2.5">Volumes</h4>
          <div className="hidden md:block">
            <SubTable
              head={
                <>
                  <TableHead className={subHead}>ID</TableHead>
                  <TableHead className={subHead}>Device</TableHead>
                  <TableHead className={subHead}>Backing</TableHead>
                  <TableHead className={cn(subHead, 'text-right')}>Size</TableHead>
                </>
              }
            >
              {volumeRows.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={4} className={cn(subCell, 'text-muted-foreground')}>
                    No volumes.
                  </TableCell>
                </TableRow>
              ) : (
                volumeRows.map((v) => (
                  <TableRow key={v.key}>
                    <TableCell className={cn(subCell, 'font-mono tabular-nums')}>
                      {v.id}
                    </TableCell>
                    <TableCell className={cn(subCell, 'font-mono')}>{v.device}</TableCell>
                    <TableCell className={cn(subCell, 'font-mono text-muted-foreground')}>
                      {v.backing}
                    </TableCell>
                    <TableCell className={cn(subCell, 'text-right font-mono tabular-nums')}>
                      {v.size}
                    </TableCell>
                  </TableRow>
                ))
              )}
            </SubTable>
          </div>
          <div className="space-y-2 md:hidden">
            {volumeRows.length === 0 ? (
              <p className="text-[12.5px] text-muted-foreground">No volumes.</p>
            ) : (
              volumeRows.map((v) => (
                <StackedRecord
                  key={v.key}
                  title={`volume ${v.id}`}
                  facts={[
                    {
                      label: 'Device',
                      value: <span className="font-mono break-all">{v.device}</span>,
                    },
                    {
                      label: 'Size',
                      value: <span className="font-mono tabular-nums">{v.size}</span>,
                    },
                    {
                      label: 'Backing',
                      value: (
                        <span className="font-mono break-all text-muted-foreground">
                          {v.backing}
                        </span>
                      ),
                    },
                  ]}
                />
              ))
            )}
          </div>

          <div className="mt-3.5 flex flex-wrap gap-2">
            <Button variant="outline" size="sm" onClick={() => onOpenDialog('add-volume')}>
              <Plus />
              Add volume
            </Button>
            <Button variant="outline" size="sm" onClick={() => onOpenDialog('snapshots')}>
              <Camera />
              Snapshots
            </Button>
            <Button variant="outline" size="sm" onClick={() => onOpenDialog('primary')}>
              <ArrowUpCircle />
              Set role
            </Button>
          </div>
        </section>
      </div>

      {(status.quorum || status.wan) && (
        <div className="grid gap-6 lg:grid-cols-2">
          {status.quorum && <QuorumPanel quorum={status.quorum} />}
          {status.wan && <WANPanel status={status} />}
        </div>
      )}

      <ResourceTopology resource={resource} status={status} />

      {(resource.profile || Object.keys(resource.labels ?? {}).length > 0) && (
        <section>
          <h4 className="eyebrow mb-2.5">Metadata</h4>
          <ResourceMetadata resource={resource} />
        </section>
      )}
    </div>
  );
}

function ResourceMetadata({
  resource,
  compact = false,
}: {
  resource: Resource;
  compact?: boolean;
}) {
  const labels = Object.entries(resource.labels ?? {}).sort(([a], [b]) =>
    a.localeCompare(b),
  );
  if (!resource.profile && labels.length === 0) return null;

  const visibleLabels = compact ? labels.slice(0, 3) : labels;
  const allLabels = labels.map(([key, value]) => `${key}=${value}`).join(', ');

  return (
    <div className="flex max-w-full flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
      {resource.profile && (
        <Badge variant="outline" className="max-w-48 truncate font-normal">
          profile: {resource.profile}
        </Badge>
      )}
      {visibleLabels.map(([key, value]) => (
        <Badge
          key={key}
          variant="secondary"
          className="max-w-48 truncate font-mono font-normal"
          title={`${key}=${value}`}
        >
          {key}={value}
        </Badge>
      ))}
      {compact && labels.length > visibleLabels.length && (
        <Badge variant="secondary" title={allLabels}>
          +{labels.length - visibleLabels.length}
        </Badge>
      )}
    </div>
  );
}
