import { cn } from '@/lib/utils';

export type SegmentedFilterOption<T extends string> = {
  value: T;
  label: string;
  count?: number;
};

/**
 * A small, closed set of filters — three or four, all visible at once — where a
 * select would hide the options and their counts behind a click. The counts are
 * part of the label because "Syncing · 0" is itself the answer, and a chip that
 * leads nowhere is worth seeing before it is clicked.
 *
 * A group of toggle buttons, not a tablist: there is no tabpanel to point
 * `aria-controls` at, no roving tabindex and no arrow-key handling, so
 * announcing tabs would promise a keyboard model this does not implement.
 * `aria-pressed` says exactly what is true — one of these buttons is currently
 * on — and leaves Tab working the way the markup already behaves.
 */
export function SegmentedFilter<T extends string>({
  value,
  onChange,
  options,
  className,
  'aria-label': ariaLabel,
}: {
  value: T;
  onChange: (value: T) => void;
  options: SegmentedFilterOption<T>[];
  className?: string;
  'aria-label'?: string;
}) {
  return (
    <div
      data-slot="segmented-filter"
      role="group"
      aria-label={ariaLabel}
      className={cn('inline-flex gap-[3px] rounded-lg bg-secondary p-[3px]', className)}
    >
      {options.map((option) => {
        const selected = option.value === value;
        return (
          <button
            key={option.value}
            type="button"
            aria-pressed={selected}
            onClick={() => onChange(option.value)}
            className={cn(
              'h-[30px] rounded-[7px] px-2.5 text-[12.5px] text-secondary-foreground transition-colors',
              'outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50',
              selected
                ? 'bg-card font-medium text-foreground shadow-[0_0_0_1px_var(--border)]'
                : 'hover:text-foreground'
            )}
          >
            {option.label}
            {option.count !== undefined ? ` · ${option.count}` : ''}
          </button>
        );
      })}
    </div>
  );
}
