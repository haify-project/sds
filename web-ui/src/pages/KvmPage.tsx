import { useQuery } from '@tanstack/react-query';
import { api } from '../services/api';
import { PageHeader } from '@/components/PageHeader';
import { Card, CardContent } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { Cpu } from 'lucide-react';
import { libvirtGuestsOf } from './guests/libvirt';
import { GuestsView } from './guests/GuestsView';

// libvirt guests whose disks are on Haify, found by the label the libvirt hook
// and `ha create --vm` put on their resources (guests/libvirt.ts).
export function KvmPage() {
  const { data: resources, isLoading } = useQuery({
    queryKey: ['resources'],
    queryFn: () => api.getResources(),
  });
  const { data: ha } = useQuery({ queryKey: ['ha'], queryFn: () => api.getHaConfigs() });
  const haResources = new Set((ha?.configs ?? []).map((c) => c.resource));
  const guests = libvirtGuestsOf(resources?.resources ?? [], haResources);

  return (
    <div>
      <PageHeader
        title="KVM"
        description="libvirt guests on Haify: the nodes holding each disk's replicas, and the node each guest runs on."
      />
      {isLoading ? (
        <div className="space-y-3">
          <Skeleton className="h-[106px] w-full" />
          <Skeleton className="h-64 w-full" />
        </div>
      ) : !guests.length ? (
        <Card>
          <CardContent className="flex flex-col items-center justify-center gap-3 py-16 text-center">
            <Cpu className="h-8 w-8 text-muted-foreground" />
            <p className="max-w-md text-sm text-muted-foreground">
              No libvirt guests on Haify yet. Install the hook on each KVM host, then start a guest whose disk is
              /dev/drbd/by-res/&lt;resource&gt;/0:
            </p>
            <code className="rounded bg-muted px-3 py-2 font-mono text-xs">
              ./deploy/libvirt/install.sh --controller &lt;controller address&gt;
            </code>
          </CardContent>
        </Card>
      ) : (
        <GuestsView guests={guests} />
      )}
    </div>
  );
}
