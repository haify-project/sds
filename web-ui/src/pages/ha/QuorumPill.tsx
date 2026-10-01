import { QuorumInfo } from '@/services/api';
import { TONE_BG } from '@/components/status';
import { cn } from '@/lib/utils';

// ==================== Small shared pieces ====================

/**
 * Quorum, stated in words first and coloured second: "Quorate" and "No quorum"
 * carry the whole meaning on their own, and the dot only repeats it.
 */
export function QuorumPill({ quorum }: { quorum?: QuorumInfo }) {
  if (!quorum) return null;
  return (
    <span className="inline-flex items-center gap-2 rounded-full border border-border px-2.5 py-0.5 text-xs">
      <span
        aria-hidden
        className={cn(
          'h-1.5 w-1.5 rounded-full',
          TONE_BG[quorum.hasQuorum ? 'ok' : 'bad']
        )}
      />
      {quorum.hasQuorum ? 'Quorate' : 'No quorum'}
      <span className="font-mono tabular-nums text-muted-foreground">
        {quorum.online}/{quorum.members}
      </span>
    </span>
  );
}
