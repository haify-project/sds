import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';

/**
 * Cluster status → badge color mapping shared by all pages, so "online",
 * "Primary", "running" etc. look the same everywhere.
 */
const GREEN = ['online', 'running', 'active', 'primary', 'uptodate', 'connected', 'healthy', 'enabled'];
const YELLOW = ['secondary', 'syncing', 'inconsistent', 'degraded', 'standby'];
const RED = ['offline', 'stopped', 'inactive', 'failed', 'error', 'diskless', 'disconnected'];

export function StatusBadge({ status, className }: { status: string; className?: string }) {
  const s = (status || 'unknown').toLowerCase();
  const color = GREEN.includes(s)
    ? 'bg-emerald-100 text-emerald-800 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-900'
    : YELLOW.includes(s)
      ? 'bg-amber-100 text-amber-800 border-amber-200 dark:bg-amber-950 dark:text-amber-300 dark:border-amber-900'
      : RED.includes(s)
        ? 'bg-red-100 text-red-800 border-red-200 dark:bg-red-950 dark:text-red-300 dark:border-red-900'
        : 'bg-slate-100 text-slate-700 border-slate-200 dark:bg-slate-900 dark:text-slate-300 dark:border-slate-800';
  return (
    <Badge variant="outline" className={cn(color, className)}>
      {status || 'unknown'}
    </Badge>
  );
}
