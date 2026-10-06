import { memo, useEffect, useMemo, useRef } from 'react';
import { useFrame } from '@react-three/fiber';
import * as THREE from 'three';
import type { TwinNode } from '../model';
import { canvasTexture, cyl, glow, ramp, rbox, type Mats, type Palette } from '../kit';
import { anchors, twin, useTwin } from '../store';
import { pickHandlers } from './pick';
import { drawScreen, screenKey, SCREEN_H, SCREEN_W } from './screen';
import { GatewayBox } from './GatewayBox';

// One node, drawn as a workstation: a tower whose drive bays are the replicas
// it holds, a glass gauge for how full its pool is, and a monitor showing its
// state. Faces +z, toward the default camera, wherever it stands.

export const TOWER = { x: -1.9, top: 3.65 };
const PLINTH_H = 0.35;
const BAYS = 7;

function Screen({ node, palette }: { node: TwinNode; palette: Palette }) {
  const { tex, ctx } = useMemo(() => canvasTexture(SCREEN_W, SCREEN_H), []);
  const key = screenKey(node);
  useEffect(() => {
    const draw = () => {
      drawScreen(ctx, node, palette);
      tex.needsUpdate = true;
    };
    draw();
    // The UI fonts may still be loading on the first draw.
    document.fonts?.ready.then(draw);
    // node is read through key; palette switches with the theme.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, palette, ctx, tex]);
  useEffect(() => () => tex.dispose(), [tex]);
  return (
    <mesh position={[0, 0, 0.09]}>
      <planeGeometry args={[3.2, 1.9]} />
      <meshBasicMaterial map={tex} toneMapped={false} />
    </mesh>
  );
}

export const ComputerNode = memo(function ComputerNode({
  node,
  mats,
  palette,
}: {
  node: TwinNode;
  mats: Mats;
  palette: Palette;
}) {
  const id = `node:${node.name}`;
  const selected = useTwin((s) => s.selected === id);
  const hovered = useTwin((s) => s.hovered === id);
  const anchor = useRef<THREE.Object3D>(null);
  const power = useRef<THREE.Mesh>(null);
  const pulse = useRef<THREE.Mesh>(null);
  const bayLeds = useRef<(THREE.Mesh | null)[]>([]);

  useEffect(() => {
    const a = anchor.current;
    if (!a) return;
    anchors.set(id, a);
    return () => {
      anchors.delete(id);
    };
  }, [id]);

  const bays = node.replicas
    .slice()
    .sort((a, b) => Number(b.primary) - Number(a.primary) || a.resource.localeCompare(b.resource))
    .slice(0, BAYS);
  // Materials are made once per node and recoloured in place: a new material
  // per render would leak GPU programs at every 5-second poll.
  const bayMats = useMemo(() => Array.from({ length: BAYS }, () => glow('#ffffff')), []);
  const fillMat = useMemo(() => glow('#ffffff', 0.9), []);
  const powerMat = useMemo(() => glow('#ffffff', 2.6), []);
  const haloMat = useMemo(() => glow('#ffffff', 1.8), []);
  useEffect(() => {
    bays.forEach((b, i) => {
      bayMats[i].color.set(palette.health[b.health]);
      bayMats[i].emissive.set(palette.health[b.health]);
    });
    const fill = `#${ramp(node.fill).getHexString()}`;
    fillMat.color.set(fill);
    fillMat.emissive.set(fill);
    powerMat.color.set(palette.health[node.health]);
    powerMat.emissive.set(palette.health[node.health]);
    haloMat.color.set(palette.accent);
    haloMat.emissive.set(palette.accent);
  });
  useEffect(
    () => () => [...bayMats, fillMat, powerMat, haloMat].forEach((m) => m.dispose()),
    [bayMats, fillMat, powerMat, haloMat],
  );

  useFrame(({ clock }) => {
    const t = clock.elapsedTime;
    // A failing node's power light blinks; a healthy one breathes.
    if (power.current) {
      const m = power.current.material as THREE.MeshStandardMaterial;
      m.emissiveIntensity = node.health === 'bad' ? (Math.sin(t * 7) > 0 ? 3 : 0.2) : 1.8 + Math.sin(t * 1.6) * 0.6;
    }
    bays.forEach((b, i) => {
      const led = bayLeds.current[i];
      if (!led) return;
      const m = led.material as THREE.MeshStandardMaterial;
      // Disk activity: a syncing copy flickers fast, a primary blinks with writes.
      const base = b.health === 'bad' ? 2.4 : 1.6;
      const flicker = b.syncing
        ? Math.sin(t * 22 + i) > 0 ? 2.8 : 0.4
        : b.primary
          ? Math.sin(t * 9 + i * 1.7) > 0.3 ? 2.6 : 0.8
          : base;
      m.emissiveIntensity = flicker;
    });
    // The controller's case breathes.
    if (node.controller) haloMat.emissiveIntensity = 1.5 + Math.sin(t * 2.2) * 0.9;
    if (pulse.current) {
      const f = twin.get().flashes.get(id);
      const age = f ? (performance.now() - f.at) / 1000 : 99;
      // Only trouble pulses; a recovery or an info event just updates the picture.
      const on = age < 6 && f!.severity !== 'info';
      pulse.current.visible = on;
      if (on) {
        const k = (age % 1.5) / 1.5;
        pulse.current.scale.setScalar(1 + k * 0.12);
        const m = pulse.current.material as THREE.MeshBasicMaterial;
        m.opacity = (1 - k) * 0.35;
        m.color.set(f!.severity === 'critical' ? palette.health.bad : palette.health.warn);
      }
    }
  });

  const towerY = PLINTH_H + 1.65;
  const faceZ = 1.5 + 0.01;

  return (
    <group position={[node.x, 0, node.z]}>
      <group {...pickHandlers(id)}>
        {/* Desk */}
        <mesh geometry={rbox(6.6, PLINTH_H, 4.6, 0.12)} material={mats.slab} position={[0, PLINTH_H / 2, 0]} castShadow receiveShadow />

        {/* Tower */}
        <group position={[TOWER.x, towerY, 0]}>
          <mesh geometry={rbox(1.6, 3.3, 3.0, 0.12)} material={mats.body} castShadow receiveShadow />
          <mesh geometry={rbox(1.46, 3.1, 0.06, 0.03)} material={mats.shell} position={[0, 0, faceZ - 0.02]} />
          {/* Power button */}
          <mesh ref={power} geometry={cyl(0.13, 0.06)} material={powerMat} position={[0, 1.3, faceZ + 0.02]} rotation={[Math.PI / 2, 0, 0]} />
          {/* Drive bays, one per replica held (primaries first) */}
          {Array.from({ length: BAYS }, (_, i) => {
            const b = bays[i];
            const y = 0.85 - i * 0.33;
            return (
              <group key={i} position={[0, y, faceZ + 0.02]}>
                <mesh geometry={rbox(1.18, 0.26, 0.05, 0.03)} material={b?.primary ? mats.accent : mats.ink} />
                {b && (
                  <mesh
                    ref={(m) => {
                      bayLeds.current[i] = m;
                    }}
                    geometry={rbox(0.14, 0.08, 0.04, 0.02)}
                    material={bayMats[i]}
                    position={[0.42, 0, 0.04]}
                  />
                )}
              </group>
            );
          })}
          {/* Vents */}
          {[0, 1, 2].map((i) => (
            <mesh key={i} geometry={rbox(1.0, 0.05, 0.04, 0.02)} material={mats.trim} position={[0, -1.35 + i * 0.12, faceZ + 0.02]} />
          ))}
          {/* Pool gauge: a glass tube on the side, filled as far as the pool is */}
          <group position={[0.82, 0, 1.05]}>
            <mesh geometry={cyl(0.17, 2.9)} material={mats.glass} />
            <mesh geometry={cyl(0.12, 2.8 * Math.max(0.03, node.fill))} material={fillMat} position={[0, -1.4 + 1.4 * Math.max(0.03, node.fill), 0]} />
          </group>
          {/* The controller runs here: the case's front edges light up */}
          {node.controller && (
            <>
              {[-0.76, 0.76].map((x) => (
                <mesh key={x} geometry={rbox(0.07, 3.12, 0.05, 0.025)} material={haloMat} position={[x, 0, faceZ + 0.01]} />
              ))}
              <mesh geometry={rbox(1.56, 0.07, 0.05, 0.025)} material={haloMat} position={[0, 1.58, faceZ + 0.01]} />
              {/* and the rim of its top */}
              {[-0.76, 0.76].map((x) => (
                <mesh key={`t${x}`} geometry={rbox(0.07, 0.05, 2.92, 0.02)} material={haloMat} position={[x, 1.655, 0]} />
              ))}
              <mesh geometry={rbox(1.56, 0.05, 0.07, 0.02)} material={haloMat} position={[0, 1.655, -1.46]} />
            </>
          )}
        </group>

        {/* Monitor */}
        <group position={[1.15, PLINTH_H, -0.5]}>
          <mesh geometry={rbox(1.2, 0.08, 0.8, 0.04)} material={mats.metal} position={[0, 0.04, 0]} castShadow />
          <mesh geometry={cyl(0.08, 1.05)} material={mats.metal} position={[0, 0.55, 0]} castShadow />
          <group position={[0, 2.0, 0.1]} rotation={[-0.08, 0, 0]}>
            <mesh geometry={rbox(3.45, 2.15, 0.16, 0.06)} material={mats.ink} castShadow />
            <Screen node={node} palette={palette} />
          </group>
        </group>
        {/* Keyboard */}
        <mesh geometry={rbox(2.5, 0.09, 0.8, 0.04)} material={mats.shell} position={[1.15, PLINTH_H + 0.045, 1.25]} castShadow />
        <mesh geometry={rbox(2.3, 0.02, 0.6, 0.01)} material={mats.trim} position={[1.15, PLINTH_H + 0.1, 1.25]} />
      </group>

      {/* ...and casts a glow on the desk around it */}
      {node.controller && (
        <mesh position={[TOWER.x, PLINTH_H + 0.01, 0.2]} rotation={[-Math.PI / 2, 0, 0]}>
          <planeGeometry args={[2.6, 3.8]} />
          <meshBasicMaterial color={palette.accent} transparent opacity={palette.dark ? 0.28 : 0.18} depthWrite={false} />
        </mesh>
      )}

      {/* Selection / hover outline under the desk */}
      {(selected || hovered) && (
        <mesh position={[0, 0.02, 0]} rotation={[-Math.PI / 2, 0, 0]}>
          <planeGeometry args={[7.4, 5.4]} />
          <meshBasicMaterial color={palette.accent} transparent opacity={selected ? 0.35 : 0.16} depthWrite={false} />
        </mesh>
      )}

      {/* Event pulse */}
      <mesh ref={pulse} position={[0, 0.04, 0]} rotation={[-Math.PI / 2, 0, 0]} visible={false}>
        <ringGeometry args={[3.5, 3.65, 64]} />
        <meshBasicMaterial transparent depthWrite={false} />
      </mesh>

      {node.gateways.map((g, i) => (
        <GatewayBox key={g.id} gw={g} mats={mats} palette={palette} x={-2.4 + i * 1.6} z={3.4} />
      ))}

      <object3D ref={anchor} position={[0, 4.6, 0]} />
    </group>
  );
});
