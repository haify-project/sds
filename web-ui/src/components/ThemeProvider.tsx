import {
  createContext,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from 'react';
import { ThemeProvider as NextThemesProvider, useTheme } from 'next-themes';
import {
  DEFAULT_COLOR,
  DEFAULT_RADIUS,
  getColorTheme,
} from '@/lib/themes';

// `.v2` re-defaults every existing install once. The old key was written on
// *any* pick, including the shipped default, so a stored `neutral` is
// indistinguishable from "never chose anything" — and `neutral`'s black
// primary was drawn for a monochrome white page, not for the warm paper and
// blue --accent the redesign ships. Bumping the key is the honest, code-free
// migration: everyone lands back on the designed look, and anyone who
// deliberately re-picks `neutral` keeps it from then on.
const COLOR_KEY = 'haify-theme-color.v2';
const RADIUS_KEY = 'haify-theme-radius';

type ThemeConfig = {
  color: string;
  radius: number;
  setColor: (c: string) => void;
  setRadius: (r: number) => void;
  reset: () => void;
};

const ThemeConfigContext = createContext<ThemeConfig | null>(null);

export function useThemeConfig(): ThemeConfig {
  const ctx = useContext(ThemeConfigContext);
  if (!ctx) throw new Error('useThemeConfig must be used within ThemeProvider');
  return ctx;
}

function readStored<T>(key: string, fallback: T, parse: (v: string) => T): T {
  if (typeof window === 'undefined') return fallback;
  const raw = window.localStorage.getItem(key);
  if (raw == null) return fallback;
  try {
    return parse(raw);
  } catch {
    return fallback;
  }
}

/** Applies the selected accent + radius as inline CSS vars on :root. */
function ThemeTokens({ children }: { children: ReactNode }) {
  const { resolvedTheme } = useTheme();
  const [color, setColorState] = useState(() =>
    readStored(COLOR_KEY, DEFAULT_COLOR, (v) => v),
  );
  const [radius, setRadiusState] = useState(() =>
    readStored(RADIUS_KEY, DEFAULT_RADIUS, (v) => Number(v)),
  );

  useEffect(() => {
    const theme = getColorTheme(color);
    const t = resolvedTheme === 'dark' ? theme.dark : theme.light;
    const root = document.documentElement;
    root.style.setProperty('--primary', t.primary);
    root.style.setProperty('--primary-foreground', t.primaryForeground);
    root.style.setProperty('--ring', t.ring);
    // Only the ring, not --sidebar-primary: the redesign shrank the accent's
    // sidebar footprint to `text-primary` on the active row's icon, so the
    // pill-fill tokens have no reader left.
    root.style.setProperty('--sidebar-ring', t.ring);
    root.style.setProperty('--radius', `${radius}rem`);
  }, [color, radius, resolvedTheme]);

  const setColor = (c: string) => {
    setColorState(c);
    window.localStorage.setItem(COLOR_KEY, c);
  };
  const setRadius = (r: number) => {
    setRadiusState(r);
    window.localStorage.setItem(RADIUS_KEY, String(r));
  };
  const reset = () => {
    setColor(DEFAULT_COLOR);
    setRadius(DEFAULT_RADIUS);
  };

  return (
    <ThemeConfigContext.Provider
      value={{ color, radius, setColor, setRadius, reset }}
    >
      {children}
    </ThemeConfigContext.Provider>
  );
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  return (
    <NextThemesProvider
      attribute="class"
      defaultTheme="light"
      enableSystem
      disableTransitionOnChange
    >
      <ThemeTokens>{children}</ThemeTokens>
    </NextThemesProvider>
  );
}
