import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '../services/api';
import { clsx } from 'clsx';
import {
  MdHub,
  MdAdd,
  MdClose,
  MdPlayArrow,
  MdStop,
  MdInfo,
  MdDelete,
  MdStorage,
} from 'react-icons/md';

export function GatewaysPage() {
  const queryClient = useQueryClient();
  const { data: gateways, isLoading } = useQuery({
    queryKey: ['gateways'],
    queryFn: () => api.getGateways(),
  });

  const { data: resources } = useQuery({
    queryKey: ['resources'],
    queryFn: () => api.getResources(),
  });

  const [showNfsModal, setShowNfsModal] = useState(false);
  const [showIscsiModal, setShowIscsiModal] = useState(false);
  const [showNvmeModal, setShowNvmeModal] = useState(false);
  const [showDetailsModal, setShowDetailsModal] = useState(false);
  const [selectedGateway, setSelectedGateway] = useState<any>(null);

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.deleteGateway(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['gateways'] });
    },
    onError: (error) => {
      alert(`Failed to delete gateway: ${error.message}`);
    },
  });

  const startMutation = useMutation({
    mutationFn: (id: string) => api.startGateway(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['gateways'] });
    },
    onError: (error) => {
      alert(`Failed to start gateway: ${error.message}`);
    },
  });

  const stopMutation = useMutation({
    mutationFn: (id: string) => api.stopGateway(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['gateways'] });
    },
    onError: (error) => {
      alert(`Failed to stop gateway: ${error.message}`);
    },
  });

  const handleDelete = (id: string, name: string) => {
    if (confirm(`Are you sure you want to delete gateway "${name}"?`)) {
      deleteMutation.mutate(id);
    }
  };

  const handleShowDetails = async (gateway: any) => {
    try {
      const data = await api.getGateway(gateway.id);
      setSelectedGateway(data.gateway);
      setShowDetailsModal(true);
    } catch (error: any) {
      alert(`Failed to get gateway details: ${error.message}`);
    }
  };

  if (isLoading) {
    return <div className="text-center py-12">Loading...</div>;
  }

  return (
    <div className="space-y-6">
      <div className="flex justify-between items-center">
        <h3 className="text-lg font-semibold">Storage Gateways</h3>
        <div className="flex gap-2">
          <button
            onClick={() => setShowNfsModal(true)}
            className="btn btn-secondary flex items-center gap-2"
          >
            <MdAdd className="h-4 w-4" />
            NFS Gateway
          </button>
          <button
            onClick={() => setShowIscsiModal(true)}
            className="btn btn-secondary flex items-center gap-2"
          >
            <MdAdd className="h-4 w-4" />
            iSCSI Gateway
          </button>
          <button
            onClick={() => setShowNvmeModal(true)}
            className="btn btn-secondary flex items-center gap-2"
          >
            <MdAdd className="h-4 w-4" />
            NVMe Gateway
          </button>
        </div>
      </div>

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
              <GatewayRow
                key={gateway.id}
                gateway={gateway}
                onDelete={handleDelete}
                onStart={startMutation.mutate}
                onStop={stopMutation.mutate}
                onShowDetails={handleShowDetails}
                isDeleting={deleteMutation.isPending}
                isStarting={startMutation.isPending}
                isStopping={stopMutation.isPending}
              />
            ))}
          </tbody>
        </table>

        {!gateways?.gateways.length && (
          <div className="text-center py-12 text-gray-500">
            No gateways found. Create a gateway to expose your storage.
          </div>
        )}
      </div>

      {/* NFS Gateway Modal */}
      {showNfsModal && (
        <Modal onClose={() => setShowNfsModal(false)} title="Create NFS Gateway">
          <CreateNFSForm
            resources={resources?.resources ?? []}
            onSuccess={() => {
              setShowNfsModal(false);
              queryClient.invalidateQueries({ queryKey: ['gateways'] });
            }}
            onCancel={() => setShowNfsModal(false)}
          />
        </Modal>
      )}

      {/* iSCSI Gateway Modal */}
      {showIscsiModal && (
        <Modal onClose={() => setShowIscsiModal(false)} title="Create iSCSI Gateway">
          <CreateISCSIForm
            resources={resources?.resources ?? []}
            onSuccess={() => {
              setShowIscsiModal(false);
              queryClient.invalidateQueries({ queryKey: ['gateways'] });
            }}
            onCancel={() => setShowIscsiModal(false)}
          />
        </Modal>
      )}

      {/* NVMe Gateway Modal */}
      {showNvmeModal && (
        <Modal onClose={() => setShowNvmeModal(false)} title="Create NVMe Gateway">
          <CreateNVMeForm
            resources={resources?.resources ?? []}
            onSuccess={() => {
              setShowNvmeModal(false);
              queryClient.invalidateQueries({ queryKey: ['gateways'] });
            }}
            onCancel={() => setShowNvmeModal(false)}
          />
        </Modal>
      )}

      {/* Details Modal */}
      {showDetailsModal && selectedGateway && (
        <Modal onClose={() => setShowDetailsModal(false)} title="Gateway Details">
          <GatewayDetails
            gateway={selectedGateway}
            onClose={() => setShowDetailsModal(false)}
          />
        </Modal>
      )}
    </div>
  );
}

interface GatewayRowProps {
  gateway: {
    id: string;
    name: string;
    type: string;
    state: string;
    node: string;
    resource: string;
    volumeId: number;
    path: string;
  };
  onDelete: (id: string, name: string) => void;
  onStart: (id: string) => void;
  onStop: (id: string) => void;
  onShowDetails: (gateway: any) => void;
  isDeleting: boolean;
  isStarting: boolean;
  isStopping: boolean;
}

function GatewayRow({ gateway, onDelete, onStart, onStop, onShowDetails, isDeleting, isStarting, isStopping }: GatewayRowProps) {
  const isRunning = gateway.state === 'running';
  const isLoading = isDeleting || isStarting || isStopping;

  const getTypeColor = (type: string) => {
    switch (type.toLowerCase()) {
      case 'nfs': return 'bg-green-100 text-green-700';
      case 'iscsi': return 'bg-blue-100 text-blue-700';
      case 'nvme': return 'bg-purple-100 text-purple-700';
      default: return 'bg-gray-100 text-gray-700';
    }
  };

  return (
    <tr className="border-b border-gray-100 hover:bg-gray-50">
      <td className="py-3 px-4">
        <div className="flex items-center gap-2">
          <div className={clsx(
            'h-8 w-8 rounded flex items-center justify-center',
            gateway.type.toLowerCase() === 'nfs' && 'bg-green-100',
            gateway.type.toLowerCase() === 'iscsi' && 'bg-blue-100',
            gateway.type.toLowerCase() === 'nvme' && 'bg-purple-100'
          )}>
            <MdHub className={clsx(
              'h-4 w-4',
              gateway.type.toLowerCase() === 'nfs' && 'text-green-600',
              gateway.type.toLowerCase() === 'iscsi' && 'text-blue-600',
              gateway.type.toLowerCase() === 'nvme' && 'text-purple-600'
            )} />
          </div>
          <span className="font-medium">{gateway.name || gateway.id}</span>
        </div>
      </td>
      <td className="py-3 px-4">
        <span className={clsx('inline-flex items-center px-2 py-1 rounded text-xs font-medium', getTypeColor(gateway.type))}>
          {gateway.type.toUpperCase()}
        </span>
      </td>
      <td className="py-3 px-4 text-sm text-gray-500">{gateway.resource}</td>
      <td className="py-3 px-4">
        <span className={clsx(
          'inline-flex items-center px-2 py-1 rounded-full text-xs font-medium',
          isRunning ? 'bg-green-100 text-green-700' : 'bg-gray-100 text-gray-700',
        )}>
          {gateway.state || 'Unknown'}
        </span>
      </td>
      <td className="py-3 px-4 text-sm text-gray-500">{gateway.node || '-'}</td>
      <td className="py-3 px-4">
        <div className="flex justify-end gap-2">
          <button
            onClick={() => onShowDetails(gateway)}
            className="btn btn-secondary text-xs py-1 px-2 flex items-center gap-1"
          >
            <MdInfo className="h-3 w-3" />
            Details
          </button>
          {isRunning ? (
            <button
              onClick={() => onStop(gateway.id)}
              disabled={isStopping}
              className="btn btn-warning text-xs py-1 px-2 flex items-center gap-1"
            >
              <MdStop className="h-3 w-3" />
              Stop
            </button>
          ) : (
            <button
              onClick={() => onStart(gateway.id)}
              disabled={isStarting}
              className="btn btn-secondary text-xs py-1 px-2 flex items-center gap-1"
            >
              <MdPlayArrow className="h-3 w-3" />
              Start
            </button>
          )}
          <button
            onClick={() => onDelete(gateway.id, gateway.name || gateway.id)}
            disabled={isDeleting}
            className="btn btn-danger text-xs py-1 px-2 flex items-center gap-1"
          >
            <MdDelete className="h-3 w-3" />
            Delete
          </button>
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

interface CreateNFSFormProps {
  resources: Array<{ name: string; nodes: string[] }>;
  onSuccess: () => void;
  onCancel: () => void;
}

function CreateNFSForm({ resources, onSuccess, onCancel }: CreateNFSFormProps) {
  const [resource, setResource] = useState('');
  const [serviceIp, setServiceIp] = useState('');
  const [exportPath, setExportPath] = useState('');
  const [allowedIps, setAllowedIps] = useState('');
  const [fsType, setFsType] = useState('ext4');

  const createMutation = useMutation({
    mutationFn: () => api.createNFSGateway({
      resource,
      serviceIp,
      exportPath,
      allowedIps: allowedIps ? allowedIps.split(',').map(s => s.trim()) : undefined,
      fsType,
    }),
    onSuccess: () => onSuccess(),
    onError: (error) => alert(`Failed to create NFS gateway: ${error.message}`),
  });

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    createMutation.mutate();
  };

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">DRBD Resource</label>
        <select
          value={resource}
          onChange={(e) => setResource(e.target.value)}
          className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
          required
        >
          <option value="">Select a resource...</option>
          {resources.map((r) => (
            <option key={r.name} value={r.name}>{r.name}</option>
          ))}
        </select>
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">Service IP (CIDR)</label>
        <input
          type="text"
          value={serviceIp}
          onChange={(e) => setServiceIp(e.target.value)}
          className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
          placeholder="e.g., 192.168.1.200/24"
          required
        />
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">Export Path</label>
        <input
          type="text"
          value={exportPath}
          onChange={(e) => setExportPath(e.target.value)}
          className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
          placeholder="e.g., /data"
          required
        />
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">Allowed IPs (comma-separated, optional)</label>
        <input
          type="text"
          value={allowedIps}
          onChange={(e) => setAllowedIps(e.target.value)}
          className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
          placeholder="e.g., 192.168.1.0/24, 10.0.0.0/8"
        />
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">Filesystem Type</label>
        <select
          value={fsType}
          onChange={(e) => setFsType(e.target.value)}
          className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
        >
          <option value="ext4">ext4</option>
          <option value="xfs">XFS</option>
        </select>
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

interface CreateISCSIFormProps {
  resources: Array<{ name: string; nodes: string[] }>;
  onSuccess: () => void;
  onCancel: () => void;
}

function CreateISCSIForm({ resources, onSuccess, onCancel }: CreateISCSIFormProps) {
  const [resource, setResource] = useState('');
  const [serviceIp, setServiceIp] = useState('');
  const [iqn, setIqn] = useState('');
  const [allowedInitiators, setAllowedInitiators] = useState('');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [implementation, setImplementation] = useState('lio');

  const createMutation = useMutation({
    mutationFn: () => api.createISCSIGateway({
      resource,
      serviceIp,
      iqn,
      allowedInitiators: allowedInitiators ? allowedInitiators.split(',').map(s => s.trim()) : undefined,
      username: username || undefined,
      password: password || undefined,
      implementation,
    }),
    onSuccess: () => onSuccess(),
    onError: (error) => alert(`Failed to create iSCSI gateway: ${error.message}`),
  });

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    createMutation.mutate();
  };

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">DRBD Resource</label>
        <select
          value={resource}
          onChange={(e) => setResource(e.target.value)}
          className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
          required
        >
          <option value="">Select a resource...</option>
          {resources.map((r) => (
            <option key={r.name} value={r.name}>{r.name}</option>
          ))}
        </select>
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">Service IP (CIDR)</label>
        <input
          type="text"
          value={serviceIp}
          onChange={(e) => setServiceIp(e.target.value)}
          className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
          placeholder="e.g., 192.168.1.100/24"
          required
        />
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">IQN (iSCSI Qualified Name)</label>
        <input
          type="text"
          value={iqn}
          onChange={(e) => setIqn(e.target.value)}
          className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
          placeholder="e.g., iqn.2024-01.com.example:sds.data"
          required
        />
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">Allowed Initiators (comma-separated, optional)</label>
        <input
          type="text"
          value={allowedInitiators}
          onChange={(e) => setAllowedInitiators(e.target.value)}
          className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
          placeholder="e.g., iqn.1994-05.com.redhat:..."
        />
      </div>

      <div className="grid grid-cols-2 gap-4">
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">CHAP Username (optional)</label>
          <input
            type="text"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
          />
        </div>
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">CHAP Password (optional)</label>
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
          />
        </div>
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">Implementation</label>
        <select
          value={implementation}
          onChange={(e) => setImplementation(e.target.value)}
          className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
        >
          <option value="lio">LIO (Linux IO)</option>
          <option value="tgt">TGT</option>
          <option value="iet">IET</option>
        </select>
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

interface CreateNVMeFormProps {
  resources: Array<{ name: string; nodes: string[] }>;
  onSuccess: () => void;
  onCancel: () => void;
}

function CreateNVMeForm({ resources, onSuccess, onCancel }: CreateNVMeFormProps) {
  const [resource, setResource] = useState('');
  const [serviceIp, setServiceIp] = useState('');
  const [nqn, setNqn] = useState('');
  const [transportType, setTransportType] = useState('tcp');

  const createMutation = useMutation({
    mutationFn: () => api.createNVMeGateway({
      resource,
      serviceIp,
      nqn,
      transportType,
    }),
    onSuccess: () => onSuccess(),
    onError: (error) => alert(`Failed to create NVMe gateway: ${error.message}`),
  });

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    createMutation.mutate();
  };

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">DRBD Resource</label>
        <select
          value={resource}
          onChange={(e) => setResource(e.target.value)}
          className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
          required
        >
          <option value="">Select a resource...</option>
          {resources.map((r) => (
            <option key={r.name} value={r.name}>{r.name}</option>
          ))}
        </select>
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">Service IP (CIDR)</label>
        <input
          type="text"
          value={serviceIp}
          onChange={(e) => setServiceIp(e.target.value)}
          className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
          placeholder="e.g., 192.168.1.150/24"
          required
        />
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">NQN (NVMe Qualified Name)</label>
        <input
          type="text"
          value={nqn}
          onChange={(e) => setNqn(e.target.value)}
          className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
          placeholder="e.g., nqn.2024-01.com.example:sds.data"
          required
        />
      </div>

      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">Transport Type</label>
        <select
          value={transportType}
          onChange={(e) => setTransportType(e.target.value)}
          className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
        >
          <option value="tcp">TCP</option>
          <option value="rdma">RDMA</option>
        </select>
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

interface GatewayDetailsProps {
  gateway: any;
  onClose: () => void;
}

function GatewayDetails({ gateway, onClose }: GatewayDetailsProps) {
  const details = [
    { label: 'ID', value: gateway.id },
    { label: 'Name', value: gateway.name },
    { label: 'Type', value: gateway.type?.toUpperCase() },
    { label: 'State', value: gateway.state },
    { label: 'Resource', value: gateway.resource },
    { label: 'Volume ID', value: gateway.volumeId },
    { label: 'Node', value: gateway.node || '-' },
    { label: 'Path', value: gateway.path || '-' },
  ];

  return (
    <div className="space-y-4">
      <div className="space-y-2">
        {details.map((detail) => (
          <div key={detail.label} className="flex justify-between py-2 border-b border-gray-100">
            <span className="text-gray-500">{detail.label}</span>
            <span className="font-medium">{detail.value || '-'}</span>
          </div>
        ))}
      </div>

      {gateway.options && Object.keys(gateway.options).length > 0 && (
        <div>
          <h4 className="text-sm font-medium text-gray-700 mb-2">Options</h4>
          <div className="space-y-1">
            {Object.entries(gateway.options).map(([key, value]) => (
              <div key={key} className="flex justify-between py-1 bg-gray-50 px-2 rounded text-sm">
                <span className="text-gray-500">{key}</span>
                <span className="font-mono text-xs">{String(value)}</span>
              </div>
            ))}
          </div>
        </div>
      )}

      <button onClick={onClose} className="btn btn-primary w-full">Close</button>
    </div>
  );
}

