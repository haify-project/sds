import { useCallback, useEffect, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { getApiToken, setApiToken, setAuthPromptHandler } from '@/services/api';

/**
 * Global API-token dialog. Registers itself as the api client's auth prompt
 * handler: whenever a request gets a 401/403 the dialog opens, and the
 * pending request resumes after the user saves a token (or gives up).
 */
export function AuthTokenDialog() {
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [token, setToken] = useState('');
  const resolverRef = useRef<((retry: boolean) => void) | null>(null);

  useEffect(() => {
    setAuthPromptHandler(
      () =>
        new Promise<boolean>((resolve) => {
          // Only one prompt at a time; concurrent 401s share the outcome.
          if (resolverRef.current) {
            resolve(false);
            return;
          }
          resolverRef.current = resolve;
          setToken(getApiToken());
          setOpen(true);
        })
    );
    return () => setAuthPromptHandler(null);
  }, []);

  const finish = useCallback((retry: boolean) => {
    setOpen(false);
    resolverRef.current?.(retry);
    resolverRef.current = null;
  }, []);

  const save = useCallback(() => {
    setApiToken(token.trim());
    // Refetch everything: queries that failed before a token was set (returning
    // empty data, e.g. empty resource dropdowns) must reload with the new token.
    queryClient.invalidateQueries();
    finish(true);
  }, [token, finish, queryClient]);

  return (
    <Dialog open={open} onOpenChange={(o) => !o && finish(false)}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>API token required</DialogTitle>
          <DialogDescription>
            The Haify controller has authentication enabled. Enter the API
            token from <code className="font-mono">/etc/sds/token</code> on a
            cluster node.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-2 py-2">
          <Label htmlFor="api-token">Token</Label>
          <Input
            id="api-token"
            type="password"
            value={token}
            placeholder="Bearer token"
            onChange={(e) => setToken(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && save()}
            autoFocus
          />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => finish(false)}>
            Cancel
          </Button>
          <Button onClick={save} disabled={!token.trim()}>
            Save & retry
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
