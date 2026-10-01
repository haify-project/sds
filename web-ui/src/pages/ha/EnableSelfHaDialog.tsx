import { useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { api } from '@/services/api';
import { toast } from 'sonner';
import { Loader2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { isRestartError } from './selfHa';

export function EnableSelfHaDialog({
  open,
  onOpenChange,
  onEnabled,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onEnabled: () => void;
}) {
  const [vip, setVip] = useState('');
  const [pool, setPool] = useState('');
  const [sizeGb, setSizeGb] = useState('1');
  const [port, setPort] = useState('7999');

  const mutation = useMutation({
    mutationFn: () =>
      api.enableSelfHa({
        vip,
        pool,
        sizeGb: parseInt(sizeGb, 10),
        port: parseInt(port, 10),
      }),
    onSuccess: () => {
      toast.info(
        'Controller is restarting under drbd-reactor management. Requests may fail briefly while the VIP comes up.',
        { duration: Infinity, closeButton: true }
      );
      onEnabled();
    },
    onError: (e: Error) => {
      if (isRestartError(e.message)) {
        toast.info('Controller is restarting; refresh shortly');
      } else {
        toast.error(e.message);
      }
    },
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Enable Controller Self-HA</DialogTitle>
          <DialogDescription>
            Run the management plane on its own DRBD resource with a floating
            VIP and drbd-reactor failover.
          </DialogDescription>
        </DialogHeader>
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            mutation.mutate();
          }}
        >
          <div className="space-y-1.5">
            <Label>Virtual IP (CIDR)</Label>
            <Input
              value={vip}
              onChange={(e) => setVip(e.target.value)}
              placeholder="192.168.1.250/24"
              required
            />
          </div>
          <div className="space-y-1.5">
            <Label>Pool</Label>
            <Input
              value={pool}
              onChange={(e) => setPool(e.target.value)}
              placeholder="vg0"
              required
            />
          </div>
          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-1.5">
              <Label>Size (GB)</Label>
              <Input
                type="number"
                value={sizeGb}
                onChange={(e) => setSizeGb(e.target.value)}
                min={1}
                required
              />
            </div>
            <div className="space-y-1.5">
              <Label>DRBD Port</Label>
              <Input
                type="number"
                value={port}
                onChange={(e) => setPort(e.target.value)}
                required
              />
            </div>
          </div>
          <DialogFooter>
            <Button
              type="submit"
              disabled={mutation.isPending || !vip || !pool}
            >
              {mutation.isPending && (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              )}
              Enable Self-HA
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
