// aiClient — talks to the `oss-agent serve` NDJSON stream that backs the SDS AI
// Copilot. The agent runs as a separate process (see ai/README.md); this client
// only speaks its wire protocol over HTTP.
//
// Wire format (oss-agent /api/chat/stream, one JSON object per line — NDJSON):
//   {"t":"tool","name":..,"args":{..}}   agent invoked a tool
//   {"t":"tool_result","name":..}         that tool returned
//   {"t":"text","d":".."}                 answer delta
//   {"t":"suggestion", ...}               a guarded action proposal (v0.4.0)
//   {"t":"error","d":".."} | {"t":"done"}
//
// The `suggestion` frame shape is normalized in parseSuggestion() below and will
// be aligned to the oss-agent v0.4.0 EventSuggestion payload once it is tagged.

// Same-origin: the controller's UI server proxies /ai to the Copilot backend,
// so this works behind a reverse proxy where :7634 is not published. Override
// with localStorage['sds.ai_base'] to point at a Copilot running elsewhere.
export function aiBase(): string {
  const override =
    typeof localStorage !== 'undefined' ? localStorage.getItem('sds.ai_base') : null;
  if (override) return override.replace(/\/$/, '');
  return '';
}

export type AISeverity = 'low' | 'medium' | 'high';

export interface AIVerdict {
  blocked: boolean;
  ruleId?: string;
  reason?: string;
  severity?: string;
}

export interface AISuggestion {
  action: string; // e.g. "ha.evict" — maps to a controller REST operation
  params: Record<string, unknown>;
  reason: string;
  severity: AISeverity;
  verdict?: AIVerdict; // red-line wall result attached by the agent
}

export type AIEvent =
  | { type: 'text'; delta: string }
  | { type: 'reset' } // discard answer text streamed so far (a preamble)
  | { type: 'tool'; name: string; args?: unknown }
  | { type: 'tool_result'; name: string }
  | { type: 'suggestion'; suggestion: AISuggestion }
  | { type: 'error'; message: string }
  | { type: 'done' };

// Raw NDJSON frame as emitted by oss-agent serve.
interface RawFrame {
  t: string;
  name?: string;
  args?: unknown;
  d?: string;
  // suggestion fields (v0.4.0 — kept permissive until the tag lands)
  action?: string;
  params?: Record<string, unknown>;
  reason?: string;
  severity?: string;
  verdict?: AIVerdict;
  suggestion?: Partial<AISuggestion>;
}

function normSeverity(s?: string): AISeverity {
  return s === 'low' || s === 'high' ? s : 'medium';
}

// parseSuggestion tolerates either a flat frame ({t:"suggestion",action,...}) or
// a nested one ({t:"suggestion",suggestion:{...}}) so it survives the exact
// v0.4.0 shape either way.
function parseSuggestion(f: RawFrame): AISuggestion | null {
  const src = f.suggestion ?? f;
  const action = typeof src.action === 'string' ? src.action : '';
  if (!action) return null;
  return {
    action,
    params: (src.params as Record<string, unknown>) ?? {},
    reason: typeof src.reason === 'string' ? src.reason : '',
    severity: normSeverity(src.severity),
    verdict: src.verdict,
  };
}

function frameToEvent(f: RawFrame): AIEvent | null {
  switch (f.t) {
    case 'text':
      return f.d ? { type: 'text', delta: f.d } : null;
    case 'reset':
      return { type: 'reset' };
    case 'tool':
      return { type: 'tool', name: f.name ?? '', args: f.args };
    case 'tool_result':
      return { type: 'tool_result', name: f.name ?? '' };
    case 'suggestion': {
      const s = parseSuggestion(f);
      return s ? { type: 'suggestion', suggestion: s } : null;
    }
    case 'error':
      return { type: 'error', message: f.d ?? 'stream error' };
    case 'done':
      return { type: 'done' };
    default:
      return null;
  }
}

export interface StreamChatResult {
  sessionId: string;
}

// streamChat POSTs one user message and invokes onEvent for every parsed frame.
// Resolves when the stream ends (or the abort signal fires). Rejects on transport
// errors so the caller can surface a connection banner.
export async function streamChat(
  message: string,
  sessionId: string | undefined,
  onEvent: (e: AIEvent) => void,
  signal?: AbortSignal,
): Promise<StreamChatResult> {
  const res = await fetch(`${aiBase()}/ai/chat/stream`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ message, session_id: sessionId ?? '' }),
    signal,
  });
  if (!res.ok || !res.body) {
    const detail = await res.text().catch(() => '');
    throw new Error(`AI backend ${res.status}: ${detail || res.statusText}`);
  }
  const sid = res.headers.get('X-Session-Id') ?? sessionId ?? '';

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = '';
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    let nl: number;
    while ((nl = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, nl).trim();
      buf = buf.slice(nl + 1);
      if (!line) continue;
      let frame: RawFrame;
      try {
        frame = JSON.parse(line) as RawFrame;
      } catch {
        continue; // skip partial/garbage lines
      }
      const ev = frameToEvent(frame);
      if (ev) onEvent(ev);
    }
  }
  // flush any trailing frame without a newline
  const tail = buf.trim();
  if (tail) {
    try {
      const ev = frameToEvent(JSON.parse(tail) as RawFrame);
      if (ev) onEvent(ev);
    } catch {
      /* ignore */
    }
  }
  return { sessionId: sid };
}
