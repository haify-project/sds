import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '../services/api';
import { clsx } from 'clsx';
import {
  MdHealthAndSafety,
  MdAdd,
  MdDelete,
  MdRefresh,
  MdExitToApp,
  MdInfo,
  MdClose,
} from 'react-icons/md';

export function HAPage() {
  const queryClient = useQueryClient();
  const { data: haConfigs, isLoading } = useQuery({
    queryKey: ['ha'],
    queryFn: () => api.getHaConfigs(),
  });

  const { data: resources } = useQuery({
    queryKey: ['resources'],
    queryFn: () => api.getResources(),
  });

  const [showCreateModal, setShowCreateModal] = useState(false);
  const [showDetailsModal, setShowDetailsModal] = useState(false);
  const [selectedHa, setSelectedHa] = useState<any>(null);

  const evictMutation = useMutation({
    mutationFn: (resource: string) => api.evictHa(resource),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['ha'] });
      queryClient.invalidateQueries({ queryKey: ['resources'] });
      alert('Eviction initiated successfully');
    },
    onError: (error) => {
      alert(`Failed to evict: ${error.message}`);
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (resource: string) => api.deleteHa(resource),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['ha'] });
    },
    onError: (error) => {
      alert(`Failed to delete HA config: ${error.message}`);
    },
  });

  const handleEvict = (resource: string) => {
    if (confirm(`Evict HA resource "${resource}"? This will trigger failover to another node.`)) {
      evictMutation.mutate(resource);
    }
  };

  const handleDelete = (resource: string) => {
    if (confirm(`Delete HA configuration for "${resource}"?`)) {
      deleteMutation.mutate(resource);
    }
  };

  const handleShowDetails = async (resource: string) => {
    try {
      const data = await api.getHaConfig(resource);
      setSelectedHa(data.config);
      setShowDetailsModal(true);
    } catch (error: any) {
      alert(`Failed to get HA details: ${error.message}`);
    }
  };

  if (isLoading) {
    return <div className="text-center py-12">Loading...</div>;
  }

  const resourceMap = new Map(
    resources?.resources.map(r => [r.name, r] as [string, typeof r]) ?? []
  );

  // Find resources without HA config
  const resourcesWithoutHA = resources?.resources.filter(
    r => !haConfigs?.configs.some(ha => ha.resource === r.name)
  ) ?? [];

  return (
    <div className="space-y-6">
      <div className="flex justify-between items-center">
        <h3 className="text-lg font-semibold">HA Configurations</h3>
        <button
          onClick={() => setShowCreateModal(true)}
          className="btn btn-primary flex items-center gap-2"
          disabled={resourcesWithoutHA.length === 0}
        >
          <MdAdd className="h-4 w-4" />
          Create HA Config
        </button>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        {haConfigs?.configs.map((config) => (
          <HAConfigCard
            key={config.resource}
            config={config}
            resource={resourceMap.get(config.resource)}
            onEvict={handleEvict}
            onDelete={handleDelete}
            onShowDetails={handleShowDetails}
            isEvicting={evictMutation.isPending}
            isDeleting={deleteMutation.isPending}
          />
        ))}
      </div>

      {(!haConfigs?.configs || haConfigs.configs.length === 0) && (
        <div className="text-center py-12 text-gray-500">
          No HA configurations found. Create a resource first, then configure HA.
        </div>
      )}

      {/* Create HA Modal */}
      {showCreateModal && (
        <Modal onClose={() => setShowCreateModal(false)} title="Create HA Configuration">
          <CreateHAForm
            resources={resourcesWithoutHA}
            onSuccess={() => {
              setShowCreateModal(false);
              queryClient.invalidateQueries({ queryKey: ['ha'] });
            }}
            onCancel={() => setShowCreateModal(false)}
          />
        </Modal>
      )}

      {/* Details Modal */}
      {showDetailsModal && selectedHa && (
        <Modal onClose={() => setShowDetailsModal(false)} title="HA Configuration Details">
          <HADetails config={selectedHa} onClose={() => setShowDetailsModal(false)} />
        </Modal>
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
    nodeStates?: Record<string, { role: string; diskState: string; replication: string }>;
  };
  onEvict: (resource: string) => void;
  onDelete: (resource: string) => void;
  onShowDetails: (resource: string) => void;
  isEvicting: boolean;
  isDeleting: boolean;
}

function HAConfigCard({ config, resource, onEvict, onDelete, onShowDetails, isEvicting, isDeleting }: HAConfigCardProps) {
  const isRunning = resource?.role === 'Primary';
  const primaryNode = resource?.nodes.find(n => resource.nodeStates?.[n]?.role === 'Primary');

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
        <InfoRow label="Mount Point" value={config.mountPoint || '-'} />
        <InfoRow label="Filesystem" value={config.fsType || '-'} />
        <InfoRow label="Primary Node" value={primaryNode || '-'} />
        <InfoRow
          label="Services"
          value={config.services?.length > 0 ? (
            <div className="flex flex-wrap gap-1">
              {config.services.map(s => (
                <span key={s} className="inline-flex items-center px-2 py-0.5 bg-blue-100 text-blue-700 rounded text-xs">
                  {s}
                </span>
              ))}
            </div>
          ) : '-'}
        />
      </div>

      <div className="mt-4 pt-4 border-t border-gray-100 flex gap-2">
        <button
          onClick={() => onShowDetails(config.resource)}
          className="btn btn-secondary text-xs flex items-center gap-1"
        >
          <MdInfo className="h-3 w-3" />
          Details
        </button>
        <button
          onClick={() => onEvict(config.resource)}
          disabled={isEvicting || !isRunning}
          className={clsx(
            'btn btn-warning text-xs flex items-center gap-1',
            (!isRunning || isEvicting) && 'opacity-50 cursor-not-allowed'
          )}
        >
          {isEvicting ? <MdRefresh className="h-3 w-3 animate-spin" /> : <MdExitToApp className="h-3 w-3" />}
          Evict
        </button>
        <button
          onClick={() => onDelete(config.resource)}
          disabled={isDeleting}
          className="btn btn-danger text-xs flex items-center gap-1"
        >
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

interface CreateHAFormProps {
  resources: Array<{ name: string; nodes: string[] }>;
  onSuccess: () => void;
  onCancel: () => void;
}

function CreateHAForm({ resources, onSuccess, onCancel }: CreateHAFormProps) {
  const [resource, setResource] = useState('');
  const [vip, setVip] = useState('');
  const [mountPoint, setMountPoint] = useState('');
  const [fsType, setFsType] = useState('ext4');
  const [services, setServices] = useState('');

  const createMutation = useMutation({
    mutationFn: () => api.makeHa(resource, {
      vip,
      mountPoint: mountPoint || undefined,
      fstype: mountPoint ? fsType : undefined,
      services: services ? services.split(',').map(s => s.trim()).filter(s => s) : undefined,
    }),
    onSuccess: () => onSuccess(),
    onError: (error) => alert(`Failed to create HA config: ${error.message}`),
  });

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    createMutation.mutate();
  };

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      {resources.length === 0 ? (
        <div className="text-center py-4 text-gray-500">
          No resources available for HA configuration.
          <br />
          All resources already have HA configured.
        </div>
      ) : (
        <>
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
                <option key={r.name} value={r.name}>{r.name} ({r.nodes.join(', ')})</option>
              ))}
            </select>
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">Virtual IP (CIDR)</label>
            <input
              type="text"
              value={vip}
              onChange={(e) => setVip(e.target.value)}
              className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
              placeholder="e.g., 192.168.1.100/24"
              required
            />
            <p className="text-xs text-gray-500 mt-1">The VIP that will float between nodes</p>
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">Mount Point (optional)</label>
            <input
              type="text"
              value={mountPoint}
              onChange={(e) => setMountPoint(e.target.value)}
              className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
              placeholder="e.g., /mnt/data"
            />
            <p className="text-xs text-gray-500 mt-1">Path where the DRBD device will be mounted</p>
          </div>

          {mountPoint && (
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
          )}

          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">Services (comma-separated, optional)</label>
            <input
              type="text"
              value={services}
              onChange={(e) => setServices(e.target.value)}
              className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
              placeholder="e.g., mysql.service, nginx.service"
            />
            <p className="text-xs text-gray-500 mt-1">Systemd services to start/stop with the resource</p>
          </div>

          <div className="flex gap-2 pt-2">
            <button
              type="button"
              onClick={onCancel}
              disabled={createMutation.isPending}
              className="btn btn-secondary flex-1"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={createMutation.isPending}
              className="btn btn-primary flex-1"
            >
              {createMutation.isPending ? 'Creating...' : 'Create'}
            </button>
          </div>
        </>
      )}
    </form>
  );
}

interface HADetailsProps {
  config: any;
  onClose: () => void;
}

function HADetails({ config, onClose }: HADetailsProps) {
  const details = [
    { label: 'Resource', value: config.resource },
    { label: 'Virtual IP', value: config.vip },
    { label: 'Mount Point', value: config.mountPoint || '-' },
    { label: 'Filesystem', value: config.fsType || '-' },
  ];

  return (
    <div className="space-y-4">
      <div className="space-y-2">
        {details.map((detail) => (
          <div key={detail.label} className="flex justify-between py-2 border-b border-gray-100">
            <span className="text-gray-500">{detail.label}</span>
            <span className="font-medium">{detail.value}</span>
          </div>
        ))}
      </div>

      {config.services && config.services.length > 0 && (
        <div>
          <h4 className="text-sm font-medium text-gray-700 mb-2">Services</h4>
          <div className="space-y-1">
            {config.services.map((service: string) => (
              <div key={service} className="flex items-center gap-2 py-1 bg-gray-50 px-2 rounded text-sm">
                <span className="text-gray-700">{service}</span>
              </div>
            ))}
          </div>
        </div>
      )}

      <button onClick={onClose} className="btn btn-primary w-full">Close</button>
    </div>
  );
}

