import * as THREE from 'three';

// Framing a set of points with a perspective camera that looks from a fixed
// direction. Shared by the cluster view and the per-resource view, so "reset"
// means the same thing in both.

/** Screen space kept clear on each side, in CSS pixels (overlays sit there). */
export interface Insets {
  top: number;
  right: number;
  bottom: number;
  left: number;
}

export interface Framing {
  /** Where the camera stands. */
  position: THREE.Vector3;
  /** The point it orbits around, on its line of sight. */
  target: THREE.Vector3;
  /** Distance from position to target. */
  distance: number;
}

/** Corners of an axis-aligned box, for feeding fitView. */
export function boxCorners(min: [number, number, number], max: [number, number, number]): THREE.Vector3[] {
  const out: THREE.Vector3[] = [];
  for (const x of [min[0], max[0]])
    for (const y of [min[1], max[1]])
      for (const z of [min[2], max[2]]) out.push(new THREE.Vector3(x, y, z));
  return out;
}

/** A direction from the target to the camera: straight in front, raised by `elevation` radians. */
export function viewFrom(elevation: number, azimuth = 0): THREE.Vector3 {
  return new THREE.Vector3(
    Math.sin(azimuth) * Math.cos(elevation),
    Math.sin(elevation),
    Math.cos(azimuth) * Math.cos(elevation),
  );
}

/**
 * The smallest distance along one screen axis at which every point fits
 * between the normalised bounds lo..hi (in tan-of-half-fov units), and the
 * sideways shift that centres them there. `a` is each point's offset along the
 * axis, `q` its depth offset; a point's screen coordinate is (a - s) / (d + q).
 */
function fitAxis(
  a: number[],
  q: number[],
  lo: number,
  hi: number,
  align = 0.5,
): { d: number; shift: (d: number) => number } {
  let d = 0;
  // Two points i, j bound the shift from both sides; they are compatible once
  // a_i - hi(d + q_i) <= a_j - lo(d + q_j).
  for (let i = 0; i < a.length; i++)
    for (let j = 0; j < a.length; j++) {
      const need = (a[i] - a[j] - hi * q[i] + lo * q[j]) / (hi - lo);
      if (need > d) d = need;
    }
  const shift = (dd: number) => {
    let low = -Infinity;
    let high = Infinity;
    for (let i = 0; i < a.length; i++) {
      low = Math.max(low, a[i] - hi * (dd + q[i]));
      high = Math.min(high, a[i] - lo * (dd + q[i]));
    }
    // low puts the points against the high bound, high against the low one.
    return low + (high - low) * align;
  };
  return { d, shift };
}

/**
 * Frames `points` from direction `dir` (unit vector, target → camera) for a
 * camera with vertical field of view `fovDeg`, on a canvas of `width`×`height`
 * pixels with `insets` kept clear and `margin` (a fraction of the remaining
 * space) left around the points. When the points fit with room to spare
 * vertically, `alignY` places them: 0 at the top, 0.5 centred, 1 at the bottom.
 */
export function fitView(
  points: THREE.Vector3[],
  dir: THREE.Vector3,
  fovDeg: number,
  width: number,
  height: number,
  insets: Insets,
  margin = 0.06,
  alignY = 0.5,
  near = 0.5,
): Framing {
  const w = Math.max(1, width);
  const h = Math.max(1, height);
  const tanV = Math.tan(THREE.MathUtils.degToRad(fovDeg) / 2);
  const tanH = tanV * (w / h);

  // Usable screen area in normalised device coordinates, shrunk by the margin.
  // A tiny canvas (or insets larger than it) still gets a usable window.
  const ndc = (from: number, to: number, size: number) => {
    let lo = -1 + (2 * from) / size;
    let hi = 1 - (2 * to) / size;
    if (hi - lo < 0.4) {
      const mid = (lo + hi) / 2;
      lo = mid - 0.2;
      hi = mid + 0.2;
    }
    const pad = ((hi - lo) * margin) / 2;
    return [lo + pad, hi - pad];
  };
  const [xl, xh] = ndc(insets.left, insets.right, w);
  const [yl, yh] = ndc(insets.bottom, insets.top, h);

  const back = dir.clone().normalize();
  const fwd = back.clone().negate();
  const right = new THREE.Vector3().crossVectors(fwd, new THREE.Vector3(0, 1, 0)).normalize();
  const up = new THREE.Vector3().crossVectors(right, fwd).normalize();

  const origin = new THREE.Vector3();
  if (points.length) {
    for (const p of points) origin.add(p);
    origin.divideScalar(points.length);
  }
  const rel = points.map((p) => p.clone().sub(origin));
  const q = rel.map((p) => p.dot(fwd));
  const fx = fitAxis(
    rel.map((p) => p.dot(right) / tanH),
    q,
    xl,
    xh,
  );
  const fy = fitAxis(
    rel.map((p) => p.dot(up) / tanV),
    q,
    yl,
    yh,
    alignY,
  );
  // Never closer than the near plane allows for the nearest point.
  const minD = Math.max(0, ...q.map((v) => near * 1.5 - v));
  const d = Math.max(fx.d, fy.d, minD, 1);
  const sx = fx.shift(d) * tanH;
  const sy = fy.shift(d) * tanV;

  const target = origin.clone().addScaledVector(right, sx).addScaledVector(up, sy);
  const position = target.clone().addScaledVector(back, d);
  return { position, target, distance: d };
}

const ease = (t: number) => (t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2);

/** A scripted camera move between two (target, position) pairs. */
export class Flight {
  private t = 0;
  constructor(
    private readonly dur: number,
    private readonly fromT: THREE.Vector3,
    private readonly fromP: THREE.Vector3,
    private readonly toT: THREE.Vector3,
    private readonly toP: THREE.Vector3,
  ) {}

  /** Advances by dt seconds, moving target and camera; false once arrived. */
  step(dt: number, target: THREE.Vector3, position: THREE.Vector3): boolean {
    this.t += dt;
    const k = ease(Math.min(1, this.t / this.dur));
    target.lerpVectors(this.fromT, this.toT, k);
    position.lerpVectors(this.fromP, this.toP, k);
    return this.t < this.dur;
  }
}
