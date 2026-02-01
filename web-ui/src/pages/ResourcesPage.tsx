import { useQuery } from '@tanstack/react-query';
import { api } from '../services/api';
import { clsx } from 'clsx';
import { MdViewInAr, MdAdd, MdVisibility, MdEdit, MdDelete } from 'react-icons/md';

export function ResourcesPage() {
  const { data: resources, isLoading } = useQuery({
    queryKey: ['resources'],
    queryFn: () => api.getResources(),
  });

  if (isLoading) {
    return <div className="text-center py-12">Loading...</div>;
  }

  return (
    <div className="space-y-6">
      <div className="flex justify-between items-center">
        <h3 className="text-lg font-semibold">DRBD Resources</h3>
        <button className="btn btn-primary flex items-center gap-2">
          <MdAdd className="h-4 w-4" />
          Create Resource
        </button>
      </div>

      <div className="card overflow-hidden">
        <table className="w-full">
          <thead>
            <tr className="border-b border-gray-200 bg-gray-50">
              <th className="text-left py-3 px-4 text-sm font-medium text-gray-500">Resource</th>
              <th className="text-left py-3 px-4 text-sm font-medium text-gray-500">Port</th>
              <th className="text-left py-3 px-4 text-sm font-medium text-gray-500">Protocol</th>
              <th className="text-left py-3 px-4 text-sm font-medium text-gray-500">Nodes</th>
              <th className="text-left py-3 px-4 text-sm font-medium text-gray-500">Role</th>
              <th className="text-left py-3 px-4 text-sm font-medium text-gray-500">Volumes</th>
              <th className="text-right py-3 px-4 text-sm font-medium text-gray-500">Actions</th>
            </tr>
          </thead>
          <tbody>
            {resources?.resources.map((resource) => (
              <ResourceRow key={resource.name} resource={resource} />
            ))}
          </tbody>
        </table>

        {!resources?.resources.length && (
          <div className="text-center py-12 text-gray-500">
            No resources found. Create your first resource to get started.
          </div>
        )}
      </div>
    </div>
  );
}

interface ResourceRowProps {
  resource: {
    name: string;
    port: number;
    protocol: string;
    nodes: string[];
    role: string;
    volumes: Array<{ volumeId: number; device: string; sizeGb: number }>;
  };
}

function ResourceRow({ resource }: ResourceRowProps) {
  return (
    <tr className="border-b border-gray-100 hover:bg-gray-50">
      <td className="py-3 px-4">
        <div className="flex items-center gap-2">
          <div className="h-8 w-8 rounded bg-purple-100 flex items-center justify-center">
            <MdViewInAr className="h-4 w-4 text-purple-600" />
          </div>
          <span className="font-medium">{resource.name}</span>
        </div>
      </td>
      <td className="py-3 px-4 text-sm text-gray-500">{resource.port}</td>
      <td className="py-3 px-4">
        <span className="inline-flex items-center px-2 py-1 rounded text-xs font-medium bg-blue-100 text-blue-700">
          {resource.protocol}
        </span>
      </td>
      <td className="py-3 px-4 text-sm text-gray-500">
        <div className="flex gap-1">
          {resource.nodes.map((node) => (
            <span key={node} className="px-2 py-0.5 bg-gray-100 rounded text-xs">
              {node}
            </span>
          ))}
        </div>
      </td>
      <td className="py-3 px-4">
        <span className={clsx(
          'inline-flex items-center px-2 py-1 rounded-full text-xs font-medium',
          resource.role === 'Primary' && 'bg-green-100 text-green-700',
          resource.role === 'Secondary' && 'bg-gray-100 text-gray-700',
          resource.role === 'Unknown' && 'bg-yellow-100 text-yellow-700',
        )}>
          {resource.role}
        </span>
      </td>
      <td className="py-3 px-4 text-sm text-gray-500">{resource.volumes.length}</td>
      <td className="py-3 px-4">
        <div className="flex justify-end gap-2">
          <button className="btn btn-secondary text-xs py-1 px-2 flex items-center gap-1">
            <MdVisibility className="h-3 w-3" />
            Status
          </button>
          <button className="btn btn-secondary text-xs py-1 px-2 flex items-center gap-1">
            <MdEdit className="h-3 w-3" />
            Edit
          </button>
          <button className="btn btn-danger text-xs py-1 px-2 flex items-center gap-1">
            <MdDelete className="h-3 w-3" />
            Delete
          </button>
        </div>
      </td>
    </tr>
  );
}

function clsx(...classes: (string | boolean | undefined)[]) {
  return classes.filter(Boolean).join(' ');
}
