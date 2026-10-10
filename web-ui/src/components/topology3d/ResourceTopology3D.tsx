import { useEffect, useMemo, useRef } from 'react';
import { Canvas, useFrame, useThree } from '@react-three/fiber';
import * as THREE from 'three';
import { RoomEnvironment } from 'three/addons/environments/RoomEnvironment.js';
import { Line2 } from 'three/addons/lines/Line2.js';
import { LineGeometry } from 'three/addons/lines/LineGeometry.js';
import { LineMaterial } from 'three/addons/lines/LineMaterial.js';
import { useTheme } from 'next-themes';
import type { Resource, ResourceStatus } from '@/services/api';
import { DARK, LIGHT, materials, type Palette } from '@/pages/twin/kit';
import { placeNodes, railTone, type PlacedNode } from '../ResourceTopology';
import { MiniComputer, towerTopOf } from './MiniComputer';
import { boxCorners, viewFrom } from './fit';
import { createViewChannel, OrbitRig } from './OrbitRig';
import { ViewControls } from './ViewControls';

// The replication topology of one resource as a little room of computers:
// its members side by side, the Primary's case glowing, dashed lines running
// from the Primary to every copy it writes to. A DR copy stands apart on its
// own platform, reached by violet (asynchronous) legs.

const SPACING = 7.2;
const DR_GAP = 4.5;
const WAN = '#a78bfa';
const FOV = 22;
const DIR = viewFrom(0.36);
/** Clear of the 3D/2D switch above, the camera buttons to the right, the caption below. */
const INSETS = { top: 30, right: 48, bottom: 22, left: 4 };

interface Arc {
  key: string;
  from: [number, number, number];
  to: [number, number, number];
  color: string;
  width: number;
  /** +1: dashes run from→to, -1: to→from, 0: still. */
  flow: number;
  fast: boolean;
}

function DashedArc({ arc }: { arc: Arc }) {
  const size = useThree((s) => s.size);
  const line = useMemo(() => {
    const a = new THREE.Vector3(...arc.from);
    const b = new THREE.Vector3(...arc.to);
    const mid = a.clone().lerp(b, 0.5);
    mid.y += 1.4 + a.distanceTo(b) * 0.16;
    const pts = new THREE.QuadraticBezierCurve3(a, mid, b).getPoints(48);
    const geo = new LineGeometry();
    geo.setPositions(pts.flatMap((p) => [p.x, p.y, p.z]));
    const mat = new LineMaterial({ dashed: true, dashSize: 0.45, gapSize: 0.3, transparent: true, worldUnits: false });
    const l = new Line2(geo, mat);
    l.computeLineDistances();
    return l;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [arc.from.join(), arc.to.join()]);
  useEffect(
    () => () => {
      line.geometry.dispose();
      line.material.dispose();
    },
    [line],
  );
  useEffect(() => {
    line.material.color.set(arc.color);
    line.material.linewidth = arc.width;
    line.material.opacity = 0.9;
    line.material.resolution.set(size.width, size.height);
  }, [line, arc.color, arc.width, size]);
  useFrame((_, dt) => {
    if (arc.flow) line.material.dashOffset -= arc.flow * dt * (arc.fast ? 2.2 : 0.8);
  });
  return <primitive object={line} />;
}

/** Soft studio light, as the cluster view uses; without it the cases go flat. */
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

function arcsFor(
  members: { node: PlacedNode; x: number }[],
  dr: { node: PlacedNode; x: number }[],
  legReplicating: (name: string) => boolean,
  p: Palette,
): Arc[] {
  const top = (m: { node: PlacedNode; x: number }) =>
    towerTopOf(m.x, m.node.kind === 'tiebreaker' || m.node.kind === 'client');
  const primary = members.find((m) => m.node.kind === 'replica' && m.node.state?.role === 'Primary');
  const arcs: Arc[] = [];
  if (primary) {
    for (const m of members) {
      if (m === primary) continue;
      const vote = m.node.kind === 'tiebreaker' || m.node.kind === 'client';
      const tone = railTone(m.node);
      arcs.push({
        key: `${primary.node.name}>${m.node.name}`,
        from: top(primary),
        to: top(m),
        color: vote ? p.health.idle : p.health[tone],
        width: vote ? 1.6 : 3,
        flow: tone === 'bad' ? 0 : 1,
        fast: Boolean(m.node.state?.replicationState?.startsWith('Sync')),
      });
    }
  } else {
    // Nobody Primary: the mesh is there, nothing is being written.
    for (let i = 1; i < members.length; i++)
      arcs.push({
        key: `${members[i - 1].node.name}-${members[i].node.name}`,
        from: top(members[i - 1]),
        to: top(members[i]),
        color: p.health.idle,
        width: 2,
        flow: 0,
        fast: false,
      });
  }
  for (const d of dr) {
    for (const m of members.filter((x) => x.node.kind === 'replica')) {
      const ok = legReplicating(m.node.name);
      arcs.push({
        key: `${m.node.name}>dr:${d.node.name}`,
        from: top(m),
        to: top(d),
        color: ok ? WAN : p.health.bad,
        width: 2.2,
        flow: ok && m === primary ? 1 : 0,
        fast: false,
      });
    }
  }
  return arcs;
}

/** Tells the page the first frame is on screen, so it can drop its placeholder. */
function FirstFrame({ onReady }: { onReady?: () => void }) {
  const done = useRef(false);
  useFrame(() => {
    if (done.current) return;
    done.current = true;
    // After this frame has been drawn, not before.
    requestAnimationFrame(() => onReady?.());
  });
  return null;
}

export default function ResourceTopology3D({
  resource,
  status,
  onReady,
}: {
  resource: Resource;
  status: ResourceStatus;
  onReady?: () => void;
}) {
  const { resolvedTheme } = useTheme();
  const palette = resolvedTheme === 'dark' ? DARK : LIGHT;
  const mats = useMemo(() => materials(palette), [palette]);
  useEffect(() => () => Object.values(mats).forEach((m) => m.dispose()), [mats]);

  const { local, remote, backing, legState } = placeNodes(resource, status);
  const span = (local.length - 1) * SPACING + (remote.length ? DR_GAP + remote.length * SPACING : 0);
  const x0 = -span / 2;
  const members = local.map((node, i) => ({ node, x: x0 + i * SPACING }));
  const drStart = x0 + (local.length - 1) * SPACING + DR_GAP + SPACING;
  const dr = remote.map((node, i) => ({ node, x: drStart + i * SPACING }));
  const arcs = arcsFor(members, dr, (n) => legState(n).replicating, palette);

  const width = span + 6;
  // Every desk, and the crest of every arc, in the home view.
  const points = useMemo(() => {
    const pts = [...members, ...dr].flatMap((m) => boxCorners([m.x - 2.8, 0, -1.9], [m.x + 2.8, 3.4, 1.9]));
    for (const a of arcs) {
      const from = new THREE.Vector3(...a.from);
      const to = new THREE.Vector3(...a.to);
      const crest = from.clone().lerp(to, 0.5);
      crest.y += (1.4 + from.distanceTo(to) * 0.16) / 2 + 0.3;
      pts.push(crest);
    }
    return pts;
    // Positions only change with the membership.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [members.map((m) => `${m.node.name}@${m.x}`).join(), dr.map((m) => `${m.node.name}@${m.x}`).join(), arcs.map((a) => a.key).join()]);
  const channel = useMemo(createViewChannel, []);

  return (
    <>
      <div className="absolute inset-0" role="img" aria-label={`Replication topology for ${resource.name}`}>
        <Canvas
          className="!absolute inset-0"
          shadows={{ type: THREE.PCFShadowMap }}
          dpr={[1, 2]}
          camera={{ fov: FOV, near: 0.5, far: 500, position: [0, width * 0.4, width * 1.1] }}
          onCreated={({ gl }) => {
            gl.toneMapping = THREE.NeutralToneMapping;
          }}
        >
          <FirstFrame onReady={onReady} />
          <Environment />
          <hemisphereLight args={[palette.dark ? '#c8d4ff' : '#ffffff', palette.dark ? '#2a2f3a' : '#c9cfe6', palette.dark ? 1.6 : 1.6]} />
          <directionalLight
            castShadow
            intensity={palette.dark ? 2.0 : 2.2}
            position={[-8, 22, 14]}
            shadow-mapSize={[1024, 1024]}
            shadow-camera-left={-width}
            shadow-camera-right={width}
            shadow-camera-top={10}
            shadow-camera-bottom={-10}
          />
          <mesh rotation={[-Math.PI / 2, 0, 0]} receiveShadow>
            <planeGeometry args={[width * 4, 60]} />
            <shadowMaterial opacity={palette.dark ? 0.35 : 0.12} />
          </mesh>
          {dr.length > 0 && (
            // The DR site: its own platform, apart from the primary site.
            <mesh position={[(dr[0].x + dr[dr.length - 1].x) / 2, 0.01, 0]} rotation={[-Math.PI / 2, 0, 0]}>
              <planeGeometry args={[dr.length * SPACING, 5.4]} />
              <meshBasicMaterial color={WAN} transparent opacity={0.12} depthWrite={false} />
            </mesh>
          )}
          <OrbitRig points={points} dir={DIR} insets={INSETS} channel={channel} />
          {members.map((m) => (
            <MiniComputer key={m.node.name} node={m.node} backing={backing} x={m.x} mats={mats} palette={palette} />
          ))}
          {dr.map((m) => (
            <MiniComputer key={m.node.name} node={m.node} backing={backing} x={m.x} mats={mats} palette={palette} />
          ))}
          {arcs.map((a) => (
            <DashedArc key={a.key} arc={a} />
          ))}
        </Canvas>
      </div>
      <ViewControls onCamera={(op) => channel.send(op)} className="absolute right-0 bottom-6 z-10" />
    </>
  );
}
