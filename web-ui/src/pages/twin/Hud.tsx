import { useEffect, useRef } from 'react';
import { RotateCw, Tag } from 'lucide-react';
import { cn } from '@/lib/utils';
import { TONE_BG } from '@/components/status';
import { ViewControls, ViewTool } from '@/components/topology3d/ViewControls';
import { eventTime, eventTitle, type ClusterEvent } from '@/services/events';
import type { Health, TwinModel } from './model';
import { GATEWAY_COLOR } from './kit';
import { twin, useTwin } from './store';

// What floats over the scene: the cluster's health in one strip, the camera
// buttons, a legend, and the live event feed.

const card = 'pointer-events-auto rounded-xl border border-border bg-card/90 shadow-lg backdrop-blur';

function Stat({ value, label, health }: { value: string; label: string; health?: Health }) {
  return (
    <div className="flex flex-col px-3 py-1.5">
      <span className="flex items-center gap-1.5 text-[17px] leading-tight font-semibold tabular-nums">
        {health && <i className={cn('size-2 rounded-full', TONE_BG[health])} />}
        {value}
      </span>
      <span className="text-[11px] text-muted-foreground">{label}</span>
    </div>
  );
}

export function HealthStrip({ model, live }: { model: TwinModel; live: boolean }) {
  const k = model.kpi;
  const replicationHealth: Health = k.degraded ? 'bad' : k.syncing ? 'warn' : 'ok';
  // The value says what is wrong, the caption what it is out of.
  const replication = k.degraded ? `${k.degraded} degraded` : k.syncing ? `${k.syncing} syncing` : `${k.healthy}/${k.resources}`;
  const replicationLabel = k.degraded || k.syncing ? `of ${k.resources} resource${k.resources === 1 ? '' : 's'}` : k.resources === 1 ? 'resource in sync' : 'resources in sync';
  const fill = Math.round(k.fill * 100);
  // The strip covers the top of the room; the camera frames the nodes below it.
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = () => twin.set({ insetTop: el.offsetTop + el.offsetHeight + 8 });
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return (
    <div ref={ref} className={cn(card, 'absolute top-3 left-3 z-20 flex flex-wrap items-center divide-x divide-border/70 max-md:right-3')}>
      <Stat value={`${k.nodesOnline}/${k.nodes}`} label="nodes online" health={k.nodesOnline === k.nodes ? 'ok' : 'bad'} />
      <Stat value={replication} label={replicationLabel} health={replicationHealth} />
      <Stat value={String(k.primaries)} label="serving (primary)" />
      {k.gateways > 0 && (
        <Stat value={`${k.gatewaysUp}/${k.gateways}`} label="gateways up" health={k.gatewaysUp === k.gateways ? 'ok' : 'bad'} />
      )}
      <Stat value={`${fill}%`} label="fullest pool" health={fill >= 90 ? 'bad' : fill >= 80 ? 'warn' : 'ok'} />
      {model.controllerNode && <Stat value={model.controllerNode} label={model.vip ? `controller · ${model.vip}` : 'controller'} />}
      <div className="flex items-center gap-1.5 px-3 text-[11px] text-muted-foreground">
        <i className={cn('size-1.5 rounded-full', live ? 'animate-pulse bg-status-ok' : 'bg-status-idle')} />
        {live ? 'live' : 'reconnecting'}
      </div>
    </div>
  );
}

export function Toolbar() {
  const labels = useTwin((s) => s.labels);
  const spin = useTwin((s) => s.spin);
  const panel = useTwin((s) => s.selected !== null);
  return (
    <ViewControls
      onCamera={(op) => twin.camera(op)}
      className={cn('absolute right-3 bottom-3 z-20', panel && 'md:right-[316px] max-md:hidden')}
    >
      <ViewTool label={spin ? 'Stop turning' : 'Turn slowly'} active={spin} onClick={() => twin.set({ spin: !spin })}>
        <RotateCw className="size-4" />
      </ViewTool>
      <ViewTool label={labels ? 'Hide quiet labels' : 'Show all labels'} active={labels} onClick={() => twin.set({ labels: !labels })}>
        <Tag className="size-4" />
      </ViewTool>
    </ViewControls>
  );
}

export function Legend({ model }: { model: TwinModel }) {
  const panel = useTwin((s) => s.selected !== null);
  const types = [...new Set(model.gateways.map((g) => g.type))];
  return (
    <div className={cn(card, 'absolute right-16 bottom-3 z-20 hidden max-w-[340px] px-3 py-2 text-[11.5px] lg:block', panel && 'right-[364px]')}>
      <div className="flex flex-wrap gap-x-3 gap-y-1">
        <span className="flex items-center gap-1.5"><i className="size-2 rounded-full bg-status-ok" />in sync</span>
        <span className="flex items-center gap-1.5"><i className="size-2 rounded-full bg-status-warn" />syncing</span>
        <span className="flex items-center gap-1.5"><i className="size-2 rounded-full bg-status-bad" />degraded / down</span>
      </div>
      <p className="mt-1 text-muted-foreground">
        Drive lights are the replicas a node holds, blue for the primary; the glass tube is its pool; the case that glows
        runs the controller. Dashes run from the primary to its copies.
      </p>
      {types.length > 0 && (
        <div className="mt-1 flex flex-wrap gap-x-3">
          {types.map((t) => (
            <span key={t} className="flex items-center gap-1.5">
              <i className="size-2 rounded-sm" style={{ background: GATEWAY_COLOR[t] }} />
              {t.toUpperCase()}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

function ago(ms: number | null): string {
  if (ms === null) return '';
  const s = Math.max(0, Math.round((Date.now() - ms) / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  return `${Math.floor(s / 3600)}h`;
}

const SEVERITY: Record<string, Health> = { critical: 'bad', warning: 'warn', info: 'ok' };

export function EventFeed({ events, model }: { events: ClusterEvent[]; model: TwinModel }) {
  if (events.length === 0) return null;
  const target = (e: ClusterEvent): string | null => {
    if (e.node && model.nodes.some((n) => n.name === e.node)) return `node:${e.node}`;
    if (e.resource) {
      const l = model.links.find((x) => x.resources.some((r) => r.name === e.resource));
      if (l) return `link:${l.key}`;
    }
    return null;
  };
  return (
    <div className={cn(card, 'absolute bottom-3 left-3 z-20 w-[340px] max-w-[calc(100%-5rem)] py-1.5 max-md:hidden')}>
      <p className="px-3 pb-1 text-[11px] font-semibold text-muted-foreground">Latest events</p>
      <ul>
        {events.slice(0, 5).map((e) => {
          const to = target(e);
          return (
            <li key={e.id}>
              <button
                type="button"
                disabled={!to}
                onClick={() => to && twin.select(to, true)}
                className="flex w-full items-start gap-2 px-3 py-1 text-left text-[12px] enabled:hover:bg-muted/70"
              >
                <i className={cn('mt-1.5 size-1.5 shrink-0 rounded-full', TONE_BG[e.status === 'resolved' ? 'ok' : SEVERITY[e.severity] ?? 'idle'])} />
                <span className="min-w-0 flex-1">
                  <span className="font-medium">{eventTitle(e)}</span>
                  <span className="block truncate text-muted-foreground" title={e.message}>{e.message}</span>
                </span>
                <span className="shrink-0 text-[11px] text-muted-foreground tabular-nums">{ago(eventTime(e))}</span>
              </button>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
