import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';
import { toneOf, type StatusTone } from '@/components/status';

const DOT: Record<StatusTone, string> = {
  ok: 'bg-status-ok',
  warn: 'bg-status-warn',
  bad: 'bg-status-bad',
  idle: 'bg-status-idle',
};

// `soft` drops the outline for a tinted fill. It reads louder than the pill, so
// it belongs on the one status a screen is actually about, not on every row.
const SOFT: Record<StatusTone, string> = {
  ok: 'bg-status-ok-soft text-status-ok',
  warn: 'bg-status-warn-soft text-status-warn',
  bad: 'bg-status-bad-soft text-status-bad',
  idle: 'bg-status-idle-soft text-muted-foreground',
};

export function StatusBadge({
  status,
  variant = 'pill',
  className,
}: {
  status: string;
  variant?: 'pill' | 'soft';
  className?: string;
}) {
  const tone = toneOf(status || 'unknown');
  if (variant === 'soft') {
    return (
      <Badge
        variant="outline"
        className={cn('rounded-full border-transparent px-2.5 py-0.5 text-xs font-medium capitalize', SOFT[tone], className)}
      >
        {status || 'unknown'}
      </Badge>
    );
  }
  // Monochrome pill; the small dot is the only color, carrying the status signal.
  return (
    <Badge
      variant="outline"
      className={cn(
        'gap-1.5 rounded-full border-border bg-card px-2.5 py-0.5 text-xs font-normal capitalize text-foreground',
        className
      )}
    >
      <span className={cn('h-1.5 w-1.5 rounded-full', DOT[tone])} />
      {status || 'unknown'}
    </Badge>
  );
}
