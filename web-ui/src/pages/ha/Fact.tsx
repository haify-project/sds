import { cn } from '@/lib/utils';

/** One labelled fact, label above value.
 *
 *  It replaced a leader-line row (label left, value flushed right). On a
 *  ~380px card that put a long mount path a card's width away from the word
 *  that named it, and made two cards' rows land at different heights. Stacked,
 *  the label sits on the value it names and every card's grid lines up. */
export function Fact({
  label,
  value,
  mono,
}: {
  label: string;
  value: React.ReactNode;
  mono?: boolean;
}) {
  return (
    <div className="min-w-0">
      <div className="eyebrow">{label}</div>
      <div
        className={cn(
          'mt-1 text-[13px] break-words',
          mono ? 'font-mono tabular-nums' : '',
        )}
      >
        {value}
      </div>
    </div>
  );
}
