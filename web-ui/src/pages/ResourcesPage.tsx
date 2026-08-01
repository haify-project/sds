import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  api,
  Resource,
  ResourceProfile,
  Volume,
  ResourceStatus,
  NodeResourceState,
  QuorumInfo,
} from '../services/api';
import { StatusBadge } from '@/components/StatusBadge';
import { ResourceTopology } from '@/components/ResourceTopology';
import { ResourceProfilesPage } from './ResourceProfilesPage';
import { useSearchParams } from 'react-router-dom';
import { SnapshotsDialog } from '@/components/SnapshotsDialog';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
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
  DialogTrigger,
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
  Star,
  ChevronDown,
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
  Network,
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

  // Profiles live here rather than in their own nav entry: they are templates
  // for resources and do nothing on their own, so they belong beside the things
  // they create. It also matches the CLI, where the command has always been
  // `sds resource profile`.
  const [params, setParams] = useSearchParams();
  const tab = params.get('tab') === 'profiles' ? 'profiles' : 'resources';

  return (
    <Tabs
      value={tab}
      onValueChange={(v) => setParams(v === 'profiles' ? { tab: 'profiles' } : {}, { replace: true })}
      className="space-y-6"
    >
      <TabsList>
        <TabsTrigger value="resources">Resources</TabsTrigger>
        <TabsTrigger value="profiles">Profiles</TabsTrigger>
      </TabsList>

      <TabsContent value="profiles" className="space-y-6">
        <ResourceProfilesPage />
      </TabsContent>

      <TabsContent value="resources" className="space-y-6">
      <div className="flex items-center justify-between">
        <h3 className="text-lg font-semibold">DRBD Resources</h3>
        <CreateResourceDialog
          nodes={nodes?.nodes ?? []}
          pools={pools?.pools ?? []}
          profiles={profiles?.profiles ?? []}
        />
      </div>

      <Card>
        {isLoading ? (
          <div className="space-y-3 p-4">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-10 w-full" />
            ))}
          </div>
        ) : !resources?.resources.length ? (
          <div className="flex flex-col items-center justify-center gap-3 py-16 text-muted-foreground">
            <Boxes className="h-10 w-10" />
            <p>No resources found. Create your first resource to get started.</p>
          </div>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Resource</TableHead>
                <TableHead>Port</TableHead>
                <TableHead>Protocol</TableHead>
                <TableHead>Nodes</TableHead>
                <TableHead>Volumes</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {resources.resources.map((resource) => (
                <ResourceRow
                  key={resource.name}
                  resource={resource}
                  pools={pools?.pools ?? []}
                  nodes={nodes?.nodes ?? []}
                />
              ))}
            </TableBody>
          </Table>
        )}
      </Card>
      </TabsContent>
    </Tabs>
  );
}

// SyncIndicator renders a compact "resyncing" badge for a resource in the list,
// shown only while a peer is actively resyncing. It has its own adaptive query
// so only resources that are syncing keep polling (2s), stopping at 100%.
function SyncIndicator({
  resourceName,
  localNode,
}: {
  resourceName: string;
  localNode?: string;
}) {
  const { data } = useQuery({
    queryKey: ['resource-status', resourceName],
    queryFn: () => api.resourceStatus(resourceName),
    refetchInterval: syncPollInterval,
  });
  const status = data?.status;
  if (!statusHasActiveSync(status)) return null;

  const syncing = Object.entries(status!.nodeStates || {}).find(([, st]) =>
    isPeerSyncing(st),
  );
  if (!syncing) return null;
  const [peer, st] = syncing;
  const pct = st.syncPercent ?? 0;
  // Direction: a SyncSource peer means the local node is the source
  // (local → peer); a SyncTarget peer means the local node is receiving
  // (peer → local).
  const local = localNode ?? 'local';
  const flow =
    st.replicationState === 'SyncTarget'
      ? `${peer} → ${local}`
      : `${local} → ${peer}`;

  return (
    <Badge
      variant="outline"
      className="gap-1 border-blue-500 text-blue-600"
      title={`Resync in progress: ${st.replicationState}`}
    >
      <Loader2 className="h-3 w-3 animate-spin" />
      同步中 {flow} {pct.toFixed(0)}%
    </Badge>
  );
}

function ResourceRow({
  resource,
  pools,
  nodes,
}: {
  resource: Resource;
  pools: PoolOpt[];
  nodes: NodeOpt[];
}) {
  const [expanded, setExpanded] = useState(false);

  return (
    <>
    <TableRow
      data-state={expanded ? 'selected' : undefined}
      className={expanded ? 'border-b-0' : undefined}
    >
      <TableCell>
        <div className="flex items-start gap-2">
          <span className="flex h-8 w-8 items-center justify-center rounded bg-primary/10">
            <Database className="h-4 w-4 text-primary" />
          </span>
          <div className="min-w-0 space-y-1 whitespace-normal">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-medium">{resource.name}</span>
              {resource.quorumRisk && (
                <Badge
                  variant="outline"
                  className="border-amber-500 text-amber-600"
                  title="2-node resource with no quorum tiebreaker: a single node failure suspends I/O"
                >
                  quorum risk
                </Badge>
              )}
              <SyncIndicator
                resourceName={resource.name}
                localNode={resource.nodes[0]}
              />
            </div>
            <ResourceMetadata resource={resource} compact />
          </div>
        </div>
      </TableCell>
      <TableCell className="text-muted-foreground">{resource.port}</TableCell>
      <TableCell>
        <Badge variant="secondary">{resource.protocol}</Badge>
      </TableCell>
      <TableCell>
        <div className="flex flex-wrap gap-1">
          {resource.nodes.map((node) => {
            const state = resource.nodeStates?.[node];
            const isPrimary = state?.role === 'Primary';
            // The off-site copy is a replica like the others to DRBD, but not to
            // an operator: it replicates asynchronously and never takes over on
            // its own, so it must not read as a peer that failover can land on.
            const isDR = resource.wanMode && node === resource.drNode;
            return (
              <span key={node} className="inline-flex items-center gap-1">
                {isPrimary && (
                  <Star className="h-3 w-3 fill-amber-400 text-amber-400" />
                )}
                <StatusBadge status={state?.role ?? node} />
                {isDR && (
                  <Badge
                    variant="outline"
                    className="border-sky-500 text-sky-600"
                    title="Off-site disaster-recovery replica: asynchronous (protocol A), reached over a WAN proxy leg, and promoted only by an explicit dr-failover"
                  >
                    DR
                  </Badge>
                )}
                {state?.role && (
                  <span className="text-xs text-muted-foreground">{node}</span>
                )}
              </span>
            );
          })}
          {resource.disklessNodes?.map((node) => (
            <span key={node} className="inline-flex items-center gap-1">
              <Badge variant="outline" title="Diskless quorum tiebreaker (votes for quorum only, never promoted or mounted)">
                tiebreaker
              </Badge>
              <span className="text-xs text-muted-foreground">{node}</span>
            </span>
          ))}
          {resource.disklessClients?.map((node) => {
            const state = resource.nodeStates?.[node];
            const isPrimary = state?.role === 'Primary';
            return (
              <span key={node} className="inline-flex items-center gap-1">
                {isPrimary && (
                  <Star className="h-3 w-3 fill-amber-400 text-amber-400" />
                )}
                <Badge
                  variant="outline"
                  className="border-sky-500/40 text-sky-600 dark:text-sky-400"
                  title="Diskless data client: no local replica, mounts the volume over the DRBD network (e.g. a Kubernetes/CSI Pod on a non-replica node)"
                >
                  <Network className="mr-1 h-3 w-3" />
                  data client
                </Badge>
                <span className="text-xs text-muted-foreground">{node}</span>
              </span>
            );
          })}
        </div>
      </TableCell>
      <TableCell className="text-muted-foreground">
        {resource.volumes.length}
      </TableCell>
      <TableCell className="text-right">
        <div className="flex items-center justify-end gap-1">
          <Button
            variant="outline"
            size="sm"
            onClick={() => setExpanded((v) => !v)}
            aria-expanded={expanded}
            aria-label={`${expanded ? 'Hide' : 'Show'} status for ${resource.name}`}
          >
            <ChevronDown
              className={`h-4 w-4 transition-transform ${expanded ? 'rotate-180' : ''}`}
            />
            Status
          </Button>
          <ResourceActionsMenu
            resource={resource}
            pools={pools}
            nodes={nodes}
          />
        </div>
      </TableCell>
    </TableRow>
    {expanded && (
      <TableRow className="hover:bg-transparent">
        {/* colSpan spans the whole table so the detail is not squeezed into one
            column; the panel below lays itself out. */}
        <TableCell colSpan={6} className="bg-muted/30 p-4">
          {/* The detail is read top-to-bottom, so cap it at a readable measure
              rather than letting it stretch across a wide table. */}
          <div className="max-w-3xl">
            <ResourceStatusPanel resource={resource} />
          </div>
        </TableCell>
      </TableRow>
    )}
    </>
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
    <div className="flex max-w-full flex-wrap items-center gap-1 text-xs text-muted-foreground">
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

function ResourceActionsMenu({
  resource,
  pools,
  nodes,
}: {
  resource: Resource;
  pools: PoolOpt[];
  nodes: NodeOpt[];
}) {
  const [primaryOpen, setPrimaryOpen] = useState(false);
  const [secondaryOpen, setSecondaryOpen] = useState(false);
  const [volumesOpen, setVolumesOpen] = useState(false);
  const [snapshotsOpen, setSnapshotsOpen] = useState(false);
  const [mountOpen, setMountOpen] = useState(false);
  const [optionsOpen, setOptionsOpen] = useState(false);
  const [scheduleOpen, setScheduleOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [addDrOpen, setAddDrOpen] = useState(false);
  const [drFailoverOpen, setDrFailoverOpen] = useState(false);

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="outline" size="icon" className="h-8 w-8">
            <MoreHorizontal className="h-4 w-4" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-48">
          <DropdownMenuLabel>{resource.name}</DropdownMenuLabel>
          <DropdownMenuSeparator />
          <DropdownMenuItem onSelect={() => setPrimaryOpen(true)}>
            <ArrowUpCircle className="h-4 w-4" />
            Set Primary
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => setSecondaryOpen(true)}>
            <ArrowDownCircle className="h-4 w-4" />
            Set Secondary
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => setVolumesOpen(true)}>
            <Database className="h-4 w-4" />
            Volumes
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => setSnapshotsOpen(true)}>
            <Camera className="h-4 w-4" />
            Snapshots
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => setMountOpen(true)}>
            <FolderCog className="h-4 w-4" />
            Filesystem / Mount
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => setOptionsOpen(true)}>
            <SlidersHorizontal className="h-4 w-4" />
            Edit DRBD Options
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => setScheduleOpen(true)}>
            <CalendarClock className="h-4 w-4" />
            Snapshot Schedule
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          {/* Off-site DR: attach one if there is none, fail over to it if there
              is. The two are mutually exclusive states of the same resource, so
              only one of them is ever offered. */}
          {resource.wanMode ? (
            <DropdownMenuItem variant="destructive" onSelect={() => setDrFailoverOpen(true)}>
              <Globe className="h-4 w-4" />
              DR Failover
            </DropdownMenuItem>
          ) : (
            <DropdownMenuItem onSelect={() => setAddDrOpen(true)}>
              <Globe className="h-4 w-4" />
              Add DR Site
            </DropdownMenuItem>
          )}
          <DropdownMenuSeparator />
          <DropdownMenuItem
            variant="destructive"
            onSelect={() => setDeleteOpen(true)}
          >
            <Trash2 className="h-4 w-4" />
            Delete
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <AddDRDialog
        open={addDrOpen}
        onOpenChange={setAddDrOpen}
        resource={resource}
        nodes={nodes}
      />
      <DRFailoverDialog
        open={drFailoverOpen}
        onOpenChange={setDrFailoverOpen}
        resource={resource}
      />
      <SetRoleDialog
        open={primaryOpen}
        onOpenChange={setPrimaryOpen}
        resource={resource}
        mode="primary"
      />
      <SetRoleDialog
        open={secondaryOpen}
        onOpenChange={setSecondaryOpen}
        resource={resource}
        mode="secondary"
      />
      <SnapshotsDialog
        resource={resource.name}
        open={snapshotsOpen}
        onOpenChange={setSnapshotsOpen}
      />
      <VolumesDialog
        open={volumesOpen}
        onOpenChange={setVolumesOpen}
        resource={resource}
        pools={pools}
      />
      <MountDialog
        open={mountOpen}
        onOpenChange={setMountOpen}
        resource={resource}
        nodes={nodes}
      />
      <EditOptionsDialog
        open={optionsOpen}
        onOpenChange={setOptionsOpen}
        resource={resource}
      />
      <ScheduleDialog
        open={scheduleOpen}
        onOpenChange={setScheduleOpen}
        resource={resource}
      />
      <DeleteResourceDialog
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
        resourceName={resource.name}
      />
    </>
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
    <div className="rounded-lg border p-3">
      <div className="mb-2 flex items-center justify-between">
        <h4 className="text-sm font-medium">Quorum</h4>
        <Badge
          variant="outline"
          className={
            hasQuorum
              ? 'border-emerald-500 text-emerald-600'
              : 'border-destructive text-destructive'
          }
          title={
            hasQuorum
              ? 'DRBD reports this node holds quorum; I/O proceeds'
              : 'DRBD reports no quorum on this node; I/O is suspended'
          }
        >
          {hasQuorum ? 'quorum held' : 'no quorum'}
        </Badge>
      </div>

      <div className="grid grid-cols-3 gap-3 text-sm">
        <div>
          <div className="text-xs text-muted-foreground">Members</div>
          <div className="font-medium">{members}</div>
        </div>
        <div>
          <div className="text-xs text-muted-foreground">Votes needed</div>
          <div className="font-medium">{required}</div>
        </div>
        <div>
          <div className="text-xs text-muted-foreground">Online</div>
          <div className="font-medium">
            {online}
            <span className="text-muted-foreground"> / {members}</span>
          </div>
        </div>
      </div>

      <p
        className={`mt-3 text-sm ${
          tolerated > 0 ? 'text-muted-foreground' : 'text-amber-600'
        }`}
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
    <div className="rounded-lg border p-3">
      <div className="mb-2 flex items-center justify-between">
        <h4 className="text-sm font-medium">Off-site replication</h4>
        <Badge
          variant="outline"
          className={
            status.wanReachable
              ? 'border-sky-500 text-sky-600'
              : 'border-destructive text-destructive'
          }
        >
          {status.wanReachable ? 'link reachable' : 'link unreachable'}
        </Badge>
      </div>

      <div className="grid grid-cols-2 gap-3 text-sm">
        <div>
          <div className="text-xs text-muted-foreground">DR node</div>
          <div className="font-medium">{status.drNode}</div>
        </div>
        <div>
          <div className="text-xs text-muted-foreground">Endpoint</div>
          <div className="font-medium">
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
          <div className="mb-1 text-xs text-muted-foreground">Proxy legs</div>
          <div className="flex flex-wrap gap-1">
            {legs.map(([label, state]) => (
              <span key={label} className="inline-flex items-center gap-1">
                <Badge
                  variant="outline"
                  className={
                    state === 'active'
                      ? 'border-emerald-500 text-emerald-600'
                      : 'border-destructive text-destructive'
                  }
                >
                  {state}
                </Badge>
                <span className="text-xs text-muted-foreground">{label}</span>
              </span>
            ))}
          </div>
        </div>
      )}

      <div className="mt-3">
        <div className="text-xs text-muted-foreground">
          Un-replicated backlog (data a DR failover would lose)
        </div>
        <div className="font-medium">
          {backlog === null ? (
            <span className="text-muted-foreground">
              unknown (proxy published no metrics)
            </span>
          ) : (
            formatBytes(backlog)
          )}
        </div>
      </div>

      <p className="mt-3 text-xs text-muted-foreground">
        Asynchronous (protocol A): the DR peer can lag, and it is never promoted
        automatically. Failover is the explicit <code>dr-failover</code> action.
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

// ResourceStatusPanel renders a resource's live status inline, under its row.
//
// It used to be a modal, which forced a choice the operator should not have to
// make: read one resource's detail, or see the list. Comparing two resources
// meant opening and closing dialogs and holding the first in your head. Expanded
// rows let several be open at once and keep every one in the context of the
// table it belongs to.
function ResourceStatusPanel({ resource }: { resource: Resource }) {
  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['resource-status', resource.name],
    queryFn: () => api.resourceStatus(resource.name),
    // Poll 2s while any peer is resyncing; stop the moment it settles. Only
    // mounted panels query, so a collapsed row costs nothing.
    refetchInterval: syncPollInterval,
  });

  const status = data?.status;

  return (
    <>
        {isLoading ? (
          <div className="space-y-3 py-2">
            <Skeleton className="h-6 w-full" />
            <Skeleton className="h-24 w-full" />
            <Skeleton className="h-24 w-full" />
          </div>
        ) : isError ? (
          <p className="py-4 text-sm text-destructive">
            {(error as Error).message}
          </p>
        ) : status ? (
          <div className="space-y-5 py-2">
            <div className="flex items-center justify-between rounded-lg border bg-muted/40 p-3">
              <span className="text-sm text-muted-foreground">Overall Role</span>
              <StatusBadge status={status.role} />
            </div>

            <ResourceTopology resource={resource} status={status} />

            {status.quorum && <QuorumPanel quorum={status.quorum} />}

            {status.wan && <WANPanel status={status} />}

            {(resource.profile || Object.keys(resource.labels ?? {}).length > 0) && (
              <div>
                <h4 className="mb-2 text-sm font-medium">Metadata</h4>
                <ResourceMetadata resource={resource} />
              </div>
            )}

            <div>
              <h4 className="mb-2 text-sm font-medium">Node States</h4>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Node</TableHead>
                    <TableHead>Role</TableHead>
                    <TableHead>Disk</TableHead>
                    <TableHead>Replication</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {Object.entries(status.nodeStates || {}).map(
                    ([node, state]) => (
                      <TableRow key={node}>
                        <TableCell className="font-medium">{node}</TableCell>
                        <TableCell>
                          <StatusBadge status={state.role} />
                        </TableCell>
                        <TableCell>
                          <StatusBadge status={state.diskState} />
                        </TableCell>
                        <TableCell>
                          {state.replicationState ? (
                            isPeerSyncing(state) ? (
                              <div className="flex min-w-[150px] items-center gap-2">
                                <StatusBadge
                                  status={state.replicationState}
                                />
                                <div className="h-1.5 flex-1 overflow-hidden rounded bg-muted">
                                  <div
                                    className="h-full rounded bg-primary transition-all"
                                    style={{
                                      width: `${Math.min(
                                        100,
                                        Math.max(0, state.syncPercent ?? 0),
                                      )}%`,
                                    }}
                                  />
                                </div>
                                <span className="text-xs tabular-nums text-muted-foreground">
                                  {(state.syncPercent ?? 0).toFixed(1)}%
                                </span>
                              </div>
                            ) : (
                              <StatusBadge status={state.replicationState} />
                            )
                          ) : (
                            <span className="text-xs text-muted-foreground">
                              —
                            </span>
                          )}
                        </TableCell>
                      </TableRow>
                    ),
                  )}
                </TableBody>
              </Table>
            </div>

            <div>
              <h4 className="mb-2 text-sm font-medium">Volumes</h4>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>ID</TableHead>
                    <TableHead>Device</TableHead>
                    <TableHead>Size</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {status.volumes?.map((vol) => (
                    <TableRow key={vol.volumeId}>
                      <TableCell>{vol.volumeId}</TableCell>
                      <TableCell className="font-mono text-xs">
                        {vol.device}
                      </TableCell>
                      <TableCell>{vol.sizeGb} GB</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          </div>
        ) : null}
    </>
  );
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
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  resource: Resource;
  pools: PoolOpt[];
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

        <Tabs defaultValue="volumes" className="py-2">
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
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  resourceName: string;
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
  nodes,
  pools,
  profiles,
}: {
  nodes: NodeOpt[];
  pools: PoolOpt[];
  profiles: ResourceProfile[];
}) {
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
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
      setOpen(false);
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
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button>
          <Plus className="h-4 w-4" />
          Create Resource
        </Button>
      </DialogTrigger>
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
              onClick={() => setOpen(false)}
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
