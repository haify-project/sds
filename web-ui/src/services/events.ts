import { getApiToken } from './api';

// Notification stream client.
//
// This reads the controller's SSE endpoint over fetch rather than with
// EventSource. EventSource cannot set an Authorization header, and the only way
// around that is putting the API token in the query string, where it ends up in
// proxy logs and browser history. Reading the stream ourselves costs a manual
// reconnect loop and buys a token that stays in a header.

export type EventSeverity = 'info' | 'warning' | 'critical';
export type EventStatus = 'firing' | 'resolved' | 'info';

export interface ClusterEvent {
  id: number;
  type: string;
  severity: EventSeverity;
  status: EventStatus;
  resource?: string;
  node?: string;
  message: string;
  details?: Record<string, string>;
  /**
   * When the event happened — under whichever name the transport it arrived on
   * uses.
   *
   * The SSE stream marshals `event.Event` directly, so its time is RFC 3339
   * under `timestamp`. `GET /v1/events` is served by the gRPC gateway from a
   * proto whose field is `timestamp_unix_ms`, so the same event arrives as
   * epoch milliseconds under `timestampUnixMs`. One type described both and
   * declared only the first, which type-checks and yields `Invalid Date` on
   * every event the REST endpoint returns.
   *
   * Read them with `eventTime`, never directly.
   */
  timestamp?: string;
  timestampUnixMs?: string;
}

/** Epoch milliseconds for an event, or null when it carries no usable time. */
export function eventTime(event: ClusterEvent): number | null {
  if (event.timestampUnixMs !== undefined) {
    const ms = Number(event.timestampUnixMs);
    return Number.isFinite(ms) ? ms : null;
  }
  if (event.timestamp !== undefined) {
    const ms = new Date(event.timestamp).getTime();
    return Number.isNaN(ms) ? null : ms;
  }
  return null;
}

export interface EventStreamHandlers {
  onEvent: (event: ClusterEvent) => void;
  /** Connection state, so the UI can say "live" or "reconnecting" honestly. */
  onStatus?: (connected: boolean) => void;
  /** Called when the controller reports notifications are switched off. */
  onDisabled?: () => void;
}

// Backoff between reconnect attempts. Starts fast (a controller failover takes
// a few seconds) and settles at 30s so a UI left open against a stopped
// controller does not hammer it forever.
const RECONNECT_MIN_MS = 1000;
const RECONNECT_MAX_MS = 30000;

/**
 * Opens the notification stream and keeps it open. Returns a function that
 * closes it; call it on unmount or the stream outlives the component.
 */
export function subscribeEvents(handlers: EventStreamHandlers): () => void {
  let closed = false;
  let controller: AbortController | null = null;
  let timer: ReturnType<typeof setTimeout> | null = null;
  let backoff = RECONNECT_MIN_MS;
  // Resuming from the last id seen is what makes a reconnect lossless: the
  // controller replays what happened while we were away instead of silently
  // skipping it.
  let lastId = 0;

  const scheduleReconnect = () => {
    if (closed) return;
    handlers.onStatus?.(false);
    timer = setTimeout(connect, backoff);
    backoff = Math.min(backoff * 2, RECONNECT_MAX_MS);
  };

  async function connect() {
    if (closed) return;
    controller = new AbortController();

    const token = getApiToken();
    const url = lastId > 0 ? `/v1/events/stream?since_id=${lastId}` : '/v1/events/stream';

    try {
      const response = await fetch(url, {
        headers: {
          Accept: 'text/event-stream',
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
        },
        signal: controller.signal,
      });

      if (response.status === 503) {
        // Notifications disabled server-side: retrying cannot fix that, so stop
        // and let the UI say so rather than showing a permanent "reconnecting"
        // state. A 503 that does not say so is something in between — a proxy
        // while the controller fails over — and is retried like any outage.
        const body = await response.text().catch(() => '');
        if (body.includes('notifications are disabled')) {
          handlers.onDisabled?.();
          closed = true;
          return;
        }
        scheduleReconnect();
        return;
      }
      if (!response.ok || !response.body) {
        scheduleReconnect();
        return;
      }

      handlers.onStatus?.(true);
      backoff = RECONNECT_MIN_MS;

      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      let buffer = '';

      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;

        buffer += decoder.decode(value, { stream: true });

        // SSE frames are separated by a blank line. A chunk boundary can land
        // mid-frame, so anything after the last separator stays buffered.
        const frames = buffer.split('\n\n');
        buffer = frames.pop() ?? '';

        for (const frame of frames) {
          const parsed = parseFrame(frame);
          if (!parsed) continue;
          lastId = Math.max(lastId, parsed.id);
          handlers.onEvent(parsed);
        }
      }
      scheduleReconnect();
    } catch {
      // Abort during teardown is the normal path, not an error worth reporting.
      if (!closed) scheduleReconnect();
    }
  }

  connect();

  return () => {
    closed = true;
    if (timer) clearTimeout(timer);
    controller?.abort();
  };
}

/** Extracts the JSON payload from one SSE frame, ignoring keepalive comments. */
function parseFrame(frame: string): ClusterEvent | null {
  const dataLines = frame
    .split('\n')
    .filter((line) => line.startsWith('data: '))
    .map((line) => line.slice(6));

  if (dataLines.length === 0) return null;
  try {
    return JSON.parse(dataLines.join('\n')) as ClusterEvent;
  } catch {
    return null;
  }
}

/** Human label for an event type, falling back to the raw dotted name. */
export function eventTypeLabel(type: string): string {
  switch (type) {
    case 'resource.failover':
      return 'Failover';
    case 'resource.degraded':
      return 'Degraded';
    case 'resource.no_primary':
      return 'No primary';
    case 'resource.promoted':
      return 'Promoted';
    case 'node.unreachable':
      return 'Node unreachable';
    case 'wan.degraded':
      return 'WAN replication';
    case 'resource.out_of_sync':
      return 'Replicas differ';
    default:
      return type;
  }
}
