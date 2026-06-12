import { useTheme } from 'next-themes';
import { Check, Laptop, Moon, Palette, RotateCcw, Sun } from 'lucide-react';
import { BASE_COLORS, RADII } from '@/lib/themes';
import { useThemeConfig } from '@/components/ThemeProvider';
import { Button } from '@/components/ui/button';
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import { cn } from '@/lib/utils';

const MODES = [
  { value: 'light', label: 'Light', icon: Sun },
  { value: 'dark', label: 'Dark', icon: Moon },
  { value: 'system', label: 'System', icon: Laptop },
] as const;

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <p className="mb-2 text-xs font-medium text-muted-foreground">{children}</p>
  );
}

export function ThemeCustomizer() {
  const { color, radius, setColor, setRadius, reset } = useThemeConfig();
  const { theme, setTheme, resolvedTheme } = useTheme();

  return (
    <Popover>
      <Tooltip>
        <TooltipTrigger asChild>
          <PopoverTrigger asChild>
            <Button
              variant="outline"
              size="icon"
              className="h-8 w-8"
              aria-label="Customize theme"
            >
              <Palette className="h-4 w-4" />
            </Button>
          </PopoverTrigger>
        </TooltipTrigger>
        <TooltipContent>Customize theme</TooltipContent>
      </Tooltip>

      <PopoverContent align="end" className="w-72">
        <div className="mb-3 flex items-center justify-between">
          <div>
            <p className="text-sm font-semibold">Theme</p>
            <p className="text-xs text-muted-foreground">
              Customize the look and feel.
            </p>
          </div>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7 text-muted-foreground"
                onClick={reset}
                aria-label="Reset theme"
              >
                <RotateCcw className="h-3.5 w-3.5" />
              </Button>
            </TooltipTrigger>
            <TooltipContent>Reset to default</TooltipContent>
          </Tooltip>
        </div>

        {/* Accent color */}
        <div className="mb-4">
          <SectionLabel>Color</SectionLabel>
          <div className="grid grid-cols-3 gap-2">
            {BASE_COLORS.map((c) => {
              const isActive = color === c.name;
              const swatch =
                resolvedTheme === 'dark' ? c.dark.primary : c.light.primary;
              return (
                <button
                  key={c.name}
                  type="button"
                  onClick={() => setColor(c.name)}
                  className={cn(
                    'flex items-center gap-1.5 rounded-md border px-2 py-1.5 text-xs font-medium transition-colors',
                    isActive
                      ? 'border-foreground/30 bg-accent'
                      : 'border-border hover:bg-accent/60',
                  )}
                >
                  <span
                    className="flex h-4 w-4 shrink-0 items-center justify-center rounded-full"
                    style={{ backgroundColor: swatch }}
                  >
                    {isActive && (
                      <Check className="h-3 w-3 text-background" strokeWidth={3} />
                    )}
                  </span>
                  <span className="truncate">{c.label}</span>
                </button>
              );
            })}
          </div>
        </div>

        {/* Radius */}
        <div className="mb-4">
          <SectionLabel>Radius</SectionLabel>
          <div className="grid grid-cols-6 gap-1.5">
            {RADII.map((r) => (
              <button
                key={r}
                type="button"
                onClick={() => setRadius(r)}
                className={cn(
                  'rounded-md border py-1 text-xs transition-colors',
                  radius === r
                    ? 'border-foreground/30 bg-accent font-medium'
                    : 'border-border text-muted-foreground hover:bg-accent/60',
                )}
              >
                {r}
              </button>
            ))}
          </div>
        </div>

        {/* Mode */}
        <div>
          <SectionLabel>Mode</SectionLabel>
          <div className="grid grid-cols-3 gap-1.5">
            {MODES.map((m) => {
              const isActive = theme === m.value;
              return (
                <button
                  key={m.value}
                  type="button"
                  onClick={() => setTheme(m.value)}
                  className={cn(
                    'flex items-center justify-center gap-1.5 rounded-md border py-1.5 text-xs transition-colors',
                    isActive
                      ? 'border-foreground/30 bg-accent font-medium'
                      : 'border-border text-muted-foreground hover:bg-accent/60',
                  )}
                >
                  <m.icon className="h-3.5 w-3.5" />
                  {m.label}
                </button>
              );
            })}
          </div>
        </div>
      </PopoverContent>
    </Popover>
  );
}
