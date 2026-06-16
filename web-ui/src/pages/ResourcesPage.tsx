import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api, Resource, Volume } from '../services/api';
import { StatusBadge } from '@/components/StatusBadge';
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
  Eye,
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

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h3 className="text-lg font-semibold">DRBD Resources</h3>
        <CreateResourceDialog
          nodes={nodes?.nodes ?? []}
          pools={pools?.pools ?? []}
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
    </div>
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
  return (
    <TableRow>
      <TableCell>
        <div className="flex items-center gap-2">
          <span className="flex h-8 w-8 items-center justify-center rounded bg-primary/10">
            <Database className="h-4 w-4 text-primary" />
          </span>
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
            return (
              <span key={node} className="inline-flex items-center gap-1">
                {isPrimary && (
                  <Star className="h-3 w-3 fill-amber-400 text-amber-400" />
                )}
                <StatusBadge status={state?.role ?? node} />
                {state?.role && (
                  <span className="text-xs text-muted-foreground">{node}</span>
                )}
              </span>
            );
          })}
          {resource.disklessNodes?.map((node) => (
            <span key={node} className="inline-flex items-center gap-1">
              <Badge variant="outline" title="Diskless quorum tiebreaker">
                tiebreaker
              </Badge>
              <span className="text-xs text-muted-foreground">{node}</span>
            </span>
          ))}
        </div>
      </TableCell>
      <TableCell className="text-muted-foreground">
        {resource.volumes.length}
      </TableCell>
      <TableCell className="text-right">
        <div className="flex items-center justify-end gap-1">
          <StatusDialog resourceName={resource.name} />
          <ResourceActionsMenu
            resource={resource}
            pools={pools}
            nodes={nodes}
          />
        </div>
      </TableCell>
    </TableRow>
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
          <DropdownMenuItem
            variant="destructive"
            onSelect={() => setDeleteOpen(true)}
          >
            <Trash2 className="h-4 w-4" />
            Delete
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

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
          <div className="grid grid-cols-5 gap-2">
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

function StatusDialog({ resourceName }: { resourceName: string }) {
  const [open, setOpen] = useState(false);

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['resource-status', resourceName],
    queryFn: () => api.resourceStatus(resourceName),
    enabled: open,
  });

  const status = data?.status;

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm">
          <Eye className="h-4 w-4" />
          Status
        </Button>
      </DialogTrigger>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Resource Status: {resourceName}</DialogTitle>
          <DialogDescription>Live DRBD status for this resource.</DialogDescription>
        </DialogHeader>

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

            <div>
              <h4 className="mb-2 text-sm font-medium">Node States</h4>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Node</TableHead>
                    <TableHead>Role</TableHead>
                    <TableHead>Disk</TableHead>
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
            {pools.map((p) => (
              <SelectItem key={`${p.node}-${p.name}`} value={p.name}>
                {p.name} ({p.node}) - {p.freeGb}GB free
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
}: {
  nodes: NodeOpt[];
  pools: PoolOpt[];
}) {
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState('');
  const [port, setPort] = useState('7000');
  const [protocol, setProtocol] = useState('C');
  const [selectedNodes, setSelectedNodes] = useState<string[]>([]);
  const [sizeGb, setSizeGb] = useState('10');
  const [pool, setPool] = useState('');
  const [storageType, setStorageType] = useState('lvm');

  const noneValue = '__none__';

  // Pool types as the backend reports them, per storage type.
  const poolTypeFor: Record<string, string> = {
    lvm: 'vg',
    'lvm-thin': 'thin_pool',
    zfs: 'zfs',
  };
  const matchingPools = pools.filter(
    (p) => p.type === poolTypeFor[storageType],
  );

  const reset = () => {
    setName('');
    setPort('7000');
    setProtocol('C');
    setSelectedNodes([]);
    setSizeGb('10');
    setPool('');
    setStorageType('lvm');
  };

  const createMutation = useMutation({
    mutationFn: (data: {
      name: string;
      port: number;
      nodes: string[];
      protocol: string;
      sizeGb: number;
      pool?: string;
      storageType?: string;
    }) => api.createResource(data),
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

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (selectedNodes.length < 1) {
      toast.error('Select at least 1 node');
      return;
    }
    createMutation.mutate({
      name,
      port: parseInt(port, 10),
      nodes: selectedNodes,
      protocol,
      sizeGb: parseInt(sizeGb, 10),
      pool: pool || undefined,
      storageType,
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

            <div className="grid grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label htmlFor="res-size">Size (GB)</Label>
                <Input
                  id="res-size"
                  type="number"
                  min={1}
                  value={sizeGb}
                  onChange={(e) => setSizeGb(e.target.value)}
                  required
                />
              </div>
              <div className="space-y-2">
                <Label>Storage Type</Label>
                <Select
                  value={storageType}
                  onValueChange={(v) => {
                    setStorageType(v);
                    setPool(''); // pools are type-specific
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
            </div>

            <div className="grid grid-cols-1 gap-4">
              <div className="space-y-2">
                <Label>Pool (optional)</Label>
                <Select
                  value={pool || noneValue}
                  onValueChange={(v) => setPool(v === noneValue ? '' : v)}
                >
                  <SelectTrigger className="w-full">
                    <SelectValue placeholder="Auto-select" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={noneValue}>Auto-select</SelectItem>
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
