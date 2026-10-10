import { cn } from '@/lib/utils';

/** The Haify mark: the same drawing as the favicon, the site and the docs. */
export function HaifyMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 100 100" aria-hidden="true" className={cn('size-6 shrink-0', className)}>
      <rect fill="#0a0e15" height="100" rx="24" width="100" />
      <rect fill="none" height="70" rx="15" stroke="#44e0ad" strokeWidth="5" width="70" x="15" y="15" />
      <path d="m31 41h38m-38 11h38m-38 11h24" stroke="#44e0ad" strokeLinecap="round" strokeWidth="6.5" />
      <circle cx="70" cy="63" fill="#f2b45b" r="6" />
    </svg>
  );
}
