import { useState } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Loader2 } from 'lucide-react';
import { api } from '@/services/api';
import { toast } from 'sonner';
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

interface RegisterNodeDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export function RegisterNodeDialog({ open, onOpenChange }: RegisterNodeDialogProps) {
  const queryClient = useQueryClient();
  const [name, setName] = useState('');
  const [address, setAddress] = useState('');

  const registerMutation = useMutation({
    mutationFn: (data: { name: string; address: string }) => api.registerNode(data),
    onSuccess: () => {
      toast.success('Node registered');
      queryClient.invalidateQueries({ queryKey: ['nodes'] });
      setName('');
      setAddress('');
      onOpenChange(false);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    registerMutation.mutate({ name: name.trim(), address: address.trim() });
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>Register Node</DialogTitle>
            <DialogDescription>
              Add a storage node to the controller inventory.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label htmlFor="node-name">Node Name</Label>
              <Input
                id="node-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="e.g., node1"
                required
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="node-address">Node Address</Label>
              <Input
                id="node-address"
                value={address}
                onChange={(e) => setAddress(e.target.value)}
                placeholder="e.g., 192.168.1.100 or hostname"
                required
              />
            </div>
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={registerMutation.isPending}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={registerMutation.isPending}>
              {registerMutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
              Register
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
