import { useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { api, Resource } from '../../services/api';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { toast } from 'sonner';
import { Plus, Loader2, X } from 'lucide-react';

export function EditOptionsDialog({
  open,
  onOpenChange,
  resource,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  resource: Resource;
}) {
  const queryClient = useQueryClient();
  const [rows, setRows] = useState<{ key: string; value: string }[]>([
    { key: '', value: '' },
  ]);

  const mutation = useMutation({
    mutationFn: (opts: Record<string, string>) =>
      api.updateResourceOptions(resource.name, opts),
    onSuccess: () => {
      toast.success(`Options applied to "${resource.name}" (drbdadm adjust)`);
      queryClient.invalidateQueries({ queryKey: ['resources'] });
      onOpenChange(false);
      setRows([{ key: '', value: '' }]);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const setRow = (i: number, field: 'key' | 'value', v: string) =>
    setRows((rs) => rs.map((r, idx) => (idx === i ? { ...r, [field]: v } : r)));
  const addRow = () => setRows((rs) => [...rs, { key: '', value: '' }]);
  const removeRow = (i: number) =>
    setRows((rs) =>
      rs.length > 1 ? rs.filter((_, idx) => idx !== i) : [{ key: '', value: '' }],
    );

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const opts: Record<string, string> = {};
    for (const r of rows) {
      const k = r.key.trim();
      const v = r.value.trim();
      if (k && v) opts[k] = v;
    }
    if (Object.keys(opts).length === 0) {
      toast.error('Add at least one option (key and value)');
      return;
    }
    mutation.mutate(opts);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>Edit DRBD Options — {resource.name}</DialogTitle>
            <DialogDescription>
              Set options as{' '}
              <code className="font-mono text-xs">section/key</code> = value
              (e.g. <code className="font-mono text-xs">net/max-buffers</code> ={' '}
              <code className="font-mono text-xs">8000</code>). A bare key goes
              to the resource-level options section. Applied live with{' '}
              <code className="font-mono text-xs">drbdadm adjust</code>.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2 py-4">
            {rows.map((r, i) => (
              <div key={i} className="flex items-center gap-2">
                <Input
                  placeholder="net/max-buffers"
                  value={r.key}
                  onChange={(e) => setRow(i, 'key', e.target.value)}
                  className="font-mono text-xs"
                />
                <span className="text-muted-foreground">=</span>
                <Input
                  placeholder="8000"
                  value={r.value}
                  onChange={(e) => setRow(i, 'value', e.target.value)}
                  className="font-mono text-xs"
                />
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8 shrink-0 text-muted-foreground hover:text-destructive"
                  onClick={() => removeRow(i)}
                  aria-label="Remove option"
                >
                  <X className="h-4 w-4" />
                </Button>
              </div>
            ))}
            <Button type="button" variant="outline" size="sm" onClick={addRow}>
              <Plus className="h-4 w-4" />
              Add option
            </Button>
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={mutation.isPending}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={mutation.isPending}>
              {mutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
              Apply
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
