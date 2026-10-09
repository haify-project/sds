import { useQuery } from '@tanstack/react-query';
import { api, type StorageJob } from '../../services/api';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { StatusBadge } from '@/components/StatusBadge';
import { sinceUnix } from './format';

const KIND_LABEL: Record<string, string> = {
  'remove-disk': 'Removing disk',
  'replace-disk': 'Replacing disk',
  'move-volume': 'Moving volume',
};

// A finished job stays on the page for an hour, so its outcome is seen, and
// only the latest few: the full history is in `haify pool jobs --all`.
const SHOW_FINISHED_SECONDS = 3600;
const SHOW_FINISHED_MAX = 3;

function visible(j: StorageJob): boolean {
  if (j.state === 'running') return true;
  const updated = Number(j.updatedUnix ?? 0);
  return Date.now() / 1000 - updated < SHOW_FINISHED_SECONDS;
}

// Long storage jobs: disks being emptied or replaced, volumes changing pool.
// Nothing is shown when there are none.
export function StorageJobsPanel() {
  const { data } = useQuery({
    queryKey: ['storage-jobs'],
    queryFn: () => api.getStorageJobs(true),
    refetchInterval: (q) => (q.state.data?.jobs?.some((j) => j.state === 'running') ? 5000 : 30000),
  });

  const all = data?.jobs ?? [];
  const jobs = [
    ...all.filter((j) => j.state === 'running'),
    ...all.filter((j) => j.state !== 'running' && visible(j)).slice(0, SHOW_FINISHED_MAX),
  ];
  if (jobs.length === 0) return null;

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Storage jobs</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        {jobs.map((j) => (
          <div key={j.id} className="flex flex-col gap-1 rounded-lg border bg-muted/40 p-3 text-sm">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span>
                <span className="font-medium">{KIND_LABEL[j.kind] ?? j.kind}</span>
                <span className="text-muted-foreground"> · {j.subject}</span>
              </span>
              <span className="flex items-center gap-2 text-xs text-muted-foreground">
                {j.state === 'running' && j.progress && <span>{j.progress}</span>}
                <span>started {sinceUnix(j.startedUnix)}</span>
                <StatusBadge status={j.state} />
              </span>
            </div>
            {j.message && (
              <p className={j.state === 'failed' ? 'text-xs text-destructive' : 'text-xs text-muted-foreground'}>
                {j.message}
              </p>
            )}
          </div>
        ))}
      </CardContent>
    </Card>
  );
}
