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

const COLOR_KEY = 'sds-theme-color';
const RADIUS_KEY = 'sds-theme-radius';

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
    root.style.setProperty('--sidebar-primary', t.primary);
    root.style.setProperty('--sidebar-primary-foreground', t.primaryForeground);
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
