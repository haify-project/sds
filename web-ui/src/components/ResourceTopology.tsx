import { Resource, ResourceStatus, NodeResourceState } from '../services/api';

// ResourceTopology draws where a resource's copies actually live and how they
// are wired.
//
// A flat list of node badges cannot express the thing that matters about a
// two-site resource: that one copy is in another building or another city, that
// the link to it is asynchronous, and that a failure there is not the same event
// as a failure here. Those distinctions decide what an operator does next, and
// they are geometry — two boxes and two kinds of line say it faster than any
// amount of prose.
//
// Solid lines are the synchronous mesh: a write is not acknowledged until every
// one of those peers has it. Dashed lines are WAN legs under protocol A, where
// the write is acknowledged first and shipped after — which is why the DR can
// lag and why it is drawn as a separate place rather than another peer.

type NodeKind = 'replica' | 'tiebreaker' | 'client' | 'dr';

interface PlacedNode {
  name: string;
  kind: NodeKind;
  state?: NodeResourceState;
}

const NODE_W = 208;
const NODE_H = 52;
const NODE_GAP = 12;
const SITE_PAD = 14;
const SITE_HEADER = 26;
const COL_GAP = 116;
const LEGEND_H = 44;
const MARGIN = 10;

export function ResourceTopology({
  resource,
  status,
}: {
  resource: Resource;
  status: ResourceStatus;
}) {
  // The state map is keyed by DRBD host name; each entry carries the SDS node
  // name so it can be paired back to the node list.
  const stateByNode = new Map<string, NodeResourceState>();
  for (const [host, st] of Object.entries(status.nodeStates ?? {})) {
    stateByNode.set(st.node || host, st);
  }

  const drNode = status.wan ? status.drNode : undefined;
  const tiebreakers = new Set(resource.disklessNodes ?? []);
  const clients = new Set(resource.disklessClients ?? []);

  const classify = (name: string): NodeKind => {
    if (name === drNode) return 'dr';
    if (tiebreakers.has(name)) return 'tiebreaker';
    if (clients.has(name)) return 'client';
    return 'replica';
  };

  const all = [
    ...(status.nodes ?? resource.nodes ?? []),
    ...(resource.disklessNodes ?? []),
    ...(resource.disklessClients ?? []),
  ];
  const seen = new Set<string>();
  const nodes: PlacedNode[] = [];
  for (const name of all) {
    if (seen.has(name)) continue;
    seen.add(name);
    nodes.push({ name, kind: classify(name), state: stateByNode.get(name) });
  }

  const local = nodes.filter((n) => n.kind !== 'dr');
  const remote = nodes.filter((n) => n.kind === 'dr');

  // A leg is only usable when the proxy is running at both ends. Reporting the
  // primary's end alone would call a tunnel healthy while the far side is dead.
  const legState = (primary: string): 'active' | 'down' => {
    const proxy = status.wanProxy ?? {};
    const near = proxy[primary];
    const far =
      remote.length > 0
        ? (proxy[`${remote[0].name} (leg ${primary})`] ?? proxy[remote[0].name])
        : undefined;
    return near === 'active' && (far === undefined || far === 'active')
      ? 'active'
      : 'down';
  };

  const siteH = (count: number) =>
    SITE_HEADER + SITE_PAD * 2 + count * NODE_H + Math.max(0, count - 1) * NODE_GAP;

  const localH = siteH(local.length);
  const remoteH = remote.length > 0 ? siteH(remote.length) : 0;
  const bodyH = Math.max(localH, remoteH);
  const width =
    MARGIN * 2 +
    NODE_W +
    SITE_PAD * 2 +
    (remote.length > 0 ? COL_GAP + NODE_W + SITE_PAD * 2 : 0);
  const height = MARGIN * 2 + bodyH + LEGEND_H;

  const localX = MARGIN;
  const remoteX = MARGIN + NODE_W + SITE_PAD * 2 + COL_GAP;
  // Centre the shorter site against the taller one so the WAN legs read as
  // horizontal links rather than a lopsided fan.
  const localY = MARGIN + (bodyH - localH) / 2;
  const remoteY = MARGIN + (bodyH - remoteH) / 2;

  const nodeY = (siteTop: number, i: number) =>
    siteTop + SITE_HEADER + SITE_PAD + i * (NODE_H + NODE_GAP);

  return (
    <div className="rounded-lg border p-3">
      <h4 className="mb-2 text-sm font-medium">Topology</h4>
      <svg
        viewBox={`0 0 ${width} ${height}`}
        className="w-full"
        style={{ maxHeight: height * 1.2 }}
        role="img"
        aria-label={`Replication topology for ${resource.name}`}
      >
        {/* Site frames first so every line and node draws over them. */}
        <SiteFrame
          x={localX}
          y={localY}
          w={NODE_W + SITE_PAD * 2}
          h={localH}
          label={remote.length > 0 ? 'Primary site' : 'Cluster'}
          sub={remote.length > 0 ? `protocol ${resource.protocol} · synchronous` : undefined}
        />
        {remote.length > 0 && (
          <SiteFrame
            x={remoteX}
            y={remoteY}
            w={NODE_W + SITE_PAD * 2}
            h={remoteH}
            label="DR site"
            sub={status.drEndpoint}
            accent
          />
        )}

        {/* The synchronous mesh, drawn as a spine down the left edge of the
            primary site: with three or more peers an all-pairs rendering is
            noise, and every one of those pairs behaves identically anyway. */}
        {local.length > 1 && (
          <line
            x1={localX + SITE_PAD - 6}
            y1={nodeY(localY, 0) + NODE_H / 2}
            x2={localX + SITE_PAD - 6}
            y2={nodeY(localY, local.length - 1) + NODE_H / 2}
            stroke="currentColor"
            className="text-emerald-500"
            strokeWidth={2}
          />
        )}
        {local.length > 1 &&
          local.map((n, i) => (
            <line
              key={`spine-${n.name}`}
              x1={localX + SITE_PAD - 6}
              y1={nodeY(localY, i) + NODE_H / 2}
              x2={localX + SITE_PAD}
              y2={nodeY(localY, i) + NODE_H / 2}
              stroke="currentColor"
              className="text-emerald-500"
              strokeWidth={2}
            />
          ))}

        {/* One dashed WAN leg per primary-site replica. DRBD 9 is a full mesh,
            so whichever replica is Primary after a local failover needs its own
            path to the DR — drawing a single line to "the site" would hide that
            a leg can be down while the others are fine. */}
        {remote.length > 0 &&
          local
            .filter((n) => n.kind === 'replica')
            .map((n) => {
              const i = local.indexOf(n);
              const s = legState(n.name);
              const x1 = localX + SITE_PAD + NODE_W;
              const y1 = nodeY(localY, i) + NODE_H / 2;
              const x2 = remoteX + SITE_PAD;
              const y2 = nodeY(remoteY, 0) + NODE_H / 2;
              const mid = (x1 + x2) / 2;
              return (
                <path
                  key={`leg-${n.name}`}
                  d={`M ${x1} ${y1} C ${mid} ${y1}, ${mid} ${y2}, ${x2} ${y2}`}
                  fill="none"
                  stroke="currentColor"
                  className={s === 'active' ? 'text-sky-500' : 'text-destructive'}
                  strokeWidth={2}
                  strokeDasharray="6 4"
                />
              );
            })}

        {local.map((n, i) => (
          <NodeCard
            key={n.name}
            x={localX + SITE_PAD}
            y={nodeY(localY, i)}
            node={n}
          />
        ))}
        {remote.map((n, i) => (
          <NodeCard
            key={n.name}
            x={remoteX + SITE_PAD}
            y={nodeY(remoteY, i)}
            node={n}
          />
        ))}

        <Legend y={height - LEGEND_H + 12} hasWAN={remote.length > 0} />
      </svg>
    </div>
  );
}

function SiteFrame({
  x,
  y,
  w,
  h,
  label,
  sub,
  accent,
}: {
  x: number;
  y: number;
  w: number;
  h: number;
  label: string;
  sub?: string;
  accent?: boolean;
}) {
  return (
    <g>
      <rect
        x={x}
        y={y}
        width={w}
        height={h}
        rx={10}
        fill="none"
        stroke="currentColor"
        className={accent ? 'text-sky-500/40' : 'text-muted-foreground/30'}
        strokeWidth={1.5}
        strokeDasharray={accent ? '5 3' : undefined}
      />
      <text
        x={x + 10}
        y={y + 16}
        className="fill-current text-[11px] font-medium text-foreground"
      >
        {label}
      </text>
      {sub && (
        <text
          x={x + w - 10}
          y={y + 16}
          textAnchor="end"
          className="fill-current text-[10px] text-muted-foreground"
        >
          {sub}
        </text>
      )}
    </g>
  );
}

function NodeCard({ x, y, node }: { x: number; y: number; node: PlacedNode }) {
  const { name, kind, state } = node;
  const isPrimary = state?.role === 'Primary';
  const healthy = state?.diskState === 'UpToDate' || kind === 'tiebreaker';

  const tag =
    kind === 'dr'
      ? 'DR'
      : kind === 'tiebreaker'
        ? 'tiebreaker'
        : kind === 'client'
          ? 'client'
          : undefined;

  return (
    <g>
      <rect
        x={x}
        y={y}
        width={NODE_W}
        height={NODE_H}
        rx={8}
        className={
          isPrimary
            ? 'fill-amber-400/10 stroke-amber-500'
            : 'fill-muted/40 stroke-border'
        }
        strokeWidth={isPrimary ? 2 : 1}
      />
      {/* A Primary is the node actually serving; it is what an operator looks
          for first, so it gets the only filled marker in the drawing. */}
      {isPrimary && (
        <circle cx={x + 12} cy={y + 16} r={4} className="fill-amber-500" />
      )}
      <text
        x={x + (isPrimary ? 22 : 12)}
        y={y + 20}
        className="fill-current text-[12px] font-medium text-foreground"
      >
        {name}
      </text>
      {tag && (
        <text
          x={x + NODE_W - 10}
          y={y + 20}
          textAnchor="end"
          className={`fill-current text-[10px] ${
            kind === 'dr' ? 'text-sky-600' : 'text-muted-foreground'
          }`}
        >
          {tag}
        </text>
      )}
      <text
        x={x + 12}
        y={y + 38}
        className={`fill-current text-[10px] ${
          healthy ? 'text-muted-foreground' : 'text-destructive'
        }`}
      >
        {state
          ? `${state.role || '—'} · ${state.diskState || '—'}`
          : 'state unknown'}
      </text>
      {state?.replicationState &&
        state.replicationState !== 'Established' &&
        state.replicationState !== '' && (
          <text
            x={x + NODE_W - 10}
            y={y + 38}
            textAnchor="end"
            className="fill-current text-[10px] text-amber-600"
          >
            {state.replicationState}
            {state.syncPercent !== undefined &&
            state.syncPercent > 0 &&
            state.syncPercent < 100
              ? ` ${state.syncPercent.toFixed(0)}%`
              : ''}
          </text>
        )}
    </g>
  );
}

function Legend({ y, hasWAN }: { y: number; hasWAN: boolean }) {
  return (
    <g>
      <line
        x1={MARGIN}
        y1={y}
        x2={MARGIN + 22}
        y2={y}
        stroke="currentColor"
        className="text-emerald-500"
        strokeWidth={2}
      />
      <text
        x={MARGIN + 28}
        y={y + 4}
        className="fill-current text-[10px] text-muted-foreground"
      >
        synchronous — write acknowledged only once every peer has it
      </text>
      {hasWAN && (
        <>
          <line
            x1={MARGIN}
            y1={y + 14}
            x2={MARGIN + 22}
            y2={y + 14}
            stroke="currentColor"
            className="text-sky-500"
            strokeWidth={2}
            strokeDasharray="6 4"
          />
          <text
            x={MARGIN + 28}
            y={y + 18}
            className="fill-current text-[10px] text-muted-foreground"
          >
            asynchronous WAN leg — acknowledged first, shipped after, so it can lag
          </text>
        </>
      )}
    </g>
  );
}
