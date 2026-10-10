import { Resource, ResourceStatus } from '../../services/api';
import { cn } from '@/lib/utils';
import { TONE_BG, TONE_SOFT } from '@/components/status';
import { StatusTickCell } from '@/components/StatusTick';
import { TableCell, TableRow } from '@/components/ui/table';
import { ChevronDown, ChevronRight } from 'lucide-react';
import { managerOf, TONE_TEXT, type Replication, totalGbOf } from './replication';
import { type RowDialog } from './types';
import { ResourceDetail } from './ResourceDetail';
import { ResourceActionsMenu } from './ResourceActionsMenu';

type NodeChip = {
  name: string;
  /** Suffix words that say what this node is; never left to the tint alone. */
  marks: string[];
  primary: boolean;
  title?: string;
};

// nodeChips flattens a resource's four kinds of participant into one ordered
// list. They are four separate fields on the API but one column here, and an
// operator scanning the column wants the node that takes the writes first.
export function nodeChips(resource: Resource, status?: ResourceStatus): NodeChip[] {
  // The state map is keyed by DRBD host name; each entry carries the Haify node
  // name so it can be paired back to the node list.
  const roleOf = new Map<string, string>();
  for (const [host, st] of Object.entries(status?.nodeStates ?? {}))
    roleOf.set(st.node || host, st.role);

  const chips: NodeChip[] = [];
  for (const name of resource.nodes) {
    const primary = (roleOf.get(name) ?? '').toLowerCase() === 'primary';
    // The off-site copy is a replica like the others to DRBD, but not to an
    // operator: it replicates asynchronously and never takes over on its own,
    // so it must not read as a peer that failover can land on.
    const isDR = Boolean(resource.wanMode) && name === resource.drNode;
    chips.push({
      name,
      primary,
      marks: [...(primary ? ['primary'] : []), ...(isDR ? ['DR'] : [])],
      title: isDR
        ? 'Off-site disaster-recovery replica: asynchronous (protocol A), reached over a WAN proxy leg, and promoted only by an explicit dr-failover'
        : undefined,
    });
  }
  for (const name of resource.disklessNodes ?? [])
    chips.push({
      name,
      primary: false,
      marks: ['tiebreaker'],
      title:
        'Diskless quorum tiebreaker (votes for quorum only, never promoted or mounted)',
    });
  for (const name of resource.disklessClients ?? []) {
    const primary = (roleOf.get(name) ?? '').toLowerCase() === 'primary';
    chips.push({
      name,
      primary,
      marks: [...(primary ? ['primary'] : []), 'client'],
      title:
        'Diskless data client: no local replica, mounts the volume over the DRBD network (e.g. a Kubernetes/CSI Pod on a non-replica node)',
    });
  }
  return chips.sort((a, b) => Number(b.primary) - Number(a.primary));
}

export function NodeChips({ chips, wrap = false }: { chips: NodeChip[]; wrap?: boolean }) {
  // Three fit the column; the rest collapse into a count that still names them
  // on hover, rather than pushing the row wider than the table. A card's fact
  // has no column to overflow — it wraps instead, and shows every node, because
  // a "+2" a thumb cannot hover over says nothing at all.
  const shown = wrap ? chips : chips.slice(0, 3);
  const rest = wrap ? [] : chips.slice(3);
  return (
    <div className={cn('flex items-center gap-1.5', wrap && 'flex-wrap')}>
      {shown.map((chip) => (
        <span
          key={chip.name}
          title={chip.title}
          className={cn(
            'inline-flex h-[22px] max-w-full items-center rounded-[4px] px-2 font-mono text-[11px]',
            chip.primary
              ? 'bg-accent font-medium text-accent-foreground'
              : 'bg-secondary text-secondary-foreground',
          )}
        >
          <span className="truncate">{chip.name}</span>
          {chip.marks.length > 0 && (
            <span className="ml-1 shrink-0 font-sans text-[10px] opacity-75">
              {chip.marks.join(' · ')}
            </span>
          )}
        </span>
      ))}
      {rest.length > 0 && (
        <span
          title={rest.map((c) => c.name).join(', ')}
          className="inline-flex h-[22px] items-center rounded-[4px] bg-secondary px-2 font-mono text-[11px] text-secondary-foreground"
        >
          +{rest.length}
        </span>
      )}
    </div>
  );
}

/** The qualifiers that follow the name. Extracted because the row and the card
 *  both show them, and a resource that is Kubernetes-managed in one view and not in
 *  the other would be a lie in whichever view the operator happened to read. */
export function ResourceChips({ resource }: { resource: Resource }) {
  const manager = managerOf(resource);
  return (
    <>
      {manager && (
        <span
          className="rounded-[4px] bg-accent px-1.5 py-0.5 text-[11.5px] text-accent-foreground"
          title={manager.about}
        >
          {manager.chip}
        </span>
      )}
      {resource.wanMode && (
        <span
          className="rounded-[4px] bg-secondary px-1.5 py-0.5 text-[11.5px] text-secondary-foreground"
          title={`Off-site asynchronous replica on ${resource.drNode ?? 'a DR node'}`}
        >
          off-site
        </span>
      )}
      {resource.quorumRisk && (
        <span
          className={cn('rounded-[4px] px-1.5 py-0.5 text-[11.5px]', TONE_SOFT.warn)}
          title="2-node resource with no quorum tiebreaker: a single node failure suspends I/O"
        >
          quorum risk
        </span>
      )}
    </>
  );
}

/** The thin bar plus the word that says what it means. */
export function ReplicationCell({ replication }: { replication: Replication }) {
  return (
    <div className="flex items-center gap-2.5" title={replication.title}>
      <div className="h-1 flex-1 overflow-hidden rounded-[2px] bg-muted">
        <div
          className={cn('h-1 transition-all', TONE_BG[replication.tone])}
          style={{ width: `${replication.percent}%` }}
        />
      </div>
      <span className={cn('font-mono text-[11.5px] tabular-nums', TONE_TEXT[replication.tone])}>
        {replication.label}
      </span>
    </div>
  );
}

export function ResourceRow({
  resource,
  replication,
  status,
  expanded,
  onToggle,
  onOpenDialog,
}: {
  resource: Resource;
  replication: Replication;
  status?: ResourceStatus;
  expanded: boolean;
  onToggle: () => void;
  onOpenDialog: (d: RowDialog) => void;
}) {
  const totalGb = totalGbOf(resource);
  const Chevron = expanded ? ChevronDown : ChevronRight;

  return (
    <>
      <TableRow className={expanded ? 'border-b-0' : undefined}>
        <StatusTickCell tone={replication.tone} />
        <TableCell>
          <div className="flex items-center gap-2.5">
            <button
              type="button"
              onClick={onToggle}
              aria-expanded={expanded}
              aria-label={`${expanded ? 'Hide' : 'Show'} detail for ${resource.name}`}
              className="-m-1 rounded p-1 text-muted-foreground outline-none hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50"
            >
              <Chevron className="h-3.5 w-3.5" />
            </button>
            <div>
              <div className="flex items-center gap-2.5">
                <span className="font-mono text-[14px] font-semibold">{resource.name}</span>
                <ResourceChips resource={resource} />
              </div>
              <div className="mt-0.5 font-mono text-[11.5px] tabular-nums text-muted-foreground @min-[1000px]:hidden">
                port {resource.port} · protocol {resource.protocol}
              </div>
            </div>
          </div>
        </TableCell>
        <TableCell className="hidden font-mono tabular-nums text-muted-foreground @min-[1000px]:table-cell">
          {resource.port}
        </TableCell>
        <TableCell className="hidden font-mono text-muted-foreground @min-[1000px]:table-cell">
          {resource.protocol}
        </TableCell>
        <TableCell>
          <NodeChips chips={nodeChips(resource, status)} />
        </TableCell>
        <TableCell className="font-mono tabular-nums text-muted-foreground">
          {resource.volumes.length} · {totalGb} GB
        </TableCell>
        <TableCell>
          <ReplicationCell replication={replication} />
        </TableCell>
        <TableCell className="pr-5 text-right">
          <ResourceActionsMenu resource={resource} onSelect={onOpenDialog} />
        </TableCell>
      </TableRow>

      {expanded && (
        <TableRow className="hover:bg-transparent">
          {/* colSpan spans the whole table so the detail is not squeezed into
              one column; the panel below lays itself out. */}
          {/* whitespace-normal: TableCell defaults to nowrap for the sake of
              one-line data cells, which would keep the panel's prose on one
              line and push it out of the card. */}
          <TableCell colSpan={8} className="bg-muted/40 p-0 whitespace-normal">
            <div className="py-5 pr-6 pl-11">
              <ResourceDetail resource={resource} onOpenDialog={onOpenDialog} />
            </div>
          </TableCell>
        </TableRow>
      )}
    </>
  );
}
