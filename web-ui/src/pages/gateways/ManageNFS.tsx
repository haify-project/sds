import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api, NFSExport } from '@/services/api';
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

// ---------- NFS Management ----------

export function ManageNFS({ resource }: { resource: string }) {
  const queryClient = useQueryClient();
  const queryKey = ['nfs-exports', resource];
  const { data, isLoading, error } = useQuery({
    queryKey,
    queryFn: () => api.listNFSExports(resource),
    retry: false,
  });

  const [exportPath, setExportPath] = useState('');
  const [clientSpec, setClientSpec] = useState('');
  const [options, setOptions] = useState('');

  const invalidate = () => queryClient.invalidateQueries({ queryKey });

  const addMutation = useMutation({
    mutationFn: () =>
      api.addNFSExport({
        resource,
        exportPath,
        clientSpec: clientSpec || undefined,
        options: options || undefined,
      }),
    onSuccess: () => {
      toast.success('Export added');
      setExportPath('');
      setClientSpec('');
      setOptions('');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const removeMutation = useMutation({
    mutationFn: (path: string) => api.removeNFSExport(resource, path),
    onSuccess: () => {
      toast.success('Export removed');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const exports: NFSExport[] = data?.exports ?? [];

  return (
    <div className="space-y-4">
      {error ? (
        <QueryError message={(error as Error).message} />
      ) : isLoading ? (
        <Skeleton className="h-24 w-full" />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Directory</TableHead>
              <TableHead>FSID</TableHead>
              <TableHead>Client</TableHead>
              <TableHead>Options</TableHead>
              <TableHead className="text-right">Action</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {exports.length === 0 ? (
              <EmptyRow colSpan={5} label="No exports configured" />
            ) : (
              exports.map((exp) => (
                <TableRow key={exp.directory}>
                  <TableCell>
                    <Mono value={exp.directory} className="text-xs" />
                  </TableCell>
                  <TableCell>
                    <Mono value={exp.fsid} className="text-xs text-muted-foreground" />
                  </TableCell>
                  <TableCell>
                    <Mono value={exp.clientspec} className="text-xs" />
                  </TableCell>
                  <TableCell>
                    <Mono value={exp.options} className="text-xs" />
                  </TableCell>
                  <TableCell className="text-right">
                    <RemoveButton
                      title="Remove export?"
                      description={`Remove NFS export "${exp.directory}"?`}
                      onConfirm={() => removeMutation.mutate(exp.directory)}
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
        <p className="text-sm font-medium">Add Export</p>
        <div className="space-y-1.5">
          <Label>Export Path</Label>
          <Input
            className="font-mono"
            value={exportPath}
            onChange={(e) => setExportPath(e.target.value)}
            placeholder="/data/share"
            required
          />
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div className="space-y-1.5">
            <Label>Client Spec</Label>
            <Input
              className="font-mono"
              value={clientSpec}
              onChange={(e) => setClientSpec(e.target.value)}
              placeholder="192.168.1.0/24"
            />
          </div>
          <div className="space-y-1.5">
            <Label>Options</Label>
            <Input
              className="font-mono"
              value={options}
              onChange={(e) => setOptions(e.target.value)}
              placeholder="rw,sync,no_root_squash"
            />
          </div>
        </div>
        <Button
          type="submit"
          size="sm"
          disabled={addMutation.isPending || !exportPath}
        >
          {addMutation.isPending ? (
            <Loader2 className="mr-2 h-4 w-4 animate-spin" />
          ) : (
            <Plus className="mr-2 h-4 w-4" />
          )}
          Add Export
        </Button>
      </form>
    </div>
  );
}
