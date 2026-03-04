import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api, ResourceStatus } from '../services/api';
import { clsx } from 'clsx';
import {
  MdViewInAr,
  MdAdd,
  MdClose,
  MdVisibility,
  MdDelete,
  MdArrowUpward,
  MdArrowDownward,
  MdStorage,
  MdFolder,
  MdRefresh,
} from 'react-icons/md';

export function ResourcesPage() {
  const queryClient = useQueryClient();
  const { data: resources, isLoading } = useQuery({
    queryKey: ['resources'],
    queryFn: () => api.getResources(),
  });

  const { data: pools } = useQuery({
    queryKey: ['pools'],
    queryFn: () => api.getPools(),
  });

  const { data: nodes } = useQuery({
    queryKey: ['nodes'],
    queryFn: () => api.getNodes(),
  });

  const [showCreateModal, setShowCreateModal] = useState(false);
  const [showStatusModal, setShowStatusModal] = useState(false);
  const [showMountModal, setShowMountModal] = useState(false);
  const [selectedResource, setSelectedResource] = useState<string | null>(null);
  const [statusData, setStatusData] = useState<ResourceStatus | null>(null);
  const [mountResource, setMountResource] = useState<{ name: string; volumeId: number } | null>(null);

  const deleteMutation = useMutation({
    mutationFn: (name: string) => api.deleteResource(name),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['resources'] });
    },
    onError: (error) => {
      alert(`Failed to delete resource: ${error.message}`);
    },
  });

  const primaryMutation = useMutation({
    mutationFn: (data: { resource: string; node: string }) =>
      api.setPrimary(data.resource, data.node, true),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['resources'] });
    },
    onError: (error) => {
      alert(`Failed to set primary: ${error.message}`);
    },
  });

  const secondaryMutation = useMutation({
    mutationFn: (data: { resource: string; node: string }) =>
      api.setSecondary(data.resource, data.node),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['resources'] });
    },
    onError: (error) => {
      alert(`Failed to set secondary: ${error.message}`);
    },
  });

  const statusMutation = useMutation({
    mutationFn: (name: string) => api.resourceStatus(name),
    onSuccess: (data) => {
      setStatusData(data.status);
      setSelectedResource(null);
    },
    onError: (error) => {
      alert(`Failed to get status: ${error.message}`);
      setSelectedResource(null);
    },
  });

  const handleDelete = (name: string) => {
    if (confirm(`Are you sure you want to delete resource "${name}"? This will remove all data.`)) {
      deleteMutation.mutate(name);
    }
  };

  const handleSetPrimary = (resource: string, node: string) => {
    if (confirm(`Set ${resource} as Primary on ${node}?`)) {
      primaryMutation.mutate({ resource, node });
    }
  };

  const handleSetSecondary = (resource: string, node: string) => {
    if (confirm(`Set ${resource} as Secondary on ${node}?`)) {
      secondaryMutation.mutate({ resource, node });
    }
  };

  const handleShowStatus = (name: string) => {
    setSelectedResource(name);
    statusMutation.mutate(name);
  };

  const handleMount = (name: string, volumeId: number) => {
    setMountResource({ name, volumeId });
    setShowMountModal(true);
  };

  if (isLoading) {
    return <div className="text-center py-12">Loading...</div>;
  }

  return (
    <div className="space-y-6">
      <div className="flex justify-between items-center">
        <h3 className="text-lg font-semibold">DRBD Resources</h3>
        <button
          onClick={() => setShowCreateModal(true)}
          className="btn btn-primary flex items-center gap-2"
        >
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
              <ResourceRow
                key={resource.name}
                resource={resource}
                onDelete={handleDelete}
                onSetPrimary={handleSetPrimary}
                onSetSecondary={handleSetSecondary}
                onShowStatus={handleShowStatus}
                onMount={handleMount}
                isLoading={statusMutation.isPending && selectedResource === resource.name}
                isDeleting={deleteMutation.isPending}
                isUpdating={primaryMutation.isPending || secondaryMutation.isPending}
              />
            ))}
          </tbody>
        </table>

        {!resources?.resources.length && (
          <div className="text-center py-12 text-gray-500">
            No resources found. Create your first resource to get started.
          </div>
        )}
      </div>

      {/* Create Resource Modal */}
      {showCreateModal && (
        <Modal onClose={() => setShowCreateModal(false)} title="Create DRBD Resource">
          <CreateResourceForm
            nodes={nodes?.nodes ?? []}
            pools={pools?.pools ?? []}
            onSuccess={() => {
              setShowCreateModal(false);
              queryClient.invalidateQueries({ queryKey: ['resources'] });
            }}
            onCancel={() => setShowCreateModal(false)}
          />
        </Modal>
      )}

      {/* Status Modal */}
      {showStatusModal && statusData && (
        <Modal onClose={() => setShowStatusModal(false)} title={`Resource Status: ${statusData.name}`}>
          <ResourceStatusView status={statusData} onClose={() => setShowStatusModal(false)} />
        </Modal>
      )}

      {/* Mount Modal */}
      {showMountModal && mountResource && (
        <Modal onClose={() => setShowMountModal(false)} title={`Mount Resource: ${mountResource.name}`}>
          <MountForm
            resourceName={mountResource.name}
            volumeId={mountResource.volumeId}
            onSuccess={() => {
              setShowMountModal(false);
              queryClient.invalidateQueries({ queryKey: ['resources'] });
            }}
            onCancel={() => setShowMountModal(false)}
          />
        </Modal>
      )}
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
    nodeStates?: Record<string, { role: string; diskState: string; replication: string }>;
  };
  onDelete: (name: string) => void;
  onSetPrimary: (resource: string, node: string) => void;
  onSetSecondary: (resource: string, node: string) => void;
  onShowStatus: (name: string) => void;
  onMount: (name: string, volumeId: number) => void;
  isLoading: boolean;
  isDeleting: boolean;
  isUpdating: boolean;
}

function ResourceRow({ resource, onDelete, onSetPrimary, onSetSecondary, onShowStatus, onMount, isLoading, isDeleting, isUpdating }: ResourceRowProps) {
  const [showActions, setShowActions] = useState(false);
  const primaryNode = resource.nodes.find(n => resource.nodeStates?.[n]?.role === 'Primary');

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
        <div className="flex flex-wrap gap-1">
          {resource.nodes.map((node) => {
            const state = resource.nodeStates?.[node];
            const isPrimary = state?.role === 'Primary';
            return (
              <span
                key={node}
                className={clsx(
                  'px-2 py-0.5 rounded text-xs',
                  isPrimary ? 'bg-green-100 text-green-700' : 'bg-gray-100 text-gray-700'
                )}
              >
                {node}{isPrimary ? ' (P)' : ''}
              </span>
            );
          })}
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
        <div className="flex justify-end gap-1">
          <button
            onClick={() => onShowStatus(resource.name)}
            disabled={isLoading}
            className="btn btn-secondary text-xs py-1 px-2 flex items-center gap-1"
          >
            {isLoading ? <MdRefresh className="h-3 w-3 animate-spin" /> : <MdVisibility className="h-3 w-3" />}
            Status
          </button>
          <div className="relative">
            <button
              onClick={() => setShowActions(!showActions)}
              className="btn btn-secondary text-xs py-1 px-2"
            >
              More
            </button>
            {showActions && (
              <div className="absolute right-0 mt-1 w-48 bg-white rounded-lg shadow-lg border z-10">
                <div className="py-1">
                  {resource.nodes.map((node) => (
                    <button
                      key={node}
                      onClick={() => {
                        onSetPrimary(resource.name, node);
                        setShowActions(false);
                      }}
                      disabled={isUpdating}
                      className="w-full text-left px-4 py-2 text-sm hover:bg-gray-100 flex items-center gap-2"
                    >
                      <MdArrowUpward className="h-3 w-3" />
                      Primary on {node}
                    </button>
                  ))}
                  {resource.volumes.map((vol) => (
                    <button
                      key={vol.volumeId}
                      onClick={() => {
                        onMount(resource.name, vol.volumeId);
                        setShowActions(false);
                      }}
                      className="w-full text-left px-4 py-2 text-sm hover:bg-gray-100 flex items-center gap-2"
                    >
                      <MdFolder className="h-3 w-3" />
                      Mount Vol {vol.volumeId}
                    </button>
                  ))}
                  <hr className="my-1" />
                  <button
                    onClick={() => {
                      onDelete(resource.name);
                      setShowActions(false);
                    }}
                    disabled={isDeleting}
                    className="w-full text-left px-4 py-2 text-sm text-red-600 hover:bg-red-50 flex items-center gap-2"
                  >
                    <MdDelete className="h-3 w-3" />
                    Delete
                  </button>
                </div>
              </div>
            )}
          </div>
        </div>
      </td>
    </tr>
  );
}

interface ModalProps {
  onClose: () => void;
  title: string;
  children: React.ReactNode;
}

function Modal({ onClose, title, children }: ModalProps) {
  return (
    <div className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50">
      <div className="bg-white rounded-lg shadow-xl max-w-lg w-full mx-4 max-h-[90vh] overflow-y-auto">
        <div className="flex items-center justify-between p-4 border-b sticky top-0 bg-white">
          <h3 className="text-lg font-semibold">{title}</h3>
          <button onClick={onClose} className="text-gray-400 hover:text-gray-600">
            <MdClose className="h-5 w-5" />
          </button>
        </div>
        <div className="p-4">{children}</div>
      </div>
    </div>
  );
}

interface CreateResourceFormProps {
  nodes: Array<{ name: string; address: string }>;
  pools: Array<{ name: string; node: string; type: string; freeGb: string }>;
  onSuccess: () => void;
  onCancel: () => void;
}

function CreateResourceForm({ nodes, pools, onSuccess, onCancel }: CreateResourceFormProps) {
  const [name, setName] = useState('');
  const [port, setPort] = useState('7000');
  const [protocol, setProtocol] = useState('C');
  const [selectedNodes, setSelectedNodes] = useState<string[]>([]);
  const [sizeGb, setSizeGb] = useState('10');
  const [pool, setPool] = useState('');

  const createMutation = useMutation({
    mutationFn: (data: {
      name: string;
      port: number;
      nodes: string[];
      protocol: string;
      sizeGb: number;
      pool: string;
    }) => api.createResource(data),
    onSuccess: () => onSuccess(),
    onError: (error) => alert(`Failed to create resource: ${error.message}`),
  });

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (selectedNodes.length < 2) {
      alert('Please select at least 2 nodes for replication');
      return;
    }
    createMutation.mutate({
      name,
      port: parseInt(port),
      nodes: selectedNodes,
      protocol,
      sizeGb: parseInt(sizeGb),
      pool,
    });
  };

  const toggleNode = (nodeName: string) => {
    setSelectedNodes((prev) =>
      prev.includes(nodeName) ? prev.filter((n) => n !== nodeName) : [...prev, nodeName]
    );
  };

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">Resource Name</label>
        <input
          type="text"
          value={name}
          onChange={(e) => setName(e.target.value)}
          className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
          placeholder="e.g., data"
          required
        />
      </div>

      <div className="grid grid-cols-2 gap-4">
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Port</label>
          <input
            type="number"
            value={port}
            onChange={(e) => setPort(e.target.value)}
            className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
            required
          />
        </div>
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Protocol</label>
          <select
            value={protocol}
            onChange={(e) => setProtocol(e.target.value)}
            className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
          >
            <option value="C">C (Sync)</option>
            <option value="A">A (Async)</option>
            <option value="B">B (Semi-sync)</option>
          </select>
        </div>
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">Nodes (select at least 2)</label>
        <div className="space-y-2">
          {nodes.map((node) => (
            <label key={node.name} className="flex items-center gap-2 cursor-pointer">
              <input
                type="checkbox"
                checked={selectedNodes.includes(node.name)}
                onChange={() => toggleNode(node.name)}
                className="rounded border-gray-300 text-primary-600"
              />
              <span className="text-sm">{node.name} ({node.address})</span>
            </label>
          ))}
        </div>
      </div>

      <div className="grid grid-cols-2 gap-4">
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Size (GB)</label>
          <input
            type="number"
            value={sizeGb}
            onChange={(e) => setSizeGb(e.target.value)}
            className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
            required
          />
        </div>
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Pool (optional)</label>
          <select
            value={pool}
            onChange={(e) => setPool(e.target.value)}
            className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
          >
            <option value="">Auto-select</option>
            {pools.map((p) => (
              <option key={`${p.node}-${p.name}`} value={p.name}>
                {p.name} ({p.node}) - {p.freeGb}GB free
              </option>
            ))}
          </select>
        </div>
      </div>

      <div className="flex gap-2 pt-2">
        <button type="button" onClick={onCancel} disabled={createMutation.isPending} className="btn btn-secondary flex-1">
          Cancel
        </button>
        <button type="submit" disabled={createMutation.isPending} className="btn btn-primary flex-1">
          {createMutation.isPending ? 'Creating...' : 'Create'}
        </button>
      </div>
    </form>
  );
}

interface ResourceStatusViewProps {
  status: ResourceStatus;
  onClose: () => void;
}

function ResourceStatusView({ status, onClose }: ResourceStatusViewProps) {
  return (
    <div className="space-y-4">
      <div className="space-y-2">
        <div className="flex justify-between py-2 border-b border-gray-100">
          <span className="text-gray-500">Name</span>
          <span className="font-medium">{status.name}</span>
        </div>
        <div className="flex justify-between py-2 border-b border-gray-100">
          <span className="text-gray-500">Role</span>
          <span className={clsx(
            'font-medium',
            status.role === 'Primary' ? 'text-green-600' : 'text-gray-600'
          )}>{status.role}</span>
        </div>
        <div className="flex justify-between py-2 border-b border-gray-100">
          <span className="text-gray-500">Nodes</span>
          <span className="font-medium">{status.nodes.join(', ')}</span>
        </div>
      </div>

      <div>
        <h4 className="text-sm font-medium text-gray-700 mb-2">Node States</h4>
        <div className="space-y-2">
          {Object.entries(status.nodeStates || {}).map(([node, state]) => (
            <div key={node} className="flex items-center justify-between p-2 bg-gray-50 rounded">
              <span className="text-sm font-medium">{node}</span>
              <div className="flex gap-2">
                <span className={clsx(
                  'text-xs px-2 py-1 rounded',
                  state.role === 'Primary' ? 'bg-green-100 text-green-700' : 'bg-gray-100 text-gray-700'
                )}>{state.role}</span>
                <span className="text-xs px-2 py-1 rounded bg-blue-100 text-blue-700">{state.diskState}</span>
              </div>
            </div>
          ))}
        </div>
      </div>

      <div>
        <h4 className="text-sm font-medium text-gray-700 mb-2">Volumes</h4>
        <div className="space-y-2">
          {status.volumes?.map((vol) => (
            <div key={vol.volumeId} className="flex items-center justify-between p-2 bg-gray-50 rounded">
              <div>
                <span className="text-sm font-medium">Volume {vol.volumeId}</span>
                <span className="text-xs text-gray-500 ml-2">{vol.device}</span>
              </div>
              <span className="text-xs text-gray-500">{vol.sizeGb} GB</span>
            </div>
          ))}
        </div>
      </div>

      <button onClick={onClose} className="btn btn-primary w-full">Close</button>
    </div>
  );
}

interface MountFormProps {
  resourceName: string;
  volumeId: number;
  onSuccess: () => void;
  onCancel: () => void;
}

function MountForm({ resourceName, volumeId, onSuccess, onCancel }: MountFormProps) {
  const [path, setPath] = useState('');
  const [fstype, setFstype] = useState('ext4');
  const [action, setAction] = useState<'mount' | 'unmount' | 'format'>('mount');

  const mountMutation = useMutation({
    mutationFn: () => api.mountResource(resourceName, volumeId, path, fstype),
    onSuccess: () => onSuccess(),
    onError: (error) => alert(`Failed to mount: ${error.message}`),
  });

  const unmountMutation = useMutation({
    mutationFn: () => api.unmountResource(resourceName, volumeId),
    onSuccess: () => onSuccess(),
    onError: (error) => alert(`Failed to unmount: ${error.message}`),
  });

  const formatMutation = useMutation({
    mutationFn: () => api.createFilesystem(resourceName, volumeId, fstype),
    onSuccess: () => {
      alert('Filesystem created successfully');
    },
    onError: (error) => alert(`Failed to create filesystem: ${error.message}`),
  });

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (action === 'mount') mountMutation.mutate();
    else if (action === 'unmount') unmountMutation.mutate();
    else formatMutation.mutate();
  };

  const isLoading = mountMutation.isPending || unmountMutation.isPending || formatMutation.isPending;

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div className="p-3 bg-gray-50 rounded-lg text-sm">
        <span className="text-gray-500">Resource:</span> {resourceName} (Volume {volumeId})
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">Action</label>
        <div className="flex gap-2">
          {(['mount', 'unmount', 'format'] as const).map((a) => (
            <button
              key={a}
              type="button"
              onClick={() => setAction(a)}
              className={clsx(
                'flex-1 py-2 px-3 text-sm rounded-md border',
                action === a ? 'bg-primary-50 border-primary-500 text-primary-700' : 'border-gray-300'
              )}
            >
              {a.charAt(0).toUpperCase() + a.slice(1)}
            </button>
          ))}
        </div>
      </div>

      {action !== 'unmount' && (
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Filesystem Type</label>
          <select
            value={fstype}
            onChange={(e) => setFstype(e.target.value)}
            className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
          >
            <option value="ext4">ext4</option>
            <option value="xfs">XFS</option>
          </select>
        </div>
      )}

      {action === 'mount' && (
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">Mount Path</label>
          <input
            type="text"
            value={path}
            onChange={(e) => setPath(e.target.value)}
            className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
            placeholder="e.g., /mnt/data"
            required
          />
        </div>
      )}

      <div className="flex gap-2 pt-2">
        <button type="button" onClick={onCancel} disabled={isLoading} className="btn btn-secondary flex-1">
          Cancel
        </button>
        <button type="submit" disabled={isLoading} className="btn btn-primary flex-1">
          {isLoading ? 'Processing...' : action.charAt(0).toUpperCase() + action.slice(1)}
        </button>
      </div>
    </form>
  );
}

