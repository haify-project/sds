import { useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { api, Resource } from '../../services/api';
import { Button } from '@/components/ui/button';
import { Label } from '@/components/ui/label';
import { Switch } from '@/components/ui/switch';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { toast } from 'sonner';
import { Loader2 } from 'lucide-react';

export function SetRoleDialog({
  open,
  onOpenChange,
  resource,
  mode,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  resource: Resource;
  mode: 'primary' | 'secondary';
}) {
  const queryClient = useQueryClient();
  const [node, setNode] = useState(resource.nodes[0] ?? '');
  const [force, setForce] = useState(false);

  const mutation = useMutation({
    mutationFn: () =>
      mode === 'primary'
        ? api.setPrimary(resource.name, node, force)
        : api.setSecondary(resource.name, node),
    onSuccess: () => {
      toast.success(
        `${resource.name} set ${mode === 'primary' ? 'Primary' : 'Secondary'} on ${node}`,
      );
      queryClient.invalidateQueries({ queryKey: ['resources'] });
      onOpenChange(false);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            Set {mode === 'primary' ? 'Primary' : 'Secondary'} — {resource.name}
          </DialogTitle>
          <DialogDescription>
            Choose the node to promote to{' '}
            {mode === 'primary' ? 'Primary' : 'Secondary'}.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-4">
          <div className="space-y-2">
            <Label>Node</Label>
            <Select value={node} onValueChange={setNode}>
              <SelectTrigger>
                <SelectValue placeholder="Select a node..." />
              </SelectTrigger>
              <SelectContent>
                {resource.nodes.map((n) => (
                  <SelectItem key={n} value={n}>
                    {n}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          {mode === 'primary' && (
            <div className="flex items-center justify-between rounded-lg border p-3">
              <div className="space-y-0.5">
                <Label htmlFor="force-primary">Force</Label>
                <p className="text-xs text-muted-foreground">
                  Force promotion even if peers are unreachable.
                </p>
              </div>
              <Switch
                id="force-primary"
                checked={force}
                onCheckedChange={setForce}
              />
            </div>
          )}
        </div>

        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={mutation.isPending}
          >
            Cancel
          </Button>
          <Button
            onClick={() => mutation.mutate()}
            disabled={mutation.isPending || !node}
          >
            {mutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
            Apply
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
