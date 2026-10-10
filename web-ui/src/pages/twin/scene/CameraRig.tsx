import { useEffect, useMemo, useRef } from 'react';
import { useFrame, useThree } from '@react-three/fiber';
import * as THREE from 'three';
import type { OrbitControls } from 'three/addons/controls/OrbitControls.js';
import { boxCorners, fitView, Flight, viewFrom, type Framing } from '@/components/topology3d/fit';
import type { TwinModel } from '../model';
import { anchors, twin } from '../store';
import { TOWER } from './ComputerNode';

// Scripted camera moves: the fitted home view, zoom, and flying to whatever
// was double-clicked. Until someone turns or zooms the room, it keeps every
// node framed as the window resizes or nodes come and go.

/** The home view looks from the front, this far above the floor (radians). */
const ELEVATION = 0.56;
const DIR = viewFrom(ELEVATION);
/** Room kept clear on the right for the camera buttons, and at the edges. */
const INSETS = { right: 60, bottom: 16, left: 16 };

/** What has to be on screen: every node with its name tag, and every arc's crest. */
export function framePoints(model: TwinModel): THREE.Vector3[] {
  const pts: THREE.Vector3[] = [];
  for (const n of model.nodes) {
    const front = n.gateways.length ? 4.0 : 2.7;
    pts.push(...boxCorners([n.x - 3.7, 0, n.z - 2.7], [n.x + 3.7, 5.4, n.z + front]));
  }
  const at = new Map(model.nodes.map((n) => [n.name, n]));
  for (const l of model.links) {
    const a = at.get(l.a);
    const b = at.get(l.b);
    if (!a || !b) continue;
    const p0 = new THREE.Vector3(a.x + TOWER.x, TOWER.top - 0.2, a.z);
    const p2 = new THREE.Vector3(b.x + TOWER.x, TOWER.top - 0.2, b.z);
    // A quadratic arc crests at half its control point's rise.
    const crest = p0.clone().lerp(p2, 0.5);
    crest.y += (2.4 + p0.distanceTo(p2) * 0.18) / 2 + 0.6;
    pts.push(crest);
  }
  return pts;
}

export function homeFraming(points: THREE.Vector3[], fov: number, width: number, height: number): Framing {
  // Spare height goes below the room, where the event feed sits.
  return fitView(points, DIR, fov, width, height, { top: twin.get().insetTop, ...INSETS }, 0.06, 0.2);
}

export function CameraRig({ model, radius }: { model: TwinModel; radius: number }) {
  const camera = useThree((s) => s.camera) as THREE.PerspectiveCamera;
  const controls = useThree((s) => s.controls) as unknown as OrbitControls | null;
  const size = useThree((s) => s.size);
  const fly = useRef<Flight | null>(null);
  const seq = useRef(0);
  /** Turned or zoomed by hand since the last reset: leave the camera alone. */
  const moved = useRef(false);
  const fitKey = useRef('');
  const home = useRef<Framing | null>(null);

  const points = useMemo(() => framePoints(model), [model]);
  const pointsKey = useMemo(() => points.map((p) => `${p.x.toFixed(1)},${p.y.toFixed(1)},${p.z.toFixed(1)}`).join(';'), [points]);

  useFrame((_, dt) => {
    if (!controls) return;
    const ui = twin.get();

    // Refit when the canvas, the overlay above it, or the nodes change.
    const key = `${size.width}x${size.height}|${ui.insetTop}|${pointsKey}`;
    if (key !== fitKey.current) {
      const first = fitKey.current === '';
      fitKey.current = key;
      const f = homeFraming(points, camera.fov, size.width, size.height);
      home.current = f;
      controls.maxDistance = Math.max(radius * 5 + 60, f.distance * 2);
      if (first || (!moved.current && !ui.spin)) {
        if (first || !fly.current) {
          controls.target.copy(f.target);
          camera.position.copy(f.position);
        } else {
          fly.current = new Flight(0.5, controls.target.clone(), camera.position.clone(), f.target, f.position);
        }
      }
    }

    const cam = ui.cam;
    if (cam && cam.seq !== seq.current) {
      seq.current = cam.seq;
      const target = controls.target;
      const sph = new THREE.Spherical().setFromVector3(camera.position.clone().sub(target));
      const go = (toT: THREE.Vector3, toP: THREE.Vector3, dur: number) => {
        fly.current = new Flight(dur, target.clone(), camera.position.clone(), toT, toP);
      };
      if (cam.op === 'home' && home.current) {
        moved.current = false;
        go(home.current.target, home.current.position, 1.1);
      } else if (cam.op === 'in' || cam.op === 'out') {
        moved.current = true;
        sph.radius = THREE.MathUtils.clamp(sph.radius * (cam.op === 'in' ? 0.7 : 1.4), controls.minDistance, controls.maxDistance);
        go(target.clone(), target.clone().add(new THREE.Vector3().setFromSpherical(sph)), 0.45);
      } else if (cam.op === 'focus' && cam.id) {
        const a = anchors.get(cam.id);
        if (a) {
          moved.current = true;
          const to = a.getWorldPosition(new THREE.Vector3());
          to.y = Math.max(1.5, to.y - 2.5);
          sph.radius = cam.id.startsWith('link:') ? 26 : 18;
          sph.phi = Math.min(sph.phi, 1.0);
          go(to, to.clone().add(new THREE.Vector3().setFromSpherical(sph)), 1.1);
        }
      }
    }
    const f = fly.current;
    if (f && !f.step(dt, controls.target, camera.position)) fly.current = null;
  });

  useEffect(() => {
    if (!controls) return;
    const grab = () => {
      fly.current = null;
      moved.current = true;
    };
    controls.addEventListener('start', grab);
    return () => controls.removeEventListener('start', grab);
  }, [controls]);
  return null;
}
