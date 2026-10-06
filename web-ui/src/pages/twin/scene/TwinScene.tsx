import { useEffect, useMemo, useRef } from 'react';
import { Canvas, useFrame, useThree } from '@react-three/fiber';
import * as THREE from 'three';
import { OrbitControls } from 'three/addons/controls/OrbitControls.js';
import { RoomEnvironment } from 'three/addons/environments/RoomEnvironment.js';
import type { TwinModel } from '../model';
import { materials, type Palette } from '../kit';
import { anchors, labelEls, twin } from '../store';
import { ComputerNode } from './ComputerNode';
import { Links } from './Links';

const coarse = typeof window !== 'undefined' && window.matchMedia('(pointer: coarse)').matches;

/** How far the nodes reach from the middle, which frames everything else. */
function extent(model: TwinModel): number {
  return Math.max(8, ...model.nodes.map((n) => Math.hypot(n.x, n.z) + 4));
}

function homeView(r: number, aspect: number) {
  // A narrow window needs to stand further back to fit the ring's width.
  const k = aspect < 1 ? Math.min(2.8, 1.15 / Math.max(0.4, aspect)) : 1;
  const d = (r * 1.75 + 8) * k;
  return { target: new THREE.Vector3(0, 1.8, 0.5), pos: new THREE.Vector3(0, d * 0.58, d * 0.92) };
}

function Environment() {
  const gl = useThree((s) => s.gl);
  const scene = useThree((s) => s.scene);
  useEffect(() => {
    const pmrem = new THREE.PMREMGenerator(gl);
    const env = pmrem.fromScene(new RoomEnvironment(), 0.04).texture;
    scene.environment = env;
    scene.environmentIntensity = 0.45;
    return () => {
      env.dispose();
      pmrem.dispose();
    };
  }, [gl, scene]);
  return null;
}

function Controls({ radius }: { radius: number }) {
  const camera = useThree((s) => s.camera);
  const gl = useThree((s) => s.gl);
  const set = useThree((s) => s.set);
  const controls = useMemo(() => {
    const c = new OrbitControls(camera, gl.domElement);
    c.enableDamping = true;
    c.dampingFactor = 0.08;
    c.minPolarAngle = 0.2;
    c.maxPolarAngle = 1.32;
    c.screenSpacePanning = false;
    c.autoRotateSpeed = 0.6;
    c.target.set(0, 1.8, 0.5);
    return c;
  }, [camera, gl]);
  useEffect(() => {
    set({ controls: controls as unknown as THREE.EventDispatcher });
    return () => controls.dispose();
  }, [controls, set]);
  useEffect(() => {
    controls.minDistance = 8;
    controls.maxDistance = radius * 5 + 60;
  }, [controls, radius]);
  // Any manual orbit stops the slow spin.
  useEffect(() => {
    const stop = () => twin.get().spin && twin.set({ spin: false });
    controls.addEventListener('start', stop);
    return () => controls.removeEventListener('start', stop);
  }, [controls]);
  useFrame(() => {
    controls.autoRotate = twin.get().spin;
    controls.update();
  });
  return null;
}

interface Fly {
  t: number;
  dur: number;
  fromT: THREE.Vector3;
  fromP: THREE.Vector3;
  toT: THREE.Vector3;
  toP: THREE.Vector3;
}
const ease = (t: number) => (t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2);

/** Scripted camera moves: home, zoom, and flying to whatever was double-clicked. */
function CameraRig({ radius }: { radius: number }) {
  const camera = useThree((s) => s.camera) as THREE.PerspectiveCamera;
  const controls = useThree((s) => s.controls) as unknown as OrbitControls | null;
  const fly = useRef<Fly | null>(null);
  const seq = useRef(0);

  useFrame((_, dt) => {
    if (!controls) return;
    const cam = twin.get().cam;
    if (cam && cam.seq !== seq.current) {
      seq.current = cam.seq;
      const target = controls.target;
      const sph = new THREE.Spherical().setFromVector3(camera.position.clone().sub(target));
      const go = (toT: THREE.Vector3, toP: THREE.Vector3, dur: number) => {
        fly.current = { t: 0, dur, fromT: target.clone(), fromP: camera.position.clone(), toT, toP };
      };
      const around = (t: THREE.Vector3) => t.clone().add(new THREE.Vector3().setFromSpherical(sph));
      if (cam.op === 'home') {
        const h = homeView(radius, camera.aspect);
        go(h.target, h.pos, 1.1);
      } else if (cam.op === 'in' || cam.op === 'out') {
        sph.radius = THREE.MathUtils.clamp(sph.radius * (cam.op === 'in' ? 0.7 : 1.4), controls.minDistance, controls.maxDistance);
        go(target.clone(), around(target), 0.45);
      } else if (cam.op === 'focus' && cam.id) {
        const a = anchors.get(cam.id);
        if (a) {
          const to = a.getWorldPosition(new THREE.Vector3());
          to.y = Math.max(1.5, to.y - 2.5);
          sph.radius = cam.id.startsWith('link:') ? 26 : 18;
          sph.phi = Math.min(sph.phi, 1.0);
          go(to, to.clone().add(new THREE.Vector3().setFromSpherical(sph)), 1.1);
        }
      }
    }
    const f = fly.current;
    if (!f) return;
    f.t += dt;
    const k = ease(Math.min(1, f.t / f.dur));
    controls.target.lerpVectors(f.fromT, f.toT, k);
    camera.position.lerpVectors(f.fromP, f.toP, k);
    if (f.t >= f.dur) fly.current = null;
  });

  useEffect(() => {
    if (!controls) return;
    const cancel = () => {
      fly.current = null;
    };
    controls.addEventListener('start', cancel);
    return () => controls.removeEventListener('start', cancel);
  }, [controls]);
  return null;
}

/** Moves each DOM name tag to where its thing is on screen, every frame. */
function LabelProjector() {
  const v = useMemo(() => new THREE.Vector3(), []);
  useFrame(({ camera, size }) => {
    for (const [id, el] of labelEls) {
      const a = anchors.get(id);
      if (!a) {
        el.style.visibility = 'hidden';
        continue;
      }
      a.getWorldPosition(v).project(camera);
      const off = v.z > 1;
      el.style.visibility = off ? 'hidden' : '';
      if (off) continue;
      const x = (v.x * 0.5 + 0.5) * size.width;
      const y = (-v.y * 0.5 + 0.5) * size.height;
      el.style.transform = `translate3d(${x.toFixed(1)}px, ${y.toFixed(1)}px, 0) translate(-50%, -100%)`;
      el.style.zIndex = String(Math.round((1 - v.z) * 1e4));
    }
  });
  return null;
}

export function TwinScene({ model, palette }: { model: TwinModel; palette: Palette }) {
  const mats = useMemo(() => materials(palette), [palette]);
  useEffect(() => () => Object.values(mats).forEach((m) => m.dispose()), [mats]);
  const r = extent(model);
  const home = homeView(r, typeof window === 'undefined' ? 1.6 : window.innerWidth / Math.max(1, window.innerHeight));
  const shadow = r + 8;

  return (
    <Canvas
      className="!absolute inset-0 touch-none"
      shadows={{ type: THREE.PCFShadowMap }}
      dpr={[1, coarse ? 1.5 : 2]}
      camera={{ fov: 32, near: 0.5, far: 1200, position: home.pos.toArray() }}
      onCreated={({ gl, camera }) => {
        gl.toneMapping = THREE.NeutralToneMapping;
        gl.toneMappingExposure = palette.dark ? 1.0 : 1.05;
        camera.lookAt(home.target);
      }}
      onPointerMissed={() => twin.select(null)}
    >
      <color attach="background" args={[palette.ground]} />
      <fog attach="fog" args={[palette.ground, r * 4, r * 9 + 80]} />
      <Environment />
      <hemisphereLight args={[palette.dark ? '#c8d4ff' : '#ffffff', palette.dark ? '#1a1d24' : '#c9cfe6', palette.dark ? 0.7 : 1.4]} />
      <directionalLight
        castShadow
        color="#fffaf2"
        intensity={palette.dark ? 1.4 : 2.3}
        position={[-r, r * 2 + 30, r + 20]}
        shadow-mapSize={coarse ? [1024, 1024] : [2048, 2048]}
        shadow-camera-left={-shadow}
        shadow-camera-right={shadow}
        shadow-camera-top={shadow}
        shadow-camera-bottom={-shadow}
        shadow-camera-near={5}
        shadow-camera-far={r * 6 + 120}
        shadow-bias={-0.0004}
        shadow-normalBias={0.03}
      />
      <mesh rotation={[-Math.PI / 2, 0, 0]} receiveShadow>
        <circleGeometry args={[r * 3 + 40, 96]} />
        <meshStandardMaterial color={palette.ground} roughness={0.95} />
      </mesh>
      <gridHelper
        args={[Math.ceil(r * 2.6 + 20), Math.ceil((r * 2.6 + 20) / 2), palette.dark ? '#2a303b' : '#d9dde8', palette.dark ? '#20252e' : '#e2e5ee']}
        position={[0, 0.005, 0]}
      />
      <Controls radius={r} />
      <CameraRig radius={r} />
      <LabelProjector />
      <Links model={model} palette={palette} />
      {model.nodes.map((n) => (
        <ComputerNode key={n.name} node={n} mats={mats} palette={palette} />
      ))}
    </Canvas>
  );
}
