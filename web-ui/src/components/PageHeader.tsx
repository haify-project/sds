import type { ReactNode } from 'react';
import { cn } from '@/lib/utils';

/**
 * The band at the top of every page: title, a one-line factual subtitle, and
 * the page's actions. It carries the rule that separates the header from the
 * content, so pages must not add one of their own; `<main>` already supplies
 * the horizontal padding, so the header spans the full content width.
 *
 * `description` is a node rather than a string on purpose — the subtitles are
 * mostly counts and identifiers, and the caller wraps the identifiers in
 * `<span className="font-mono text-foreground">` so they read as machine names.
 */
export function PageHeader({
  title,
  description,
  actions,
  className,
}: {
  title: string;
  description?: ReactNode;
  actions?: ReactNode;
  className?: string;
}) {
  return (
    <div
      data-slot="page-header"
      className={cn('mb-6 flex items-end justify-between gap-4 border-b border-border pb-5', className)}
    >
      <div className="min-w-0">
        <h1 className="text-[23px] leading-tight font-semibold tracking-[-0.015em]">{title}</h1>
        {description ? <div className="mt-1.5 text-[13px] text-muted-foreground">{description}</div> : null}
      </div>
      {actions ? <div className="flex shrink-0 items-center gap-2.5">{actions}</div> : null}
    </div>
  );
}
