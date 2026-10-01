import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api, SelfHaStatus } from '@/services/api';
import { StatusBadge } from '@/components/StatusBadge';
import { cn } from '@/lib/utils';
import { toast } from 'sonner';
import { LogOut, Loader2, ShieldCheck, ShieldOff } from 'lucide-react';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
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
import { Label } from '@/components/ui/label';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { SELF_HA_RESOURCE, isRestartError } from './selfHa';
import { Fact } from './Fact';
import { EnableSelfHaDialog } from './EnableSelfHaDialog';

// ==================== Controller Self-HA ====================

export function SelfHaCard({ foldedIntoPromoter }: { foldedIntoPromoter: boolean }) {
  const queryClient = useQueryClient();
  const {
    data: status,
    isLoading,
    isError,
    error,
    refetch,
  } = useQuery({
    queryKey: ['selfha'],
    queryFn: () => api.getSelfHaStatus(),
    refetchInterval: 15000,
  });

  const [enableOpen, setEnableOpen] = useState(false);

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ['selfha'] });

  if (isLoading) {
    return (
      <Card className="gap-4 py-5">
        <CardHeader className="px-5">
          <CardTitle className="font-mono text-[15px]">
            {SELF_HA_RESOURCE}
          </CardTitle>
        </CardHeader>
        <CardContent className="px-5">
          <Skeleton className="h-24 w-full" />
        </CardContent>
      </Card>
    );
  }

  // A failed status query is NOT the same as "self-HA disabled" — showing
  // the disabled state here would invite an accidental second enablement.
  if (isError) {
    return (
      <Card className="gap-4 py-5">
        <CardHeader className="px-5">
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0">
              <CardTitle className="font-mono text-[15px]">
                {SELF_HA_RESOURCE}
              </CardTitle>
              <p className="mt-1 text-[12.5px] text-muted-foreground">
                Control plane · state unknown
              </p>
            </div>
            <StatusBadge status="unknown" />
          </div>
        </CardHeader>
        <CardContent className="space-y-3 px-5">
          <p className="text-[13px] text-muted-foreground">
            Could not load self-HA status: {(error as Error).message}
          </p>
          <Button size="sm" variant="outline" onClick={() => refetch()}>
            <Loader2 className="mr-2 h-4 w-4" />
            Retry
          </Button>
        </CardContent>
      </Card>
    );
  }

  // Enabled and already shown as a promoter: that card carries the chip and
  // these controls, so a second card here would be the duplicate this change
  // removed. Everything above still renders — a loading or errored status has
  // no promoter card to have been folded into.
  if (status?.enabled && foldedIntoPromoter) return null;

  return (
    <Card className="gap-4 py-5">
      <CardHeader className="px-5">
        <div className="flex items-start justify-between gap-4">
          <div className="min-w-0">
            <CardTitle className="font-mono text-[15px]">
              {status?.resource || SELF_HA_RESOURCE}
            </CardTitle>
            <p className="mt-1 text-[12.5px] text-muted-foreground">
              Control plane ·{' '}
              {status?.enabled ? 'self-managed promoter' : 'standalone'}
            </p>
          </div>
          {status?.enabled ? (
            <StatusBadge status="enabled" />
          ) : (
            <Button size="sm" onClick={() => setEnableOpen(true)}>
              <ShieldCheck className="mr-2 h-4 w-4" />
              Enable Self-HA
            </Button>
          )}
        </div>
      </CardHeader>
      <CardContent className="px-5">
        {status?.enabled ? (
          <SelfHaEnabled status={status} />
        ) : (
          <div className="flex items-start gap-3">
            <ShieldOff
              aria-hidden
              className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground"
            />
            <p className="text-[13px] text-muted-foreground">
              Standalone controller. Enable Self-HA to run the management plane
              on its own DRBD resource with a floating VIP and automatic
              failover.
            </p>
          </div>
        )}
      </CardContent>

      <EnableSelfHaDialog
        open={enableOpen}
        onOpenChange={setEnableOpen}
        onEnabled={() => {
          setEnableOpen(false);
          invalidate();
        }}
      />
    </Card>
  );
}

function SelfHaEnabled({ status }: { status: SelfHaStatus }) {
  const { data: nodes } = useQuery({
    queryKey: ['nodes'],
    queryFn: () => api.getNodes(),
  });

  // status.activeNode is an address; members are node names. Resolve the
  // active node's name so it displays as a name and highlights correctly.
  const nodeNameByAddr = new Map(
    (nodes?.nodes ?? []).map((n) => [n.address, n.name]),
  );
  const activeNodeName = status.activeNode
    ? nodeNameByAddr.get(status.activeNode) ?? status.activeNode
    : '';

  return (
    <div className="space-y-4">
      <p className="text-[12.5px] text-muted-foreground">
        Active on{' '}
        <span className="font-mono text-foreground">
          {activeNodeName || 'no node'}
        </span>
      </p>

      <Fact label="Virtual IP" value={status.vip || '-'} mono />

      <div>
        <div className="eyebrow">Member nodes</div>
        <div className="mt-2 flex flex-wrap gap-2">
          {(status.nodes ?? []).map((node) => {
            const isActive = node === activeNodeName;
            return (
              <span
                key={node}
                className={cn(
                  'inline-flex items-center gap-2 rounded-[5px] border px-2 py-1 font-mono text-xs',
                  isActive
                    ? 'border-transparent bg-accent text-accent-foreground'
                    : 'border-border bg-muted text-muted-foreground'
                )}
              >
                {node}
                {isActive ? (
                  <span className="text-[10.5px]">active</span>
                ) : null}
              </span>
            );
          })}
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-2 border-t border-border pt-4">
        <SelfHaControls status={status} />
      </div>
    </div>
  );
}

/**
 * Evict-controller and disable, wherever the control plane is shown.
 *
 * They used to live inside the self-HA card and only there. Now the control
 * plane is normally a promoter card, so these had to be reachable from it —
 * and lifting them into a shared component rather than copying them is what
 * keeps one behaviour: the eviction re-reads the active node from the server
 * before acting and then polls for where the controller actually landed, which
 * a second copy would drift away from on the first edit.
 *
 * Kept as buttons rather than menu items, deliberately: both open an
 * AlertDialog, and a dialog nested in a dropdown unmounts with the menu.
 */
export function SelfHaControls({ status }: { status: SelfHaStatus }) {
  const queryClient = useQueryClient();
  const [disableNode, setDisableNode] = useState(status.activeNode || '');

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ['selfha'] });

  // Report where the controller actually landed. "Eviction initiated" alone is
  // not enough to tell whether anything happened: the card may already have
  // been showing a stale active node, and a controller that fails back to the
  // node you thought it was on looks identical to one that never moved.
  //
  // The controller is the thing being relocated, so the API is unreachable for
  // the middle of this — errors are expected and are not a failure.
  const pollControllerMoved = async (fromNode: string) => {
    for (let i = 0; i < 20; i++) {
      await new Promise((r) => setTimeout(r, 2000));
      try {
        const s = await api.getSelfHaStatus();
        if (s.activeNode && s.activeNode !== fromNode) {
          toast.success(`Controller is now active on ${s.activeNode}`);
          invalidate();
          return;
        }
      } catch {
        // The controller is mid-move; keep waiting.
      }
    }
    toast.info('Controller failover is taking longer than expected');
  };

  const evictMutation = useMutation({
    // Read the active node back from the server first. Evict acts on whichever
    // node is active right now, which need not be the one on screen — this card
    // can be showing state from before the tab was last backgrounded. Taking
    // "from" off the card instead would report a move that did not happen.
    mutationFn: async () => {
      const before = await api.getSelfHaStatus();
      toast.info(
        before.activeNode
          ? `Evicting controller from ${before.activeNode}; failing over`
          : 'Controller eviction initiated; failing over',
      );
      await api.evictHa(SELF_HA_RESOURCE);
      return before.activeNode ?? '';
    },
    onSuccess: (from) => {
      invalidate();
      void pollControllerMoved(from);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const disableMutation = useMutation({
    mutationFn: (node: string) => api.disableSelfHa(node),
    onSuccess: () => {
      toast.success('Self-HA disabled; controller returning to standalone');
      invalidate();
    },
    onError: (e: Error) => {
      if (isRestartError(e.message)) {
        toast.info('Controller is restarting; refresh shortly');
      } else {
        toast.error(e.message);
      }
    },
  });


  const onEvict = () => evictMutation.mutate();
  const onDisable = (node: string) => disableMutation.mutate(node);
  const isEvicting = evictMutation.isPending;
  const isDisabling = disableMutation.isPending;

  return (
    <>
        <AlertDialog>
          <AlertDialogTrigger asChild>
            <Button variant="outline" size="sm" disabled={isEvicting}>
              {isEvicting ? (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              ) : (
                <LogOut className="mr-2 h-4 w-4" />
              )}
              Evict Controller
            </Button>
          </AlertDialogTrigger>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Evict the controller?</AlertDialogTitle>
              <AlertDialogDescription>
                The management plane will briefly fail over to another node.
                In-flight requests may fail for a few seconds until the VIP
                moves and the controller restarts on the new active node.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction onClick={onEvict}>
                Evict Controller
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>

        <AlertDialog>
          <AlertDialogTrigger asChild>
            <Button variant="outline" size="sm" disabled={isDisabling}>
              {isDisabling ? (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              ) : (
                <ShieldOff className="mr-2 h-4 w-4" />
              )}
              Disable
            </Button>
          </AlertDialogTrigger>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Disable Self-HA?</AlertDialogTitle>
              <AlertDialogDescription>
                The controller will restart in standalone mode on the selected
                node. The VIP is released, so this UI endpoint may move and
                requests may fail briefly — reconnect to the node's own address
                afterwards.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <div className="space-y-1.5 py-2">
              <Label>Standalone Node</Label>
              <Select value={disableNode} onValueChange={setDisableNode}>
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="Select a node..." />
                </SelectTrigger>
                <SelectContent>
                  {(status.nodes ?? []).map((node) => (
                    <SelectItem key={node} value={node}>
                      {node}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction
                disabled={!disableNode}
                onClick={() => disableNode && onDisable(disableNode)}
              >
                Disable Self-HA
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
    </>
  );
}
