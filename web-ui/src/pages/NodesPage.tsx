import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Server,
  Plus,
  Trash2,
  Loader2,
  Check,
  X,
  ChevronRight,
  ChevronDown,
  MoreHorizontal,
  Copy,
  Activity,
} from 'lucide-react';
import { api, type Node, type HealthInfo } from '@/services/api';
import { toast } from 'sonner';
import { cn, copyToClipboard } from '@/lib/utils';
import { PageHeader } from '@/components/PageHeader';
import { StatusTickCell, StatusTickHead } from '@/components/StatusTick';
import { TONE_BG, toneOf, type StatusTone } from '@/components/status';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
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
} from '@/components/ui/alert-dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Skeleton } from '@/components/ui/skeleton';

/** Body cells span the tick gutter plus six columns; one constant so the
 *  expanded row cannot drift out of the grid when a column is added. */
const COLUMN_COUNT = 7;

function formatLastSeen(lastSeen: string): string {
  const ts = Number(lastSeen);
  if (!ts) return '-';
  return new Date(ts * 1000).toLocaleString();
}

/** "41s ago" scans in a column; a locale timestamp does not. The absolute
 *  value stays reachable as the cell's title and in the expanded facts. */
function formatAge(lastSeen: string): string {
  const ts = Number(lastSeen);
  if (!ts) return '-';
  const secs = Math.floor(Date.now() / 1000 - ts);
  if (secs < 0) return 'now';
  if (secs < 5) return 'now';
  if (secs < 60) return `${secs}s ago`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
  return `${Math.floor(secs / 86400)}d ago`;
}

/**
 * Tone → text colour. `TONE_SOFT` bundles a tinted fill with its readable text
 * colour; the readiness word is bare text on the row, so it needs the text
 * half alone. Literal strings so the Tailwind scanner still sees them.
 */
const TONE_TEXT: Record<StatusTone, string> = {
  ok: 'text-status-ok-text',
  warn: 'text-status-warn-text',
  bad: 'text-status-bad-text',
  idle: 'text-status-idle-text',
};

type Readiness = {
  dots: { label: string; tone: StatusTone }[];
  word: string;
  tone: StatusTone;
};

/**
 * What the promoter's start chain needs, reduced to three dots and one word.
 * The word is not optional: the dots repeat it, they never carry it alone.
 * Ordering is worst-first — a missing DRBD is the answer even if the reactor
 * is also stopped.
 */
function readinessOf(h: HealthInfo): Readiness {
  const dots: Readiness['dots'] = [
    { label: 'DRBD', tone: h.drbdInstalled ? 'ok' : 'bad' },
    {
      label: 'drbd-reactor',
      tone: !h.drbdReactorInstalled ? 'bad' : h.drbdReactorRunning ? 'ok' : 'warn',
    },
    { label: 'resource agents', tone: h.resourceAgentsInstalled ? 'ok' : 'bad' },
  ];
  if (!h.drbdInstalled) return { dots, word: 'DRBD missing', tone: 'bad' };
  if (!h.drbdReactorInstalled) return { dots, word: 'reactor missing', tone: 'bad' };
  if (!h.resourceAgentsInstalled) return { dots, word: 'agents missing', tone: 'bad' };
  if (!h.drbdReactorRunning) return { dots, word: 'reactor stopped', tone: 'warn' };
  return { dots, word: 'ready', tone: 'ok' };
}

/** A checked node's result plus when it was taken — the expanded row states the
 *  time, because a health check is a reading, not a live property. */
type HealthResult = { info: HealthInfo; at: number };

export function NodesPage() {
  const queryClient = useQueryClient();
  const { data: nodes, isLoading } = useQuery({
    queryKey: ['nodes'],
    queryFn: () => api.getNodes(),
  });

  const [registerOpen, setRegisterOpen] = useState(false);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [confirmNode, setConfirmNode] = useState<Node | null>(null);

  // Health is per node, not a single slot: the page can now show every node's
  // reading at once, and "Check all" fires several checks concurrently.
  const [health, setHealth] = useState<Record<string, HealthResult>>({});
  const [healthErrors, setHealthErrors] = useState<Record<string, string>>({});
  const [checking, setChecking] = useState<string[]>([]);

  const healthCheckMutation = useMutation({
    mutationFn: (nodeName: string) => api.healthCheck(nodeName),
    onMutate: (nodeName) => {
      setChecking((c) => (c.includes(nodeName) ? c : [...c, nodeName]));
      setHealthErrors(({ [nodeName]: _dropped, ...rest }) => rest);
    },
    onSuccess: (data, nodeName) => {
      setHealth((h) => ({ ...h, [nodeName]: { info: data.health, at: Date.now() } }));
    },
    onError: (e: Error, nodeName) => {
      toast.error(e.message);
      // The failure used to close the dialog and survive only as a toast. It
      // now stays where the answer was expected, so a dismissed toast is not
      // the only record of it.
      setHealthErrors((x) => ({ ...x, [nodeName]: e.message }));
      setExpanded(nodeName);
    },
    onSettled: (_data, _error, nodeName) => {
      setChecking((c) => c.filter((n) => n !== nodeName));
    },
  });

  const unregisterMutation = useMutation({
    mutationFn: (address: string) => api.unregisterNode(address),
    onSuccess: () => {
      toast.success('Node unregistered');
      queryClient.invalidateQueries({ queryKey: ['nodes'] });
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const list = nodes?.nodes ?? [];
  const online = list.filter((n) => n.state === 'online');
  const checkedTimes = Object.values(health).map((h) => h.at);
  const lastCheck = checkedTimes.length ? Math.max(...checkedTimes) : null;

  const copyAddress = async (address: string) => {
    const ok = await copyToClipboard(address);
    if (ok) toast.success('Address copied');
    else toast.error('Could not copy address');
  };

  // N read-only GETs, one per online node. Offline nodes are excluded for the
  // same reason the per-row button is disabled for them.
  const checkAll = () => online.forEach((n) => healthCheckMutation.mutate(n.name));

  return (
    <div>
      <PageHeader
        title="Nodes"
        description={
          <>
            <span className="font-mono tabular-nums text-foreground">{list.length}</span>{' '}
            registered ·{' '}
            <span className="font-mono tabular-nums text-foreground">{online.length}</span>{' '}
            online
            {lastCheck ? (
              <>
                {' '}
                · last health check{' '}
                <span className="font-mono tabular-nums text-foreground">
                  {new Date(lastCheck).toLocaleTimeString()}
                </span>
              </>
            ) : null}
          </>
        }
        actions={
          <>
            <Button
              variant="outline"
              onClick={checkAll}
              disabled={!online.length || checking.length > 0}
            >
              {checking.length > 0 ? (
                <Loader2 className="animate-spin" />
              ) : (
                <Activity />
              )}
              Check all
            </Button>
            <Button onClick={() => setRegisterOpen(true)}>
              <Plus />
              Register node
            </Button>
          </>
        }
      />

      {isLoading ? (
        <Card>
          <CardContent className="p-0">
            <div className="space-y-3 p-5">
              {Array.from({ length: 4 }).map((_, i) => (
                <Skeleton key={i} className="h-9 w-full" />
              ))}
            </div>
          </CardContent>
        </Card>
      ) : !list.length ? (
        <Card>
          <CardContent className="flex flex-col items-center justify-center gap-3 py-16 text-center">
            <Server className="h-8 w-8 text-muted-foreground" />
            <p className="text-sm text-muted-foreground">
              No nodes registered. Register a storage node to get started.
            </p>
            <Button variant="outline" onClick={() => setRegisterOpen(true)}>
              <Plus />
              Register node
            </Button>
          </CardContent>
        </Card>
      ) : (
        <Card className="overflow-hidden">
          <CardContent className="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <StatusTickHead />
                  <TableHead>Node</TableHead>
                  <TableHead>Address</TableHead>
                  <TableHead>System</TableHead>
                  <TableHead>Readiness</TableHead>
                  <TableHead>Last seen</TableHead>
                  <TableHead className="pr-5 text-right">
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {list.map((node) => {
                  const isOnline = node.state === 'online';
                  const isChecking = checking.includes(node.name);
                  const isDeleting =
                    unregisterMutation.isPending &&
                    unregisterMutation.variables === node.address;
                  const result = health[node.name];
                  const readiness = result ? readinessOf(result.info) : null;
                  const isOpen = expanded === node.name;
                  const panelId = `node-detail-${node.name}`;

                  return (
                    <NodeRows
                      key={node.name}
                      node={node}
                      isOnline={isOnline}
                      isChecking={isChecking}
                      isDeleting={isDeleting}
                      isOpen={isOpen}
                      panelId={panelId}
                      result={result}
                      readiness={readiness}
                      error={healthErrors[node.name]}
                      onToggle={() => setExpanded(isOpen ? null : node.name)}
                      onCheck={() => healthCheckMutation.mutate(node.name)}
                      onCopyAddress={() => copyAddress(node.address)}
                      onUnregister={() => setConfirmNode(node)}
                    />
                  );
                })}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}

      <RegisterNodeDialog open={registerOpen} onOpenChange={setRegisterOpen} />

      {/* One dialog for the page, opened by whichever row's menu asked: a
          confirmation nested inside a menu unmounts with the menu. */}
      <AlertDialog
        open={!!confirmNode}
        onOpenChange={(open) => !open && setConfirmNode(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Unregister node "{confirmNode?.name}"?</AlertDialogTitle>
            <AlertDialogDescription>
              This removes the node from the controller's inventory. Resources and pools
              hosted on this node will no longer be managed. This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                if (confirmNode) unregisterMutation.mutate(confirmNode.address);
              }}
            >
              Unregister
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

interface NodeRowsProps {
  node: Node;
  isOnline: boolean;
  isChecking: boolean;
  isDeleting: boolean;
  isOpen: boolean;
  panelId: string;
  result?: HealthResult;
  readiness: Readiness | null;
  error?: string;
  onToggle: () => void;
  onCheck: () => void;
  onCopyAddress: () => void;
  onUnregister: () => void;
}

/** The summary row and, when open, the detail row beneath it — a fragment
 *  rather than a component boundary, because both are `<tr>`s of one table. */
function NodeRows({
  node,
  isOnline,
  isChecking,
  isDeleting,
  isOpen,
  panelId,
  result,
  readiness,
  error,
  onToggle,
  onCheck,
  onCopyAddress,
  onUnregister,
}: NodeRowsProps) {
  const Chevron = isOpen ? ChevronDown : ChevronRight;

  return (
    <>
      <TableRow className={cn(isOpen && 'border-b-transparent bg-muted/40')}>
        <StatusTickCell status={node.state} />
        <TableCell>
          {/* The expand affordance is the name itself, so the target is the
              width of the cell and still a real button for the keyboard. */}
          <button
            type="button"
            onClick={onToggle}
            aria-expanded={isOpen}
            aria-controls={panelId}
            className="flex items-center gap-2.5 rounded-md text-left outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
          >
            <Chevron
              className={cn('size-3.5', isOpen ? 'text-foreground' : 'text-muted-foreground')}
            />
            <span className="flex items-baseline gap-[7px]">
              <span className="font-mono text-[14px] font-semibold">{node.name}</span>
              <span className="text-[11.5px] text-muted-foreground">{node.hostname}</span>
            </span>
          </button>
        </TableCell>
        <TableCell className="font-mono tabular-nums text-muted-foreground">
          {node.address}
        </TableCell>
        <TableCell className="font-mono tabular-nums text-muted-foreground">
          {node.version || '-'}
        </TableCell>
        <TableCell>
          {readiness ? (
            <span className="flex items-center gap-1.5">
              {readiness.dots.map((dot) => (
                <span
                  key={dot.label}
                  title={dot.label}
                  className={cn('size-[7px] rounded-full', TONE_BG[dot.tone])}
                />
              ))}
              <span className={cn('ml-1 text-[12.5px]', TONE_TEXT[readiness.tone])}>
                {readiness.word}
              </span>
            </span>
          ) : (
            <span className="text-[12.5px] text-muted-foreground">not checked</span>
          )}
        </TableCell>
        <TableCell
          className="font-mono tabular-nums text-muted-foreground"
          title={formatLastSeen(node.lastSeen)}
        >
          {formatAge(node.lastSeen)}
        </TableCell>
        <TableCell className="pr-5 text-right">
          <span className="inline-flex items-center gap-1.5">
            <Button
              variant="outline"
              size="sm"
              disabled={isChecking || !isOnline}
              onClick={onCheck}
            >
              {isChecking ? <Loader2 className="animate-spin" /> : null}
              Check health
            </Button>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  disabled={isDeleting}
                  aria-label={`Actions for ${node.name}`}
                >
                  {isDeleting ? (
                    <Loader2 className="animate-spin" />
                  ) : (
                    <MoreHorizontal />
                  )}
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-[220px]">
                <DropdownMenuItem onSelect={onCopyAddress}>
                  <Copy />
                  Copy address
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem variant="destructive" onSelect={onUnregister}>
                  <Trash2 />
                  Unregister node…
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </span>
        </TableCell>
      </TableRow>

      {isOpen ? (
        <TableRow className="hover:bg-transparent">
          <TableCell colSpan={COLUMN_COUNT} id={panelId} className="h-auto bg-muted/40 p-0">
            <div className="flex flex-col gap-4 py-1 pr-6 pb-[22px] pl-[46px]">
              <div className="flex items-center justify-between gap-4">
                <div className="eyebrow">
                  Health check
                  {result ? (
                    <>
                      {' · '}
                      <span className="font-mono text-[11px] tracking-normal text-foreground normal-case tabular-nums">
                        {new Date(result.at).toLocaleTimeString()}
                      </span>
                    </>
                  ) : null}
                </div>
                <div className="text-[12px] text-muted-foreground">
                  what the promoter's start chain needs on this node
                </div>
              </div>

              {error ? (
                <p className="text-[12.5px] text-status-bad-text">
                  Health check failed: {error}
                </p>
              ) : null}

              {result ? (
                <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
                  <HealthTile label="DRBD" ok={result.info.drbdInstalled}>
                    {result.info.drbdInstalled
                      ? result.info.drbdVersion || 'installed'
                      : 'not installed'}
                  </HealthTile>
                  <HealthTile label="drbd-reactor" ok={result.info.drbdReactorInstalled}>
                    {result.info.drbdReactorInstalled
                      ? `${result.info.drbdReactorVersion || 'installed'} · ${
                          result.info.drbdReactorRunning ? 'running' : 'stopped'
                        }`
                      : 'not installed'}
                  </HealthTile>
                  <HealthTile
                    label="Resource agents"
                    ok={result.info.resourceAgentsInstalled}
                  >
                    <AgentChips agents={result.info.availableAgents} />
                  </HealthTile>
                </div>
              ) : (
                <div className="flex items-center gap-3">
                  <p className="text-[12.5px] text-muted-foreground">
                    {isOnline
                      ? 'No health check has run for this node yet.'
                      : 'Node is offline; a health check needs a reachable node.'}
                  </p>
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={isChecking || !isOnline}
                    onClick={onCheck}
                  >
                    {isChecking ? <Loader2 className="animate-spin" /> : null}
                    Check health
                  </Button>
                </div>
              )}

              <div className="grid grid-cols-2 gap-3 border-t border-border/70 pt-3.5 md:grid-cols-5">
                <Fact label="Hostname" value={node.hostname || '-'} />
                <Fact label="Address" value={node.address} />
                <Fact label="State" value={node.state} tone={toneOf(node.state)} />
                <Fact label="Version" value={node.version || '-'} />
                <Fact label="Last seen" value={formatLastSeen(node.lastSeen)} />
              </div>
            </div>
          </TableCell>
        </TableRow>
      ) : null}
    </>
  );
}

/** One prerequisite: whether it is there, and what version answered. The tick
 *  and the cross are shapes, not two shades of the same dot. */
function HealthTile({
  label,
  ok,
  children,
}: {
  label: string;
  ok: boolean;
  children: React.ReactNode;
}) {
  return (
    <div className="rounded-lg border border-border bg-card px-3.5 py-3">
      <div className="flex items-center gap-2 text-[12.5px] font-medium">
        {ok ? (
          <Check className="size-[15px] text-status-ok" />
        ) : (
          <X className="size-[15px] text-status-bad" />
        )}
        {label}
        <span className="sr-only">{ok ? ' installed' : ' not installed'}</span>
      </div>
      <div className="mt-1.5 font-mono text-[11.5px] tabular-nums text-muted-foreground">
        {children}
      </div>
    </div>
  );
}

/** Five agent names is enough to recognise a working node; the rest are a
 *  count, with the full list on the overflow chip's title. */
function AgentChips({ agents }: { agents: string[] }) {
  const all = agents ?? [];
  if (!all.length) return <>no OCF agents detected</>;
  const shown = all.slice(0, 5);
  const rest = all.slice(5);
  return (
    <span className="flex flex-wrap items-center gap-1.5">
      {shown.map((agent) => (
        <span
          key={agent}
          className="rounded border border-border bg-secondary px-1.5 py-0.5 text-[11px] text-foreground"
        >
          {agent}
        </span>
      ))}
      {rest.length ? (
        <span className="px-1 py-0.5 text-[11px]" title={rest.join(', ')}>
          +{rest.length}
        </span>
      ) : null}
    </span>
  );
}

function Fact({
  label,
  value,
  tone,
}: {
  label: string;
  value: string;
  tone?: StatusTone;
}) {
  return (
    <div className="min-w-0">
      <div className="eyebrow">{label}</div>
      <div className="mt-1.5 flex items-center gap-1.5 truncate font-mono text-[13px] tabular-nums">
        {tone ? <span className={cn('size-1.5 rounded-full', TONE_BG[tone])} /> : null}
        {value}
      </div>
    </div>
  );
}

interface RegisterNodeDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

function RegisterNodeDialog({ open, onOpenChange }: RegisterNodeDialogProps) {
  const queryClient = useQueryClient();
  const [name, setName] = useState('');
  const [address, setAddress] = useState('');

  const registerMutation = useMutation({
    mutationFn: (data: { name: string; address: string }) => api.registerNode(data),
    onSuccess: () => {
      toast.success('Node registered');
      queryClient.invalidateQueries({ queryKey: ['nodes'] });
      setName('');
      setAddress('');
      onOpenChange(false);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    registerMutation.mutate({ name: name.trim(), address: address.trim() });
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>Register Node</DialogTitle>
            <DialogDescription>
              Add a storage node to the controller inventory.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label htmlFor="node-name">Node Name</Label>
              <Input
                id="node-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="e.g., orange1"
                required
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="node-address">Node Address</Label>
              <Input
                id="node-address"
                value={address}
                onChange={(e) => setAddress(e.target.value)}
                placeholder="e.g., 192.168.1.100 or hostname"
                required
              />
            </div>
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={registerMutation.isPending}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={registerMutation.isPending}>
              {registerMutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
              Register
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
