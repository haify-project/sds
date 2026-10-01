import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api, ISCSILUN } from '@/services/api';
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
import { Switch } from '@/components/ui/switch';
import { Skeleton } from '@/components/ui/skeleton';
import { Mono } from './GatewayDetailPanels';
import { QueryError, EmptyRow, RemoveButton } from './ManageControls';

// ---------- iSCSI Management ----------

export function ManageISCSI({ resource }: { resource: string }) {
  return (
    <Tabs defaultValue="luns">
      <TabsList className="grid w-full grid-cols-3">
        <TabsTrigger value="luns">LUNs</TabsTrigger>
        <TabsTrigger value="initiators">Initiators</TabsTrigger>
        <TabsTrigger value="chap">CHAP</TabsTrigger>
      </TabsList>
      <TabsContent value="luns">
        <ISCSILuns resource={resource} />
      </TabsContent>
      <TabsContent value="initiators">
        <ISCSIInitiators resource={resource} />
      </TabsContent>
      <TabsContent value="chap">
        <ISCSIChap resource={resource} />
      </TabsContent>
    </Tabs>
  );
}

function ISCSILuns({ resource }: { resource: string }) {
  const queryClient = useQueryClient();
  const queryKey = ['iscsi-luns', resource];
  const { data, isLoading, error } = useQuery({
    queryKey,
    queryFn: () => api.listISCSILUNs(resource),
    retry: false,
  });

  const [lun, setLun] = useState('');
  const [device, setDevice] = useState('');
  const invalidate = () => queryClient.invalidateQueries({ queryKey });

  const addMutation = useMutation({
    mutationFn: () =>
      api.addISCSILUN({ resource, lun: parseInt(lun, 10), device }),
    onSuccess: () => {
      toast.success('LUN added');
      setLun('');
      setDevice('');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const removeMutation = useMutation({
    mutationFn: (l: number) => api.removeISCSILUN(resource, l),
    onSuccess: () => {
      toast.success('LUN removed');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const luns: ISCSILUN[] = data?.luns ?? [];

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
              <TableHead>LUN</TableHead>
              <TableHead>Device</TableHead>
              <TableHead>Target IQN</TableHead>
              <TableHead className="text-right">Action</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {luns.length === 0 ? (
              <EmptyRow colSpan={4} label="No LUNs configured" />
            ) : (
              luns.map((l) => (
                <TableRow key={l.lun}>
                  <TableCell className="font-mono">{l.lun}</TableCell>
                  <TableCell>
                    <Mono value={l.device} className="text-xs" />
                  </TableCell>
                  <TableCell>
                    <Mono
                      value={l.targetIqn}
                      className="text-xs text-muted-foreground"
                    />
                  </TableCell>
                  <TableCell className="text-right">
                    <RemoveButton
                      title="Remove LUN?"
                      description={`Remove LUN ${l.lun}?`}
                      onConfirm={() => removeMutation.mutate(l.lun)}
                    />
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      )}

      <form
        className="space-y-3 rounded-lg border border-border p-3"
        onSubmit={(e) => {
          e.preventDefault();
          addMutation.mutate();
        }}
      >
        <p className="text-sm font-medium">Add LUN</p>
        <div className="grid grid-cols-2 gap-3">
          <div className="space-y-1.5">
            <Label>LUN Number</Label>
            <Input
              className="font-mono"
              type="number"
              value={lun}
              onChange={(e) => setLun(e.target.value)}
              placeholder="1"
              required
            />
          </div>
          <div className="space-y-1.5">
            <Label>Device</Label>
            <Input
              className="font-mono"
              value={device}
              onChange={(e) => setDevice(e.target.value)}
              placeholder="/dev/drbd1001"
              required
            />
          </div>
        </div>
        <Button
          type="submit"
          size="sm"
          disabled={addMutation.isPending || !lun || !device}
        >
          {addMutation.isPending ? (
            <Loader2 className="mr-2 h-4 w-4 animate-spin" />
          ) : (
            <Plus className="mr-2 h-4 w-4" />
          )}
          Add LUN
        </Button>
      </form>
    </div>
  );
}

function ISCSIInitiators({ resource }: { resource: string }) {
  const queryClient = useQueryClient();
  const queryKey = ['iscsi-initiators', resource];
  const { data, isLoading, error } = useQuery({
    queryKey,
    queryFn: () => api.listISCSIInitiators(resource),
    retry: false,
  });

  const [initiator, setInitiator] = useState('');
  const invalidate = () => queryClient.invalidateQueries({ queryKey });

  const addMutation = useMutation({
    mutationFn: () => api.addISCSIInitiator(resource, initiator),
    onSuccess: () => {
      toast.success('Initiator added');
      setInitiator('');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const removeMutation = useMutation({
    mutationFn: (iqn: string) => api.removeISCSIInitiator(resource, iqn),
    onSuccess: () => {
      toast.success('Initiator removed');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const initiators = data?.initiators ?? [];

  return (
    <div className="space-y-4 pt-2">
      {error ? (
        <QueryError message={(error as Error).message} />
      ) : isLoading ? (
        <Skeleton className="h-12 w-full" />
      ) : initiators.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          No allowed initiators configured.
        </p>
      ) : (
        <div className="flex flex-wrap gap-2">
          {initiators.map((iqn) => (
            <Badge
              key={iqn}
              variant="secondary"
              className="max-w-[320px] gap-1 font-mono"
              title={iqn}
            >
              <span className="truncate">{iqn}</span>
              <button
                type="button"
                aria-label={`Remove initiator ${iqn}`}
                onClick={() => removeMutation.mutate(iqn)}
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
          <Label>Initiator IQN</Label>
          <Input
            className="font-mono"
            value={initiator}
            onChange={(e) => setInitiator(e.target.value)}
            placeholder="iqn.1994-05.com.redhat:..."
            required
          />
        </div>
        <Button type="submit" disabled={addMutation.isPending || !initiator}>
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

function ISCSIChap({ resource }: { resource: string }) {
  const queryClient = useQueryClient();
  const queryKey = ['iscsi-chap', resource];
  const { data, isLoading, error } = useQuery({
    queryKey,
    queryFn: () => api.getISCSIChap(resource),
    retry: false,
  });

  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [mutual, setMutual] = useState(false);
  const [loaded, setLoaded] = useState(false);

  if (data && !loaded) {
    setUsername(data.username ?? '');
    setPassword(data.password ?? '');
    setMutual(!!data.mutual);
    setLoaded(true);
  }

  const setMutation = useMutation({
    mutationFn: () =>
      api.setISCSIChap({ resource, username, password, mutual }),
    onSuccess: () => {
      toast.success('CHAP credentials updated');
      queryClient.invalidateQueries({ queryKey });
    },
    onError: (e: Error) => toast.error(e.message),
  });

  if (error) {
    return (
      <div className="pt-2">
        <QueryError message={(error as Error).message} />
      </div>
    );
  }
  if (isLoading) {
    return <Skeleton className="mt-2 h-32 w-full" />;
  }

  return (
    <form
      className="space-y-4 pt-2"
      onSubmit={(e) => {
        e.preventDefault();
        setMutation.mutate();
      }}
    >
      <div className="space-y-1.5">
        <Label>Username</Label>
        <Input
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          autoComplete="off"
        />
      </div>
      <div className="space-y-1.5">
        <Label>Password</Label>
        <Input
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoComplete="new-password"
        />
      </div>
      <div className="flex items-center justify-between rounded-lg border border-border p-3">
        <div>
          <Label>Mutual CHAP</Label>
          <p className="text-xs text-muted-foreground">
            Require the target to authenticate to the initiator too.
          </p>
        </div>
        <Switch checked={mutual} onCheckedChange={setMutual} />
      </div>
      <Button type="submit" disabled={setMutation.isPending}>
        {setMutation.isPending && (
          <Loader2 className="mr-2 h-4 w-4 animate-spin" />
        )}
        Save CHAP
      </Button>
    </form>
  );
}
