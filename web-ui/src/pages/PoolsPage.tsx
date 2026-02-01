import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '../services/api';
import { clsx } from 'clsx';
import {
  MdStorage,
  MdAdd,
  MdClose,
  MdAddCircle,
  MdDelete,
} from 'react-icons/md';

export function PoolsPage() {
  const queryClient = useQueryClient();
  const { data: pools, isLoading } = useQuery({
    queryKey: ['pools'],
    queryFn: () => api.getPools(),
  });

  const { data: nodes } = useQuery({
    queryKey: ['nodes'],
    queryFn: () => api.getNodes(),
  });

  const [showCreateModal, setShowCreateModal] = useState(false);
  const [showAddDiskModal, setShowAddDiskModal] = useState(false);
  const [selectedPool, setSelectedPool] = useState<{ name: string; node: string } | null>(null);

  // Create pool mutation
  const createPoolMutation = useMutation({
    mutationFn: (data: { name: string; type: string; node: string; disks: string[] }) =>
      api.createPool(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['pools'] });
      setShowCreateModal(false);
    },
    onError: (error) => {
      alert(`Failed to create pool: ${error.message}`);
    },
  });

  // Add disk mutation
  const addDiskMutation = useMutation({
    mutationFn: (data: { pool: string; disk: string; node: string }) =>
      api.addDisk(data.pool, data.disk, data.node),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['pools'] });
      setShowAddDiskModal(false);
      setSelectedPool(null);
    },
    onError: (error) => {
      alert(`Failed to add disk: ${error.message}`);
    },
  });

  // Delete pool mutation
  const deletePoolMutation = useMutation({
    mutationFn: (data: { name: string; node: string }) =>
      api.deletePool(data.name),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['pools'] });
    },
    onError: (error) => {
      alert(`Failed to delete pool: ${error.message}`);
    },
  });

  // Create node address to name mapping
  const nodeMap = nodes?.nodes.reduce((acc, node) => {
    acc[node.address] = node.name;
    return acc;
  }, {} as Record<string, string>) ?? {};

  const handleAddDisk = (poolName: string, nodeAddr: string) => {
    setSelectedPool({ name: poolName, node: nodeAddr });
    setShowAddDiskModal(true);
  };

  const handleDeletePool = (poolName: string, nodeName: string) => {
    if (confirm(`Are you sure you want to delete pool "${poolName}"?`)) {
      deletePoolMutation.mutate({ name: poolName, node: nodeName });
    }
  };

  if (isLoading) {
    return <div className="text-center py-12">Loading...</div>;
  }

  // Group pools by node
  const poolsByNode = pools?.pools.reduce((acc, pool) => {
    if (!acc[pool.node]) {
      acc[pool.node] = [];
    }
    acc[pool.node].push(pool);
    return acc;
  }, {} as Record<string, typeof pools.pools>) ?? {};

  return (
    <div className="space-y-6">
      <div className="flex justify-between items-center">
        <h3 className="text-lg font-semibold">Storage Pools</h3>
        <button
          onClick={() => setShowCreateModal(true)}
          className="btn btn-primary flex items-center gap-2"
        >
          <MdAdd className="h-4 w-4" />
          Create Pool
        </button>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 xl:grid-cols-3 gap-6">
        {Object.entries(poolsByNode).map(([nodeAddr, nodePools]) => {
          const nodeName = nodeMap[nodeAddr] || nodeAddr;
          return (
            <div key={nodeAddr} className="card">
              <div className="flex items-center gap-3 mb-4">
                <div className="h-10 w-10 rounded-full bg-primary-100 flex items-center justify-center">
                  <MdStorage className="h-5 w-5 text-primary-600" />
                </div>
                <div>
                  <h4 className="font-semibold text-gray-900">{nodeName}</h4>
                  <p className="text-sm text-gray-500">{nodeAddr} • {nodePools.length} pool{nodePools.length > 1 ? 's' : ''}</p>
                </div>
              </div>

              <div className="space-y-3">
                {nodePools.map((pool) => (
                  <PoolItem
                    key={`${pool.node}-${pool.name}`}
                    pool={pool}
                    nodeName={nodeName}
                    onAddDisk={handleAddDisk}
                    onDelete={handleDeletePool}
                    isDeleting={deletePoolMutation.isPending}
                  />
                ))}
              </div>
            </div>
          );
        })}
      </div>

      {/* Create Pool Modal */}
      {showCreateModal && (
        <Modal onClose={() => setShowCreateModal(false)} title="Create Storage Pool">
          <CreatePoolForm
            nodes={nodes?.nodes ?? []}
            isCreating={createPoolMutation.isPending}
            onSubmit={(data) => createPoolMutation.mutate(data)}
            onCancel={() => setShowCreateModal(false)}
          />
        </Modal>
      )}

      {/* Add Disk Modal */}
      {showAddDiskModal && selectedPool && (
        <Modal onClose={() => setShowAddDiskModal(false)} title="Add Disk to Pool">
          <AddDiskForm
            poolName={selectedPool.name}
            isAdding={addDiskMutation.isPending}
            onSubmit={(disk) => addDiskMutation.mutate({ pool: selectedPool.name, disk, node: selectedPool.node })}
            onCancel={() => {
              setShowAddDiskModal(false);
              setSelectedPool(null);
            }}
          />
        </Modal>
      )}
    </div>
  );
}

interface PoolItemProps {
  pool: {
    name: string;
    type: string;
    node: string;
    totalGb: string;
    freeGb: string;
    thin: boolean;
    compression: string;
  };
  nodeName: string;
  onAddDisk: (poolName: string, nodeAddr: string) => void;
  onDelete: (poolName: string, nodeName: string) => void;
  isDeleting: boolean;
}

function PoolItem({ pool, nodeName, onAddDisk, onDelete, isDeleting }: PoolItemProps) {
  const total = Number(pool.totalGb);
  const free = Number(pool.freeGb);
  const usedPercent = total > 0 ? ((total - free) / total) * 100 : 0;

  return (
    <div className="p-3 bg-gray-50 rounded-lg">
      <div className="flex justify-between items-center mb-2">
        <div>
          <span className="font-medium text-sm">{pool.name}</span>
        </div>
        <div className="flex items-center gap-2">
          <span className={clsx('text-xs px-2 py-0.5 rounded', pool.type === 'zfs' ? 'bg-blue-100 text-blue-700' : 'bg-purple-100 text-purple-700')}>
            {pool.type.toUpperCase()}
          </span>
          <button
            onClick={() => onAddDisk(pool.name, pool.node)}
            className="text-gray-400 hover:text-primary-600 transition-colors"
            title="Add disk"
          >
            <MdAddCircle className="h-4 w-4" />
          </button>
          <button
            onClick={() => onDelete(pool.name, nodeName)}
            disabled={isDeleting}
            className={clsx(
              'transition-colors',
              isDeleting ? 'text-gray-300 cursor-not-allowed' : 'text-gray-400 hover:text-red-600'
            )}
            title="Delete pool"
          >
            <MdDelete className="h-4 w-4" />
          </button>
        </div>
      </div>

      <div className="flex justify-between text-xs text-gray-500 mb-1">
        <span>{free} GB free</span>
        <span>{total} GB total</span>
      </div>

      <div className="h-2 bg-gray-200 rounded-full overflow-hidden">
        <div
          className="h-full bg-primary-500 rounded-full transition-all"
          style={{ width: `${usedPercent}%` }}
        />
      </div>
    </div>
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
      <div className="bg-white rounded-lg shadow-xl max-w-lg w-full mx-4">
        <div className="flex items-center justify-between p-4 border-b">
          <h3 className="text-lg font-semibold">{title}</h3>
          <button
            onClick={onClose}
            className="text-gray-400 hover:text-gray-600"
          >
            <MdClose className="h-5 w-5" />
          </button>
        </div>
        <div className="p-4">
          {children}
        </div>
      </div>
    </div>
  );
}

interface CreatePoolFormProps {
  nodes: Array<{ name: string; address: string }>;
  isCreating: boolean;
  onSubmit: (data: { name: string; type: string; node: string; disks: string[] }) => void;
  onCancel: () => void;
}

function CreatePoolForm({ nodes, isCreating, onSubmit, onCancel }: CreatePoolFormProps) {
  const [name, setName] = useState('');
  const [type, setType] = useState('vg');
  const [node, setNode] = useState('');
  const [disks, setDisks] = useState('');

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const diskList = disks.split(',').map(d => d.trim()).filter(d => d);
    onSubmit({ name, type, node, disks: diskList });
  };

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">
          Pool Name
        </label>
        <input
          type="text"
          value={name}
          onChange={(e) => setName(e.target.value)}
          className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
          placeholder="e.g., data"
          required
        />
        <p className="text-xs text-gray-500 mt-1">Will be prefixed with "sds_"</p>
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">
          Pool Type
        </label>
        <select
          value={type}
          onChange={(e) => setType(e.target.value)}
          className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
        >
          <option value="vg">LVM VG</option>
          <option value="zfs">ZFS</option>
          <option value="thin_pool">LVM Thin Pool</option>
        </select>
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">
          Node
        </label>
        <select
          value={node}
          onChange={(e) => setNode(e.target.value)}
          className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
          required
        >
          <option value="">Select a node...</option>
          {nodes.map((n) => (
            <option key={n.name} value={n.name}>
              {n.name} ({n.address})
            </option>
          ))}
        </select>
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">
          Disks
        </label>
        <input
          type="text"
          value={disks}
          onChange={(e) => setDisks(e.target.value)}
          className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
          placeholder="e.g., /dev/vdb, /dev/vdc"
          required
        />
        <p className="text-xs text-gray-500 mt-1">Comma-separated disk paths</p>
      </div>

      <div className="flex gap-2 pt-2">
        <button
          type="button"
          onClick={onCancel}
          disabled={isCreating}
          className="btn btn-secondary flex-1"
        >
          Cancel
        </button>
        <button
          type="submit"
          disabled={isCreating}
          className="btn btn-primary flex-1"
        >
          {isCreating ? 'Creating...' : 'Create'}
        </button>
      </div>
    </form>
  );
}

interface AddDiskFormProps {
  poolName: string;
  isAdding: boolean;
  onSubmit: (disk: string) => void;
  onCancel: () => void;
}

function AddDiskForm({ poolName, isAdding, onSubmit, onCancel }: AddDiskFormProps) {
  const [disk, setDisk] = useState('');

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    onSubmit(disk);
  };

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">
          Pool
        </label>
        <input
          type="text"
          value={poolName}
          disabled
          className="w-full px-3 py-2 border border-gray-300 rounded-md bg-gray-100 text-gray-600"
        />
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">
          Disk Path
        </label>
        <input
          type="text"
          value={disk}
          onChange={(e) => setDisk(e.target.value)}
          className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
          placeholder="e.g., /dev/vdd"
          required
        />
      </div>

      <div className="flex gap-2 pt-2">
        <button
          type="button"
          onClick={onCancel}
          disabled={isAdding}
          className="btn btn-secondary flex-1"
        >
          Cancel
        </button>
        <button
          type="submit"
          disabled={isAdding}
          className="btn btn-primary flex-1"
        >
          {isAdding ? 'Adding...' : 'Add Disk'}
        </button>
      </div>
    </form>
  );
}

function clsx(...classes: (string | boolean | undefined)[]) {
  return classes.filter(Boolean).join(' ');
}
