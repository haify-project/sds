import type { ReactNode } from 'react';
import { Home, Minus, Plus } from 'lucide-react';
import { cn } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';

// The camera buttons every 3D view carries: reset, zoom in, zoom out, plus
// whatever a view adds below them. One look for the cluster view and the
// per-resource views.

export type ViewOp = 'home' | 'in' | 'out';

export function ViewTool({
  label,
  onClick,
  active,
  children,
}: {
  label: string;
  onClick: () => void;
  active?: boolean;
  children: ReactNode;
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          variant={active ? 'default' : 'ghost'}
          size="icon"
          className="size-8"
          aria-label={label}
          aria-pressed={active}
          onClick={onClick}
        >
          {children}
        </Button>
      </TooltipTrigger>
      <TooltipContent side="left">{label}</TooltipContent>
    </Tooltip>
  );
}

export function ViewControls({
  onCamera,
  className,
  children,
}: {
  onCamera: (op: ViewOp) => void;
  className?: string;
  children?: ReactNode;
}) {
  return (
    <div
      role="toolbar"
      aria-label="View"
      aria-orientation="vertical"
      className={cn(
        'pointer-events-auto flex flex-col gap-0.5 rounded-xl border border-border bg-card/90 p-1 shadow-lg backdrop-blur',
        className,
      )}
    >
      <ViewTool label="Reset view" onClick={() => onCamera('home')}>
        <Home className="size-4" />
      </ViewTool>
      <ViewTool label="Zoom in" onClick={() => onCamera('in')}>
        <Plus className="size-4" />
      </ViewTool>
      <ViewTool label="Zoom out" onClick={() => onCamera('out')}>
        <Minus className="size-4" />
      </ViewTool>
      {children}
    </div>
  );
}
