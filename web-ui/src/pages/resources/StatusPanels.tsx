import { ResourceStatus, QuorumInfo } from '../../services/api';
import { cn } from '@/lib/utils';
import { TONE_SOFT, type StatusTone } from '@/components/status';

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
export function QuorumPanel({ quorum }: { quorum: QuorumInfo }) {
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
export function WANPanel({ status }: { status: ResourceStatus }) {
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

export function formatBytes(n: number): string {
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
