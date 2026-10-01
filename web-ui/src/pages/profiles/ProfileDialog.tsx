import { useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Loader2, Pencil, Plus } from 'lucide-react';
import { toast } from 'sonner';
import { api, ResourceProfile } from '@/services/api';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import {
  unsetValue,
  type ProfileForm,
  emptyForm,
  profileToForm,
  formToProfile,
} from './form';

export function ProfileDialog({ profile }: { profile?: ResourceProfile }) {
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
