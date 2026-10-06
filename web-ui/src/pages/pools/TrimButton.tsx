import { useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '../../services/api';
import { Button } from '@/components/ui/button';
import { toast } from 'sonner';
import { Loader2, Scissors } from 'lucide-react';

// Trims every mounted DRBD filesystem now, on the node serving it. The
// controller also does this daily, so this is for "the pool is fuller than
// the data on it".
export function TrimButton() {
  const queryClient = useQueryClient();
  const trim = useMutation({
    mutationFn: () => api.trimPools(),
    onSuccess: (res) => {
      const failed = (res.results ?? []).filter((r) => r.error);
      if (failed.length > 0) {
        toast.warning(res.message, {
          description: failed.map((r) => `${r.node} ${r.mount}: ${r.error}`).join('\n'),
        });
      } else {
        toast.success(res.message);
      }
      queryClient.invalidateQueries({ queryKey: ['pools'] });
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <Button
      variant="outline"
      onClick={() => trim.mutate()}
      disabled={trim.isPending}
      title="Give the thin pools back the space filesystems have freed"
    >
      {trim.isPending ? <Loader2 className="h-4 w-4 animate-spin" /> : <Scissors className="h-4 w-4" />}
      {trim.isPending ? 'Trimming…' : 'Trim now'}
    </Button>
  );
}
