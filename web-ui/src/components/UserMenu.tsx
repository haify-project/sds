import { useQuery } from '@tanstack/react-query';
import { ShieldCheck, LogOut, User as UserIcon } from 'lucide-react';
import { api, setApiToken } from '@/services/api';
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Separator } from '@/components/ui/separator';
import { ThemeControls } from '@/components/ThemeControls';

/**
 * Top-right avatar menu: who you are signed in as (when RBAC is on), theme
 * settings, and sign out — consolidated into a single popover under the avatar.
 */
export function UserMenu() {
  const { data: whoami } = useQuery({
    queryKey: ['rbac', 'whoami'],
    queryFn: () => api.getRbacWhoami(),
    retry: false,
  });

  const signedIn = !!(whoami?.enabled && whoami.user);
  const initial = signedIn ? whoami!.user!.charAt(0).toUpperCase() : null;

  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          size="icon"
          className="h-8 w-8 rounded-full p-0"
          aria-label="Account and settings"
        >
          {initial ? (
            <span className="text-xs font-semibold">{initial}</span>
          ) : (
            <UserIcon className="h-4 w-4" />
          )}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-72">
        {signedIn && (
          <>
            <div className="flex items-center gap-3">
              <span className="flex h-10 w-10 items-center justify-center rounded-full bg-primary/10 text-sm font-semibold text-primary ring-1 ring-primary/20">
                {initial}
              </span>
              <div className="min-w-0">
                <p className="truncate text-sm font-semibold">{whoami!.user}</p>
                <div className="mt-0.5 flex flex-wrap items-center gap-1">
                  <Badge variant="secondary" className="text-[0.65rem] capitalize">
                    {whoami!.role}
                  </Badge>
                  {whoami!.can_admin && (
                    <Badge className="gap-1 text-[0.65rem]">
                      <ShieldCheck className="h-2.5 w-2.5" />
                      admin
                    </Badge>
                  )}
                </div>
              </div>
            </div>
            <Separator className="my-3" />
          </>
        )}

        <ThemeControls />

        <Separator className="my-3" />
        <Button
          variant="ghost"
          className="w-full justify-start text-muted-foreground hover:text-foreground"
          onClick={() => {
            setApiToken('');
            window.location.reload();
          }}
        >
          <LogOut className="h-4 w-4" />
          Sign out
        </Button>
      </PopoverContent>
    </Popover>
  );
}
