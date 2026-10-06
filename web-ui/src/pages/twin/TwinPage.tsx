import { useEffect, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router';
import { useQueries, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTheme } from 'next-themes';
import { Loader2 } from 'lucide-react';
import { api, type ResourceStatus } from '@/services/api';
import { subscribeEvents, type ClusterEvent } from '@/services/events';
import { syncPollInterval } from '../resources/replication';
import { buildModel } from './model';
import { DARK, LIGHT } from './kit';
import { twin } from './store';
import { TwinScene } from './scene/TwinScene';
import { Labels } from './Labels';
import { Inspector } from './Inspector';
import { EventFeed, HealthStrip, Legend, Toolbar } from './Hud';

// The 3D view: the cluster as a room of computers, for watching rather than
// operating. Every number comes from the same queries the other pages use (and
// shares their cache); the event stream makes a failover show up within a
// second instead of at the next poll.

const POLL = 5000;
// Resource status runs drbdadm on the nodes, so it is polled slower than the
// lists — and fast while a copy resyncs, as the Resources page does.
const STATUS_POLL = 15000;

export function TwinPage() {
  const qc = useQueryClient();
  const { resolvedTheme } = useTheme();
  const palette = resolvedTheme === 'dark' ? DARK : LIGHT;

  const nodes = useQuery({ queryKey: ['nodes'], queryFn: () => api.getNodes(), refetchInterval: POLL });
  const pools = useQuery({ queryKey: ['pools'], queryFn: () => api.getPools(), refetchInterval: POLL * 3 });
  const resources = useQuery({ queryKey: ['resources'], queryFn: () => api.getResources(), refetchInterval: POLL });
  const gateways = useQuery({ queryKey: ['gateways'], queryFn: () => api.getGateways(), refetchInterval: POLL });
  const selfHa = useQuery({ queryKey: ['selfha'], queryFn: () => api.getSelfHaStatus(), refetchInterval: POLL });

  const list = useMemo(() => resources.data?.resources ?? [], [resources.data]);
  const statusQueries = useQueries({
    queries: list.map((r) => ({
      queryKey: ['resource-status', r.name],
      queryFn: () => api.resourceStatus(r.name),
      refetchInterval: (q: { state: { data?: unknown } }) => syncPollInterval(q) || STATUS_POLL,
    })),
  });
  const statusStamp = statusQueries.map((q) => q.dataUpdatedAt).join(',');

  const model = useMemo(() => {
    const statuses = new Map<string, ResourceStatus | undefined>(
      list.map((r, i) => [r.name, statusQueries[i]?.data?.status]),
    );
    return buildModel(
      nodes.data?.nodes ?? [],
      pools.data?.pools ?? [],
      list,
      statuses,
      gateways.data?.gateways ?? [],
      selfHa.data,
    );
    // statusQueries is a new array every render; statusStamp says when it changed.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nodes.data, pools.data, list, gateways.data, selfHa.data, statusStamp]);

  // Live events: pulse what they touch, and refetch it rather than wait for the poll.
  const [events, setEvents] = useState<ClusterEvent[]>([]);
  const [live, setLive] = useState(false);
  // Read through a ref so the stream is opened once, not again at every poll
  // that returns a new resource list.
  const membersOf = useRef(new Map<string, string[]>());
  membersOf.current = new Map(list.map((r) => [r.name, r.nodes]));
  useEffect(() => {
    return subscribeEvents({
      onStatus: setLive,
      onEvent: (e) => {
        setEvents((prev) => [e, ...prev.filter((x) => x.id !== e.id)].slice(0, 20));
        const sev = e.status === 'resolved' ? 'info' : e.severity;
        if (e.node) twin.flash(`node:${e.node}`, sev);
        if (e.resource) {
          for (const n of membersOf.current.get(e.resource) ?? []) twin.flash(`node:${n}`, sev);
          qc.invalidateQueries({ queryKey: ['resource-status', e.resource] });
        }
        qc.invalidateQueries({ queryKey: ['nodes'] });
        qc.invalidateQueries({ queryKey: ['selfha'] });
      },
    });
  }, [qc]);

  // Seed the feed with what happened before the page was opened.
  const recent = useQuery({ queryKey: ['events', 'twin'], queryFn: () => api.listEvents({ limit: 5 }) });
  const feed = events.length ? events : [...(recent.data?.events ?? [])].reverse();

  // Leaving the page must not leave a pointer cursor or a selection behind.
  useEffect(
    () => () => {
      document.body.style.cursor = '';
      twin.set({ selected: null, hovered: null });
    },
    [],
  );

  const loading = nodes.isLoading || resources.isLoading;

  return (
    // Full bleed: undo the content area's padding so the room fills it.
    <div className="relative -m-6 h-[calc(100%+3rem)] min-h-[520px] overflow-hidden lg:-mx-8 lg:-my-6">
      {loading ? (
        <div className="flex h-full items-center justify-center" role="status">
          <Loader2 className="size-5 animate-spin text-muted-foreground" />
          <span className="sr-only">Loading</span>
        </div>
      ) : model.nodes.length === 0 ? (
        <div className="flex h-full flex-col items-center justify-center gap-2 text-center">
          <p className="text-[15px] font-medium">No nodes yet</p>
          <p className="text-[13px] text-muted-foreground">
            Register a node and it appears here as a computer.{' '}
            <Link to="/nodes" className="font-medium text-primary hover:underline">
              Go to Nodes
            </Link>
          </p>
        </div>
      ) : (
        <>
          <TwinScene model={model} palette={palette} />
          <Labels model={model} />
          <HealthStrip model={model} live={live} />
          <Inspector model={model} />
          <EventFeed events={feed} model={model} />
          <Legend model={model} />
          <Toolbar />
        </>
      )}
    </div>
  );
}
