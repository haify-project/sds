import { useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { api, type Volume } from '../../services/api';
import { Button } from '@/components/ui/button';
import { Label } from '@/components/ui/label';
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { toast } from 'sonner';
import { Loader2 } from 'lucide-react';
import { type PoolOpt } from './types';

// Moves one volume to another pool, one node at a time, while the resource
// keeps serving. Only pools present on every node holding a replica are
// offered: the controller refuses the others.
export function MoveVolumeForm({
  resourceName,
  replicaNodes,
  volume,
  pools,
  onStarted,
}: {
  resourceName: string;
  replicaNodes: string[];
  volume: Volume;
  pools: PoolOpt[];
  onStarted: () => void;
}) {
  const queryClient = useQueryClient();
  const [target, setTarget] = useState('');
  const [confirming, setConfirming] = useState(false);

  // Pool names may or may not carry the haify_ prefix on the node.
  const bare = (name?: string) => (name ?? '').replace(/^haify_/, '');
  const candidates = Array.from(new Set(pools.map((p) => p.name))).filter(
    (name) =>
      bare(name) !== bare(volume.pool) && replicaNodes.every((n) => pools.some((p) => p.name === name && p.node === n)),
  );

  const move = useMutation({
    mutationFn: () => api.moveVolume({ resource: resourceName, volumeId: volume.volumeId, pool: target }),
    onSuccess: (res) => {
      toast.success(res.message);
      queryClient.invalidateQueries({ queryKey: ['storage-jobs'] });
      onStarted();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  if (candidates.length === 0) {
    return (
      <p className="mt-3 text-xs text-muted-foreground">
        No other pool exists on every node holding a replica ({replicaNodes.join(', ')}).
      </p>
    );
  }

  return (
    <div className="mt-3 space-y-2">
      <Label>Move to pool</Label>
      <div className="flex items-end gap-2">
        <Select value={target} onValueChange={setTarget}>
          <SelectTrigger className="flex-1">
            <SelectValue placeholder="Select a pool..." />
          </SelectTrigger>
          <SelectContent>
            {candidates.map((name) => (
              <SelectItem key={name} value={name}>
                {name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button type="button" disabled={!target || move.isPending} onClick={() => setConfirming(true)}>
          {move.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
          Move
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">
        Rebuilt in the new pool one node at a time, each copy resynced in full from the others. The resource keeps
        serving throughout.
      </p>

      <AlertDialog open={confirming} onOpenChange={setConfirming}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              Move volume {volume.volumeId} of {resourceName} to {target}?
            </AlertDialogTitle>
            <AlertDialogDescription>
              Its snapshots in {volume.pool || 'the current pool'} are deleted with the old copy on each node. Back
              the resource up first if you need them.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => move.mutate()}>Move volume</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
