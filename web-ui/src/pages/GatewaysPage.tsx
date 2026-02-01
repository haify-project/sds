import { useQuery } from '@tanstack/react-query';
import { api } from '../services/api';
import { clsx } from 'clsx';
import {
  MdHub,
  MdAdd,
  MdClose,
  MdPlay,
  MdStop,
  MdInfo,
  MdDelete,
} from 'react-icons/md';

export function GatewaysPage() {
  const { data: gateways, isLoading } = useQuery({
    queryKey: ['gateways'],
    queryFn: () => api.getGateways(),
  });

  if (isLoading) {
    return <div className="text-center py-12">Loading...</div>;
  }

  return (
    <div className="space-y-6">
      <div className="flex justify-between items-center">
        <h3 className="text-lg font-semibold">Storage Gateways</h3>
        <div className="flex gap-2">
          <button className="btn btn-secondary flex items-center gap-2">
            <MdAdd className="h-4 w-4" />
            NFS Gateway
          </button>
          <button className="btn btn-secondary flex items-center gap-2">
            <MdAdd className="h-4 w-4" />
            iSCSI Gateway
          </button>
          <button className="btn btn-secondary flex items-center gap-2">
            <MdAdd className="h-4 w-4" />
            NVMe Gateway
          </button>
        </div>
      </div>

      {/* Storage Gateways */}
      <div>
        <h4 className="text-md font-semibold mb-3">All Gateways</h4>
        <div className="card overflow-hidden">
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-200 bg-gray-50">
                <th className="text-left py-3 px-4 text-sm font-medium text-gray-500">Name</th>
                <th className="text-left py-3 px-4 text-sm font-medium text-gray-500">Type</th>
                <th className="text-left py-3 px-4 text-sm font-medium text-gray-500">Resource</th>
                <th className="text-left py-3 px-4 text-sm font-medium text-gray-500">State</th>
                <th className="text-left py-3 px-4 text-sm font-medium text-gray-500">Node</th>
                <th className="text-right py-3 px-4 text-sm font-medium text-gray-500">Actions</th>
              </tr>
            </thead>
            <tbody>
              {gateways?.gateways.map((gateway) => (
                <tr key={gateway.id} className="border-b border-gray-100 hover:bg-gray-50">
                  <td className="py-3 px-4">
                    <div className="flex items-center gap-2">
                      <div className="h-8 w-8 rounded bg-orange-100 flex items-center justify-center">
                        <MdHub className="h-4 w-4 text-orange-600" />
                      </div>
                      <span className="font-medium">{gateway.name}</span>
                    </div>
                  </td>
                  <td className="py-3 px-4">
                    <span className="inline-flex items-center px-2 py-1 rounded text-xs font-medium bg-orange-100 text-orange-700">
                      {gateway.type.toUpperCase()}
                    </span>
                  </td>
                  <td className="py-3 px-4 text-sm text-gray-500">{gateway.resource}</td>
                  <td className="py-3 px-4">
                    <span className={clsx(
                      'inline-flex items-center px-2 py-1 rounded-full text-xs font-medium',
                      gateway.state === 'running' ? 'bg-green-100 text-green-700' : 'bg-gray-100 text-gray-700',
                    )}>
                      {gateway.state || 'Unknown'}
                    </span>
                  </td>
                  <td className="py-3 px-4 text-sm text-gray-500">{gateway.node || '-'}</td>
                  <td className="py-3 px-4">
                    <div className="flex justify-end gap-2">
                      <button className="btn btn-secondary text-xs py-1 px-2 flex items-center gap-1">
                        <MdInfo className="h-3 w-3" />
                        Details
                      </button>
                      <button className="btn btn-secondary text-xs py-1 px-2 flex items-center gap-1">
                        <MdDelete className="h-3 w-3" />
                        Delete
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>

          {!gateways?.gateways.length && (
            <div className="text-center py-12 text-gray-500">
              No gateways found. Create a gateway to expose your storage.
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function clsx(...classes: (string | boolean | undefined)[]) {
  return classes.filter(Boolean).join(' ');
}
