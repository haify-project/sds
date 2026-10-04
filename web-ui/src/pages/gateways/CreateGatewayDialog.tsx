import { useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { api, Resource } from '@/services/api';
import { toast } from 'sonner';
import { Loader2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { type GwKind } from './protocol';
import { CreateSMBForm } from './CreateSMBForm';

// ==================== Create Dialog ====================

export function CreateGatewayDialog({
  type,
  onTypeChange,
  resources,
  onCreated,
}: {
  type: GwKind | null;
  onTypeChange: (type: GwKind | null) => void;
  resources: Resource[];
  onCreated: () => void;
}) {
  return (
    <Dialog open={!!type} onOpenChange={(open) => !open && onTypeChange(null)}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Create Gateway</DialogTitle>
          <DialogDescription>
            Expose a DRBD resource via NFS, iSCSI, NVMe-oF or SMB.
          </DialogDescription>
        </DialogHeader>
        <Tabs
          value={type ?? 'nfs'}
          onValueChange={(v) => onTypeChange(v as GwKind)}
        >
          <TabsList className="grid w-full grid-cols-4">
            <TabsTrigger value="nfs">NFS</TabsTrigger>
            <TabsTrigger value="iscsi">iSCSI</TabsTrigger>
            <TabsTrigger value="nvme">NVMe</TabsTrigger>
            <TabsTrigger value="smb">SMB</TabsTrigger>
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
          <TabsContent value="smb">
            <CreateSMBForm resources={resources} onCreated={onCreated} />
          </TabsContent>
        </Tabs>
      </DialogContent>
    </Dialog>
  );
}

export function ResourceSelect({
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
            <SelectItem key={r.name} value={r.name} className="font-mono">
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
          className="font-mono"
          value={serviceIp}
          onChange={(e) => setServiceIp(e.target.value)}
          placeholder="192.168.1.200/24"
          required
        />
      </div>
      <div className="space-y-1.5">
        <Label>Export Path</Label>
        <Input
          className="font-mono"
          value={exportPath}
          onChange={(e) => setExportPath(e.target.value)}
          placeholder="/data"
          required
        />
      </div>
      <div className="space-y-1.5">
        <Label>Allowed IPs (comma-separated, optional)</Label>
        <Input
          className="font-mono"
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
          className="font-mono"
          value={serviceIp}
          onChange={(e) => setServiceIp(e.target.value)}
          placeholder="192.168.1.100/24"
          required
        />
      </div>
      <div className="space-y-1.5">
        <Label>IQN</Label>
        <Input
          className="font-mono"
          value={iqn}
          onChange={(e) => setIqn(e.target.value)}
          placeholder="iqn.2024-01.com.example:sds.data"
          required
        />
      </div>
      <div className="space-y-1.5">
        <Label>Allowed Initiators (comma-separated, optional)</Label>
        <Input
          className="font-mono"
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
          className="font-mono"
          value={serviceIp}
          onChange={(e) => setServiceIp(e.target.value)}
          placeholder="192.168.1.150/24"
          required
        />
      </div>
      <div className="space-y-1.5">
        <Label>NQN</Label>
        <Input
          className="font-mono"
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
