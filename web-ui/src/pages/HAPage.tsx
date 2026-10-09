import { useState } from 'react';
import { useNavigate } from 'react-router';
import { useQuery, useQueries, useMutation, useQueryClient } from '@tanstack/react-query';
import { api, HaConfig, Resource } from '@/services/api';
import { PageHeader } from '@/components/PageHeader';
import { toast } from 'sonner';
import { HeartPulse, Plus } from 'lucide-react';
import { Card, CardContent } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { type PromoterView } from './ha/promoter';
import { TopologySection } from './ha/TopologySection';
import { SelfHaCard } from './ha/SelfHaCard';
import { PromoterCard } from './ha/PromoterCard';
import { DetailsDialog } from './ha/DetailsDialog';

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

  // The promoter as deployed. Separate from the resource status above because
  // it answers a different question — that one is DRBD's view of the replicas,
  // this one is systemd's view of what the promoter starts.
  // Self-HA reads the same query key the card uses, so this shares one fetch.
  // The page needs it to know which promoter IS the control plane — the two
  // used to be separate cards for the same resource.
  const { data: selfHa } = useQuery({
    queryKey: ['selfha'],
    queryFn: () => api.getSelfHaStatus(),
    refetchInterval: 15000,
  });

  // DRBD reports the Primary by host name (`haify-e`) and the registry lists
  // members by node name (`node-e`). Without this map the active member is
  // never the one highlighted.
  const { data: nodeList } = useQuery({
    queryKey: ['nodes'],
    queryFn: () => api.getNodes(),
  });
  const nodeNameByHost = new Map<string, string>();
  for (const n of nodeList?.nodes ?? []) {
    if (n.hostname) nodeNameByHost.set(n.hostname, n.name);
    if (n.address) nodeNameByHost.set(n.address, n.name);
    nodeNameByHost.set(n.name, n.name);
  }

  const promoterQueries = useQueries({
    queries: configs.map((c) => ({
      queryKey: ['ha-promoter', c.resource],
      queryFn: () => api.getHaStatus(c.resource),
      refetchInterval: 15000,
    })),
  });

  const resourceMap = new Map(
    (resources?.resources ?? []).map((r) => [r.name, r] as [string, Resource])
  );

  const promoters: PromoterView[] = configs.map((config, i) => {
    const resource = resourceMap.get(config.resource);
    const status = statusQueries[i]?.data?.status;
    const promoter = promoterQueries[i]?.data?.promoters?.find(
      (pr) => pr.drbdResource === config.resource,
    );
    const nodeStates = status?.nodeStates ?? {};
    // Keyed by DRBD host name, which is what evict and the failover poll
    // compare against — resolving it to a Haify node name here would break both.
    const primaryNode = Object.keys(nodeStates).find(
      (n) => nodeStates[n]?.role === 'Primary'
    );
    return {
      config,
      resource,
      status,
      promoter,
      primaryNode,
      members: resource?.nodes ?? [],
      activeMember: primaryNode ? nodeNameByHost.get(primaryNode) ?? primaryNode : undefined,
    };
  });

  // Which promoter, if any, IS the control plane — and whether it has a card
  // for the self-HA panel to fold into.
  const controlPlaneResource = selfHa?.enabled ? selfHa.resource : undefined;
  const controlPlaneHasCard =
    !isLoading &&
    !!controlPlaneResource &&
    promoters.some((p) => p.config.resource === controlPlaneResource);

  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: ['ha'] });
    queryClient.invalidateQueries({ queryKey: ['resources'] });
    queryClient.invalidateQueries({ queryKey: ['ha-promoter'] });
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

          {/* One card per resource.
              The control plane used to get two: this card and, because its
              resource is also an HA config, a promoter card beside it — same
              resource, different fields, different buttons. It is a ROLE a
              promoter has, so it is a chip on that promoter's card now, and
              this card only appears when there is nothing to fold it into:
              self-HA off (the Enable affordance lives here), still loading, or
              errored — an errored status must never render as "disabled", which
              would invite a second enablement.

              Self-HA runs off its own query, so this stays on screen while the
              HA config list is still in flight; hiding it behind that load would
              take the controller's own failover controls away for as long as an
              unrelated call is slow. */}
          <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
            <SelfHaCard foldedIntoPromoter={controlPlaneHasCard} />
            {isLoading
              ? Array.from({ length: 2 }).map((_, i) => (
                  <Skeleton key={i} className="h-64 w-full" />
                ))
              : promoters.map((p) => (
                  <PromoterCard
                    key={p.config.resource}
                    view={p}
                    controlPlane={
                      p.config.resource === controlPlaneResource ? selfHa : undefined
                    }
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
