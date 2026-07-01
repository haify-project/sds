import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { Lock, Plus, Trash2, Loader2, Copy } from 'lucide-react';
import { api } from '@/services/api';
import { copyToClipboard } from '@/lib/utils';
import {
  Card,
  CardContent,
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
  DialogTrigger,
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
import { toast } from 'sonner';

const FALLBACK_ROLES = ['admin', 'operator', 'viewer'];

export function AccessPage() {
  const { data: whoami, isLoading: whoamiLoading } = useQuery({
    queryKey: ['rbac', 'whoami'],
    queryFn: () => api.getRbacWhoami(),
  });

  const canAdmin = !!whoami?.can_admin;

  const { data: policies, isLoading: policiesLoading } = useQuery({
    queryKey: ['rbac', 'policies'],
    queryFn: () => api.getRbacPolicies(),
    enabled: canAdmin,
  });

  const { data: rolesData } = useQuery({
    queryKey: ['rbac', 'roles'],
    queryFn: () => api.getRbacRoles(),
    enabled: canAdmin,
  });
  const roles = rolesData?.roles?.length ? rolesData.roles : FALLBACK_ROLES;

  if (whoamiLoading) {
    return (
      <div className="space-y-6">
        <Skeleton className="h-28 w-full max-w-md" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (!whoami?.enabled) {
    return (
      <Card className="max-w-xl">
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <Lock className="h-4 w-4 text-muted-foreground" />
            Access Control
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            RBAC is not enabled on this controller. Access is governed by a
            single shared API token. Enable{' '}
            <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">
              [rbac]
            </code>{' '}
            in <span className="font-mono">controller.toml</span> for per-user
            roles.
          </p>
        </CardContent>
      </Card>
    );
  }

  if (!canAdmin) {
    return (
      <Card className="max-w-xl">
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <Lock className="h-4 w-4 text-muted-foreground" />
            Access Control
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            You are signed in as{' '}
            <span className="font-medium text-foreground">{whoami.user}</span>{' '}
            (<span className="capitalize">{whoami.role}</span>). User and role
            management is available to administrators only.
          </p>
        </CardContent>
      </Card>
    );
  }

  return (
    <div className="space-y-6">
      {
        <>
          {/* Users */}
          <Card>
            <CardHeader className="flex flex-row items-center justify-between">
              <CardTitle className="text-base">Users</CardTitle>
              <AddUserDialog roles={roles} />
            </CardHeader>
            <CardContent>
              {policiesLoading ? (
                <Skeleton className="h-24 w-full" />
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Name</TableHead>
                      <TableHead>Role</TableHead>
                      <TableHead className="w-10" />
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {(policies?.users ?? []).map((u) => (
                      <UserRow
                        key={u.name}
                        name={u.name}
                        role={u.role}
                        pinned={!!u.pinned}
                        roles={roles}
                      />
                    ))}
                  </TableBody>
                </Table>
              )}
            </CardContent>
          </Card>

          {/* Effective policy */}
          <Card>
            <CardHeader>
              <CardTitle className="text-base">Effective Policy</CardTitle>
            </CardHeader>
            <CardContent>
              {policiesLoading ? (
                <Skeleton className="h-40 w-full" />
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Role</TableHead>
                      <TableHead>Object</TableHead>
                      <TableHead>Action</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {(policies?.policies ?? []).map((p, i) => (
                      <TableRow key={`${p.role}-${p.object}-${p.action}-${i}`}>
                        <TableCell className="font-medium capitalize">
                          {p.role}
                        </TableCell>
                        <TableCell className="font-mono text-xs">
                          {p.object}
                        </TableCell>
                        <TableCell className="font-mono text-xs">
                          {p.action}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}
            </CardContent>
          </Card>
        </>
      }
    </div>
  );
}

function UserRow({
  name,
  role,
  pinned,
  roles,
}: {
  name: string;
  role: string;
  pinned: boolean;
  roles: string[];
}) {
  const queryClient = useQueryClient();

  const setRole = useMutation({
    mutationFn: (newRole: string) => api.setRbacUserRole(name, newRole),
    onSuccess: (_d, newRole) => {
      toast.success(`${name} is now ${newRole}`);
      queryClient.invalidateQueries({ queryKey: ['rbac'] });
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const remove = useMutation({
    mutationFn: () => api.deleteRbacUser(name),
    onSuccess: () => {
      toast.success(`User "${name}" removed`);
      queryClient.invalidateQueries({ queryKey: ['rbac'] });
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <TableRow>
      <TableCell className="font-medium">
        {name}
        {pinned && (
          <Badge variant="outline" className="ml-2 text-[0.65rem]">
            config
          </Badge>
        )}
      </TableCell>
      <TableCell>
        <Select
          value={role}
          onValueChange={(v) => setRole.mutate(v)}
          disabled={setRole.isPending}
        >
          <SelectTrigger className="h-8 w-36 capitalize">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {roles.map((r) => (
              <SelectItem key={r} value={r} className="capitalize">
                {r}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </TableCell>
      <TableCell>
        {!pinned && (
          <AlertDialog>
            <AlertDialogTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7 text-muted-foreground hover:text-destructive"
                title="Remove user"
              >
                <Trash2 className="h-4 w-4" />
              </Button>
            </AlertDialogTrigger>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>Remove user "{name}"?</AlertDialogTitle>
                <AlertDialogDescription>
                  Their token will stop working immediately. This cannot be
                  undone.
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>Cancel</AlertDialogCancel>
                <AlertDialogAction
                  onClick={() => remove.mutate()}
                  className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
                >
                  Remove
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        )}
      </TableCell>
    </TableRow>
  );
}

function AddUserDialog({ roles }: { roles: string[] }) {
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState('');
  const [role, setRole] = useState(roles[0] ?? 'viewer');
  const [token, setToken] = useState('');
  const [createdToken, setCreatedToken] = useState<string | null>(null);

  const create = useMutation({
    mutationFn: () =>
      api.createRbacUser({ name, role, token: token || undefined }),
    onSuccess: (res) => {
      queryClient.invalidateQueries({ queryKey: ['rbac'] });
      setOpen(false);
      setName('');
      setToken('');
      setCreatedToken(res.token);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogTrigger asChild>
          <Button size="sm">
            <Plus className="h-4 w-4" />
            Add User
          </Button>
        </DialogTrigger>
        <DialogContent>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              create.mutate();
            }}
          >
            <DialogHeader>
              <DialogTitle>Add User</DialogTitle>
              <DialogDescription>
                Create a user and assign a role. A secure token is generated
                unless you provide one.
              </DialogDescription>
            </DialogHeader>

            <div className="space-y-4 py-4">
              <div className="space-y-2">
                <Label htmlFor="rbac-name">Name</Label>
                <Input
                  id="rbac-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="e.g., alice"
                  required
                />
              </div>
              <div className="space-y-2">
                <Label>Role</Label>
                <Select value={role} onValueChange={setRole}>
                  <SelectTrigger className="capitalize">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {roles.map((r) => (
                      <SelectItem key={r} value={r} className="capitalize">
                        {r}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-2">
                <Label htmlFor="rbac-token">Token (optional)</Label>
                <Input
                  id="rbac-token"
                  value={token}
                  onChange={(e) => setToken(e.target.value)}
                  placeholder="Leave blank to auto-generate"
                />
                <p className="text-xs text-muted-foreground">
                  If set, must be at least 16 characters.
                </p>
              </div>
            </div>

            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => setOpen(false)}
                disabled={create.isPending}
              >
                Cancel
              </Button>
              <Button type="submit" disabled={create.isPending || !name}>
                {create.isPending && (
                  <Loader2 className="h-4 w-4 animate-spin" />
                )}
                Create
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      {/* One-time token reveal */}
      <Dialog
        open={!!createdToken}
        onOpenChange={(o) => !o && setCreatedToken(null)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Token created</DialogTitle>
            <DialogDescription>
              Copy this token now — it is the only time it is shown. Give it to
              the user to authenticate.
            </DialogDescription>
          </DialogHeader>
          <div className="flex items-center gap-2">
            <code className="flex-1 overflow-x-auto rounded bg-muted px-3 py-2 font-mono text-xs">
              {createdToken}
            </code>
            <Button
              size="icon"
              variant="outline"
              className="h-9 w-9 shrink-0"
              onClick={async () => {
                if (createdToken) {
                  const ok = await copyToClipboard(createdToken);
                  if (ok) toast.success('Token copied');
                  else toast.error('Copy failed — select and copy manually');
                }
              }}
            >
              <Copy className="h-4 w-4" />
            </Button>
          </div>
          <DialogFooter>
            <Button onClick={() => setCreatedToken(null)}>Done</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
