import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import {
  AlertTriangle,
  Check,
  ChevronRight,
  RefreshCw,
  ShieldAlert,
  X,
} from 'lucide-react';
import {
  api,
  AuditEvent,
  ControllerLogEntry,
  ControllerLogQuery,
} from '@/services/api';
import { cn } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Badge } from '@/components/ui/badge';
import { Skeleton } from '@/components/ui/skeleton';
import { Switch } from '@/components/ui/switch';
import { Label } from '@/components/ui/label';
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';

// Two logs, answering two different questions, so they are two tabs rather than
// one merged stream:
//
//   Audit    — who changed what, and did it work. Persisted with the rest of
//              the controller's state, so it survives a failover.
//   Controller — what this process is doing right now. A memory ring on
//              whichever node is currently active.

const REFRESH_MS = 10000;

// Proto int64 fields arrive as strings over grpc-gateway JSON.
function num(v: string | undefined): number {
  return v ? Number(v) : 0;
}

function formatTime(unixMs: string | undefined): string {
  const n = num(unixMs);
  if (!n) return '—';
  const d = new Date(n);
  const pad = (x: number) => String(x).padStart(2, '0');
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(
    d.getMinutes(),
  )}:${pad(d.getSeconds())}`;
}

function formatLatency(ms: string | undefined): string {
  const n = num(ms);
  if (n < 1000) return `${n}ms`;
  return `${(n / 1000).toFixed(1)}s`;
}

export function LogsPage() {
  return (
    <Tabs defaultValue="audit" className="space-y-6">
      <TabsList>
        <TabsTrigger value="audit">Audit Trail</TabsTrigger>
        <TabsTrigger value="controller">Controller Log</TabsTrigger>
      </TabsList>
      <TabsContent value="audit">
        <AuditPanel />
      </TabsContent>
      <TabsContent value="controller">
        <ControllerLogPanel />
      </TabsContent>
    </Tabs>
  );
}

// ==================== Audit ====================

function AuditPanel() {
  const [failuresOnly, setFailuresOnly] = useState(false);
  const [target, setTarget] = useState('');
  const [live, setLive] = useState(true);

  const { data, isLoading, isFetching, refetch } = useQuery({
    queryKey: ['audit', failuresOnly, target],
    queryFn: () =>
      api.listAuditEvents({
        limit: 200,
        failuresOnly,
        target: target.trim() || undefined,
      }),
    refetchInterval: live ? REFRESH_MS : false,
  });

  const events = data?.events ?? [];

  return (
    <Card>
      <CardHeader className="gap-2">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <CardTitle className="text-base">Audit Trail</CardTitle>
            <CardDescription>
              Every state-changing API call. Stored with the controller&apos;s
              own state, so it follows the controller across a failover.
            </CardDescription>
          </div>
          <RefreshControls
            live={live}
            onLiveChange={setLive}
            isFetching={isFetching}
            onRefresh={() => void refetch()}
            idPrefix="audit"
          />
        </div>

        <div className="flex flex-wrap items-center gap-3 pt-1">
          <Input
            value={target}
            onChange={(e) => setTarget(e.target.value)}
            placeholder="Filter by resource…"
            className="h-8 w-56"
          />
          <div className="flex items-center gap-2">
            <Switch
              id="audit-failures"
              checked={failuresOnly}
              onCheckedChange={setFailuresOnly}
            />
            <Label htmlFor="audit-failures" className="text-sm font-normal">
              Failures only
            </Label>
          </div>
          {data?.total && (
            <span className="text-xs text-muted-foreground">
              showing {events.length} of {num(data.total)} recorded
            </span>
          )}
        </div>
      </CardHeader>

      <CardContent>
        {isLoading ? (
          <Skeleton className="h-64 w-full" />
        ) : data && !data.success ? (
          // A disabled audit log and an empty one look identical in a table,
          // and mean opposite things. Say which it is.
          <EmptyNote icon={<ShieldAlert className="h-4 w-4" />}>
            {data.message}
          </EmptyNote>
        ) : events.length === 0 ? (
          <EmptyNote>No matching entries.</EmptyNote>
        ) : (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-32">Time</TableHead>
                  <TableHead>Action</TableHead>
                  <TableHead>Target</TableHead>
                  <TableHead>Caller</TableHead>
                  <TableHead className="w-24">Result</TableHead>
                  <TableHead className="w-20 text-right">Took</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {events.map((ev, i) => (
                  <AuditRow key={`${ev.timestampUnixMs}-${i}`} event={ev} />
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function AuditRow({ event }: { event: AuditEvent }) {
  const ok = event.result === 'OK';
  return (
    <TableRow className={cn(!ok && 'bg-destructive/5')}>
      <TableCell className="font-mono text-xs text-muted-foreground">
        {formatTime(event.timestampUnixMs)}
      </TableCell>
      <TableCell className="font-medium">{event.method}</TableCell>
      <TableCell className="font-mono text-xs">{event.target || '—'}</TableCell>
      <TableCell className="font-mono text-xs text-muted-foreground">
        {event.user ? `${event.user} @ ` : ''}
        {event.client}
        {/* The controller moves; without this an entry is ambiguous about
            where it was recorded. */}
        {event.node && (
          <span className="ml-1 opacity-60">on {event.node}</span>
        )}
      </TableCell>
      <TableCell>
        {ok ? (
          <span className="flex items-center gap-1 text-xs text-emerald-600 dark:text-emerald-400">
            <Check className="h-3 w-3" />
            OK
          </span>
        ) : (
          <span
            className="flex items-center gap-1 text-xs text-destructive"
            title={event.error}
          >
            <X className="h-3 w-3" />
            {event.result}
          </span>
        )}
      </TableCell>
      <TableCell className="text-right font-mono text-xs text-muted-foreground">
        {formatLatency(event.latencyMs)}
      </TableCell>
    </TableRow>
  );
}

// ==================== Controller log ====================

const LEVELS = ['all', 'debug', 'info', 'warn', 'error'] as const;

function ControllerLogPanel() {
  const [level, setLevel] = useState<string>('info');
  const [contains, setContains] = useState('');
  const [live, setLive] = useState(true);

  const query: ControllerLogQuery = {
    limit: 300,
    level: level === 'all' ? undefined : level,
    contains: contains.trim() || undefined,
  };

  const { data, isLoading, isFetching, refetch } = useQuery({
    queryKey: ['controller-logs', level, contains],
    queryFn: () => api.listControllerLogs(query),
    refetchInterval: live ? REFRESH_MS : false,
  });

  const entries = data?.entries ?? [];

  return (
    <Card>
      <CardHeader className="gap-2">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <CardTitle className="text-base">Controller Log</CardTitle>
            <CardDescription>
              Recent output from the running controller
              {data?.node ? ` on ${data.node}` : ''}. Held in memory, so it
              starts fresh wherever the controller is now running.
            </CardDescription>
          </div>
          <RefreshControls
            live={live}
            onLiveChange={setLive}
            isFetching={isFetching}
            onRefresh={() => void refetch()}
            idPrefix="ctrl"
          />
        </div>

        <div className="flex flex-wrap items-center gap-3 pt-1">
          <Select value={level} onValueChange={setLevel}>
            <SelectTrigger className="h-8 w-32">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {LEVELS.map((l) => (
                <SelectItem key={l} value={l}>
                  {l === 'all' ? 'All levels' : l}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Input
            value={contains}
            onChange={(e) => setContains(e.target.value)}
            placeholder="Search messages…"
            className="h-8 w-64"
          />
          {data?.truncated && (
            <span className="flex items-center gap-1 text-xs text-muted-foreground">
              <AlertTriangle className="h-3 w-3" />
              buffer wrapped; older lines discarded
            </span>
          )}
        </div>
      </CardHeader>

      <CardContent>
        {isLoading ? (
          <Skeleton className="h-64 w-full" />
        ) : data && !data.success ? (
          <EmptyNote icon={<ShieldAlert className="h-4 w-4" />}>
            {data.message}
          </EmptyNote>
        ) : entries.length === 0 ? (
          <EmptyNote>No matching lines.</EmptyNote>
        ) : (
          <div className="divide-y divide-border rounded-md border border-border">
            {entries.map((e, i) => (
              <LogLine key={`${e.timestampUnixMs}-${i}`} entry={e} />
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

const LEVEL_STYLES: Record<string, string> = {
  debug: 'text-muted-foreground',
  info: 'text-sky-600 dark:text-sky-400',
  warn: 'text-amber-600 dark:text-amber-400',
  error: 'text-destructive',
};

function LogLine({ entry }: { entry: ControllerLogEntry }) {
  const [open, setOpen] = useState(false);
  const fields = Object.entries(entry.fields ?? {});
  const hasDetail = fields.length > 0 || Boolean(entry.caller);

  return (
    <div className="px-3 py-1.5 text-xs">
      <button
        type="button"
        onClick={() => hasDetail && setOpen((v) => !v)}
        className={cn(
          'flex w-full items-start gap-2 text-left font-mono',
          hasDetail ? 'cursor-pointer' : 'cursor-default',
        )}
      >
        <ChevronRight
          className={cn(
            'mt-0.5 h-3 w-3 shrink-0 transition-transform',
            open && 'rotate-90',
            !hasDetail && 'invisible',
          )}
        />
        <span className="shrink-0 text-muted-foreground">
          {formatTime(entry.timestampUnixMs)}
        </span>
        <span
          className={cn(
            'w-10 shrink-0 uppercase',
            LEVEL_STYLES[entry.level] ?? 'text-muted-foreground',
          )}
        >
          {entry.level}
        </span>
        {entry.logger && (
          <Badge variant="outline" className="shrink-0 px-1 py-0 text-[0.65rem]">
            {entry.logger}
          </Badge>
        )}
        <span className="break-all">{entry.message}</span>
      </button>

      {open && (
        <div className="ml-7 mt-1 space-y-0.5 font-mono text-[0.7rem] text-muted-foreground">
          {entry.caller && <div>caller: {entry.caller}</div>}
          {fields.map(([k, v]) => (
            <div key={k}>
              <span className="text-foreground/70">{k}</span>: {v}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// ==================== Shared ====================

function RefreshControls({
  live,
  onLiveChange,
  isFetching,
  onRefresh,
  idPrefix,
}: {
  live: boolean;
  onLiveChange: (v: boolean) => void;
  isFetching: boolean;
  onRefresh: () => void;
  idPrefix: string;
}) {
  return (
    <div className="flex items-center gap-3">
      <div className="flex items-center gap-2">
        <Switch
          id={`${idPrefix}-live`}
          checked={live}
          onCheckedChange={onLiveChange}
        />
        <Label htmlFor={`${idPrefix}-live`} className="text-sm font-normal">
          Live
        </Label>
      </div>
      <Button variant="outline" size="sm" onClick={onRefresh}>
        <RefreshCw className={cn('h-3.5 w-3.5', isFetching && 'animate-spin')} />
        Refresh
      </Button>
    </div>
  );
}

function EmptyNote({
  icon,
  children,
}: {
  icon?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <div className="flex items-center justify-center gap-2 py-12 text-sm text-muted-foreground">
      {icon}
      {children}
    </div>
  );
}
