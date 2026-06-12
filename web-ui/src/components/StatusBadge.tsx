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
  // Monochrome pill; the small dot is the only color, carrying the status signal.
  const dot = GREEN.includes(s)
    ? 'bg-emerald-500'
    : YELLOW.includes(s)
      ? 'bg-amber-500'
      : RED.includes(s)
        ? 'bg-red-500'
        : 'bg-muted-foreground';
  return (
    <Badge
      variant="outline"
      className={cn(
        'gap-1.5 border-border bg-transparent font-normal capitalize text-foreground',
        className
      )}
    >
      <span className={cn('h-1.5 w-1.5 rounded-full', dot)} />
      {status || 'unknown'}
    </Badge>
  );
}
