import { cn } from '@/lib/utils';
import { TONE_BG, type StatusTone } from '@/components/status';

/**
 * One dash per thing, coloured by its tone — "3/4 nodes online" as a picture
 * rather than a second number. Decorative: the count it illustrates is always
 * spelled out next to it, so screen readers get nothing useful from the dashes.
 */
export function SegmentBar({ segments, className }: { segments: StatusTone[]; className?: string }) {
  return (
    <div data-slot="segment-bar" aria-hidden className={cn('flex flex-wrap gap-1', className)}>
      {/* Index keys: the spans hold no state, no focus and no identity to keep. */}
      {segments.map((tone, i) => (
        <span key={i} className={cn('h-1 w-[22px] rounded-[2px]', TONE_BG[tone])} />
      ))}
    </div>
  );
}
