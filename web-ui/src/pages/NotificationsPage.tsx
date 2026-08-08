import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { Bell, Plus, Trash2, Loader2, Send, BellOff } from 'lucide-react';
import { api, type NotifyChannel, type NotifyChannelInput } from '@/services/api';
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Skeleton } from '@/components/ui/skeleton';
import { Switch } from '@/components/ui/switch';
import { toast } from 'sonner';

const FALLBACK_KINDS = ['generic', 'feishu', 'slack', 'wecom', 'dingtalk'];

const KIND_LABELS: Record<string, string> = {
  generic: 'Webhook (raw JSON)',
  feishu: 'Feishu / Lark bot',
  slack: 'Slack incoming webhook',
  wecom: 'WeCom group bot',
  dingtalk: 'DingTalk robot',
};

// What to put in the URL field, per kind. Getting this wrong is the single
// most common way a channel ends up configured and silent.
const KIND_HINTS: Record<string, string> = {
  generic: 'Any endpoint of yours. Receives the event JSON unchanged.',
  feishu:
    'Feishu bot address: https://open.feishu.cn/open-apis/bot/v2/hook/…',
  slack: 'Slack incoming webhook: https://hooks.slack.com/services/…',
  wecom:
    'WeCom group bot: https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=…',
  dingtalk:
    'DingTalk robot: https://oapi.dingtalk.com/robot/send?access_token=…',
};

const SEVERITIES = [
  { value: 'info', label: 'Everything (info and above)' },
  { value: 'warning', label: 'Warning and above' },
  { value: 'critical', label: 'Critical only' },
];

const EMPTY_FORM: NotifyChannelInput = {
  name: '',
  kind: 'feishu',
  url: '',
  minSeverity: 'warning',
  enabled: true,
  secret: '',
};

export function NotificationsPage() {
  const queryClient = useQueryClient();
  const [dialogOpen, setDialogOpen] = useState(false);
  const [form, setForm] = useState<NotifyChannelInput>(EMPTY_FORM);
  const [editing, setEditing] = useState<NotifyChannel | null>(null);
  const [testing, setTesting] = useState<string | null>(null);

  const { data, isLoading } = useQuery({
    queryKey: ['notify', 'channels'],
    queryFn: () => api.listNotifyChannels(),
  });

  const channels = data?.channels ?? [];
  const kinds = data?.kinds?.length ? data.kinds : FALLBACK_KINDS;

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ['notify', 'channels'] });

  const saveMutation = useMutation({
    mutationFn: (input: NotifyChannelInput) => api.saveNotifyChannel(input),
    onSuccess: (res) => {
      if (!res.success) {
        toast.error(res.message);
        return;
      }
      toast.success(res.message);
      setDialogOpen(false);
      setForm(EMPTY_FORM);
      setEditing(null);
      invalidate();
    },
    onError: (err: Error) => toast.error(err.message),
  });

  const deleteMutation = useMutation({
    mutationFn: (name: string) => api.deleteNotifyChannel(name),
    onSuccess: (res) => {
      res.success ? toast.success(res.message) : toast.error(res.message);
      invalidate();
    },
    onError: (err: Error) => toast.error(err.message),
  });

  // A test is not a formality. Feishu, WeCom and DingTalk answer HTTP 200 for
  // a message they refused, so the only way to learn that a channel is silent
  // is to make it speak and read the reply.
  const runTest = async (name: string) => {
    setTesting(name);
    try {
      const res = await api.testNotifyChannel(name);
      res.success
        ? toast.success(res.message)
        : toast.error(res.message, { duration: 10000 });
    } catch (err) {
      toast.error((err as Error).message, { duration: 10000 });
    } finally {
      setTesting(null);
    }
  };

  const openCreate = () => {
    setEditing(null);
    setForm(EMPTY_FORM);
    setDialogOpen(true);
  };

  const openEdit = (ch: NotifyChannel) => {
    setEditing(ch);
    setForm({
      name: ch.name,
      kind: ch.kind || 'generic',
      url: ch.url,
      minSeverity: ch.minSeverity || 'info',
      types: ch.types,
      headers: ch.headers,
      enabled: ch.enabled ?? true,
      // Left empty on purpose: the API never returns a stored secret, and an
      // empty one means "keep it". Typing a new value replaces it.
      secret: '',
    });
    setDialogOpen(true);
  };

  const toggleEnabled = (ch: NotifyChannel, enabled: boolean) =>
    saveMutation.mutate({
      name: ch.name,
      kind: ch.kind,
      url: ch.url,
      minSeverity: ch.minSeverity,
      types: ch.types,
      headers: ch.headers,
      enabled,
    });

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Notifications</h1>
          <p className="text-sm text-muted-foreground">
            Where this cluster's alerts are delivered. Changes take effect
            immediately — the controller is not restarted.
          </p>
        </div>
        <Button onClick={openCreate}>
          <Plus className="mr-2 h-4 w-4" />
          Add channel
        </Button>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Bell className="h-5 w-5" />
            Channels
          </CardTitle>
          <CardDescription>
            Send a test after adding one. Feishu, WeCom and DingTalk reply
            HTTP 200 even when they refuse a message, so a mistyped bot URL
            looks like it is working until the day it matters.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {isLoading ? (
            <div className="space-y-2">
              <Skeleton className="h-10 w-full" />
              <Skeleton className="h-10 w-full" />
            </div>
          ) : channels.length === 0 ? (
            <div className="py-10 text-center text-sm text-muted-foreground">
              <BellOff className="mx-auto mb-3 h-8 w-8 opacity-50" />
              No channels yet. Alerts are still recorded and visible in the bell
              menu, but nobody is being told about them.
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Kind</TableHead>
                  <TableHead>Sends</TableHead>
                  <TableHead>Endpoint</TableHead>
                  <TableHead>Enabled</TableHead>
                  <TableHead className="text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {channels.map((ch) => (
                  <TableRow key={ch.name}>
                    <TableCell className="font-medium">
                      <button
                        className="hover:underline"
                        onClick={() => openEdit(ch)}
                      >
                        {ch.name}
                      </button>
                      {ch.hasSecret && (
                        <Badge variant="outline" className="ml-2">
                          signed
                        </Badge>
                      )}
                    </TableCell>
                    <TableCell>
                      <Badge variant="secondary">
                        {KIND_LABELS[ch.kind] ?? ch.kind}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-sm text-muted-foreground">
                      {ch.minSeverity || 'info'} and above
                    </TableCell>
                    <TableCell className="max-w-[22rem] truncate font-mono text-xs text-muted-foreground">
                      {ch.url}
                    </TableCell>
                    <TableCell>
                      <Switch
                        checked={ch.enabled ?? false}
                        onCheckedChange={(v) => toggleEnabled(ch, v)}
                      />
                    </TableCell>
                    <TableCell className="text-right">
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={testing === ch.name}
                        onClick={() => runTest(ch.name)}
                      >
                        {testing === ch.name ? (
                          <Loader2 className="h-4 w-4 animate-spin" />
                        ) : (
                          <Send className="h-4 w-4" />
                        )}
                        <span className="ml-2">Test</span>
                      </Button>
                      <AlertDialog>
                        <AlertDialogTrigger asChild>
                          <Button variant="ghost" size="sm">
                            <Trash2 className="h-4 w-4 text-destructive" />
                          </Button>
                        </AlertDialogTrigger>
                        <AlertDialogContent>
                          <AlertDialogHeader>
                            <AlertDialogTitle>
                              Delete channel "{ch.name}"?
                            </AlertDialogTitle>
                            <AlertDialogDescription>
                              Alerts will stop being delivered here. If you only
                              want to silence it for now, turn it off instead —
                              that keeps the endpoint so you do not have to find
                              the bot URL again later.
                            </AlertDialogDescription>
                          </AlertDialogHeader>
                          <AlertDialogFooter>
                            <AlertDialogCancel>Cancel</AlertDialogCancel>
                            <AlertDialogAction
                              onClick={() => deleteMutation.mutate(ch.name)}
                            >
                              Delete
                            </AlertDialogAction>
                          </AlertDialogFooter>
                        </AlertDialogContent>
                      </AlertDialog>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>
              {editing ? `Edit ${editing.name}` : 'Add notification channel'}
            </DialogTitle>
            <DialogDescription>
              {KIND_HINTS[form.kind] ?? ''}
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="channel-name">Name</Label>
              <Input
                id="channel-name"
                value={form.name}
                disabled={!!editing}
                placeholder="oncall"
                onChange={(e) => setForm({ ...form, name: e.target.value })}
              />
            </div>

            <div className="space-y-2">
              <Label>Kind</Label>
              <Select
                value={form.kind}
                onValueChange={(kind) => setForm({ ...form, kind })}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {kinds.map((k) => (
                    <SelectItem key={k} value={k}>
                      {KIND_LABELS[k] ?? k}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-2">
              <Label htmlFor="channel-url">Webhook URL</Label>
              <Input
                id="channel-url"
                value={form.url}
                placeholder="https://…"
                onChange={(e) => setForm({ ...form, url: e.target.value })}
              />
            </div>

            <div className="space-y-2">
              <Label>Deliver</Label>
              <Select
                value={form.minSeverity ?? 'info'}
                onValueChange={(minSeverity) =>
                  setForm({ ...form, minSeverity })
                }
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {SEVERITIES.map((s) => (
                    <SelectItem key={s.value} value={s.value}>
                      {s.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            {form.kind === 'dingtalk' && (
              <div className="space-y-2">
                <Label htmlFor="channel-secret">
                  Signing secret (加签){' '}
                  <span className="font-normal text-muted-foreground">
                    optional
                  </span>
                </Label>
                <Input
                  id="channel-secret"
                  type="password"
                  autoComplete="new-password"
                  value={form.secret ?? ''}
                  placeholder={
                    editing?.hasSecret
                      ? 'Stored — leave blank to keep it'
                      : 'SEC…'
                  }
                  onChange={(e) => setForm({ ...form, secret: e.target.value })}
                />
                <p className="text-xs text-muted-foreground">
                  Only needed if the robot is secured by signing rather than by
                  a keyword or an IP allowlist. The stored value is never sent
                  back to this page.
                </p>
              </div>
            )}

            <div className="flex items-center justify-between rounded-md border p-3">
              <div>
                <Label>Enabled</Label>
                <p className="text-xs text-muted-foreground">
                  Off keeps the configuration but delivers nothing.
                </p>
              </div>
              <Switch
                checked={form.enabled}
                onCheckedChange={(enabled) => setForm({ ...form, enabled })}
              />
            </div>
          </div>

          <DialogFooter>
            <Button variant="outline" onClick={() => setDialogOpen(false)}>
              Cancel
            </Button>
            <Button
              disabled={!form.name || !form.url || saveMutation.isPending}
              onClick={() => saveMutation.mutate(form)}
            >
              {saveMutation.isPending && (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              )}
              Save
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
