import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { MainLayout } from './layouts/MainLayout';
import { DashboardPage } from './pages/DashboardPage';
import { NodesPage } from './pages/NodesPage';
import { PoolsPage } from './pages/PoolsPage';
import { ResourcesPage } from './pages/ResourcesPage';
import { GatewaysPage } from './pages/GatewaysPage';
import { HAPage } from './pages/HAPage';

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
      <BrowserRouter>
        <Routes>
          <Route path="/" element={<MainLayout />}>
            <Route index element={<Navigate to="/dashboard" replace />} />
            <Route path="dashboard" element={<DashboardPage />} />
            <Route path="nodes" element={<NodesPage />} />
            <Route path="pools" element={<PoolsPage />} />
            <Route path="resources" element={<ResourcesPage />} />
            <Route path="gateways" element={<GatewaysPage />} />
            <Route path="ha" element={<HAPage />} />
          </Route>
        </Routes>
      </BrowserRouter>
    </QueryClientProvider>
  );
}

export default App;
