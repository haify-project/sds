import type { TwinNode } from '../model';
import type { Palette } from '../kit';
import { formatAge } from '../../nodes/health';

// What a node's monitor shows: its name, state, fullest pool and what it holds.
// Drawn on a canvas that becomes the screen's texture; redrawn only when one of
// these facts changes.

export const SCREEN_W = 640;
export const SCREEN_H = 380;

const FONT = '"Instrument Sans", system-ui, sans-serif';
const MONO = '"IBM Plex Mono", ui-monospace, monospace';

export function screenKey(n: TwinNode): string {
  return JSON.stringify([
    n.name, n.state, n.health, n.controller, Math.round(n.fill * 100), n.address,
    n.replicas.map((r) => [r.resource, r.health, r.primary, r.label]),
    n.tiebreakerOf.length, n.gateways.map((g) => [g.type, g.health]),
    n.health === 'bad' ? formatAge(n.lastSeen) : '',
  ]);
}

function roundRect(g: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, r: number) {
  g.beginPath();
  g.roundRect(x, y, w, h, r);
}

export function drawScreen(g: CanvasRenderingContext2D, n: TwinNode, p: Palette) {
  const W = SCREEN_W;
  const H = SCREEN_H;
  const bg = g.createLinearGradient(0, 0, 0, H);
  bg.addColorStop(0, '#132036');
  bg.addColorStop(1, p.screen);
  g.fillStyle = bg;
  g.fillRect(0, 0, W, H);

  const tone = p.health[n.health];
  if (n.health === 'bad') {
    g.fillStyle = 'rgba(239,68,68,0.12)';
    g.fillRect(0, 0, W, H);
  }

  // Header: name and state.
  g.fillStyle = p.screenText;
  g.font = `700 54px ${FONT}`;
  g.textBaseline = 'alphabetic';
  g.fillText(n.name, 34, 76);
  const nameW = g.measureText(n.name).width;
  g.font = `600 24px ${FONT}`;
  const state = n.state.toUpperCase();
  const sw = g.measureText(state).width + 34;
  roundRect(g, 34 + nameW + 18, 44, sw, 38, 19);
  g.fillStyle = tone;
  g.fill();
  g.fillStyle = '#0b1220';
  g.fillText(state, 34 + nameW + 35, 71);

  g.font = `400 22px ${MONO}`;
  g.fillStyle = p.screenMuted;
  g.fillText(n.controller ? `${n.address}  ·  controller` : n.address, 36, 112);

  if (n.health === 'bad') {
    g.font = `700 40px ${FONT}`;
    g.fillStyle = '#ff8a8a';
    g.fillText('NOT RESPONDING', 36, 220);
    g.font = `400 22px ${FONT}`;
    g.fillStyle = p.screenMuted;
    g.fillText(`last seen ${formatAge(n.lastSeen)}`, 36, 260);
    return;
  }

  // Fullest pool.
  const pool = [...n.pools].sort((a, b) => b.used - a.used)[0];
  const y0 = 150;
  g.font = `600 22px ${FONT}`;
  g.fillStyle = p.screenMuted;
  g.fillText(pool ? `pool ${pool.name}` : 'no pool', 36, y0);
  if (pool) {
    const pct = Math.round(pool.used * 100);
    g.textAlign = 'right';
    g.fillStyle = pct >= 90 ? '#ff8a8a' : pct >= 80 ? '#ffd27a' : p.screenText;
    g.fillText(`${pct}%`, W - 36, y0);
    g.textAlign = 'left';
    roundRect(g, 36, y0 + 14, W - 72, 16, 8);
    g.fillStyle = 'rgba(255,255,255,0.1)';
    g.fill();
    roundRect(g, 36, y0 + 14, Math.max(16, (W - 72) * pool.used), 16, 8);
    g.fillStyle = pct >= 90 ? '#ef4444' : pct >= 80 ? '#f5a524' : '#4f8cff';
    g.fill();
  }

  // What it holds.
  const primaries = n.replicas.filter((r) => r.primary).length;
  const trouble = n.replicas.filter((r) => r.health === 'bad' || r.health === 'warn');
  const stats: [string, string][] = [
    [String(n.replicas.length), n.replicas.length === 1 ? 'replica' : 'replicas'],
    [String(primaries), 'primary'],
    [String(n.gateways.length), n.gateways.length === 1 ? 'gateway' : 'gateways'],
  ];
  stats.forEach(([v, k], i) => {
    const x = 36 + i * 196;
    g.font = `700 50px ${FONT}`;
    g.fillStyle = p.screenText;
    g.fillText(v, x, 262);
    g.font = `500 21px ${FONT}`;
    g.fillStyle = p.screenMuted;
    g.fillText(k, x, 292);
  });

  // The one line that needs attention, if any.
  g.font = `500 22px ${FONT}`;
  if (trouble.length) {
    const t = trouble.find((r) => r.health === 'bad') ?? trouble[0];
    g.fillStyle = t.health === 'bad' ? '#ff8a8a' : '#ffd27a';
    const more = trouble.length > 1 ? `  +${trouble.length - 1}` : '';
    g.fillText(`● ${t.resource}  ${t.label}${more}`, 36, 344);
  } else {
    g.fillStyle = '#6fe0a8';
    g.fillText('● every replica in sync', 36, 344);
  }
}
