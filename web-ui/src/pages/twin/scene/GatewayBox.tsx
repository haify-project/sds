import { useEffect, useMemo, useRef } from 'react';
import { useFrame } from '@react-three/fiber';
import * as THREE from 'three';
import type { TwinGateway } from '../model';
import { GATEWAY_COLOR, glow, rbox, type Mats, type Palette } from '../kit';
import { anchors, useTwin } from '../store';
import { pickHandlers } from './pick';

// A gateway as a small network appliance in front of the node serving it: a
// coloured band says the protocol, the light says whether it is up.

export function GatewayBox({
  gw,
  mats,
  palette,
  x,
  z,
}: {
  gw: TwinGateway;
  mats: Mats;
  palette: Palette;
  x: number;
  z: number;
}) {
  const id = `gw:${gw.id}`;
  const selected = useTwin((s) => s.selected === id);
  const hovered = useTwin((s) => s.hovered === id);
  const anchor = useRef<THREE.Object3D>(null);
  const led = useRef<THREE.Mesh>(null);
  const band = useMemo(() => glow(GATEWAY_COLOR[gw.type] ?? palette.accent, 0.5), [gw.type, palette]);
  const light = useMemo(() => glow('#ffffff', 2.4), []);
  useEffect(() => {
    light.color.set(palette.health[gw.health]);
    light.emissive.set(palette.health[gw.health]);
  }, [light, palette, gw.health]);
  useEffect(() => () => [band, light].forEach((m) => m.dispose()), [band, light]);
  useEffect(() => {
    if (!anchor.current) return;
    anchors.set(id, anchor.current);
    return () => {
      anchors.delete(id);
    };
  }, [id]);

  useFrame(({ clock }) => {
    if (!led.current) return;
    const m = led.current.material as THREE.MeshStandardMaterial;
    m.emissiveIntensity = gw.health === 'ok' ? 1.6 + Math.sin(clock.elapsedTime * 3 + x) * 0.8 : Math.sin(clock.elapsedTime * 7) > 0 ? 2.8 : 0.2;
  });

  return (
    <group position={[x, 0, z]} {...pickHandlers(id)}>
      <mesh geometry={rbox(1.35, 0.7, 1.1, 0.08)} material={mats.body} position={[0, 0.35, 0]} castShadow receiveShadow />
      <mesh geometry={rbox(1.36, 0.14, 1.11, 0.03)} material={band} position={[0, 0.5, 0]} />
      <mesh ref={led} geometry={rbox(0.16, 0.08, 0.04, 0.02)} material={light} position={[0.45, 0.22, 0.56]} />
      {[0, 1, 2].map((i) => (
        <mesh key={i} geometry={rbox(0.14, 0.1, 0.04, 0.02)} material={mats.ink} position={[-0.42 + i * 0.22, 0.22, 0.56]} />
      ))}
      {(selected || hovered) && (
        <mesh position={[0, 0.03, 0]} rotation={[-Math.PI / 2, 0, 0]}>
          <planeGeometry args={[1.9, 1.6]} />
          <meshBasicMaterial color={palette.accent} transparent opacity={selected ? 0.4 : 0.2} depthWrite={false} />
        </mesh>
      )}
      <object3D ref={anchor} position={[0, 1.0, 0]} />
    </group>
  );
}
