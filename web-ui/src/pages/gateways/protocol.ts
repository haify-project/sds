import { toneOf, type StatusTone } from '@/components/status';

// ==================== Protocol vocabulary ====================

export type GwKind = 'nfs' | 'iscsi' | 'nvme';

// The ports are the backend's own constants (pkg/gateway/gateway.go), not a
// guess: a gateway is reachable on exactly one of them.
export const PROTOCOL: Record<GwKind, { label: string; port: number }> = {
  nfs: { label: 'NFS', port: 2049 },
  iscsi: { label: 'iSCSI', port: 3260 },
  nvme: { label: 'NVMe-oF', port: 4420 },
};

// The backend reports the NVMe type as "nvmeof" (reactor config naming).
export function gwKind(type: string | undefined): GwKind {
  if (type === 'nvmeof' || type === 'nvme') return 'nvme';
  if (type === 'iscsi') return 'iscsi';
  return 'nfs';
}

// The list endpoint says "started"; older records and the reactor say
// "running". Both mean the promoter's services came up.
export function isRunning(state: string | undefined): boolean {
  return state === 'started' || state === 'running';
}

// Thin alias: `toneOf` now knows "started" itself, so there is nothing to
// normalise — kept as a name because the call sites read better for it.
export function gatewayTone(state: string | undefined): StatusTone {
  return toneOf(state);
}

export function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? '' : 's'}`;
}
