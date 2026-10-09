/**
 * shadcn-style theme presets. Each color preset only overrides the accent
 * tokens (primary / ring) for light and dark; the background, borders and text
 * come from index.css and stay put — which is now warm paper and blue-tinted
 * ink, not monochrome, so "Neutral" no longer reproduces black & white and is
 * no longer the default. "Haify" is: it restates the shipped accent, so picking
 * it always returns the console to the designed look.
 */

export type ThemeTokens = {
  /** Accent fill — buttons, progress, active states. */
  primary: string;
  /** Readable text on top of the accent fill. */
  primaryForeground: string;
  /** Focus ring color. */
  ring: string;
};

export type ColorTheme = {
  /** Stable id persisted to localStorage. */
  name: string;
  /** Human label shown in the panel. */
  label: string;
  light: ThemeTokens;
  dark: ThemeTokens;
};

const WHITE = 'oklch(0.985 0 0)';
const INK = 'oklch(0.205 0 0)';

export const BASE_COLORS: ColorTheme[] = [
  {
    name: 'haify',
    label: 'Haify',
    light: {
      primary: 'oklch(0.46 0.115 255)',
      primaryForeground: 'oklch(0.99 0 0)',
      ring: 'oklch(0.46 0.115 255)',
    },
    dark: {
      primary: 'oklch(0.72 0.11 255)',
      primaryForeground: 'oklch(0.17 0.008 260)',
      ring: 'oklch(0.72 0.11 255)',
    },
  },
  {
    name: 'neutral',
    label: 'Neutral',
    light: { primary: INK, primaryForeground: WHITE, ring: 'oklch(0.708 0 0)' },
    dark: {
      primary: 'oklch(0.922 0 0)',
      primaryForeground: INK,
      ring: 'oklch(0.556 0 0)',
    },
  },
  {
    name: 'red',
    label: 'Red',
    light: {
      primary: 'oklch(0.577 0.245 27.3)',
      primaryForeground: WHITE,
      ring: 'oklch(0.577 0.245 27.3)',
    },
    dark: {
      primary: 'oklch(0.637 0.237 25.3)',
      primaryForeground: WHITE,
      ring: 'oklch(0.637 0.237 25.3)',
    },
  },
  {
    name: 'rose',
    label: 'Rose',
    light: {
      primary: 'oklch(0.586 0.222 17)',
      primaryForeground: WHITE,
      ring: 'oklch(0.586 0.222 17)',
    },
    dark: {
      primary: 'oklch(0.645 0.21 16)',
      primaryForeground: WHITE,
      ring: 'oklch(0.645 0.21 16)',
    },
  },
  {
    name: 'orange',
    label: 'Orange',
    light: {
      primary: 'oklch(0.646 0.183 51)',
      primaryForeground: WHITE,
      ring: 'oklch(0.646 0.183 51)',
    },
    dark: {
      primary: 'oklch(0.7 0.17 49)',
      primaryForeground: WHITE,
      ring: 'oklch(0.7 0.17 49)',
    },
  },
  {
    name: 'amber',
    label: 'Amber',
    light: {
      primary: 'oklch(0.769 0.16 70)',
      primaryForeground: INK,
      ring: 'oklch(0.769 0.16 70)',
    },
    dark: {
      primary: 'oklch(0.8 0.155 70)',
      primaryForeground: INK,
      ring: 'oklch(0.8 0.155 70)',
    },
  },
  {
    name: 'yellow',
    label: 'Yellow',
    light: {
      primary: 'oklch(0.82 0.16 88)',
      primaryForeground: INK,
      ring: 'oklch(0.82 0.16 88)',
    },
    dark: {
      primary: 'oklch(0.85 0.16 90)',
      primaryForeground: INK,
      ring: 'oklch(0.85 0.16 90)',
    },
  },
  {
    name: 'green',
    label: 'Green',
    light: {
      primary: 'oklch(0.627 0.17 149)',
      primaryForeground: WHITE,
      ring: 'oklch(0.627 0.17 149)',
    },
    dark: {
      primary: 'oklch(0.696 0.17 149)',
      primaryForeground: INK,
      ring: 'oklch(0.696 0.17 149)',
    },
  },
  {
    name: 'emerald',
    label: 'Emerald',
    light: {
      primary: 'oklch(0.63 0.15 165)',
      primaryForeground: WHITE,
      ring: 'oklch(0.63 0.15 165)',
    },
    dark: {
      primary: 'oklch(0.7 0.15 165)',
      primaryForeground: INK,
      ring: 'oklch(0.7 0.15 165)',
    },
  },
  {
    name: 'teal',
    label: 'Teal',
    light: {
      primary: 'oklch(0.62 0.12 185)',
      primaryForeground: WHITE,
      ring: 'oklch(0.62 0.12 185)',
    },
    dark: {
      primary: 'oklch(0.72 0.13 185)',
      primaryForeground: INK,
      ring: 'oklch(0.72 0.13 185)',
    },
  },
  {
    name: 'cyan',
    label: 'Cyan',
    light: {
      primary: 'oklch(0.62 0.12 210)',
      primaryForeground: WHITE,
      ring: 'oklch(0.62 0.12 210)',
    },
    dark: {
      primary: 'oklch(0.72 0.13 210)',
      primaryForeground: INK,
      ring: 'oklch(0.72 0.13 210)',
    },
  },
  {
    name: 'blue',
    label: 'Blue',
    light: {
      primary: 'oklch(0.546 0.215 262.9)',
      primaryForeground: WHITE,
      ring: 'oklch(0.546 0.215 262.9)',
    },
    dark: {
      primary: 'oklch(0.623 0.19 260)',
      primaryForeground: WHITE,
      ring: 'oklch(0.623 0.19 260)',
    },
  },
  {
    name: 'indigo',
    label: 'Indigo',
    light: {
      primary: 'oklch(0.51 0.21 277)',
      primaryForeground: WHITE,
      ring: 'oklch(0.51 0.21 277)',
    },
    dark: {
      primary: 'oklch(0.6 0.19 276)',
      primaryForeground: WHITE,
      ring: 'oklch(0.6 0.19 276)',
    },
  },
  {
    name: 'violet',
    label: 'Violet',
    light: {
      primary: 'oklch(0.541 0.246 293)',
      primaryForeground: WHITE,
      ring: 'oklch(0.541 0.246 293)',
    },
    dark: {
      primary: 'oklch(0.62 0.21 292)',
      primaryForeground: WHITE,
      ring: 'oklch(0.62 0.21 292)',
    },
  },
];

/** Radius presets, in rem. */
export const RADII = [0, 0.3, 0.5, 0.625, 0.75, 1] as const;

export const DEFAULT_COLOR = 'haify';
export const DEFAULT_RADIUS = 0.625;

export function getColorTheme(name: string): ColorTheme {
  // Fall back to the named default, not to whatever happens to sit at index 0
  // — the two coincide today only because `haify` is first in the list.
  return (
    BASE_COLORS.find((c) => c.name === name) ??
    BASE_COLORS.find((c) => c.name === DEFAULT_COLOR)!
  );
}
