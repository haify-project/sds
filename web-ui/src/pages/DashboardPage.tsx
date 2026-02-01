import { useQuery } from '@tanstack/react-query';
import { api } from '../services/api';
import { MdComputer, MdStorage, MdViewInAr, MdHub } from 'react-icons/md';

export function DashboardPage() {
  const { data: nodes } = useQuery({
    queryKey: ['nodes'],
    queryFn: () => api.getNodes(),
  });

  const { data: pools } = useQuery({
    queryKey: ['pools'],
    queryFn: () => api.getPools(),
  });

  const { data: resources } = useQuery({
    queryKey: ['resources'],
    queryFn: () => api.getResources(),
  });

  const { data: gateways } = useQuery({
    queryKey: ['gateways'],
    queryFn: () => api.getGateways(),
  });

  const onlineNodes = nodes?.nodes.filter((n) => n.state === 'online').length ?? 0;
  const totalStorage = pools?.pools.reduce((acc, p) => acc + Number(p.totalGb), 0) ?? 0;
  const freeStorage = pools?.pools.reduce((acc, p) => acc + Number(p.freeGb), 0) ?? 0;

  return (
    <div className="space-y-6">
      {/* Stats Cards */}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-6">
        <StatCard
          title="Nodes"
          value={`${onlineNodes}/${nodes?.nodes.length ?? 0}`}
          subtitle="Online nodes"
          color="blue"
          icon={<MdComputer className="h-6 w-6" />}
        />
        <StatCard
          title="Storage"
          value={`${freeStorage}GB`}
          subtitle={`of ${totalStorage}GB free`}
          color="green"
          icon={<MdStorage className="h-6 w-6" />}
        />
        <StatCard
          title="Resources"
          value={resources?.resources.length ?? 0}
          subtitle="DRBD resources"
          color="purple"
          icon={<MdViewInAr className="h-6 w-6" />}
        />
        <StatCard
          title="Gateways"
          value={gateways?.gateways.length ?? 0}
          subtitle="Active gateways"
          color="orange"
          icon={<MdHub className="h-6 w-6" />}
        />
      </div>

      {/* Nodes List */}
      <div className="card">
        <h3 className="text-lg font-semibold mb-4">Cluster Nodes</h3>
        <div className="overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-200">
                <th className="text-left py-3 px-4 text-sm font-medium text-gray-500">Name</th>
                <th className="text-left py-3 px-4 text-sm font-medium text-gray-500">Address</th>
                <th className="text-left py-3 px-4 text-sm font-medium text-gray-500">Hostname</th>
                <th className="text-left py-3 px-4 text-sm font-medium text-gray-500">State</th>
                <th className="text-left py-3 px-4 text-sm font-medium text-gray-500">Version</th>
              </tr>
            </thead>
            <tbody>
              {nodes?.nodes.map((node) => (
                <tr key={node.name} className="border-b border-gray-100 hover:bg-gray-50">
                  <td className="py-3 px-4 text-sm">{node.name}</td>
                  <td className="py-3 px-4 text-sm text-gray-500">{node.address}</td>
                  <td className="py-3 px-4 text-sm text-gray-500">{node.hostname}</td>
                  <td className="py-3 px-4">
                    <span className="inline-flex items-center px-2 py-1 rounded-full text-xs font-medium bg-green-100 text-green-800">
                      {node.state}
                    </span>
                  </td>
                  <td className="py-3 px-4 text-sm text-gray-500">{node.version}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}

interface StatCardProps {
  title: string;
  value: number | string;
  subtitle: string;
  color: 'blue' | 'green' | 'purple' | 'orange';
  icon: React.ReactNode;
}

function StatCard({ title, value, subtitle, color, icon }: StatCardProps) {
  const colors = {
    blue: 'bg-blue-500',
    green: 'bg-green-500',
    purple: 'bg-purple-500',
    orange: 'bg-orange-500',
  };

  return (
    <div className="card">
      <div className="flex items-center justify-between">
        <div>
          <p className="text-sm font-medium text-gray-500">{title}</p>
          <p className="text-2xl font-bold text-gray-900 mt-1">{value}</p>
          <p className="text-sm text-gray-500 mt-1">{subtitle}</p>
        </div>
        <div className={`h-12 w-12 rounded-lg ${colors[color]} flex items-center justify-center text-white`}>
          {icon}
        </div>
      </div>
    </div>
  );
}
