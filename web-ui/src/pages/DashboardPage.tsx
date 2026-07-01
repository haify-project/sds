import { useQuery } from '@tanstack/react-query';
import { Server, HardDrive, Boxes, Network, ShieldCheck } from 'lucide-react';
import { api } from '@/services/api';
import { StatusBadge } from '@/components/StatusBadge';
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import { Badge } from '@/components/ui/badge';
import { Progress } from '@/components/ui/progress';
import { Skeleton } from '@/components/ui/skeleton';
import { Separator } from '@/components/ui/separator';

export function DashboardPage() {
  const { data: nodes, isLoading: nodesLoading } = useQuery({
    queryKey: ['nodes'],
    queryFn: () => api.getNodes(),
  });

  const { data: pools, isLoading: poolsLoading } = useQuery({
    queryKey: ['pools'],
    queryFn: () => api.getPools(),
  });

  const { data: resources, isLoading: resourcesLoading } = useQuery({
    queryKey: ['resources'],
    queryFn: () => api.getResources(),
  });

  const { data: gateways, isLoading: gatewaysLoading } = useQuery({
    queryKey: ['gateways'],
    queryFn: () => api.getGateways(),
  });

  const { data: selfHa, isLoading: selfHaLoading } = useQuery({
    queryKey: ['selfha'],
    queryFn: () => api.getSelfHaStatus(),
  });

  const totalNodes = nodes?.nodes.length ?? 0;
  const onlineNodes =
    nodes?.nodes.filter((n) => n.state === 'online').length ?? 0;

  // selfHa.activeNode is an address; members are node names. Resolve the
  // active node's name so it displays as a name and highlights correctly.
  const nodeNameByAddr = new Map(
    (nodes?.nodes ?? []).map((n) => [n.address, n.name]),
  );
  const activeNodeName = selfHa?.activeNode
    ? nodeNameByAddr.get(selfHa.activeNode) ?? selfHa.activeNode
    : '';

  const totalStorage =
    pools?.pools.reduce((acc, p) => acc + Number(p.totalGb), 0) ?? 0;
  const freeStorage =
    pools?.pools.reduce((acc, p) => acc + Number(p.freeGb), 0) ?? 0;
  const usedStorage = Math.max(totalStorage - freeStorage, 0);
  const usagePct = totalStorage > 0 ? (usedStorage / totalStorage) * 100 : 0;

  const totalResources = resources?.resources.length ?? 0;

  const totalGateways = gateways?.gateways.length ?? 0;
  const runningGateways =
    gateways?.gateways.filter((g) => g.state === 'running').length ?? 0;

  return (
    <div className="space-y-6">
      {/* Stat cards */}
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-4">
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-normal text-muted-foreground">
              Online Nodes
            </CardTitle>
            <Server className="h-4 w-4 text-primary" />
          </CardHeader>
          <CardContent>
            {nodesLoading ? (
              <Skeleton className="h-8 w-24" />
            ) : (
              <>
                <div className="font-mono text-3xl font-semibold tracking-tight tabular-nums">
                  {onlineNodes}
                  <span className="text-xl text-muted-foreground">
                    /{totalNodes}
                  </span>
                </div>
                <p className="mt-1 text-xs text-muted-foreground">
                  nodes reporting online
                </p>
              </>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-normal text-muted-foreground">
              Storage
            </CardTitle>
            <HardDrive className="h-4 w-4 text-primary" />
          </CardHeader>
          <CardContent>
            {poolsLoading ? (
              <Skeleton className="h-8 w-32" />
            ) : (
              <>
                <div className="font-mono text-3xl font-semibold tracking-tight tabular-nums">
                  {freeStorage}
                  <span className="text-sm font-normal text-muted-foreground">
                    {' '}
                    / {totalStorage} GB free
                  </span>
                </div>
                <Progress value={usagePct} className="mt-3" />
                <p className="mt-1 text-xs text-muted-foreground">
                  {usedStorage} GB used ({usagePct.toFixed(0)}%)
                </p>
              </>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-normal text-muted-foreground">
              Resources
            </CardTitle>
            <Boxes className="h-4 w-4 text-primary" />
          </CardHeader>
          <CardContent>
            {resourcesLoading ? (
              <Skeleton className="h-8 w-16" />
            ) : (
              <>
                <div className="font-mono text-3xl font-semibold tracking-tight tabular-nums">
                  {totalResources}
                </div>
                <p className="mt-1 text-xs text-muted-foreground">
                  DRBD resources
                </p>
              </>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-normal text-muted-foreground">
              Gateways
            </CardTitle>
            <Network className="h-4 w-4 text-primary" />
          </CardHeader>
          <CardContent>
            {gatewaysLoading ? (
              <Skeleton className="h-8 w-20" />
            ) : (
              <>
                <div className="font-mono text-3xl font-semibold tracking-tight tabular-nums">
                  {runningGateways}
                  <span className="text-xl text-muted-foreground">
                    /{totalGateways}
                  </span>
                </div>
                <p className="mt-1 text-xs text-muted-foreground">
                  running gateways
                </p>
              </>
            )}
          </CardContent>
        </Card>
      </div>

      {/* Controller Self-HA */}
      <Card>
        <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
          <CardTitle className="flex items-center gap-2 text-base">
            <ShieldCheck className="h-5 w-5 text-muted-foreground" />
            Controller Self-HA
          </CardTitle>
          {!selfHaLoading && (
            <StatusBadge status={selfHa?.enabled ? 'enabled' : 'disabled'} />
          )}
        </CardHeader>
        <CardContent>
          {selfHaLoading ? (
            <div className="space-y-2">
              <Skeleton className="h-5 w-48" />
              <Skeleton className="h-5 w-64" />
            </div>
          ) : selfHa?.enabled ? (
            <div className="space-y-3 text-sm">
              <div className="flex flex-wrap gap-x-8 gap-y-2">
                <div>
                  <span className="text-muted-foreground">VIP: </span>
                  <span className="font-medium">{selfHa.vip || '-'}</span>
                </div>
                <div>
                  <span className="text-muted-foreground">Active node: </span>
                  <span className="font-medium">{activeNodeName || '-'}</span>
                </div>
                <div>
                  <span className="text-muted-foreground">Resource: </span>
                  <span className="font-medium">{selfHa.resource || '-'}</span>
                </div>
              </div>
              {selfHa.nodes && selfHa.nodes.length > 0 && (
                <>
                  <Separator />
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-muted-foreground">Members:</span>
                    {selfHa.nodes.map((n) => (
                      <Badge
                        key={n}
                        variant={n === activeNodeName ? 'default' : 'secondary'}
                      >
                        {n}
                        {n === activeNodeName && ' (active)'}
                      </Badge>
                    ))}
                  </div>
                </>
              )}
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">
              Standalone controller — self-HA is not enabled.
            </p>
          )}
        </CardContent>
      </Card>

      {/* Cluster nodes table */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Cluster Nodes</CardTitle>
        </CardHeader>
        <CardContent>
          {nodesLoading ? (
            <div className="space-y-2">
              {Array.from({ length: 3 }).map((_, i) => (
                <Skeleton key={i} className="h-10 w-full" />
              ))}
            </div>
          ) : totalNodes === 0 ? (
            <div className="flex flex-col items-center justify-center gap-2 py-10 text-center">
              <Server className="h-8 w-8 text-muted-foreground" />
              <p className="text-sm text-muted-foreground">
                No nodes registered yet.
              </p>
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Address</TableHead>
                  <TableHead>Hostname</TableHead>
                  <TableHead>State</TableHead>
                  <TableHead>Version</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {nodes?.nodes.map((node) => (
                  <TableRow key={node.name}>
                    <TableCell className="font-medium">{node.name}</TableCell>
                    <TableCell className="font-mono text-xs text-muted-foreground">
                      {node.address}
                    </TableCell>
                    <TableCell className="font-mono text-xs text-muted-foreground">
                      {node.hostname}
                    </TableCell>
                    <TableCell>
                      <StatusBadge status={node.state} />
                    </TableCell>
                    <TableCell className="font-mono text-xs text-muted-foreground">
                      {node.version}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
