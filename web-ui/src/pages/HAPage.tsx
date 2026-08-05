import { useState } from 'react';
import { useNavigate } from 'react-router';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api, HaConfig, Resource, SelfHaStatus } from '@/services/api';
import { StatusBadge } from '@/components/StatusBadge';
import { toast } from 'sonner';
import {
  HeartPulse,
  Plus,
  Trash2,
  Info,
  LogOut,
  Loader2,
  ShieldCheck,
  ShieldOff,
  Server,
  ChevronDown,
  ChevronRight,
  FileCode,
  RotateCw,
  Save,
} from 'lucide-react';
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import { Button } from '@/components/ui/button';
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
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Badge } from '@/components/ui/badge';
import { Skeleton } from '@/components/ui/skeleton';
import { Separator } from '@/components/ui/separator';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';

const SELF_HA_RESOURCE = 'sds-meta';

/** Self-HA enable/disable failures often manifest as fetch errors while the
 * controller restarts under drbd-reactor. Surface those as informational. */
function isRestartError(message: string): boolean {
  return message.includes('fetch') || message.includes('Failed');
}

export function HAPage() {
  const queryClient = useQueryClient();
  const navigate = useNavigate();

  const { data: haConfigs, isLoading } = useQuery({
    queryKey: ['ha'],
    queryFn: () => api.getHaConfigs(),
  });

  const { data: resources } = useQuery({
    queryKey: ['resources'],
    queryFn: () => api.getResources(),
  });

  const [detailsConfig, setDetailsConfig] = useState<HaConfig | null>(null);

  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: ['ha'] });
    queryClient.invalidateQueries({ queryKey: ['resources'] });
  };

  // Confirm the failover actually completed with a second toast. evictHa blocks
  // until the resource is promoted elsewhere, so by onSuccess the move is
  // (usually) already done — check immediately first, then poll a few times as a
  // safety net, and report the new active node.
  const pollFailoverComplete = async (resource: string, fromNode?: string) => {
    for (let i = 0; i < 20; i++) {
      try {
        const s = await api.resourceStatus(resource);
        const states = s.status?.nodeStates ?? {};
        const primary = Object.keys(states).find(
          (n) => states[n]?.role === 'Primary',
        );
        if (primary && primary !== fromNode) {
          toast.success(
            `Failover complete — ${resource} is now active on ${primary}`,
          );
          invalidate();
          queryClient.invalidateQueries({ queryKey: ['ha-status', resource] });
          return;
        }
      } catch {
        // transient errors during the VIP move — keep polling
      }
      await new Promise((r) => setTimeout(r, 2000));
    }
    toast.info(`${resource}: failover is taking longer than expected`);
  };

  const evictMutation = useMutation({
    mutationFn: ({ resource }: { resource: string; fromNode?: string }) =>
      api.evictHa(resource),
    // Fire the "initiated" toast the moment the user confirms — evictHa blocks
    // for the whole failover, so putting this in onSuccess would delay it to the
    // very end and make both toasts appear together.
    onMutate: () => {
      toast.info('Eviction initiated; failover in progress');
    },
    onSuccess: (_data, { resource, fromNode }) => {
      invalidate();
      void pollFailoverComplete(resource, fromNode);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const deleteMutation = useMutation({
    mutationFn: (resource: string) => api.deleteHa(resource),
    onSuccess: () => {
      toast.success('HA configuration deleted');
      queryClient.invalidateQueries({ queryKey: ['ha'] });
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const showDetails = async (resource: string) => {
    try {
      const data = await api.getHaConfig(resource);
      setDetailsConfig(data.config);
    } catch (e) {
      toast.error((e as Error).message);
    }
  };

  const resourceMap = new Map(
    (resources?.resources ?? []).map((r) => [r.name, r] as [string, Resource])
  );

  const configs = haConfigs?.configs ?? [];

  return (
    <div className="space-y-6">
      <SelfHaCard />

      <div className="flex items-center justify-between">
        <div>
          <h3 className="text-lg font-semibold">HA Configurations</h3>
          <p className="text-sm text-muted-foreground">
            Make DRBD resources highly available with a floating VIP and
            automatic failover.
          </p>
        </div>
        <Button onClick={() => navigate('/ha/create')}>
          <Plus className="mr-2 h-4 w-4" />
          Create HA Config
        </Button>
      </div>

      {isLoading ? (
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
          {Array.from({ length: 2 }).map((_, i) => (
            <Skeleton key={i} className="h-64 w-full" />
          ))}
        </div>
      ) : configs.length === 0 ? (
        <Card>
          <CardContent className="flex flex-col items-center justify-center gap-2 py-12 text-center">
            <HeartPulse className="h-8 w-8 text-muted-foreground" />
            <p className="text-sm text-muted-foreground">
              No HA configurations found. Create a resource first, then
              configure HA.
            </p>
          </CardContent>
        </Card>
      ) : (
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
          {configs.map((config) => (
            <HAConfigCard
              key={config.resource}
              config={config}
              resource={resourceMap.get(config.resource)}
              onShowDetails={showDetails}
              onEvict={(r, fromNode) =>
                evictMutation.mutate({ resource: r, fromNode })
              }
              onDelete={(r) => deleteMutation.mutate(r)}
              isEvicting={evictMutation.isPending}
              isDeleting={deleteMutation.isPending}
            />
          ))}
        </div>
      )}

      <DetailsDialog
        config={detailsConfig}
        onOpenChange={(open) => !open && setDetailsConfig(null)}
      />
    </div>
  );
}

// ==================== Controller Self-HA ====================

function SelfHaCard() {
  const queryClient = useQueryClient();
  const {
    data: status,
    isLoading,
    isError,
    error,
    refetch,
  } = useQuery({
    queryKey: ['selfha'],
    queryFn: () => api.getSelfHaStatus(),
    refetchInterval: 15000,
  });

  const [enableOpen, setEnableOpen] = useState(false);

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ['selfha'] });

  // Report where the controller actually landed. "Eviction initiated" alone is
  // not enough to tell whether anything happened: the card may already have
  // been showing a stale active node, and a controller that fails back to the
  // node you thought it was on looks identical to one that never moved.
  //
  // The controller is the thing being relocated, so the API is unreachable for
  // the middle of this — errors are expected and are not a failure.
  const pollControllerMoved = async (fromNode: string) => {
    for (let i = 0; i < 20; i++) {
      await new Promise((r) => setTimeout(r, 2000));
      try {
        const s = await api.getSelfHaStatus();
        if (s.activeNode && s.activeNode !== fromNode) {
          toast.success(`Controller is now active on ${s.activeNode}`);
          invalidate();
          return;
        }
      } catch {
        // The controller is mid-move; keep waiting.
      }
    }
    toast.info('Controller failover is taking longer than expected');
  };

  const evictMutation = useMutation({
    // Read the active node back from the server first. Evict acts on whichever
    // node is active right now, which need not be the one on screen — this card
    // can be showing state from before the tab was last backgrounded. Taking
    // "from" off the card instead would report a move that did not happen.
    mutationFn: async () => {
      const before = await api.getSelfHaStatus();
      toast.info(
        before.activeNode
          ? `Evicting controller from ${before.activeNode}; failing over`
          : 'Controller eviction initiated; failing over',
      );
      await api.evictHa(SELF_HA_RESOURCE);
      return before.activeNode ?? '';
    },
    onSuccess: (from) => {
      invalidate();
      void pollControllerMoved(from);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const disableMutation = useMutation({
    mutationFn: (node: string) => api.disableSelfHa(node),
    onSuccess: () => {
      toast.success('Self-HA disabled; controller returning to standalone');
      invalidate();
    },
    onError: (e: Error) => {
      if (isRestartError(e.message)) {
        toast.info('Controller is restarting; refresh shortly');
      } else {
        toast.error(e.message);
      }
    },
  });

  if (isLoading) {
    return (
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <ShieldCheck className="h-5 w-5" />
            Controller Self-HA
          </CardTitle>
        </CardHeader>
        <CardContent>
          <Skeleton className="h-20 w-full" />
        </CardContent>
      </Card>
    );
  }

  // A failed status query is NOT the same as "self-HA disabled" — showing
  // the disabled state here would invite an accidental second enablement.
  if (isError) {
    return (
      <Card>
        <CardHeader>
          <div className="flex items-start justify-between gap-4">
            <CardTitle className="flex items-center gap-2 text-base">
              <ShieldOff className="h-5 w-5 text-amber-500" />
              Controller Self-HA
            </CardTitle>
            <StatusBadge status="unknown" />
          </div>
        </CardHeader>
        <CardContent className="space-y-3">
          <p className="text-sm text-muted-foreground">
            Could not load self-HA status: {(error as Error).message}
          </p>
          <Button size="sm" variant="outline" onClick={() => refetch()}>
            <Loader2 className="mr-2 h-4 w-4" />
            Retry
          </Button>
        </CardContent>
      </Card>
    );
  }

  return (
    <Card>
      <CardHeader>
        <div className="flex items-start justify-between gap-4">
          <CardTitle className="flex items-center gap-2 text-base">
            {status?.enabled ? (
              <ShieldCheck className="h-5 w-5 text-emerald-600" />
            ) : (
              <ShieldOff className="h-5 w-5 text-muted-foreground" />
            )}
            Controller Self-HA
          </CardTitle>
          {status?.enabled ? (
            <StatusBadge status="enabled" />
          ) : (
            <Button size="sm" onClick={() => setEnableOpen(true)}>
              <ShieldCheck className="mr-2 h-4 w-4" />
              Enable Self-HA
            </Button>
          )}
        </div>
      </CardHeader>
      <CardContent>
        {status?.enabled ? (
          <SelfHaEnabled
            status={status}
            onEvict={() => evictMutation.mutate()}
            onDisable={(node) => disableMutation.mutate(node)}
            isEvicting={evictMutation.isPending}
            isDisabling={disableMutation.isPending}
          />
        ) : (
          <p className="text-sm text-muted-foreground">
            Standalone controller. Enable Self-HA to run the management plane on
            its own DRBD resource with a floating VIP and automatic failover.
          </p>
        )}
      </CardContent>

      <EnableSelfHaDialog
        open={enableOpen}
        onOpenChange={setEnableOpen}
        onEnabled={() => {
          setEnableOpen(false);
          invalidate();
        }}
      />
    </Card>
  );
}

function SelfHaEnabled({
  status,
  onEvict,
  onDisable,
  isEvicting,
  isDisabling,
}: {
  status: SelfHaStatus;
  onEvict: () => void;
  onDisable: (node: string) => void;
  isEvicting: boolean;
  isDisabling: boolean;
}) {
  const [disableNode, setDisableNode] = useState(status.activeNode || '');

  const { data: nodes } = useQuery({
    queryKey: ['nodes'],
    queryFn: () => api.getNodes(),
  });

  // status.activeNode is an address; members are node names. Resolve the
  // active node's name so it displays as a name and highlights correctly.
  const nodeNameByAddr = new Map(
    (nodes?.nodes ?? []).map((n) => [n.address, n.name]),
  );
  const activeNodeName = status.activeNode
    ? nodeNameByAddr.get(status.activeNode) ?? status.activeNode
    : '';

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="space-y-1">
          <p className="text-xs font-medium text-muted-foreground">Virtual IP</p>
          <p className="font-mono text-sm">{status.vip || '-'}</p>
        </div>
        <div className="space-y-1">
          <p className="text-xs font-medium text-muted-foreground">
            Active Node
          </p>
          <Badge className="bg-emerald-600 hover:bg-emerald-600">
            <Server className="mr-1 h-3 w-3" />
            {activeNodeName || '-'}
          </Badge>
        </div>
      </div>

      <div className="space-y-1.5">
        <p className="text-xs font-medium text-muted-foreground">Member Nodes</p>
        <div className="flex flex-wrap gap-2">
          {(status.nodes ?? []).map((node) => (
            <Badge
              key={node}
              className={
                node === activeNodeName
                  ? 'bg-emerald-600 hover:bg-emerald-600'
                  : undefined
              }
              variant={node === activeNodeName ? 'default' : 'secondary'}
            >
              {node === activeNodeName && <Server className="mr-1 h-3 w-3" />}
              {node}
              {node === activeNodeName && ' (active)'}
            </Badge>
          ))}
        </div>
      </div>

      <Separator />

      <div className="flex flex-wrap gap-2">
        <AlertDialog>
          <AlertDialogTrigger asChild>
            <Button variant="outline" size="sm" disabled={isEvicting}>
              {isEvicting ? (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              ) : (
                <LogOut className="mr-2 h-4 w-4" />
              )}
              Evict Controller
            </Button>
          </AlertDialogTrigger>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Evict the controller?</AlertDialogTitle>
              <AlertDialogDescription>
                The management plane will briefly fail over to another node.
                In-flight requests may fail for a few seconds until the VIP
                moves and the controller restarts on the new active node.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction onClick={onEvict}>
                Evict Controller
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>

        <AlertDialog>
          <AlertDialogTrigger asChild>
            <Button variant="outline" size="sm" disabled={isDisabling}>
              {isDisabling ? (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              ) : (
                <ShieldOff className="mr-2 h-4 w-4" />
              )}
              Disable
            </Button>
          </AlertDialogTrigger>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Disable Self-HA?</AlertDialogTitle>
              <AlertDialogDescription>
                The controller will restart in standalone mode on the selected
                node. The VIP is released, so this UI endpoint may move and
                requests may fail briefly — reconnect to the node's own address
                afterwards.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <div className="space-y-1.5 py-2">
              <Label>Standalone Node</Label>
              <Select value={disableNode} onValueChange={setDisableNode}>
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="Select a node..." />
                </SelectTrigger>
                <SelectContent>
                  {(status.nodes ?? []).map((node) => (
                    <SelectItem key={node} value={node}>
                      {node}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction
                disabled={!disableNode}
                onClick={() => disableNode && onDisable(disableNode)}
              >
                Disable Self-HA
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </div>
    </div>
  );
}

function EnableSelfHaDialog({
  open,
  onOpenChange,
  onEnabled,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onEnabled: () => void;
}) {
  const [vip, setVip] = useState('');
  const [pool, setPool] = useState('');
  const [sizeGb, setSizeGb] = useState('1');
  const [port, setPort] = useState('7999');

  const mutation = useMutation({
    mutationFn: () =>
      api.enableSelfHa({
        vip,
        pool,
        sizeGb: parseInt(sizeGb, 10),
        port: parseInt(port, 10),
      }),
    onSuccess: () => {
      toast.info(
        'Controller is restarting under drbd-reactor management. Requests may fail briefly while the VIP comes up.',
        { duration: Infinity, closeButton: true }
      );
      onEnabled();
    },
    onError: (e: Error) => {
      if (isRestartError(e.message)) {
        toast.info('Controller is restarting; refresh shortly');
      } else {
        toast.error(e.message);
      }
    },
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Enable Controller Self-HA</DialogTitle>
          <DialogDescription>
            Run the management plane on its own DRBD resource with a floating
            VIP and drbd-reactor failover.
          </DialogDescription>
        </DialogHeader>
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            mutation.mutate();
          }}
        >
          <div className="space-y-1.5">
            <Label>Virtual IP (CIDR)</Label>
            <Input
              value={vip}
              onChange={(e) => setVip(e.target.value)}
              placeholder="192.168.1.250/24"
              required
            />
          </div>
          <div className="space-y-1.5">
            <Label>Pool</Label>
            <Input
              value={pool}
              onChange={(e) => setPool(e.target.value)}
              placeholder="vg0"
              required
            />
          </div>
          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-1.5">
              <Label>Size (GB)</Label>
              <Input
                type="number"
                value={sizeGb}
                onChange={(e) => setSizeGb(e.target.value)}
                min={1}
                required
              />
            </div>
            <div className="space-y-1.5">
              <Label>DRBD Port</Label>
              <Input
                type="number"
                value={port}
                onChange={(e) => setPort(e.target.value)}
                required
              />
            </div>
          </div>
          <DialogFooter>
            <Button
              type="submit"
              disabled={mutation.isPending || !vip || !pool}
            >
              {mutation.isPending && (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              )}
              Enable Self-HA
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// ==================== HA Config Card ====================

function HAConfigCard({
  config,
  resource,
  onShowDetails,
  onEvict,
  onDelete,
  isEvicting,
  isDeleting,
}: {
  config: HaConfig;
  resource?: Resource;
  onShowDetails: (resource: string) => void;
  onEvict: (resource: string, fromNode?: string) => void;
  onDelete: (resource: string) => void;
  isEvicting: boolean;
  isDeleting: boolean;
}) {
  // The resources list endpoint reports Role "Unknown" without node states;
  // live status comes from the per-resource status RPC instead.
  const { data: liveStatus } = useQuery({
    queryKey: ['ha-status', config.resource],
    queryFn: () => api.resourceStatus(config.resource),
    refetchInterval: 15000,
  });
  const nodeStates = liveStatus?.status?.nodeStates ?? resource?.nodeStates ?? {};
  const primaryNode = Object.keys(nodeStates).find(
    (n) => nodeStates[n]?.role === 'Primary'
  );
  const isRunning = Boolean(primaryNode);

  return (
    <Card>
      <CardHeader>
        <div className="flex items-start justify-between gap-4">
          <div className="flex items-center gap-3">
            <div className="flex h-10 w-10 items-center justify-center rounded-lg bg-muted">
              <HeartPulse className="h-5 w-5 text-muted-foreground" />
            </div>
            <div>
              <CardTitle className="text-base">{config.resource}</CardTitle>
              {resource && (
                <p className="text-sm text-muted-foreground">
                  Port: {resource.port} • Protocol: {resource.protocol}
                </p>
              )}
            </div>
          </div>
          <StatusBadge status={isRunning ? 'running' : 'stopped'} />
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="space-y-1 text-sm">
          <InfoRow label="VIP" value={config.vip} mono />
          <InfoRow label="Mount Point" value={config.mountPoint || '-'} mono />
          <InfoRow label="Filesystem" value={config.fsType || '-'} />
          <InfoRow label="Primary Node" value={primaryNode || '-'} />
          <div className="flex items-start justify-between gap-2 py-1">
            <span className="text-muted-foreground">Services</span>
            {config.services?.length > 0 ? (
              <div className="flex flex-wrap justify-end gap-1">
                {config.services.map((s) => (
                  <Badge key={s} variant="secondary" className="font-mono">
                    {s}
                  </Badge>
                ))}
              </div>
            ) : (
              <span className="font-medium">-</span>
            )}
          </div>
        </div>

        <Separator />

        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => onShowDetails(config.resource)}
          >
            <Info className="mr-1 h-3 w-3" />
            Details
          </Button>

          <AlertDialog>
            <AlertDialogTrigger asChild>
              <Button
                variant="outline"
                size="sm"
                disabled={isEvicting || !isRunning}
              >
                {isEvicting ? (
                  <Loader2 className="mr-1 h-3 w-3 animate-spin" />
                ) : (
                  <LogOut className="mr-1 h-3 w-3" />
                )}
                Evict
              </Button>
            </AlertDialogTrigger>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>Evict "{config.resource}"?</AlertDialogTitle>
                <AlertDialogDescription>
                  This triggers a failover to another node. The VIP moves and
                  clients will briefly lose connectivity until the resource is
                  promoted elsewhere.
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>Cancel</AlertDialogCancel>
                <AlertDialogAction
                  onClick={() => onEvict(config.resource, primaryNode)}
                >
                  Evict
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>

          <AlertDialog>
            <AlertDialogTrigger asChild>
              <Button variant="outline" size="sm" disabled={isDeleting}>
                <Trash2 className="mr-1 h-3 w-3 text-destructive" />
                Delete
              </Button>
            </AlertDialogTrigger>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>
                  Delete HA configuration?
                </AlertDialogTitle>
                <AlertDialogDescription>
                  This removes the drbd-reactor HA config for "{config.resource}
                  ". The DRBD resource and its data are not affected, but
                  automatic failover stops.
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>Cancel</AlertDialogCancel>
                <AlertDialogAction onClick={() => onDelete(config.resource)}>
                  Delete
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        </div>

        <Separator />

        <TomlEditorSection resource={config.resource} />
      </CardContent>
    </Card>
  );
}

// ==================== drbd-reactor Promoter TOML editor ====================

function TomlEditorSection({ resource }: { resource: string }) {
  const [open, setOpen] = useState(false);
  const [content, setContent] = useState<string | null>(null);

  // Lazily load the promoter TOML the first time the section is expanded.
  const { data, isFetching, isError, error, refetch } = useQuery({
    queryKey: ['ha-toml', resource],
    queryFn: () => api.getHaToml(resource),
    enabled: open,
  });

  // Seed the editable buffer from the server whenever a fresh copy arrives and
  // the user hasn't started editing yet.
  const serverContent = data?.content ?? '';
  if (open && content === null && data) {
    setContent(serverContent);
  }

  const syncMutation = useMutation({
    mutationFn: (text: string) => api.syncHaToml(resource, text),
    onSuccess: (res) => toast.success(res.message || 'TOML synced'),
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <div className="space-y-2">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-2 text-sm font-medium text-muted-foreground hover:text-foreground"
      >
        {open ? (
          <ChevronDown className="h-4 w-4" />
        ) : (
          <ChevronRight className="h-4 w-4" />
        )}
        <FileCode className="h-4 w-4" />
        DRBD Reactor Promoter Config ({resource}.toml)
      </button>

      {open && (
        <div className="space-y-2">
          {data?.path && (
            <p className="font-mono text-xs text-muted-foreground">
              {data.path}
            </p>
          )}

          {isFetching && content === null ? (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              Loading TOML...
            </div>
          ) : isError ? (
            <p className="text-xs text-destructive">
              Could not load TOML: {(error as Error).message}
            </p>
          ) : (
            <textarea
              value={content ?? ''}
              onChange={(e) => setContent(e.target.value)}
              spellCheck={false}
              rows={12}
              className="w-full rounded-md border border-input bg-transparent p-3 font-mono text-xs shadow-xs outline-none transition-[color,box-shadow] focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 dark:bg-input/30"
            />
          )}

          <div className="flex flex-wrap gap-2">
            <Button
              size="sm"
              disabled={
                syncMutation.isPending ||
                content === null ||
                content.trim() === ''
              }
              onClick={() => content !== null && syncMutation.mutate(content)}
            >
              {syncMutation.isPending ? (
                <Loader2 className="mr-1 h-3 w-3 animate-spin" />
              ) : (
                <Save className="mr-1 h-3 w-3" />
              )}
              Sync
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={isFetching}
              onClick={() => {
                setContent(null);
                void refetch();
              }}
            >
              <RotateCw className="mr-1 h-3 w-3" />
              Reload
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}

function InfoRow({
  label,
  value,
  mono,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div className="flex justify-between gap-2 py-1">
      <span className="text-muted-foreground">{label}</span>
      <span className={mono ? 'font-mono text-xs' : 'font-medium'}>{value}</span>
    </div>
  );
}

// ==================== Details Dialog ====================

function DetailsDialog({
  config,
  onOpenChange,
}: {
  config: HaConfig | null;
  onOpenChange: (open: boolean) => void;
}) {
  const rows = config
    ? [
        ['Resource', config.resource],
        ['Virtual IP', config.vip],
        ['Mount Point', config.mountPoint || '-'],
        ['Filesystem', config.fsType || '-'],
      ]
    : [];

  return (
    <Dialog open={!!config} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>HA Configuration Details</DialogTitle>
        </DialogHeader>
        <div className="space-y-1 text-sm">
          {rows.map(([label, value]) => (
            <div
              key={label}
              className="flex justify-between border-b py-2 last:border-0"
            >
              <span className="text-muted-foreground">{label}</span>
              <span className="font-medium">{value}</span>
            </div>
          ))}
          {config?.services && config.services.length > 0 && (
            <div className="pt-2">
              <p className="mb-2 text-xs font-medium text-muted-foreground">
                Services
              </p>
              <div className="flex flex-wrap gap-2">
                {config.services.map((service) => (
                  <Badge key={service} variant="secondary" className="font-mono">
                    {service}
                  </Badge>
                ))}
              </div>
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
