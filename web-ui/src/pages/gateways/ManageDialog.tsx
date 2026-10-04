import { useMutation, useQueryClient } from '@tanstack/react-query';
import { api, Gateway } from '@/services/api';
import { toast } from 'sonner';
import { Loader2, AlertCircle } from 'lucide-react';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { PROTOCOL, gwKind } from './protocol';
import { ManageNFS } from './ManageNFS';
import { ManageISCSI } from './ManageISCSI';
import { ManageNVMe } from './ManageNVMe';
import { ManageSMB } from './ManageSMB';

// ==================== Manage Dialog ====================

export function ManageDialog({
  gateway,
  onOpenChange,
}: {
  gateway: Gateway | null;
  onOpenChange: (open: boolean) => void;
}) {
  const queryClient = useQueryClient();
  const restartMutation = useMutation({
    mutationFn: async (id: string) => {
      await api.stopGateway(id);
      await api.startGateway(id);
    },
    onSuccess: () => {
      toast.success('Gateway restarting; pending changes will apply');
      queryClient.invalidateQueries({ queryKey: ['gateways'] });
      onOpenChange(false);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const kind = gateway ? gwKind(gateway.type) : null;

  return (
    <Dialog open={!!gateway} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>
            Manage {kind ? PROTOCOL[kind].label : ''} gateway —{' '}
            <span className="font-mono">{gateway?.name || gateway?.id}</span>
          </DialogTitle>
          <DialogDescription>
            Resource:{' '}
            <span className="font-mono text-foreground">{gateway?.resource}</span>
          </DialogDescription>
        </DialogHeader>

        {/* SMB share and user changes apply live; the others wait for a restart. */}
        {kind !== 'smb' && (
        <div className="flex items-start justify-between gap-3 rounded-lg border border-border bg-muted/60 p-3 text-sm">
          <div className="flex items-start gap-2">
            <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
            <span>
              Changes are persisted to the gateway config and take effect on the
              next restart or failover — the running target is not modified live.
            </span>
          </div>
          <Button
            size="sm"
            variant="outline"
            className="shrink-0"
            disabled={restartMutation.isPending}
            onClick={() => gateway && restartMutation.mutate(gateway.id)}
          >
            {restartMutation.isPending && (
              <Loader2 className="h-3 w-3 animate-spin" />
            )}
            Restart now
          </Button>
        </div>
        )}

        {kind === 'nfs' && <ManageNFS resource={gateway!.resource} />}
        {kind === 'iscsi' && <ManageISCSI resource={gateway!.resource} />}
        {kind === 'nvme' && <ManageNVMe resource={gateway!.resource} />}
        {kind === 'smb' && <ManageSMB resource={gateway!.resource} />}
      </DialogContent>
    </Dialog>
  );
}
