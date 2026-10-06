import * as THREE from 'three';
import { RoundedBoxGeometry } from 'three/addons/geometries/RoundedBoxGeometry.js';
import type { Health } from './model';

// Palette and materials for the 3D view, one set per UI theme so the scene sits
// on the same paper as the rest of the console.

export interface Palette {
  dark: boolean;
  ground: string;
  slab: string;
  body: string;
  shell: string;
  trim: string;
  ink: string;
  screen: string;
  screenText: string;
  screenMuted: string;
  glass: string;
  accent: string;
  health: Record<Health, string>;
}

const HEALTH = { ok: '#22b573', warn: '#f5a524', bad: '#ef4444', idle: '#94a0b8' };

export const LIGHT: Palette = {
  dark: false,
  ground: '#eceef3',
  slab: '#f9fafc',
  body: '#ffffff',
  shell: '#e4e8f0',
  trim: '#c9d0de',
  ink: '#2a3142',
  screen: '#0f1724',
  screenText: '#e8eefc',
  screenMuted: '#8d9bb8',
  glass: '#b9cdfa',
  accent: '#2f5cb5',
  health: HEALTH,
};

export const DARK: Palette = {
  dark: true,
  ground: '#14171d',
  slab: '#1e222b',
  body: '#2c313d',
  shell: '#242934',
  trim: '#3a4150',
  ink: '#0c0f14',
  screen: '#070a10',
  screenText: '#e8eefc',
  screenMuted: '#7f8aa3',
  glass: '#5b7cc4',
  accent: '#7aa3ea',
  health: HEALTH,
};

export type Mats = ReturnType<typeof materials>;

const std = (color: THREE.ColorRepresentation, o: THREE.MeshStandardMaterialParameters = {}) =>
  new THREE.MeshStandardMaterial({ color, roughness: 0.55, metalness: 0, ...o });

export function materials(p: Palette) {
  return {
    slab: std(p.slab, { roughness: 0.7 }),
    body: std(p.body, { roughness: 0.42 }),
    shell: std(p.shell),
    trim: std(p.trim, { roughness: 0.5 }),
    ink: std(p.ink, { roughness: 0.35 }),
    metal: std(p.dark ? '#5d6678' : '#b7c1d8', { roughness: 0.3, metalness: 0.6 }),
    glass: std(p.glass, { roughness: 0.05, transparent: true, opacity: 0.22, depthWrite: false }),
    accent: std(p.accent, { roughness: 0.4 }),
  };
}

/** A glowing status colour: LEDs, cables, packets. */
export function glow(color: string, intensity = 1.6): THREE.MeshStandardMaterial {
  return new THREE.MeshStandardMaterial({ color, emissive: color, emissiveIntensity: intensity, roughness: 0.4 });
}

const geoCache = new Map<string, THREE.BufferGeometry>();

/** Rounded box, cached by size. */
export function rbox(w: number, h: number, d: number, r = 0.08): THREE.BufferGeometry {
  const rr = Math.min(r, w / 2 - 0.001, h / 2 - 0.001, d / 2 - 0.001);
  const key = `rb:${w}:${h}:${d}:${rr}`;
  let g = geoCache.get(key);
  if (!g) {
    g = rr > 0.005 ? new RoundedBoxGeometry(w, h, d, 2, rr) : new THREE.BoxGeometry(w, h, d);
    geoCache.set(key, g);
  }
  return g;
}

export function cyl(r: number, h: number, seg = 24): THREE.BufferGeometry {
  const key = `cy:${r}:${h}:${seg}`;
  let g = geoCache.get(key);
  if (!g) {
    g = new THREE.CylinderGeometry(r, r, h, seg);
    geoCache.set(key, g);
  }
  return g;
}

/** Cool blue → green → amber → red, for how full a pool is. */
const RAMP = ['#4f7cff', '#3fb8e8', '#33c48d', '#f5c542', '#f58a3d', '#ef4444'].map((c) => new THREE.Color(c));
export function ramp(t: number, out = new THREE.Color()): THREE.Color {
  const x = Math.max(0, Math.min(0.9999, t)) * (RAMP.length - 1);
  const i = Math.floor(x);
  return out.copy(RAMP[i]).lerp(RAMP[i + 1], x - i);
}

export const GATEWAY_COLOR: Record<string, string> = {
  nfs: '#3b82f6',
  iscsi: '#8b5cf6',
  nvmeof: '#f97316',
  nvme: '#f97316',
  smb: '#14b8a6',
};

export function canvasTexture(w: number, h: number): { tex: THREE.CanvasTexture; ctx: CanvasRenderingContext2D } {
  const c = document.createElement('canvas');
  c.width = w;
  c.height = h;
  const tex = new THREE.CanvasTexture(c);
  tex.colorSpace = THREE.SRGBColorSpace;
  tex.anisotropy = 8;
  return { tex, ctx: c.getContext('2d')! };
}
