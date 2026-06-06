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
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import { setApiToken } from '@/services/api';

const navigation = [
  { name: 'Dashboard', href: '/dashboard', icon: LayoutDashboard },
  { name: 'Nodes', href: '/nodes', icon: Server },
  { name: 'Pools', href: '/pools', icon: Database },
  { name: 'Resources', href: '/resources', icon: Box },
  { name: 'Gateways', href: '/gateways', icon: Network },
  { name: 'HA', href: '/ha', icon: ShieldCheck },
];

export function MainLayout() {
  const location = useLocation();

  return (
    <div className="flex h-screen bg-background">
      {/* Sidebar */}
      <aside className="w-64 bg-sidebar text-sidebar-foreground flex flex-col border-r border-sidebar-border">
        <div className="p-6 border-b border-sidebar-border">
          <div className="flex items-center gap-3">
            <HardDrive className="h-8 w-8 text-sidebar-primary" />
            <div>
              <h1 className="text-xl font-bold">SDS Controller</h1>
              <p className="text-xs text-sidebar-foreground/60">Software Defined Storage</p>
            </div>
          </div>
        </div>
        <nav className="flex-1 p-4 space-y-1">
          {navigation.map((item) => {
            const isActive = location.pathname === item.href;
            return (
              <Link
                key={item.name}
                to={item.href}
                className={cn(
                  'flex items-center gap-3 px-3 py-2 rounded-md text-sm font-medium transition-colors',
                  isActive
                    ? 'bg-sidebar-primary text-sidebar-primary-foreground'
                    : 'text-sidebar-foreground/70 hover:bg-sidebar-accent hover:text-sidebar-accent-foreground'
                )}
              >
                <item.icon className="h-4 w-4" />
                {item.name}
              </Link>
            );
          })}
        </nav>
        <div className="p-4 border-t border-sidebar-border flex items-center justify-between">
          <span className="text-xs text-sidebar-foreground/50">v1.4.0</span>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7 text-sidebar-foreground/50 hover:text-sidebar-foreground hover:bg-sidebar-accent"
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
      <div className="flex-1 flex flex-col overflow-hidden">
        <header className="bg-card border-b px-6 py-4">
          <h2 className="text-lg font-semibold">
            {navigation.find((n) => n.href === location.pathname)?.name ?? 'Dashboard'}
          </h2>
        </header>
        <main className="flex-1 overflow-auto p-6">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
