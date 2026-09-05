import { useState } from 'react';
import {
  useQueries,
  useQuery,
  useMutation,
  useQueryClient,
} from '@tanstack/react-query';
import {
  api,
  Resource,
  ResourceProfile,
  Volume,
  ResourceStatus,
  NodeResourceState,
  QuorumInfo,
} from '../services/api';
import { cn } from '@/lib/utils';
import { toneOf, TONE_BG, TONE_SOFT, type StatusTone } from '@/components/status';
import { PageHeader } from '@/components/PageHeader';
import { StatusTickCell, StatusTickHead } from '@/components/StatusTick';
import { SegmentedFilter } from '@/components/SegmentedFilter';
import { RoleChip } from '@/components/RoleChip';
import { ResourceTopology } from '@/components/ResourceTopology';
import { ResourceProfilesPage } from './ResourceProfilesPage';
import { useSearchParams } from 'react-router';
import { SnapshotsDialog } from '@/components/SnapshotsDialog';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import { Badge } from '@/components/ui/badge';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Switch } from '@/components/ui/switch';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { toast } from 'sonner';
import {
  Plus,
  ChevronDown,
  ChevronRight,
  ArrowUpCircle,
  ArrowDownCircle,
  Database,
  FolderCog,
  Trash2,
  MoreHorizontal,
  Loader2,
  Boxes,
  Camera,
  SlidersHorizontal,
  X,
  CalendarClock,
  Globe,
  Search,
} from 'lucide-react';

interface NodeOpt {
  name: string;
  address: string;
}
interface PoolOpt {
  name: string;
  node: string;
  type: string;
  freeGb: string;
}

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

function isCsiManaged(resource: Resource): boolean {
  return resource.labels?.[csiManagedLabel] === 'csi';
}

// The text colour that pairs with each tone. `TONE_SOFT` carries a fill with it,
// which is too loud for a word sitting on the row's own background. Literal
// class strings so the Tailwind scanner still sees all four.
const TONE_TEXT: Record<StatusTone, string> = {
  ok: 'text-status-ok-text',
  warn: 'text-status-warn-text',
  bad: 'text-status-bad-text',
  idle: 'text-muted-foreground',
};

// isPeerSyncing reports whether a peer node-state represents a resync in
// progress. The local node has no replication relationship (empty
// replicationState) so it never counts as syncing.
function isPeerSyncing(state?: NodeResourceState): boolean {
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
function syncPollInterval(query: {
  state: { data?: unknown };
}): number | false {
  const status = (query.state.data as { status?: ResourceStatus } | undefined)
    ?.status;
  return statusHasActiveSync(status) ? 2000 : false;
}

/** What the Replication column says, and what the row's tick is coloured from. */
type Replication = {
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
function replicationSummary(status?: ResourceStatus): Replication {
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

type FilterKey = 'all' | 'healthy' | 'syncing' | 'offsite';

export function ResourcesPage() {
  const { data: resources, isLoading } = useQuery({
    queryKey: ['resources'],
    queryFn: () => api.getResources(),
  });

  const { data: pools } = useQuery({
    queryKey: ['pools'],
    queryFn: () => api.getPools(),
  });

  const { data: nodes } = useQuery({
    queryKey: ['nodes'],
    queryFn: () => api.getNodes(),
  });

  const { data: profiles } = useQuery({
    queryKey: ['resource-profiles'],
    queryFn: () => api.getResourceProfiles(),
  });

  const list = resources?.resources ?? [];

  // One live-status query per resource, held here rather than in each row: the
  // segmented filter has to count healthy and syncing resources, and those
  // words exist nowhere in the list response. The rows and the expanded panels
  // read the same query keys, so they are served from this cache rather than
  // fetching again, and the adaptive interval still stops polling the moment a
  // resource settles.
  const statusQueries = useQueries({
    queries: list.map((r) => ({
      queryKey: ['resource-status', r.name],
      queryFn: () => api.resourceStatus(r.name),
      refetchInterval: syncPollInterval,
    })),
  });

  const replication = new Map<string, Replication>(
    list.map((r, i) => [r.name, replicationSummary(statusQueries[i]?.data?.status)]),
  );

  const [params, setParams] = useSearchParams();
  // Profiles live here rather than in their own nav entry: they are templates
  // for resources and do nothing on their own, so they belong beside the things
  // they create. It also matches the CLI, where the command has always been
  // `sds resource profile`.
  const tab = params.get('tab') === 'profiles' ? 'profiles' : 'resources';

  const [query, setQuery] = useState('');
  const [filter, setFilter] = useState<FilterKey>('all');
  const [createOpen, setCreateOpen] = useState(false);

  const matchesFilter = (r: Resource, key: FilterKey) => {
    if (key === 'all') return true;
    if (key === 'offsite') return Boolean(r.wanMode);
    const tone = replication.get(r.name)?.tone;
    return key === 'healthy' ? tone === 'ok' : tone === 'warn';
  };

  const counts = {
    all: list.length,
    healthy: list.filter((r) => matchesFilter(r, 'healthy')).length,
    syncing: list.filter((r) => matchesFilter(r, 'syncing')).length,
    offsite: list.filter((r) => matchesFilter(r, 'offsite')).length,
  };

  // Name, node names and labels: the three things an operator actually types
  // when hunting for one resource among many.
  const needle = query.trim().toLowerCase();
  const visible = list.filter((r) => {
    if (!matchesFilter(r, filter)) return false;
    if (!needle) return true;
    const haystack = [
      r.name,
      ...r.nodes,
      ...(r.disklessNodes ?? []),
      ...(r.disklessClients ?? []),
      r.profile ?? '',
      ...Object.entries(r.labels ?? {}).map(([k, v]) => `${k}=${v}`),
    ]
      .join(' ')
      .toLowerCase();
    return haystack.includes(needle);
  });

  const volumeCount = list.reduce((n, r) => n + r.volumes.length, 0);
  const offsiteCount = counts.offsite;

  return (
    <div>
      <PageHeader
        className="mb-5"
        title="Resources"
        description={
          tab === 'profiles' ? (
            <>
              <span className="font-mono tabular-nums text-foreground">
                {profiles?.profiles?.length ?? 0}
              </span>{' '}
              resource profiles
            </>
          ) : (
            <>
              <span className="font-mono tabular-nums text-foreground">{list.length}</span>{' '}
              DRBD resources ·{' '}
              <span className="font-mono tabular-nums text-foreground">{volumeCount}</span>{' '}
              volumes
              {offsiteCount > 0 ? (
                <>
                  {' '}
                  ·{' '}
                  <span className="font-mono tabular-nums text-foreground">
                    {offsiteCount}
                  </span>{' '}
                  replicated off-site
                </>
              ) : null}
            </>
          )
        }
        actions={
          tab === 'resources' ? (
            <>
              <div className="relative w-[210px]">
                <Search className="pointer-events-none absolute top-1/2 left-2.5 h-[15px] w-[15px] -translate-y-1/2 text-muted-foreground" />
                <Input
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder="Filter resources"
                  aria-label="Filter resources by name, node or label"
                  className="h-[34px] pl-8 text-[13px]"
                />
              </div>
              <Button onClick={() => setCreateOpen(true)}>
                <Plus />
                Create resource
              </Button>
            </>
          ) : null
        }
      />

      <Tabs
        value={tab}
        onValueChange={(v) =>
          setParams(v === 'profiles' ? { tab: 'profiles' } : {}, { replace: true })
        }
        className="space-y-4"
      >
        <TabsList>
          <TabsTrigger value="resources">Resources</TabsTrigger>
          <TabsTrigger value="profiles">Profiles</TabsTrigger>
        </TabsList>

        <TabsContent value="profiles">
          <ResourceProfilesPage />
        </TabsContent>

        <TabsContent value="resources" className="space-y-3.5">
          <SegmentedFilter
            aria-label="Filter resources by state"
            value={filter}
            onChange={setFilter}
            options={[
              { value: 'all', label: 'All', count: counts.all },
              { value: 'healthy', label: 'Healthy', count: counts.healthy },
              { value: 'syncing', label: 'Syncing', count: counts.syncing },
              { value: 'offsite', label: 'Off-site', count: counts.offsite },
            ]}
          />

          <Card className="overflow-hidden">
            <CardContent className="p-0">
              {isLoading ? (
                <div className="space-y-3 p-5">
                  {[0, 1, 2].map((i) => (
                    <Skeleton key={i} className="h-9 w-full" />
                  ))}
                </div>
              ) : !list.length ? (
                <div className="flex flex-col items-center justify-center gap-3 py-16 text-center">
                  <Boxes className="h-8 w-8 text-muted-foreground" />
                  <p className="text-sm text-muted-foreground">
                    No resources found. Create your first resource to get started.
                  </p>
                  <Button variant="outline" onClick={() => setCreateOpen(true)}>
                    <Plus />
                    Create resource
                  </Button>
                </div>
              ) : !visible.length ? (
                <p className="py-16 text-center text-sm text-muted-foreground">
                  No resource matches this filter.
                </p>
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <StatusTickHead />
                      <TableHead>Resource</TableHead>
                      <TableHead>Port</TableHead>
                      <TableHead>Protocol</TableHead>
                      <TableHead>Nodes</TableHead>
                      <TableHead>Volumes</TableHead>
                      <TableHead className="w-[200px]">Replication</TableHead>
                      <TableHead className="pr-5 text-right">Actions</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {visible.map((resource) => (
                      <ResourceRow
                        key={resource.name}
                        resource={resource}
                        replication={
                          replication.get(resource.name) ?? {
                            tone: 'idle',
                            label: 'unknown',
                            percent: 0,
                          }
                        }
                        pools={pools?.pools ?? []}
                        nodes={nodes?.nodes ?? []}
                      />
                    ))}
                  </TableBody>
                </Table>
              )}
            </CardContent>
          </Card>

          {list.length > 0 && (
            <p className="text-xs text-muted-foreground">
              Showing{' '}
              <span className="font-mono tabular-nums">{visible.length}</span> of{' '}
              <span className="font-mono tabular-nums">{list.length}</span> resources
            </p>
          )}
        </TabsContent>
      </Tabs>

      <CreateResourceDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        nodes={nodes?.nodes ?? []}
        pools={pools?.pools ?? []}
        profiles={profiles?.profiles ?? []}
      />
    </div>
  );
}

/** Every dialog a row can open. One at a time, so one nullable holds it. */
type RowDialog =
  | 'primary'
  | 'secondary'
  | 'volumes'
  | 'add-volume'
  | 'snapshots'
  | 'mount'
  | 'options'
  | 'schedule'
  | 'add-dr'
  | 'dr-failover'
  | 'delete';

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
function nodeChips(resource: Resource, status?: ResourceStatus): NodeChip[] {
  // The state map is keyed by DRBD host name; each entry carries the SDS node
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

function NodeChips({ chips }: { chips: NodeChip[] }) {
  // Three fit the column; the rest collapse into a count that still names them
  // on hover, rather than pushing the row wider than the table.
  const shown = chips.slice(0, 3);
  const rest = chips.slice(3);
  return (
    <div className="flex items-center gap-1.5">
      {shown.map((chip) => (
        <span
          key={chip.name}
          title={chip.title}
          className={cn(
            'inline-flex h-[22px] items-center rounded-[4px] px-2 font-mono text-[11px]',
            chip.primary
              ? 'bg-accent font-medium text-accent-foreground'
              : 'bg-secondary text-secondary-foreground',
          )}
        >
          {chip.name}
          {chip.marks.length > 0 && (
            <span className="ml-1 font-sans text-[10px] opacity-75">
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

/** The thin bar plus the word that says what it means. */
function ReplicationCell({ replication }: { replication: Replication }) {
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

function ResourceRow({
  resource,
  replication,
  pools,
  nodes,
}: {
  resource: Resource;
  replication: Replication;
  pools: PoolOpt[];
  nodes: NodeOpt[];
}) {
  const [expanded, setExpanded] = useState(false);
  const [dialog, setDialog] = useState<RowDialog | null>(null);
  const close = () => setDialog(null);

  // Shares the page's query key, so an expanded row costs no extra request.
  const { data } = useQuery({
    queryKey: ['resource-status', resource.name],
    queryFn: () => api.resourceStatus(resource.name),
    refetchInterval: syncPollInterval,
  });
  const status = data?.status;

  // sizeGb crosses the wire as a proto int64, i.e. a JSON *string*; summing
  // it without Number() concatenates instead of adding.
  const totalGb = resource.volumes.reduce((n, v) => n + Number(v.sizeGb), 0);
  const Chevron = expanded ? ChevronDown : ChevronRight;

  return (
    <>
      <TableRow className={expanded ? 'border-b-0' : undefined}>
        <StatusTickCell tone={replication.tone} />
        <TableCell>
          <div className="flex items-center gap-2.5">
            <button
              type="button"
              onClick={() => setExpanded((v) => !v)}
              aria-expanded={expanded}
              aria-label={`${expanded ? 'Hide' : 'Show'} detail for ${resource.name}`}
              className="-m-1 rounded p-1 text-muted-foreground outline-none hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50"
            >
              <Chevron className="h-3.5 w-3.5" />
            </button>
            <span className="font-mono text-[14px] font-semibold">{resource.name}</span>
            {isCsiManaged(resource) && (
              <span
                className="rounded-[4px] bg-accent px-1.5 py-0.5 text-[11.5px] text-accent-foreground"
                title="Provisioned by the Kubernetes CSI driver. Its lifecycle belongs to Kubernetes — delete the PersistentVolumeClaim instead of removing it here."
              >
                kubernetes
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
          </div>
        </TableCell>
        <TableCell className="font-mono tabular-nums text-muted-foreground">
          {resource.port}
        </TableCell>
        <TableCell className="font-mono text-muted-foreground">
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
          <ResourceActionsMenu resource={resource} onSelect={setDialog} />
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
            <ResourceDetail
              resource={resource}
              onOpenDialog={setDialog}
            />
          </TableCell>
        </TableRow>
      )}

      <AddDRDialog
        open={dialog === 'add-dr'}
        onOpenChange={(o) => (o ? setDialog('add-dr') : close())}
        resource={resource}
        nodes={nodes}
      />
      <DRFailoverDialog
        open={dialog === 'dr-failover'}
        onOpenChange={(o) => (o ? setDialog('dr-failover') : close())}
        resource={resource}
      />
      <SetRoleDialog
        open={dialog === 'primary'}
        onOpenChange={(o) => (o ? setDialog('primary') : close())}
        resource={resource}
        mode="primary"
      />
      <SetRoleDialog
        open={dialog === 'secondary'}
        onOpenChange={(o) => (o ? setDialog('secondary') : close())}
        resource={resource}
        mode="secondary"
      />
      <SnapshotsDialog
        resource={resource.name}
        open={dialog === 'snapshots'}
        onOpenChange={(o) => (o ? setDialog('snapshots') : close())}
      />
      <VolumesDialog
        open={dialog === 'volumes' || dialog === 'add-volume'}
        onOpenChange={(o) => (o ? setDialog('volumes') : close())}
        defaultTab={dialog === 'add-volume' ? 'add' : 'volumes'}
        resource={resource}
        pools={pools}
      />
      <MountDialog
        open={dialog === 'mount'}
        onOpenChange={(o) => (o ? setDialog('mount') : close())}
        resource={resource}
        nodes={nodes}
      />
      <EditOptionsDialog
        open={dialog === 'options'}
        onOpenChange={(o) => (o ? setDialog('options') : close())}
        resource={resource}
      />
      <ScheduleDialog
        open={dialog === 'schedule'}
        onOpenChange={(o) => (o ? setDialog('schedule') : close())}
        resource={resource}
      />
      <DeleteResourceDialog
        open={dialog === 'delete'}
        onOpenChange={(o) => (o ? setDialog('delete') : close())}
        resourceName={resource.name}
        csiManaged={isCsiManaged(resource)}
      />
    </>
  );
}

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

// ResourceDetail renders a resource's live status inline, under its row.
//
// It used to be a modal, which forced a choice the operator should not have to
// make: read one resource's detail, or see the list. Comparing two resources
// meant opening and closing dialogs and holding the first in your head. Expanded
// rows let several be open at once and keep every one in the context of the
// table it belongs to. Only the reading moved: every mutation is still a dialog
// with its own confirmation.
function ResourceDetail({
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
      <div className="space-y-3 px-11 py-5">
        <Skeleton className="h-24 w-full" />
      </div>
    );
  if (isError)
    return (
      <p className="px-11 py-5 text-sm text-destructive">{(error as Error).message}</p>
    );
  if (!status) return null;

  const nodeStates = Object.entries(status.nodeStates ?? {});
  // The status volumes carry the live device; the list volumes carry the
  // backing LV. Neither alone is the whole row.
  const backingOf = new Map(resource.volumes.map((v) => [v.volumeId, v]));
  const volumes = status.volumes?.length ? status.volumes : resource.volumes;

  return (
    <div className="space-y-6 py-5 pr-6 pl-11">
      <div className="grid gap-6 lg:grid-cols-2">
        <section>
          <h4 className="eyebrow mb-2.5">Per-node state</h4>
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
            {nodeStates.length === 0 ? (
              <TableRow>
                <TableCell colSpan={4} className={cn(subCell, 'text-muted-foreground')}>
                  No node states reported.
                </TableCell>
              </TableRow>
            ) : (
              nodeStates.map(([host, st]) => {
                const node = st.node || host;
                const syncing = isPeerSyncing(st);
                return (
                  <TableRow key={host}>
                    <TableCell className={cn(subCell, 'font-mono')}>{node}</TableCell>
                    <TableCell className={subCell}>
                      <RoleChip
                        role={st.role}
                        suffix={
                          resource.wanMode && node === resource.drNode ? '· DR' : undefined
                        }
                      />
                    </TableCell>
                    <TableCell
                      className={cn(subCell, 'font-mono', TONE_TEXT[toneOf(st.diskState)])}
                    >
                      {st.diskState || '—'}
                    </TableCell>
                    <TableCell className={subCell}>
                      {!st.replicationState ? (
                        <span className="text-muted-foreground">—</span>
                      ) : syncing ? (
                        <div className="flex min-w-[140px] items-center gap-2">
                          <div className="h-1 flex-1 overflow-hidden rounded-[2px] bg-muted">
                            <div
                              className="h-1 bg-status-warn transition-all"
                              style={{
                                width: `${Math.min(100, Math.max(0, st.syncPercent ?? 0))}%`,
                              }}
                            />
                          </div>
                          <span className="font-mono text-[11.5px] tabular-nums text-status-warn-text">
                            {(st.syncPercent ?? 0).toFixed(1)}%
                          </span>
                        </div>
                      ) : (
                        <span className="font-mono text-muted-foreground">
                          {st.replicationState}
                        </span>
                      )}
                    </TableCell>
                  </TableRow>
                );
              })
            )}
          </SubTable>
        </section>

        <section>
          <h4 className="eyebrow mb-2.5">Volumes</h4>
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
            {volumes.length === 0 ? (
              <TableRow>
                <TableCell colSpan={4} className={cn(subCell, 'text-muted-foreground')}>
                  No volumes.
                </TableCell>
              </TableRow>
            ) : (
              volumes.map((vol) => {
                const backing = backingOf.get(vol.volumeId) ?? vol;
                const path =
                  backing.pool && backing.backingVolume
                    ? `${backing.pool}/${backing.backingVolume}`
                    : backing.backingVolume || '—';
                return (
                  <TableRow key={vol.volumeId}>
                    <TableCell className={cn(subCell, 'font-mono tabular-nums')}>
                      {vol.volumeId}
                    </TableCell>
                    <TableCell className={cn(subCell, 'font-mono')}>{vol.device}</TableCell>
                    <TableCell className={cn(subCell, 'font-mono text-muted-foreground')}>
                      {path}
                    </TableCell>
                    <TableCell
                      className={cn(subCell, 'text-right font-mono tabular-nums')}
                    >
                      {vol.sizeGb} GB
                    </TableCell>
                  </TableRow>
                );
              })
            )}
          </SubTable>

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

// Every action a row can take, behind one control. A row of six buttons reads
// as six decisions to make; a `⋯` reads as one, and the destructive ones stay
// destructive inside it.
function ResourceActionsMenu({
  resource,
  onSelect,
}: {
  resource: Resource;
  onSelect: (d: RowDialog) => void;
}) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="h-8 w-8 text-muted-foreground hover:text-foreground"
          aria-label={`Actions for ${resource.name}`}
        >
          <MoreHorizontal className="h-4 w-4" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-52">
        <DropdownMenuLabel className="font-mono">{resource.name}</DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={() => onSelect('primary')}>
          <ArrowUpCircle className="h-4 w-4" />
          Set Primary
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => onSelect('secondary')}>
          <ArrowDownCircle className="h-4 w-4" />
          Set Secondary
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => onSelect('volumes')}>
          <Database className="h-4 w-4" />
          Volumes
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => onSelect('snapshots')}>
          <Camera className="h-4 w-4" />
          Snapshots
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => onSelect('mount')}>
          <FolderCog className="h-4 w-4" />
          Filesystem / Mount
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => onSelect('options')}>
          <SlidersHorizontal className="h-4 w-4" />
          Edit DRBD Options
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => onSelect('schedule')}>
          <CalendarClock className="h-4 w-4" />
          Snapshot Schedule
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        {/* Off-site DR: attach one if there is none, fail over to it if there
            is. The two are mutually exclusive states of the same resource, so
            only one of them is ever offered. */}
        {resource.wanMode ? (
          <DropdownMenuItem variant="destructive" onSelect={() => onSelect('dr-failover')}>
            <Globe className="h-4 w-4" />
            DR Failover
          </DropdownMenuItem>
        ) : (
          <DropdownMenuItem onSelect={() => onSelect('add-dr')}>
            <Globe className="h-4 w-4" />
            Add DR Site
          </DropdownMenuItem>
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem variant="destructive" onSelect={() => onSelect('delete')}>
          <Trash2 className="h-4 w-4" />
          Delete
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function EditOptionsDialog({
  open,
  onOpenChange,
  resource,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  resource: Resource;
}) {
  const queryClient = useQueryClient();
  const [rows, setRows] = useState<{ key: string; value: string }[]>([
    { key: '', value: '' },
  ]);

  const mutation = useMutation({
    mutationFn: (opts: Record<string, string>) =>
      api.updateResourceOptions(resource.name, opts),
    onSuccess: () => {
      toast.success(`Options applied to "${resource.name}" (drbdadm adjust)`);
      queryClient.invalidateQueries({ queryKey: ['resources'] });
      onOpenChange(false);
      setRows([{ key: '', value: '' }]);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const setRow = (i: number, field: 'key' | 'value', v: string) =>
    setRows((rs) => rs.map((r, idx) => (idx === i ? { ...r, [field]: v } : r)));
  const addRow = () => setRows((rs) => [...rs, { key: '', value: '' }]);
  const removeRow = (i: number) =>
    setRows((rs) =>
      rs.length > 1 ? rs.filter((_, idx) => idx !== i) : [{ key: '', value: '' }],
    );

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const opts: Record<string, string> = {};
    for (const r of rows) {
      const k = r.key.trim();
      const v = r.value.trim();
      if (k && v) opts[k] = v;
    }
    if (Object.keys(opts).length === 0) {
      toast.error('Add at least one option (key and value)');
      return;
    }
    mutation.mutate(opts);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>Edit DRBD Options — {resource.name}</DialogTitle>
            <DialogDescription>
              Set options as{' '}
              <code className="font-mono text-xs">section/key</code> = value
              (e.g. <code className="font-mono text-xs">net/max-buffers</code> ={' '}
              <code className="font-mono text-xs">8000</code>). A bare key goes
              to the resource-level options section. Applied live with{' '}
              <code className="font-mono text-xs">drbdadm adjust</code>.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2 py-4">
            {rows.map((r, i) => (
              <div key={i} className="flex items-center gap-2">
                <Input
                  placeholder="net/max-buffers"
                  value={r.key}
                  onChange={(e) => setRow(i, 'key', e.target.value)}
                  className="font-mono text-xs"
                />
                <span className="text-muted-foreground">=</span>
                <Input
                  placeholder="8000"
                  value={r.value}
                  onChange={(e) => setRow(i, 'value', e.target.value)}
                  className="font-mono text-xs"
                />
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8 shrink-0 text-muted-foreground hover:text-destructive"
                  onClick={() => removeRow(i)}
                  aria-label="Remove option"
                >
                  <X className="h-4 w-4" />
                </Button>
              </div>
            ))}
            <Button type="button" variant="outline" size="sm" onClick={addRow}>
              <Plus className="h-4 w-4" />
              Add option
            </Button>
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={mutation.isPending}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={mutation.isPending}>
              {mutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
              Apply
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

const KEEP_FIELDS: { key: keyof import('@/services/api').GFSRetention; label: string }[] = [
  { key: 'hourly', label: 'Hourly' },
  { key: 'daily', label: 'Daily' },
  { key: 'weekly', label: 'Weekly' },
  { key: 'monthly', label: 'Monthly' },
  { key: 'yearly', label: 'Yearly' },
];

function ScheduleDialog({
  open,
  onOpenChange,
  resource,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  resource: Resource;
}) {
  const queryClient = useQueryClient();
  const [cron, setCron] = useState('0 * * * *');
  const [keep, setKeep] = useState<Record<string, string>>({
    hourly: '24',
    daily: '7',
    weekly: '4',
    monthly: '6',
    yearly: '0',
  });

  const { data, isLoading } = useQuery({
    queryKey: ['snapshot-schedules'],
    queryFn: () => api.getSnapshotSchedules(),
    enabled: open,
  });
  const existing = data?.schedules?.find((s) => s.name === resource.name);

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ['snapshot-schedules'] });

  const createMutation = useMutation({
    mutationFn: () =>
      api.createSnapshotSchedule({
        resource: resource.name,
        cron: cron.trim(),
        keep: Object.fromEntries(
          KEEP_FIELDS.map((f) => [f.key, parseInt(keep[f.key] || '0', 10) || 0]),
        ),
        enabled: true,
      }),
    onSuccess: () => {
      toast.success(`Snapshot schedule saved for "${resource.name}"`);
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const deleteMutation = useMutation({
    mutationFn: () => api.deleteSnapshotSchedule(resource.name),
    onSuccess: () => {
      toast.success(`Snapshot schedule deleted for "${resource.name}"`);
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!cron.trim()) {
      toast.error('Enter a cron expression (e.g. "0 * * * *")');
      return;
    }
    if (KEEP_FIELDS.every((f) => (parseInt(keep[f.key] || '0', 10) || 0) === 0)) {
      toast.error('Keep at least one of hourly/daily/weekly/monthly/yearly');
      return;
    }
    createMutation.mutate();
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Snapshot Schedule — {resource.name}</DialogTitle>
          <DialogDescription>
            Cron-driven snapshots on every diskful node, pruned by a
            grandfather-father-son retention policy. One schedule per resource.
          </DialogDescription>
        </DialogHeader>

        {isLoading ? (
          <Skeleton className="h-16 w-full" />
        ) : existing ? (
          <div className="rounded-lg border p-3 text-sm">
            <div className="flex items-center justify-between">
              <span className="font-mono">{existing.cron}</span>
              <Badge variant={existing.enabled ? 'default' : 'secondary'}>
                {existing.enabled ? 'enabled' : 'disabled'}
              </Badge>
            </div>
            <p className="mt-1 text-muted-foreground">
              keep: hourly={existing.keep?.hourly ?? 0} daily=
              {existing.keep?.daily ?? 0} weekly={existing.keep?.weekly ?? 0}{' '}
              monthly={existing.keep?.monthly ?? 0} yearly=
              {existing.keep?.yearly ?? 0}
            </p>
            {existing.nextRun && (
              <p className="text-muted-foreground">
                next run: {new Date(existing.nextRun).toLocaleString()}
              </p>
            )}
            {existing.lastRun && (
              <p className="text-muted-foreground">
                last run: {new Date(existing.lastRun).toLocaleString()}
              </p>
            )}
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="mt-2 text-destructive"
              onClick={() => deleteMutation.mutate()}
              disabled={deleteMutation.isPending}
            >
              <Trash2 className="h-4 w-4" />
              Delete schedule
            </Button>
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">
            No schedule yet. Create one below.
          </p>
        )}

        <form onSubmit={submit} className="space-y-4 pt-2">
          <div className="space-y-2">
            <Label htmlFor="sched-cron">
              Cron (standard 5-field, e.g. "0 * * * *" = hourly)
            </Label>
            <Input
              id="sched-cron"
              value={cron}
              onChange={(e) => setCron(e.target.value)}
              className="font-mono text-sm"
              placeholder="0 * * * *"
            />
          </div>
          <div className="grid grid-cols-3 gap-2 sm:grid-cols-5">
            {KEEP_FIELDS.map((f) => (
              <div key={f.key} className="space-y-1">
                <Label htmlFor={`keep-${f.key}`} className="text-xs">
                  {f.label}
                </Label>
                <Input
                  id={`keep-${f.key}`}
                  type="number"
                  min={0}
                  value={keep[f.key] ?? '0'}
                  onChange={(e) =>
                    setKeep((k) => ({ ...k, [f.key]: e.target.value }))
                  }
                  className="text-sm"
                />
              </div>
            ))}
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
            >
              Close
            </Button>
            <Button type="submit" disabled={createMutation.isPending}>
              {createMutation.isPending && (
                <Loader2 className="h-4 w-4 animate-spin" />
              )}
              {existing ? 'Replace schedule' : 'Create schedule'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** A tinted word. Tone never travels without the word it is tinting. */
function ToneWord({ tone, children }: { tone: StatusTone; children: React.ReactNode }) {
  return (
    <span
      className={cn(
        'inline-flex items-center rounded-[5px] px-2 py-0.5 text-xs',
        TONE_SOFT[tone],
      )}
    >
      {children}
    </span>
  );
}

// QuorumPanel answers the one question that governs availability: how many more
// nodes can be lost before this resource stops serving?
//
// It is spelled out rather than left as a badge because the arithmetic is not
// obvious. DRBD counts every configured node — diskless tiebreakers and the
// off-site DR alike — and needs a strict majority of that total. So attaching a
// DR raises the bar from two votes to three, which can exactly cancel the
// tiebreaker that was added to reach two in the first place.
function QuorumPanel({ quorum }: { quorum: QuorumInfo }) {
  const { members, required, online, hasQuorum, tolerated } = quorum;

  return (
    <div className="rounded-lg border border-border bg-card p-4">
      <div className="mb-3 flex items-center justify-between">
        <h4 className="eyebrow">Quorum</h4>
        <ToneWord tone={hasQuorum ? 'ok' : 'bad'}>
          {hasQuorum ? 'quorum held' : 'no quorum'}
        </ToneWord>
      </div>

      <div className="grid grid-cols-3 gap-3">
        <div>
          <div className="text-xs text-muted-foreground">Members</div>
          <div className="font-mono text-[15px] tabular-nums">{members}</div>
        </div>
        <div>
          <div className="text-xs text-muted-foreground">Votes needed</div>
          <div className="font-mono text-[15px] tabular-nums">{required}</div>
        </div>
        <div>
          <div className="text-xs text-muted-foreground">Online</div>
          <div className="font-mono text-[15px] tabular-nums">
            {online}
            <span className="text-muted-foreground"> / {members}</span>
          </div>
        </div>
      </div>

      <p
        className={cn(
          'mt-3 text-[13px]',
          tolerated > 0 ? 'text-muted-foreground' : 'text-status-warn-text',
        )}
      >
        {tolerated > 0
          ? `${tolerated} more member${tolerated === 1 ? '' : 's'} may be lost before I/O suspends.`
          : 'No margin left: the next member lost suspends I/O.'}
      </p>
    </div>
  );
}

// WANPanel shows the off-site leg: where the DR is, whether each tunnel is up,
// and how much data a failover right now would lose.
//
// The backlog is the figure that matters and it is deliberately shown as
// "unknown" when the proxy published nothing. Under protocol A the primary
// acknowledges writes before they cross the WAN, so a confident "0" that was
// really "no data" would understate the loss window of a decision made on it.
function WANPanel({ status }: { status: ResourceStatus }) {
  const m = status.wanMetrics;
  const legs = Object.entries(status.wanProxy ?? {});
  const backlog =
    m?.bufferUsedBytes === undefined ? null : Number(m.bufferUsedBytes);

  return (
    <div className="rounded-lg border border-border bg-card p-4">
      <div className="mb-3 flex items-center justify-between">
        <h4 className="eyebrow">Off-site replication</h4>
        <ToneWord tone={status.wanReachable ? 'ok' : 'bad'}>
          {status.wanReachable ? 'link reachable' : 'link unreachable'}
        </ToneWord>
      </div>

      <div className="grid grid-cols-2 gap-3">
        <div>
          <div className="text-xs text-muted-foreground">DR node</div>
          <div className="font-mono text-[13px]">{status.drNode}</div>
        </div>
        <div>
          <div className="text-xs text-muted-foreground">Endpoint</div>
          <div className="font-mono text-[13px] tabular-nums">
            {status.drEndpoint}
            {status.wanPort ? `:${status.wanPort}` : ''}
          </div>
        </div>
      </div>

      {legs.length > 0 && (
        <div className="mt-3">
          {/* One leg per primary-site replica: DRBD 9 is a full mesh, so
              whichever replica is Primary after a local failover needs its own
              path to the DR. Listing them separately keeps a dead tunnel from
              hiding behind a healthy one. */}
          <div className="mb-1.5 text-xs text-muted-foreground">Proxy legs</div>
          <div className="flex flex-wrap gap-2">
            {legs.map(([label, state]) => (
              <span key={label} className="inline-flex items-center gap-1.5">
                <ToneWord tone={state === 'active' ? 'ok' : 'bad'}>{state}</ToneWord>
                <span className="font-mono text-xs text-muted-foreground">{label}</span>
              </span>
            ))}
          </div>
        </div>
      )}

      <div className="mt-3">
        <div className="text-xs text-muted-foreground">
          Un-replicated backlog (data a DR failover would lose)
        </div>
        <div className="font-mono text-[15px] tabular-nums">
          {backlog === null ? (
            <span className="font-sans text-[13px] text-muted-foreground">
              unknown (proxy published no metrics)
            </span>
          ) : (
            formatBytes(backlog)
          )}
        </div>
      </div>

      <p className="mt-3 text-xs text-muted-foreground">
        Asynchronous (protocol A): the DR peer can lag, and it is never promoted
        automatically. Failover is the explicit <code className="font-mono">dr-failover</code>{' '}
        action.
      </p>
    </div>
  );
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ['KiB', 'MiB', 'GiB', 'TiB'];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i += 1;
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`;
}

// AddDRDialog attaches an off-site asynchronous replica to a running resource.
//
// This used to be a create-time-only decision, which is the wrong moment to have
// to make it: off-site DR is what an operator adds after a service has proven it
// matters. The dialog therefore asks only for the things the controller cannot
// work out — which node, and the address the primary site should dial.
function AddDRDialog({
  open,
  onOpenChange,
  resource,
  nodes,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  resource: Resource;
  nodes: NodeOpt[];
}) {
  const [drNode, setDrNode] = useState('');
  const [endpoint, setEndpoint] = useState('');
  const [wanPort, setWanPort] = useState('');
  const queryClient = useQueryClient();

  // A node already holding a replica or the quorum tiebreaker cannot also be
  // the DR site, so it is not worth offering.
  const taken = new Set([
    ...resource.nodes,
    ...(resource.disklessNodes ?? []),
    ...(resource.disklessClients ?? []),
  ]);
  const candidates = nodes.filter((n) => !taken.has(n.name));

  const add = useMutation({
    mutationFn: () =>
      api.addDR(resource.name, {
        drNode,
        drEndpoint: endpoint.trim(),
        wanPort: wanPort ? Number(wanPort) : 0,
      }),
    onSuccess: (res) => {
      if (!res.success) {
        toast.error(res.message || 'Failed to add DR site');
        return;
      }
      toast.success(
        `DR site "${drNode}" attached on WAN port ${res.wanPort}; initial sync runs in the background`,
      );
      queryClient.invalidateQueries({ queryKey: ['resources'] });
      queryClient.invalidateQueries({ queryKey: ['resource-status', resource.name] });
      onOpenChange(false);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Add DR site to "{resource.name}"</DialogTitle>
          <DialogDescription>
            An off-site copy kept asynchronously. The existing replicas keep
            their synchronous link and the resource keeps serving throughout.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <div className="space-y-2">
            <Label>DR node</Label>
            <select
              className="h-9 w-full rounded-md border bg-background px-3 text-sm"
              value={drNode}
              onChange={(e) => setDrNode(e.target.value)}
            >
              <option value="">Select a node…</option>
              {candidates.map((n) => (
                <option key={n.name} value={n.name}>
                  {n.name} ({n.address})
                </option>
              ))}
            </select>
            {candidates.length === 0 && (
              <p className="text-xs text-destructive">
                Every registered node already takes part in this resource.
                Register the DR machine first.
              </p>
            )}
          </div>

          <div className="space-y-2">
            <Label>Public endpoint</Label>
            <Input
              value={endpoint}
              onChange={(e) => setEndpoint(e.target.value)}
              placeholder="203.0.113.7 or dr.example.com"
            />
            <p className="text-xs text-muted-foreground">
              The address the primary site dials. It is often not the node's
              management address: a cloud node's public IP is usually NAT'd and
              absent from its own interfaces.
            </p>
          </div>

          <div className="space-y-2">
            <Label>WAN port (optional)</Label>
            <Input
              type="number"
              value={wanPort}
              onChange={(e) => setWanPort(e.target.value)}
              placeholder="auto-allocate"
            />
            <p className="text-xs text-muted-foreground">
              One port per replica is used from here. Whichever ports are chosen
              must be reachable on the DR node — open them in its firewall or
              security group first.
            </p>
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button
            onClick={() => add.mutate()}
            disabled={!drNode || !endpoint.trim() || add.isPending}
          >
            {add.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
            Add DR Site
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// DRFailoverDialog force-promotes the DR copy after the primary site is lost.
//
// The action is lossy by construction: replication is asynchronous, so writes
// the primary already acknowledged may never have crossed the WAN. How much is
// not a detail to bury in prose — it is the decision — so the current backlog is
// read live and shown next to the button, and "unknown" is shown as unknown
// rather than rendered as a reassuring zero.
function DRFailoverDialog({
  open,
  onOpenChange,
  resource,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  resource: Resource;
}) {
  const [confirm, setConfirm] = useState('');
  const queryClient = useQueryClient();

  const { data } = useQuery({
    queryKey: ['resource-status', resource.name],
    queryFn: () => api.resourceStatus(resource.name),
    enabled: open,
  });
  const status = data?.status;
  const drNode = status?.drNode ?? resource.drNode ?? '';
  const backlog = status?.wanMetrics?.bufferUsedBytes;

  const failover = useMutation({
    // There is no dedicated RPC: a DR failover IS a forced promote of the DR
    // node. Force is required because the DR peer is by definition not known to
    // be current relative to a primary site that is gone, and a plain promote
    // would refuse for exactly that reason.
    mutationFn: () => api.setPrimary(resource.name, drNode, true),
    onSuccess: (res) => {
      if (!res.success) {
        toast.error(res.message || 'DR failover failed');
        return;
      }
      toast.success(`"${resource.name}" promoted on DR node "${drNode}"`);
      queryClient.invalidateQueries({ queryKey: ['resources'] });
      queryClient.invalidateQueries({ queryKey: ['resource-status', resource.name] });
      onOpenChange(false);
      setConfirm('');
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>DR failover: promote "{resource.name}" off-site</DialogTitle>
          <DialogDescription>
            Use this when the primary site is lost. It force-promotes the DR
            copy, which is not automatic and not reversible by itself.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <div className="rounded-lg border p-3 text-sm">
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">DR node</span>
              <span className="font-medium">{drNode || '—'}</span>
            </div>
          </div>

          <div className="rounded-lg border border-destructive/40 bg-destructive/5 p-3">
            <p className="text-sm font-medium text-destructive">Data that will be lost</p>
            <p className="mt-1 text-lg font-semibold">
              {backlog === undefined ? (
                <span className="text-base font-normal text-muted-foreground">
                  unknown — the proxy published no metrics
                </span>
              ) : (
                formatBytes(Number(backlog))
              )}
            </p>
            <p className="mt-2 text-xs text-muted-foreground">
              Replication is asynchronous: the primary acknowledged these writes
              to its applications before they crossed the WAN. Promoting now
              accepts their loss.
            </p>
          </div>

          <div className="space-y-2">
            <Label>
              Type <span className="font-mono">{resource.name}</span> to confirm
            </Label>
            <Input value={confirm} onChange={(e) => setConfirm(e.target.value)} />
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button
            variant="destructive"
            onClick={() => failover.mutate()}
            disabled={confirm !== resource.name || !drNode || failover.isPending}
          >
            {failover.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
            Promote DR
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function SetRoleDialog({
  open,
  onOpenChange,
  resource,
  mode,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  resource: Resource;
  mode: 'primary' | 'secondary';
}) {
  const queryClient = useQueryClient();
  const [node, setNode] = useState(resource.nodes[0] ?? '');
  const [force, setForce] = useState(false);

  const mutation = useMutation({
    mutationFn: () =>
      mode === 'primary'
        ? api.setPrimary(resource.name, node, force)
        : api.setSecondary(resource.name, node),
    onSuccess: () => {
      toast.success(
        `${resource.name} set ${mode === 'primary' ? 'Primary' : 'Secondary'} on ${node}`,
      );
      queryClient.invalidateQueries({ queryKey: ['resources'] });
      onOpenChange(false);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            Set {mode === 'primary' ? 'Primary' : 'Secondary'} — {resource.name}
          </DialogTitle>
          <DialogDescription>
            Choose the node to promote to{' '}
            {mode === 'primary' ? 'Primary' : 'Secondary'}.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-4">
          <div className="space-y-2">
            <Label>Node</Label>
            <Select value={node} onValueChange={setNode}>
              <SelectTrigger>
                <SelectValue placeholder="Select a node..." />
              </SelectTrigger>
              <SelectContent>
                {resource.nodes.map((n) => (
                  <SelectItem key={n} value={n}>
                    {n}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          {mode === 'primary' && (
            <div className="flex items-center justify-between rounded-lg border p-3">
              <div className="space-y-0.5">
                <Label htmlFor="force-primary">Force</Label>
                <p className="text-xs text-muted-foreground">
                  Force promotion even if peers are unreachable.
                </p>
              </div>
              <Switch
                id="force-primary"
                checked={force}
                onCheckedChange={setForce}
              />
            </div>
          )}
        </div>

        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={mutation.isPending}
          >
            Cancel
          </Button>
          <Button
            onClick={() => mutation.mutate()}
            disabled={mutation.isPending || !node}
          >
            {mutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
            Apply
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function VolumesDialog({
  open,
  onOpenChange,
  resource,
  pools,
  defaultTab = 'volumes',
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  resource: Resource;
  pools: PoolOpt[];
  /** Which tab the dialog lands on — "Add volume" opens straight on the form. */
  defaultTab?: 'volumes' | 'add';
}) {
  const queryClient = useQueryClient();

  const { data, isLoading } = useQuery({
    queryKey: ['resource', resource.name],
    queryFn: () => api.getResource(resource.name),
    enabled: open,
  });

  const volumes = data?.resource?.volumes ?? resource.volumes;

  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: ['resource', resource.name] });
    queryClient.invalidateQueries({ queryKey: ['resources'] });
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Volumes — {resource.name}</DialogTitle>
          <DialogDescription>
            Manage volumes backing this resource.
          </DialogDescription>
        </DialogHeader>

        {/* Keyed so reopening on a different tab actually lands there: Radix
            keeps its own state for an uncontrolled `defaultValue`. */}
        <Tabs key={defaultTab} defaultValue={defaultTab} className="py-2">
          <TabsList>
            <TabsTrigger value="volumes">Volumes</TabsTrigger>
            <TabsTrigger value="add">Add Volume</TabsTrigger>
          </TabsList>

          <TabsContent value="volumes" className="space-y-2">
            {isLoading ? (
              <Skeleton className="h-24 w-full" />
            ) : volumes.length === 0 ? (
              <p className="py-4 text-center text-sm text-muted-foreground">
                No volumes.
              </p>
            ) : (
              volumes.map((vol) => (
                <VolumeRow
                  key={vol.volumeId}
                  resourceName={resource.name}
                  volume={vol}
                  onChanged={invalidate}
                />
              ))
            )}
          </TabsContent>

          <TabsContent value="add">
            <AddVolumeForm
              resourceName={resource.name}
              pools={pools}
              onAdded={invalidate}
            />
          </TabsContent>
        </Tabs>
      </DialogContent>
    </Dialog>
  );
}

function VolumeRow({
  resourceName,
  volume,
  onChanged,
}: {
  resourceName: string;
  volume: Volume;
  onChanged: () => void;
}) {
  const [resizing, setResizing] = useState(false);
  const [newSize, setNewSize] = useState(String(volume.sizeGb));

  const resizeMutation = useMutation({
    mutationFn: (sizeGb: number) =>
      api.resizeVolume(resourceName, volume.volumeId, sizeGb),
    onSuccess: () => {
      toast.success(`Volume ${volume.volumeId} resized`);
      onChanged();
      setResizing(false);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const removeMutation = useMutation({
    mutationFn: () => api.removeVolume(resourceName, volume.volumeId),
    onSuccess: () => {
      toast.success(`Volume ${volume.volumeId} removed`);
      onChanged();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <div className="rounded-lg border bg-muted/40 p-3">
      <div className="flex items-center justify-between">
        <div className="flex flex-col">
          <span className="text-sm font-medium">Volume {volume.volumeId}</span>
          <span className="font-mono text-xs text-muted-foreground">
            {volume.device}
          </span>
        </div>
        <div className="flex items-center gap-2">
          <Badge variant="secondary">{volume.sizeGb} GB</Badge>
          <Button
            variant="outline"
            size="sm"
            onClick={() => setResizing((v) => !v)}
          >
            Resize
          </Button>
          <AlertDialog>
            <AlertDialogTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-8 text-muted-foreground hover:text-destructive"
              >
                <Trash2 className="h-4 w-4" />
              </Button>
            </AlertDialogTrigger>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>
                  Remove volume {volume.volumeId}?
                </AlertDialogTitle>
                <AlertDialogDescription>
                  This permanently destroys the backing storage for volume{' '}
                  {volume.volumeId} of {resourceName}. This cannot be undone.
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>Cancel</AlertDialogCancel>
                <AlertDialogAction
                  onClick={() => removeMutation.mutate()}
                  className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
                >
                  Remove
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        </div>
      </div>

      {resizing && (
        <form
          className="mt-3 flex items-end gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            const size = parseInt(newSize, 10);
            if (Number.isFinite(size) && size > 0) resizeMutation.mutate(size);
          }}
        >
          <div className="flex-1 space-y-1">
            <Label htmlFor={`resize-${volume.volumeId}`}>New size (GB)</Label>
            <Input
              id={`resize-${volume.volumeId}`}
              type="number"
              min={1}
              value={newSize}
              onChange={(e) => setNewSize(e.target.value)}
            />
          </div>
          <Button type="submit" disabled={resizeMutation.isPending}>
            {resizeMutation.isPending && (
              <Loader2 className="h-4 w-4 animate-spin" />
            )}
            Apply
          </Button>
        </form>
      )}
    </div>
  );
}

function AddVolumeForm({
  resourceName,
  pools,
  onAdded,
}: {
  resourceName: string;
  pools: PoolOpt[];
  onAdded: () => void;
}) {
  const [volume, setVolume] = useState('');
  const [pool, setPool] = useState('');
  const [sizeGb, setSizeGb] = useState('10');

  const addMutation = useMutation({
    mutationFn: (data: { volume: string; pool: string; sizeGb: number }) =>
      api.addVolume(resourceName, data),
    onSuccess: () => {
      toast.success(`Volume "${volume}" added`);
      onAdded();
      setVolume('');
      setPool('');
      setSizeGb('10');
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    addMutation.mutate({
      volume,
      pool,
      sizeGb: parseInt(sizeGb, 10),
    });
  };

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div className="space-y-2">
        <Label htmlFor="add-vol-name">Volume Name</Label>
        <Input
          id="add-vol-name"
          value={volume}
          onChange={(e) => setVolume(e.target.value)}
          placeholder="e.g., data2"
          required
        />
      </div>
      <div className="space-y-2">
        <Label>Pool</Label>
        <Select value={pool} onValueChange={setPool}>
          <SelectTrigger>
            <SelectValue placeholder="Select a pool..." />
          </SelectTrigger>
          <SelectContent>
            {Array.from(
              pools
                .reduce((m, p) => {
                  const cur = m.get(p.name);
                  // A pool name is shared across the diskful nodes; show one
                  // entry per name with the tightest (minimum) free space, so
                  // SelectItem values stay unique and the trigger doesn't
                  // concatenate duplicate labels.
                  if (!cur || p.freeGb < cur.freeGb) m.set(p.name, p);
                  return m;
                }, new Map<string, PoolOpt>())
                .values()
            ).map((p) => (
              <SelectItem key={p.name} value={p.name}>
                {p.name} - {p.freeGb}GB free
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <div className="space-y-2">
        <Label htmlFor="add-vol-size">Size (GB)</Label>
        <Input
          id="add-vol-size"
          type="number"
          min={1}
          value={sizeGb}
          onChange={(e) => setSizeGb(e.target.value)}
          required
        />
      </div>
      <Button type="submit" disabled={addMutation.isPending || !pool}>
        {addMutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
        Add Volume
      </Button>
    </form>
  );
}

function MountDialog({
  open,
  onOpenChange,
  resource,
  nodes,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  resource: Resource;
  nodes: NodeOpt[];
}) {
  const multiVolume = resource.volumes.length > 1;
  const [volumeId, setVolumeId] = useState(
    String(resource.volumes[0]?.volumeId ?? 0),
  );

  const [fstype, setFstype] = useState('ext4');
  const [formatNode, setFormatNode] = useState('');
  const [mountPath, setMountPath] = useState('');
  const [mountNode, setMountNode] = useState('');
  const [unmountNode, setUnmountNode] = useState('');

  const vid = parseInt(volumeId, 10) || 0;
  const noneValue = '__none__';

  const formatMutation = useMutation({
    mutationFn: () =>
      api.createFilesystem(
        resource.name,
        vid,
        fstype,
        formatNode || undefined,
      ),
    onSuccess: () => toast.success('Filesystem created'),
    onError: (e: Error) => toast.error(e.message),
  });

  const mountMutation = useMutation({
    mutationFn: () =>
      api.mountResource(
        resource.name,
        vid,
        mountPath,
        undefined,
        mountNode || undefined,
      ),
    onSuccess: () => {
      toast.success(`Mounted ${resource.name} at ${mountPath}`);
      onOpenChange(false);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const unmountMutation = useMutation({
    mutationFn: () =>
      api.unmountResource(resource.name, vid, unmountNode || undefined),
    onSuccess: () => {
      toast.success(`Unmounted ${resource.name}`);
      onOpenChange(false);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Filesystem / Mount — {resource.name}</DialogTitle>
          <DialogDescription>
            Format, mount, or unmount the resource volume.
          </DialogDescription>
        </DialogHeader>

        {multiVolume && (
          <div className="space-y-2 pt-2">
            <Label>Volume</Label>
            <Select value={volumeId} onValueChange={setVolumeId}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {resource.volumes.map((v) => (
                  <SelectItem key={v.volumeId} value={String(v.volumeId)}>
                    Volume {v.volumeId} ({v.sizeGb} GB)
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        )}

        <Tabs defaultValue="format" className="py-2">
          <TabsList>
            <TabsTrigger value="format">Format</TabsTrigger>
            <TabsTrigger value="mount">Mount</TabsTrigger>
            <TabsTrigger value="unmount">Unmount</TabsTrigger>
          </TabsList>

          <TabsContent value="format" className="space-y-4">
            <div className="space-y-2">
              <Label>Filesystem Type</Label>
              <Select value={fstype} onValueChange={setFstype}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="ext4">ext4</SelectItem>
                  <SelectItem value="xfs">xfs</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label>Node (optional)</Label>
              <Select
                value={formatNode || noneValue}
                onValueChange={(v) =>
                  setFormatNode(v === noneValue ? '' : v)
                }
              >
                <SelectTrigger>
                  <SelectValue placeholder="Auto (Primary)" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={noneValue}>Auto (Primary)</SelectItem>
                  {nodes.map((n) => (
                    <SelectItem key={n.name} value={n.name}>
                      {n.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <Button
              onClick={() => formatMutation.mutate()}
              disabled={formatMutation.isPending}
            >
              {formatMutation.isPending && (
                <Loader2 className="h-4 w-4 animate-spin" />
              )}
              Create Filesystem
            </Button>
          </TabsContent>

          <TabsContent value="mount" className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="mount-path">Mount Path</Label>
              <Input
                id="mount-path"
                value={mountPath}
                onChange={(e) => setMountPath(e.target.value)}
                placeholder="/mnt/data"
              />
            </div>
            <div className="space-y-2">
              <Label>Node (optional)</Label>
              <Select
                value={mountNode || noneValue}
                onValueChange={(v) => setMountNode(v === noneValue ? '' : v)}
              >
                <SelectTrigger>
                  <SelectValue placeholder="Auto (Primary)" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={noneValue}>Auto (Primary)</SelectItem>
                  {nodes.map((n) => (
                    <SelectItem key={n.name} value={n.name}>
                      {n.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <Button
              onClick={() => mountMutation.mutate()}
              disabled={mountMutation.isPending || !mountPath}
            >
              {mountMutation.isPending && (
                <Loader2 className="h-4 w-4 animate-spin" />
              )}
              Mount
            </Button>
          </TabsContent>

          <TabsContent value="unmount" className="space-y-4">
            <div className="space-y-2">
              <Label>Node (optional)</Label>
              <Select
                value={unmountNode || noneValue}
                onValueChange={(v) =>
                  setUnmountNode(v === noneValue ? '' : v)
                }
              >
                <SelectTrigger>
                  <SelectValue placeholder="Auto (Primary)" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={noneValue}>Auto (Primary)</SelectItem>
                  {nodes.map((n) => (
                    <SelectItem key={n.name} value={n.name}>
                      {n.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <Button
              variant="destructive"
              onClick={() => unmountMutation.mutate()}
              disabled={unmountMutation.isPending}
            >
              {unmountMutation.isPending && (
                <Loader2 className="h-4 w-4 animate-spin" />
              )}
              Unmount
            </Button>
          </TabsContent>
        </Tabs>
      </DialogContent>
    </Dialog>
  );
}

function DeleteResourceDialog({
  open,
  onOpenChange,
  resourceName,
  csiManaged = false,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  resourceName: string;
  csiManaged?: boolean;
}) {
  const queryClient = useQueryClient();

  const deleteMutation = useMutation({
    mutationFn: () => api.deleteResource(resourceName),
    onSuccess: () => {
      toast.success(`Resource "${resourceName}" deleted`);
      queryClient.invalidateQueries({ queryKey: ['resources'] });
      onOpenChange(false);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete resource "{resourceName}"?</AlertDialogTitle>
          <AlertDialogDescription>
            This removes the DRBD resource and destroys all backing volumes and
            their data on every node. This action cannot be undone.
          </AlertDialogDescription>
          {csiManaged && (
            <AlertDialogDescription className="mt-2 rounded border border-amber-500/40 bg-amber-500/10 p-2 text-amber-700 dark:text-amber-400">
              This volume was provisioned by the Kubernetes CSI driver. Deleting
              it here leaves the PersistentVolume that still references it
              stranded, and Kubernetes will not recreate the data. Delete the
              PersistentVolumeClaim instead and let the driver clean up.
            </AlertDialogDescription>
          )}
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            onClick={(e) => {
              e.preventDefault();
              deleteMutation.mutate();
            }}
            className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
          >
            {deleteMutation.isPending && (
              <Loader2 className="h-4 w-4 animate-spin" />
            )}
            Delete
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

function CreateResourceDialog({
  open,
  onOpenChange,
  nodes,
  pools,
  profiles,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  nodes: NodeOpt[];
  pools: PoolOpt[];
  profiles: ResourceProfile[];
}) {
  const queryClient = useQueryClient();
  const [name, setName] = useState('');
  const [port, setPort] = useState('7000');
  const [protocol, setProtocol] = useState('C');
  const [selectedNodes, setSelectedNodes] = useState<string[]>([]);
  const [storageType, setStorageType] = useState('lvm');
  const [profile, setProfile] = useState('');
  const [labelsInput, setLabelsInput] = useState('');
  // One or more DRBD volumes (volume 0..N). Each has its own size and pool.
  const [volumes, setVolumes] = useState<{ sizeGb: string; pool: string }[]>([
    { sizeGb: '10', pool: '' },
  ]);
  // DRBD options as section/key -> value rows (net/disk/options), optional.
  const [optionRows, setOptionRows] = useState<{ key: string; value: string }[]>(
    [],
  );
  const [showOptions, setShowOptions] = useState(false);

  const noneValue = '__none__';

  const setVolume = (i: number, patch: Partial<{ sizeGb: string; pool: string }>) =>
    setVolumes((prev) =>
      prev.map((v, idx) => (idx === i ? { ...v, ...patch } : v)),
    );
  const addVolume = () =>
    setVolumes((prev) => [...prev, { sizeGb: '10', pool: '' }]);
  const removeVolume = (i: number) =>
    setVolumes((prev) => prev.filter((_, idx) => idx !== i));

  const setOptionRow = (
    i: number,
    patch: Partial<{ key: string; value: string }>,
  ) =>
    setOptionRows((prev) =>
      prev.map((r, idx) => (idx === i ? { ...r, ...patch } : r)),
    );
  const addOptionRow = () =>
    setOptionRows((prev) => [...prev, { key: '', value: '' }]);
  const removeOptionRow = (i: number) =>
    setOptionRows((prev) => prev.filter((_, idx) => idx !== i));

  // Pool types as the backend reports them, per storage type.
  const poolTypeFor: Record<string, string> = {
    lvm: 'vg',
    'lvm-thin': 'thin_pool',
    zfs: 'zfs',
  };
  const matchingPools = pools.filter(
    (p) => p.type === poolTypeFor[storageType],
  );
  const selectedProfile = profiles.find((item) => item.name === profile);

  const reset = () => {
    setName('');
    setPort('7000');
    setProtocol('C');
    setSelectedNodes([]);
    setStorageType('lvm');
    setProfile('');
    setLabelsInput('');
    setVolumes([{ sizeGb: '10', pool: '' }]);
    setOptionRows([]);
    setShowOptions(false);
  };

  const createMutation = useMutation({
    mutationFn: (data: Parameters<typeof api.createResource>[0]) =>
      api.createResource(data),
    onSuccess: () => {
      toast.success(`Resource "${name}" created`);
      queryClient.invalidateQueries({ queryKey: ['resources'] });
      onOpenChange(false);
      reset();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const toggleNode = (nodeName: string) => {
    setSelectedNodes((prev) =>
      prev.includes(nodeName)
        ? prev.filter((n) => n !== nodeName)
        : [...prev, nodeName],
    );
  };

  const selectProfile = (value: string) => {
    if (value === noneValue) {
      setProfile('');
      return;
    }
    setProfile(value);
    const selected = profiles.find((item) => item.name === value);
    if (!selected) return;
    if (selected.protocol) setProtocol(selected.protocol);
    if (selected.storageType) setStorageType(selected.storageType);
    if (selected.pool) {
      setVolumes((prev) => prev.map((volume) => ({ ...volume, pool: selected.pool })));
    }
  };

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (selectedNodes.length < 1) {
      toast.error('Select at least 1 node');
      return;
    }
    const parsedVolumes = volumes.map((v) => ({
      sizeGb: parseInt(v.sizeGb, 10),
      pool: v.pool || undefined,
    }));
    if (parsedVolumes.some((v) => !v.sizeGb || v.sizeGb < 1)) {
      toast.error('Every volume needs a size of at least 1 GB');
      return;
    }
    // Collect non-empty option rows into a section/key -> value map.
    const drbdOptions: Record<string, string> = {};
    for (const r of optionRows) {
      const key = r.key.trim();
      if (key) drbdOptions[key] = r.value.trim();
    }
    const labels: Record<string, string> = {};
    for (const part of labelsInput.split(',')) {
      const entry = part.trim();
      if (!entry) continue;
      const separator = entry.indexOf('=');
      if (separator < 1) {
        toast.error(`Invalid label "${entry}". Use key=value.`);
        return;
      }
      const key = entry.slice(0, separator).trim();
      const value = entry.slice(separator + 1).trim();
      if (!key) {
        toast.error(`Invalid label "${entry}". Label keys cannot be empty.`);
        return;
      }
      labels[key] = value;
    }
    createMutation.mutate({
      name,
      port: parseInt(port, 10),
      nodes: selectedNodes,
      protocol,
      storageType,
      volumes: parsedVolumes,
      drbdOptions: Object.keys(drbdOptions).length ? drbdOptions : undefined,
      profile: profile || undefined,
      labels: Object.keys(labels).length ? labels : undefined,
    });
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] overflow-y-auto">
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>Create DRBD Resource</DialogTitle>
            <DialogDescription>
              Define a replicated DRBD resource across one or more nodes.
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label htmlFor="res-name">Resource Name</Label>
              <Input
                id="res-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="e.g., data"
                required
              />
            </div>

            <div className="space-y-2">
              <Label>Profile (optional)</Label>
              <Select
                value={profile || noneValue}
                onValueChange={selectProfile}
              >
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="No profile" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={noneValue}>No profile</SelectItem>
                  {profiles.map((item) => (
                    <SelectItem key={item.name} value={item.name}>
                      {item.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {profiles.length === 0 && (
                <p className="text-xs text-muted-foreground">
                  No resource profiles are configured.
                </p>
              )}
            </div>

            <div className="space-y-2">
              <Label htmlFor="res-labels">Labels (optional)</Label>
              <Input
                id="res-labels"
                value={labelsInput}
                onChange={(e) => setLabelsInput(e.target.value)}
                placeholder="environment=prod, team=storage"
                className="font-mono text-sm"
              />
              <p className="text-xs text-muted-foreground">
                Comma-separated key=value pairs. Explicit labels override profile labels.
              </p>
            </div>

            <div className="grid grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label htmlFor="res-port">Port</Label>
                <Input
                  id="res-port"
                  type="number"
                  value={port}
                  onChange={(e) => setPort(e.target.value)}
                  required
                />
              </div>
              <div className="space-y-2">
                <Label>Protocol</Label>
                <Select value={protocol} onValueChange={setProtocol}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="C">C (Sync)</SelectItem>
                    <SelectItem value="A">A (Async)</SelectItem>
                    <SelectItem value="B">B (Semi-sync)</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>

            <div className="space-y-2">
              <Label>Nodes</Label>
              <div className="space-y-2 rounded-lg border p-3">
                {nodes.map((node) => (
                  <label
                    key={node.name}
                    className="flex cursor-pointer items-center gap-2 text-sm"
                  >
                    <input
                      type="checkbox"
                      className="h-4 w-4 rounded border-input accent-primary"
                      checked={selectedNodes.includes(node.name)}
                      onChange={() => toggleNode(node.name)}
                    />
                    <span>
                      {node.name}{' '}
                      <span className="text-muted-foreground">
                        ({node.address})
                      </span>
                    </span>
                  </label>
                ))}
                {nodes.length === 0 && (
                  <p className="text-sm text-muted-foreground">
                    No nodes available.
                  </p>
                )}
              </div>
              {selectedNodes.length === 1 && (
                <p className="text-xs text-amber-600 dark:text-amber-400">
                  Only 1 node selected — this resource will have no replication.
                </p>
              )}
            </div>

            <div className="space-y-2">
              <Label>Storage Type</Label>
              <Select
                value={storageType}
                onValueChange={(v) => {
                  setStorageType(v);
                  // Pools are type-specific; clear each volume's pick.
                  setVolumes((prev) => prev.map((vol) => ({ ...vol, pool: '' })));
                }}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="lvm">LVM</SelectItem>
                  <SelectItem value="lvm-thin">LVM Thin</SelectItem>
                  <SelectItem value="zfs">ZFS</SelectItem>
                </SelectContent>
              </Select>
            </div>

            {/* Volumes (volume 0..N) */}
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <Label>Volumes</Label>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={addVolume}
                >
                  <Plus className="h-4 w-4" />
                  Add volume
                </Button>
              </div>
              <div className="space-y-2 rounded-lg border p-3">
                {volumes.map((vol, i) => {
                  const effectivePool = vol.pool || selectedProfile?.pool || '';
                  return (
                  <div key={i} className="flex items-end gap-2">
                    <div className="w-24 space-y-1">
                      <Label className="text-xs text-muted-foreground">
                        {i === 0 ? 'Size (GB)' : `Vol ${i} · GB`}
                      </Label>
                      <Input
                        type="number"
                        min={1}
                        value={vol.sizeGb}
                        onChange={(e) => setVolume(i, { sizeGb: e.target.value })}
                        required
                      />
                    </div>
                    <div className="flex-1 space-y-1">
                      <Label className="text-xs text-muted-foreground">
                        Pool (optional)
                      </Label>
                      <Select
                        value={effectivePool || noneValue}
                        onValueChange={(v) =>
                          setVolume(i, { pool: v === noneValue ? '' : v })
                        }
                      >
                        <SelectTrigger className="w-full">
                          <SelectValue placeholder="Auto-select" />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value={noneValue}>Auto-select</SelectItem>
                          {effectivePool && !matchingPools.some((p) => p.name === effectivePool) && (
                            <SelectItem value={effectivePool}>
                              {effectivePool} (profile)
                            </SelectItem>
                          )}
                          {matchingPools.map((p) => (
                            <SelectItem
                              key={`${p.node}-${p.name}`}
                              value={p.name}
                            >
                              {p.name} ({p.node}) - {p.freeGb}GB free
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      className="h-9 w-9 shrink-0 text-muted-foreground hover:text-destructive"
                      onClick={() => removeVolume(i)}
                      disabled={volumes.length === 1}
                      title="Remove volume"
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>
                  );
                })}
              </div>
            </div>

            {/* DRBD options (advanced) */}
            <div className="space-y-2">
              <button
                type="button"
                className="text-sm font-medium text-muted-foreground hover:text-foreground"
                onClick={() => setShowOptions((s) => !s)}
              >
                {showOptions ? '▾' : '▸'} DRBD Options (advanced)
              </button>
              {showOptions && (
                <div className="space-y-2 rounded-lg border p-3">
                  <p className="text-xs text-muted-foreground">
                    Keys are <code className="font-mono">section/key</code> —
                    e.g. <code className="font-mono">net/max-buffers</code>,{' '}
                    <code className="font-mono">disk/on-io-error</code>,{' '}
                    <code className="font-mono">options/auto-promote</code>. See
                    the DRBD 9 user guide.
                  </p>
                  {optionRows.map((row, i) => (
                    <div key={i} className="flex items-center gap-2">
                      <Input
                        className="flex-1"
                        placeholder="net/max-buffers"
                        value={row.key}
                        onChange={(e) => setOptionRow(i, { key: e.target.value })}
                      />
                      <Input
                        className="flex-1"
                        placeholder="8000"
                        value={row.value}
                        onChange={(e) =>
                          setOptionRow(i, { value: e.target.value })
                        }
                      />
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        className="h-9 w-9 shrink-0 text-muted-foreground hover:text-destructive"
                        onClick={() => removeOptionRow(i)}
                        title="Remove option"
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    </div>
                  ))}
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={addOptionRow}
                  >
                    <Plus className="h-4 w-4" />
                    Add option
                  </Button>
                </div>
              )}
            </div>
          </div>

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={createMutation.isPending}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={createMutation.isPending || selectedNodes.length < 1}
            >
              {createMutation.isPending && (
                <Loader2 className="h-4 w-4 animate-spin" />
              )}
              Create
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
