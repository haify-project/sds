import { type ReactNode } from 'react';
import { NFSExport, ISCSILUN, NVMeNamespace } from '@/services/api';
import { Plus } from 'lucide-react';
import { TONE_BG } from '@/components/status';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Skeleton } from '@/components/ui/skeleton';
import { type GwKind, isRunning, gatewayTone, plural } from './protocol';
import { type GatewayDetail } from './useGatewayDetail';
import { QueryError } from './ManageControls';

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
export function Mono({ value, className }: { value: string; className?: string }) {
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

export function NFSDetailPanel({
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

export function ISCSIDetailPanel({
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

export function NVMeDetailPanel({
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
export function StartChainPanel({
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
