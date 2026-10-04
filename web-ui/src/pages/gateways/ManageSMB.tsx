import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api, SMBShare } from '@/services/api';
import { toast } from 'sonner';
import { Plus, Loader2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Skeleton } from '@/components/ui/skeleton';
import { Mono } from './GatewayDetailPanels';
import { QueryError, EmptyRow, RemoveButton } from './ManageControls';

// ---------- SMB Management ----------
//
// Shares and users live on the gateway's state volume and are changed on the
// node serving it, live: smbd re-reads its shares without dropping sessions.
// A stopped gateway answers with an error saying so.

export function ManageSMB({ resource }: { resource: string }) {
  return (
    <div className="space-y-6">
      <SMBShares resource={resource} />
      <SMBUsers resource={resource} />
    </div>
  );
}

function SMBShares({ resource }: { resource: string }) {
  const queryClient = useQueryClient();
  const queryKey = ['smb-shares', resource];
  const { data, isLoading, error } = useQuery({
    queryKey,
    queryFn: () => api.listSMBShares(resource),
    retry: false,
  });
  const [name, setName] = useState('');
  const [path, setPath] = useState('');
  const [users, setUsers] = useState('');
  const invalidate = () => queryClient.invalidateQueries({ queryKey });

  const add = useMutation({
    mutationFn: () =>
      api.addSMBShare(resource, {
        name,
        path: path || undefined,
        validUsers: users ? users.split(',').map((u) => u.trim()).filter(Boolean) : undefined,
      }),
    onSuccess: () => {
      toast.success('Share added');
      setName('');
      setPath('');
      setUsers('');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });
  const remove = useMutation({
    mutationFn: (share: string) => api.removeSMBShare(resource, share),
    onSuccess: () => {
      toast.success('Share removed; its data was left in place');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const shares: SMBShare[] = data?.shares ?? [];
  return (
    <div className="space-y-3">
      <p className="text-sm font-medium">Shares</p>
      {error ? (
        <QueryError message={(error as Error).message} />
      ) : isLoading ? (
        <Skeleton className="h-24 w-full" />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Path</TableHead>
              <TableHead>Users</TableHead>
              <TableHead className="text-right">Action</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {shares.length === 0 ? (
              <EmptyRow colSpan={4} label="No shares" />
            ) : (
              shares.map((sh) => (
                <TableRow key={sh.name}>
                  <TableCell>
                    <Mono value={sh.name} className="text-xs" />
                    {sh.readOnly && <span className="ml-2 text-xs text-muted-foreground">read-only</span>}
                  </TableCell>
                  <TableCell>
                    <Mono value={'/' + (sh.path ?? '')} className="text-xs" />
                  </TableCell>
                  <TableCell className="text-xs">
                    {sh.validUsers?.length ? sh.validUsers.join(', ') : 'all users'}
                  </TableCell>
                  <TableCell className="text-right">
                    <RemoveButton
                      title="Remove share?"
                      description={`Remove SMB share "${sh.name}"? Its files stay on the volume.`}
                      onConfirm={() => remove.mutate(sh.name)}
                    />
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      )}
      <form
        className="grid grid-cols-3 gap-3 rounded-lg border border-border p-3"
        onSubmit={(e) => {
          e.preventDefault();
          add.mutate();
        }}
      >
        <div className="space-y-1.5">
          <Label>Name</Label>
          <Input className="font-mono" value={name} onChange={(e) => setName(e.target.value)} required />
        </div>
        <div className="space-y-1.5">
          <Label>Path (under the volume)</Label>
          <Input className="font-mono" value={path} onChange={(e) => setPath(e.target.value)} placeholder="projects/a" />
        </div>
        <div className="space-y-1.5">
          <Label>Users (optional)</Label>
          <Input className="font-mono" value={users} onChange={(e) => setUsers(e.target.value)} placeholder="alice,bob" />
        </div>
        <Button type="submit" size="sm" className="w-fit" disabled={add.isPending || !name}>
          {add.isPending ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <Plus className="mr-2 h-4 w-4" />}
          Add Share
        </Button>
      </form>
    </div>
  );
}

function SMBUsers({ resource }: { resource: string }) {
  const queryClient = useQueryClient();
  const queryKey = ['smb-users', resource];
  const { data, isLoading, error } = useQuery({
    queryKey,
    queryFn: () => api.listSMBUsers(resource),
    retry: false,
  });
  const [user, setUser] = useState('');
  const [password, setPassword] = useState('');
  const invalidate = () => queryClient.invalidateQueries({ queryKey });

  const set = useMutation({
    mutationFn: () => api.setSMBUser(resource, user, password),
    onSuccess: () => {
      toast.success(`User ${user} set`);
      setUser('');
      setPassword('');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });
  const remove = useMutation({
    mutationFn: (u: string) => api.removeSMBUser(resource, u),
    onSuccess: () => {
      toast.success('User removed');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const list = data?.users ?? [];
  return (
    <div className="space-y-3">
      <p className="text-sm font-medium">Users</p>
      {error ? (
        <QueryError message={(error as Error).message} />
      ) : isLoading ? (
        <Skeleton className="h-16 w-full" />
      ) : (
        <div className="flex flex-wrap gap-2">
          {list.length === 0 && <span className="text-xs text-muted-foreground">No users yet</span>}
          {list.map((u) => (
            <span key={u} className="flex items-center gap-1 rounded border border-border px-2 py-0.5 text-xs">
              <Mono value={u} />
              <RemoveButton
                title="Remove user?"
                description={`Remove SMB user "${u}"?`}
                onConfirm={() => remove.mutate(u)}
              />
            </span>
          ))}
        </div>
      )}
      <form
        className="grid grid-cols-3 items-end gap-3 rounded-lg border border-border p-3"
        onSubmit={(e) => {
          e.preventDefault();
          set.mutate();
        }}
      >
        <div className="space-y-1.5">
          <Label>User</Label>
          <Input className="font-mono" value={user} onChange={(e) => setUser(e.target.value)} required />
        </div>
        <div className="space-y-1.5">
          <Label>Password</Label>
          <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} required />
        </div>
        <Button type="submit" size="sm" disabled={set.isPending || !user || !password}>
          {set.isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
          Add / set password
        </Button>
      </form>
    </div>
  );
}
