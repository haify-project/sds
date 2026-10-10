import { useEffect, useMemo, useRef } from 'react';
import { useFrame, useThree } from '@react-three/fiber';
import * as THREE from 'three';
import { OrbitControls } from 'three/addons/controls/OrbitControls.js';
import { fitView, Flight, type Framing, type Insets } from './fit';
import type { ViewOp } from './ViewControls';

// Camera handling for a 3D view that sits inside a scrolling page: drag to
// turn (the wheel keeps scrolling the page), the buttons zoom and reset, and
// until someone moves it the camera keeps everything framed as the canvas
// resizes.

/** How the buttons outside the canvas reach the rig inside it. */
export interface ViewChannel {
  cmd: { op: ViewOp; seq: number } | null;
  send(op: ViewOp): void;
}

export function createViewChannel(): ViewChannel {
  let seq = 0;
  const ch: ViewChannel = {
    cmd: null,
    send(op) {
      ch.cmd = { op, seq: ++seq };
    },
  };
  return ch;
}

export function OrbitRig({
  points,
  dir,
  insets,
  channel,
}: {
  /** What must be on screen in the home view. */
  points: THREE.Vector3[];
  /** Direction from the target to the camera in the home view. */
  dir: THREE.Vector3;
  insets: Insets;
  channel: ViewChannel;
}) {
  const camera = useThree((s) => s.camera) as THREE.PerspectiveCamera;
  const gl = useThree((s) => s.gl);
  const size = useThree((s) => s.size);

  const controls = useMemo(() => {
    const c = new OrbitControls(camera, gl.domElement);
    c.enableZoom = false;
    c.enablePan = false;
    c.enableDamping = true;
    c.dampingFactor = 0.08;
    c.minPolarAngle = 0.35;
    c.maxPolarAngle = 1.3;
    c.minAzimuthAngle = -1.1;
    c.maxAzimuthAngle = 1.1;
    return c;
  }, [camera, gl]);
  useEffect(() => () => controls.dispose(), [controls]);

  const fly = useRef<Flight | null>(null);
  const seq = useRef(0);
  const moved = useRef(false);
  const fitKey = useRef('');
  const home = useRef<Framing | null>(null);
  const pointsKey = useMemo(() => points.map((p) => `${p.x.toFixed(1)},${p.y.toFixed(1)},${p.z.toFixed(1)}`).join(';'), [points]);
  const insetsKey = `${insets.top},${insets.right},${insets.bottom},${insets.left}`;

  useEffect(() => {
    const grab = () => {
      fly.current = null;
      moved.current = true;
    };
    controls.addEventListener('start', grab);
    return () => controls.removeEventListener('start', grab);
  }, [controls]);

  useFrame((_, dt) => {
    const key = `${size.width}x${size.height}|${insetsKey}|${pointsKey}`;
    if (key !== fitKey.current) {
      const first = fitKey.current === '';
      fitKey.current = key;
      const f = fitView(points, dir, camera.fov, size.width, size.height, insets);
      home.current = f;
      if (first || !moved.current) {
        fly.current = null;
        controls.target.copy(f.target);
        camera.position.copy(f.position);
      }
    }

    const cmd = channel.cmd;
    if (cmd && cmd.seq !== seq.current && home.current) {
      seq.current = cmd.seq;
      const h = home.current;
      const from = () => [controls.target.clone(), camera.position.clone()] as const;
      if (cmd.op === 'home') {
        moved.current = false;
        fly.current = new Flight(0.8, ...from(), h.target, h.position);
      } else {
        moved.current = true;
        const offset = camera.position.clone().sub(controls.target);
        const d = THREE.MathUtils.clamp(offset.length() * (cmd.op === 'in' ? 0.75 : 1.33), h.distance * 0.3, h.distance * 2.5);
        offset.setLength(d);
        fly.current = new Flight(0.4, ...from(), controls.target.clone(), controls.target.clone().add(offset));
      }
    }
    const f = fly.current;
    if (f && !f.step(dt, controls.target, camera.position)) fly.current = null;
    controls.update();
  });
  return null;
}
