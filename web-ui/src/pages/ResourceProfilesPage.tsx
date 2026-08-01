import { useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Boxes,
  Braces,
  Layers3,
  Loader2,
  Pencil,
  Plus,
  ShieldCheck,
  Trash2,
} from 'lucide-react';
import { toast } from 'sonner';
import { api, ResourceProfile } from '@/services/api';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
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
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { Skeleton } from '@/components/ui/skeleton';

const unsetValue = '__unset__';

type ProfileForm = {
  name: string;
  protocol: string;
  storageType: string;
  pool: string;
  replicas: string;
  replicasOnDifferent: string;
  replicasOnSame: string;
  drbdOptions: string;
  labels: string;
};

const emptyForm: ProfileForm = {
  name: '',
  protocol: 'C',
  storageType: 'lvm',
  pool: '',
  replicas: '2',
  replicasOnDifferent: '',
  replicasOnSame: '',
  drbdOptions: '',
  labels: '',
};

function formatMap(values?: Record<string, string>): string {
  return Object.entries(values ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, value]) => `${key}=${value}`)
    .join(', ');
}

function parseMap(value: string, field: string): Record<string, string> {
  const result: Record<string, string> = {};
  for (const raw of value.split(',')) {
    const entry = raw.trim();
    if (!entry) continue;
    const separator = entry.indexOf('=');
    if (separator < 1) {
      throw new Error(`${field}: "${entry}" must use key=value`);
    }
    const key = entry.slice(0, separator).trim();
    const itemValue = entry.slice(separator + 1).trim();
    if (!key || !itemValue) {
      throw new Error(`${field}: "${entry}" must have a non-empty key and value`);
    }
    result[key] = itemValue;
  }
  return result;
}

function parseList(value: string): string[] {
  return value
    .split(',')
    .map((item) => item.trim())
    .filter(Boolean);
}

function profileToForm(profile: ResourceProfile): ProfileForm {
  return {
    name: profile.name,
    protocol: profile.protocol || '',
    storageType: profile.storageType || '',
    pool: profile.pool || '',
    replicas: profile.replicas ? String(profile.replicas) : '',
    replicasOnDifferent: (profile.replicasOnDifferent ?? []).join(', '),
    replicasOnSame: (profile.replicasOnSame ?? []).join(', '),
    drbdOptions: formatMap(profile.drbdOptions),
    labels: formatMap(profile.labels),
  };
}

function formToProfile(form: ProfileForm): ResourceProfile {
  const replicas = form.replicas.trim() ? Number(form.replicas) : 0;
  if (!Number.isInteger(replicas) || replicas < 0) {
    throw new Error('Replicas must be a non-negative integer');
  }
  return {
    name: form.name.trim(),
    protocol: form.protocol,
    storageType: form.storageType,
    pool: form.pool.trim(),
    replicas,
    replicasOnDifferent: parseList(form.replicasOnDifferent),
    replicasOnSame: parseList(form.replicasOnSame),
    drbdOptions: parseMap(form.drbdOptions, 'DRBD options'),
    labels: parseMap(form.labels, 'Labels'),
  };
}


// InstantiateProfileDialog creates a resource from a profile.
//
// A profile is only ever a set of defaults, so the useful gesture is "make me
// one of these" — and it needs almost nothing from the operator, because the
// profile already answers everything except what to call it and how big it is.
//
// It deliberately does NOT offer node selection. Choosing nodes by hand is what
// switches placement OFF: the controller ignores replicas and the fault-domain
// constraints the moment an explicit node list arrives. Offering both here
// would let an operator pick nodes that quietly violate the very profile they
// selected. The general Create Resource dialog still allows explicit nodes for
// the cases that want them.
function InstantiateProfileDialog({ profile }: { profile: ResourceProfile }) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState('');
  const [port, setPort] = useState('7100');
  const [sizeGb, setSizeGb] = useState('10');
  const queryClient = useQueryClient();

  const create = useMutation({
    mutationFn: () =>
      api.createResource({
        name: name.trim(),
        port: Number(port),
        sizeGb: Number(sizeGb),
        profile: profile.name,
        // No nodes: the controller places the replicas from the profile.
      }),
    onSuccess: (res) => {
      if (!res.success) {
        toast.error(res.message || 'Failed to create resource');
        return;
      }
      toast.success(`Resource "${name.trim()}" created from profile "${profile.name}"`);
      queryClient.invalidateQueries({ queryKey: ['resources'] });
      setOpen(false);
      setName('');
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const constraints = [
    profile.replicas ? `${profile.replicas} replicas` : null,
    profile.replicasOnDifferent?.length
      ? `spread across ${profile.replicasOnDifferent.join(', ')}`
      : null,
    profile.replicasOnSame?.length
      ? `all within the same ${profile.replicasOnSame.join(', ')}`
      : null,
  ].filter(Boolean);

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="ghost" size="icon" aria-label={`Create a resource from ${profile.name}`}>
          <Plus className="h-4 w-4" />
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Create resource from "{profile.name}"</DialogTitle>
          <DialogDescription>
            Everything except the name, port and size comes from the profile.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <div className="space-y-2">
            <Label>Resource name</Label>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="e.g. data"
              autoFocus
            />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-2">
              <Label>Port</Label>
              <Input type="number" value={port} onChange={(e) => setPort(e.target.value)} />
            </div>
            <div className="space-y-2">
              <Label>Size (GB)</Label>
              <Input type="number" value={sizeGb} onChange={(e) => setSizeGb(e.target.value)} />
            </div>
          </div>

          <div className="rounded-lg border bg-muted/40 p-3 text-sm">
            <p className="mb-1 font-medium">Placement</p>
            <p className="text-muted-foreground">
              {constraints.length > 0
                ? `The controller will choose nodes: ${constraints.join(', ')}.`
                : 'The controller will choose nodes by free space.'}
            </p>
            <p className="mt-2 text-xs text-muted-foreground">
              Creation is refused if no set of nodes satisfies the profile — it
              never falls back to a placement that breaks the constraints.
            </p>
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => setOpen(false)}>
            Cancel
          </Button>
          <Button
            onClick={() => create.mutate()}
            disabled={!name.trim() || !port || !sizeGb || create.isPending}
          >
            {create.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
            Create
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function ResourceProfilesPage() {
  const { data, isLoading } = useQuery({
    queryKey: ['resource-profiles'],
    queryFn: () => api.getResourceProfiles(),
  });
  const { data: resources } = useQuery({
    queryKey: ['resources'],
    queryFn: () => api.getResources(),
  });

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
            <ProfileCard key={profile.name} profile={profile} resourceCount={usage[profile.name] ?? 0} />
          ))}
        </div>
      )}
    </div>
  );
}

function ProfileCard({ profile, resourceCount }: { profile: ResourceProfile; resourceCount: number }) {
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
            <InstantiateProfileDialog profile={profile} />
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

function ProfileDialog({ profile }: { profile?: ResourceProfile }) {
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState<ProfileForm>(() => profile ? profileToForm(profile) : emptyForm);

  const save = useMutation({
    mutationFn: (value: ResourceProfile) => api.createResourceProfile(value),
    onSuccess: (_response, saved) => {
      toast.success(`Profile "${saved.name}" saved`);
      queryClient.invalidateQueries({ queryKey: ['resource-profiles'] });
      setOpen(false);
      if (!profile) setForm(emptyForm);
    },
    onError: (error: Error) => toast.error(error.message),
  });

  const update = (field: keyof ProfileForm, value: string) => {
    setForm((current) => ({ ...current, [field]: value }));
  };

  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    try {
      const value = formToProfile(form);
      if (!value.name) throw new Error('Profile name is required');
      save.mutate(value);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : 'Invalid profile');
    }
  };

  return (
    <Dialog open={open} onOpenChange={(next) => {
      setOpen(next);
      if (next) setForm(profile ? profileToForm(profile) : emptyForm);
    }}>
      <DialogTrigger asChild>
        {profile ? (
          <Button variant="ghost" size="icon" title={`Edit ${profile.name}`}>
            <Pencil className="h-4 w-4" />
          </Button>
        ) : (
          <Button><Plus className="h-4 w-4" /> Create Profile</Button>
        )}
      </DialogTrigger>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <form onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>{profile ? `Edit ${profile.name}` : 'Create Resource Profile'}</DialogTitle>
            <DialogDescription>
              These values become defaults at resource creation time. Explicit resource settings override them.
            </DialogDescription>
          </DialogHeader>

          <div className="grid gap-4 py-5 sm:grid-cols-2">
            <Field label="Name" htmlFor="profile-name" className="sm:col-span-2">
              <Input id="profile-name" value={form.name} onChange={(e) => update('name', e.target.value)} disabled={!!profile} required />
            </Field>
            <Field label="Protocol" htmlFor="profile-protocol">
              <Select value={form.protocol || unsetValue} onValueChange={(value) => update('protocol', value === unsetValue ? '' : value)}>
                <SelectTrigger id="profile-protocol"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value={unsetValue}>Controller default</SelectItem>
                  {['A', 'B', 'C'].map((value) => <SelectItem key={value} value={value}>{value}</SelectItem>)}
                </SelectContent>
              </Select>
            </Field>
            <Field label="Storage type" htmlFor="profile-storage">
              <Select value={form.storageType || unsetValue} onValueChange={(value) => update('storageType', value === unsetValue ? '' : value)}>
                <SelectTrigger id="profile-storage"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value={unsetValue}>Controller default</SelectItem>
                  <SelectItem value="lvm">LVM</SelectItem>
                  <SelectItem value="lvm-thin">LVM Thin</SelectItem>
                  <SelectItem value="zfs">ZFS</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            <Field label="Pool" htmlFor="profile-pool">
              <Input id="profile-pool" value={form.pool} onChange={(e) => update('pool', e.target.value)} placeholder="fast" />
            </Field>
            <Field label="Replicas" htmlFor="profile-replicas">
              <Input id="profile-replicas" type="number" min="0" step="1" value={form.replicas} onChange={(e) => update('replicas', e.target.value)} placeholder="2" />
            </Field>
            <Field label="Spread across labels" htmlFor="profile-different" hint="Comma-separated node label keys, such as zone, rack.">
              <Input id="profile-different" value={form.replicasOnDifferent} onChange={(e) => update('replicasOnDifferent', e.target.value)} placeholder="zone, rack" />
            </Field>
            <Field label="Keep within labels" htmlFor="profile-same" hint="All replicas must share these node label values.">
              <Input id="profile-same" value={form.replicasOnSame} onChange={(e) => update('replicasOnSame', e.target.value)} placeholder="region" />
            </Field>
            <Field label="DRBD options" htmlFor="profile-options" hint="Comma-separated key=value pairs." className="sm:col-span-2">
              <Input id="profile-options" value={form.drbdOptions} onChange={(e) => update('drbdOptions', e.target.value)} placeholder="net/max-buffers=8000, disk/on-io-error=detach" className="font-mono text-sm" />
            </Field>
            <Field label="Default labels" htmlFor="profile-labels" hint="Applied to resources unless the create request overrides the same key." className="sm:col-span-2">
              <Input id="profile-labels" value={form.labels} onChange={(e) => update('labels', e.target.value)} placeholder="environment=prod, tier=critical" className="font-mono text-sm" />
            </Field>
          </div>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>Cancel</Button>
            <Button type="submit" disabled={save.isPending}>
              {save.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
              Save Profile
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function Field({
  label,
  htmlFor,
  hint,
  className = '',
  children,
}: {
  label: string;
  htmlFor: string;
  hint?: string;
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <div className={`space-y-2 ${className}`}>
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
    </div>
  );
}

function DeleteProfileDialog({ profile, resourceCount }: { profile: ResourceProfile; resourceCount: number }) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    mutationFn: () => api.deleteResourceProfile(profile.name),
    onSuccess: () => {
      toast.success(`Profile "${profile.name}" deleted`);
      queryClient.invalidateQueries({ queryKey: ['resource-profiles'] });
    },
    onError: (error: Error) => toast.error(error.message),
  });

  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>
        <Button variant="ghost" size="icon" title={`Delete ${profile.name}`}>
          <Trash2 className="h-4 w-4 text-destructive" />
        </Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete profile “{profile.name}”?</AlertDialogTitle>
          <AlertDialogDescription>
            New resources can no longer use this profile. The {resourceCount} existing resource{resourceCount === 1 ? '' : 's'} attributed to it keep their resolved configuration and continue operating normally.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction onClick={() => remove.mutate()} disabled={remove.isPending}>
            {remove.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
            Delete Profile
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
