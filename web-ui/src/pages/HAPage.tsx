import { useQuery } from '@tanstack/react-query';
import { api } from '../services/api';
import { clsx } from 'clsx';
import {
  MdHealthAndSafety,
  MdAdd,
  MdDelete,
  MdRefresh,
  MdExitToApp,
  MdInfo,
} from 'react-icons/md';

export function HAPage() {
  const { data: haConfigs, isLoading } = useQuery({
    queryKey: ['ha'],
    queryFn: () => api.getHaConfigs(),
  });

  const { data: resources } = useQuery({
    queryKey: ['resources'],
    queryFn: () => api.getResources(),
  });

  if (isLoading) {
    return <div className="text-center py-12">Loading...</div>;
  }

  const resourceMap = new Map(
    resources?.resources.map(r => [r.name, r] as [string, typeof r]) ?? []
  );

  return (
    <div className="space-y-6">
      <div className="flex justify-between items-center">
        <h3 className="text-lg font-semibold">HA Configurations</h3>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        {haConfigs?.configs.map((config) => (
          <HAConfigCard
            key={config.resource}
            config={config}
            resource={resourceMap.get(config.resource)}
          />
        ))}
      </div>

      {(!haConfigs?.configs || haConfigs.configs.length === 0) && (
        <div className="text-center py-12 text-gray-500">
          No HA configurations found. Create a gateway with HA enabled to get started.
        </div>
      )}
    </div>
  );
}

interface HAConfigCardProps {
  config: {
    resource: string;
    vip: string;
    mountPoint: string;
    fsType: string;
    services: string[];
  };
  resource?: {
    name: string;
    port: number;
    protocol: string;
    nodes: string[];
    role: string;
    volumes: Array<{ volumeId: number; device: string; sizeGb: number }>;
  };
}

function HAConfigCard({ config, resource }: HAConfigCardProps) {
  const isRunning = resource?.role === 'Primary';

  return (
    <div className="card">
      <div className="flex items-start justify-between mb-4">
        <div className="flex items-center gap-3">
          <div className="h-12 w-12 rounded-lg bg-primary-100 flex items-center justify-center">
            <MdHealthAndSafety className="h-6 w-6 text-primary-600" />
          </div>
          <div>
            <h4 className="font-semibold text-gray-900">{config.resource}</h4>
            {resource && (
              <p className="text-sm text-gray-500">
                Port: {resource.port} • Protocol: {resource.protocol}
              </p>
            )}
          </div>
        </div>
        <span className={clsx(
          'inline-flex items-center gap-1 px-3 py-1 rounded-full text-sm font-medium',
          isRunning ? 'bg-green-100 text-green-700' : 'bg-gray-100 text-gray-700'
        )}>
          {isRunning ? 'Running' : 'Stopped'}
        </span>
      </div>

      <div className="space-y-3">
        <InfoRow label="VIP" value={config.vip} />
        <InfoRow label="Mount Point" value={config.mountPoint} />
        <InfoRow label="Filesystem" value={config.fsType} />
        <InfoRow
          label="Services"
          value={config.services.map(s => (
            <span key={s} className="inline-flex items-center px-2 py-0.5 bg-blue-100 text-blue-700 rounded text-xs mr-1">
              {s}
            </span>
          ))}
        />
      </div>

      <div className="mt-4 pt-4 border-t border-gray-100 flex gap-2">
        <button className="btn btn-secondary text-xs flex items-center gap-1">
          <MdInfo className="h-3 w-3" />
          Details
        </button>
        <button className="btn btn-secondary text-xs flex items-center gap-1">
          <MdRefresh className="h-3 w-3" />
          Check
        </button>
        <button className="btn btn-warning text-xs flex items-center gap-1">
          <MdExitToApp className="h-3 w-3" />
          Evict
        </button>
        <button className="btn btn-danger text-xs flex items-center gap-1">
          <MdDelete className="h-3 w-3" />
          Delete
        </button>
      </div>
    </div>
  );
}

interface InfoRowProps {
  label: string;
  value: string | React.ReactNode;
}

function InfoRow({ label, value }: InfoRowProps) {
  return (
    <div className="flex justify-between text-sm">
      <span className="text-gray-500">{label}</span>
      <span className="text-gray-900 font-mono text-xs">
        {typeof value === 'string' ? value : value}
      </span>
    </div>
  );
}

function clsx(...classes: (string | boolean | undefined)[]) {
  return classes.filter(Boolean).join(' ');
}
