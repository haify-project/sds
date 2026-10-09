import { useEffect, useRef, useState } from 'react';
import { Streamdown } from 'streamdown';
import { toast } from 'sonner';
import {
  Sparkles,
  Send,
  X,
  Wrench,
  ShieldAlert,
  Check,
  Loader2,
  AlertTriangle,
} from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Input } from '@/components/ui/input';
import { cn } from '@/lib/utils';
import { api } from '@/services/api';
import {
  ApprovalGoneError,
  decideApproval,
  streamChat,
  type AIApproval,
  type AIEvent,
  type AIOutcome,
  type AISuggestion,
} from '@/lib/aiClient';

interface ToolTrace {
  name: string;
  args?: unknown;
  done: boolean;
}

// argSummary renders a tool call's arguments compactly as "k=v  k=v".
function argSummary(args: unknown): string {
  if (!args || typeof args !== 'object') return '';
  return Object.entries(args as Record<string, unknown>)
    .map(([k, v]) => `${k}=${typeof v === 'object' ? JSON.stringify(v) : String(v)}`)
    .join('  ');
}

interface Msg {
  id: string;
  role: 'user' | 'assistant';
  text: string;
  tools: ToolTrace[];
  suggestions: AISuggestion[];
  approvals: AIApproval[];
  outcome?: AIOutcome;
  error?: string;
}

let msgSeq = 0;
const nextId = () => `m${++msgSeq}`;

// StreamingMarkdown reveals `text` progressively and renders it with Streamdown,
// which safely handles incomplete markdown (unterminated **, code fences, tables)
// as the prefix grows — so the answer "streams" visually even when the backend
// delivers it in one frame. State is keyed per message, so a completed message
// keeps its full text and doesn't re-animate on re-render.
function StreamingMarkdown({ text }: { text: string }) {
  const [shown, setShown] = useState(0);
  useEffect(() => {
    if (shown >= text.length) return;
    const id = setInterval(() => {
      setShown((s) => Math.min(text.length, s + Math.max(2, Math.ceil((text.length - s) / 12))));
    }, 24);
    return () => clearInterval(id);
  }, [text, shown]);
  return (
    <Streamdown className="copilot-md text-sm [&_pre]:text-xs [&_code]:text-xs">
      {text.slice(0, shown)}
    </Streamdown>
  );
}

// executeSuggestion maps a guarded action proposal onto the existing controller
// REST the UI already uses. Only a small, explicit allowlist is executable from
// the copilot (O3); everything else is surfaced as manual. Writes still go
// through the same endpoints as the normal UI buttons.
async function executeSuggestion(s: AISuggestion): Promise<string> {
  const p = s.params as Record<string, string | undefined>;
  switch (s.action) {
    case 'ha.evict':
    case 'ha.failover': {
      // Evicting the current primary IS a failover (demote here, promote elsewhere).
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

function severityBorder(sev: string): string {
  switch (sev) {
    case 'high':
      return 'border-l-red-500';
    case 'low':
      return 'border-l-muted-foreground/40';
    default:
      return 'border-l-amber-500';
  }
}

function SuggestionCard({ suggestion }: { suggestion: AISuggestion }) {
  const [state, setState] = useState<'idle' | 'running' | 'done' | 'failed' | 'dismissed'>('idle');
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
    <div className={cn('mt-2 rounded-md border border-l-2 border-border bg-card p-3 text-xs', severityBorder(suggestion.severity))}>
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-1.5">
          <Sparkles className="h-3.5 w-3.5 text-primary" />
          <span className="text-[0.7rem] font-medium text-muted-foreground">Suggested action</span>
        </div>
        <Badge variant="outline" className={cn('text-[0.65rem]', severityClass(suggestion.severity))}>
          {suggestion.severity}
        </Badge>
      </div>

      <div className="mt-1.5 font-mono font-medium">{suggestion.action}</div>
      <ArgChips args={suggestion.params} />
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
      ) : state === 'dismissed' ? (
        <div className="mt-2 flex items-center gap-1.5 text-muted-foreground">
          <X className="h-3.5 w-3.5" />
          <span>Dismissed</span>
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
          <Button
            size="sm"
            variant="ghost"
            className="h-7 px-2 text-xs"
            disabled={state === 'running'}
            onClick={() => setState('dismissed')}
          >
            Dismiss
          </Button>
        </div>
      )}
    </div>
  );
}

function ArgChips({ args }: { args: Record<string, unknown> }) {
  if (Object.keys(args).length === 0) return null;
  return (
    <div className="mt-1.5 flex flex-wrap gap-1">
      {Object.entries(args).map(([k, v]) => (
        <span key={k} className="rounded bg-muted px-1.5 py-0.5 font-mono text-[0.65rem]">
          {k}=<span className="text-foreground">{typeof v === 'object' ? JSON.stringify(v) : String(v)}</span>
        </span>
      ))}
    </div>
  );
}

// ApprovalCard answers a write call the agent is holding. Nothing has run yet:
// Approve runs exactly these arguments, Reject tells the agent it may not.
function ApprovalCard({ approval }: { approval: AIApproval }) {
  const [state, setState] = useState<'waiting' | 'sending' | 'approved' | 'rejected' | 'gone'>('waiting');

  const decide = async (approve: boolean) => {
    setState('sending');
    try {
      await decideApproval(approval.id, approve);
      setState(approve ? 'approved' : 'rejected');
    } catch (e) {
      if (e instanceof ApprovalGoneError) {
        setState('gone');
        return;
      }
      setState('waiting');
      toast.error(e instanceof Error ? e.message : String(e));
    }
  };

  return (
    <div className="mt-2 rounded-md border border-l-2 border-border border-l-amber-500 bg-card p-3 text-xs">
      <div className="flex items-center gap-1.5">
        <ShieldAlert className="h-3.5 w-3.5 text-amber-500" />
        <span className="text-[0.7rem] font-medium text-muted-foreground">Approval required</span>
      </div>
      <div className="mt-1.5 font-mono font-medium">{approval.name}</div>
      <ArgChips args={approval.args} />

      {state === 'approved' || state === 'rejected' || state === 'gone' ? (
        <div
          className={cn(
            'mt-2 flex items-center gap-1.5',
            state === 'approved' ? 'text-emerald-500' : 'text-muted-foreground',
          )}
        >
          {state === 'approved' ? <Check className="h-3.5 w-3.5" /> : <X className="h-3.5 w-3.5" />}
          <span>{state === 'approved' ? 'Approved' : state === 'rejected' ? 'Rejected' : 'No longer pending'}</span>
        </div>
      ) : (
        <div className="mt-2 flex gap-2">
          <Button
            size="sm"
            className="h-7 px-2 text-xs"
            disabled={state === 'sending'}
            onClick={() => decide(true)}
          >
            {state === 'sending' ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : 'Approve'}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            className="h-7 px-2 text-xs"
            disabled={state === 'sending'}
            onClick={() => decide(false)}
          >
            Reject
          </Button>
        </div>
      )}
    </div>
  );
}

function OutcomeStrip({ outcome }: { outcome: AIOutcome }) {
  const blocked = outcome.status === 'blocked';
  return (
    <div
      className={cn(
        'flex items-start gap-1.5 rounded-md border p-2 text-xs',
        blocked
          ? 'border-amber-500/40 bg-amber-500/10 text-amber-600 dark:text-amber-400'
          : 'border-emerald-500/40 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400',
      )}
    >
      {blocked ? (
        <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
      ) : (
        <Check className="mt-0.5 h-3.5 w-3.5 shrink-0" />
      )}
      <span>
        <span className="font-medium">{blocked ? 'Blocked' : 'Done'}</span>
        {outcome.text && <span className="text-foreground/80"> — {outcome.text}</span>}
      </span>
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

  // Resizable split pane: drag the left edge to change the panel width.
  const [width, setWidth] = useState<number>(() => {
    const s = typeof localStorage !== 'undefined' ? localStorage.getItem('sds.ai_width') : null;
    const n = s ? Number(s) : NaN;
    return Number.isFinite(n) ? n : 400;
  });
  // On phones (< md) the Copilot is a full-screen overlay, so the fixed pixel
  // width (and the drag-to-resize handle) only apply from md up.
  const [isDesktop, setIsDesktop] = useState<boolean>(() =>
    typeof window !== 'undefined'
      ? window.matchMedia('(min-width: 768px)').matches
      : true,
  );
  useEffect(() => {
    const mq = window.matchMedia('(min-width: 768px)');
    const onChange = () => setIsDesktop(mq.matches);
    mq.addEventListener('change', onChange);
    return () => mq.removeEventListener('change', onChange);
  }, []);
  const dragging = useRef(false);
  useEffect(() => {
    const clamp = (w: number) => Math.min(Math.max(w, 320), Math.round(window.innerWidth * 0.7));
    const onMove = (e: MouseEvent) => {
      if (!dragging.current) return;
      setWidth(clamp(window.innerWidth - e.clientX));
    };
    const onUp = () => {
      if (!dragging.current) return;
      dragging.current = false;
      document.body.style.userSelect = '';
      document.body.style.cursor = '';
      setWidth((w) => {
        localStorage.setItem('sds.ai_width', String(w));
        return w;
      });
    };
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
    return () => {
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
    };
  }, []);

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
    const userMsg: Msg = { id: nextId(), role: 'user', text: q, tools: [], suggestions: [], approvals: [] };
    const botMsg: Msg = { id: nextId(), role: 'assistant', text: '', tools: [], suggestions: [], approvals: [] };
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
            m.tools = [...m.tools, { name: e.name, args: e.args, done: false }];
          });
          break;
        case 'tool_result':
          patchLast((m) => {
            // mark the last still-running call of this tool as done
            const tools = [...m.tools];
            for (let i = tools.length - 1; i >= 0; i--) {
              if (tools[i].name === e.name && !tools[i].done) {
                tools[i] = { ...tools[i], done: true };
                break;
              }
            }
            m.tools = tools;
          });
          break;
        case 'suggestion':
          patchLast((m) => {
            // Dedupe: the model (or agent-go's duplicate-call re-execution) can
            // emit the same proposal twice in one turn; show it once.
            const key = (s: AISuggestion) => `${s.action}|${JSON.stringify(s.params)}`;
            if (m.suggestions.some((s) => key(s) === key(e.suggestion))) return;
            m.suggestions = [...m.suggestions, e.suggestion];
          });
          break;
        case 'approval':
          patchLast((m) => {
            m.approvals = [...m.approvals, e.approval];
          });
          break;
        case 'outcome':
          patchLast((m) => {
            m.outcome = e.outcome;
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
    <aside
      style={isDesktop ? { width } : undefined}
      className={cn(
        'flex shrink-0 flex-col border-l border-border bg-background',
        // Phone: full-screen overlay. md+: in-flow resizable side panel.
        'fixed inset-0 z-50 w-full md:relative md:inset-auto md:z-auto md:w-auto',
      )}
    >
      {/* Drag handle to resize the panel — desktop only. */}
      <div
        onMouseDown={() => {
          dragging.current = true;
          document.body.style.userSelect = 'none';
          document.body.style.cursor = 'col-resize';
        }}
        title="Drag to resize"
        className="absolute -left-1 top-0 z-10 hidden h-full w-2 cursor-col-resize hover:bg-primary/30 md:block"
      />
      <header className="flex h-14 items-center justify-between border-b border-border px-4">
        <div className="flex items-center gap-2">
          <Sparkles className="h-4 w-4 text-primary" />
          <span className="text-sm font-semibold tracking-tight">Haify Copilot</span>
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
                {m.tools.length > 0 && (
                  <div className="space-y-1 rounded-md border border-border bg-muted/30 p-2">
                    {m.tools.map((t, i) => {
                      const summary = argSummary(t.args);
                      return (
                        <div key={i} className="flex items-start gap-1.5 text-xs">
                          {t.done ? (
                            <Check className="mt-0.5 h-3 w-3 shrink-0 text-emerald-500" />
                          ) : (
                            <Loader2 className="mt-0.5 h-3 w-3 shrink-0 animate-spin text-muted-foreground" />
                          )}
                          <Wrench className="mt-0.5 h-3 w-3 shrink-0 text-muted-foreground" />
                          <span className="font-mono text-foreground">{t.name}</span>
                          {summary && (
                            <span className="truncate font-mono text-muted-foreground" title={summary}>
                              {summary}
                            </span>
                          )}
                        </div>
                      );
                    })}
                  </div>
                )}
                {m.approvals.map((a) => (
                  <ApprovalCard key={a.id} approval={a} />
                ))}
                {m.text && <StreamingMarkdown text={m.text} />}
                {m.outcome && <OutcomeStrip outcome={m.outcome} />}
                {m.suggestions.map((s, i) => (
                  <SuggestionCard key={i} suggestion={s} />
                ))}
                {m.error && (
                  <div className="flex items-start gap-1.5 rounded-md border border-red-500/40 bg-red-500/10 p-2 text-xs text-red-500">
                    <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                    <span className="break-all">{m.error}</span>
                  </div>
                )}
                {busy && m === messages[messages.length - 1] && !m.text && m.tools.length === 0 && (
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
            placeholder="Ask the Haify Copilot…"
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
