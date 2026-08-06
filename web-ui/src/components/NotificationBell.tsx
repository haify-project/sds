import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router';
import { toast } from 'sonner';
import { Bell, CheckCheck, CircleAlert, Info, TriangleAlert, WifiOff } from 'lucide-react';
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover';
import { cn } from '@/lib/utils';
import {
  eventTypeLabel,
  subscribeEvents,
  type ClusterEvent,
} from '@/services/events';

// How many notifications the bell keeps. The controller retains more; this is
// what one panel can usefully show without turning into a log viewer.
const MAX_KEPT = 200;

// Which id the operator has already seen. Per-workstation, so it belongs in
// localStorage rather than on the server: two people watching the same cluster
// have separately read (or not read) the same alerts.
const READ_KEY = 'sds.notifications.lastReadId';

// An event replayed from the controller's history on reconnect is not news.
// Only something that happened in the last half-minute earns a toast, so
// reloading the page after an incident does not re-announce all of it.
const TOAST_WINDOW_MS = 30_000;

function readLastReadId(): number {
  try {
    return Number(localStorage.getItem(READ_KEY)) || 0;
  } catch {
    return 0;
  }
}

function writeLastReadId(id: number) {
  try {
    localStorage.setItem(READ_KEY, String(id));
  } catch {
    /* the count simply resets on reload */
  }
}

// eventTitle labels a row and its toast identically. A recovery has to say so
// in the title: otherwise a cleared fault and a fresh one are both "Degraded",
// and telling them apart means reading the whole message.
function eventTitle(event: ClusterEvent): string {
  const label = eventTypeLabel(event.type);
  return event.status === 'resolved' ? `${label} resolved` : label;
}

function severityIcon(event: ClusterEvent) {
  if (event.status === 'resolved') {
    return <CheckCheck className="h-4 w-4 shrink-0 text-emerald-500" />;
  }
  switch (event.severity) {
    case 'critical':
      return <CircleAlert className="h-4 w-4 shrink-0 text-red-500" />;
    case 'warning':
      return <TriangleAlert className="h-4 w-4 shrink-0 text-amber-500" />;
    default:
      return <Info className="h-4 w-4 shrink-0 text-muted-foreground" />;
  }
}

function relativeTime(iso: string): string {
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return '';
  const secs = Math.max(0, Math.round((Date.now() - then) / 1000));
  if (secs < 60) return `${secs}s ago`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
  return `${Math.floor(secs / 86400)}d ago`;
}

export function NotificationBell() {
  const [events, setEvents] = useState<ClusterEvent[]>([]);
  const [connected, setConnected] = useState(false);
  const [disabled, setDisabled] = useState(false);
  const [open, setOpen] = useState(false);
  const [lastReadId, setLastReadId] = useState(readLastReadId);

  // Ticks once a minute so "3m ago" does not stay "3m ago" for an hour.
  const [, setClock] = useState(0);
  useEffect(() => {
    const t = setInterval(() => setClock((c) => c + 1), 60_000);
    return () => clearInterval(t);
  }, []);

  // The stream callback must not be re-created on every render, or the effect
  // below tears down and re-opens the connection constantly.
  const handleEvent = useCallback((event: ClusterEvent) => {
    setEvents((prev) => {
      // A reconnect can replay an event we already hold; ids are unique.
      if (prev.some((e) => e.id === event.id)) return prev;
      return [event, ...prev].slice(0, MAX_KEPT);
    });

    // Hold toasts until the document's stylesheets have finished loading.
    //
    // Sonner measures a toast's height once, when it mounts, and pins the
    // element to that height. Mount one before the stylesheet has applied —
    // exactly what a cold-cache first load does, and a first load during an
    // incident is precisely when this feature matters — and it is measured with
    // no width: the text wraps one character per line and the toast is sized at
    // ~1250px instead of ~90px, covering the header and the very bell it is
    // pointing at. readyState 'complete' is the signal that stylesheets are in,
    // and unlike requestAnimationFrame it still arrives in a background tab.
    //
    // Nothing is lost by holding off: the bell records the event either way.
    if (document.readyState !== 'complete') return;

    const age = Date.now() - new Date(event.timestamp).getTime();
    if (age > TOAST_WINDOW_MS || Number.isNaN(age)) return;

    if (event.status === 'resolved') {
      toast.success(eventTitle(event), { description: event.message });
    } else if (event.severity === 'critical') {
      // Long, but not indefinite. A toast that never dismisses accumulates into
      // a stack that covers the page, and the bell already keeps the record —
      // which is what makes letting the toast go safe.
      toast.error(eventTitle(event), {
        description: event.message,
        duration: 15_000,
      });
    } else if (event.severity === 'warning') {
      toast.warning(eventTitle(event), { description: event.message });
    }
  }, []);

  useEffect(
    () =>
      subscribeEvents({
        onEvent: handleEvent,
        onStatus: setConnected,
        onDisabled: () => setDisabled(true),
      }),
    [handleEvent],
  );

  const unread = useMemo(
    () => events.filter((e) => e.id > lastReadId).length,
    [events, lastReadId],
  );

  const newestId = events.length > 0 ? events[0].id : 0;

  // Opening the panel is the act of reading it.
  const markRead = useCallback(() => {
    if (newestId > lastReadId) {
      setLastReadId(newestId);
      writeLastReadId(newestId);
    }
  }, [newestId, lastReadId]);

  const openedRef = useRef(false);
  useEffect(() => {
    if (open && !openedRef.current) {
      openedRef.current = true;
      markRead();
    }
    if (!open) openedRef.current = false;
  }, [open, markRead]);

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          aria-label={unread > 0 ? `Notifications, ${unread} unread` : 'Notifications'}
          title="Notifications"
          className="relative rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-sidebar-accent hover:text-foreground"
        >
          <Bell className="h-4 w-4" />
          {unread > 0 && (
            <span className="absolute -right-0.5 -top-0.5 flex h-4 min-w-4 items-center justify-center rounded-full bg-red-500 px-1 text-[10px] font-semibold leading-none text-white">
              {unread > 99 ? '99+' : unread}
            </span>
          )}
        </button>
      </PopoverTrigger>

      <PopoverContent align="end" className="w-96 p-0">
        <div className="flex items-center justify-between border-b border-border px-3 py-2">
          <div className="flex items-center gap-2">
            <span className="text-sm font-semibold">Notifications</span>
            {!disabled && !connected && (
              <span
                className="flex items-center gap-1 text-xs text-muted-foreground"
                title="Reconnecting to the controller"
              >
                <WifiOff className="h-3 w-3" />
                offline
              </span>
            )}
          </div>
          {events.length > 0 && unread > 0 && (
            <button
              type="button"
              onClick={markRead}
              className="text-xs text-muted-foreground hover:text-foreground"
            >
              Mark all read
            </button>
          )}
        </div>

        <div className="max-h-96 overflow-auto">
          {disabled ? (
            <p className="px-3 py-6 text-center text-sm text-muted-foreground">
              Notifications are switched off on the controller.
              <br />
              Set <code className="text-xs">[alert] enabled = true</code> in
              controller.toml.
            </p>
          ) : events.length === 0 ? (
            <p className="px-3 py-6 text-center text-sm text-muted-foreground">
              Nothing to report.
            </p>
          ) : (
            <ul className="divide-y divide-border">
              {events.map((event) => (
                <li
                  key={event.id}
                  className={cn(
                    'flex gap-2 px-3 py-2.5',
                    event.id > lastReadId && 'bg-accent/40',
                  )}
                >
                  <div className="pt-0.5">{severityIcon(event)}</div>
                  <div className="min-w-0 flex-1">
                    <div className="flex items-baseline justify-between gap-2">
                      <span className="truncate text-sm font-medium">
                        {eventTitle(event)}
                      </span>
                      <span className="shrink-0 text-xs text-muted-foreground">
                        {relativeTime(event.timestamp)}
                      </span>
                    </div>
                    <p className="mt-0.5 break-words text-xs text-muted-foreground">
                      {event.message}
                    </p>
                    {event.resource && (
                      <Link
                        to="/resources"
                        onClick={() => setOpen(false)}
                        className="mt-1 inline-block text-xs text-primary hover:underline"
                      >
                        {event.resource}
                      </Link>
                    )}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}
