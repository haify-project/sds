import { useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { api, Resource } from '@/services/api';
import { toast } from 'sonner';
import { Loader2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { ResourceSelect } from './CreateGatewayDialog';

// An SMB gateway: a standalone Samba server (workgroup, local users) on the
// service IP. Users are added from the manage dialog once it runs.
export function CreateSMBForm({
  resources,
  onCreated,
}: {
  resources: Resource[];
  onCreated: () => void;
}) {
  const [resource, setResource] = useState('');
  const [serviceIp, setServiceIp] = useState('');
  const [workgroup, setWorkgroup] = useState('');
  const [shareName, setShareName] = useState('');

  const mutation = useMutation({
    mutationFn: () =>
      api.createSMBGateway({
        resource,
        serviceIp,
        workgroup: workgroup || undefined,
        shareName: shareName || undefined,
      }),
    onSuccess: () => {
      toast.success('SMB gateway created; add users from Manage');
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
      <ResourceSelect resources={resources} value={resource} onChange={setResource} />
      <div className="space-y-1.5">
        <Label>Service IP (CIDR)</Label>
        <Input
          className="font-mono"
          value={serviceIp}
          onChange={(e) => setServiceIp(e.target.value)}
          placeholder="192.168.1.210/24"
          required
        />
      </div>
      <div className="grid grid-cols-2 gap-3">
        <div className="space-y-1.5">
          <Label>Workgroup</Label>
          <Input
            className="font-mono"
            value={workgroup}
            onChange={(e) => setWorkgroup(e.target.value)}
            placeholder="WORKGROUP"
          />
        </div>
        <div className="space-y-1.5">
          <Label>First share</Label>
          <Input
            className="font-mono"
            value={shareName}
            onChange={(e) => setShareName(e.target.value)}
            placeholder={resource || 'resource name'}
          />
        </div>
      </div>
      <p className="text-xs text-muted-foreground">
        Sessions do not survive a failover: clients reconnect to the service IP.
      </p>
      <Button type="submit" disabled={mutation.isPending || !resource || !serviceIp}>
        {mutation.isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
        Create SMB gateway
      </Button>
    </form>
  );
}
