// Type-only, so this does not create a runtime cycle with events.ts (which
// imports getApiToken from here). The SSE stream and the history endpoint carry
// the same event, so they share one type rather than drifting into two.
import type { ClusterEvent } from '../events';
import { queryString, type RequestFn } from './client';

// ==================== Logs ====================

export interface AuditEvent {
  timestampUnixMs: string;
  method: string;
  client: string;
  user?: string;
  target?: string;
  result: string;
  granted: boolean;
  latencyMs: string;
  error?: string;
  node?: string;
}

export interface AuditEventsResponse {
  success: boolean;
  message: string;
  events?: AuditEvent[];
  total?: string;
}

export interface AuditQuery {
  limit?: number;
  method?: string;
  target?: string;
  user?: string;
  failuresOnly?: boolean;
  sinceUnixMs?: number;
}

export interface ClusterEventsResponse {
  success: boolean;
  message: string;
  /** Oldest first, as the controller returns them. */
  events?: ClusterEvent[];
  /** Published since the controller started. Larger than the oldest id
   *  returned means the buffer has already discarded history. */
  published?: string;
  /** Dropped because a subscriber could not keep up. */
  dropped?: string;
}

export interface EventQuery {
  limit?: number;
  /** "info" (default) | "warning" | "critical" */
  minSeverity?: string;
  resource?: string;
  sinceId?: number;
}

export interface ControllerLogEntry {
  timestampUnixMs: string;
  level: string;
  logger?: string;
  caller?: string;
  message: string;
  fields?: Record<string, string>;
}

export interface ControllerLogsResponse {
  success: boolean;
  message: string;
  entries?: ControllerLogEntry[];
  node?: string;
  truncated?: boolean;
}

export interface ControllerLogQuery {
  limit?: number;
  level?: string;
  contains?: string;
}

export const logsApi = (request: RequestFn) => ({
  // ==================== Logs ====================

  listAuditEvents: (params: AuditQuery = {}) =>
    request<AuditEventsResponse>(`/audit${queryString(params)}`),

  // The controller's bounded in-memory event history — what just happened to
  // the cluster, as opposed to /audit, which records who called what. The SSE
  // stream in services/events.ts is the live tail of this same bus; this is how
  // a page that was not open at the time reads back what it missed.
  listEvents: (params: EventQuery = {}) =>
    request<ClusterEventsResponse>(`/events${queryString(params)}`),

  listControllerLogs: (params: ControllerLogQuery = {}) =>
    request<ControllerLogsResponse>(`/logs${queryString(params)}`),
});
