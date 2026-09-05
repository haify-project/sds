import { lazy, Suspense, useEffect, useRef, useState } from 'react';
import { Outlet, Link, useLocation } from 'react-router';
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
  PanelLeft,
  ScrollText,
  BellRing,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { UserMenu } from '@/components/UserMenu';
import { NotificationBell } from '@/components/NotificationBell';

// Collapsing the sidebar is a per-workstation preference, so it is remembered
// across reloads. Storage can throw (private mode, disabled cookies); the
// preference is not worth failing a render over.
const COLLAPSE_KEY = 'sds.sidebar.collapsed';

function readCollapsed(): boolean {
  try {
    return localStorage.getItem(COLLAPSE_KEY) === '1';
  } catch {
    return false;
  }
}

function writeCollapsed(value: boolean) {
  try {
    localStorage.setItem(COLLAPSE_KEY, value ? '1' : '0');
  } catch {
    /* preference simply won't persist */
  }
}

// The Copilot pulls in the whole Markdown/streaming rendering stack — by far
// the heaviest thing in the bundle, and most sessions never open it. Load it
// on first use instead of making every page wait for it.
const AICopilot = lazy(() =>
  import('@/components/AICopilot').then((m) => ({ default: m.AICopilot })),
);

// PageFallback stands in while a lazily-loaded route chunk arrives. It fills
// the content area only: the sidebar and header are already rendered and must
// not flicker on navigation.
function PageFallback() {
  return (
    <div
      className="flex h-full min-h-64 items-center justify-center"
      role="status"
      aria-live="polite"
    >
      <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
      <span className="sr-only">Loading</span>
    </div>
  );
}

type NavItem = {
  name: string;
  href: string;
  icon: typeof LayoutDashboard;
  /** Count pill on the right of the row. Omitted means no pill at all. */
  badge?: number;
};

// Two groups, because the nine destinations answer two different questions:
// "what is the cluster made of" and "what is it doing / who may touch it".
// Nine flat rows made the operator read the whole list every time.
const navGroups: { label: string; items: NavItem[] }[] = [
  {
    label: 'Cluster',
    items: [
      { name: 'Dashboard', href: '/dashboard', icon: LayoutDashboard },
      { name: 'Nodes', href: '/nodes', icon: Server },
      { name: 'Pools', href: '/pools', icon: Database },
      { name: 'Resources', href: '/resources', icon: Box },
      { name: 'Gateways', href: '/gateways', icon: Network },
      { name: 'HA', href: '/ha', icon: ShieldCheck },
    ],
  },
  {
    label: 'Operations',
    items: [
      { name: 'Notifications', href: '/notifications', icon: BellRing },
      { name: 'Access', href: '/access', icon: Lock },
      { name: 'Logs', href: '/logs', icon: ScrollText },
    ],
  },
];

// Flat view of the same data, for the current-section lookup below.
const navItems = navGroups.flatMap((g) => g.items);

// SidebarContent is the shared nav body, rendered both in the static desktop
// sidebar and inside the mobile slide-in drawer. onNavigate lets the mobile
// drawer close itself when a link is tapped.
//
// `collapsed` is desktop-only: the mobile drawer always renders full width, so
// it never passes it.
function SidebarContent({
  pathname,
  onNavigate,
  collapsed = false,
  aiOpen,
  onToggleCopilot,
}: {
  pathname: string;
  onNavigate?: () => void;
  collapsed?: boolean;
  aiOpen: boolean;
  onToggleCopilot: () => void;
}) {
  return (
    <div
      className={cn(
        'flex min-h-0 flex-1 flex-col py-5',
        collapsed ? 'px-2' : 'px-3',
      )}
    >
      <div
        className={cn(
          'flex items-center pb-5',
          collapsed ? 'justify-center' : 'gap-2.5 px-2.5',
        )}
      >
        <HardDrive className="size-5 shrink-0 text-primary" />
        {!collapsed && (
          <div className="leading-tight">
            <h1 className="text-[13.5px] font-semibold tracking-tight">
              SDS Controller
            </h1>
            <p className="text-[11px] text-muted-foreground">
              Software Defined Storage
            </p>
          </div>
        )}
      </div>

      <nav className="flex-1 space-y-5 overflow-y-auto">
        {navGroups.map((group) => (
          <div key={group.label}>
            {/* The label is the point of the grouping; collapsed there is no
                room for it and the gap between blocks carries it instead. */}
            {!collapsed && (
              <div className="eyebrow px-2.5 pb-2">{group.label}</div>
            )}
            <div className="flex flex-col gap-0.5">
              {group.items.map((item) => {
                const isActive =
                  pathname === item.href || pathname.startsWith(item.href + '/');
                return (
                  <Link
                    key={item.name}
                    to={item.href}
                    onClick={onNavigate}
                    // Collapsed leaves only an icon, so the name has to survive
                    // for screen readers and as a hover hint.
                    title={collapsed ? item.name : undefined}
                    aria-label={collapsed ? item.name : undefined}
                    className={cn(
                      'flex h-[34px] items-center gap-2.5 rounded-[7px] text-[13.5px] transition-colors',
                      collapsed ? 'justify-center px-0' : 'px-2.5',
                      isActive
                        ? // A white pill lifted off the paper — no left accent
                          // bar, the elevation alone is the selection.
                          'bg-sidebar-accent font-semibold text-foreground shadow-[0_1px_2px_oklch(0.3_0.02_260/0.06),0_0_0_1px_var(--sidebar-border)]'
                        : 'text-muted-foreground hover:bg-sidebar-accent/60 hover:text-foreground',
                    )}
                  >
                    <item.icon
                      className={cn(
                        'size-[17px] shrink-0',
                        isActive && 'text-primary',
                      )}
                    />
                    {!collapsed && item.name}
                    {!collapsed && item.badge !== undefined && (
                      <span className="ml-auto rounded-full bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
                        {item.badge}
                      </span>
                    )}
                  </Link>
                );
              })}
            </div>
          </div>
        ))}
      </nav>

      <div className="mt-auto flex flex-col gap-2.5 pt-4">
        {collapsed ? (
          <button
            type="button"
            onClick={onToggleCopilot}
            aria-pressed={aiOpen}
            aria-label="SDS Copilot"
            title="SDS Copilot"
            className={cn(
              'flex h-9 items-center justify-center rounded-[7px] transition-colors',
              aiOpen
                ? 'bg-accent text-accent-foreground'
                : 'text-muted-foreground hover:bg-sidebar-accent hover:text-foreground',
            )}
          >
            <Sparkles className="size-[17px]" />
          </button>
        ) : (
          <button
            type="button"
            onClick={onToggleCopilot}
            aria-pressed={aiOpen}
            title="SDS Copilot"
            className={cn(
              'rounded-[9px] border bg-card p-3 text-left transition-colors',
              aiOpen ? 'border-primary/50' : 'border-border hover:border-ring/40',
            )}
          >
            <span className="flex items-center gap-2 text-[13px] font-semibold">
              <Sparkles className="size-4 shrink-0 text-primary" />
              Copilot
            </span>
            <span className="mt-1.5 block text-[12px] leading-snug text-muted-foreground">
              Ask about pools, quorum or a failover.
            </span>
          </button>
        )}
        <span
          className={cn(
            'font-mono text-[11px] text-muted-foreground',
            collapsed ? 'text-center' : 'px-2.5',
          )}
        >
          {collapsed ? 'v1.8' : 'v1.8.1'}
        </span>
      </div>
    </div>
  );
}

export function MainLayout() {
  const location = useLocation();
  const isFetching = useIsFetching();
  const [aiOpen, setAiOpen] = useState(false);
  const [navOpen, setNavOpen] = useState(false);
  const [collapsed, setCollapsed] = useState(readCollapsed);

  function toggleCollapsed() {
    setCollapsed((v) => {
      writeCollapsed(!v);
      return !v;
    });
  }

  // Nothing of the Copilot exists until the user asks for it. Once it has been
  // opened it stays mounted even while closed, so its conversation survives
  // toggling the panel shut — the chunk is already downloaded by then, so this
  // costs nothing.
  const aiRequested = useRef(false);
  if (aiOpen) {
    aiRequested.current = true;
  }

  // Close the mobile drawer whenever the route changes (a nav tap navigates).
  useEffect(() => {
    setNavOpen(false);
  }, [location.pathname]);

  // Match the deepest nav item whose path prefixes the current location, so
  // sub-routes (e.g. /ha/create) still show their section title ("HA") and
  // highlight the right nav item instead of falling back to the default.
  const current = [...navItems]
    .sort((a, b) => b.href.length - a.href.length)
    .find(
      (n) =>
        location.pathname === n.href ||
        location.pathname.startsWith(n.href + '/'),
    );

  return (
    <div className="flex h-screen bg-background">
      {/* Desktop sidebar — hidden on phones (< md), collapsible to icons. */}
      <aside
        className={cn(
          'hidden flex-col border-r border-sidebar-border bg-sidebar text-sidebar-foreground transition-[width] duration-200 md:flex',
          collapsed ? 'w-16' : 'w-[244px]',
        )}
      >
        <SidebarContent
          pathname={location.pathname}
          collapsed={collapsed}
          aiOpen={aiOpen}
          onToggleCopilot={() => setAiOpen((v) => !v)}
        />
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
              aiOpen={aiOpen}
              // The Copilot panel would open behind the drawer otherwise.
              onToggleCopilot={() => {
                setNavOpen(false);
                setAiOpen((v) => !v);
              }}
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
            {/* Desktop counterpart of the hamburger: collapses the sidebar to
                icons rather than opening a drawer. */}
            <button
              type="button"
              onClick={toggleCollapsed}
              aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
              aria-pressed={collapsed}
              title={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
              className="-ml-1 hidden rounded-md p-1.5 text-muted-foreground hover:bg-sidebar-accent hover:text-foreground md:block"
            >
              <PanelLeft className="h-5 w-5" />
            </button>
            <h2 className="text-[13.5px] font-semibold tracking-tight">
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
                  ? 'bg-accent text-accent-foreground'
                  : 'text-muted-foreground hover:bg-sidebar-accent hover:text-foreground',
              )}
            >
              <Sparkles className="h-4 w-4" />
              Copilot
            </button>
            <NotificationBell />
            <UserMenu />
          </div>
        </header>
        <main className="app-canvas flex-1 overflow-auto p-6 lg:px-8 lg:py-6">
          <Suspense fallback={<PageFallback />}>
            <Outlet />
          </Suspense>
        </main>
      </div>

      {aiRequested.current && (
        <Suspense fallback={null}>
          <AICopilot open={aiOpen} onClose={() => setAiOpen(false)} />
        </Suspense>
      )}
    </div>
  );
}
