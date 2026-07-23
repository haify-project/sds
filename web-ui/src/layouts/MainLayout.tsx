import { useEffect, useState } from 'react';
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
  Menu,
  Layers3,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { UserMenu } from '@/components/UserMenu';
import { AICopilot } from '@/components/AICopilot';

const navigation = [
  { name: 'Dashboard', href: '/dashboard', icon: LayoutDashboard },
  { name: 'Nodes', href: '/nodes', icon: Server },
  { name: 'Pools', href: '/pools', icon: Database },
  { name: 'Resources', href: '/resources', icon: Box },
  { name: 'Profiles', href: '/resource-profiles', icon: Layers3 },
  { name: 'Gateways', href: '/gateways', icon: Network },
  { name: 'HA', href: '/ha', icon: ShieldCheck },
  { name: 'Access', href: '/access', icon: Lock },
];

// SidebarContent is the shared nav body, rendered both in the static desktop
// sidebar and inside the mobile slide-in drawer. onNavigate lets the mobile
// drawer close itself when a link is tapped.
function SidebarContent({
  pathname,
  onNavigate,
}: {
  pathname: string;
  onNavigate?: () => void;
}) {
  return (
    <>
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
            pathname === item.href || pathname.startsWith(item.href + '/');
          return (
            <Link
              key={item.name}
              to={item.href}
              onClick={onNavigate}
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
    </>
  );
}

export function MainLayout() {
  const location = useLocation();
  const isFetching = useIsFetching();
  const [aiOpen, setAiOpen] = useState(false);
  const [navOpen, setNavOpen] = useState(false);

  // Close the mobile drawer whenever the route changes (a nav tap navigates).
  useEffect(() => {
    setNavOpen(false);
  }, [location.pathname]);

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
      {/* Desktop sidebar — hidden on phones (< md). */}
      <aside className="hidden w-60 flex-col border-r border-sidebar-border bg-sidebar text-sidebar-foreground md:flex">
        <SidebarContent pathname={location.pathname} />
      </aside>

      {/* Mobile drawer — slides in over the content when the hamburger opens it. */}
      {navOpen && (
        <div className="fixed inset-0 z-50 md:hidden">
          <button
            type="button"
            aria-label="Close menu"
            onClick={() => setNavOpen(false)}
            className="absolute inset-0 bg-black/50"
          />
          <aside className="absolute left-0 top-0 flex h-full w-64 max-w-[80vw] flex-col border-r border-sidebar-border bg-sidebar text-sidebar-foreground shadow-xl">
            <SidebarContent
              pathname={location.pathname}
              onNavigate={() => setNavOpen(false)}
            />
          </aside>
        </div>
      )}

      {/* Main content */}
      <div className="flex flex-1 flex-col overflow-hidden">
        <header className="flex h-14 items-center justify-between border-b border-border px-4 sm:px-6">
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={() => setNavOpen(true)}
              aria-label="Open menu"
              className="-ml-1 rounded-md p-1.5 text-muted-foreground hover:bg-sidebar-accent hover:text-foreground md:hidden"
            >
              <Menu className="h-5 w-5" />
            </button>
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
        <main className="app-canvas flex-1 overflow-auto p-4 sm:p-6 lg:p-8">
          <Outlet />
        </main>
      </div>

      <AICopilot open={aiOpen} onClose={() => setAiOpen(false)} />
    </div>
  );
}
