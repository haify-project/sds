import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  api,
  Gateway,
  Resource,
  NFSExport,
  ISCSILUN,
  NVMeNamespace,
} from '@/services/api';
import { StatusBadge } from '@/components/StatusBadge';
import { toast } from 'sonner';
import {
  Network,
  Plus,
  Play,
  Square,
  Trash2,
  Info,
  Settings2,
  Loader2,
  X,
  AlertCircle,
  Inbox,
} from 'lucide-react';
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
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
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Badge } from '@/components/ui/badge';
import { Switch } from '@/components/ui/switch';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';

export function GatewaysPage() {
  const queryClient = useQueryClient();
  const { data: gateways, isLoading } = useQuery({
    queryKey: ['gateways'],
    queryFn: () => api.getGateways(),
  });
  const { data: resources } = useQuery({
    queryKey: ['resources'],
    queryFn: () => api.getResources(),
  });

  const [createOpen, setCreateOpen] = useState(false);
  const [detailsGateway, setDetailsGateway] = useState<Gateway | null>(null);
  const [manageGateway, setManageGateway] = useState<Gateway | null>(null);

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ['gateways'] });

  const startMutation = useMutation({
    mutationFn: (id: string) => api.startGateway(id),
    onSuccess: () => {
      toast.success('Gateway started');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const stopMutation = useMutation({
    mutationFn: (id: string) => api.stopGateway(id),
    onSuccess: () => {
      toast.success('Gateway stopped');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.deleteGateway(id),
    onSuccess: () => {
      toast.success('Gateway deleted');
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const showDetails = async (gateway: Gateway) => {
    try {
      const data = await api.getGateway(gateway.id);
      setDetailsGateway(data.gateway);
    } catch (e) {
      toast.error((e as Error).message);
    }
  };

  const list = gateways?.gateways ?? [];

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h3 className="text-lg font-semibold">Storage Gateways</h3>
          <p className="text-sm text-muted-foreground">
            Export DRBD resources over NFS, iSCSI and NVMe-oF.
          </p>
        </div>
        <Button onClick={() => setCreateOpen(true)}>
          <Plus className="mr-2 h-4 w-4" />
          Create Gateway
        </Button>
      </div>

      <Card>
        <CardContent className="pt-6">
          {isLoading ? (
            <div className="space-y-2">
              {Array.from({ length: 4 }).map((_, i) => (
                <Skeleton key={i} className="h-12 w-full" />
              ))}
            </div>
          ) : list.length === 0 ? (
            <div className="flex flex-col items-center justify-center gap-2 py-12 text-center">
              <Network className="h-8 w-8 text-muted-foreground" />
              <p className="text-sm text-muted-foreground">
                No gateways yet. Create a gateway to expose your storage.
              </p>
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Type</TableHead>
                  <TableHead>Resource</TableHead>
                  <TableHead>State</TableHead>
                  <TableHead>Node</TableHead>
                  <TableHead className="text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {list.map((gateway) => {
                  const isRunning = gateway.state === 'running';
                  return (
                    <TableRow key={gateway.id}>
                      <TableCell className="font-medium">
                        <div className="flex items-center gap-2">
                          <Network className="h-4 w-4 text-muted-foreground" />
                          {gateway.name || gateway.id}
                        </div>
                      </TableCell>
                      <TableCell>
                        <Badge variant="secondary">
                          {gateway.type.toUpperCase()}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-muted-foreground">
                        {gateway.resource}
                      </TableCell>
                      <TableCell>
                        <StatusBadge status={gateway.state || 'unknown'} />
                      </TableCell>
                      <TableCell className="text-muted-foreground">
                        {gateway.node || '-'}
                      </TableCell>
                      <TableCell>
                        <div className="flex justify-end gap-1">
                          <Button
                            variant="outline"
                            size="sm"
                            onClick={() => setManageGateway(gateway)}
                          >
                            <Settings2 className="mr-1 h-3 w-3" />
                            Manage
                          </Button>
                          <Button
                            variant="outline"
                            size="sm"
                            onClick={() => showDetails(gateway)}
                          >
                            <Info className="h-3 w-3" />
                          </Button>
                          {isRunning ? (
                            <Button
                              variant="outline"
                              size="sm"
                              disabled={stopMutation.isPending}
                              onClick={() => stopMutation.mutate(gateway.id)}
                            >
                              <Square className="h-3 w-3" />
                            </Button>
                          ) : (
                            <Button
                              variant="outline"
                              size="sm"
                              disabled={startMutation.isPending}
                              onClick={() => startMutation.mutate(gateway.id)}
                            >
                              <Play className="h-3 w-3" />
                            </Button>
                          )}
                          <AlertDialog>
                            <AlertDialogTrigger asChild>
                              <Button variant="outline" size="sm">
                                <Trash2 className="h-3 w-3 text-destructive" />
                              </Button>
                            </AlertDialogTrigger>
                            <AlertDialogContent>
                              <AlertDialogHeader>
                                <AlertDialogTitle>
                                  Delete gateway?
                                </AlertDialogTitle>
                                <AlertDialogDescription>
                                  This removes the drbd-reactor config for
                                  gateway "{gateway.name || gateway.id}". The
                                  underlying DRBD resource is not affected.
                                </AlertDialogDescription>
                              </AlertDialogHeader>
                              <AlertDialogFooter>
                                <AlertDialogCancel>Cancel</AlertDialogCancel>
                                <AlertDialogAction
                                  onClick={() =>
                                    deleteMutation.mutate(gateway.id)
                                  }
                                >
                                  Delete
                                </AlertDialogAction>
                              </AlertDialogFooter>
                            </AlertDialogContent>
                          </AlertDialog>
                        </div>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <CreateGatewayDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        resources={resources?.resources ?? []}
        onCreated={() => {
          setCreateOpen(false);
          invalidate();
        }}
      />

      <DetailsDialog
        gateway={detailsGateway}
        onOpenChange={(open) => !open && setDetailsGateway(null)}
      />

      <ManageDialog
        gateway={manageGateway}
        onOpenChange={(open) => !open && setManageGateway(null)}
      />
    </div>
  );
}

// ==================== Create Dialog ====================

function CreateGatewayDialog({
  open,
  onOpenChange,
  resources,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  resources: Resource[];
  onCreated: () => void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Create Gateway</DialogTitle>
          <DialogDescription>
            Expose a DRBD resource via NFS, iSCSI or NVMe-oF.
          </DialogDescription>
        </DialogHeader>
        <Tabs defaultValue="nfs">
          <TabsList className="grid w-full grid-cols-3">
            <TabsTrigger value="nfs">NFS</TabsTrigger>
            <TabsTrigger value="iscsi">iSCSI</TabsTrigger>
            <TabsTrigger value="nvme">NVMe</TabsTrigger>
          </TabsList>
          <TabsContent value="nfs">
            <CreateNFSForm resources={resources} onCreated={onCreated} />
          </TabsContent>
          <TabsContent value="iscsi">
            <CreateISCSIForm resources={resources} onCreated={onCreated} />
          </TabsContent>
          <TabsContent value="nvme">
            <CreateNVMeForm resources={resources} onCreated={onCreated} />
          </TabsContent>
        </Tabs>
      </DialogContent>
    </Dialog>
  );
}

function ResourceSelect({
  resources,
  value,
  onChange,
}: {
  resources: Resource[];
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <div className="space-y-1.5">
      <Label>DRBD Resource</Label>
      <Select value={value} onValueChange={onChange}>
        <SelectTrigger className="w-full">
          <SelectValue placeholder="Select a resource..." />
        </SelectTrigger>
        <SelectContent>
          {resources.map((r) => (
            <SelectItem key={r.name} value={r.name}>
              {r.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}

function CreateNFSForm({
  resources,
  onCreated,
}: {
  resources: Resource[];
  onCreated: () => void;
}) {
  const [resource, setResource] = useState('');
  const [serviceIp, setServiceIp] = useState('');
  const [exportPath, setExportPath] = useState('');
  const [allowedIps, setAllowedIps] = useState('');
  const [fsType, setFsType] = useState('ext4');

  const mutation = useMutation({
    mutationFn: () =>
      api.createNFSGateway({
        resource,
        serviceIp,
        exportPath,
        allowedIps: allowedIps
          ? allowedIps.split(',').map((s) => s.trim()).filter(Boolean)
          : undefined,
        fsType,
      }),
    onSuccess: () => {
      toast.success('NFS gateway created');
      onCreated();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <form
      className="space-y-4 pt-2"
      onSubmit={(e) => {
        e.preventDefault();
        mutation.mutate();
      }}
    >
      <ResourceSelect
        resources={resources}
        value={resource}
        onChange={setResource}
      />
      <div className="space-y-1.5">
        <Label>Service IP (CIDR)</Label>
        <Input
          value={serviceIp}
          onChange={(e) => setServiceIp(e.target.value)}
          placeholder="192.168.1.200/24"
          required
        />
      </div>
      <div className="space-y-1.5">
        <Label>Export Path</Label>
        <Input
          value={exportPath}
          onChange={(e) => setExportPath(e.target.value)}
          placeholder="/data"
          required
        />
      </div>
      <div className="space-y-1.5">
        <Label>Allowed IPs (comma-separated, optional)</Label>
        <Input
          value={allowedIps}
          onChange={(e) => setAllowedIps(e.target.value)}
          placeholder="192.168.1.0/24, 10.0.0.0/8"
        />
      </div>
      <div className="space-y-1.5">
        <Label>Filesystem Type</Label>
        <Select value={fsType} onValueChange={setFsType}>
          <SelectTrigger className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="ext4">ext4</SelectItem>
            <SelectItem value="xfs">XFS</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <DialogFooter>
        <Button type="submit" disabled={mutation.isPending || !resource}>
          {mutation.isPending && (
            <Loader2 className="mr-2 h-4 w-4 animate-spin" />
          )}
          Create NFS Gateway
        </Button>
      </DialogFooter>
    </form>
  );
}

function CreateISCSIForm({
  resources,
  onCreated,
}: {
  resources: Resource[];
  onCreated: () => void;
}) {
  const [resource, setResource] = useState('');
  const [serviceIp, setServiceIp] = useState('');
  const [iqn, setIqn] = useState('');
  const [allowedInitiators, setAllowedInitiators] = useState('');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [implementation, setImplementation] = useState('lio');

  const mutation = useMutation({
    mutationFn: () =>
      api.createISCSIGateway({
        resource,
        serviceIp,
        iqn,
        allowedInitiators: allowedInitiators
          ? allowedInitiators.split(',').map((s) => s.trim()).filter(Boolean)
          : undefined,
        username: username || undefined,
        password: password || undefined,
        implementation,
      }),
    onSuccess: () => {
      toast.success('iSCSI gateway created');
      onCreated();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <form
      className="space-y-4 pt-2"
      onSubmit={(e) => {
        e.preventDefault();
        mutation.mutate();
      }}
    >
      <ResourceSelect
        resources={resources}
        value={resource}
        onChange={setResource}
      />
      <div className="space-y-1.5">
        <Label>Service IP (CIDR)</Label>
        <Input
          value={serviceIp}
          onChange={(e) => setServiceIp(e.target.value)}
          placeholder="192.168.1.100/24"
          required
        />
      </div>
      <div className="space-y-1.5">
        <Label>IQN</Label>
        <Input
          value={iqn}
          onChange={(e) => setIqn(e.target.value)}
          placeholder="iqn.2024-01.com.example:sds.data"
          required
        />
      </div>
      <div className="space-y-1.5">
        <Label>Allowed Initiators (comma-separated, optional)</Label>
        <Input
          value={allowedInitiators}
          onChange={(e) => setAllowedInitiators(e.target.value)}
          placeholder="iqn.1994-05.com.redhat:..."
        />
      </div>
      <div className="grid grid-cols-2 gap-4">
        <div className="space-y-1.5">
          <Label>CHAP Username (optional)</Label>
          <Input
            value={username}
            onChange={(e) => setUsername(e.target.value)}
          />
        </div>
        <div className="space-y-1.5">
          <Label>CHAP Password (optional)</Label>
          <Input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </div>
      </div>
      <div className="space-y-1.5">
        <Label>Implementation</Label>
        <Select value={implementation} onValueChange={setImplementation}>
          <SelectTrigger className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="lio">LIO (Linux IO)</SelectItem>
            <SelectItem value="tgt">TGT</SelectItem>
            <SelectItem value="iet">IET</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <DialogFooter>
        <Button type="submit" disabled={mutation.isPending || !resource}>
          {mutation.isPending && (
            <Loader2 className="mr-2 h-4 w-4 animate-spin" />
          )}
          Create iSCSI Gateway
        </Button>
      </DialogFooter>
    </form>
  );
}

function CreateNVMeForm({
  resources,
  onCreated,
}: {
  resources: Resource[];
  onCreated: () => void;
}) {
  const [resource, setResource] = useState('');
  const [serviceIp, setServiceIp] = useState('');
  const [nqn, setNqn] = useState('');
  const [transportType, setTransportType] = useState('tcp');

  const mutation = useMutation({
    mutationFn: () =>
      api.createNVMeGateway({ resource, serviceIp, nqn, transportType }),
    onSuccess: () => {
      toast.success('NVMe gateway created');
      onCreated();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <form
      className="space-y-4 pt-2"
      onSubmit={(e) => {
        e.preventDefault();
        mutation.mutate();
      }}
    >
      <ResourceSelect
        resources={resources}
        value={resource}
        onChange={setResource}
      />
      <div className="space-y-1.5">
        <Label>Service IP (CIDR)</Label>
        <Input
          value={serviceIp}
          onChange={(e) => setServiceIp(e.target.value)}
          placeholder="192.168.1.150/24"
          required
        />
      </div>
      <div className="space-y-1.5">
        <Label>NQN</Label>
        <Input
          value={nqn}
          onChange={(e) => setNqn(e.target.value)}
          placeholder="nqn.2024-01.com.example:sds.data"
          required
        />
      </div>
      <div className="space-y-1.5">
        <Label>Transport Type</Label>
        <Select value={transportType} onValueChange={setTransportType}>
          <SelectTrigger className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="tcp">TCP</SelectItem>
            <SelectItem value="rdma">RDMA</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <DialogFooter>
        <Button type="submit" disabled={mutation.isPending || !resource}>
          {mutation.isPending && (
            <Loader2 className="mr-2 h-4 w-4 animate-spin" />
          )}
          Create NVMe Gateway
        </Button>
      </DialogFooter>
    </form>
  );
}

// ==================== Details Dialog ====================

function DetailsDialog({
  gateway,
  onOpenChange,
}: {
  gateway: Gateway | null;
  onOpenChange: (open: boolean) => void;
}) {
  const rows = gateway
    ? [
        ['ID', gateway.id],
        ['Name', gateway.name],
        ['Type', gateway.type?.toUpperCase()],
        ['State', gateway.state],
        ['Resource', gateway.resource],
        ['Volume ID', String(gateway.volumeId)],
        ['Node', gateway.node || '-'],
        ['Path', gateway.path || '-'],
      ]
    : [];

  return (
    <Dialog open={!!gateway} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Gateway Details</DialogTitle>
        </DialogHeader>
        <div className="space-y-1 text-sm">
          {rows.map(([label, value]) => (
            <div
              key={label}
              className="flex justify-between border-b py-2 last:border-0"
            >
              <span className="text-muted-foreground">{label}</span>
              <span className="font-medium">{value || '-'}</span>
            </div>
          ))}
          {gateway?.options && Object.keys(gateway.options).length > 0 && (
            <div className="pt-2">
              <p className="mb-2 text-xs font-medium text-muted-foreground">
                Options
              </p>
              <div className="space-y-1">
                {Object.entries(gateway.options).map(([k, v]) => (
                  <div
                    key={k}
                    className="flex justify-between rounded bg-muted px-2 py-1 text-xs"
                  >
                    <span className="text-muted-foreground">{k}</span>
                    <span className="font-mono">{String(v)}</span>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

// ==================== Manage Dialog ====================

function ManageDialog({
  gateway,
  onOpenChange,
}: {
  gateway: Gateway | null;
  onOpenChange: (open: boolean) => void;
}) {
  return (
    <Dialog open={!!gateway} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>
            Manage {gateway?.type?.toUpperCase()} Gateway —{' '}
            {gateway?.name || gateway?.id}
          </DialogTitle>
          <DialogDescription>
            Resource: {gateway?.resource}
          </DialogDescription>
        </DialogHeader>
        {gateway?.type === 'nfs' && (
          <ManageNFS resource={gateway.resource} />
        )}
        {gateway?.type === 'iscsi' && (
          <ManageISCSI resource={gateway.resource} />
        )}
        {gateway?.type === 'nvme' && (
          <ManageNVMe resource={gateway.resource} />
        )}
      </DialogContent>
    </Dialog>
  );
}

function QueryError({ message }: { message: string }) {
  return (
    <div className="flex items-start gap-2 rounded-md border border-amber-200 bg-amber-50 p-3 text-sm text-amber-800 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-300">
      <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
      <span>{message}</span>
    </div>
  );
}

function EmptyRow({ colSpan, label }: { colSpan: number; label: string }) {
  return (
    <TableRow>
      <TableCell
        colSpan={colSpan}
        className="py-6 text-center text-muted-foreground"
      >
        <div className="flex flex-col items-center gap-1">
          <Inbox className="h-5 w-5" />
          <span className="text-xs">{label}</span>
        </div>
      </TableCell>
    </TableRow>
  );
}

// ---------- NFS Management ----------

function ManageNFS({ resource }: { resource: string }) {
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
                  <TableCell className="font-mono text-xs">
                    {exp.directory}
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {exp.fsid || '-'}
                  </TableCell>
                  <TableCell className="font-mono text-xs">
                    {exp.clientspec || '-'}
                  </TableCell>
                  <TableCell className="font-mono text-xs">
                    {exp.options || '-'}
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
        className="space-y-3 rounded-md border p-3"
        onSubmit={(e) => {
          e.preventDefault();
          addMutation.mutate();
        }}
      >
        <p className="text-sm font-medium">Add Export</p>
        <div className="space-y-1.5">
          <Label>Export Path</Label>
          <Input
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
              value={clientSpec}
              onChange={(e) => setClientSpec(e.target.value)}
              placeholder="192.168.1.0/24"
            />
          </div>
          <div className="space-y-1.5">
            <Label>Options</Label>
            <Input
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

// ---------- iSCSI Management ----------

function ManageISCSI({ resource }: { resource: string }) {
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
                  <TableCell>{l.lun}</TableCell>
                  <TableCell className="font-mono text-xs">
                    {l.device}
                  </TableCell>
                  <TableCell className="font-mono text-xs">
                    {l.targetIqn || '-'}
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
        className="space-y-3 rounded-md border p-3"
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
            <Badge key={iqn} variant="secondary" className="gap-1 font-mono">
              {iqn}
              <button
                type="button"
                onClick={() => removeMutation.mutate(iqn)}
                className="ml-1 rounded-full hover:text-destructive"
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
      <div className="flex items-center justify-between rounded-md border p-3">
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

// ---------- NVMe Management ----------

function ManageNVMe({ resource }: { resource: string }) {
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
                  <TableCell>{ns.namespaceId}</TableCell>
                  <TableCell className="font-mono text-xs">
                    {ns.backingPath}
                  </TableCell>
                  <TableCell className="font-mono text-xs">
                    {ns.uuid || '-'}
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
            <Badge key={nqn} variant="secondary" className="gap-1 font-mono">
              {nqn}
              <button
                type="button"
                onClick={() => removeMutation.mutate(nqn)}
                className="ml-1 rounded-full hover:text-destructive"
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

// ---------- Shared remove button ----------

function RemoveButton({
  title,
  description,
  onConfirm,
}: {
  title: string;
  description: string;
  onConfirm: () => void;
}) {
  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>
        <Button variant="ghost" size="sm">
          <Trash2 className="h-3 w-3 text-destructive" />
        </Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction onClick={onConfirm}>Remove</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
