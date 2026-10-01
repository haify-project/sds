import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api, Resource } from '../../services/api';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { toast } from 'sonner';
import { Trash2, Loader2 } from 'lucide-react';

const KEEP_FIELDS: { key: keyof import('@/services/api').GFSRetention; label: string }[] = [
  { key: 'hourly', label: 'Hourly' },
  { key: 'daily', label: 'Daily' },
  { key: 'weekly', label: 'Weekly' },
  { key: 'monthly', label: 'Monthly' },
  { key: 'yearly', label: 'Yearly' },
];

export function ScheduleDialog({
  open,
  onOpenChange,
  resource,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  resource: Resource;
}) {
  const queryClient = useQueryClient();
  const [cron, setCron] = useState('0 * * * *');
  const [keep, setKeep] = useState<Record<string, string>>({
    hourly: '24',
    daily: '7',
    weekly: '4',
    monthly: '6',
    yearly: '0',
  });

  const { data, isLoading } = useQuery({
    queryKey: ['snapshot-schedules'],
    queryFn: () => api.getSnapshotSchedules(),
    enabled: open,
  });
  const existing = data?.schedules?.find((s) => s.name === resource.name);

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ['snapshot-schedules'] });

  const createMutation = useMutation({
    mutationFn: () =>
      api.createSnapshotSchedule({
        resource: resource.name,
        cron: cron.trim(),
        keep: Object.fromEntries(
          KEEP_FIELDS.map((f) => [f.key, parseInt(keep[f.key] || '0', 10) || 0]),
        ),
        enabled: true,
      }),
    onSuccess: () => {
      toast.success(`Snapshot schedule saved for "${resource.name}"`);
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const deleteMutation = useMutation({
    mutationFn: () => api.deleteSnapshotSchedule(resource.name),
    onSuccess: () => {
      toast.success(`Snapshot schedule deleted for "${resource.name}"`);
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!cron.trim()) {
      toast.error('Enter a cron expression (e.g. "0 * * * *")');
      return;
    }
    if (KEEP_FIELDS.every((f) => (parseInt(keep[f.key] || '0', 10) || 0) === 0)) {
      toast.error('Keep at least one of hourly/daily/weekly/monthly/yearly');
      return;
    }
    createMutation.mutate();
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Snapshot Schedule — {resource.name}</DialogTitle>
          <DialogDescription>
            Cron-driven snapshots on every diskful node, pruned by a
            grandfather-father-son retention policy. One schedule per resource.
          </DialogDescription>
        </DialogHeader>

        {isLoading ? (
          <Skeleton className="h-16 w-full" />
        ) : existing ? (
          <div className="rounded-lg border p-3 text-sm">
            <div className="flex items-center justify-between">
              <span className="font-mono">{existing.cron}</span>
              <Badge variant={existing.enabled ? 'default' : 'secondary'}>
                {existing.enabled ? 'enabled' : 'disabled'}
              </Badge>
            </div>
            <p className="mt-1 text-muted-foreground">
              keep: hourly={existing.keep?.hourly ?? 0} daily=
              {existing.keep?.daily ?? 0} weekly={existing.keep?.weekly ?? 0}{' '}
              monthly={existing.keep?.monthly ?? 0} yearly=
              {existing.keep?.yearly ?? 0}
            </p>
            {existing.nextRun && (
              <p className="text-muted-foreground">
                next run: {new Date(existing.nextRun).toLocaleString()}
              </p>
            )}
            {existing.lastRun && (
              <p className="text-muted-foreground">
                last run: {new Date(existing.lastRun).toLocaleString()}
              </p>
            )}
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="mt-2 text-destructive"
              onClick={() => deleteMutation.mutate()}
              disabled={deleteMutation.isPending}
            >
              <Trash2 className="h-4 w-4" />
              Delete schedule
            </Button>
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">
            No schedule yet. Create one below.
          </p>
        )}

        <form onSubmit={submit} className="space-y-4 pt-2">
          <div className="space-y-2">
            <Label htmlFor="sched-cron">
              Cron (standard 5-field, e.g. "0 * * * *" = hourly)
            </Label>
            <Input
              id="sched-cron"
              value={cron}
              onChange={(e) => setCron(e.target.value)}
              className="font-mono text-sm"
              placeholder="0 * * * *"
            />
          </div>
          <div className="grid grid-cols-3 gap-2 sm:grid-cols-5">
            {KEEP_FIELDS.map((f) => (
              <div key={f.key} className="space-y-1">
                <Label htmlFor={`keep-${f.key}`} className="text-xs">
                  {f.label}
                </Label>
                <Input
                  id={`keep-${f.key}`}
                  type="number"
                  min={0}
                  value={keep[f.key] ?? '0'}
                  onChange={(e) =>
                    setKeep((k) => ({ ...k, [f.key]: e.target.value }))
                  }
                  className="text-sm"
                />
              </div>
            ))}
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
            >
              Close
            </Button>
            <Button type="submit" disabled={createMutation.isPending}>
              {createMutation.isPending && (
                <Loader2 className="h-4 w-4 animate-spin" />
              )}
              {existing ? 'Replace schedule' : 'Create schedule'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
