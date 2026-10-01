import { useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Layers3 } from 'lucide-react';
import { api } from '@/services/api';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { ProfileCard } from './profiles/ProfileCard';
import { ProfileDialog } from './profiles/ProfileDialog';

export function ResourceProfilesPage() {
  const { data, isLoading } = useQuery({
    queryKey: ['resource-profiles'],
    queryFn: () => api.getResourceProfiles(),
  });
  const { data: resources } = useQuery({
    queryKey: ['resources'],
    queryFn: () => api.getResources(),
  });
  const { data: nodeData } = useQuery({
    queryKey: ['nodes'],
    queryFn: () => api.getNodes(),
  });
  const nodeList = nodeData?.nodes ?? [];

  const usage = useMemo(() => {
    const counts: Record<string, number> = {};
    for (const resource of resources?.resources ?? []) {
      if (resource.profile) counts[resource.profile] = (counts[resource.profile] ?? 0) + 1;
    }
    return counts;
  }, [resources]);

  const profiles = [...(data?.profiles ?? [])].sort((a, b) => a.name.localeCompare(b.name));

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <h3 className="text-lg font-semibold">Resource Profiles</h3>
          <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
            Reusable creation defaults for placement, storage, DRBD behavior, and metadata.
            Profiles are resolved when a resource is created; later edits never reconfigure existing resources.
          </p>
        </div>
        <ProfileDialog />
      </div>

      {isLoading ? (
        <div className="grid gap-4 lg:grid-cols-2 xl:grid-cols-3">
          {[0, 1, 2].map((item) => (
            <Card key={item}>
              <CardHeader><Skeleton className="h-7 w-40" /></CardHeader>
              <CardContent className="space-y-3">
                <Skeleton className="h-16 w-full" />
                <Skeleton className="h-20 w-full" />
              </CardContent>
            </Card>
          ))}
        </div>
      ) : profiles.length === 0 ? (
        <Card className="border-dashed">
          <CardContent className="flex flex-col items-center gap-3 py-16 text-center">
            <Layers3 className="h-10 w-10 text-muted-foreground" />
            <div>
              <p className="font-medium">No resource profiles</p>
              <p className="mt-1 text-sm text-muted-foreground">
                Create a profile to standardize replica counts, pools, fault domains, and labels.
              </p>
            </div>
            <ProfileDialog />
          </CardContent>
        </Card>
      ) : (
        <div className="grid gap-4 lg:grid-cols-2 xl:grid-cols-3">
          {profiles.map((profile) => (
            <ProfileCard
              key={profile.name}
              profile={profile}
              resourceCount={usage[profile.name] ?? 0}
              nodes={nodeList}
            />
          ))}
        </div>
      )}
    </div>
  );
}
