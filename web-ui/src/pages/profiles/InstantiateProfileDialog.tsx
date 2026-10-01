import { useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Loader2, Plus } from 'lucide-react';
import { toast } from 'sonner';
import { api, Node, ResourceProfile } from '@/services/api';
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

// InstantiateProfileDialog creates a resource from a profile.
//
// A profile is only ever a set of defaults, so the useful gesture is "make me
// one of these", and it needs little from the operator: the profile already
// answers everything except what to call it and how big it is.
//
// Placement is offered as an explicit either/or because that is what it is.
// Handing the controller a node list does not merely skip auto-placement, it
// discards `replicas` and every fault-domain constraint the profile carries —
// the answer has already been given, so nothing is left to solve. A dialog that
// showed both at once would let someone pick nodes that quietly violate the very
// profile they chose, and never say so.
export function InstantiateProfileDialog({
  profile,
  nodes,
}: {
  profile: ResourceProfile;
  nodes: Node[];
}) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState('');
  const [port, setPort] = useState('7100');
  const [sizeGb, setSizeGb] = useState('10');
  // Seeded from the profile but editable: the count is a fill-if-empty default
  // like every other field, so "this template, one more copy" is a legitimate
  // thing to ask for without editing the template.
  const [replicas, setReplicas] = useState(String(profile.replicas || 2));
  const [manual, setManual] = useState(false);
  const [picked, setPicked] = useState<string[]>([]);
  const queryClient = useQueryClient();

  const create = useMutation({
    mutationFn: () =>
      api.createResource({
        name: name.trim(),
        port: Number(port),
        sizeGb: Number(sizeGb),
        profile: profile.name,
        ...(manual
          ? { nodes: picked }
          : // No nodes: the controller places them from replicas plus the
            // profile's constraints.
            { replicas: Number(replicas) }),
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
    profile.replicasOnDifferent?.length
      ? `spread across ${profile.replicasOnDifferent.join(', ')}`
      : null,
    profile.replicasOnSame?.length
      ? `all within the same ${profile.replicasOnSame.join(', ')}`
      : null,
  ].filter(Boolean);

  const hasConstraints = constraints.length > 0;
  const ready =
    name.trim() && port && sizeGb && (manual ? picked.length > 0 : Number(replicas) > 0);

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="ghost" size="icon" aria-label={`Create a resource from ${profile.name}`}>
          <Plus className="h-4 w-4" />
        </Button>
      </DialogTrigger>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Create resource from "{profile.name}"</DialogTitle>
          <DialogDescription>
            Protocol, storage, pool, DRBD options and labels all come from the profile.
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

          <div className="space-y-3 rounded-lg border p-3">
            <div className="flex items-center justify-between">
              <Label className="text-sm font-medium">Placement</Label>
              <div className="flex rounded-md border p-0.5 text-xs">
                <button
                  type="button"
                  onClick={() => setManual(false)}
                  className={`rounded px-2 py-1 ${!manual ? 'bg-primary text-primary-foreground' : 'text-muted-foreground'}`}
                >
                  Automatic
                </button>
                <button
                  type="button"
                  onClick={() => setManual(true)}
                  className={`rounded px-2 py-1 ${manual ? 'bg-primary text-primary-foreground' : 'text-muted-foreground'}`}
                >
                  Choose nodes
                </button>
              </div>
            </div>

            {manual ? (
              <>
                <div className="max-h-44 space-y-1 overflow-y-auto">
                  {nodes.map((n) => (
                    <label
                      key={n.name}
                      className="flex cursor-pointer items-center gap-2 rounded px-1 py-1 text-sm hover:bg-muted"
                    >
                      <input
                        type="checkbox"
                        checked={picked.includes(n.name)}
                        onChange={(e) =>
                          setPicked((prev) =>
                            e.target.checked
                              ? [...prev, n.name]
                              : prev.filter((x) => x !== n.name),
                          )
                        }
                      />
                      <span>{n.name}</span>
                      <span className="text-xs text-muted-foreground">({n.address})</span>
                    </label>
                  ))}
                </div>
                {hasConstraints && (
                  <p className="text-xs text-amber-600">
                    Naming nodes discards this profile's placement rules (
                    {constraints.join(', ')}). Nothing will check that your
                    choice satisfies them.
                  </p>
                )}
              </>
            ) : (
              <>
                <div className="space-y-2">
                  <Label className="text-xs text-muted-foreground">Replicas</Label>
                  <Input
                    type="number"
                    min={1}
                    value={replicas}
                    onChange={(e) => setReplicas(e.target.value)}
                  />
                </div>
                <p className="text-xs text-muted-foreground">
                  {hasConstraints
                    ? `The controller chooses the nodes: ${constraints.join(', ')}.`
                    : 'The controller chooses the nodes by free space.'}{' '}
                  Creation is refused if no set of nodes satisfies the profile —
                  it never falls back to a placement that breaks the constraints.
                </p>
              </>
            )}
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => setOpen(false)}>
            Cancel
          </Button>
          <Button onClick={() => create.mutate()} disabled={!ready || create.isPending}>
            {create.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
            Create
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
