import type { ReactNode } from 'react';
import { cn } from '@/lib/utils';

/**
 * DRBD role, rendered as a chip rather than a status badge: Primary is a fact
 * about where the writes go, not a health signal, and colouring it green would
 * make every Secondary look broken. Only Primary gets the accent; the rest
 * differ by weight and text colour alone.
 *
 * The role string is displayed exactly as handed in ("Primary", "Diskless
 * client"); only the match that picks the styling is case-insensitive.
 */
export function RoleChip({
  role,
  suffix,
  className,
}: {
  role: string;
  suffix?: ReactNode;
  className?: string;
}) {
  // An empty role is nothing to say, not an empty chip to look at.
  if (!role) return null;
  const r = role.toLowerCase();
  const chipClass =
    r === 'primary'
      ? 'bg-accent text-accent-foreground font-medium'
      : // Secondary and anything unrecognised read the same: grey, and grey at
        // full strength — `text-muted-foreground` on `bg-secondary` is 4.08:1,
        // under AA, and an unfamiliar role is exactly the word you need to read.
        'bg-secondary text-secondary-foreground';
  return (
    <span
      data-slot="role-chip"
      className={cn('inline-flex items-center rounded-[5px] px-2 py-0.5 text-xs', chipClass, className)}
    >
      {role}
      {suffix ? <span className="ml-1 text-[10.5px] text-muted-foreground">{suffix}</span> : null}
    </span>
  );
}
