import type { PlacedNode } from '../ResourceTopology';
import type { Palette } from '@/pages/twin/kit';

// What one member's monitor says about its copy of the resource: whose it is,
// its role, its disk, and where the bytes sit.

export const SCREEN_W = 560;
export const SCREEN_H = 330;

const FONT = '"Instrument Sans", system-ui, sans-serif';
const MONO = '"IBM Plex Mono", ui-monospace, monospace';

const KIND_NOTE: Record<PlacedNode['kind'], string> = {
  replica: '',
  tiebreaker: 'quorum vote only',
  client: 'diskless client',
  dr: 'disaster recovery · protocol A',
};

export function screenKey(n: PlacedNode, backing: string): string {
  return JSON.stringify([n.name, n.kind, n.state?.role, n.state?.diskState, n.state?.replicationState, n.state?.syncPercent, backing]);
}

export function drawScreen(g: CanvasRenderingContext2D, n: PlacedNode, backing: string, tone: string, p: Palette) {
  const W = SCREEN_W;
  const H = SCREEN_H;
  const bg = g.createLinearGradient(0, 0, 0, H);
  bg.addColorStop(0, '#132036');
  bg.addColorStop(1, p.screen);
  g.fillStyle = bg;
  g.fillRect(0, 0, W, H);

  const diskless = n.kind === 'tiebreaker' || n.kind === 'client';
  const primary = !diskless && n.state?.role === 'Primary';

  g.fillStyle = p.screenText;
  g.font = `700 60px ${FONT}`;
  g.fillText(n.name, 30, 82);

  // Role chip: the one fact this view exists to show.
  const role = diskless ? 'Diskless' : n.state?.role || 'Unknown';
  g.font = `600 28px ${FONT}`;
  const rw = g.measureText(role).width + 36;
  g.beginPath();
  g.roundRect(30, 112, rw, 48, 12);
  g.fillStyle = primary ? '#3b82f6' : 'rgba(255,255,255,0.12)';
  g.fill();
  g.fillStyle = primary ? '#ffffff' : p.screenText;
  g.fillText(role, 48, 146);
  if (KIND_NOTE[n.kind]) {
    g.font = `500 24px ${FONT}`;
    g.fillStyle = p.screenMuted;
    g.fillText(KIND_NOTE[n.kind], 30 + rw + 18, 146);
  }

  // Disk state, in its status colour, then where the bytes are.
  const sync = n.state?.syncPercent;
  const syncing = n.state?.replicationState?.startsWith('Sync') && sync !== undefined && sync < 100;
  const disk = syncing ? `syncing ${Math.round(sync)}%` : n.state?.diskState || 'unknown';
  g.font = `600 32px ${MONO}`;
  g.fillStyle = tone;
  g.fillText(disk, 30, 222);
  if (!diskless && backing) {
    g.font = `400 26px ${MONO}`;
    g.fillStyle = p.screenMuted;
    g.fillText(backing, 30, 270);
  }
  if (syncing) {
    g.beginPath();
    g.roundRect(30, 290, W - 60, 12, 6);
    g.fillStyle = 'rgba(255,255,255,0.12)';
    g.fill();
    g.beginPath();
    g.roundRect(30, 290, Math.max(12, (W - 60) * (sync / 100)), 12, 6);
    g.fillStyle = '#f5a524';
    g.fill();
  }
}
