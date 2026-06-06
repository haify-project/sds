import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api, Pool } from '../services/api';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
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
import { Progress } from '@/components/ui/progress';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { toast } from 'sonner';
import {
  HardDrive,
  Plus,
  PlusCircle,
  Trash2,
  Loader2,
  Database,
  Server,
} from 'lucide-react';

const POOL_TYPE_OPTIONS = [
  { value: 'vg', label: 'LVM VG' },
  { value: 'thin_pool', label: 'LVM Thin Pool' },
  { value: 'zfs', label: 'ZFS' },
];

function poolTypeBadgeClass(type: string): string {
  switch (type) {
    case 'zfs':
      return 'bg-blue-100 text-blue-700 border-blue-200 dark:bg-blue-950 dark:text-blue-300 dark:border-blue-900';
    case 'thin_pool':
      return 'bg-amber-100 text-amber-700 border-amber-200 dark:bg-amber-950 dark:text-amber-300 dark:border-amber-900';
    case 'vg':
    default:
      return 'bg-purple-100 text-purple-700 border-purple-200 dark:bg-purple-950 dark:text-purple-300 dark:border-purple-900';
  }
}

function poolTypeLabel(type: string): string {
  const opt = POOL_TYPE_OPTIONS.find((o) => o.value === type);
  return opt ? opt.label : type.toUpperCase();
}

export function PoolsPage() {
  const queryClient = useQueryClient();

  const { data: pools, isLoading } = useQuery({
    queryKey: ['pools'],
    queryFn: () => api.getPools(),
  });

  const { data: nodes } = useQuery({
    queryKey: ['nodes'],
    queryFn: () => api.getNodes(),
  });

  const nodeMap =
    nodes?.nodes.reduce(
      (acc, node) => {
        acc[node.address] = node.name;
        return acc;
      },
      {} as Record<string, string>,
    ) ?? {};

  if (isLoading) {
    return (
      <div className="space-y-6">
        <div className="flex items-center justify-between">
          <h3 className="text-lg font-semibold">Storage Pools</h3>
        </div>
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-2 xl:grid-cols-3">
          {[0, 1, 2].map((i) => (
            <Card key={i}>
              <CardHeader>
                <Skeleton className="h-6 w-40" />
              </CardHeader>
              <CardContent className="space-y-3">
                <Skeleton className="h-16 w-full" />
                <Skeleton className="h-16 w-full" />
              </CardContent>
            </Card>
          ))}
        </div>
      </div>
    );
  }

  const poolsByNode =
    pools?.pools.reduce(
      (acc, pool) => {
        if (!acc[pool.node]) acc[pool.node] = [];
        acc[pool.node].push(pool);
        return acc;
      },
      {} as Record<string, Pool[]>,
    ) ?? {};

  const nodeEntries = Object.entries(poolsByNode);

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h3 className="text-lg font-semibold">Storage Pools</h3>
        <CreatePoolDialog nodes={nodes?.nodes ?? []} />
      </div>

      {nodeEntries.length === 0 ? (
        <div className="flex flex-col items-center justify-center gap-3 py-16 text-muted-foreground">
          <Database className="h-10 w-10" />
          <p>No storage pools found. Create your first pool to get started.</p>
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-2 xl:grid-cols-3">
          {nodeEntries.map(([nodeAddr, nodePools]) => {
            const nodeName = nodeMap[nodeAddr] || nodeAddr;
            return (
              <Card key={nodeAddr}>
                <CardHeader>
                  <CardTitle className="flex items-center gap-3">
                    <span className="flex h-10 w-10 items-center justify-center rounded-full bg-primary/10">
                      <Server className="h-5 w-5 text-primary" />
                    </span>
                    <span className="flex flex-col">
                      <span>{nodeName}</span>
                      <span className="text-xs font-normal text-muted-foreground">
                        {nodeAddr} &bull; {nodePools.length} pool
                        {nodePools.length > 1 ? 's' : ''}
                      </span>
                    </span>
                  </CardTitle>
                </CardHeader>
                <CardContent className="space-y-3">
                  {nodePools.map((pool) => (
                    <PoolItem key={`${pool.node}-${pool.name}`} pool={pool} />
                  ))}
                </CardContent>
              </Card>
            );
          })}
        </div>
      )}
    </div>
  );
}

function PoolItem({ pool }: { pool: Pool }) {
  const total = Number(pool.totalGb);
  const free = Number(pool.freeGb);
  const usedPercent = total > 0 ? ((total - free) / total) * 100 : 0;

  return (
    <div className="rounded-lg border bg-muted/40 p-3">
      <div className="mb-2 flex items-center justify-between">
        <span className="text-sm font-medium">{pool.name}</span>
        <div className="flex items-center gap-1">
          <Badge variant="outline" className={poolTypeBadgeClass(pool.type)}>
            {poolTypeLabel(pool.type)}
          </Badge>
          <AddDiskDialog pool={pool} />
          <DeletePoolDialog pool={pool} />
        </div>
      </div>

      <div className="mb-1 flex justify-between text-xs text-muted-foreground">
        <span>{free} GB free</span>
        <span>{total} GB total</span>
      </div>
      <Progress value={usedPercent} className="h-2" />
    </div>
  );
}

function CreatePoolDialog({
  nodes,
}: {
  nodes: Array<{ name: string; address: string }>;
}) {
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState('');
  const [type, setType] = useState('vg');
  const [node, setNode] = useState('');
  const [disks, setDisks] = useState('');

  const createMutation = useMutation({
    mutationFn: (data: {
      name: string;
      type: string;
      node: string;
      disks: string[];
    }) => api.createPool(data),
    onSuccess: () => {
      toast.success(`Pool "${name}" created`);
      queryClient.invalidateQueries({ queryKey: ['pools'] });
      setOpen(false);
      setName('');
      setType('vg');
      setNode('');
      setDisks('');
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const diskList = disks
      .split(',')
      .map((d) => d.trim())
      .filter((d) => d);
    createMutation.mutate({ name, type, node, disks: diskList });
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button>
          <Plus className="h-4 w-4" />
          Create Pool
        </Button>
      </DialogTrigger>
      <DialogContent>
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>Create Storage Pool</DialogTitle>
            <DialogDescription>
              Create a new storage pool on a node from one or more disks.
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label htmlFor="pool-name">Pool Name</Label>
              <Input
                id="pool-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="e.g., data"
                required
              />
              <p className="text-xs text-muted-foreground">
                Will be prefixed with "sds_"
              </p>
            </div>

            <div className="space-y-2">
              <Label>Pool Type</Label>
              <Select value={type} onValueChange={setType}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {POOL_TYPE_OPTIONS.map((opt) => (
                    <SelectItem key={opt.value} value={opt.value}>
                      {opt.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-2">
              <Label>Node</Label>
              <Select value={node} onValueChange={setNode} required>
                <SelectTrigger>
                  <SelectValue placeholder="Select a node..." />
                </SelectTrigger>
                <SelectContent>
                  {nodes.map((n) => (
                    <SelectItem key={n.name} value={n.name}>
                      {n.name} ({n.address})
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-2">
              <Label htmlFor="pool-disks">Disks</Label>
              <Input
                id="pool-disks"
                value={disks}
                onChange={(e) => setDisks(e.target.value)}
                placeholder="/dev/sdc, /dev/sdd"
                required
              />
              <p className="text-xs text-muted-foreground">
                Comma-separated disk paths, e.g. /dev/sdc, /dev/sdd
              </p>
            </div>
          </div>

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => setOpen(false)}
              disabled={createMutation.isPending}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={createMutation.isPending || !node}>
              {createMutation.isPending && (
                <Loader2 className="h-4 w-4 animate-spin" />
              )}
              Create
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function AddDiskDialog({ pool }: { pool: Pool }) {
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [disk, setDisk] = useState('');

  const addDiskMutation = useMutation({
    mutationFn: (d: string) => api.addDisk(pool.name, d, pool.node),
    onSuccess: () => {
      toast.success(`Disk added to "${pool.name}"`);
      queryClient.invalidateQueries({ queryKey: ['pools'] });
      setOpen(false);
      setDisk('');
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    addDiskMutation.mutate(disk);
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="h-7 w-7 text-muted-foreground hover:text-primary"
          title="Add disk"
        >
          <PlusCircle className="h-4 w-4" />
        </Button>
      </DialogTrigger>
      <DialogContent>
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>Add Disk to Pool</DialogTitle>
            <DialogDescription>
              Extend pool "{pool.name}" on {pool.node} with an additional disk.
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label>Pool</Label>
              <Input value={pool.name} disabled />
            </div>
            <div className="space-y-2">
              <Label htmlFor="add-disk-path">Disk Path</Label>
              <Input
                id="add-disk-path"
                value={disk}
                onChange={(e) => setDisk(e.target.value)}
                placeholder="/dev/sdd"
                required
              />
            </div>
          </div>

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => setOpen(false)}
              disabled={addDiskMutation.isPending}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={addDiskMutation.isPending}>
              {addDiskMutation.isPending && (
                <Loader2 className="h-4 w-4 animate-spin" />
              )}
              Add Disk
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function DeletePoolDialog({ pool }: { pool: Pool }) {
  const queryClient = useQueryClient();

  const deleteMutation = useMutation({
    mutationFn: () => api.deletePool(pool.name, pool.node),
    onSuccess: () => {
      toast.success(`Pool "${pool.name}" deleted`);
      queryClient.invalidateQueries({ queryKey: ['pools'] });
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="h-7 w-7 text-muted-foreground hover:text-destructive"
          title="Delete pool"
        >
          <Trash2 className="h-4 w-4" />
        </Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete pool "{pool.name}"?</AlertDialogTitle>
          <AlertDialogDescription>
            This removes the pool from node {pool.node}. Any storage capacity it
            provides will no longer be available for new resources. This action
            cannot be undone.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            onClick={() => deleteMutation.mutate()}
            className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
          >
            <HardDrive className="h-4 w-4" />
            Delete
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
