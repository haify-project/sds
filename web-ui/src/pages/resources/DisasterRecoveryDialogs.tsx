import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api, Resource } from '../../services/api';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { toast } from 'sonner';
import { Loader2 } from 'lucide-react';
import { type NodeOpt } from './types';
import { formatBytes } from './StatusPanels';

// AddDRDialog attaches an off-site asynchronous replica to a running resource.
//
// This used to be a create-time-only decision, which is the wrong moment to have
// to make it: off-site DR is what an operator adds after a service has proven it
// matters. The dialog therefore asks only for the things the controller cannot
// work out — which node, and the address the primary site should dial.
export function AddDRDialog({
  open,
  onOpenChange,
  resource,
  nodes,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  resource: Resource;
  nodes: NodeOpt[];
}) {
  const [drNode, setDrNode] = useState('');
  const [endpoint, setEndpoint] = useState('');
  const [wanPort, setWanPort] = useState('');
  const queryClient = useQueryClient();

  // A node already holding a replica or the quorum tiebreaker cannot also be
  // the DR site, so it is not worth offering.
  const taken = new Set([
    ...resource.nodes,
    ...(resource.disklessNodes ?? []),
    ...(resource.disklessClients ?? []),
  ]);
  const candidates = nodes.filter((n) => !taken.has(n.name));

  const add = useMutation({
    mutationFn: () =>
      api.addDR(resource.name, {
        drNode,
        drEndpoint: endpoint.trim(),
        wanPort: wanPort ? Number(wanPort) : 0,
      }),
    onSuccess: (res) => {
      if (!res.success) {
        toast.error(res.message || 'Failed to add DR site');
        return;
      }
      toast.success(
        `DR site "${drNode}" attached on WAN port ${res.wanPort}; initial sync runs in the background`,
      );
      queryClient.invalidateQueries({ queryKey: ['resources'] });
      queryClient.invalidateQueries({ queryKey: ['resource-status', resource.name] });
      onOpenChange(false);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Add DR site to "{resource.name}"</DialogTitle>
          <DialogDescription>
            An off-site copy kept asynchronously. The existing replicas keep
            their synchronous link and the resource keeps serving throughout.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <div className="space-y-2">
            <Label>DR node</Label>
            <select
              className="h-9 w-full rounded-md border bg-background px-3 text-sm"
              value={drNode}
              onChange={(e) => setDrNode(e.target.value)}
            >
              <option value="">Select a node…</option>
              {candidates.map((n) => (
                <option key={n.name} value={n.name}>
                  {n.name} ({n.address})
                </option>
              ))}
            </select>
            {candidates.length === 0 && (
              <p className="text-xs text-destructive">
                Every registered node already takes part in this resource.
                Register the DR machine first.
              </p>
            )}
          </div>

          <div className="space-y-2">
            <Label>Public endpoint</Label>
            <Input
              value={endpoint}
              onChange={(e) => setEndpoint(e.target.value)}
              placeholder="203.0.113.7 or dr.example.com"
            />
            <p className="text-xs text-muted-foreground">
              The address the primary site dials. It is often not the node's
              management address: a cloud node's public IP is usually NAT'd and
              absent from its own interfaces.
            </p>
          </div>

          <div className="space-y-2">
            <Label>WAN port (optional)</Label>
            <Input
              type="number"
              value={wanPort}
              onChange={(e) => setWanPort(e.target.value)}
              placeholder="auto-allocate"
            />
            <p className="text-xs text-muted-foreground">
              One port per replica is used from here. Whichever ports are chosen
              must be reachable on the DR node — open them in its firewall or
              security group first.
            </p>
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button
            onClick={() => add.mutate()}
            disabled={!drNode || !endpoint.trim() || add.isPending}
          >
            {add.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
            Add DR Site
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// DRFailoverDialog force-promotes the DR copy after the primary site is lost.
//
// The action is lossy by construction: replication is asynchronous, so writes
// the primary already acknowledged may never have crossed the WAN. How much is
// not a detail to bury in prose — it is the decision — so the current backlog is
// read live and shown next to the button, and "unknown" is shown as unknown
// rather than rendered as a reassuring zero.
export function DRFailoverDialog({
  open,
  onOpenChange,
  resource,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  resource: Resource;
}) {
  const [confirm, setConfirm] = useState('');
  const queryClient = useQueryClient();

  const { data } = useQuery({
    queryKey: ['resource-status', resource.name],
    queryFn: () => api.resourceStatus(resource.name),
    enabled: open,
  });
  const status = data?.status;
  const drNode = status?.drNode ?? resource.drNode ?? '';
  const backlog = status?.wanMetrics?.bufferUsedBytes;

  const failover = useMutation({
    // There is no dedicated RPC: a DR failover IS a forced promote of the DR
    // node. Force is required because the DR peer is by definition not known to
    // be current relative to a primary site that is gone, and a plain promote
    // would refuse for exactly that reason.
    mutationFn: () => api.setPrimary(resource.name, drNode, true),
    onSuccess: (res) => {
      if (!res.success) {
        toast.error(res.message || 'DR failover failed');
        return;
      }
      toast.success(`"${resource.name}" promoted on DR node "${drNode}"`);
      queryClient.invalidateQueries({ queryKey: ['resources'] });
      queryClient.invalidateQueries({ queryKey: ['resource-status', resource.name] });
      onOpenChange(false);
      setConfirm('');
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>DR failover: promote "{resource.name}" off-site</DialogTitle>
          <DialogDescription>
            Use this when the primary site is lost. It force-promotes the DR
            copy, which is not automatic and not reversible by itself.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <div className="rounded-lg border p-3 text-sm">
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">DR node</span>
              <span className="font-medium">{drNode || '—'}</span>
            </div>
          </div>

          <div className="rounded-lg border border-destructive/40 bg-destructive/5 p-3">
            <p className="text-sm font-medium text-destructive">Data that will be lost</p>
            <p className="mt-1 text-lg font-semibold">
              {backlog === undefined ? (
                <span className="text-base font-normal text-muted-foreground">
                  unknown — the proxy published no metrics
                </span>
              ) : (
                formatBytes(Number(backlog))
              )}
            </p>
            <p className="mt-2 text-xs text-muted-foreground">
              Replication is asynchronous: the primary acknowledged these writes
              to its applications before they crossed the WAN. Promoting now
              accepts their loss.
            </p>
          </div>

          <div className="space-y-2">
            <Label>
              Type <span className="font-mono">{resource.name}</span> to confirm
            </Label>
            <Input value={confirm} onChange={(e) => setConfirm(e.target.value)} />
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button
            variant="destructive"
            onClick={() => failover.mutate()}
            disabled={confirm !== resource.name || !drNode || failover.isPending}
          >
            {failover.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
            Promote DR
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
