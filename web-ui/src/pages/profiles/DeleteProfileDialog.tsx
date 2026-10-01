import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Loader2, Trash2 } from 'lucide-react';
import { toast } from 'sonner';
import { api, ResourceProfile } from '@/services/api';
import { Button } from '@/components/ui/button';
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

export function DeleteProfileDialog({ profile, resourceCount }: { profile: ResourceProfile; resourceCount: number }) {
  const queryClient = useQueryClient();
  const remove = useMutation({
    mutationFn: () => api.deleteResourceProfile(profile.name),
    onSuccess: () => {
      toast.success(`Profile "${profile.name}" deleted`);
      queryClient.invalidateQueries({ queryKey: ['resource-profiles'] });
    },
    onError: (error: Error) => toast.error(error.message),
  });

  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>
        <Button variant="ghost" size="icon" title={`Delete ${profile.name}`}>
          <Trash2 className="h-4 w-4 text-destructive" />
        </Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete profile “{profile.name}”?</AlertDialogTitle>
          <AlertDialogDescription>
            New resources can no longer use this profile. The {resourceCount} existing resource{resourceCount === 1 ? '' : 's'} attributed to it keep their resolved configuration and continue operating normally.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction onClick={() => remove.mutate()} disabled={remove.isPending}>
            {remove.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
            Delete Profile
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
