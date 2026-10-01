import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api, NVMeNamespace } from '@/services/api';
import { toast } from 'sonner';
import { Plus, Loader2, X } from 'lucide-react';
import { Button } from '@/components/ui/button';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Badge } from '@/components/ui/badge';
import { Skeleton } from '@/components/ui/skeleton';
import { Mono } from './GatewayDetailPanels';
import { QueryError, EmptyRow, RemoveButton } from './ManageControls';

// ---------- NVMe Management ----------

export function ManageNVMe({ resource }: { resource: string }) {
  return (
    <Tabs defaultValue="namespaces">
      <TabsList className="grid w-full grid-cols-2">
        <TabsTrigger value="namespaces">Namespaces</TabsTrigger>
        <TabsTrigger value="hosts">Allowed Hosts</TabsTrigger>
      </TabsList>
      <TabsContent value="namespaces">
        <NVMeNamespaces resource={resource} />
      </TabsContent>
      <TabsContent value="hosts">
        <NVMeHosts resource={resource} />
      </TabsContent>
    </Tabs>
  );
}

function NVMeNamespaces({ resource }: { resource: string }) {
  const queryClient = useQueryClient();
  const queryKey = ['nvme-namespaces', resource];
  const { data, isLoading, error } = useQuery({
    queryKey,
    queryFn: () => api.listNVMeNamespaces(resource),
    retry: false,
  });

  const [device, setDevice] = useState('');
  const invalidate = () => queryClient.invalidateQueries({ queryKey });

  const addMutation = useMutation({
    mutationFn: () => api.addNVMeNamespace(resource, device),
    onSuccess: () => {
      toast.success('Namespace added');
      setDevice('');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const removeMutation = useMutation({
    mutationFn: (id: number) => api.removeNVMeNamespace(resource, id),
    onSuccess: () => {
      toast.success('Namespace removed');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const namespaces: NVMeNamespace[] = data?.namespaces ?? [];

  return (
    <div className="space-y-4 pt-2">
      {error ? (
        <QueryError message={(error as Error).message} />
      ) : isLoading ? (
        <Skeleton className="h-24 w-full" />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>NSID</TableHead>
              <TableHead>Backing Path</TableHead>
              <TableHead>UUID</TableHead>
              <TableHead className="text-right">Action</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {namespaces.length === 0 ? (
              <EmptyRow colSpan={4} label="No namespaces configured" />
            ) : (
              namespaces.map((ns) => (
                <TableRow key={ns.namespaceId}>
                  <TableCell className="font-mono">{ns.namespaceId}</TableCell>
                  <TableCell>
                    <Mono value={ns.backingPath} className="text-xs" />
                  </TableCell>
                  <TableCell>
                    <Mono
                      value={ns.uuid}
                      className="text-xs text-muted-foreground"
                    />
                  </TableCell>
                  <TableCell className="text-right">
                    <RemoveButton
                      title="Remove namespace?"
                      description={`Remove namespace ${ns.namespaceId}?`}
                      onConfirm={() => removeMutation.mutate(ns.namespaceId)}
                    />
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      )}

      <form
        className="flex items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          addMutation.mutate();
        }}
      >
        <div className="flex-1 space-y-1.5">
          <Label>Device Path</Label>
          <Input
            className="font-mono"
            value={device}
            onChange={(e) => setDevice(e.target.value)}
            placeholder="/dev/drbd1001"
            required
          />
        </div>
        <Button type="submit" disabled={addMutation.isPending || !device}>
          {addMutation.isPending ? (
            <Loader2 className="h-4 w-4 animate-spin" />
          ) : (
            <Plus className="h-4 w-4" />
          )}
        </Button>
      </form>
    </div>
  );
}

function NVMeHosts({ resource }: { resource: string }) {
  const queryClient = useQueryClient();
  const queryKey = ['nvme-hosts', resource];
  const { data, isLoading, error } = useQuery({
    queryKey,
    queryFn: () => api.listNVMeHosts(resource),
    retry: false,
  });

  const [hostNqn, setHostNqn] = useState('');
  const invalidate = () => queryClient.invalidateQueries({ queryKey });

  const addMutation = useMutation({
    mutationFn: () => api.addNVMeHost(resource, hostNqn),
    onSuccess: () => {
      toast.success('Host added');
      setHostNqn('');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const removeMutation = useMutation({
    mutationFn: (nqn: string) => api.removeNVMeHost(resource, nqn),
    onSuccess: () => {
      toast.success('Host removed');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const hosts = data?.hosts ?? [];

  return (
    <div className="space-y-4 pt-2">
      {error ? (
        <QueryError message={(error as Error).message} />
      ) : isLoading ? (
        <Skeleton className="h-12 w-full" />
      ) : hosts.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          No allowed hosts configured.
        </p>
      ) : (
        <div className="flex flex-wrap gap-2">
          {hosts.map((nqn) => (
            <Badge
              key={nqn}
              variant="secondary"
              className="max-w-[320px] gap-1 font-mono"
              title={nqn}
            >
              <span className="truncate">{nqn}</span>
              <button
                type="button"
                aria-label={`Remove host ${nqn}`}
                onClick={() => removeMutation.mutate(nqn)}
                className="ml-1 shrink-0 rounded-full hover:text-destructive"
              >
                <X className="h-3 w-3" />
              </button>
            </Badge>
          ))}
        </div>
      )}

      <form
        className="flex items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          addMutation.mutate();
        }}
      >
        <div className="flex-1 space-y-1.5">
          <Label>Host NQN</Label>
          <Input
            className="font-mono"
            value={hostNqn}
            onChange={(e) => setHostNqn(e.target.value)}
            placeholder="nqn.2014-08.org.nvmexpress:uuid:..."
            required
          />
        </div>
        <Button type="submit" disabled={addMutation.isPending || !hostNqn}>
          {addMutation.isPending ? (
            <Loader2 className="h-4 w-4 animate-spin" />
          ) : (
            <Plus className="h-4 w-4" />
          )}
        </Button>
      </form>
    </div>
  );
}
