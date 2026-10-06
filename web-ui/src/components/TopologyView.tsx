import { lazy, Suspense, useEffect, useRef, useState } from 'react';
import { Loader2 } from 'lucide-react';
import { cn } from '@/lib/utils';
import type { Resource, ResourceStatus } from '@/services/api';
import { ResourceTopology } from './ResourceTopology';

// A resource's replication topology, as a 3D room of computers or as the flat
// diagram. Which one is a per-workstation preference.
//
// Each 3D view is a WebGL context and a browser allows only a handful, while
// the HA page lists one topology per promoter. So a 3D view exists only while
// it is on screen, and three.js loads only when the first one does.

const ResourceTopology3D = lazy(() => import('./topology3d/ResourceTopology3D'));

const MODE_KEY = 'sds.topology.mode';
type Mode = '3d' | '2d';

function readMode(): Mode {
  try {
    return localStorage.getItem(MODE_KEY) === '2d' ? '2d' : '3d';
  } catch {
    return '3d';
  }
}

const webgl = (() => {
  try {
    return typeof document !== 'undefined' && Boolean(document.createElement('canvas').getContext('webgl2'));
  } catch {
    return false;
  }
})();

function useOnScreen<T extends Element>() {
  const ref = useRef<T>(null);
  const [on, setOn] = useState(false);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const io = new IntersectionObserver(([e]) => setOn(e.isIntersecting), { rootMargin: '200px 0px' });
    io.observe(el);
    return () => io.disconnect();
  }, []);
  return [ref, on] as const;
}

export function TopologyView({ resource, status }: { resource: Resource; status: ResourceStatus }) {
  const [mode, setMode] = useState<Mode>(readMode);
  const [ref, onScreen] = useOnScreen<HTMLDivElement>();
  const show3d = webgl && mode === '3d';

  const choose = (m: Mode) => {
    setMode(m);
    try {
      localStorage.setItem(MODE_KEY, m);
    } catch {
      /* the choice just won't persist */
    }
  };

  return (
    <div className="relative">
      {webgl && (
        <div className="absolute top-0 right-0 z-10 inline-flex rounded-md border border-border bg-card p-0.5 text-[12px]">
          {(['3d', '2d'] as const).map((m) => (
            <button
              key={m}
              type="button"
              aria-pressed={mode === m}
              onClick={() => choose(m)}
              className={cn(
                'rounded px-2 py-0.5 font-medium uppercase text-muted-foreground',
                mode === m && 'bg-muted text-foreground',
              )}
            >
              {m}
            </button>
          ))}
        </div>
      )}
      {show3d ? (
        <div ref={ref} className="relative h-[300px] w-full sm:h-[340px]" role="img" aria-label={`Replication topology for ${resource.name}`}>
          {onScreen && (
            <Suspense
              fallback={
                <div className="flex h-full items-center justify-center">
                  <Loader2 className="size-4 animate-spin text-muted-foreground" />
                </div>
              }
            >
              <ResourceTopology3D resource={resource} status={status} />
            </Suspense>
          )}
          <p className="pointer-events-none absolute bottom-1 left-0 text-[11.5px] text-muted-foreground">
            The glowing case is the Primary; dashes run from it to each copy. Drag to turn.
          </p>
        </div>
      ) : (
        <ResourceTopology resource={resource} status={status} />
      )}
    </div>
  );
}
