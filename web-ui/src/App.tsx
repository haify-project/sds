import { lazy } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { TooltipProvider } from '@/components/ui/tooltip';
import { Toaster } from '@/components/ui/sonner';
import { AuthTokenDialog } from '@/components/AuthTokenDialog';
import { MainLayout } from './layouts/MainLayout';

// Pages are code-split so the first paint only downloads the shell and the one
// route being visited. Loading them eagerly meant every visitor paid for every
// page — including CreateHAPage's drag-and-drop stack — before seeing anything,
// which is a second on the LAN and far worse over a long-haul link.
const DashboardPage = lazy(() =>
  import('./pages/DashboardPage').then((m) => ({ default: m.DashboardPage })),
);
const NodesPage = lazy(() =>
  import('./pages/NodesPage').then((m) => ({ default: m.NodesPage })),
);
const PoolsPage = lazy(() =>
  import('./pages/PoolsPage').then((m) => ({ default: m.PoolsPage })),
);
const ResourcesPage = lazy(() =>
  import('./pages/ResourcesPage').then((m) => ({ default: m.ResourcesPage })),
);
const GatewaysPage = lazy(() =>
  import('./pages/GatewaysPage').then((m) => ({ default: m.GatewaysPage })),
);
const HAPage = lazy(() =>
  import('./pages/HAPage').then((m) => ({ default: m.HAPage })),
);
const CreateHAPage = lazy(() =>
  import('./pages/CreateHAPage').then((m) => ({ default: m.CreateHAPage })),
);
const AccessPage = lazy(() =>
  import('./pages/AccessPage').then((m) => ({ default: m.AccessPage })),
);
// The Suspense boundary lives in MainLayout, around the Outlet, so the sidebar
// and header stay put while a route's chunk arrives.

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: false,
      retry: 1,
      staleTime: 5000,
    },
  },
});

function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <TooltipProvider>
        <BrowserRouter>
          <Routes>
            <Route path="/" element={<MainLayout />}>
              <Route index element={<Navigate to="/dashboard" replace />} />
              <Route path="dashboard" element={<DashboardPage />} />
              <Route path="nodes" element={<NodesPage />} />
              <Route path="pools" element={<PoolsPage />} />
              <Route path="resources" element={<ResourcesPage />} />
              {/* Profiles moved under Resources; keep the old path working
                  rather than 404ing links and bookmarks that already exist. */}
              <Route
                path="resource-profiles"
                element={<Navigate to="/resources?tab=profiles" replace />}
              />
              <Route path="gateways" element={<GatewaysPage />} />
              <Route path="ha" element={<HAPage />} />
              <Route path="ha/create" element={<CreateHAPage />} />
              <Route path="access" element={<AccessPage />} />
            </Route>
          </Routes>
        </BrowserRouter>
        <AuthTokenDialog />
        <Toaster richColors position="top-right" />
      </TooltipProvider>
    </QueryClientProvider>
  );
}

export default App;
