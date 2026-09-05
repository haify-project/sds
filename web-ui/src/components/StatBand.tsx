import type { ReactNode } from 'react';
import { cn } from '@/lib/utils';
import { Skeleton } from '@/components/ui/skeleton';

/**
 * One bordered band instead of a row of stat cards. Four cards give four
 * borders, four shadows and four gaps for four numbers that are read together;
 * a single band with dividers says "these belong to one cluster" and costs the
 * page one border.
 */
export function StatBand({ className, children }: { className?: string; children: ReactNode }) {
  return (
    <div
      data-slot="stat-band"
      className={cn(
        'flex items-stretch divide-x divide-border overflow-hidden rounded-lg border border-border bg-card',
        className
      )}
    >
      {children}
    </div>
  );
}

/**
 * `grow` exists because the columns are not equally wide in content: a storage
 * figure with "of 960 GB free" after it needs more room than a node count.
 * `unit` beginning with "/" is the denominator form ("3 /4") and stays in the
 * mono face so the digits line up with the value; anything else is prose.
 */
export function StatBandItem({
  label,
  value,
  unit,
  detail,
  grow = 1,
  loading = false,
  className,
}: {
  label: string;
  value: ReactNode;
  unit?: ReactNode;
  detail?: ReactNode;
  grow?: number;
  loading?: boolean;
  className?: string;
}) {
  const denominator = typeof unit === 'string' && unit.startsWith('/');
  return (
    <div data-slot="stat-band-item" style={{ flexGrow: grow }} className={cn('min-w-0 px-5 py-4', className)}>
      <div className="eyebrow">{label}</div>
      {loading ? (
        <Skeleton className="mt-2 h-[27px] w-20" />
      ) : (
        <div className="mt-2 flex items-baseline gap-1.5">
          <span className="font-mono text-[27px] leading-none font-medium tracking-[-0.02em] tabular-nums">
            {value}
          </span>
          {unit ? (
            <span
              className={
                denominator
                  ? 'font-mono text-base text-muted-foreground'
                  : 'text-[13px] text-muted-foreground'
              }
            >
              {unit}
            </span>
          ) : null}
        </div>
      )}
      {detail ? <div className="mt-2.5 text-xs text-muted-foreground">{detail}</div> : null}
    </div>
  );
}
