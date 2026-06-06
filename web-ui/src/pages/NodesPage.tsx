import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Server,
  HeartPulse,
  Info,
  Plus,
  Trash2,
  Loader2,
  CheckCircle2,
  XCircle,
} from 'lucide-react';
import { api, type Node, type HealthInfo } from '@/services/api';
import { StatusBadge } from '@/components/StatusBadge';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog';
import { Badge } from '@/components/ui/badge';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Separator } from '@/components/ui/separator';
import { Skeleton } from '@/components/ui/skeleton';

function formatLastSeen(lastSeen: string): string {
  const ts = Number(lastSeen);
  if (!ts) return '-';
  return new Date(ts * 1000).toLocaleString();
}

export function NodesPage() {
  const queryClient = useQueryClient();
  const { data: nodes, isLoading } = useQuery({
    queryKey: ['nodes'],
    queryFn: () => api.getNodes(),
  });

  const [registerOpen, setRegisterOpen] = useState(false);
  const [healthNode, setHealthNode] = useState<string | null>(null);
  const [healthData, setHealthData] = useState<HealthInfo | null>(null);
  const [detailsNode, setDetailsNode] = useState<Node | null>(null);

  const healthCheckMutation = useMutation({
    mutationFn: (nodeName: string) => api.healthCheck(nodeName),
    onSuccess: (data) => {
      setHealthData(data.health);
    },
    onError: (e: Error) => {
      toast.error(e.message);
      setHealthNode(null);
    },
  });

  const unregisterMutation = useMutation({
    mutationFn: (address: string) => api.unregisterNode(address),
    onSuccess: () => {
      toast.success('Node unregistered');
      queryClient.invalidateQueries({ queryKey: ['nodes'] });
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const handleHealthCheck = (nodeName: string) => {
    setHealthNode(nodeName);
    setHealthData(null);
    healthCheckMutation.mutate(nodeName);
  };

  const closeHealthDialog = (open: boolean) => {
    if (!open) {
      setHealthNode(null);
      setHealthData(null);
    }
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">Storage Nodes</h2>
        <Button onClick={() => setRegisterOpen(true)}>
          <Plus className="h-4 w-4" />
          Register Node
        </Button>
      </div>

      {isLoading ? (
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-2 xl:grid-cols-3">
          {Array.from({ length: 3 }).map((_, i) => (
            <Card key={i}>
              <CardHeader>
                <Skeleton className="h-6 w-32" />
              </CardHeader>
              <CardContent className="space-y-3">
                <Skeleton className="h-4 w-full" />
                <Skeleton className="h-4 w-3/4" />
                <Skeleton className="h-9 w-full" />
              </CardContent>
            </Card>
          ))}
        </div>
      ) : !nodes?.nodes.length ? (
        <Card>
          <CardContent className="flex flex-col items-center justify-center gap-3 py-16 text-center">
            <Server className="h-10 w-10 text-muted-foreground" />
            <p className="text-sm text-muted-foreground">
              No nodes registered. Register a storage node to get started.
            </p>
            <Button onClick={() => setRegisterOpen(true)} variant="outline">
              <Plus className="h-4 w-4" />
              Register Node
            </Button>
          </CardContent>
        </Card>
      ) : (
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-2 xl:grid-cols-3">
          {nodes.nodes.map((node) => {
            const isOnline = node.state === 'online';
            const isChecking =
              healthCheckMutation.isPending && healthNode === node.name;
            const isDeleting =
              unregisterMutation.isPending &&
              unregisterMutation.variables === node.address;
            return (
              <Card key={node.name} className="flex flex-col">
                <CardHeader className="flex flex-row items-start justify-between space-y-0">
                  <div className="flex items-center gap-3">
                    <div className="flex h-10 w-10 items-center justify-center rounded-full bg-muted">
                      <Server className="h-5 w-5 text-muted-foreground" />
                    </div>
                    <div>
                      <CardTitle className="text-base">{node.name}</CardTitle>
                      <p className="text-sm text-muted-foreground">
                        {node.hostname}
                      </p>
                    </div>
                  </div>
                  <StatusBadge status={node.state} />
                </CardHeader>
                <CardContent className="flex flex-1 flex-col">
                  <div className="space-y-2 text-sm">
                    <div className="flex justify-between gap-2">
                      <span className="text-muted-foreground">Address</span>
                      <span className="font-medium">{node.address}</span>
                    </div>
                    <div className="flex justify-between gap-2">
                      <span className="text-muted-foreground">Version</span>
                      <span className="font-medium">{node.version || '-'}</span>
                    </div>
                    <div className="flex justify-between gap-2">
                      <span className="text-muted-foreground">Last Seen</span>
                      <span className="font-medium">
                        {formatLastSeen(node.lastSeen)}
                      </span>
                    </div>
                  </div>

                  <Separator className="my-4" />

                  <div className="mt-auto flex gap-2">
                    <Button
                      variant="outline"
                      size="sm"
                      className="flex-1"
                      disabled={isChecking || !isOnline}
                      onClick={() => handleHealthCheck(node.name)}
                    >
                      {isChecking ? (
                        <Loader2 className="h-4 w-4 animate-spin" />
                      ) : (
                        <HeartPulse className="h-4 w-4" />
                      )}
                      Health
                    </Button>
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => setDetailsNode(node)}
                    >
                      <Info className="h-4 w-4" />
                      Details
                    </Button>
                    <AlertDialog>
                      <AlertDialogTrigger asChild>
                        <Button
                          variant="destructive"
                          size="sm"
                          disabled={isDeleting}
                        >
                          {isDeleting ? (
                            <Loader2 className="h-4 w-4 animate-spin" />
                          ) : (
                            <Trash2 className="h-4 w-4" />
                          )}
                        </Button>
                      </AlertDialogTrigger>
                      <AlertDialogContent>
                        <AlertDialogHeader>
                          <AlertDialogTitle>
                            Unregister node "{node.name}"?
                          </AlertDialogTitle>
                          <AlertDialogDescription>
                            This removes the node from the controller's
                            inventory. Resources and pools hosted on this node
                            will no longer be managed. This action cannot be
                            undone.
                          </AlertDialogDescription>
                        </AlertDialogHeader>
                        <AlertDialogFooter>
                          <AlertDialogCancel>Cancel</AlertDialogCancel>
                          <AlertDialogAction
                            onClick={() =>
                              unregisterMutation.mutate(node.address)
                            }
                          >
                            Unregister
                          </AlertDialogAction>
                        </AlertDialogFooter>
                      </AlertDialogContent>
                    </AlertDialog>
                  </div>
                </CardContent>
              </Card>
            );
          })}
        </div>
      )}

      <RegisterNodeDialog open={registerOpen} onOpenChange={setRegisterOpen} />

      <HealthDialog
        open={!!healthData}
        nodeName={healthNode}
        health={healthData}
        onOpenChange={closeHealthDialog}
      />

      <DetailsDialog
        node={detailsNode}
        onOpenChange={(open) => !open && setDetailsNode(null)}
      />
    </div>
  );
}

interface RegisterNodeDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

function RegisterNodeDialog({ open, onOpenChange }: RegisterNodeDialogProps) {
  const queryClient = useQueryClient();
  const [name, setName] = useState('');
  const [address, setAddress] = useState('');

  const registerMutation = useMutation({
    mutationFn: (data: { name: string; address: string }) =>
      api.registerNode(data),
    onSuccess: () => {
      toast.success('Node registered');
      queryClient.invalidateQueries({ queryKey: ['nodes'] });
      setName('');
      setAddress('');
      onOpenChange(false);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    registerMutation.mutate({ name: name.trim(), address: address.trim() });
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>Register Node</DialogTitle>
            <DialogDescription>
              Add a storage node to the controller inventory.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label htmlFor="node-name">Node Name</Label>
              <Input
                id="node-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="e.g., orange1"
                required
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="node-address">Node Address</Label>
              <Input
                id="node-address"
                value={address}
                onChange={(e) => setAddress(e.target.value)}
                placeholder="e.g., 192.168.1.100 or hostname"
                required
              />
            </div>
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={registerMutation.isPending}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={registerMutation.isPending}>
              {registerMutation.isPending && (
                <Loader2 className="h-4 w-4 animate-spin" />
              )}
              Register
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

interface HealthDialogProps {
  open: boolean;
  nodeName: string | null;
  health: HealthInfo | null;
  onOpenChange: (open: boolean) => void;
}

function HealthCheckRow({
  label,
  ok,
  detail,
}: {
  label: string;
  ok: boolean;
  detail?: string;
}) {
  return (
    <div className="flex items-center justify-between rounded-lg border p-3">
      <div className="flex items-center gap-3">
        {ok ? (
          <CheckCircle2 className="h-5 w-5 text-emerald-500" />
        ) : (
          <XCircle className="h-5 w-5 text-red-500" />
        )}
        <div>
          <p className="text-sm font-medium">{label}</p>
          {detail && (
            <p className="text-xs text-muted-foreground">{detail}</p>
          )}
        </div>
      </div>
    </div>
  );
}

function HealthDialog({
  open,
  nodeName,
  health,
  onOpenChange,
}: HealthDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            Health Check{nodeName ? ` — ${nodeName}` : ''}
          </DialogTitle>
          <DialogDescription>
            Prerequisite software and OCF agents on the node.
          </DialogDescription>
        </DialogHeader>
        {health && (
          <div className="space-y-4 py-2">
            <div className="space-y-2">
              <HealthCheckRow
                label="DRBD"
                ok={health.drbdInstalled}
                detail={
                  health.drbdInstalled
                    ? `Installed${
                        health.drbdVersion
                          ? ` · v${health.drbdVersion}`
                          : ''
                      }`
                    : 'Not installed'
                }
              />
              <HealthCheckRow
                label="DRBD Reactor"
                ok={health.drbdReactorInstalled}
                detail={
                  health.drbdReactorInstalled
                    ? `Installed${
                        health.drbdReactorVersion
                          ? ` · v${health.drbdReactorVersion}`
                          : ''
                      } · ${
                        health.drbdReactorRunning ? 'running' : 'stopped'
                      }`
                    : 'Not installed'
                }
              />
              <HealthCheckRow
                label="Resource Agents"
                ok={health.resourceAgentsInstalled}
                detail={
                  health.resourceAgentsInstalled
                    ? 'Installed'
                    : 'Not installed'
                }
              />
            </div>

            <div>
              <p className="mb-2 text-sm font-medium">Available OCF Agents</p>
              {health.availableAgents && health.availableAgents.length > 0 ? (
                <div className="flex flex-wrap gap-1.5">
                  {health.availableAgents.map((agent) => (
                    <Badge key={agent} variant="secondary">
                      {agent}
                    </Badge>
                  ))}
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">
                  No OCF agents detected.
                </p>
              )}
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

interface DetailsDialogProps {
  node: Node | null;
  onOpenChange: (open: boolean) => void;
}

function DetailsDialog({ node, onOpenChange }: DetailsDialogProps) {
  const details: { label: string; value: string }[] = node
    ? [
        { label: 'Name', value: node.name },
        { label: 'Address', value: node.address },
        { label: 'Hostname', value: node.hostname },
        { label: 'State', value: node.state },
        { label: 'Version', value: node.version || '-' },
        { label: 'Last Seen', value: formatLastSeen(node.lastSeen) },
      ]
    : [];

  return (
    <Dialog open={!!node} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Node Details</DialogTitle>
          <DialogDescription>{node?.name}</DialogDescription>
        </DialogHeader>
        <div className="py-2">
          {details.map((d, i) => (
            <div key={d.label}>
              {i > 0 && <Separator />}
              <div className="flex items-center justify-between gap-4 py-2.5 text-sm">
                <span className="text-muted-foreground">{d.label}</span>
                {d.label === 'State' ? (
                  <StatusBadge status={d.value} />
                ) : (
                  <span className="font-medium">{d.value}</span>
                )}
              </div>
            </div>
          ))}
        </div>
      </DialogContent>
    </Dialog>
  );
}
