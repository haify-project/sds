import { useMemo, useState, type ReactNode } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  api,
  Gateway,
  Resource,
  NFSExport,
  ISCSILUN,
  NVMeNamespace,
} from '@/services/api';
import { toast } from 'sonner';
import {
  Plus,
  Trash2,
  Loader2,
  X,
  AlertCircle,
  ChevronDown,
  ChevronRight,
  MoreHorizontal,
  Network,
} from 'lucide-react';
import { PageHeader } from '@/components/PageHeader';
import { SegmentedFilter } from '@/components/SegmentedFilter';
import { StatusTickCell, StatusTickHead } from '@/components/StatusTick';
import { toneOf, TONE_BG, type StatusTone } from '@/components/status';
import { Card, CardContent } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
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
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Badge } from '@/components/ui/badge';
import { Switch } from '@/components/ui/switch';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';

// ==================== Protocol vocabulary ====================

type GwKind = 'nfs' | 'iscsi' | 'nvme';

// The ports are the backend's own constants (pkg/gateway/gateway.go), not a
// guess: a gateway is reachable on exactly one of them.
const PROTOCOL: Record<GwKind, { label: string; port: number }> = {
  nfs: { label: 'NFS', port: 2049 },
  iscsi: { label: 'iSCSI', port: 3260 },
  nvme: { label: 'NVMe-oF', port: 4420 },
};

// The backend reports the NVMe type as "nvmeof" (reactor config naming).
function gwKind(type: string | undefined): GwKind {
  if (type === 'nvmeof' || type === 'nvme') return 'nvme';
  if (type === 'iscsi') return 'iscsi';
  return 'nfs';
}

// The list endpoint says "started"; older records and the reactor say
// "running". Both mean the promoter's services came up.
function isRunning(state: string | undefined): boolean {
  return state === 'started' || state === 'running';
}

// `toneOf` knows "running", not the API's "started" — normalise before asking,
// so the row's tick and the chain's dots cannot disagree about one gateway.
function gatewayTone(state: string | undefined): StatusTone {
  return toneOf(isRunning(state) ? 'running' : state);
}

function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? '' : 's'}`;
}

// ==================== Page ====================

export function GatewaysPage() {
  const queryClient = useQueryClient();
  const { data: gateways, isLoading } = useQuery({
    queryKey: ['gateways'],
    queryFn: () => api.getGateways(),
    // A freshly created gateway is briefly "failed"/transitional while the
    // drbd-reactor promoter starts its services; poll until every gateway
    // settles so the UI reflects the real state instead of a stale snapshot.
    refetchInterval: (query) => {
      const gws =
        (query.state.data as { gateways?: { state?: string }[] } | undefined)
          ?.gateways ?? [];
      return gws.some((g) => g.state !== 'started' && g.state !== 'stopped')
        ? 3000
        : false;
    },
  });
  const { data: resources } = useQuery({
    queryKey: ['resources'],
    queryFn: () => api.getResources(),
  });

  const [createType, setCreateType] = useState<GwKind | null>(null);
  const [manageGateway, setManageGateway] = useState<Gateway | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);
  // The delete confirmation lives at page level rather than inside each row's
  // dropdown: a Radix AlertDialog nested in a menu item is unmounted with the
  // menu the moment it would open.
  const [pendingDelete, setPendingDelete] = useState<Gateway | null>(null);
  const [filter, setFilter] = useState<GwKind | 'all'>('all');

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ['gateways'] });

  const startMutation = useMutation({
    mutationFn: (id: string) => api.startGateway(id),
    onSuccess: () => {
      toast.success('Gateway started');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const stopMutation = useMutation({
    mutationFn: (id: string) => api.stopGateway(id),
    onSuccess: () => {
      toast.success('Gateway stopped');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.deleteGateway(id),
    onSuccess: () => {
      toast.success('Gateway deleted');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const list = useMemo(() => gateways?.gateways ?? [], [gateways]);
  const counts = useMemo(() => {
    const c: Record<GwKind, number> = { nfs: 0, iscsi: 0, nvme: 0 };
    for (const g of list) c[gwKind(g.type)] += 1;
    return c;
  }, [list]);
  const running = list.filter((g) => isRunning(g.state)).length;
  const shown = filter === 'all' ? list : list.filter((g) => gwKind(g.type) === filter);

  const openCreate = (kind: GwKind) => {
    // Pull a fresh resource list so newly created resources show up in the
    // dropdown without a full page reload.
    queryClient.invalidateQueries({ queryKey: ['resources'] });
    setCreateType(kind);
  };

  return (
    <div>
      <PageHeader
        title="Gateways"
        description={
          <>
            A gateway is a DRBD resource plus a drbd-reactor promoter config, so
            the export follows the resource on failover.{' '}
            <span className="font-mono text-foreground">{list.length}</span>{' '}
            configured,{' '}
            <span className="font-mono text-foreground">{running}</span> started.
          </>
        }
        actions={
          <>
            <SegmentedFilter
              aria-label="Filter gateways by protocol"
              value={filter}
              onChange={setFilter}
              options={[
                { value: 'all', label: 'All', count: list.length },
                { value: 'nfs', label: 'NFS', count: counts.nfs },
                { value: 'iscsi', label: 'iSCSI', count: counts.iscsi },
                { value: 'nvme', label: 'NVMe-oF', count: counts.nvme },
              ]}
            />
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button>
                  <Plus />
                  New gateway
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onSelect={() => openCreate('nfs')}>
                  NFS export
                </DropdownMenuItem>
                <DropdownMenuItem onSelect={() => openCreate('iscsi')}>
                  iSCSI target
                </DropdownMenuItem>
                <DropdownMenuItem onSelect={() => openCreate('nvme')}>
                  NVMe-oF subsystem
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </>
        }
      />

      <Card>
        <CardContent className="px-0">
          {isLoading ? (
            <div className="space-y-2 px-5 py-2">
              {Array.from({ length: 4 }).map((_, i) => (
                <Skeleton key={i} className="h-12 w-full" />
              ))}
            </div>
          ) : shown.length === 0 ? (
            <div className="flex flex-col items-center justify-center gap-2 py-14 text-center">
              <Network className="h-7 w-7 text-muted-foreground" />
              <p className="text-sm text-muted-foreground">
                {list.length === 0
                  ? 'No gateways yet. Create one to export a resource over NFS, iSCSI or NVMe-oF.'
                  : `No ${PROTOCOL[filter as GwKind].label} gateways configured.`}
              </p>
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <StatusTickHead />
                  <TableHead>Gateway</TableHead>
                  <TableHead>Resource</TableHead>
                  <TableHead>Service IP</TableHead>
                  <TableHead>Active node</TableHead>
                  <TableHead>Clients</TableHead>
                  <TableHead className="pr-5 text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {shown.map((gateway) => (
                  <GatewayRow
                    key={gateway.id}
                    gateway={gateway}
                    expanded={expanded === gateway.id}
                    onToggle={() =>
                      setExpanded((cur) => (cur === gateway.id ? null : gateway.id))
                    }
                    onManage={() => setManageGateway(gateway)}
                    onDelete={() => setPendingDelete(gateway)}
                    onStart={() => startMutation.mutate(gateway.id)}
                    onStop={() => stopMutation.mutate(gateway.id)}
                    startPending={startMutation.isPending}
                    stopPending={stopMutation.isPending}
                  />
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <CreateGatewayDialog
        type={createType}
        onTypeChange={setCreateType}
        resources={resources?.resources ?? []}
        onCreated={() => {
          setCreateType(null);
          invalidate();
        }}
      />

      <ManageDialog
        gateway={manageGateway}
        onOpenChange={(open) => !open && setManageGateway(null)}
      />

      <AlertDialog
        open={!!pendingDelete}
        onOpenChange={(open) => !open && setPendingDelete(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete gateway?</AlertDialogTitle>
            <AlertDialogDescription>
              This removes the drbd-reactor config for gateway "
              {pendingDelete?.name || pendingDelete?.id}". The underlying DRBD
              resource is not affected.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() =>
                pendingDelete && deleteMutation.mutate(pendingDelete.id)
              }
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// ==================== Row ====================

/**
 * What the row and its expansion both need. The query keys are the ones the
 * manage dialog already uses, so opening the dialog reuses this cache instead
 * of re-fetching, and a mutation there invalidates the row too. Only the
 * queries this gateway's protocol has are enabled; the rest never fire.
 */
function useGatewayDetail(kind: GwKind, resource: string) {
  const nfs = kind === 'nfs';
  const iscsi = kind === 'iscsi';
  const nvme = kind === 'nvme';
  const exports = useQuery({
    queryKey: ['nfs-exports', resource],
    queryFn: () => api.listNFSExports(resource),
    enabled: nfs,
    retry: false,
  });
  const luns = useQuery({
    queryKey: ['iscsi-luns', resource],
    queryFn: () => api.listISCSILUNs(resource),
    enabled: iscsi,
    retry: false,
  });
  const initiators = useQuery({
    queryKey: ['iscsi-initiators', resource],
    queryFn: () => api.listISCSIInitiators(resource),
    enabled: iscsi,
    retry: false,
  });
  const chap = useQuery({
    queryKey: ['iscsi-chap', resource],
    queryFn: () => api.getISCSIChap(resource),
    enabled: iscsi,
    retry: false,
  });
  const namespaces = useQuery({
    queryKey: ['nvme-namespaces', resource],
    queryFn: () => api.listNVMeNamespaces(resource),
    enabled: nvme,
    retry: false,
  });
  const hosts = useQuery({
    queryKey: ['nvme-hosts', resource],
    queryFn: () => api.listNVMeHosts(resource),
    enabled: nvme,
    retry: false,
  });
  return { exports, luns, initiators, chap, namespaces, hosts };
}

function GatewayRow({
  gateway,
  expanded,
  onToggle,
  onManage,
  onDelete,
  onStart,
  onStop,
  startPending,
  stopPending,
}: {
  gateway: Gateway;
  expanded: boolean;
  onToggle: () => void;
  onManage: () => void;
  onDelete: () => void;
  onStart: () => void;
  onStop: () => void;
  startPending: boolean;
  stopPending: boolean;
}) {
  const kind = gwKind(gateway.type);
  const proto = PROTOCOL[kind];
  const detail = useGatewayDetail(kind, gateway.resource);
  const running = isRunning(gateway.state);
  const tone = gatewayTone(gateway.state);
  const options = (gateway.options ?? {}) as Record<string, unknown>;
  const opt = (key: string) => {
    const v = options[key];
    return typeof v === 'string' && v ? v : '';
  };
  const serviceIp = opt('service_ip');
  const name = gateway.name || gateway.id;

  // The subtitle's count comes from the protocol's own list endpoint; while it
  // is still loading or has errored there is no honest number to print.
  const items =
    kind === 'nfs'
      ? detail.exports.data?.exports
      : kind === 'iscsi'
        ? detail.luns.data?.luns
        : detail.namespaces.data?.namespaces;
  const itemLabel = kind === 'nfs' ? 'export' : kind === 'iscsi' ? 'LUN' : 'namespace';
  const subtitle = [
    proto.label,
    `port ${proto.port}`,
    items ? plural(items.length, itemLabel) : null,
  ]
    .filter(Boolean)
    .join(' · ');

  return (
    <>
      <TableRow className={expanded ? 'border-b-transparent' : undefined}>
        <StatusTickCell tone={tone} />
        <TableCell>
          <button
            type="button"
            aria-expanded={expanded}
            onClick={onToggle}
            className="flex items-center gap-2.5 text-left outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
          >
            {expanded ? (
              <ChevronDown className="size-3.5 shrink-0 text-foreground" />
            ) : (
              <ChevronRight className="size-3.5 shrink-0 text-muted-foreground" />
            )}
            <span className="min-w-0">
              <span className="block max-w-[220px] truncate font-mono text-sm font-semibold">
                {name}
              </span>
              <span className="mt-0.5 block text-[11.5px] text-muted-foreground">
                {subtitle}
              </span>
            </span>
          </button>
        </TableCell>
        <TableCell className="font-mono text-muted-foreground">
          {gateway.resource}
        </TableCell>
        <TableCell className="font-mono text-muted-foreground">
          <span className="block max-w-[170px] truncate" title={serviceIp || undefined}>
            {serviceIp || '—'}
          </span>
        </TableCell>
        <TableCell>
          <span className="block font-mono">{gateway.node || '—'}</span>
          {running ? null : (
            <span className="mt-0.5 block text-[11.5px] text-muted-foreground">
              {gateway.state || 'unknown'}
            </span>
          )}
        </TableCell>
        <TableCell className="text-muted-foreground">
          <ClientsSummary kind={kind} detail={detail} />
        </TableCell>
        <TableCell className="pr-5 text-right">
          <div className="flex items-center justify-end gap-1.5">
            {running ? (
              <Button
                variant="outline"
                size="sm"
                disabled={stopPending}
                onClick={onStop}
              >
                {stopPending && <Loader2 className="animate-spin" />}
                Stop
              </Button>
            ) : (
              <Button size="sm" disabled={startPending} onClick={onStart}>
                {startPending && <Loader2 className="animate-spin" />}
                Start
              </Button>
            )}
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="icon-sm" aria-label={`More actions for ${name}`}>
                  <MoreHorizontal />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onSelect={onManage}>
                  Manage {proto.label}…
                </DropdownMenuItem>
                <DropdownMenuItem onSelect={onToggle}>
                  {expanded ? 'Hide detail' : 'Show detail'}
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem variant="destructive" onSelect={onDelete}>
                  Delete gateway
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </TableCell>
      </TableRow>

      {expanded ? (
        <TableRow className="hover:bg-transparent">
          <TableCell colSpan={7} className="h-auto bg-muted/40 p-0">
            <div className="grid grid-cols-1 gap-6 py-1 pr-6 pb-6 pl-11 lg:grid-cols-5">
              <div className="min-w-0 lg:col-span-3">
                {kind === 'nfs' ? (
                  <NFSDetailPanel detail={detail} onManage={onManage} />
                ) : kind === 'iscsi' ? (
                  <ISCSIDetailPanel detail={detail} onManage={onManage} />
                ) : (
                  <NVMeDetailPanel detail={detail} onManage={onManage} />
                )}
              </div>
              <div className="min-w-0 lg:col-span-2">
                <StartChainPanel
                  kind={kind}
                  detail={detail}
                  state={gateway.state}
                  identity={
                    kind === 'nfs'
                      ? [
                          ['Export directory', opt('export_directory') || opt('export_path')],
                          ['Filesystem', opt('fs_type')],
                        ]
                      : kind === 'iscsi'
                        ? [
                            ['Target IQN', opt('iqn')],
                            ['Implementation', opt('implementation')],
                          ]
                        : [
                            ['Subsystem NQN', opt('nqn')],
                            ['Transport', opt('transport_type')],
                          ]
                  }
                />
              </div>
            </div>
          </TableCell>
        </TableRow>
      ) : null}
    </>
  );
}

type GatewayDetail = ReturnType<typeof useGatewayDetail>;

/**
 * Who can reach this gateway, in the terms its protocol actually uses. Nothing
 * here is inferred: an empty allow-list is reported as empty, and a list we
 * could not read is reported as unknown rather than as zero.
 */
function ClientsSummary({ kind, detail }: { kind: GwKind; detail: GatewayDetail }) {
  if (kind === 'nfs') {
    const q = detail.exports;
    if (q.isLoading) return <span>…</span>;
    if (q.error || !q.data) return <span>—</span>;
    const specs = [
      ...new Set(q.data.exports.map((e) => e.clientspec).filter(Boolean)),
    ];
    if (specs.length > 0) {
      return <span title={specs.join(', ')}>{plural(specs.length, 'allowed client')}</span>;
    }
    return <span>{q.data.exports.length > 0 ? 'no client restriction' : 'no exports yet'}</span>;
  }

  if (kind === 'iscsi') {
    const q = detail.initiators;
    if (q.isLoading) return <span>…</span>;
    if (q.error || !q.data) return <span>—</span>;
    const n = q.data.initiators.length;
    const chap = detail.chap.data?.username ? ' · CHAP' : '';
    return (
      <span title={q.data.initiators.join(', ') || undefined}>
        {n === 0 ? 'no initiators allowed yet' : plural(n, 'initiator')}
        {chap}
      </span>
    );
  }

  const q = detail.hosts;
  if (q.isLoading) return <span>…</span>;
  if (q.error || !q.data) return <span>—</span>;
  const n = q.data.hosts.length;
  return (
    <span title={q.data.hosts.join(', ') || undefined}>
      {n === 0 ? 'no hosts allowed yet' : plural(n, 'allowed host')}
    </span>
  );
}

// ==================== Expanded detail ====================

function PanelLabel({ children }: { children: ReactNode }) {
  return <div className="eyebrow mb-2.5">{children}</div>;
}

/** A denser table than the page's own — 40px rows, for a panel inside a row. */
function MiniTable({ heads, children }: { heads: string[]; children: ReactNode }) {
  return (
    <div className="overflow-x-auto rounded-lg border border-border bg-card">
      <table className="w-full border-collapse text-left">
        <thead>
          <tr>
            {heads.map((h) => (
              <th key={h} className="eyebrow px-3.5 pt-2.5 pb-2 whitespace-nowrap">
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

const MINI_TD =
  'h-10 border-t border-border/60 px-3.5 text-[12.5px] whitespace-nowrap';

function MiniEmpty({ colSpan, label }: { colSpan: number; label: string }) {
  return (
    <tr>
      <td colSpan={colSpan} className={`${MINI_TD} text-center text-muted-foreground`}>
        {label}
      </td>
    </tr>
  );
}

/** Long identifiers get one line and a tooltip, never a wrap. */
function Mono({ value, className }: { value: string; className?: string }) {
  return (
    <span
      title={value || undefined}
      className={`block max-w-[280px] truncate font-mono ${className ?? ''}`}
    >
      {value || '—'}
    </span>
  );
}

function PanelState({ query }: { query: { isLoading: boolean; error: unknown } }) {
  if (query.error) return <QueryError message={(query.error as Error).message} />;
  return <Skeleton className="h-24 w-full" />;
}

function NFSDetailPanel({
  detail,
  onManage,
}: {
  detail: GatewayDetail;
  onManage: () => void;
}) {
  const q = detail.exports;
  const exports: NFSExport[] = q.data?.exports ?? [];
  return (
    <div>
      <PanelLabel>Exports</PanelLabel>
      {q.isLoading || q.error ? (
        <PanelState query={q} />
      ) : (
        <MiniTable heads={['Directory', 'FSID', 'Client', 'Options']}>
          {exports.length === 0 ? (
            <MiniEmpty colSpan={4} label="No exports configured" />
          ) : (
            exports.map((e) => (
              <tr key={e.directory}>
                <td className={MINI_TD}>
                  <Mono value={e.directory} />
                </td>
                <td className={MINI_TD}>
                  <Mono value={e.fsid} className="text-muted-foreground" />
                </td>
                <td className={MINI_TD}>
                  <Mono value={e.clientspec} />
                </td>
                <td className={MINI_TD}>
                  <Mono value={e.options} className="text-muted-foreground" />
                </td>
              </tr>
            ))
          )}
        </MiniTable>
      )}
      <Button variant="outline" size="sm" className="mt-3" onClick={onManage}>
        <Plus />
        Manage exports
      </Button>
    </div>
  );
}

function ISCSIDetailPanel({
  detail,
  onManage,
}: {
  detail: GatewayDetail;
  onManage: () => void;
}) {
  const q = detail.luns;
  const luns: ISCSILUN[] = q.data?.luns ?? [];
  const initiators = detail.initiators.data?.initiators ?? [];
  const chap = detail.chap.data;
  return (
    <div>
      <PanelLabel>LUNs</PanelLabel>
      {q.isLoading || q.error ? (
        <PanelState query={q} />
      ) : (
        <MiniTable heads={['LUN', 'Device', 'Target IQN']}>
          {luns.length === 0 ? (
            <MiniEmpty colSpan={3} label="No LUNs configured" />
          ) : (
            luns.map((l) => (
              <tr key={l.lun}>
                <td className={`${MINI_TD} font-mono`}>{l.lun}</td>
                <td className={MINI_TD}>
                  <Mono value={l.device} />
                </td>
                <td className={MINI_TD}>
                  <Mono value={l.targetIqn} className="text-muted-foreground" />
                </td>
              </tr>
            ))
          )}
        </MiniTable>
      )}

      <div className="mt-4">
        <PanelLabel>Allowed initiators</PanelLabel>
        {detail.initiators.error ? (
          <QueryError message={(detail.initiators.error as Error).message} />
        ) : initiators.length === 0 ? (
          <p className="text-[12.5px] text-muted-foreground">
            None — no initiator could log in.
          </p>
        ) : (
          <div className="flex flex-wrap gap-1.5">
            {initiators.map((iqn) => (
              <Badge
                key={iqn}
                variant="secondary"
                className="max-w-[280px] font-mono"
                title={iqn}
              >
                <span className="truncate">{iqn}</span>
              </Badge>
            ))}
          </div>
        )}
        <p className="mt-2.5 text-[12.5px] text-muted-foreground">
          {chap?.username ? (
            <>
              CHAP on, user{' '}
              <span className="font-mono text-foreground">{chap.username}</span>
              {chap.mutual ? ', mutual' : ', one-way'}
            </>
          ) : (
            'CHAP not configured.'
          )}
        </p>
      </div>

      <Button variant="outline" size="sm" className="mt-3" onClick={onManage}>
        Manage LUNs, initiators and CHAP
      </Button>
    </div>
  );
}

function NVMeDetailPanel({
  detail,
  onManage,
}: {
  detail: GatewayDetail;
  onManage: () => void;
}) {
  const q = detail.namespaces;
  const namespaces: NVMeNamespace[] = q.data?.namespaces ?? [];
  const hosts = detail.hosts.data?.hosts ?? [];
  return (
    <div>
      <PanelLabel>Namespaces</PanelLabel>
      {q.isLoading || q.error ? (
        <PanelState query={q} />
      ) : (
        <MiniTable heads={['NSID', 'Backing path', 'UUID']}>
          {namespaces.length === 0 ? (
            <MiniEmpty colSpan={3} label="No namespaces configured" />
          ) : (
            namespaces.map((ns) => (
              <tr key={ns.namespaceId}>
                <td className={`${MINI_TD} font-mono`}>{ns.namespaceId}</td>
                <td className={MINI_TD}>
                  <Mono value={ns.backingPath} />
                </td>
                <td className={MINI_TD}>
                  <Mono value={ns.uuid} className="text-muted-foreground" />
                </td>
              </tr>
            ))
          )}
        </MiniTable>
      )}

      <div className="mt-4">
        <PanelLabel>Allowed hosts</PanelLabel>
        {detail.hosts.error ? (
          <QueryError message={(detail.hosts.error as Error).message} />
        ) : hosts.length === 0 ? (
          <p className="text-[12.5px] text-muted-foreground">
            None — no host could connect.
          </p>
        ) : (
          <div className="flex flex-wrap gap-1.5">
            {hosts.map((nqn) => (
              <Badge
                key={nqn}
                variant="secondary"
                className="max-w-[280px] font-mono"
                title={nqn}
              >
                <span className="truncate">{nqn}</span>
              </Badge>
            ))}
          </div>
        )}
      </div>

      <Button variant="outline" size="sm" className="mt-3" onClick={onManage}>
        Manage namespaces and hosts
      </Button>
    </div>
  );
}

/**
 * The promoter's start[] array, in order. The fixed agents are the ones the
 * generator writes for this protocol (pkg/gateway/{nfs,iscsi,nvmeof}.go); the
 * repeated ones are one per export / LUN / namespace, which is exactly what
 * the list endpoints above parse back out of the same config — so the chain is
 * as long as the gateway really is, not as long as a template says.
 *
 * The dots repeat the gateway state named in words directly above them; no
 * agent is probed individually, so none of them may claim its own health.
 */
function StartChainPanel({
  kind,
  detail,
  state,
  identity,
}: {
  kind: GwKind;
  detail: GatewayDetail;
  state: string;
  identity: [string, string][];
}) {
  const running = isRunning(state);
  const tone = gatewayTone(state);

  let chain: string[];
  if (kind === 'nfs') {
    const exports = detail.exports.data?.exports ?? [];
    chain = [
      'ocf:heartbeat:Filesystem fs_cluster_private',
      'ocf:heartbeat:Filesystem fs_export',
      'ocf:heartbeat:IPaddr2 service_ip',
      'ocf:heartbeat:nfsserver nfsserver',
      ...exports.map((_, i) => `ocf:heartbeat:exportfs export_${i}`),
    ];
  } else if (kind === 'iscsi') {
    const luns = detail.luns.data?.luns ?? [];
    chain = [
      'ocf:heartbeat:Filesystem fs_cluster_private',
      'ocf:heartbeat:IPaddr2 service_ip0',
      'ocf:heartbeat:iSCSITarget target',
      ...luns.map((l) => `ocf:heartbeat:iSCSILogicalUnit lu${l.lun}`),
    ];
  } else {
    const namespaces = detail.namespaces.data?.namespaces ?? [];
    chain = [
      'ocf:heartbeat:Filesystem fs_cluster_private',
      'ocf:heartbeat:IPaddr2 service_ip',
      'ocf:heartbeat:nvmet-subsystem subsys',
      ...namespaces.map((ns) => `ocf:heartbeat:nvmet-namespace ns_${ns.namespaceId}`),
      'ocf:heartbeat:nvmet-port port',
    ];
  }

  const shownIdentity = identity.filter(([, value]) => value);

  return (
    <div>
      <PanelLabel>Promoter start chain</PanelLabel>
      <div className="rounded-lg border border-border bg-card p-4">
        <p className="mb-3 flex items-center gap-2 text-[12.5px] text-muted-foreground">
          <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${TONE_BG[tone]}`} />
          {plural(chain.length, 'agent')}, started in order —{' '}
          {running ? 'currently running' : `gateway ${state || 'not started'}`}
        </p>
        <ol className="flex flex-col gap-2">
          {chain.map((agent, i) => (
            <li key={agent} className="flex items-center gap-2.5 text-[12.5px]">
              <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
                {i + 1}
              </span>
              <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${TONE_BG[tone]}`} />
              <span className="min-w-0 truncate font-mono" title={agent}>
                {agent}
              </span>
            </li>
          ))}
        </ol>
        {shownIdentity.length > 0 ? (
          <dl className="mt-3.5 space-y-1.5 border-t border-border/70 pt-3">
            {shownIdentity.map(([label, value]) => (
              <div key={label} className="flex items-baseline justify-between gap-3">
                <dt className="shrink-0 text-[11.5px] text-muted-foreground">
                  {label}
                </dt>
                <dd className="min-w-0 truncate font-mono text-[11.5px]" title={value}>
                  {value}
                </dd>
              </div>
            ))}
          </dl>
        ) : null}
      </div>
    </div>
  );
}

// ==================== Create Dialog ====================

function CreateGatewayDialog({
  type,
  onTypeChange,
  resources,
  onCreated,
}: {
  type: GwKind | null;
  onTypeChange: (type: GwKind | null) => void;
  resources: Resource[];
  onCreated: () => void;
}) {
  return (
    <Dialog open={!!type} onOpenChange={(open) => !open && onTypeChange(null)}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Create Gateway</DialogTitle>
          <DialogDescription>
            Expose a DRBD resource via NFS, iSCSI or NVMe-oF.
          </DialogDescription>
        </DialogHeader>
        <Tabs
          value={type ?? 'nfs'}
          onValueChange={(v) => onTypeChange(v as GwKind)}
        >
          <TabsList className="grid w-full grid-cols-3">
            <TabsTrigger value="nfs">NFS</TabsTrigger>
            <TabsTrigger value="iscsi">iSCSI</TabsTrigger>
            <TabsTrigger value="nvme">NVMe</TabsTrigger>
          </TabsList>
          <TabsContent value="nfs">
            <CreateNFSForm resources={resources} onCreated={onCreated} />
          </TabsContent>
          <TabsContent value="iscsi">
            <CreateISCSIForm resources={resources} onCreated={onCreated} />
          </TabsContent>
          <TabsContent value="nvme">
            <CreateNVMeForm resources={resources} onCreated={onCreated} />
          </TabsContent>
        </Tabs>
      </DialogContent>
    </Dialog>
  );
}

function ResourceSelect({
  resources,
  value,
  onChange,
}: {
  resources: Resource[];
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <div className="space-y-1.5">
      <Label>DRBD Resource</Label>
      <Select value={value} onValueChange={onChange}>
        <SelectTrigger className="w-full">
          <SelectValue placeholder="Select a resource..." />
        </SelectTrigger>
        <SelectContent>
          {resources.map((r) => (
            <SelectItem key={r.name} value={r.name} className="font-mono">
              {r.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}

function CreateNFSForm({
  resources,
  onCreated,
}: {
  resources: Resource[];
  onCreated: () => void;
}) {
  const [resource, setResource] = useState('');
  const [serviceIp, setServiceIp] = useState('');
  const [exportPath, setExportPath] = useState('');
  const [allowedIps, setAllowedIps] = useState('');
  const [fsType, setFsType] = useState('ext4');

  const mutation = useMutation({
    mutationFn: () =>
      api.createNFSGateway({
        resource,
        serviceIp,
        exportPath,
        allowedIps: allowedIps
          ? allowedIps.split(',').map((s) => s.trim()).filter(Boolean)
          : undefined,
        fsType,
      }),
    onSuccess: () => {
      toast.success('NFS gateway created');
      onCreated();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <form
      className="space-y-4 pt-2"
      onSubmit={(e) => {
        e.preventDefault();
        mutation.mutate();
      }}
    >
      <ResourceSelect
        resources={resources}
        value={resource}
        onChange={setResource}
      />
      <div className="space-y-1.5">
        <Label>Service IP (CIDR)</Label>
        <Input
          className="font-mono"
          value={serviceIp}
          onChange={(e) => setServiceIp(e.target.value)}
          placeholder="192.168.1.200/24"
          required
        />
      </div>
      <div className="space-y-1.5">
        <Label>Export Path</Label>
        <Input
          className="font-mono"
          value={exportPath}
          onChange={(e) => setExportPath(e.target.value)}
          placeholder="/data"
          required
        />
      </div>
      <div className="space-y-1.5">
        <Label>Allowed IPs (comma-separated, optional)</Label>
        <Input
          className="font-mono"
          value={allowedIps}
          onChange={(e) => setAllowedIps(e.target.value)}
          placeholder="192.168.1.0/24, 10.0.0.0/8"
        />
      </div>
      <div className="space-y-1.5">
        <Label>Filesystem Type</Label>
        <Select value={fsType} onValueChange={setFsType}>
          <SelectTrigger className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="ext4">ext4</SelectItem>
            <SelectItem value="xfs">XFS</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <DialogFooter>
        <Button type="submit" disabled={mutation.isPending || !resource}>
          {mutation.isPending && (
            <Loader2 className="mr-2 h-4 w-4 animate-spin" />
          )}
          Create NFS Gateway
        </Button>
      </DialogFooter>
    </form>
  );
}

function CreateISCSIForm({
  resources,
  onCreated,
}: {
  resources: Resource[];
  onCreated: () => void;
}) {
  const [resource, setResource] = useState('');
  const [serviceIp, setServiceIp] = useState('');
  const [iqn, setIqn] = useState('');
  const [allowedInitiators, setAllowedInitiators] = useState('');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [implementation, setImplementation] = useState('lio-t');

  const mutation = useMutation({
    mutationFn: () =>
      api.createISCSIGateway({
        resource,
        serviceIp,
        iqn,
        allowedInitiators: allowedInitiators
          ? allowedInitiators.split(',').map((s) => s.trim()).filter(Boolean)
          : undefined,
        username: username || undefined,
        password: password || undefined,
        implementation,
      }),
    onSuccess: () => {
      toast.success('iSCSI gateway created');
      onCreated();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <form
      className="space-y-4 pt-2"
      onSubmit={(e) => {
        e.preventDefault();
        mutation.mutate();
      }}
    >
      <ResourceSelect
        resources={resources}
        value={resource}
        onChange={setResource}
      />
      <div className="space-y-1.5">
        <Label>Service IP (CIDR)</Label>
        <Input
          className="font-mono"
          value={serviceIp}
          onChange={(e) => setServiceIp(e.target.value)}
          placeholder="192.168.1.100/24"
          required
        />
      </div>
      <div className="space-y-1.5">
        <Label>IQN</Label>
        <Input
          className="font-mono"
          value={iqn}
          onChange={(e) => setIqn(e.target.value)}
          placeholder="iqn.2024-01.com.example:sds.data"
          required
        />
      </div>
      <div className="space-y-1.5">
        <Label>Allowed Initiators (comma-separated, optional)</Label>
        <Input
          className="font-mono"
          value={allowedInitiators}
          onChange={(e) => setAllowedInitiators(e.target.value)}
          placeholder="iqn.1994-05.com.redhat:..."
        />
      </div>
      <div className="grid grid-cols-2 gap-4">
        <div className="space-y-1.5">
          <Label>CHAP Username (optional)</Label>
          <Input
            value={username}
            onChange={(e) => setUsername(e.target.value)}
          />
        </div>
        <div className="space-y-1.5">
          <Label>CHAP Password (optional)</Label>
          <Input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </div>
      </div>
      <div className="space-y-1.5">
        <Label>Implementation</Label>
        <Select value={implementation} onValueChange={setImplementation}>
          <SelectTrigger className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="lio-t">LIO (targetcli)</SelectItem>
            <SelectItem value="scst">SCST</SelectItem>
            <SelectItem value="tgt">TGT</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <DialogFooter>
        <Button type="submit" disabled={mutation.isPending || !resource}>
          {mutation.isPending && (
            <Loader2 className="mr-2 h-4 w-4 animate-spin" />
          )}
          Create iSCSI Gateway
        </Button>
      </DialogFooter>
    </form>
  );
}

function CreateNVMeForm({
  resources,
  onCreated,
}: {
  resources: Resource[];
  onCreated: () => void;
}) {
  const [resource, setResource] = useState('');
  const [serviceIp, setServiceIp] = useState('');
  const [nqn, setNqn] = useState('');
  const [transportType, setTransportType] = useState('tcp');

  const mutation = useMutation({
    mutationFn: () =>
      api.createNVMeGateway({ resource, serviceIp, nqn, transportType }),
    onSuccess: () => {
      toast.success('NVMe gateway created');
      onCreated();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <form
      className="space-y-4 pt-2"
      onSubmit={(e) => {
        e.preventDefault();
        mutation.mutate();
      }}
    >
      <ResourceSelect
        resources={resources}
        value={resource}
        onChange={setResource}
      />
      <div className="space-y-1.5">
        <Label>Service IP (CIDR)</Label>
        <Input
          className="font-mono"
          value={serviceIp}
          onChange={(e) => setServiceIp(e.target.value)}
          placeholder="192.168.1.150/24"
          required
        />
      </div>
      <div className="space-y-1.5">
        <Label>NQN</Label>
        <Input
          className="font-mono"
          value={nqn}
          onChange={(e) => setNqn(e.target.value)}
          placeholder="nqn.2024-01.com.example:sds.data"
          required
        />
      </div>
      <div className="space-y-1.5">
        <Label>Transport Type</Label>
        <Select value={transportType} onValueChange={setTransportType}>
          <SelectTrigger className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="tcp">TCP</SelectItem>
            <SelectItem value="rdma">RDMA</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <DialogFooter>
        <Button type="submit" disabled={mutation.isPending || !resource}>
          {mutation.isPending && (
            <Loader2 className="mr-2 h-4 w-4 animate-spin" />
          )}
          Create NVMe Gateway
        </Button>
      </DialogFooter>
    </form>
  );
}

// ==================== Manage Dialog ====================

function ManageDialog({
  gateway,
  onOpenChange,
}: {
  gateway: Gateway | null;
  onOpenChange: (open: boolean) => void;
}) {
  const queryClient = useQueryClient();
  const restartMutation = useMutation({
    mutationFn: async (id: string) => {
      await api.stopGateway(id);
      await api.startGateway(id);
    },
    onSuccess: () => {
      toast.success('Gateway restarting; pending changes will apply');
      queryClient.invalidateQueries({ queryKey: ['gateways'] });
      onOpenChange(false);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const kind = gateway ? gwKind(gateway.type) : null;

  return (
    <Dialog open={!!gateway} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>
            Manage {kind ? PROTOCOL[kind].label : ''} gateway —{' '}
            <span className="font-mono">{gateway?.name || gateway?.id}</span>
          </DialogTitle>
          <DialogDescription>
            Resource:{' '}
            <span className="font-mono text-foreground">{gateway?.resource}</span>
          </DialogDescription>
        </DialogHeader>

        <div className="flex items-start justify-between gap-3 rounded-lg border border-border bg-muted/60 p-3 text-sm">
          <div className="flex items-start gap-2">
            <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
            <span>
              Changes are persisted to the gateway config and take effect on the
              next restart or failover — the running target is not modified live.
            </span>
          </div>
          <Button
            size="sm"
            variant="outline"
            className="shrink-0"
            disabled={restartMutation.isPending}
            onClick={() => gateway && restartMutation.mutate(gateway.id)}
          >
            {restartMutation.isPending && (
              <Loader2 className="h-3 w-3 animate-spin" />
            )}
            Restart now
          </Button>
        </div>

        {kind === 'nfs' && <ManageNFS resource={gateway!.resource} />}
        {kind === 'iscsi' && <ManageISCSI resource={gateway!.resource} />}
        {kind === 'nvme' && <ManageNVMe resource={gateway!.resource} />}
      </DialogContent>
    </Dialog>
  );
}

function QueryError({ message }: { message: string }) {
  return (
    <div className="flex items-start gap-2 rounded-lg border border-status-warn/40 bg-status-warn-soft p-3 text-[12.5px] text-status-warn-text">
      <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
      <span>{message}</span>
    </div>
  );
}

function EmptyRow({ colSpan, label }: { colSpan: number; label: string }) {
  return (
    <TableRow>
      <TableCell
        colSpan={colSpan}
        className="py-6 text-center text-muted-foreground"
      >
        {label}
      </TableCell>
    </TableRow>
  );
}

// ---------- NFS Management ----------

function ManageNFS({ resource }: { resource: string }) {
  const queryClient = useQueryClient();
  const queryKey = ['nfs-exports', resource];
  const { data, isLoading, error } = useQuery({
    queryKey,
    queryFn: () => api.listNFSExports(resource),
    retry: false,
  });

  const [exportPath, setExportPath] = useState('');
  const [clientSpec, setClientSpec] = useState('');
  const [options, setOptions] = useState('');

  const invalidate = () => queryClient.invalidateQueries({ queryKey });

  const addMutation = useMutation({
    mutationFn: () =>
      api.addNFSExport({
        resource,
        exportPath,
        clientSpec: clientSpec || undefined,
        options: options || undefined,
      }),
    onSuccess: () => {
      toast.success('Export added');
      setExportPath('');
      setClientSpec('');
      setOptions('');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const removeMutation = useMutation({
    mutationFn: (path: string) => api.removeNFSExport(resource, path),
    onSuccess: () => {
      toast.success('Export removed');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const exports: NFSExport[] = data?.exports ?? [];

  return (
    <div className="space-y-4">
      {error ? (
        <QueryError message={(error as Error).message} />
      ) : isLoading ? (
        <Skeleton className="h-24 w-full" />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Directory</TableHead>
              <TableHead>FSID</TableHead>
              <TableHead>Client</TableHead>
              <TableHead>Options</TableHead>
              <TableHead className="text-right">Action</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {exports.length === 0 ? (
              <EmptyRow colSpan={5} label="No exports configured" />
            ) : (
              exports.map((exp) => (
                <TableRow key={exp.directory}>
                  <TableCell>
                    <Mono value={exp.directory} className="text-xs" />
                  </TableCell>
                  <TableCell>
                    <Mono value={exp.fsid} className="text-xs text-muted-foreground" />
                  </TableCell>
                  <TableCell>
                    <Mono value={exp.clientspec} className="text-xs" />
                  </TableCell>
                  <TableCell>
                    <Mono value={exp.options} className="text-xs" />
                  </TableCell>
                  <TableCell className="text-right">
                    <RemoveButton
                      title="Remove export?"
                      description={`Remove NFS export "${exp.directory}"?`}
                      onConfirm={() => removeMutation.mutate(exp.directory)}
                    />
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      )}

      <form
        className="space-y-3 rounded-lg border border-border p-3"
        onSubmit={(e) => {
          e.preventDefault();
          addMutation.mutate();
        }}
      >
        <p className="text-sm font-medium">Add Export</p>
        <div className="space-y-1.5">
          <Label>Export Path</Label>
          <Input
            className="font-mono"
            value={exportPath}
            onChange={(e) => setExportPath(e.target.value)}
            placeholder="/data/share"
            required
          />
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div className="space-y-1.5">
            <Label>Client Spec</Label>
            <Input
              className="font-mono"
              value={clientSpec}
              onChange={(e) => setClientSpec(e.target.value)}
              placeholder="192.168.1.0/24"
            />
          </div>
          <div className="space-y-1.5">
            <Label>Options</Label>
            <Input
              className="font-mono"
              value={options}
              onChange={(e) => setOptions(e.target.value)}
              placeholder="rw,sync,no_root_squash"
            />
          </div>
        </div>
        <Button
          type="submit"
          size="sm"
          disabled={addMutation.isPending || !exportPath}
        >
          {addMutation.isPending ? (
            <Loader2 className="mr-2 h-4 w-4 animate-spin" />
          ) : (
            <Plus className="mr-2 h-4 w-4" />
          )}
          Add Export
        </Button>
      </form>
    </div>
  );
}

// ---------- iSCSI Management ----------

function ManageISCSI({ resource }: { resource: string }) {
  return (
    <Tabs defaultValue="luns">
      <TabsList className="grid w-full grid-cols-3">
        <TabsTrigger value="luns">LUNs</TabsTrigger>
        <TabsTrigger value="initiators">Initiators</TabsTrigger>
        <TabsTrigger value="chap">CHAP</TabsTrigger>
      </TabsList>
      <TabsContent value="luns">
        <ISCSILuns resource={resource} />
      </TabsContent>
      <TabsContent value="initiators">
        <ISCSIInitiators resource={resource} />
      </TabsContent>
      <TabsContent value="chap">
        <ISCSIChap resource={resource} />
      </TabsContent>
    </Tabs>
  );
}

function ISCSILuns({ resource }: { resource: string }) {
  const queryClient = useQueryClient();
  const queryKey = ['iscsi-luns', resource];
  const { data, isLoading, error } = useQuery({
    queryKey,
    queryFn: () => api.listISCSILUNs(resource),
    retry: false,
  });

  const [lun, setLun] = useState('');
  const [device, setDevice] = useState('');
  const invalidate = () => queryClient.invalidateQueries({ queryKey });

  const addMutation = useMutation({
    mutationFn: () =>
      api.addISCSILUN({ resource, lun: parseInt(lun, 10), device }),
    onSuccess: () => {
      toast.success('LUN added');
      setLun('');
      setDevice('');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const removeMutation = useMutation({
    mutationFn: (l: number) => api.removeISCSILUN(resource, l),
    onSuccess: () => {
      toast.success('LUN removed');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const luns: ISCSILUN[] = data?.luns ?? [];

  return (
    <div className="space-y-4 pt-2">
      {error ? (
        <QueryError message={(error as Error).message} />
      ) : isLoading ? (
        <Skeleton className="h-24 w-full" />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>LUN</TableHead>
              <TableHead>Device</TableHead>
              <TableHead>Target IQN</TableHead>
              <TableHead className="text-right">Action</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {luns.length === 0 ? (
              <EmptyRow colSpan={4} label="No LUNs configured" />
            ) : (
              luns.map((l) => (
                <TableRow key={l.lun}>
                  <TableCell className="font-mono">{l.lun}</TableCell>
                  <TableCell>
                    <Mono value={l.device} className="text-xs" />
                  </TableCell>
                  <TableCell>
                    <Mono
                      value={l.targetIqn}
                      className="text-xs text-muted-foreground"
                    />
                  </TableCell>
                  <TableCell className="text-right">
                    <RemoveButton
                      title="Remove LUN?"
                      description={`Remove LUN ${l.lun}?`}
                      onConfirm={() => removeMutation.mutate(l.lun)}
                    />
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      )}

      <form
        className="space-y-3 rounded-lg border border-border p-3"
        onSubmit={(e) => {
          e.preventDefault();
          addMutation.mutate();
        }}
      >
        <p className="text-sm font-medium">Add LUN</p>
        <div className="grid grid-cols-2 gap-3">
          <div className="space-y-1.5">
            <Label>LUN Number</Label>
            <Input
              className="font-mono"
              type="number"
              value={lun}
              onChange={(e) => setLun(e.target.value)}
              placeholder="1"
              required
            />
          </div>
          <div className="space-y-1.5">
            <Label>Device</Label>
            <Input
              className="font-mono"
              value={device}
              onChange={(e) => setDevice(e.target.value)}
              placeholder="/dev/drbd1001"
              required
            />
          </div>
        </div>
        <Button
          type="submit"
          size="sm"
          disabled={addMutation.isPending || !lun || !device}
        >
          {addMutation.isPending ? (
            <Loader2 className="mr-2 h-4 w-4 animate-spin" />
          ) : (
            <Plus className="mr-2 h-4 w-4" />
          )}
          Add LUN
        </Button>
      </form>
    </div>
  );
}

function ISCSIInitiators({ resource }: { resource: string }) {
  const queryClient = useQueryClient();
  const queryKey = ['iscsi-initiators', resource];
  const { data, isLoading, error } = useQuery({
    queryKey,
    queryFn: () => api.listISCSIInitiators(resource),
    retry: false,
  });

  const [initiator, setInitiator] = useState('');
  const invalidate = () => queryClient.invalidateQueries({ queryKey });

  const addMutation = useMutation({
    mutationFn: () => api.addISCSIInitiator(resource, initiator),
    onSuccess: () => {
      toast.success('Initiator added');
      setInitiator('');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const removeMutation = useMutation({
    mutationFn: (iqn: string) => api.removeISCSIInitiator(resource, iqn),
    onSuccess: () => {
      toast.success('Initiator removed');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const initiators = data?.initiators ?? [];

  return (
    <div className="space-y-4 pt-2">
      {error ? (
        <QueryError message={(error as Error).message} />
      ) : isLoading ? (
        <Skeleton className="h-12 w-full" />
      ) : initiators.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          No allowed initiators configured.
        </p>
      ) : (
        <div className="flex flex-wrap gap-2">
          {initiators.map((iqn) => (
            <Badge
              key={iqn}
              variant="secondary"
              className="max-w-[320px] gap-1 font-mono"
              title={iqn}
            >
              <span className="truncate">{iqn}</span>
              <button
                type="button"
                aria-label={`Remove initiator ${iqn}`}
                onClick={() => removeMutation.mutate(iqn)}
                className="ml-1 shrink-0 rounded-full hover:text-destructive"
              >
                <X className="h-3 w-3" />
              </button>
            </Badge>
          ))}
        </div>
      )}

      <form
        className="flex items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          addMutation.mutate();
        }}
      >
        <div className="flex-1 space-y-1.5">
          <Label>Initiator IQN</Label>
          <Input
            className="font-mono"
            value={initiator}
            onChange={(e) => setInitiator(e.target.value)}
            placeholder="iqn.1994-05.com.redhat:..."
            required
          />
        </div>
        <Button type="submit" disabled={addMutation.isPending || !initiator}>
          {addMutation.isPending ? (
            <Loader2 className="h-4 w-4 animate-spin" />
          ) : (
            <Plus className="h-4 w-4" />
          )}
        </Button>
      </form>
    </div>
  );
}

function ISCSIChap({ resource }: { resource: string }) {
  const queryClient = useQueryClient();
  const queryKey = ['iscsi-chap', resource];
  const { data, isLoading, error } = useQuery({
    queryKey,
    queryFn: () => api.getISCSIChap(resource),
    retry: false,
  });

  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [mutual, setMutual] = useState(false);
  const [loaded, setLoaded] = useState(false);

  if (data && !loaded) {
    setUsername(data.username ?? '');
    setPassword(data.password ?? '');
    setMutual(!!data.mutual);
    setLoaded(true);
  }

  const setMutation = useMutation({
    mutationFn: () =>
      api.setISCSIChap({ resource, username, password, mutual }),
    onSuccess: () => {
      toast.success('CHAP credentials updated');
      queryClient.invalidateQueries({ queryKey });
    },
    onError: (e: Error) => toast.error(e.message),
  });

  if (error) {
    return (
      <div className="pt-2">
        <QueryError message={(error as Error).message} />
      </div>
    );
  }
  if (isLoading) {
    return <Skeleton className="mt-2 h-32 w-full" />;
  }

  return (
    <form
      className="space-y-4 pt-2"
      onSubmit={(e) => {
        e.preventDefault();
        setMutation.mutate();
      }}
    >
      <div className="space-y-1.5">
        <Label>Username</Label>
        <Input
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          autoComplete="off"
        />
      </div>
      <div className="space-y-1.5">
        <Label>Password</Label>
        <Input
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoComplete="new-password"
        />
      </div>
      <div className="flex items-center justify-between rounded-lg border border-border p-3">
        <div>
          <Label>Mutual CHAP</Label>
          <p className="text-xs text-muted-foreground">
            Require the target to authenticate to the initiator too.
          </p>
        </div>
        <Switch checked={mutual} onCheckedChange={setMutual} />
      </div>
      <Button type="submit" disabled={setMutation.isPending}>
        {setMutation.isPending && (
          <Loader2 className="mr-2 h-4 w-4 animate-spin" />
        )}
        Save CHAP
      </Button>
    </form>
  );
}

// ---------- NVMe Management ----------

function ManageNVMe({ resource }: { resource: string }) {
  return (
    <Tabs defaultValue="namespaces">
      <TabsList className="grid w-full grid-cols-2">
        <TabsTrigger value="namespaces">Namespaces</TabsTrigger>
        <TabsTrigger value="hosts">Allowed Hosts</TabsTrigger>
      </TabsList>
      <TabsContent value="namespaces">
        <NVMeNamespaces resource={resource} />
      </TabsContent>
      <TabsContent value="hosts">
        <NVMeHosts resource={resource} />
      </TabsContent>
    </Tabs>
  );
}

function NVMeNamespaces({ resource }: { resource: string }) {
  const queryClient = useQueryClient();
  const queryKey = ['nvme-namespaces', resource];
  const { data, isLoading, error } = useQuery({
    queryKey,
    queryFn: () => api.listNVMeNamespaces(resource),
    retry: false,
  });

  const [device, setDevice] = useState('');
  const invalidate = () => queryClient.invalidateQueries({ queryKey });

  const addMutation = useMutation({
    mutationFn: () => api.addNVMeNamespace(resource, device),
    onSuccess: () => {
      toast.success('Namespace added');
      setDevice('');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const removeMutation = useMutation({
    mutationFn: (id: number) => api.removeNVMeNamespace(resource, id),
    onSuccess: () => {
      toast.success('Namespace removed');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const namespaces: NVMeNamespace[] = data?.namespaces ?? [];

  return (
    <div className="space-y-4 pt-2">
      {error ? (
        <QueryError message={(error as Error).message} />
      ) : isLoading ? (
        <Skeleton className="h-24 w-full" />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>NSID</TableHead>
              <TableHead>Backing Path</TableHead>
              <TableHead>UUID</TableHead>
              <TableHead className="text-right">Action</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {namespaces.length === 0 ? (
              <EmptyRow colSpan={4} label="No namespaces configured" />
            ) : (
              namespaces.map((ns) => (
                <TableRow key={ns.namespaceId}>
                  <TableCell className="font-mono">{ns.namespaceId}</TableCell>
                  <TableCell>
                    <Mono value={ns.backingPath} className="text-xs" />
                  </TableCell>
                  <TableCell>
                    <Mono
                      value={ns.uuid}
                      className="text-xs text-muted-foreground"
                    />
                  </TableCell>
                  <TableCell className="text-right">
                    <RemoveButton
                      title="Remove namespace?"
                      description={`Remove namespace ${ns.namespaceId}?`}
                      onConfirm={() => removeMutation.mutate(ns.namespaceId)}
                    />
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      )}

      <form
        className="flex items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          addMutation.mutate();
        }}
      >
        <div className="flex-1 space-y-1.5">
          <Label>Device Path</Label>
          <Input
            className="font-mono"
            value={device}
            onChange={(e) => setDevice(e.target.value)}
            placeholder="/dev/drbd1001"
            required
          />
        </div>
        <Button type="submit" disabled={addMutation.isPending || !device}>
          {addMutation.isPending ? (
            <Loader2 className="h-4 w-4 animate-spin" />
          ) : (
            <Plus className="h-4 w-4" />
          )}
        </Button>
      </form>
    </div>
  );
}

function NVMeHosts({ resource }: { resource: string }) {
  const queryClient = useQueryClient();
  const queryKey = ['nvme-hosts', resource];
  const { data, isLoading, error } = useQuery({
    queryKey,
    queryFn: () => api.listNVMeHosts(resource),
    retry: false,
  });

  const [hostNqn, setHostNqn] = useState('');
  const invalidate = () => queryClient.invalidateQueries({ queryKey });

  const addMutation = useMutation({
    mutationFn: () => api.addNVMeHost(resource, hostNqn),
    onSuccess: () => {
      toast.success('Host added');
      setHostNqn('');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const removeMutation = useMutation({
    mutationFn: (nqn: string) => api.removeNVMeHost(resource, nqn),
    onSuccess: () => {
      toast.success('Host removed');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const hosts = data?.hosts ?? [];

  return (
    <div className="space-y-4 pt-2">
      {error ? (
        <QueryError message={(error as Error).message} />
      ) : isLoading ? (
        <Skeleton className="h-12 w-full" />
      ) : hosts.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          No allowed hosts configured.
        </p>
      ) : (
        <div className="flex flex-wrap gap-2">
          {hosts.map((nqn) => (
            <Badge
              key={nqn}
              variant="secondary"
              className="max-w-[320px] gap-1 font-mono"
              title={nqn}
            >
              <span className="truncate">{nqn}</span>
              <button
                type="button"
                aria-label={`Remove host ${nqn}`}
                onClick={() => removeMutation.mutate(nqn)}
                className="ml-1 shrink-0 rounded-full hover:text-destructive"
              >
                <X className="h-3 w-3" />
              </button>
            </Badge>
          ))}
        </div>
      )}

      <form
        className="flex items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          addMutation.mutate();
        }}
      >
        <div className="flex-1 space-y-1.5">
          <Label>Host NQN</Label>
          <Input
            className="font-mono"
            value={hostNqn}
            onChange={(e) => setHostNqn(e.target.value)}
            placeholder="nqn.2014-08.org.nvmexpress:uuid:..."
            required
          />
        </div>
        <Button type="submit" disabled={addMutation.isPending || !hostNqn}>
          {addMutation.isPending ? (
            <Loader2 className="h-4 w-4 animate-spin" />
          ) : (
            <Plus className="h-4 w-4" />
          )}
        </Button>
      </form>
    </div>
  );
}

// ---------- Shared remove button ----------

function RemoveButton({
  title,
  description,
  onConfirm,
}: {
  title: string;
  description: string;
  onConfirm: () => void;
}) {
  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>
        <Button variant="ghost" size="icon-sm" aria-label={title}>
          <Trash2 className="text-destructive" />
        </Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction onClick={onConfirm}>Remove</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
