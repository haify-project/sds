import { Link } from 'react-router';
import { ArrowRight, Crosshair, X } from 'lucide-react';
import { cn } from '@/lib/utils';
import { TONE_BG, TONE_TEXT } from '@/components/status';
import { Button } from '@/components/ui/button';
import type { Health, TwinGateway, TwinLink, TwinModel, TwinNode } from './model';
import { twin, useTwin } from './store';

// The side panel for whatever is selected in the scene: the facts behind the
// picture, and the page where it can be acted on.

function Dot({ health }: { health: Health }) {
  return <i className={cn('inline-block size-2 shrink-0 rounded-full', TONE_BG[health])} />;
}

function Row({ k, v, mono }: { k: string; v: React.ReactNode; mono?: boolean }) {
  return (
    <div className="flex items-baseline justify-between gap-3 py-1 text-[13px]">
      <span className="text-muted-foreground">{k}</span>
      <span className={cn('truncate text-right', mono && 'font-mono text-[12px]')}>{v}</span>
    </div>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="border-t border-border/70 px-4 py-3">
      <h3 className="mb-1.5 text-[12px] font-semibold text-muted-foreground">{title}</h3>
      {children}
    </section>
  );
}

function Bar({ value }: { value: number }) {
  const pct = Math.round(value * 100);
  return (
    <div className="h-1.5 w-full overflow-hidden rounded-full bg-muted">
      <div
        className={cn('h-full rounded-full', pct >= 90 ? 'bg-status-bad' : pct >= 80 ? 'bg-status-warn' : 'bg-primary')}
        style={{ width: `${Math.max(2, pct)}%` }}
      />
    </div>
  );
}

function NodeBody({ n }: { n: TwinNode }) {
  const replicas = [...n.replicas].sort(
    (a, b) => Number(b.primary) - Number(a.primary) || a.resource.localeCompare(b.resource),
  );
  return (
    <>
      <div className="px-4 pb-3">
        <Row k="Address" v={n.address} mono />
        <Row k="State" v={<span className={TONE_TEXT[n.health]}>{n.state}</span>} />
        {n.controller && <Row k="Runs" v="the controller" />}
        {n.version && <Row k="OS" v={n.version} />}
      </div>
      <Section title="Pools">
        {n.pools.length === 0 && <p className="text-[13px] text-muted-foreground">No pool on this node.</p>}
        {n.pools.map((p) => (
          <div key={p.name} className="py-1">
            <div className="mb-1 flex justify-between text-[13px]">
              <span>
                {p.name}
                <span className="text-muted-foreground">{p.thin ? ' · thin' : ''}</span>
              </span>
              <span className="tabular-nums">{Math.round(p.used * 100)}%</span>
            </div>
            <Bar value={p.used} />
          </div>
        ))}
      </Section>
      <Section title={`Replicas (${replicas.length})`}>
        {replicas.length === 0 && <p className="text-[13px] text-muted-foreground">Holds no replica.</p>}
        <ul className="max-h-64 space-y-0.5 overflow-auto">
          {replicas.map((r) => (
            <li key={r.resource} className="flex items-center gap-2 py-0.5 text-[13px]">
              <Dot health={r.health} />
              <span className="truncate font-medium">{r.resource}</span>
              {r.primary && <span className="rounded bg-primary/12 px-1.5 text-[11px] font-semibold text-primary">primary</span>}
              <span className={cn('ml-auto shrink-0 text-[12px]', TONE_TEXT[r.health])}>{r.label}</span>
            </li>
          ))}
        </ul>
      </Section>
      {n.tiebreakerOf.length > 0 && (
        <Section title="Quorum tiebreaker for">
          <p className="text-[13px]">{n.tiebreakerOf.join(', ')}</p>
        </Section>
      )}
      {n.gateways.length > 0 && (
        <Section title="Gateways served here">
          {n.gateways.map((g) => (
            <div key={g.id} className="flex items-center gap-2 py-0.5 text-[13px]">
              <Dot health={g.health} />
              <span className="font-medium uppercase">{g.type}</span>
              <span className="text-muted-foreground">{g.resource}</span>
              <span className={cn('ml-auto text-[12px]', TONE_TEXT[g.health])}>{g.state}</span>
            </div>
          ))}
        </Section>
      )}
    </>
  );
}

function LinkBody({ l }: { l: TwinLink }) {
  return (
    <Section title={`Replicated between ${l.a} and ${l.b}`}>
      {l.wan && <p className="mb-1 text-[12px] text-muted-foreground">Includes an off-site (WAN) copy.</p>}
      <ul className="max-h-80 space-y-0.5 overflow-auto">
        {l.resources.map((r) => (
          <li key={r.name} className="flex items-center gap-2 py-0.5 text-[13px]">
            <Dot health={r.health} />
            <span className="truncate font-medium">{r.name}</span>
            {r.from && <span className="shrink-0 text-[11px] text-muted-foreground">from {r.from}</span>}
            <span className={cn('ml-auto shrink-0 text-[12px]', TONE_TEXT[r.health])}>{r.label}</span>
          </li>
        ))}
      </ul>
    </Section>
  );
}

function GatewayBody({ g }: { g: TwinGateway }) {
  return (
    <div className="px-4 pb-3">
      <Row k="Protocol" v={g.type.toUpperCase()} />
      <Row k="Resource" v={g.resource} />
      <Row k="Served by" v={g.node || '—'} />
      <Row k="State" v={<span className={TONE_TEXT[g.health]}>{g.state}</span>} />
    </div>
  );
}

export function Inspector({ model }: { model: TwinModel }) {
  const selected = useTwin((s) => s.selected);
  if (!selected) return null;
  const [kind, key] = [selected.slice(0, selected.indexOf(':')), selected.slice(selected.indexOf(':') + 1)];
  const node = kind === 'node' ? model.nodes.find((n) => n.name === key) : undefined;
  const link = kind === 'link' ? model.links.find((l) => l.key === key) : undefined;
  const gw = kind === 'gw' ? model.gateways.find((g) => g.id === key) : undefined;
  if (!node && !link && !gw) return null;

  const health = node?.health ?? link?.health ?? gw!.health;
  const title = node ? node.name : link ? `${link.a} ↔ ${link.b}` : `${gw!.type.toUpperCase()} gateway`;
  const kindLabel = node ? 'Node' : link ? 'Replication' : 'Gateway';
  const page = node ? '/nodes' : link ? '/resources' : '/gateways';

  return (
    <aside className="pointer-events-auto absolute top-3 right-3 bottom-3 z-20 flex w-[300px] max-w-[calc(100%-1.5rem)] flex-col overflow-hidden rounded-xl border border-border bg-card/95 shadow-xl backdrop-blur max-md:top-auto max-md:left-3 max-md:max-h-[55%] max-md:w-auto">
      <header className="flex items-start gap-2 px-4 pt-3 pb-2">
        <div className="min-w-0 flex-1">
          <p className="text-[11px] font-medium text-muted-foreground">{kindLabel}</p>
          <h2 className="flex items-center gap-2 truncate text-[17px] font-semibold">
            <Dot health={health} />
            <span className="truncate">{title}</span>
          </h2>
        </div>
        <Button variant="ghost" size="icon" className="size-7" aria-label="Fly to it" onClick={() => twin.select(selected, true)}>
          <Crosshair className="size-4" />
        </Button>
        <Button variant="ghost" size="icon" className="size-7" aria-label="Close" onClick={() => twin.select(null)}>
          <X className="size-4" />
        </Button>
      </header>
      <div className="min-h-0 flex-1 overflow-auto">
        {node && <NodeBody n={node} />}
        {link && <LinkBody l={link} />}
        {gw && <GatewayBody g={gw} />}
      </div>
      <footer className="border-t border-border/70 px-4 py-2.5">
        <Link to={page} className="inline-flex items-center gap-1 text-[13px] font-medium text-primary hover:underline">
          Manage on the {kindLabel === 'Replication' ? 'Resources' : `${kindLabel}s`} page
          <ArrowRight className="size-3.5" />
        </Link>
      </footer>
    </aside>
  );
}
