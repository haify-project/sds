import { cn } from '@/lib/utils';
import type { Resource, ResourceStatus } from '@/services/api';
import { placeNodes, type PlacedNode } from '../ResourceTopology';

// What stands in a 3D view's place until its canvas has drawn: the members in
// a row, the Primary marked, in plain DOM. Same box as the canvas, so nothing
// on the page moves when the room appears.

function Member({ node }: { node: PlacedNode }) {
  const diskless = node.kind === 'tiebreaker' || node.kind === 'client';
  const primary = !diskless && node.state?.role === 'Primary';
  const role = node.kind === 'dr' ? 'DR' : diskless ? 'Diskless' : node.state?.role || '';
  return (
    <div
      className={cn(
        'flex h-14 w-24 shrink-0 flex-col justify-center rounded-lg border bg-card/70 px-2.5',
        primary ? 'border-primary/50' : 'border-border/70',
        node.kind === 'dr' && 'border-dashed',
      )}
    >
      <span className="truncate font-mono text-[11.5px] font-medium">{node.name}</span>
      <span className={cn('text-[11px]', primary ? 'text-primary' : 'text-muted-foreground')}>{role}</span>
    </div>
  );
}

export function TopologyPlaceholder({ resource, status }: { resource: Resource; status: ResourceStatus }) {
  const { local, remote } = placeNodes(resource, status);
  return (
    <div className="absolute inset-0 flex items-center justify-center overflow-hidden" aria-hidden>
      <div className="flex items-center gap-3 px-4">
        {local.map((n) => (
          <Member key={n.name} node={n} />
        ))}
        {remote.length > 0 && <span className="mx-2 h-px w-8 shrink-0 border-t border-dashed border-muted-foreground/50" />}
        {remote.map((n) => (
          <Member key={n.name} node={n} />
        ))}
      </div>
    </div>
  );
}
