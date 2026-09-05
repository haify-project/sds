import { cn } from '@/lib/utils';
import type { StatusTone } from '@/components/status';

const TONE_BG: Record<StatusTone, string> = {
  ok: 'bg-status-ok',
  warn: 'bg-status-warn',
  bad: 'bg-status-bad',
  idle: 'bg-status-idle',
};

export type Segment = { tone: StatusTone };

/**
 * One dash per thing, coloured by its tone — "3/4 nodes online" as a picture
 * rather than a second number. Decorative: the count it illustrates is always
 * spelled out next to it, so screen readers get nothing useful from the dashes.
 */
export function SegmentBar({ segments, className }: { segments: Segment[]; className?: string }) {
  return (
    <div data-slot="segment-bar" aria-hidden className={cn('flex gap-1', className)}>
      {segments.map((segment, i) => (
        <span key={i} className={cn('h-1 w-[22px] rounded-[2px]', TONE_BG[segment.tone])} />
      ))}
    </div>
  );
}
