import { useEffect, useRef, useState } from 'react';
import { toast } from 'sonner';
import {
  Sparkles,
  Send,
  X,
  Wrench,
  ShieldAlert,
  Check,
  Loader2,
} from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Input } from '@/components/ui/input';
import { cn } from '@/lib/utils';
import { api } from '@/services/api';
import {
  streamChat,
  type AIEvent,
  type AISuggestion,
} from '@/lib/aiClient';

interface ToolTrace {
  name: string;
  args?: unknown;
}

interface Msg {
  id: string;
  role: 'user' | 'assistant';
  text: string;
  tools: ToolTrace[];
  suggestions: AISuggestion[];
  error?: string;
}

let msgSeq = 0;
const nextId = () => `m${++msgSeq}`;

// executeSuggestion maps a guarded action proposal onto the existing controller
// REST the UI already uses. Only a small, explicit allowlist is executable from
// the copilot (O3); everything else is surfaced as manual. Writes still go
// through the same endpoints as the normal UI buttons.
async function executeSuggestion(s: AISuggestion): Promise<string> {
  const p = s.params as Record<string, string | undefined>;
  switch (s.action) {
    case 'ha.evict': {
      if (!p.resource) throw new Error('missing resource');
      return (await api.evictHa(p.resource)).message;
    }
    case 'ha.create': {
      if (!p.resource) throw new Error('missing resource');
      return (
        await api.makeHa(p.resource, {
          vip: p.vip,
          mountPoint: p.mountPoint,
          fstype: p.fstype,
          services: p.services ? String(p.services).split(',').map((x) => x.trim()) : undefined,
        })
      ).message;
    }
    case 'snapshot.create': {
      if (!p.volume || !p.snapshotName) throw new Error('missing volume/snapshotName');
      return (await api.createSnapshot(p.volume, p.snapshotName, p.node)).message;
    }
    default:
      throw new Error(`"${s.action}" is not auto-executable — do it manually`);
  }
}

function severityClass(sev: string): string {
  switch (sev) {
    case 'high':
      return 'border-red-500/40 text-red-500';
    case 'low':
      return 'border-muted-foreground/30 text-muted-foreground';
    default:
      return 'border-amber-500/40 text-amber-500';
  }
}

function SuggestionCard({ suggestion }: { suggestion: AISuggestion }) {
  const [state, setState] = useState<'idle' | 'running' | 'done' | 'failed'>('idle');
  const [result, setResult] = useState('');
  const blocked = suggestion.verdict?.blocked ?? false;

  const approve = async () => {
    setState('running');
    try {
      const msg = await executeSuggestion(suggestion);
      setState('done');
      setResult(msg || 'done');
      toast.success(`${suggestion.action} executed`);
    } catch (e) {
      setState('failed');
      setResult(e instanceof Error ? e.message : String(e));
      toast.error(`${suggestion.action} failed`);
    }
  };

  return (
    <div className="mt-2 rounded-md border border-border bg-card p-3 text-xs">
      <div className="flex items-center justify-between gap-2">
        <span className="font-mono font-medium">{suggestion.action}</span>
        <Badge variant="outline" className={cn('text-[0.65rem]', severityClass(suggestion.severity))}>
          {suggestion.severity}
        </Badge>
      </div>
      {Object.keys(suggestion.params).length > 0 && (
        <div className="mt-1 font-mono text-[0.7rem] text-muted-foreground break-all">
          {Object.entries(suggestion.params)
            .map(([k, v]) => `${k}=${String(v)}`)
            .join('  ')}
        </div>
      )}
      {suggestion.reason && <p className="mt-1.5 text-muted-foreground">{suggestion.reason}</p>}

      {blocked && (
        <div className="mt-2 flex items-start gap-1.5 rounded bg-red-500/10 p-1.5 text-red-500">
          <ShieldAlert className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          <span>
            Blocked by red-line
            {suggestion.verdict?.ruleId ? ` (${suggestion.verdict.ruleId})` : ''}
            {suggestion.verdict?.reason ? `: ${suggestion.verdict.reason}` : ''}
          </span>
        </div>
      )}

      {state === 'done' || state === 'failed' ? (
        <div
          className={cn(
            'mt-2 flex items-center gap-1.5',
            state === 'done' ? 'text-emerald-500' : 'text-red-500',
          )}
        >
          {state === 'done' ? <Check className="h-3.5 w-3.5" /> : <X className="h-3.5 w-3.5" />}
          <span className="break-all">{result}</span>
        </div>
      ) : (
        <div className="mt-2 flex gap-2">
          <Button
            size="sm"
            className="h-7 px-2 text-xs"
            disabled={blocked || state === 'running'}
            onClick={approve}
          >
            {state === 'running' ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : 'Approve'}
          </Button>
        </div>
      )}
    </div>
  );
}

export function AICopilot({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [messages, setMessages] = useState<Msg[]>([]);
  const [input, setInput] = useState('');
  const [busy, setBusy] = useState(false);
  const sessionRef = useRef<string | undefined>(
    typeof localStorage !== 'undefined'
      ? localStorage.getItem('sds.ai_session') ?? undefined
      : undefined,
  );
  const scrollRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight });
  }, [messages]);

  const patchLast = (fn: (m: Msg) => void) =>
    setMessages((prev) => {
      if (prev.length === 0) return prev;
      const copy = [...prev];
      const last = { ...copy[copy.length - 1] };
      fn(last);
      copy[copy.length - 1] = last;
      return copy;
    });

  const send = async () => {
    const q = input.trim();
    if (!q || busy) return;
    setInput('');
    setBusy(true);
    const userMsg: Msg = { id: nextId(), role: 'user', text: q, tools: [], suggestions: [] };
    const botMsg: Msg = { id: nextId(), role: 'assistant', text: '', tools: [], suggestions: [] };
    setMessages((prev) => [...prev, userMsg, botMsg]);

    const onEvent = (e: AIEvent) => {
      switch (e.type) {
        case 'text':
          patchLast((m) => {
            m.text += e.delta;
          });
          break;
        case 'reset':
          patchLast((m) => {
            m.text = '';
          });
          break;
        case 'tool':
          patchLast((m) => {
            m.tools = [...m.tools, { name: e.name, args: e.args }];
          });
          break;
        case 'suggestion':
          patchLast((m) => {
            m.suggestions = [...m.suggestions, e.suggestion];
          });
          break;
        case 'error':
          patchLast((m) => {
            m.error = e.message;
          });
          break;
        default:
          break;
      }
    };

    try {
      const { sessionId } = await streamChat(q, sessionRef.current, onEvent);
      if (sessionId) {
        sessionRef.current = sessionId;
        localStorage.setItem('sds.ai_session', sessionId);
      }
    } catch (e) {
      patchLast((m) => {
        m.error = e instanceof Error ? e.message : 'connection failed';
      });
    } finally {
      setBusy(false);
    }
  };

  if (!open) return null;

  return (
    <aside className="flex w-96 shrink-0 flex-col border-l border-border bg-background">
      <header className="flex h-14 items-center justify-between border-b border-border px-4">
        <div className="flex items-center gap-2">
          <Sparkles className="h-4 w-4 text-primary" />
          <span className="text-sm font-semibold tracking-tight">SDS Copilot</span>
        </div>
        <Button variant="ghost" size="icon" className="h-7 w-7" onClick={onClose}>
          <X className="h-4 w-4" />
        </Button>
      </header>

      <div ref={scrollRef} className="flex-1 space-y-4 overflow-auto p-4">
        {messages.length === 0 && (
          <p className="mt-8 text-center text-sm text-muted-foreground">
            Ask about the cluster, diagnose an issue, or request a guarded action.
          </p>
        )}
        {messages.map((m) => (
          <div key={m.id} className={cn('text-sm', m.role === 'user' ? 'text-right' : '')}>
            {m.role === 'user' ? (
              <div className="inline-block rounded-lg bg-primary px-3 py-2 text-primary-foreground">
                {m.text}
              </div>
            ) : (
              <div className="space-y-2">
                {m.tools.map((t, i) => (
                  <div
                    key={i}
                    className="flex items-center gap-1.5 text-xs text-muted-foreground"
                  >
                    <Wrench className="h-3 w-3" />
                    <span className="font-mono">{t.name}</span>
                  </div>
                ))}
                {m.text && <div className="whitespace-pre-wrap leading-relaxed">{m.text}</div>}
                {m.suggestions.map((s, i) => (
                  <SuggestionCard key={i} suggestion={s} />
                ))}
                {m.error && <div className="text-xs text-red-500">⚠ {m.error}</div>}
                {busy && m === messages[messages.length - 1] && !m.text && (
                  <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
                )}
              </div>
            )}
          </div>
        ))}
      </div>

      <footer className="border-t border-border p-3">
        <form
          className="flex items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            send();
          }}
        >
          <Input
            value={input}
            onChange={(e) => setInput(e.target.value)}
            placeholder="Ask the SDS Copilot…"
            disabled={busy}
          />
          <Button type="submit" size="icon" disabled={busy || !input.trim()}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Send className="h-4 w-4" />}
          </Button>
        </form>
      </footer>
    </aside>
  );
}
