import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';
import { toneOf, TONE_BG, TONE_SOFT } from '@/components/status';

/**
 * Two readings of one status. `pill` is monochrome with a coloured dot — the
 * safe default, because a table of thirty rows should not be thirty tinted
 * blocks. `soft` drops the outline for a tinted fill; it reads louder, so it
 * belongs on the one status a screen is actually about.
 *
 * Only what the variants differ in is spelled out here — `Badge` already sets
 * the pill shape, padding and type scale.
 */
export function StatusBadge({
  status,
  variant = 'pill',
  className,
}: {
  status: string;
  variant?: 'pill' | 'soft';
  className?: string;
}) {
  const tone = toneOf(status);
  const soft = variant === 'soft';
  return (
    <Badge
      variant="outline"
      className={cn(
        'px-2.5 capitalize',
        soft && cn('border-transparent', TONE_SOFT[tone]),
        !soft && 'gap-1.5 bg-card font-normal',
        className
      )}
    >
      {/* The dot is the pill's only colour, and carries the whole signal. */}
      {soft ? null : <span className={cn('h-1.5 w-1.5 rounded-full', TONE_BG[tone])} />}
      {status || 'unknown'}
    </Badge>
  );
}
