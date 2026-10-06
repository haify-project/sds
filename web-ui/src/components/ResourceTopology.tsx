import { Resource, ResourceStatus, NodeResourceState } from '../services/api';
import { toneOf, type StatusTone } from '@/components/status';

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
// Solid accent lines are the synchronous mesh: a write is not acknowledged until
// every one of those peers has it. Dashed neutral lines are WAN legs under
// protocol A, where the write is acknowledged first and shipped after — which is
// why the DR can lag and why it is drawn as a separate place rather than another
// peer.
//
// The component draws the diagram and nothing else: no border, no padding, no
// heading. The card it sits in owns those, and a bordered box inside a card is
// two frames for one thing.

export type NodeKind = 'replica' | 'tiebreaker' | 'client' | 'dr';

export interface PlacedNode {
  name: string;
  kind: NodeKind;
  state?: NodeResourceState;
}

/**
 * Tone → the SVG paint for it. `TONE_BG` is a Tailwind *background* class and a
 * `class` on a `<rect>` sets no fill, so the diagram needs the tokens
 * themselves. Same four names, same four variables — one vocabulary with the
 * tables' StatusTick, just spelled for an attribute.
 */
const TONE_FILL: Record<StatusTone, string> = {
  ok: 'var(--status-ok)',
  warn: 'var(--status-warn)',
  bad: 'var(--status-bad)',
  idle: 'var(--status-idle)',
};

const NODE_W = 224;
const NODE_H = 84;
const NODE_GAP = 14;
const SITE_PAD = 22;
const SITE_HEADER = 34;
const COL_GAP = 168;
const MARGIN = 12;

// SVG text scales with the viewBox, so an unbounded `w-full` turns a narrow
// single-site drawing into billboard type inside a wide card. 1.6× of the
// natural geometry is as far as it may grow before the labels stop reading as
// labels.
const MAX_UPSCALE = 1.6;

/**
 * Where a resource's copies live and what each one is: shared by the 2D
 * diagram and the 3D one, so the two cannot classify a node differently.
 */
export function placeNodes(resource: Resource, status: ResourceStatus) {
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

  // Where the replica's bytes actually sit. The volume list is per-resource, so
  // it is the same sentence on every replica — which is the point: a node whose
  // box omits it has no local copy at all.
  const vols = resource.volumes ?? [];
  const pool =
    vols.length > 0 && vols.every((v) => v.pool && v.pool === vols[0].pool)
      ? vols[0].pool
      : undefined;
  // sizeGb is a proto int64, i.e. a JSON string: summed as-is it concatenates
  // ("0" + "2" read as "02 GB").
  const totalGb = vols.reduce((sum, v) => sum + (Number(v.sizeGb) || 0), 0);
  const backing = [pool, totalGb > 0 ? `${totalGb} GB` : undefined]
    .filter(Boolean)
    .join(' · ');

  // Two independent facts about a WAN leg, and they can disagree.
  //
  // `replicating` is DRBD's own verdict on the DR peer — Established means bytes
  // are crossing, whatever anything else claims. `proxy` is the sds-proxy
  // process probe at both ends; reporting the near end alone would call a tunnel
  // healthy while the far side is dead.
  //
  // Observed on the live cluster: every proxy leg reads "inactive" while the DR
  // peer is Established and UpToDate at 100%. So the probe is not a proxy for
  // link health, and drawing a red broken line over a link that is demonstrably
  // carrying replication would be the diagram asserting something the data does
  // not say. Replication decides how the leg is drawn; the probe is an
  // annotation beside it.
  const legState = (primary: string) => {
    const proxy = status.wanProxy ?? {};
    const near = proxy[primary];
    const far =
      remote.length > 0
        ? (proxy[`${remote[0].name} (leg ${primary})`] ?? proxy[remote[0].name])
        : undefined;
    const probed = near !== undefined || far !== undefined;
    const proxyUp = near === 'active' && (far === undefined || far === 'active');
    const drState = remote[0]?.state?.replicationState ?? '';
    return {
      replicating: /^(established|syncsource|synctarget|verif|pausedsync)/i.test(drState),
      drState,
      proxy: !probed ? ('unknown' as const) : proxyUp ? ('active' as const) : ('inactive' as const),
    };
  };

  return { local, remote, backing, legState };
}

export function ResourceTopology({
  resource,
  status,
}: {
  resource: Resource;
  status: ResourceStatus;
}) {
  const { local, remote, backing, legState } = placeNodes(resource, status);

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
  const height = MARGIN * 2 + bodyH;

  const localX = MARGIN;
  const remoteX = MARGIN + NODE_W + SITE_PAD * 2 + COL_GAP;
  // Centre the shorter site against the taller one so the WAN legs read as
  // horizontal links rather than a lopsided fan.
  const localY = MARGIN + (bodyH - localH) / 2;
  const remoteY = MARGIN + (bodyH - remoteH) / 2;

  const nodeY = (siteTop: number, i: number) =>
    siteTop + SITE_HEADER + SITE_PAD + i * (NODE_H + NODE_GAP);

  const spineX = localX + SITE_PAD - 11;

  return (
    <div>
      <Legend hasWAN={remote.length > 0} protocol={resource.protocol} />
      <div className="mx-auto w-full" style={{ maxWidth: width * MAX_UPSCALE }}>
        <svg
          viewBox={`0 0 ${width} ${height}`}
          className="w-full"
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
            sub={`tcp ${resource.port} · protocol ${resource.protocol}`}
          />
          {remote.length > 0 && (
            <SiteFrame
              x={remoteX}
              y={remoteY}
              w={NODE_W + SITE_PAD * 2}
              h={remoteH}
              label="DR site"
              sub={status.drEndpoint}
              dashed
            />
          )}

          {/* The synchronous mesh, drawn as a spine down the left edge of the
              primary site: with three or more peers an all-pairs rendering is
              noise, and every one of those pairs behaves identically anyway. */}
          {local.length > 1 && (
            <line
              x1={spineX}
              y1={nodeY(localY, 0) + NODE_H / 2}
              x2={spineX}
              y2={nodeY(localY, local.length - 1) + NODE_H / 2}
              stroke="var(--primary)"
              strokeWidth={2}
            />
          )}
          {local.length > 1 &&
            local.map((n, i) => (
              <line
                key={`spine-${n.name}`}
                x1={spineX}
                y1={nodeY(localY, i) + NODE_H / 2}
                x2={localX + SITE_PAD}
                y2={nodeY(localY, i) + NODE_H / 2}
                stroke="var(--primary)"
                strokeWidth={2}
              />
            ))}

          {/* One dashed WAN leg per primary-site replica. DRBD 9 is a full mesh,
              so whichever replica is Primary after a local failover needs its own
              path to the DR — drawing a single line to "the site" would hide that
              a leg can be down while the others are fine. */}
          {remote.length > 0 &&
            (() => {
              const legs = local.filter((m) => m.kind === 'replica');
              // Every leg lands on the same DR box, so three labels saying the
              // same sentence collide and say it three times. When the legs
              // agree — the normal case — the sentence is stated once, on the
              // middle one; only a leg that differs gets its own.
              const label = (m: PlacedNode) => {
                const st = legState(m.name);
                const line = st.replicating
                  ? status.wanPort
                    ? `tcp ${status.wanPort} · protocol A`
                    : 'protocol A'
                  : st.drState || 'not replicating';
                const caveat =
                  st.replicating && st.proxy === 'inactive' ? 'proxy probe: inactive' : '';
                return { st, line, caveat, key: `${line}|${caveat}` };
              };
              const labels = legs.map(label);
              const uniform = labels.every((l) => l.key === labels[0].key);
              const spokesman = legs[Math.floor((legs.length - 1) / 2)]?.name;
              return legs.map((n) => {
                const i = local.indexOf(n);
                const s = legState(n.name);
                const lab = label(n);
                const showLabel = !uniform || n.name === spokesman;
                const x1 = localX + SITE_PAD + NODE_W;
                const y1 = nodeY(localY, i) + NODE_H / 2;
                const x2 = remoteX + SITE_PAD;
                const y2 = nodeY(remoteY, 0) + NODE_H / 2;
                const mid = (x1 + x2) / 2;
                // Every leg converges on the same DR box, so labels parked at
                // each curve's midpoint pile up on each other. Slide each one
                // along its own curve instead — a point on the cubic at t,
                // staggered per leg.
                const t =
                  uniform || legs.length < 2
                    ? 0.5
                    : 0.3 + (0.4 * legs.indexOf(n)) / (legs.length - 1);
                const u = 1 - t;
                const lx = u * u * u * x1 + 3 * u * u * t * mid + 3 * u * t * t * mid + t * t * t * x2;
                const ly = y1 * (u * u * u + 3 * u * u * t) + y2 * (3 * u * t * t + t * t * t);
                return (
                  <g key={`leg-${n.name}`}>
                    <path
                      d={`M ${x1} ${y1} C ${mid} ${y1}, ${mid} ${y2}, ${x2} ${y2}`}
                      fill="none"
                      stroke={
                        s.replicating ? 'var(--muted-foreground)' : 'var(--status-bad)'
                      }
                      strokeWidth={2}
                      strokeDasharray="6 5"
                    />
                    {/* Never colour alone: the line says how it is drawn, the
                        label says why. */}
                    {showLabel && (
                      <text
                        x={lx}
                        y={ly - 8}
                        textAnchor="middle"
                        className="font-mono text-[11px]"
                        fill={s.replicating ? 'var(--muted-foreground)' : 'var(--status-bad)'}
                        stroke="var(--card)"
                        strokeWidth={4}
                        paintOrder="stroke"
                      >
                        {lab.line}
                        {uniform && legs.length > 1 ? ` · ${legs.length} legs` : ''}
                      </text>
                    )}
                    {/* The probe disagreeing with DRBD is worth saying out loud
                        — but as a caveat under a working link, not as its state. */}
                    {showLabel && lab.caveat && (
                      <text
                        x={lx}
                        y={ly + 6}
                        textAnchor="middle"
                        className="font-mono text-[11px]"
                        fill="var(--status-warn-text)"
                        stroke="var(--card)"
                        strokeWidth={4}
                        paintOrder="stroke"
                      >
                        {lab.caveat}
                      </text>
                    )}
                  </g>
                );
              });
            })()}

          {local.map((n, i) => (
            <NodeCard
              key={n.name}
              x={localX + SITE_PAD}
              y={nodeY(localY, i)}
              node={n}
              backing={backing}
            />
          ))}
          {remote.map((n, i) => (
            <NodeCard
              key={n.name}
              x={remoteX + SITE_PAD}
              y={nodeY(remoteY, i)}
              node={n}
              backing={backing}
            />
          ))}
        </svg>
      </div>
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
  dashed,
}: {
  x: number;
  y: number;
  w: number;
  h: number;
  label: string;
  sub?: string;
  dashed?: boolean;
}) {
  return (
    <g>
      <rect
        x={x}
        y={y}
        width={w}
        height={h}
        rx={12}
        fill="var(--muted)"
        stroke="var(--border)"
        strokeWidth={1}
        // The DR frame is dashed for the same reason its legs are: what is
        // inside it is a copy that lags.
        strokeDasharray={dashed ? '5 4' : undefined}
      />
      <text
        x={x + 18}
        y={y + 22}
        className="font-sans text-[11px] font-medium tracking-[0.07em] uppercase"
        fill="var(--muted-foreground)"
      >
        {label}
      </text>
      {sub && (
        <text
          x={x + w - 18}
          y={y + 22}
          textAnchor="end"
          className="font-mono text-[11px]"
          fill="var(--muted-foreground)"
        >
          {sub}
        </text>
      )}
    </g>
  );
}

/** Rough advance width for the chip's 11px sans label — SVG cannot measure. */
const chipW = (label: string) => label.length * 6.4 + 16;

/**
 * The rail repeats the disk state the box already writes out. A tiebreaker or
 * a diskless client has no disk *on purpose*, so reading its "Diskless" state
 * through `toneOf` would paint every healthy quorum vote red; for those the
 * link is the health.
 */
export function railTone(node: PlacedNode): StatusTone {
  const st = node.state;
  if (!st) return 'idle';
  if (node.kind === 'tiebreaker' || node.kind === 'client') {
    const r = st.replicationState;
    return !r || r === 'Established' ? 'ok' : 'warn';
  }
  return toneOf(st.diskState);
}

function NodeCard({
  x,
  y,
  node,
  backing,
}: {
  x: number;
  y: number;
  node: PlacedNode;
  backing: string;
}) {
  const { name, kind, state } = node;
  const diskless = kind === 'tiebreaker' || kind === 'client';
  const isPrimary = !diskless && state?.role === 'Primary';

  // Diskless nodes are their own kind of thing, and they say so in a word:
  // colour alone would leave "why is this box different" unanswerable.
  const roleLabel = diskless ? 'Diskless' : state?.role || 'Role unknown';
  const qualifier =
    kind === 'tiebreaker'
      ? 'quorum vote only'
      : kind === 'client'
        ? 'client mount'
        : kind === 'dr'
          ? 'off-site'
          : undefined;

  // Facts line: what the disk is doing, and — for a node that actually holds a
  // copy — where that copy lives and how big it is.
  const facts = [state?.diskState || 'disk state unknown', diskless ? undefined : backing]
    .filter(Boolean)
    .join(' · ');

  const resync =
    state?.replicationState &&
    state.replicationState !== 'Established' &&
    state.replicationState !== ''
      ? state.replicationState +
        (state.syncPercent !== undefined &&
        state.syncPercent > 0 &&
        state.syncPercent < 100
          ? ` ${state.syncPercent.toFixed(0)}%`
          : '')
      : undefined;

  return (
    <g>
      <rect
        x={x}
        y={y}
        width={NODE_W}
        height={NODE_H}
        rx={10}
        fill="var(--card)"
        // A Primary is the node actually serving; it is what an operator looks
        // for first, so it gets the only accented outline in the drawing.
        stroke={isPrimary ? 'var(--primary)' : 'var(--border)'}
        strokeWidth={isPrimary ? 1.6 : 1}
        strokeDasharray={kind === 'dr' ? '5 4' : undefined}
      />
      <rect
        aria-hidden
        x={x + 3}
        y={y + 10}
        width={3.5}
        height={NODE_H - 20}
        rx={2}
        fill={TONE_FILL[railTone(node)]}
      />
      <text
        x={x + 16}
        y={y + 26}
        className="font-mono text-[13.5px] font-semibold"
        fill="var(--foreground)"
      >
        {name}
      </text>

      {/* RoleChip's vocabulary in SVG: Primary in the accent tint, everything
          else grey, and the word carries the meaning either way. */}
      <rect
        x={x + 16}
        y={y + 36}
        width={chipW(roleLabel)}
        height={19}
        rx={4}
        fill={isPrimary ? 'var(--accent)' : 'var(--secondary)'}
      />
      <text
        x={x + 24}
        y={y + 49.5}
        className={`font-sans text-[11px] ${isPrimary ? 'font-semibold' : ''}`}
        fill={isPrimary ? 'var(--accent-foreground)' : 'var(--secondary-foreground)'}
      >
        {roleLabel}
      </text>
      {qualifier && (
        <text
          x={x + 24 + chipW(roleLabel)}
          y={y + 49.5}
          className="font-mono text-[10.5px]"
          fill="var(--muted-foreground)"
        >
          {qualifier}
        </text>
      )}

      <text
        x={x + 16}
        y={y + 72}
        className="font-mono text-[10.5px]"
        fill="var(--muted-foreground)"
      >
        {facts}
      </text>
      {resync && (
        <text
          x={x + NODE_W - 12}
          y={y + 72}
          textAnchor="end"
          className="font-mono text-[10.5px]"
          fill="var(--status-warn-text)"
        >
          {resync}
        </text>
      )}
    </g>
  );
}

/**
 * HTML, not SVG: legend text must not scale with the viewBox, and this is the
 * one part of the drawing that is prose. Every stroke style is named in words —
 * the line samples only repeat what the label says.
 */
function Legend({ hasWAN, protocol }: { hasWAN: boolean; protocol: string }) {
  return (
    <div className="mb-3 flex flex-wrap items-center gap-x-5 gap-y-1.5 text-[11.5px] text-muted-foreground">
      <span className="inline-flex items-center gap-2">
        <span aria-hidden className="inline-block h-[2px] w-4 bg-primary" />
        synchronous{protocol ? ` (protocol ${protocol})` : ''} — acknowledged
        only once every peer has the write
      </span>
      {hasWAN && (
        <span className="inline-flex items-center gap-2">
          <span
            aria-hidden
            className="inline-block w-4 border-t-2 border-dashed border-muted-foreground"
          />
          asynchronous WAN leg (protocol A) — acknowledged first, shipped after,
          so it can lag
        </span>
      )}
      <span>rail colour repeats the disk state written in each box</span>
    </div>
  );
}
