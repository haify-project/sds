import { useMemo } from 'react';
import { useFrame } from '@react-three/fiber';
import * as THREE from 'three';
import { boxCorners } from '@/components/topology3d/fit';
import type { TwinModel } from '../model';
import { anchors, labelEls, labelSpots } from '../store';

// Moves each DOM name tag to where its thing is on screen, every frame, and
// keeps them from piling up. Node tags, alarms and whatever is selected or
// hovered always show; a quiet replication or gateway tag takes the first spot
// along its line that is clear of those and of the nodes' screens and towers,
// and steps aside (hides) when there is none. Hovering the line brings it back.

interface Rect {
  l: number;
  t: number;
  r: number;
  b: number;
}

const GAP = 4;
const hit = (a: Rect, b: Rect) => a.l < b.r + GAP && b.l < a.r + GAP && a.t < b.b + GAP && b.t < a.b + GAP;

/** The part of a node a tag must not cover: tower and monitor, not the desk edge. */
const BODY = boxCorners([-2.7, 0.4, -0.6], [2.8, 3.9, 1.4]);

interface Entry {
  el: HTMLElement;
  rank: number;
  link: boolean;
  w: number;
  h: number;
  spots: { x: number; y: number; z: number }[];
}

export function LabelProjector({ model }: { model: TwinModel }) {
  const v = useMemo(() => new THREE.Vector3(), []);
  useFrame(({ camera, size }) => {
    const toScreen = (p: THREE.Vector3) => {
      v.copy(p).project(camera);
      return { x: (v.x * 0.5 + 0.5) * size.width, y: (-v.y * 0.5 + 0.5) * size.height, z: v.z };
    };

    // Screen boxes of the nodes' bodies, which quiet tags keep off.
    const bodies: Rect[] = [];
    for (const n of model.nodes) {
      const r: Rect = { l: Infinity, t: Infinity, r: -Infinity, b: -Infinity };
      let behind = false;
      for (const c of BODY) {
        const s = toScreen(v.set(c.x + n.x, c.y, c.z + n.z));
        if (s.z > 1) behind = true;
        r.l = Math.min(r.l, s.x);
        r.r = Math.max(r.r, s.x);
        r.t = Math.min(r.t, s.y);
        r.b = Math.max(r.b, s.y);
      }
      if (!behind) bodies.push(r);
    }

    // Read every size first, then write: one layout per frame at most.
    const entries: Entry[] = [];
    for (const [id, el] of labelEls) {
      const world = labelSpots.get(id) ?? [];
      const a = anchors.get(id);
      const pts = world.length ? world : a ? [a.getWorldPosition(new THREE.Vector3())] : [];
      const spots = pts.map(toScreen).filter((s) => s.z <= 1);
      if (!spots.length) {
        el.style.visibility = 'hidden';
        continue;
      }
      entries.push({ el, rank: Number(el.dataset.rank ?? 0), link: id.startsWith('link:'), w: el.offsetWidth, h: el.offsetHeight, spots });
    }

    // Most important first, then nearest first.
    entries.sort((x, y) => y.rank - x.rank || x.spots[0].z - y.spots[0].z);
    const placed: Rect[] = [];
    for (const e of entries) {
      if (e.w === 0) continue; // switched off: display none
      const rectAt = (s: { x: number; y: number }): Rect => ({ l: s.x - e.w / 2, t: s.y - e.h, r: s.x + e.w / 2, b: s.y });
      let spot: { x: number; y: number; z: number } | undefined;
      if (e.rank >= 2) {
        spot = e.spots[0];
      } else {
        spot = e.spots.find((s) => {
          const r = rectAt(s);
          if (r.l < 0 || r.t < 0 || r.r > size.width || r.b > size.height) return false;
          if (placed.some((p) => hit(r, p))) return false;
          return !(e.link && bodies.some((p) => hit(r, p)));
        });
      }
      if (!spot) {
        e.el.style.visibility = 'hidden';
        continue;
      }
      placed.push(rectAt(spot));
      e.el.style.visibility = '';
      e.el.style.transform = `translate3d(${spot.x.toFixed(1)}px, ${spot.y.toFixed(1)}px, 0) translate(-50%, -100%)`;
      e.el.style.zIndex = String(Math.round((1 - spot.z) * 1e4) + e.rank * 1e5);
    }
  });
  return null;
}
