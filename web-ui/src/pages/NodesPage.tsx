import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api, HealthInfo } from '../services/api';
import { clsx } from 'clsx';
import {
  MdComputer,
  MdCheckCircle,
  MdCancel,
  MdFavorite,
  MdInfo,
  MdClose,
  MdAdd,
  MdRefresh
} from 'react-icons/md';

export function NodesPage() {
  const queryClient = useQueryClient();
  const { data: nodes, isLoading } = useQuery({
    queryKey: ['nodes'],
    queryFn: () => api.getNodes(),
  });

  const [showRegisterModal, setShowRegisterModal] = useState(false);
  const [selectedNode, setSelectedNode] = useState<string | null>(null);
  const [healthData, setHealthData] = useState<HealthInfo | null>(null);
  const [showDetailsModal, setShowDetailsModal] = useState(false);
  const [detailsNode, setDetailsNode] = useState<any>(null);

  const healthCheckMutation = useMutation({
    mutationFn: (nodeName: string) => api.healthCheck(nodeName),
    onSuccess: (data) => {
      setHealthData(data.health);
      setSelectedNode(null);
    },
    onError: (error) => {
      alert(`Health check failed: ${error.message}`);
      setSelectedNode(null);
    },
  });

  const handleHealthCheck = (nodeName: string) => {
    setSelectedNode(nodeName);
    healthCheckMutation.mutate(nodeName);
  };

  const handleShowDetails = (node: any) => {
    setDetailsNode(node);
    setShowDetailsModal(true);
  };

  const closeHealthModal = () => {
    setHealthData(null);
    setSelectedNode(null);
  };

  if (isLoading) {
    return <div className="text-center py-12">Loading...</div>;
  }

  return (
    <div className="space-y-6">
      <div className="flex justify-between items-center">
        <h3 className="text-lg font-semibold">Storage Nodes</h3>
        <button
          onClick={() => setShowRegisterModal(true)}
          className="btn btn-primary flex items-center gap-2"
        >
          <MdAdd className="h-4 w-4" />
          Register Node
        </button>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 xl:grid-cols-3 gap-6">
        {nodes?.nodes.map((node) => (
          <NodeCard
            key={node.name}
            node={node}
            onHealthCheck={handleHealthCheck}
            onShowDetails={handleShowDetails}
            isLoading={healthCheckMutation.isPending && selectedNode === node.name}
          />
        ))}
      </div>

      {/* Register Node Modal */}
      {showRegisterModal && (
        <Modal onClose={() => setShowRegisterModal(false)} title="Register Node">
          <RegisterNodeForm
            onSuccess={() => {
              setShowRegisterModal(false);
              queryClient.invalidateQueries({ queryKey: ['nodes'] });
            }}
            onCancel={() => setShowRegisterModal(false)}
          />
        </Modal>
      )}

      {/* Health Check Modal */}
      {healthData && (
        <Modal onClose={closeHealthModal} title="Health Check Results">
          <HealthResult health={healthData} onClose={closeHealthModal} />
        </Modal>
      )}

      {/* Node Details Modal */}
      {showDetailsModal && detailsNode && (
        <Modal onClose={() => setShowDetailsModal(false)} title="Node Details">
          <NodeDetails node={detailsNode} onClose={() => setShowDetailsModal(false)} />
        </Modal>
      )}
    </div>
  );
}

interface NodeCardProps {
  node: {
    name: string;
    address: string;
    hostname: string;
    state: string;
    lastSeen: string;
    version: string;
  };
  onHealthCheck: (name: string) => void;
  onShowDetails: (node: any) => void;
  isLoading: boolean;
}

function NodeCard({ node, onHealthCheck, onShowDetails, isLoading }: NodeCardProps) {
  const isOnline = node.state === 'online';

  return (
    <div className="card">
      <div className="flex items-start justify-between mb-4">
        <div className="flex items-center gap-3">
          <div className={clsx(
            'h-10 w-10 rounded-full flex items-center justify-center',
            isOnline ? 'bg-green-100' : 'bg-red-100'
          )}>
            <MdComputer className={clsx(
              'h-5 w-5',
              isOnline ? 'text-green-600' : 'text-red-600'
            )} />
          </div>
          <div>
            <h4 className="font-semibold text-gray-900">{node.name}</h4>
            <p className="text-sm text-gray-500">{node.hostname}</p>
          </div>
        </div>
        <span
          className={clsx(
            'inline-flex items-center gap-1 px-2 py-1 rounded-full text-xs font-medium',
            isOnline
              ? 'bg-green-100 text-green-800'
              : 'bg-red-100 text-red-800'
          )}
        >
          {isOnline ? <MdCheckCircle className="h-3 w-3" /> : <MdCancel className="h-3 w-3" />}
          {isOnline ? 'Online' : 'Offline'}
        </span>
      </div>

      <div className="space-y-2 text-sm">
        <div className="flex justify-between">
          <span className="text-gray-500">Address</span>
          <span className="text-gray-900">{node.address}</span>
        </div>
        <div className="flex justify-between">
          <span className="text-gray-500">Version</span>
          <span className="text-gray-900">{node.version}</span>
        </div>
        <div className="flex justify-between">
          <span className="text-gray-500">Last Seen</span>
          <span className="text-gray-900">
            {new Date(Number(node.lastSeen) * 1000).toLocaleString()}
          </span>
        </div>
      </div>

      <div className="mt-4 pt-4 border-t border-gray-100 flex gap-2">
        <button
          onClick={() => onHealthCheck(node.name)}
          disabled={isLoading || !isOnline}
          className={clsx(
            'btn btn-secondary flex-1 text-xs flex items-center justify-center gap-1',
            (isLoading || !isOnline) && 'opacity-50 cursor-not-allowed'
          )}
        >
          {isLoading ? <MdRefresh className="h-3 w-3 animate-spin" /> : <MdFavorite className="h-3 w-3" />}
          Health Check
        </button>
        <button
          onClick={() => onShowDetails(node)}
          className="btn btn-secondary text-xs flex items-center justify-center gap-1"
        >
          <MdInfo className="h-3 w-3" />
          Details
        </button>
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

interface RegisterNodeFormProps {
  onSuccess: () => void;
  onCancel: () => void;
}

function RegisterNodeForm({ onSuccess, onCancel }: RegisterNodeFormProps) {
  const [nodeName, setNodeName] = useState('');
  const [nodeAddress, setNodeAddress] = useState('');

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    // TODO: Implement registration API call
    alert(`Register node: ${nodeName} at ${nodeAddress}`);
    onSuccess();
  };

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">
          Node Name
        </label>
        <input
          type="text"
          value={nodeName}
          onChange={(e) => setNodeName(e.target.value)}
          className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
          placeholder="e.g., orange1"
          required
        />
      </div>
      <div>
        <label className="block text-sm font-medium text-gray-700 mb-1">
          Node Address
        </label>
        <input
          type="text"
          value={nodeAddress}
          onChange={(e) => setNodeAddress(e.target.value)}
          className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-primary-500"
          placeholder="e.g., 192.168.1.100"
          required
        />
      </div>
      <div className="flex gap-2 pt-2">
        <button
          type="button"
          onClick={onCancel}
          className="btn btn-secondary flex-1"
        >
          Cancel
        </button>
        <button
          type="submit"
          className="btn btn-primary flex-1"
        >
          Register
        </button>
      </div>
    </form>
  );
}

interface HealthResultProps {
  health: HealthInfo;
  onClose: () => void;
}

function HealthResult({ health, onClose }: HealthResultProps) {
  const checks = [
    { name: 'DRBD', installed: health.drbdInstalled, version: health.drbdVersion },
    { name: 'DRBD Reactor', installed: health.drbdReactorInstalled, version: health.drbdReactorVersion, running: health.drbdReactorRunning },
    { name: 'Resource Agents', installed: health.resourceAgentsInstalled },
  ];

  return (
    <div className="space-y-4">
      <div className="space-y-2">
        {checks.map((check) => (
          <div key={check.name} className="flex items-center justify-between p-3 bg-gray-50 rounded-lg">
            <div className="flex items-center gap-3">
              {check.installed ? (
                <MdCheckCircle className="h-5 w-5 text-green-500" />
              ) : (
                <MdCancel className="h-5 w-5 text-red-500" />
              )}
              <div>
                <p className="font-medium">{check.name}</p>
                {check.version && (
                  <p className="text-sm text-gray-500">Version: {check.version}</p>
                )}
              </div>
            </div>
            {check.running !== undefined && (
              <span className={clsx(
                'px-2 py-1 rounded text-xs font-medium',
                check.running ? 'bg-green-100 text-green-700' : 'bg-yellow-100 text-yellow-700'
              )}>
                {check.running ? 'Running' : 'Stopped'}
              </span>
            )}
          </div>
        ))}
      </div>

      {health.availableAgents && health.availableAgents.length > 0 && (
        <div>
          <p className="text-sm font-medium text-gray-700 mb-2">Available OCF Agents:</p>
          <div className="flex flex-wrap gap-1">
            {health.availableAgents.map((agent) => (
              <span key={agent} className="px-2 py-1 bg-blue-100 text-blue-700 rounded text-xs">
                {agent}
              </span>
            ))}
          </div>
        </div>
      )}

      <button onClick={onClose} className="btn btn-primary w-full">
        Close
      </button>
    </div>
  );
}

interface NodeDetailsProps {
  node: any;
  onClose: () => void;
}

function NodeDetails({ node, onClose }: NodeDetailsProps) {
  const details = [
    { label: 'Name', value: node.name },
    { label: 'Address', value: node.address },
    { label: 'Hostname', value: node.hostname },
    { label: 'State', value: node.state },
    { label: 'Version', value: node.version },
    {
      label: 'Last Seen',
      value: new Date(Number(node.lastSeen) * 1000).toLocaleString()
    },
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
      <button onClick={onClose} className="btn btn-primary w-full">
        Close
      </button>
    </div>
  );
}

function clsx(...classes: (string | boolean | undefined)[]) {
  return classes.filter(Boolean).join(' ');
}
