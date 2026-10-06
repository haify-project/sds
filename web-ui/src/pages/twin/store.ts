import { useSyncExternalStore } from 'react';
import type * as THREE from 'three';

// UI state shared between the canvas and the DOM around it. A plain external
// store: the scene reads it inside useFrame without re-rendering, the panels
// subscribe to the slices they show.

export type Selection = string | null; // "node:<name>", "link:<a>|<b>", "gw:<id>"

export type CamOp = { op: 'home' | 'focus' | 'in' | 'out'; id?: string; seq: number };

interface TwinUI {
  selected: Selection;
  hovered: Selection;
  labels: boolean;
  spin: boolean;
  cam: CamOp | null;
  /** Things an event just touched, with when, so they pulse for a while. */
  flashes: Map<string, { at: number; severity: 'warning' | 'critical' | 'info' }>;
}

// On a phone every tag at once buries the picture; alarms still show.
const wide = typeof window === 'undefined' || window.innerWidth >= 768;

let state: TwinUI = { selected: null, hovered: null, labels: wide, spin: false, cam: null, flashes: new Map() };
const subs = new Set<() => void>();
let camSeq = 0;

export const twin = {
  get: () => state,
  set(patch: Partial<TwinUI>) {
    state = { ...state, ...patch };
    subs.forEach((f) => f());
  },
  select(id: Selection, focus = false) {
    twin.set({ selected: id, ...(focus && id ? { cam: { op: 'focus', id, seq: ++camSeq } } : {}) });
  },
  camera(op: CamOp['op']) {
    twin.set({ cam: { op, seq: ++camSeq } });
  },
  flash(id: string, severity: 'warning' | 'critical' | 'info') {
    const flashes = new Map(state.flashes);
    flashes.set(id, { at: performance.now(), severity });
    twin.set({ flashes });
  },
  subscribe(f: () => void) {
    subs.add(f);
    return () => subs.delete(f);
  },
};

export function useTwin<T>(pick: (s: TwinUI) => T): T {
  return useSyncExternalStore(twin.subscribe, () => pick(state));
}

/** Where each thing's name tag sits, in world space: filled in by the scene. */
export const anchors = new Map<string, THREE.Object3D>();
/** The DOM name tags, moved every frame by the label projector. */
export const labelEls = new Map<string, HTMLElement>();
