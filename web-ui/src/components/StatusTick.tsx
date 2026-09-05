import { cn } from '@/lib/utils';
import { TableCell } from '@/components/ui/table';
import { toneOf, type StatusTone } from '@/components/status';

const TONE_BG: Record<StatusTone, string> = {
  ok: 'bg-status-ok',
  warn: 'bg-status-warn',
  bad: 'bg-status-bad',
  idle: 'bg-status-idle',
};

type TickProps = { tone?: StatusTone; status?: string; className?: string };

function resolve(tone: StatusTone | undefined, status: string | undefined): StatusTone {
  return tone ?? toneOf(status);
}

/**
 * A 3px rule at the start of a table row: the row's health readable while
 * scanning the column of names, without spending a column on it. Purely a
 * repeat of the status the row already states in words, so it is hidden from
 * assistive tech rather than given a label of its own.
 */
export function StatusTick({ tone, status, className }: TickProps) {
  return (
    <span
      aria-hidden
      data-slot="status-tick"
      className={cn('block h-6 w-[3px] rounded-[2px]', TONE_BG[resolve(tone, status)], className)}
    />
  );
}

/** The tick plus the near-zero-width cell it always lives in. */
export function StatusTickCell({ tone, status, className }: TickProps) {
  return (
    <TableCell className={cn('w-[4px] pr-0 pl-5', className)}>
      <StatusTick tone={tone} status={status} />
    </TableCell>
  );
}
