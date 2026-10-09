import { useState } from 'react';
import { Pencil, Plus, X } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';

/**
 * A node's or a resource's labels, shown as k=v chips, edited in place. Save
 * sends only what changed: new and changed keys with their value, removed keys
 * with an empty one, which both the node and the resource API read as "remove".
 */
export function LabelsEditor({
  labels,
  onSave,
  saving = false,
}: {
  labels: Record<string, string> | undefined;
  onSave: (changes: Record<string, string>) => void;
  saving?: boolean;
}) {
  const current = labels ?? {};
  const [draft, setDraft] = useState<Record<string, string> | null>(null);
  const [entry, setEntry] = useState('');
  const [error, setError] = useState('');

  const shown = draft ?? current;
  const keys = Object.keys(shown).sort((a, b) => a.localeCompare(b));

  const add = () => {
    const [k, ...rest] = entry.split('=');
    const key = k.trim();
    const value = rest.join('=').trim();
    if (!key || !value || rest.length === 0) {
      setError('Type key=value');
      return;
    }
    setDraft({ ...shown, [key]: value });
    setEntry('');
    setError('');
  };

  const save = () => {
    if (!draft) return;
    const changes: Record<string, string> = {};
    for (const [k, v] of Object.entries(draft)) if (current[k] !== v) changes[k] = v;
    for (const k of Object.keys(current)) if (!(k in draft)) changes[k] = '';
    if (Object.keys(changes).length) onSave(changes);
    setDraft(null);
  };

  return (
    <div className="flex flex-wrap items-center gap-1.5">
      {keys.length === 0 && !draft ? <span className="text-xs text-muted-foreground">No labels</span> : null}
      {keys.map((k) => (
        <Badge key={k} variant="secondary" className="max-w-64 gap-1 font-mono font-normal" title={`${k}=${shown[k]}`}>
          <span className="truncate">
            {k}={shown[k]}
          </span>
          {draft ? (
            <button
              type="button"
              aria-label={`Remove ${k}`}
              className="rounded-sm text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
              onClick={() => {
                const next = { ...draft };
                delete next[k];
                setDraft(next);
              }}
            >
              <X className="size-3" />
            </button>
          ) : null}
        </Badge>
      ))}
      {draft ? (
        <>
          <Input
            value={entry}
            onChange={(e) => setEntry(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault();
                add();
              }
            }}
            placeholder="key=value"
            aria-label="New label"
            className="h-7 w-40 font-mono text-xs"
          />
          <Button type="button" size="sm" variant="outline" className="h-7" onClick={add}>
            <Plus />
            Add
          </Button>
          <Button type="button" size="sm" className="h-7" onClick={save} disabled={saving}>
            Save
          </Button>
          <Button
            type="button"
            size="sm"
            variant="ghost"
            className="h-7"
            onClick={() => {
              setDraft(null);
              setEntry('');
              setError('');
            }}
          >
            Cancel
          </Button>
          {error ? <span className="text-xs text-status-bad-text">{error}</span> : null}
        </>
      ) : (
        <Button
          type="button"
          size="sm"
          variant="ghost"
          className="h-7 text-muted-foreground"
          onClick={() => setDraft({ ...current })}
          disabled={saving}
        >
          <Pencil />
          Edit labels
        </Button>
      )}
    </div>
  );
}
