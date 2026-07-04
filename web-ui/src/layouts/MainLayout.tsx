import { useState } from 'react';
import { Outlet, Link, useLocation } from 'react-router-dom';
import { useIsFetching } from '@tanstack/react-query';
import {
  LayoutDashboard,
  Server,
  Database,
  Box,
  Network,
  ShieldCheck,
  HardDrive,
  Lock,
  Loader2,
  Sparkles,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { UserMenu } from '@/components/UserMenu';
import { AICopilot } from '@/components/AICopilot';

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
  const isFetching = useIsFetching();
  const [aiOpen, setAiOpen] = useState(false);

  // Match the deepest nav item whose path prefixes the current location, so
  // sub-routes (e.g. /ha/create) still show their section title ("HA") and
  // highlight the right nav item instead of falling back to the default.
  const current = [...navigation]
    .sort((a, b) => b.href.length - a.href.length)
    .find(
      (n) =>
        location.pathname === n.href ||
        location.pathname.startsWith(n.href + '/'),
    );

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
            const isActive =
              location.pathname === item.href ||
              location.pathname.startsWith(item.href + '/');
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
        <div className="border-t border-sidebar-border px-4 py-3">
          <span className="font-mono text-[0.7rem] text-muted-foreground">
            v1.8.1
          </span>
        </div>
      </aside>

      {/* Main content */}
      <div className="flex flex-1 flex-col overflow-hidden">
        <header className="flex h-14 items-center justify-between border-b border-border px-6">
          <div className="flex items-center gap-2">
            <h2 className="text-sm font-semibold tracking-tight">
              {current?.name ?? 'Dashboard'}
            </h2>
            {isFetching > 0 && (
              <span
                className="flex items-center gap-1 text-xs text-muted-foreground"
                role="status"
                aria-live="polite"
              >
                <Loader2 className="h-3 w-3 animate-spin" />
                Refreshing
              </span>
            )}
          </div>
          <div className="flex items-center gap-1">
            <button
              type="button"
              onClick={() => setAiOpen((v) => !v)}
              aria-pressed={aiOpen}
              title="SDS Copilot"
              className={cn(
                'flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-sm transition-colors',
                aiOpen
                  ? 'bg-primary text-primary-foreground'
                  : 'text-muted-foreground hover:bg-sidebar-accent hover:text-foreground',
              )}
            >
              <Sparkles className="h-4 w-4" />
              Copilot
            </button>
            <UserMenu />
          </div>
        </header>
        <main className="app-canvas flex-1 overflow-auto p-6 lg:p-8">
          <Outlet />
        </main>
      </div>

      <AICopilot open={aiOpen} onClose={() => setAiOpen(false)} />
    </div>
  );
}
