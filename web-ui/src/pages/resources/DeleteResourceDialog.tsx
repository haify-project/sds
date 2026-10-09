import { useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '../../services/api';
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog';
import { toast } from 'sonner';
import { Loader2 } from 'lucide-react';

export function DeleteResourceDialog({
  open,
  onOpenChange,
  resourceName,
  deleteWarning,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  resourceName: string;
  /** Set when another system manages the resource: why not to delete it here. */
  deleteWarning?: string;
}) {
  const queryClient = useQueryClient();

  const deleteMutation = useMutation({
    mutationFn: () => api.deleteResource(resourceName),
    onSuccess: () => {
      toast.success(`Resource "${resourceName}" deleted`);
      queryClient.invalidateQueries({ queryKey: ['resources'] });
      onOpenChange(false);
    },
    onError: (e: Error) => toast.error(e.message),
  });

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete resource "{resourceName}"?</AlertDialogTitle>
          <AlertDialogDescription>
            This removes the DRBD resource and destroys all backing volumes and
            their data on every node. This action cannot be undone.
          </AlertDialogDescription>
          {deleteWarning && (
            <AlertDialogDescription className="mt-2 rounded border border-amber-500/40 bg-amber-500/10 p-2 text-amber-700 dark:text-amber-400">
              {deleteWarning}
            </AlertDialogDescription>
          )}
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            onClick={(e) => {
              e.preventDefault();
              deleteMutation.mutate();
            }}
            className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
          >
            {deleteMutation.isPending && (
              <Loader2 className="h-4 w-4 animate-spin" />
            )}
            Delete
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
