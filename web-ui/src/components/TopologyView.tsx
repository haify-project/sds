import { lazy, Suspense, useEffect, useState } from 'react';
import { cn } from '@/lib/utils';
import type { Resource, ResourceStatus } from '@/services/api';
import { ResourceTopology } from './ResourceTopology';
import { TopologyPlaceholder } from './topology3d/Placeholder';

// A resource's replication topology, as a 3D room of computers or as the flat
// diagram. Which one is a per-workstation preference.
//
// Each 3D view is a WebGL context and a browser allows only a handful, while
// the HA page lists one topology per promoter. So a 3D view exists only while
// it is on screen (a plain placeholder of the same size stands in until its
// first frame), and three.js is fetched while the browser is idle.

const load3d = () => import('./topology3d/ResourceTopology3D');
const ResourceTopology3D = lazy(load3d);

function preload3d() {
  const w = window as Window & { requestIdleCallback?: (cb: () => void) => number };
  const go = () => void load3d().catch(() => undefined);
  if (w.requestIdleCallback) w.requestIdleCallback(go);
  else setTimeout(go, 200);
}

const MODE_KEY = 'haify.topology.mode';
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

/** Whether the element is on screen (or nearly). A callback ref, so it also
 * starts watching when the element first appears, as on a switch to 3D. */
function useOnScreen<T extends Element>() {
  const [el, setEl] = useState<T | null>(null);
  const [on, setOn] = useState(false);
  useEffect(() => {
    if (!el) {
      setOn(false);
      return;
    }
    const io = new IntersectionObserver(([e]) => setOn(e.isIntersecting), { rootMargin: '150px 0px' });
    io.observe(el);
    return () => io.disconnect();
  }, [el]);
  return [setEl, on] as const;
}

export function TopologyView({ resource, status }: { resource: Resource; status: ResourceStatus }) {
  const [mode, setMode] = useState<Mode>(readMode);
  const [ref, onScreen] = useOnScreen<HTMLDivElement>();
  const show3d = webgl && mode === '3d';
  // The canvas has drawn its first frame; until then the placeholder shows.
  const [ready, setReady] = useState(false);
  useEffect(() => {
    if (!onScreen || !show3d) setReady(false);
  }, [onScreen, show3d]);
  useEffect(() => {
    if (show3d) preload3d();
  }, [show3d]);

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
        <div ref={ref} className="relative h-[300px] w-full sm:h-[340px]">
          {!ready && <TopologyPlaceholder resource={resource} status={status} />}
          {onScreen && (
            <Suspense fallback={null}>
              <div className={cn('absolute inset-0 transition-opacity duration-300', ready ? 'opacity-100' : 'opacity-0')}>
                <ResourceTopology3D resource={resource} status={status} onReady={() => setReady(true)} />
              </div>
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
