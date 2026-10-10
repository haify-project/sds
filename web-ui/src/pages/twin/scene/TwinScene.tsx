import { useEffect, useMemo } from 'react';
import { Canvas, useFrame, useThree } from '@react-three/fiber';
import * as THREE from 'three';
import { OrbitControls } from 'three/addons/controls/OrbitControls.js';
import { RoomEnvironment } from 'three/addons/environments/RoomEnvironment.js';
import type { TwinModel } from '../model';
import { materials, type Palette } from '../kit';
import { twin } from '../store';
import { ComputerNode } from './ComputerNode';
import { Links } from './Links';
import { CameraRig, framePoints, homeFraming } from './CameraRig';
import { LabelProjector } from './LabelProjector';

const FOV = 32;
const coarse = typeof window !== 'undefined' && window.matchMedia('(pointer: coarse)').matches;

/** How far the nodes reach from the middle, which frames everything else. */
function extent(model: TwinModel): number {
  return Math.max(8, ...model.nodes.map((n) => Math.hypot(n.x, n.z) + 4));
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

function Controls({ radius, target }: { radius: number; target: THREE.Vector3 }) {
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
    c.target.copy(target);
    return c;
    // The target is only where it starts; the camera rig moves it after.
    // eslint-disable-next-line react-hooks/exhaustive-deps
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

export function TwinScene({ model, palette }: { model: TwinModel; palette: Palette }) {
  const mats = useMemo(() => materials(palette), [palette]);
  useEffect(() => () => Object.values(mats).forEach((m) => m.dispose()), [mats]);
  const r = extent(model);
  // A first guess from the window; the rig refits to the canvas on its first frame.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const home = useMemo(() => homeFraming(framePoints(model), FOV, window.innerWidth - 240, window.innerHeight - 56), []);
  const shadow = r + 8;

  return (
    <Canvas
      className="!absolute inset-0 touch-none"
      shadows={{ type: THREE.PCFShadowMap }}
      dpr={[1, coarse ? 1.5 : 2]}
      camera={{ fov: FOV, near: 0.5, far: 1200, position: home.position.toArray() }}
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
      <Controls radius={r} target={home.target} />
      <CameraRig model={model} radius={r} />
      <LabelProjector model={model} />
      <Links model={model} palette={palette} />
      {model.nodes.map((n) => (
        <ComputerNode key={n.name} node={n} mats={mats} palette={palette} />
      ))}
    </Canvas>
  );
}
