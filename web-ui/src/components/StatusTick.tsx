import type { ReactNode } from 'react';
import { cn } from '@/lib/utils';
import { TableCell, TableHead } from '@/components/ui/table';
import { toneOf, TONE_BG, type StatusTone } from '@/components/status';

/**
 * A 3px rule at the start of a table row: the row's health readable while
 * scanning the column of names, without spending a column on it. Purely a
 * repeat of the status the row already states in words, so the rule itself is
 * hidden from assistive tech rather than given a label of its own.
 *
 * The `td` is deliberately *not* aria-hidden — a hidden cell would leave the
 * row one cell short of its header and break the table's grid integrity; only
 * the span inside it is hidden. The narrow look comes from the row's left
 * gutter (`pl-5` with no right padding), not from `w-[4px]`, which a table
 * layout treats as a minimum and will happily exceed.
 */

/** Either an already-resolved tone or a raw status string — never neither. */
type TickProps = { className?: string } & (
  | { tone: StatusTone; status?: never }
  | { status: string | undefined; tone?: never }
);

export function StatusTick({ tone, status, className }: TickProps) {
  return (
    <span
      aria-hidden
      data-slot="status-tick"
      className={cn('block h-6 w-[3px] rounded-[2px]', TONE_BG[tone ?? toneOf(status)], className)}
    />
  );
}

/**
 * The tick plus the gutter cell it always lives in. `children` is where a row
 * that has nowhere else to say its status out loud puts an `sr-only` word —
 * the tick is colour only, so something in the row has to carry the meaning.
 */
export function StatusTickCell({
  className,
  children,
  ...props
}: TickProps & { children?: ReactNode }) {
  return (
    <TableCell className={cn('w-[4px] pr-0 pl-5', className)}>
      <StatusTick {...props} />
      {children}
    </TableCell>
  );
}

/**
 * The header cell that pairs with `StatusTickCell`. Exported so a table cannot
 * adopt the tick column and silently end up with one more body cell than it
 * has headings.
 */
export function StatusTickHead() {
  return <TableHead className="w-[4px] p-0" aria-hidden />;
}
