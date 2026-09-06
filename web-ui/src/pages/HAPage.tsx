import { useState } from 'react';
import { useNavigate } from 'react-router';
import {
  useQuery,
  useQueries,
  useMutation,
  useQueryClient,
} from '@tanstack/react-query';
import {
  api,
  HaConfig,
  QuorumInfo,
  Resource,
  ResourceStatus,
  SelfHaStatus,
} from '@/services/api';
import { PageHeader } from '@/components/PageHeader';
import { StatusBadge } from '@/components/StatusBadge';
import { ResourceTopology } from '@/components/ResourceTopology';
import { TONE_BG } from '@/components/status';
import { mountUnitFor, vipUnitFor } from '@/lib/toml';
import { cn } from '@/lib/utils';
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
import { Skeleton } from '@/components/ui/skeleton';
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

/** One HA config joined to the two payloads that describe it: the cluster-wide
 * resource record and its live per-resource status. Assembled once in the page
 * so the topology and the promoter card read the same numbers. */
type PromoterView = {
  config: HaConfig;
  resource?: Resource;
  status?: ResourceStatus;
  primaryNode?: string;
};

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

  const configs = haConfigs?.configs ?? [];

  // The resources list endpoint reports Role "Unknown" without node states;
  // live status comes from the per-resource status RPC instead. Held here
  // rather than inside each card because the header line and the topology need
  // the same answer — the query keys and interval are unchanged, so a card
  // reading ['ha-status', name] still shares this one fetch.
  const statusQueries = useQueries({
    queries: configs.map((c) => ({
      queryKey: ['ha-status', c.resource],
      queryFn: () => api.resourceStatus(c.resource),
      refetchInterval: 15000,
    })),
  });

  const resourceMap = new Map(
    (resources?.resources ?? []).map((r) => [r.name, r] as [string, Resource])
  );

  const promoters: PromoterView[] = configs.map((config, i) => {
    const resource = resourceMap.get(config.resource);
    const status = statusQueries[i]?.data?.status;
    const nodeStates = status?.nodeStates ?? {};
    // Keyed by DRBD host name, which is what evict and the failover poll
    // compare against — resolving it to an SDS node name here would break both.
    const primaryNode = Object.keys(nodeStates).find(
      (n) => nodeStates[n]?.role === 'Primary'
    );
    return { config, resource, status, primaryNode };
  });

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

  return (
    <div>
      <PageHeader
        title="High availability"
        description={<HeaderSummary loading={isLoading} promoters={promoters} />}
        actions={
          <Button onClick={() => navigate('/ha/create')}>
            <Plus className="mr-2 h-4 w-4" />
            Create HA resource
          </Button>
        }
      />

      <div className="space-y-6">
        {isLoading ? (
          <Skeleton className="h-72 w-full" />
        ) : promoters.length > 0 ? (
          <TopologySection promoters={promoters} />
        ) : null}

        <section className="space-y-4">
          <h2 className="text-[14.5px] font-semibold">Promoters</h2>

          {/* Self-HA runs off its own query, so it stays on screen while the
              HA config list is still in flight — hiding it behind that load
              would take the controller's own failover controls away for as
              long as an unrelated call is slow. */}
          <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
            <SelfHaCard />
            {isLoading
              ? Array.from({ length: 2 }).map((_, i) => (
                  <Skeleton key={i} className="h-64 w-full" />
                ))
              : promoters.map((p) => (
                  <PromoterCard
                    key={p.config.resource}
                    view={p}
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

          {!isLoading && promoters.length === 0 ? (
            <Card>
              <CardContent className="flex flex-col items-center justify-center gap-2 py-10 text-center">
                <HeartPulse className="h-7 w-7 text-muted-foreground" />
                <p className="text-[13px] text-muted-foreground">
                  No HA configurations found. Create a resource first, then
                  configure HA.
                </p>
              </CardContent>
            </Card>
          ) : null}
        </section>
      </div>

      <DetailsDialog
        config={detailsConfig}
        onOpenChange={(open) => !open && setDetailsConfig(null)}
      />
    </div>
  );
}

// ==================== Derived header line ====================

/**
 * Only facts the payloads actually carry: how many promoters exist, how many
 * hold quorum, and how many are promoted somewhere right now. Quorum is dropped
 * from the line entirely when no status has reported it rather than guessed at.
 */
function HeaderSummary({
  loading,
  promoters,
}: {
  loading: boolean;
  promoters: PromoterView[];
}) {
  if (loading) return <>Reading HA configurations…</>;
  if (promoters.length === 0) return <>No HA resources configured yet</>;

  const n = promoters.length;
  const quorate = promoters.filter((p) => p.status?.quorum?.hasQuorum).length;
  const withQuorumInfo = promoters.filter((p) => p.status?.quorum).length;
  const active = promoters.filter((p) => p.primaryNode).length;

  const parts = [`${n} promoter${n === 1 ? '' : 's'}`];
  if (withQuorumInfo > 0) {
    parts.push(
      quorate === withQuorumInfo
        ? withQuorumInfo === n
          ? 'all quorate'
          : `${quorate} quorate`
        : `${withQuorumInfo - quorate} without quorum`
    );
  }
  parts.push(active === n ? 'all promoted' : `${active} of ${n} promoted`);

  return <>{parts.join(' · ')}</>;
}

// ==================== Replication topology ====================

/**
 * The page's subject, and the first thing on it. This reuses
 * `ResourceTopology` rather than drawing a second renderer: it already places
 * the sites, the synchronous mesh and the dashed WAN legs from this same status
 * payload, and it carries its own legend. What the card adds is the connection
 * facts the SVG has no room for — the DRBD port, the protocol and the VIP.
 */
function TopologySection({ promoters }: { promoters: PromoterView[] }) {
  return (
    <section className="space-y-4">
      <h2 className="text-[14.5px] font-semibold">Replication topology</h2>
      <div className="space-y-5">
        {promoters.map(({ config, resource, status }) => (
          <Card key={config.resource} className="gap-4 py-5">
            <CardHeader className="gap-1 px-5">
              <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
                <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
                  <CardTitle className="font-mono text-[15px]">
                    {config.resource}
                  </CardTitle>
                  {config.vip ? (
                    <span className="font-mono text-xs tabular-nums text-muted-foreground">
                      vip {config.vip}
                    </span>
                  ) : null}
                  {config.mountPoint ? (
                    <span className="font-mono text-xs text-muted-foreground">
                      {config.mountPoint}
                      {config.fsType ? ` · ${config.fsType}` : ''}
                    </span>
                  ) : null}
                </div>
                <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
                  {resource ? (
                    <span className="font-mono text-xs tabular-nums text-muted-foreground">
                      tcp {resource.port} · protocol {resource.protocol}
                    </span>
                  ) : null}
                  {status?.drEndpoint ? (
                    <span className="font-mono text-xs tabular-nums text-muted-foreground">
                      dr {status.drEndpoint}
                    </span>
                  ) : null}
                  <QuorumPill quorum={status?.quorum} />
                </div>
              </div>
            </CardHeader>
            {/* ResourceTopology draws a viewBox'd SVG at `w-full`, so today it
                shrinks to whatever it is given rather than overflowing. This is
                the box it would scroll in the day it stops — the page itself
                must never scroll sideways. */}
            <CardContent className="overflow-x-auto px-5">
              {resource && status ? (
                <ResourceTopology resource={resource} status={status} />
              ) : (
                <p className="text-[13px] text-muted-foreground">
                  Replication state for{' '}
                  <span className="font-mono">{config.resource}</span> has not
                  arrived yet — the resource is not in the cluster resource list,
                  or its status call has not returned.
                </p>
              )}
            </CardContent>
          </Card>
        ))}
      </div>
    </section>
  );
}

// ==================== Small shared pieces ====================

/**
 * Quorum, stated in words first and coloured second: "Quorate" and "No quorum"
 * carry the whole meaning on their own, and the dot only repeats it.
 */
function QuorumPill({ quorum }: { quorum?: QuorumInfo }) {
  if (!quorum) return null;
  return (
    <span className="inline-flex items-center gap-2 rounded-full border border-border px-2.5 py-0.5 text-xs">
      <span
        aria-hidden
        className={cn(
          'h-1.5 w-1.5 rounded-full',
          TONE_BG[quorum.hasQuorum ? 'ok' : 'bad']
        )}
      />
      {quorum.hasQuorum ? 'Quorate' : 'No quorum'}
      <span className="font-mono tabular-nums text-muted-foreground">
        {quorum.online}/{quorum.members}
      </span>
    </span>
  );
}

/** One ordered entry of the promoter's start[], numbered so the order — which
 * is the whole point of the list — is readable without counting rows. */
function StartListItem({ index, unit }: { index: number; unit: string }) {
  return (
    <li className="flex items-center gap-2.5">
      <span className="w-4 shrink-0 font-mono text-[11px] tabular-nums text-muted-foreground">
        {index}
      </span>
      {/* These are long unbreakable mono strings —
          `service-ip@192.168.123.251-24`. On a phone the chip wraps inside the
          card rather than pushing its width out; the desktop card has the room
          to truncate instead and keep the list scannable down its left edge. */}
      <span className="min-w-0 rounded-[5px] border border-border bg-muted px-2 py-1 font-mono text-xs break-all md:truncate">
        {unit}
      </span>
    </li>
  );
}

/**
 * The promoter's start[] as the backend composes it: the mount unit, then the
 * VIP's service-ip unit, then the configured services. Derived from the same
 * config the generator reads, so it cannot disagree with the TOML below it.
 */
function startListFor(config: HaConfig): string[] {
  return [
    config.mountPoint ? mountUnitFor(config.mountPoint) : '',
    config.vip ? vipUnitFor(config.vip) : '',
    ...(config.services ?? []),
  ].filter((u) => u !== '');
}

function FactRow({
  label,
  value,
  mono,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div className="flex items-baseline justify-between gap-3 py-1">
      <span className="text-[13px] text-muted-foreground">{label}</span>
      <span
        className={cn(
          'min-w-0 truncate text-[13px]',
          mono ? 'font-mono tabular-nums' : 'font-medium'
        )}
      >
        {value}
      </span>
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
      <Card className="gap-4 py-5">
        <CardHeader className="px-5">
          <CardTitle className="font-mono text-[15px]">
            {SELF_HA_RESOURCE}
          </CardTitle>
        </CardHeader>
        <CardContent className="px-5">
          <Skeleton className="h-24 w-full" />
        </CardContent>
      </Card>
    );
  }

  // A failed status query is NOT the same as "self-HA disabled" — showing
  // the disabled state here would invite an accidental second enablement.
  if (isError) {
    return (
      <Card className="gap-4 py-5">
        <CardHeader className="px-5">
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0">
              <CardTitle className="font-mono text-[15px]">
                {SELF_HA_RESOURCE}
              </CardTitle>
              <p className="mt-1 text-[12.5px] text-muted-foreground">
                Control plane · state unknown
              </p>
            </div>
            <StatusBadge status="unknown" />
          </div>
        </CardHeader>
        <CardContent className="space-y-3 px-5">
          <p className="text-[13px] text-muted-foreground">
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
    <Card className="gap-4 py-5">
      <CardHeader className="px-5">
        <div className="flex items-start justify-between gap-4">
          <div className="min-w-0">
            <CardTitle className="font-mono text-[15px]">
              {status?.resource || SELF_HA_RESOURCE}
            </CardTitle>
            <p className="mt-1 text-[12.5px] text-muted-foreground">
              Control plane ·{' '}
              {status?.enabled ? 'self-managed promoter' : 'standalone'}
            </p>
          </div>
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
      <CardContent className="px-5">
        {status?.enabled ? (
          <SelfHaEnabled
            status={status}
            onEvict={() => evictMutation.mutate()}
            onDisable={(node) => disableMutation.mutate(node)}
            isEvicting={evictMutation.isPending}
            isDisabling={disableMutation.isPending}
          />
        ) : (
          <div className="flex items-start gap-3">
            <ShieldOff
              aria-hidden
              className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground"
            />
            <p className="text-[13px] text-muted-foreground">
              Standalone controller. Enable Self-HA to run the management plane
              on its own DRBD resource with a floating VIP and automatic
              failover.
            </p>
          </div>
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
      <p className="text-[12.5px] text-muted-foreground">
        Active on{' '}
        <span className="font-mono text-foreground">
          {activeNodeName || 'no node'}
        </span>
      </p>

      <div className="space-y-0.5">
        <FactRow label="Virtual IP" value={status.vip || '-'} mono />
      </div>

      <div>
        <div className="eyebrow">Member nodes</div>
        <div className="mt-2 flex flex-wrap gap-2">
          {(status.nodes ?? []).map((node) => {
            const isActive = node === activeNodeName;
            return (
              <span
                key={node}
                className={cn(
                  'inline-flex items-center gap-2 rounded-[5px] border px-2 py-1 font-mono text-xs',
                  isActive
                    ? 'border-transparent bg-accent text-accent-foreground'
                    : 'border-border bg-muted text-muted-foreground'
                )}
              >
                {node}
                {isActive ? (
                  <span className="text-[10.5px]">active</span>
                ) : null}
              </span>
            );
          })}
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-2 border-t border-border pt-4">
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

// ==================== Promoter card ====================

function PromoterCard({
  view,
  onShowDetails,
  onEvict,
  onDelete,
  isEvicting,
  isDeleting,
}: {
  view: PromoterView;
  onShowDetails: (resource: string) => void;
  onEvict: (resource: string, fromNode?: string) => void;
  onDelete: (resource: string) => void;
  isEvicting: boolean;
  isDeleting: boolean;
}) {
  const { config, resource, status, primaryNode } = view;
  // No primary anywhere means nothing is promoted, so there is nothing to evict.
  const isRunning = Boolean(primaryNode);
  const startList = startListFor(config);

  return (
    <Card className="gap-4 py-5">
      <CardHeader className="px-5">
        <div className="flex items-start justify-between gap-4">
          <div className="min-w-0">
            <CardTitle className="font-mono text-[15px]">
              {config.resource}
            </CardTitle>
            <p className="mt-1 text-[12.5px] text-muted-foreground">
              Promoter ·{' '}
              {isRunning ? (
                <>
                  active on{' '}
                  <span className="font-mono text-foreground">
                    {primaryNode}
                  </span>
                </>
              ) : (
                'not currently promoted'
              )}
            </p>
          </div>
          <div className="flex shrink-0 flex-col items-end gap-2">
            <QuorumPill quorum={status?.quorum} />
            <StatusBadge status={isRunning ? 'running' : 'stopped'} />
          </div>
        </div>
      </CardHeader>

      <CardContent className="space-y-4 px-5">
        <div className="space-y-0.5">
          <FactRow label="Virtual IP" value={config.vip || '-'} mono />
          <FactRow
            label="Mount point"
            value={config.mountPoint || '-'}
            mono
          />
          <FactRow label="Filesystem" value={config.fsType || '-'} />
          {resource ? (
            <FactRow
              label="Replication"
              value={`tcp ${resource.port} · protocol ${resource.protocol}`}
              mono
            />
          ) : null}
        </div>

        {startList.length > 0 ? (
          <div>
            <div className="eyebrow">Start list</div>
            <ol className="mt-2 space-y-1.5">
              {startList.map((unit, i) => (
                <StartListItem key={unit} index={i + 1} unit={unit} />
              ))}
            </ol>
          </div>
        ) : null}

        <div className="flex flex-wrap items-center gap-2 border-t border-border pt-4">
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

        <div className="border-t border-border pt-4">
          <TomlEditorSection resource={config.resource} />
        </div>
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
        className="flex w-full items-center gap-2 text-[12.5px] font-medium text-muted-foreground hover:text-foreground"
      >
        {open ? (
          <ChevronDown className="h-4 w-4" />
        ) : (
          <ChevronRight className="h-4 w-4" />
        )}
        <FileCode className="h-4 w-4" />
        <span>
          Promoter config <span className="font-mono">{resource}.toml</span>
        </span>
      </button>

      {open && (
        <div className="space-y-2">
          {data?.path && (
            <p className="font-mono text-xs break-all text-muted-foreground">
              {data.path}
            </p>
          )}

          {isFetching && content === null ? (
            <div className="flex items-center gap-2 text-[13px] text-muted-foreground">
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
              className="w-full rounded-md border border-input bg-transparent p-3 font-mono text-xs outline-none transition-[color,box-shadow] focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 dark:bg-input/30"
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

// ==================== Details Dialog ====================

function DetailsDialog({
  config,
  onOpenChange,
}: {
  config: HaConfig | null;
  onOpenChange: (open: boolean) => void;
}) {
  const rows = config
    ? ([
        ['Resource', config.resource, true],
        ['Virtual IP', config.vip, true],
        ['Mount Point', config.mountPoint || '-', true],
        ['Filesystem', config.fsType || '-', false],
      ] as [string, string, boolean][])
    : [];

  return (
    <Dialog open={!!config} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>HA Configuration Details</DialogTitle>
        </DialogHeader>
        <div className="space-y-1 text-[13px]">
          {rows.map(([label, value, mono]) => (
            <div
              key={label}
              className="flex justify-between border-b border-border py-2 last:border-0"
            >
              <span className="text-muted-foreground">{label}</span>
              <span className={mono ? 'font-mono tabular-nums' : 'font-medium'}>
                {value}
              </span>
            </div>
          ))}
          {config?.services && config.services.length > 0 && (
            <div className="pt-3">
              <div className="eyebrow">Services</div>
              <div className="mt-2 flex flex-wrap gap-2">
                {config.services.map((service) => (
                  <span
                    key={service}
                    className="rounded-[5px] border border-border bg-muted px-2 py-1 font-mono text-xs"
                  >
                    {service}
                  </span>
                ))}
              </div>
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
