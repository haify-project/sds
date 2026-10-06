import { useEffect, useMemo, useRef } from 'react';
import { useFrame } from '@react-three/fiber';
import type * as THREE from 'three';
import { railTone, type PlacedNode } from '../ResourceTopology';
import { canvasTexture, cyl, glow, rbox, type Mats, type Palette } from '@/pages/twin/kit';
import { drawScreen, screenKey, SCREEN_H, SCREEN_W } from './screen';

// One member of a resource as a small workstation. A replica has a tower with
// a drive light in its disk's colour; a tiebreaker or diskless client has a
// slim box with no drives. The Primary's case glows.

export const DESK_H = 0.3;
const TOWER_X = -1.55;

export function MiniComputer({
  node,
  backing,
  x,
  mats,
  palette,
}: {
  node: PlacedNode;
  backing: string;
  x: number;
  mats: Mats;
  palette: Palette;
}) {
  const diskless = node.kind === 'tiebreaker' || node.kind === 'client';
  const primary = !diskless && node.state?.role === 'Primary';
  const tone = palette.health[railTone(node)];

  const { tex, ctx } = useMemo(() => canvasTexture(SCREEN_W, SCREEN_H), []);
  const key = screenKey(node, backing);
  useEffect(() => {
    const draw = () => {
      drawScreen(ctx, node, backing, tone, palette);
      tex.needsUpdate = true;
    };
    draw();
    document.fonts?.ready.then(draw);
    // node is read through key.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, tone, palette, ctx, tex]);

  const ledMat = useMemo(() => glow('#ffffff', 2.2), []);
  const caseGlow = useMemo(() => glow('#ffffff', 1.6), []);
  useEffect(() => {
    ledMat.color.set(tone);
    ledMat.emissive.set(tone);
    caseGlow.color.set(palette.accent);
    caseGlow.emissive.set(palette.accent);
  }, [ledMat, caseGlow, tone, palette]);
  useEffect(() => () => [tex, ledMat, caseGlow].forEach((m) => m.dispose()), [tex, ledMat, caseGlow]);

  const led = useRef<THREE.Mesh>(null);
  useFrame(({ clock }) => {
    const t = clock.elapsedTime;
    if (primary) caseGlow.emissiveIntensity = 1.4 + Math.sin(t * 2.2) * 0.8;
    if (led.current) {
      // A primary takes writes, a syncing copy is being written: both flicker.
      const busy = primary || node.state?.replicationState?.startsWith('Sync');
      (led.current.material as THREE.MeshStandardMaterial).emissiveIntensity = busy
        ? Math.sin(t * 11) > 0.2 ? 2.6 : 0.7
        : 1.8;
    }
  });

  const towerH = diskless ? 2.0 : 2.9;
  const towerW = diskless ? 0.8 : 1.35;
  const towerY = DESK_H + towerH / 2;
  const faceZ = 1.3 + 0.01;

  return (
    <group position={[x, 0, 0]}>
      <mesh geometry={rbox(5.6, DESK_H, 3.8, 0.1)} material={mats.slab} position={[0, DESK_H / 2, 0]} castShadow receiveShadow />

      {/* Tower: drives for a replica, an empty slim case for a diskless member */}
      <group position={[TOWER_X, towerY, 0]}>
        <mesh geometry={rbox(towerW, towerH, 2.6, 0.1)} material={diskless ? mats.glass : mats.body} castShadow={!diskless} receiveShadow />
        {diskless ? (
          <mesh geometry={rbox(towerW - 0.12, towerH - 0.12, 2.48, 0.08)} material={mats.shell} />
        ) : (
          <>
            <mesh geometry={rbox(towerW - 0.14, towerH - 0.18, 0.05, 0.03)} material={mats.shell} position={[0, 0, faceZ - 0.02]} />
            {[0, 1, 2, 3].map((i) => (
              <mesh
                key={i}
                geometry={rbox(towerW - 0.4, 0.24, 0.05, 0.03)}
                material={i === 0 && primary ? mats.accent : mats.ink}
                position={[0, 0.75 - i * 0.32, faceZ + 0.02]}
              />
            ))}
            <mesh ref={led} geometry={rbox(0.14, 0.08, 0.04, 0.02)} material={ledMat} position={[towerW / 2 - 0.36, 0.75, faceZ + 0.06]} />
            <mesh geometry={cyl(0.11, 0.05)} material={ledMat} position={[0, 1.15, faceZ + 0.02]} rotation={[Math.PI / 2, 0, 0]} />
          </>
        )}
        {primary && (
          <>
            {[-towerW / 2 + 0.03, towerW / 2 - 0.03].map((ex) => (
              <mesh key={ex} geometry={rbox(0.06, towerH - 0.06, 0.05, 0.02)} material={caseGlow} position={[ex, 0, faceZ + 0.01]} />
            ))}
            <mesh geometry={rbox(towerW - 0.06, 0.06, 0.05, 0.02)} material={caseGlow} position={[0, towerH / 2 - 0.03, faceZ + 0.01]} />
            {[-towerW / 2 + 0.03, towerW / 2 - 0.03].map((ex) => (
              <mesh key={`t${ex}`} geometry={rbox(0.06, 0.05, 2.62, 0.02)} material={caseGlow} position={[ex, towerH / 2 + 0.005, 0]} />
            ))}
          </>
        )}
      </group>

      {/* Monitor */}
      <group position={[0.95, DESK_H, -0.4]}>
        <mesh geometry={rbox(1.0, 0.07, 0.7, 0.03)} material={mats.metal} position={[0, 0.035, 0]} castShadow />
        <mesh geometry={cyl(0.07, 0.9)} material={mats.metal} position={[0, 0.47, 0]} castShadow />
        <group position={[0, 1.75, 0.08]} rotation={[-0.08, 0, 0]}>
          <mesh geometry={rbox(3.1, 1.88, 0.14, 0.05)} material={mats.ink} castShadow />
          <mesh position={[0, 0, 0.08]}>
            <planeGeometry args={[2.9, 1.71]} />
            <meshBasicMaterial map={tex} toneMapped={false} />
          </mesh>
        </group>
      </group>
      <mesh geometry={rbox(2.2, 0.08, 0.7, 0.03)} material={mats.shell} position={[0.95, DESK_H + 0.04, 1.15]} castShadow />

      {primary && (
        <mesh position={[TOWER_X, DESK_H + 0.01, 0.15]} rotation={[-Math.PI / 2, 0, 0]}>
          <planeGeometry args={[2.3, 3.3]} />
          <meshBasicMaterial color={palette.accent} transparent opacity={palette.dark ? 0.28 : 0.18} depthWrite={false} />
        </mesh>
      )}
    </group>
  );
}

/** Where a member's links attach: the top of its tower. */
export function towerTopOf(x: number, diskless: boolean): [number, number, number] {
  return [x + TOWER_X, DESK_H + (diskless ? 2.0 : 2.9) - 0.1, 0];
}
