import { Outlet, Link, useLocation } from 'react-router-dom';
import {
  LayoutDashboard,
  Server,
  Database,
  Box,
  Network,
  ShieldCheck,
  HardDrive,
  KeyRound,
  Lock,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import { ThemeCustomizer } from '@/components/ThemeCustomizer';
import { setApiToken } from '@/services/api';

const navigation = [
  { name: 'Dashboard', href: '/dashboard', icon: LayoutDashboard },
  { name: 'Nodes', href: '/nodes', icon: Server },
  { name: 'Pools', href: '/pools', icon: Database },
  { name: 'Resources', href: '/resources', icon: Box },
  { name: 'Gateways', href: '/gateways', icon: Network },
  { name: 'HA', href: '/ha', icon: ShieldCheck },
  { name: 'Access', href: '/access', icon: Lock },
];

export function MainLayout() {
  const location = useLocation();

  const current = navigation.find((n) => n.href === location.pathname);

  return (
    <div className="flex h-screen bg-background">
      {/* Sidebar */}
      <aside className="flex w-60 flex-col border-r border-sidebar-border bg-sidebar text-sidebar-foreground">
        <div className="flex items-center gap-2.5 px-5 py-5">
          <HardDrive className="h-5 w-5 text-primary" />
          <div className="leading-tight">
            <h1 className="text-sm font-semibold tracking-tight">SDS Controller</h1>
            <p className="text-[0.7rem] text-muted-foreground">
              Software Defined Storage
            </p>
          </div>
        </div>
        <nav className="flex-1 space-y-0.5 px-3 py-2">
          {navigation.map((item) => {
            const isActive = location.pathname === item.href;
            return (
              <Link
                key={item.name}
                to={item.href}
                className={cn(
                  'flex items-center gap-3 rounded-md px-3 py-2 text-sm transition-colors',
                  isActive
                    ? 'bg-sidebar-primary font-medium text-sidebar-primary-foreground'
                    : 'text-muted-foreground hover:bg-sidebar-accent hover:text-foreground'
                )}
              >
                <item.icon className="h-4 w-4" />
                {item.name}
              </Link>
            );
          })}
        </nav>
        <div className="flex items-center justify-between border-t border-sidebar-border px-4 py-3">
          <span className="font-mono text-[0.7rem] text-muted-foreground">
            v1.4.0
          </span>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7 text-muted-foreground hover:text-foreground"
                onClick={() => {
                  setApiToken('');
                  window.location.reload();
                }}
                aria-label="Clear API token"
              >
                <KeyRound className="h-4 w-4" />
              </Button>
            </TooltipTrigger>
            <TooltipContent side="right">Clear stored API token</TooltipContent>
          </Tooltip>
        </div>
      </aside>

      {/* Main content */}
      <div className="flex flex-1 flex-col overflow-hidden">
        <header className="flex h-14 items-center justify-between border-b border-border px-6">
          <h2 className="text-sm font-semibold tracking-tight">
            {current?.name ?? 'Dashboard'}
          </h2>
          <ThemeCustomizer />
        </header>
        <main className="app-canvas flex-1 overflow-auto p-6 lg:p-8">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
