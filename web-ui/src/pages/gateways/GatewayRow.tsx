import { Gateway } from '@/services/api';
import { Loader2, ChevronDown, ChevronRight, MoreHorizontal } from 'lucide-react';
import { StatusTickCell } from '@/components/StatusTick';
import { Button } from '@/components/ui/button';
import { TableCell, TableRow } from '@/components/ui/table';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import {
  type GwKind,
  PROTOCOL,
  gwKind,
  isRunning,
  gatewayTone,
  plural,
} from './protocol';
import { useGatewayDetail, type GatewayDetail } from './useGatewayDetail';
import {
  NFSDetailPanel,
  ISCSIDetailPanel,
  NVMeDetailPanel,
  StartChainPanel,
} from './GatewayDetailPanels';

export function GatewayRow({
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
