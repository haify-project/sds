import { memo } from 'react';
import { cn } from '@/lib/utils';
import { TONE_BG } from '@/components/status';
import type { Health, TwinModel } from './model';
import { labelEls, twin, useTwin } from './store';

// Name tags over the scene. Plain DOM, so they stay crisp and selectable; the
// scene's label projector moves them every frame.

const Tag = memo(function Tag({
  id,
  health,
  title,
  detail,
  always,
}: {
  id: string;
  health: Health;
  title: string;
  detail?: string;
  /** Shown even with labels off: anything that needs a look. */
  always: boolean;
}) {
  const selected = useTwin((s) => s.selected === id);
  const hovered = useTwin((s) => s.hovered === id);
  const all = useTwin((s) => s.labels);
  const show = all || always || selected || hovered;
  const alarm = health === 'bad';
  return (
    <button
      type="button"
      ref={(el) => {
        if (el) labelEls.set(id, el);
        else labelEls.delete(id);
      }}
      onClick={() => twin.select(id)}
      onDoubleClick={() => twin.select(id, true)}
      className={cn(
        'pointer-events-auto absolute left-0 top-0 flex items-center gap-1.5 whitespace-nowrap rounded-full border border-border/60 bg-card/95 py-1 pl-2 pr-2.5 text-[11.5px] text-card-foreground shadow-md backdrop-blur will-change-transform',
        alarm && 'border-transparent bg-destructive text-white',
        selected && 'border-transparent bg-primary text-primary-foreground',
        !show && 'hidden',
      )}
    >
      <i className={cn('size-[7px] shrink-0 rounded-full', alarm || selected ? 'bg-white' : TONE_BG[health])} />
      <b className="font-semibold">{title}</b>
      {detail && <span className={cn('text-muted-foreground', (alarm || selected) && 'text-white/80')}>{detail}</span>}
    </button>
  );
});

export function Labels({ model }: { model: TwinModel }) {
  return (
    <div className="pointer-events-none absolute inset-0 z-10 overflow-hidden">
      {model.nodes.map((n) => (
        <Tag
          key={n.name}
          id={`node:${n.name}`}
          health={n.health}
          title={n.name}
          detail={n.health !== 'ok' ? n.state : n.controller ? 'controller' : `${n.replicas.length} replicas`}
          always
        />
      ))}
      {model.links.map((l) => {
        const sync = l.resources.filter((r) => r.syncing).length;
        const bad = l.resources.filter((r) => r.health === 'bad').length;
        const detail = bad ? `${bad} degraded` : sync ? `${sync} syncing` : l.health === 'idle' ? 'state unknown' : 'in sync';
        return (
          <Tag
            key={l.key}
            id={`link:${l.key}`}
            health={l.health}
            title={`${l.resources.length} resource${l.resources.length === 1 ? '' : 's'}`}
            detail={detail}
            always={l.health === 'bad' || l.health === 'warn'}
          />
        );
      })}
      {model.gateways.map((g) => (
        <Tag
          key={g.id}
          id={`gw:${g.id}`}
          health={g.health}
          title={g.type.toUpperCase()}
          detail={g.resource}
          always={g.health === 'bad'}
        />
      ))}
    </div>
  );
}
