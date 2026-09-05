import type { ReactNode } from 'react';
import { ChevronDown, ChevronRight } from 'lucide-react';
import { cn } from '@/lib/utils';
import { TONE_BG, toneOf, type StatusTone } from '@/components/status';

/**
 * The phone shape of a table row.
 *
 * A dense table is the right form on a desktop and the wrong one on a 375px
 * screen: the columns that matter — status, nodes, replication — end up past
 * the right edge, reachable only by a horizontal scroll nobody performs while
 * standing up. Below `md` each page renders this instead, from the same derived
 * values it feeds its row, so the two views cannot describe the same thing
 * differently.
 *
 * The card keeps the row's vocabulary: the tone rail on the leading edge, the
 * identifier in mono, `.eyebrow` labels on the facts. What changes is the
 * arrangement — facts stack into a two-column grid instead of competing for
 * width, and the actions get a row of their own where they can be hit with a
 * thumb.
 */
export function RecordCard({
  tone,
  status,
  title,
  subtitle,
  facts,
  actions,
  open,
  onToggle,
  detailId,
  children,
  className,
}: {
  /** Either a resolved tone or the raw status word; the rail needs one. */
  tone?: StatusTone;
  status?: string;
  /** The identifier line — mono name plus whatever chips qualify it. */
  title: ReactNode;
  /** A second line under the title: protocol, host name, port. */
  subtitle?: ReactNode;
  /** Label/value pairs. Long values wrap; the label stays put. */
  facts?: { label: string; value: ReactNode }[];
  /** Buttons. They sit on their own row so they clear a thumb. */
  actions?: ReactNode;
  /** Present only on a card that expands. */
  open?: boolean;
  onToggle?: () => void;
  detailId?: string;
  /** The expanded detail, rendered when `open`. */
  children?: ReactNode;
  className?: string;
}) {
  const expandable = onToggle !== undefined;
  const Chevron = open ? ChevronDown : ChevronRight;

  return (
    <div
      data-slot="record-card"
      className={cn(
        'relative overflow-hidden rounded-lg border border-border bg-card',
        className,
      )}
    >
      {/* The rail runs the full height of the card, the way the tick runs the
          height of a row. aria-hidden: the status is in the facts as a word. */}
      <span
        aria-hidden
        className={cn(
          'absolute inset-y-0 left-0 w-[3px]',
          TONE_BG[tone ?? toneOf(status)],
        )}
      />

      <div className="py-3.5 pr-3.5 pl-4.5">
        <div className="flex items-start gap-2">
          {expandable ? (
            <button
              type="button"
              onClick={onToggle}
              aria-expanded={open}
              aria-controls={open ? detailId : undefined}
              // min-h-11: a 44px target, which is the floor for a thumb.
              className="-my-1 flex min-h-11 min-w-0 flex-1 items-start gap-2 rounded-md py-1 text-left outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
            >
              <Chevron
                className={cn(
                  'mt-0.5 size-4 shrink-0',
                  open ? 'text-foreground' : 'text-muted-foreground',
                )}
              />
              <span className="min-w-0 flex-1">
                <span className="flex flex-wrap items-center gap-2">{title}</span>
                {subtitle ? (
                  <span className="mt-1 block text-[12px] text-muted-foreground">{subtitle}</span>
                ) : null}
              </span>
            </button>
          ) : (
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2">{title}</div>
              {subtitle ? (
                <div className="mt-1 text-[12px] text-muted-foreground">{subtitle}</div>
              ) : null}
            </div>
          )}
          {actions ? <div className="flex shrink-0 items-center gap-1">{actions}</div> : null}
        </div>

        {facts && facts.length > 0 ? (
          <dl className="mt-3.5 grid grid-cols-2 gap-x-4 gap-y-3">
            {facts.map((f) => (
              <div key={f.label} className="min-w-0">
                <dt className="eyebrow">{f.label}</dt>
                <dd className="mt-1 text-[13px] break-words">{f.value}</dd>
              </div>
            ))}
          </dl>
        ) : null}
      </div>

      {open && children ? (
        <div
          id={detailId}
          role="region"
          className="border-t border-border/70 bg-muted/50 px-4.5 py-3.5"
        >
          {children}
        </div>
      ) : null}
    </div>
  );
}

/**
 * The stack the cards live in, and its desktop counterpart's opposite number:
 * pages render `<RecordCards>` and a `<Table>` side by side, each hidden at the
 * other's breakpoint. `md` is where the tables stop fitting, not a round number.
 */
export function RecordCards({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <div data-slot="record-cards" className={cn('flex flex-col gap-2.5 md:hidden', className)}>
      {children}
    </div>
  );
}
