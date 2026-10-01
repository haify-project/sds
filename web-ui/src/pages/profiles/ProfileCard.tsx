import { Boxes, Braces, Layers3, ShieldCheck } from 'lucide-react';
import { Node, ResourceProfile } from '@/services/api';
import { Badge } from '@/components/ui/badge';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { InstantiateProfileDialog } from './InstantiateProfileDialog';
import { ProfileDialog } from './ProfileDialog';
import { DeleteProfileDialog } from './DeleteProfileDialog';

export function ProfileCard({
  profile,
  resourceCount,
  nodes,
}: {
  profile: ResourceProfile;
  resourceCount: number;
  nodes: Node[];
}) {
  const placement = [
    ...(profile.replicasOnDifferent ?? []).map((key) => `spread:${key}`),
    ...(profile.replicasOnSame ?? []).map((key) => `same:${key}`),
  ];
  const options = Object.entries(profile.drbdOptions ?? {}).sort(([a], [b]) => a.localeCompare(b));
  const labels = Object.entries(profile.labels ?? {}).sort(([a], [b]) => a.localeCompare(b));

  return (
    <Card className="overflow-hidden">
      <CardHeader className="border-b bg-muted/30 pb-4">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <CardTitle className="flex items-center gap-2 text-base">
              <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-primary/10">
                <Layers3 className="h-4 w-4 text-primary" />
              </span>
              <span className="truncate">{profile.name}</span>
            </CardTitle>
            <p className="mt-2 text-xs text-muted-foreground">
              {resourceCount} existing resource{resourceCount === 1 ? '' : 's'} created with this profile
            </p>
          </div>
          <div className="flex shrink-0 items-center gap-1">
            <InstantiateProfileDialog profile={profile} nodes={nodes} />
            <ProfileDialog profile={profile} />
            <DeleteProfileDialog profile={profile} resourceCount={resourceCount} />
          </div>
        </div>
      </CardHeader>
      <CardContent className="space-y-5 pt-5">
        <div className="grid grid-cols-3 gap-2">
          <Metric label="Replicas" value={profile.replicas ? String(profile.replicas) : 'default'} />
          <Metric label="Protocol" value={profile.protocol || 'default'} />
          <Metric label="Storage" value={profile.storageType || 'default'} />
        </div>

        <div className="space-y-2">
          <p className="flex items-center gap-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">
            <Boxes className="h-3.5 w-3.5" /> Pool and placement
          </p>
          <div className="flex flex-wrap gap-1.5">
            <Badge variant="outline" className="font-mono font-normal">pool:{profile.pool || 'auto'}</Badge>
            {placement.length ? placement.map((item) => (
              <Badge key={item} variant="secondary" className="font-mono font-normal">{item}</Badge>
            )) : <span className="text-xs text-muted-foreground">No fault-domain constraints</span>}
          </div>
        </div>

        <MetadataBlock icon={ShieldCheck} title="DRBD options" entries={options} empty="Controller defaults" />
        <MetadataBlock icon={Braces} title="Default labels" entries={labels} empty="No labels" />
      </CardContent>
    </Card>
  );
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-md border bg-background px-3 py-2">
      <p className="text-[0.65rem] uppercase tracking-wide text-muted-foreground">{label}</p>
      <p className="mt-1 truncate font-mono text-sm font-medium">{value}</p>
    </div>
  );
}

function MetadataBlock({
  icon: Icon,
  title,
  entries,
  empty,
}: {
  icon: typeof ShieldCheck;
  title: string;
  entries: [string, string][];
  empty: string;
}) {
  return (
    <div className="space-y-2">
      <p className="flex items-center gap-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">
        <Icon className="h-3.5 w-3.5" /> {title}
      </p>
      {entries.length ? (
        <div className="flex flex-wrap gap-1.5">
          {entries.map(([key, value]) => (
            <Badge key={key} variant="secondary" className="max-w-full font-mono font-normal">
              <span className="truncate">{key}={value}</span>
            </Badge>
          ))}
        </div>
      ) : <p className="text-xs text-muted-foreground">{empty}</p>}
    </div>
  );
}
