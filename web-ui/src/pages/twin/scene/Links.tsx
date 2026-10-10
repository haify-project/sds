import { useEffect, useMemo, useRef } from 'react';
import { useFrame, useThree } from '@react-three/fiber';
import { Line2 } from 'three/addons/lines/Line2.js';
import { LineGeometry } from 'three/addons/lines/LineGeometry.js';
import { LineMaterial } from 'three/addons/lines/LineMaterial.js';
import * as THREE from 'three';
import type { TwinLink, TwinModel, TwinNode } from '../model';
import type { Palette } from '../kit';
import { anchors, labelSpots, useTwin } from '../store';
import { pickHandlers } from './pick';
import { TOWER } from './ComputerNode';

// Replication between two nodes: one dashed arc per pair however many
// resources share it, wider the more there are, its dashes running from the
// Primary. Tiebreaker votes are fainter dashes on the floor.

const WAN = '#a78bfa';

function towerTop(n: TwinNode) {
  return new THREE.Vector3(n.x + TOWER.x, TOWER.top - 0.2, n.z);
}

function Arc({ link, nodes, palette }: { link: TwinLink; nodes: Map<string, TwinNode>; palette: Palette }) {
  const id = `link:${link.key}`;
  const selected = useTwin((s) => s.selected === id);
  const hovered = useTwin((s) => s.hovered === id);
  const anchor = useRef<THREE.Object3D>(null);
  const size = useThree((s) => s.size);
  const a = nodes.get(link.a)!;
  const b = nodes.get(link.b)!;

  const curve = useMemo(() => {
    const p0 = towerTop(a);
    const p2 = towerTop(b);
    const mid = p0.clone().lerp(p2, 0.5);
    mid.y += 2.4 + p0.distanceTo(p2) * 0.18;
    return new THREE.QuadraticBezierCurve3(p0, mid, p2);
    // Rebuilt only when a node moves, not at every poll's new objects.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [a.x, a.z, b.x, b.z]);

  // A dashed line rather than a cable: replication is a relationship, not a
  // wire. Its dashes run away from the Primary, faster while a copy resyncs.
  const line = useMemo(() => {
    const geo = new LineGeometry();
    geo.setPositions(curve.getPoints(64).flatMap((p) => [p.x, p.y, p.z]));
    const mat = new LineMaterial({ dashed: true, dashSize: 0.55, gapSize: 0.35, transparent: true, worldUnits: false });
    const l = new Line2(geo, mat);
    l.computeLineDistances();
    return l;
  }, [curve]);
  useEffect(
    () => () => {
      line.geometry.dispose();
      line.material.dispose();
    },
    [line],
  );
  const hit = useMemo(() => new THREE.TubeGeometry(curve, 32, 0.45, 6, false), [curve]);
  useEffect(() => () => hit.dispose(), [hit]);

  const color = link.wan && link.health === 'ok' ? WAN : palette.health[link.health];
  const width = Math.min(4, 1.6 + link.resources.length * 0.2);
  useEffect(() => {
    const m = line.material;
    m.color.set(color);
    m.linewidth = selected ? width + 1.5 : hovered ? width + 0.8 : width;
    m.opacity = selected || hovered ? 1 : 0.85;
    m.resolution.set(size.width, size.height);
  }, [line, color, width, selected, hovered, size]);

  useEffect(() => {
    if (!anchor.current) return;
    anchors.set(id, anchor.current);
    return () => {
      anchors.delete(id);
    };
  }, [id]);
  // The tag's places along the line, middle first, then outwards.
  useEffect(() => {
    labelSpots.set(id, [0.5, 0.4, 0.6, 0.3, 0.7, 0.22, 0.78].map((t) => curve.getPointAt(t)));
    return () => {
      labelSpots.delete(id);
    };
  }, [id, curve]);

  // Which way writes go: from the end that is Primary for most resources.
  const fromA = link.resources.filter((r) => r.from === link.a).length;
  const fromB = link.resources.filter((r) => r.from === link.b).length;
  const flowing = link.health !== 'bad' && fromA + fromB > 0;
  const dir = fromA >= fromB ? -1 : 1;
  useFrame((_, dt) => {
    if (!flowing) return;
    line.material.dashOffset += dir * dt * (link.syncing ? 2.4 : 0.9);
  });

  const mid = curve.getPointAt(0.5);

  return (
    <group>
      <primitive object={line} />
      {/* An invisible fat tube, so a thin line is still easy to click */}
      <mesh geometry={hit} visible={false} {...pickHandlers(id)} />
      <object3D ref={anchor} position={mid} />
    </group>
  );
}

function TiebreakerLines({ model, nodes, palette }: { model: TwinModel; nodes: Map<string, TwinNode>; palette: Palette }) {
  const line = useMemo(() => {
    const seen = new Set<string>();
    const pts: THREE.Vector3[] = [];
    for (const t of model.tiebreakers) {
      const k = [t.node, t.peer].sort().join('|');
      if (seen.has(k)) continue;
      seen.add(k);
      const a = nodes.get(t.node);
      const b = nodes.get(t.peer);
      if (!a || !b) continue;
      pts.push(new THREE.Vector3(a.x, 0.06, a.z), new THREE.Vector3(b.x, 0.06, b.z));
    }
    const geo = new THREE.BufferGeometry().setFromPoints(pts);
    const l = new THREE.LineSegments(
      geo,
      new THREE.LineDashedMaterial({ color: palette.health.idle, dashSize: 0.5, gapSize: 0.4, transparent: true, opacity: 0.6 }),
    );
    l.computeLineDistances();
    return l;
  }, [model.tiebreakers, nodes, palette]);
  useEffect(
    () => () => {
      line.geometry.dispose();
      (line.material as THREE.Material).dispose();
    },
    [line],
  );
  return <primitive object={line} />;
}

export function Links({ model, palette }: { model: TwinModel; palette: Palette }) {
  const nodes = useMemo(() => new Map(model.nodes.map((n) => [n.name, n])), [model.nodes]);
  return (
    <group>
      <TiebreakerLines model={model} nodes={nodes} palette={palette} />
      {model.links.map((l) => (
        <Arc key={l.key} link={l} nodes={nodes} palette={palette} />
      ))}
    </group>
  );
}
