import { useState } from 'react';
import { useQueries, useQuery } from '@tanstack/react-query';
import { api, Resource, ResourceStatus } from '../services/api';
import { PageHeader } from '@/components/PageHeader';
import { StatusTickHead } from '@/components/StatusTick';
import { SegmentedFilter } from '@/components/SegmentedFilter';
import { RecordCard, RecordCards } from '@/components/RecordCard';
import { ResourceProfilesPage } from './ResourceProfilesPage';
import { useSearchParams } from 'react-router';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Table,
  TableBody,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { Plus, Boxes, Search } from 'lucide-react';
import {
  syncPollInterval,
  type Replication,
  replicationSummary,
  UNKNOWN_REPLICATION,
  totalGbOf,
} from './resources/replication';
import { type FilterKey, type RowDialog } from './resources/types';
import {
  nodeChips,
  NodeChips,
  ResourceChips,
  ReplicationCell,
  ResourceRow,
} from './resources/ResourceRow';
import { ResourceDialogs } from './resources/ResourceDialogs';
import { ResourceDetail } from './resources/ResourceDetail';
import { ResourceActionsMenu } from './resources/ResourceActionsMenu';
import { CreateResourceDialog } from './resources/CreateResourceDialog';

export function ResourcesPage() {
  const { data: resources, isLoading } = useQuery({
    queryKey: ['resources'],
    queryFn: () => api.getResources(),
  });

  const { data: pools } = useQuery({
    queryKey: ['pools'],
    queryFn: () => api.getPools(),
  });

  const { data: nodes } = useQuery({
    queryKey: ['nodes'],
    queryFn: () => api.getNodes(),
  });

  const { data: profiles } = useQuery({
    queryKey: ['resource-profiles'],
    queryFn: () => api.getResourceProfiles(),
  });

  const list = resources?.resources ?? [];

  // One live-status query per resource, held here rather than in each row: the
  // segmented filter has to count healthy and syncing resources, and those
  // words exist nowhere in the list response. The rows and the expanded panels
  // read the same query keys, so they are served from this cache rather than
  // fetching again, and the adaptive interval still stops polling the moment a
  // resource settles.
  const statusQueries = useQueries({
    queries: list.map((r) => ({
      queryKey: ['resource-status', r.name],
      queryFn: () => api.resourceStatus(r.name),
      refetchInterval: syncPollInterval,
    })),
  });

  // Derived once for both shapes of a row: the table at `md` and up and the
  // cards below it read these two maps, never a query of their own, so the two
  // views cannot describe the same resource differently.
  const statusOf = new Map<string, ResourceStatus | undefined>(
    list.map((r, i) => [r.name, statusQueries[i]?.data?.status]),
  );
  const replication = new Map<string, Replication>(
    list.map((r) => [r.name, replicationSummary(statusOf.get(r.name))]),
  );

  const [params, setParams] = useSearchParams();
  // Profiles live here rather than in their own nav entry: they are templates
  // for resources and do nothing on their own, so they belong beside the things
  // they create. It also matches the CLI, where the command has always been
  // `sds resource profile`.
  const tab = params.get('tab') === 'profiles' ? 'profiles' : 'resources';

  const [query, setQuery] = useState('');
  const [filter, setFilter] = useState<FilterKey>('all');
  const [createOpen, setCreateOpen] = useState(false);

  // Expansion and the row dialogs live on the page rather than inside the row:
  // below `md` a resource is a card and above it a table row, and state held in
  // either would be a second copy that disagrees with the other. Several may be
  // open at once, which is the whole point of an inline panel over a modal.
  const [expanded, setExpanded] = useState<string[]>([]);
  const [dialog, setDialog] = useState<{ resource: string; kind: RowDialog } | null>(
    null,
  );
  const toggleExpanded = (name: string) =>
    setExpanded((cur) =>
      cur.includes(name) ? cur.filter((n) => n !== name) : [...cur, name],
    );

  const matchesFilter = (r: Resource, key: FilterKey) => {
    if (key === 'all') return true;
    if (key === 'offsite') return Boolean(r.wanMode);
    const tone = replication.get(r.name)?.tone;
    return key === 'healthy' ? tone === 'ok' : tone === 'warn';
  };

  const counts = {
    all: list.length,
    healthy: list.filter((r) => matchesFilter(r, 'healthy')).length,
    syncing: list.filter((r) => matchesFilter(r, 'syncing')).length,
    offsite: list.filter((r) => matchesFilter(r, 'offsite')).length,
  };

  // Name, node names and labels: the three things an operator actually types
  // when hunting for one resource among many.
  const needle = query.trim().toLowerCase();
  const visible = list.filter((r) => {
    if (!matchesFilter(r, filter)) return false;
    if (!needle) return true;
    const haystack = [
      r.name,
      ...r.nodes,
      ...(r.disklessNodes ?? []),
      ...(r.disklessClients ?? []),
      r.profile ?? '',
      ...Object.entries(r.labels ?? {}).map(([k, v]) => `${k}=${v}`),
    ]
      .join(' ')
      .toLowerCase();
    return haystack.includes(needle);
  });

  const volumeCount = list.reduce((n, r) => n + r.volumes.length, 0);
  const offsiteCount = counts.offsite;

  return (
    <div>
      <PageHeader
        className="mb-5"
        title="Resources"
        description={
          tab === 'profiles' ? (
            <>
              <span className="font-mono tabular-nums text-foreground">
                {profiles?.profiles?.length ?? 0}
              </span>{' '}
              resource profiles
            </>
          ) : (
            <>
              <span className="font-mono tabular-nums text-foreground">{list.length}</span>{' '}
              DRBD resources ·{' '}
              <span className="font-mono tabular-nums text-foreground">{volumeCount}</span>{' '}
              volumes
              {offsiteCount > 0 ? (
                <>
                  {' '}
                  ·{' '}
                  <span className="font-mono tabular-nums text-foreground">
                    {offsiteCount}
                  </span>{' '}
                  replicated off-site
                </>
              ) : null}
            </>
          )
        }
        actions={
          tab === 'resources' ? (
            <>
              <div className="relative w-full sm:w-[210px]">
                <Search className="pointer-events-none absolute top-1/2 left-2.5 h-[15px] w-[15px] -translate-y-1/2 text-muted-foreground" />
                <Input
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder="Filter resources"
                  aria-label="Filter resources by name, node or label"
                  className="h-[34px] pl-8 text-[13px]"
                />
              </div>
              <Button onClick={() => setCreateOpen(true)}>
                <Plus />
                Create resource
              </Button>
            </>
          ) : null
        }
      />

      <Tabs
        value={tab}
        onValueChange={(v) =>
          setParams(v === 'profiles' ? { tab: 'profiles' } : {}, { replace: true })
        }
        className="space-y-4"
      >
        {/* The strip is an inline row that never wraps; it scrolls in its own
            lane so a longer label cannot widen the page. */}
        <div className="overflow-x-auto">
          <TabsList>
            <TabsTrigger value="resources">Resources</TabsTrigger>
            <TabsTrigger value="profiles">Profiles</TabsTrigger>
          </TabsList>
        </div>

        <TabsContent value="profiles">
          <ResourceProfilesPage />
        </TabsContent>

        <TabsContent value="resources" className="space-y-3.5">
          {/* Four chips with counts are wider than 375px the moment a count
              reaches two digits. They scroll here rather than widening the page. */}
          <div className="overflow-x-auto">
            <SegmentedFilter
              aria-label="Filter resources by state"
              value={filter}
              onChange={setFilter}
              options={[
                { value: 'all', label: 'All', count: counts.all },
                { value: 'healthy', label: 'Healthy', count: counts.healthy },
                { value: 'syncing', label: 'Syncing', count: counts.syncing },
                { value: 'offsite', label: 'Off-site', count: counts.offsite },
              ]}
            />
          </div>

          {isLoading || !visible.length ? (
            <Card className="overflow-hidden">
              <CardContent className="p-0">
                {isLoading ? (
                  <div className="space-y-3 p-5">
                    {[0, 1, 2].map((i) => (
                      <Skeleton key={i} className="h-9 w-full" />
                    ))}
                  </div>
                ) : !list.length ? (
                  <div className="flex flex-col items-center justify-center gap-3 px-5 py-16 text-center">
                    <Boxes className="h-8 w-8 text-muted-foreground" />
                    <p className="text-sm text-balance text-muted-foreground">
                      No resources found. Create your first resource to get started.
                    </p>
                    <Button variant="outline" onClick={() => setCreateOpen(true)}>
                      <Plus />
                      Create resource
                    </Button>
                  </div>
                ) : (
                  <p className="px-5 py-16 text-center text-sm text-muted-foreground">
                    No resource matches this filter.
                  </p>
                )}
              </CardContent>
            </Card>
          ) : (
            <>
              {/* An eight-column table at 375px puts nodes, volumes and
                  replication past the right edge. Below `md` each row is a card
                  built from the same derived values the row below uses. */}
              <RecordCards>
                {visible.map((resource) => {
                  const rep = replication.get(resource.name) ?? UNKNOWN_REPLICATION;
                  const isOpen = expanded.includes(resource.name);
                  return (
                    <RecordCard
                      key={resource.name}
                      tone={rep.tone}
                      open={isOpen}
                      onToggle={() => toggleExpanded(resource.name)}
                      detailId={`resource-card-${resource.name}`}
                      title={
                        <>
                          <span className="font-mono text-[14px] font-semibold break-all">
                            {resource.name}
                          </span>
                          <ResourceChips resource={resource} />
                        </>
                      }
                      subtitle={
                        <span className="font-mono tabular-nums">
                          port {resource.port} · protocol {resource.protocol}
                        </span>
                      }
                      actions={
                        <ResourceActionsMenu
                          resource={resource}
                          onSelect={(kind) =>
                            setDialog({ resource: resource.name, kind })
                          }
                        />
                      }
                      facts={[
                        {
                          label: 'Nodes',
                          value: (
                            <NodeChips
                              chips={nodeChips(resource, statusOf.get(resource.name))}
                              wrap
                            />
                          ),
                        },
                        {
                          label: 'Volumes',
                          value: (
                            <span className="font-mono tabular-nums">
                              {resource.volumes.length} · {totalGbOf(resource)} GB
                            </span>
                          ),
                        },
                        { label: 'Replication', value: <ReplicationCell replication={rep} /> },
                      ]}
                    >
                      <ResourceDetail
                        resource={resource}
                        onOpenDialog={(kind) =>
                          setDialog({ resource: resource.name, kind })
                        }
                      />
                    </RecordCard>
                  );
                })}
              </RecordCards>

              <Card className="hidden overflow-hidden md:block">
                <CardContent className="p-0">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <StatusTickHead />
                        <TableHead>Resource</TableHead>
                        <TableHead>Port</TableHead>
                        <TableHead>Protocol</TableHead>
                        <TableHead>Nodes</TableHead>
                        <TableHead>Volumes</TableHead>
                        <TableHead className="w-[200px]">Replication</TableHead>
                        <TableHead className="pr-5 text-right">Actions</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {visible.map((resource) => (
                        <ResourceRow
                          key={resource.name}
                          resource={resource}
                          replication={
                            replication.get(resource.name) ?? UNKNOWN_REPLICATION
                          }
                          status={statusOf.get(resource.name)}
                          expanded={expanded.includes(resource.name)}
                          onToggle={() => toggleExpanded(resource.name)}
                          onOpenDialog={(kind) =>
                            setDialog({ resource: resource.name, kind })
                          }
                        />
                      ))}
                    </TableBody>
                  </Table>
                </CardContent>
              </Card>
            </>
          )}

          {list.length > 0 && (
            <p className="text-xs text-muted-foreground">
              Showing{' '}
              <span className="font-mono tabular-nums">{visible.length}</span> of{' '}
              <span className="font-mono tabular-nums">{list.length}</span> resources
            </p>
          )}

          {/* One set of dialogs per resource, mounted here rather than inside a
              row: the card and the row are two renderings of one resource, and
              a dialog owned by either would exist twice. */}
          {visible.map((resource) => (
            <ResourceDialogs
              key={resource.name}
              resource={resource}
              kind={dialog?.resource === resource.name ? dialog.kind : null}
              onSelect={(kind) =>
                setDialog(kind ? { resource: resource.name, kind } : null)
              }
              pools={pools?.pools ?? []}
              nodes={nodes?.nodes ?? []}
            />
          ))}
        </TabsContent>
      </Tabs>

      <CreateResourceDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        nodes={nodes?.nodes ?? []}
        pools={pools?.pools ?? []}
        profiles={profiles?.profiles ?? []}
      />
    </div>
  );
}
