// API base URL - use relative path for embedded UI
const API_BASE = window.location.hostname === 'localhost' || window.location.hostname === '127.0.0.1'
  ? 'http://orange1:3375/v1'
  : `${window.location.protocol}//${window.location.hostname}:3375/v1`;

export interface ApiResponse<T = unknown> {
  success: boolean;
  message: string;
}

export interface Node {
  name: string;
  address: string;
  hostname: string;
  state: string;
  lastSeen: string;
  version: string;
}

export interface Pool {
  name: string;
  type: string;
  node: string;
  totalGb: string;
  freeGb: string;
  devices: string[];
  thin: boolean;
  compression: string;
}

export interface Resource {
  name: string;
  port: number;
  protocol: string;
  nodes: string[];
  role: string;
  volumes: Volume[];
  nodeStates: Record<string, NodeState>;
  disklessNodes?: string[];
  quorumRisk?: boolean;
}

export interface GFSRetention {
  hourly?: number;
  daily?: number;
  weekly?: number;
  monthly?: number;
  yearly?: number;
}

export interface SnapshotSchedule {
  name: string;
  resource: string;
  cron: string;
  enabled: boolean;
  keep?: GFSRetention;
  lastRun?: string;
  nextRun?: string;
}

export interface SnapshotSchedulesResponse extends ApiResponse {
  schedules: SnapshotSchedule[];
}

export interface Volume {
  volumeId: number;
  device: string;
  sizeGb: number;
  // Backing location, used to build the "<pool>/<lv>" path snapshot APIs
  // operate on.
  pool?: string;
  backingVolume?: string;
}

export interface NodeState {
  role: string;
  diskState: string;
  replication: string;
}

export interface Gateway {
  id: string;
  name: string;
  type: string;
  state: string;
  node: string;
  resource: string;
  volumeId: number;
  path: string;
  options: Record<string, unknown>;
}

export interface HaConfig {
  resource: string;
  vip: string;
  mountPoint: string;
  fsType: string;
  services: string[];
}

export interface ResourceStatus {
  name: string;
  role: string;
  nodes: string[];
  nodeStates: Record<string, NodeResourceState>;
  volumes: Volume[];
}

export interface NodeResourceState {
  role: string;
  diskState: string;
  replicationState: string;
}

export interface Snapshot {
  name: string;
  volume: string;
  sizeGb: number;
  createdAt: string;
}

export interface NodesResponse extends ApiResponse {
  nodes: Node[];
}

export interface PoolsResponse extends ApiResponse {
  pools: Pool[];
}

export interface ResourcesResponse extends ApiResponse {
  resources: Resource[];
}

export interface GatewaysResponse extends ApiResponse {
  gateways: Gateway[];
}

export interface HaConfigsResponse extends ApiResponse {
  configs: HaConfig[];
}

export interface SnapshotsResponse extends ApiResponse {
  snapshots: Snapshot[];
}

// API token storage for controllers with [auth] enabled. The token is kept
// in localStorage; on a 401/403 the registered prompt handler (a proper
// dialog mounted by the app shell) asks for a token once and the request is
// retried — no separate login page is needed for this single-admin model.
const TOKEN_STORAGE_KEY = 'sds_api_token';

export function getApiToken(): string {
  return localStorage.getItem(TOKEN_STORAGE_KEY) ?? '';
}

export function setApiToken(token: string): void {
  if (token) {
    localStorage.setItem(TOKEN_STORAGE_KEY, token);
  } else {
    localStorage.removeItem(TOKEN_STORAGE_KEY);
  }
}

// authPromptHandler resolves to true when the user supplied a (new) token
// and the failed request should be retried. The app shell registers a
// dialog-based handler; window.prompt is the headless fallback.
type AuthPromptHandler = () => Promise<boolean>;
let authPromptHandler: AuthPromptHandler | null = null;

export function setAuthPromptHandler(handler: AuthPromptHandler | null): void {
  authPromptHandler = handler;
}

async function promptForToken(): Promise<boolean> {
  if (authPromptHandler) {
    return authPromptHandler();
  }
  const entered = window.prompt(
    'SDS API token required (controller has authentication enabled):',
    getApiToken()
  );
  if (entered === null) return false;
  setApiToken(entered.trim());
  return true;
}

class ApiClient {
  private baseUrl: string;

  constructor(baseUrl: string = API_BASE) {
    this.baseUrl = baseUrl;
  }

  private authHeaders(): Record<string, string> {
    const token = getApiToken();
    return token ? { Authorization: `Bearer ${token}` } : {};
  }

  private async request<T>(
    endpoint: string,
    options?: RequestInit
  ): Promise<T> {
    const url = `${this.baseUrl}${endpoint}`;
    const doFetch = () =>
      fetch(url, {
        headers: {
          'Content-Type': 'application/json',
          ...this.authHeaders(),
          ...options?.headers,
        },
        mode: 'cors',
        ...options,
      });

    let response = await doFetch();

    if (response.status === 401 || response.status === 403) {
      if (await promptForToken()) {
        response = await doFetch();
      }
    }

    if (!response.ok) {
      const errorText = await response.text();
      throw new Error(`API error: ${response.status} ${errorText || response.statusText}`);
    }

    return response.json() as Promise<T>;
  }

  // ==================== Nodes ====================
  getNodes = () => this.request<NodesResponse>('/nodes');

  getNode = (address: string) =>
    this.request<ApiResponse & { node: Node }>(`/nodes/${address}`);

  registerNode = (data: { name: string; address: string }) =>
    this.request<ApiResponse & { node: Node }>('/nodes', {
      method: 'POST',
      body: JSON.stringify(data),
    });

  unregisterNode = (address: string) =>
    this.request<ApiResponse>(`/nodes/${address}`, { method: 'DELETE' });

  healthCheck = (node: string) =>
    this.request<ApiResponse & { health: HealthInfo }>(`/nodes/${node}/health`);

  // ==================== Pools ====================
  getPools = () => this.request<PoolsResponse>('/pools');

  getPool = (name: string) =>
    this.request<ApiResponse & { pool: Pool }>(`/pools/${name}`);

  createPool = (data: {
    name: string;
    type: string;
    node: string;
    disks?: string[];
    sizeGb?: number;
  }) =>
    this.request<ApiResponse>('/pools', {
      method: 'POST',
      body: JSON.stringify(data),
    });

  deletePool = (name: string, node?: string) =>
    this.request<ApiResponse>(`/pools/${name}${node ? `?node=${node}` : ''}`, { method: 'DELETE' });

  // ZFS pools live on a dedicated API: creation takes vdevs (whole devices
  // or partitions), not the LVM disks/VG flow.
  createZFSPool = (data: { name: string; node: string; vdevs: string[] }) =>
    this.request<ApiResponse>('/zfs/pools', {
      method: 'POST',
      body: JSON.stringify(data),
    });

  deleteZFSPool = (name: string, node?: string) =>
    this.request<ApiResponse>(`/zfs/pools/${name}${node ? `?node=${node}` : ''}`, {
      method: 'DELETE',
    });

  addDisk = (pool: string, disk: string, node?: string) =>
    this.request<ApiResponse>(`/pools/${pool}/disks`, {
      method: 'POST',
      body: JSON.stringify({ disk, node }),
    });

  // ==================== Resources ====================
  getResources = () => this.request<ResourcesResponse>('/resources');

  getResource = (name: string) =>
    this.request<ApiResponse & { resource: Resource }>(`/resources/${name}`);

  createResource = (data: {
    name: string;
    port: number;
    nodes: string[];
    protocol?: string;
    sizeGb?: number;
    pool?: string;
    storageType?: string;
  }) =>
    this.request<ApiResponse>('/resources', {
      method: 'POST',
      body: JSON.stringify(data),
    });

  deleteResource = (name: string) =>
    this.request<ApiResponse>(`/resources/${name}`, { method: 'DELETE' });

  resourceStatus = (name: string) =>
    this.request<ApiResponse & { status: ResourceStatus }>(`/resources/${name}/status`);

  updateResourceOptions = (name: string, options: Record<string, string>) =>
    this.request<ApiResponse>(
      `/resources/${encodeURIComponent(name)}/options`,
      { method: 'POST', body: JSON.stringify({ options }) }
    );

  // ==================== Snapshot schedules ====================
  getSnapshotSchedules = () =>
    this.request<SnapshotSchedulesResponse>('/snapshot-schedules');

  createSnapshotSchedule = (data: {
    resource: string;
    cron: string;
    keep: GFSRetention;
    enabled: boolean;
  }) =>
    this.request<ApiResponse>('/snapshot-schedules', {
      method: 'POST',
      body: JSON.stringify(data),
    });

  deleteSnapshotSchedule = (name: string) =>
    this.request<ApiResponse>(
      `/snapshot-schedules/${encodeURIComponent(name)}`,
      { method: 'DELETE' },
    );

  setPrimary = (resource: string, node: string, force = false) =>
    this.request<ApiResponse>(`/resources/${resource}/primary`, {
      method: 'POST',
      body: JSON.stringify({ node, force }),
    });

  setSecondary = (resource: string, node: string) =>
    this.request<ApiResponse>(`/resources/${resource}/secondary`, {
      method: 'POST',
      body: JSON.stringify({ node }),
    });

  // Volume operations
  addVolume = (resource: string, data: { volume: string; pool: string; sizeGb: number }) =>
    this.request<ApiResponse>(`/resources/${resource}/volumes`, {
      method: 'POST',
      body: JSON.stringify(data),
    });

  removeVolume = (resource: string, volumeId: number) =>
    this.request<ApiResponse>(`/resources/${resource}/volumes/${volumeId}`, { method: 'DELETE' });

  resizeVolume = (resource: string, volumeId: number, sizeGb: number) =>
    this.request<ApiResponse>(`/resources/${resource}/volumes/${volumeId}`, {
      method: 'PATCH',
      body: JSON.stringify({ sizeGb }),
    });

  // Filesystem operations
  createFilesystem = (resource: string, volumeId: number, fstype: string, node?: string) =>
    this.request<ApiResponse>(`/resources/${resource}/volumes/${volumeId}/filesystem`, {
      method: 'POST',
      body: JSON.stringify({ fstype, node }),
    });

  mountResource = (resource: string, volumeId: number, path: string, fstype?: string, node?: string) =>
    this.request<ApiResponse>(`/resources/${resource}/volumes/${volumeId}/mount`, {
      method: 'POST',
      body: JSON.stringify({ path, fstype, node }),
    });

  unmountResource = (resource: string, volumeId: number, node?: string) =>
    this.request<ApiResponse>(`/resources/${resource}/volumes/${volumeId}/unmount`, {
      method: 'POST',
      body: JSON.stringify({ node }),
    });

  // ==================== HA ====================
  getHaConfigs = () => this.request<HaConfigsResponse>('/ha');

  getHaConfig = (resource: string) =>
    this.request<ApiResponse & { config: HaConfig }>(`/resources/${resource}/ha`);

  makeHa = (resource: string, data: {
    vip?: string;
    mountPoint?: string;
    fstype?: string;
    services?: string[];
  }) =>
    this.request<ApiResponse & { configPath: string }>(`/resources/${resource}/ha`, {
      method: 'POST',
      body: JSON.stringify(data),
    });

  deleteHa = (resource: string) =>
    this.request<ApiResponse>(`/resources/${resource}/ha`, { method: 'DELETE' });

  evictHa = (resource: string) =>
    this.request<ApiResponse>(`/resources/${resource}/ha/evict`, {
      method: 'POST',
      body: JSON.stringify({}),
    });

  // ==================== Snapshots ====================
  listSnapshots = (volume: string, node?: string) =>
    this.request<SnapshotsResponse>(`/volumes/${volume}/snapshots${node ? `?node=${node}` : ''}`);

  createSnapshot = (volume: string, snapshotName: string, node?: string) =>
    this.request<ApiResponse>(`/volumes/${volume}/snapshots`, {
      method: 'POST',
      body: JSON.stringify({ snapshotName, node }),
    });

  deleteSnapshot = (volume: string, snapshotName: string, node?: string) =>
    this.request<ApiResponse>(`/volumes/${volume}/snapshots/${snapshotName}${node ? `?node=${node}` : ''}`, { method: 'DELETE' });

  restoreSnapshot = (volume: string, snapshotName: string, node?: string) =>
    this.request<ApiResponse>(`/volumes/${volume}/snapshots/${snapshotName}/restore`, {
      method: 'POST',
      body: JSON.stringify({ node }),
    });

  // ==================== Gateways ====================
  getGateways = () => this.request<GatewaysResponse>('/gateways');

  getGateway = (id: string) =>
    this.request<ApiResponse & { gateway: Gateway }>(`/gateways/${id}`);

  // NFS Gateway
  createNFSGateway = (data: {
    resource: string;
    serviceIp: string;
    exportPath: string;
    allowedIps?: string[];
    fsType?: string;
    options?: Record<string, string>;
  }) =>
    this.request<ApiResponse & { configPath: string }>('/gateways/nfs', {
      method: 'POST',
      body: JSON.stringify(data),
    });

  // iSCSI Gateway
  createISCSIGateway = (data: {
    resource: string;
    serviceIp: string;
    iqn: string;
    allowedInitiators?: string[];
    username?: string;
    password?: string;
    implementation?: string;
    options?: Record<string, string>;
  }) =>
    this.request<ApiResponse & { configPath: string }>('/gateways/iscsi', {
      method: 'POST',
      body: JSON.stringify(data),
    });

  // NVMe Gateway
  createNVMeGateway = (data: {
    resource: string;
    serviceIp: string;
    nqn: string;
    transportType?: string;
    options?: Record<string, string>;
  }) =>
    this.request<ApiResponse & { configPath: string }>('/gateways/nvme', {
      method: 'POST',
      body: JSON.stringify(data),
    });

  deleteGateway = (id: string) =>
    this.request<ApiResponse>(`/gateways/${id}`, { method: 'DELETE' });

  startGateway = (id: string) =>
    this.request<ApiResponse>(`/gateways/${id}/start`, { method: 'POST' });

  stopGateway = (id: string) =>
    this.request<ApiResponse>(`/gateways/${id}/stop`, { method: 'POST' });

  // ==================== NFS Export Management ====================
  listNFSExports = (resource: string) =>
    this.request<ApiResponse & { exports: NFSExport[] }>('/gateways/nfs/exports:list', {
      method: 'POST',
      body: JSON.stringify({ resource }),
    });

  addNFSExport = (data: {
    resource: string;
    exportPath: string;
    fsid?: number;
    clientSpec?: string;
    options?: string;
  }) =>
    this.request<ApiResponse>('/gateways/nfs/exports:add', {
      method: 'POST',
      body: JSON.stringify(data),
    });

  removeNFSExport = (resource: string, exportPath: string) =>
    this.request<ApiResponse>('/gateways/nfs/exports:remove', {
      method: 'POST',
      body: JSON.stringify({ resource, exportPath }),
    });

  // ==================== iSCSI LUN / Initiator / CHAP ====================
  listISCSILUNs = (resource: string) =>
    this.request<ApiResponse & { luns: ISCSILUN[] }>('/gateways/iscsi/luns:list', {
      method: 'POST',
      body: JSON.stringify({ resource }),
    });

  addISCSILUN = (data: { resource: string; lun: number; device: string }) =>
    this.request<ApiResponse>('/gateways/iscsi/luns:add', {
      method: 'POST',
      body: JSON.stringify(data),
    });

  removeISCSILUN = (resource: string, lun: number) =>
    this.request<ApiResponse>('/gateways/iscsi/luns:remove', {
      method: 'POST',
      body: JSON.stringify({ resource, lun }),
    });

  listISCSIInitiators = (resource: string) =>
    this.request<ApiResponse & { initiators: string[] }>('/gateways/iscsi/initiators:list', {
      method: 'POST',
      body: JSON.stringify({ resource }),
    });

  addISCSIInitiator = (resource: string, initiator: string) =>
    this.request<ApiResponse>('/gateways/iscsi/initiators:add', {
      method: 'POST',
      body: JSON.stringify({ resource, initiator }),
    });

  removeISCSIInitiator = (resource: string, initiator: string) =>
    this.request<ApiResponse>('/gateways/iscsi/initiators:remove', {
      method: 'POST',
      body: JSON.stringify({ resource, initiator }),
    });

  getISCSIChap = (resource: string) =>
    this.request<ApiResponse & { username: string; password: string; mutual: boolean }>(
      '/gateways/iscsi/chap:get',
      { method: 'POST', body: JSON.stringify({ resource }) }
    );

  setISCSIChap = (data: {
    resource: string;
    username: string;
    password: string;
    mutual?: boolean;
  }) =>
    this.request<ApiResponse>('/gateways/iscsi/chap:set', {
      method: 'POST',
      body: JSON.stringify(data),
    });

  // ==================== NVMe Namespace / Host ====================
  listNVMeNamespaces = (resource: string) =>
    this.request<ApiResponse & { namespaces: NVMeNamespace[] }>('/gateways/nvme/namespaces:list', {
      method: 'POST',
      body: JSON.stringify({ resource }),
    });

  addNVMeNamespace = (resource: string, device: string) =>
    this.request<ApiResponse>('/gateways/nvme/namespaces:add', {
      method: 'POST',
      body: JSON.stringify({ resource, device }),
    });

  removeNVMeNamespace = (resource: string, namespaceId: number) =>
    this.request<ApiResponse>('/gateways/nvme/namespaces:remove', {
      method: 'POST',
      body: JSON.stringify({ resource, namespaceId }),
    });

  listNVMeHosts = (resource: string) =>
    this.request<ApiResponse & { hosts: string[] }>('/gateways/nvme/hosts:list', {
      method: 'POST',
      body: JSON.stringify({ resource }),
    });

  addNVMeHost = (resource: string, hostNqn: string) =>
    this.request<ApiResponse>('/gateways/nvme/hosts:add', {
      method: 'POST',
      body: JSON.stringify({ resource, hostNqn }),
    });

  removeNVMeHost = (resource: string, hostNqn: string) =>
    this.request<ApiResponse>('/gateways/nvme/hosts:remove', {
      method: 'POST',
      body: JSON.stringify({ resource, hostNqn }),
    });

  // ==================== Controller Self-HA ====================
  getSelfHaStatus = () => this.request<SelfHaStatus>('/selfha');

  enableSelfHa = (data: {
    vip: string;
    pool: string;
    sizeGb?: number;
    port?: number;
    nodes?: string[];
  }) =>
    this.request<ApiResponse & { resource: string; handoffLog: string }>('/selfha/enable', {
      method: 'POST',
      body: JSON.stringify(data),
    });

  disableSelfHa = (node: string) =>
    this.request<ApiResponse>('/selfha/disable', {
      method: 'POST',
      body: JSON.stringify({ node }),
    });

  // ==================== RBAC ====================
  getRbacWhoami = () => this.request<RbacWhoami>('/rbac/whoami');

  getRbacPolicies = () => this.request<RbacPolicies>('/rbac/policies');

  getRbacRoles = () =>
    this.request<{ enabled: boolean; roles?: string[] }>('/rbac/roles');

  createRbacUser = (data: { name: string; role: string; token?: string }) =>
    this.request<{ name: string; role: string; token: string }>('/rbac/users', {
      method: 'POST',
      body: JSON.stringify(data),
    });

  setRbacUserRole = (name: string, role: string) =>
    this.request<{ ok: boolean }>(
      `/rbac/users/${encodeURIComponent(name)}/role`,
      { method: 'PUT', body: JSON.stringify({ role }) }
    );

  deleteRbacUser = (name: string) =>
    this.request<{ ok: boolean }>(
      `/rbac/users/${encodeURIComponent(name)}`,
      { method: 'DELETE' }
    );
}

export interface RbacWhoami {
  enabled: boolean;
  user?: string;
  role?: string;
  can_admin?: boolean;
  error?: string;
}

export interface RbacPolicy {
  role: string;
  object: string;
  action: string;
}

export interface RbacUserRole {
  name: string;
  role: string;
  pinned?: boolean;
}

export interface RbacPolicies {
  enabled: boolean;
  policies?: RbacPolicy[];
  users?: RbacUserRole[];
}

export interface NFSExport {
  directory: string;
  fsid: string;
  clientspec: string;
  options: string;
}

export interface ISCSILUN {
  lun: number;
  device: string;
  targetIqn: string;
}

export interface NVMeNamespace {
  namespaceId: number;
  backingPath: string;
  uuid: string;
  nguid: string;
  nqn: string;
}

export interface SelfHaStatus {
  success: boolean;
  message: string;
  enabled: boolean;
  resource: string;
  vip: string;
  nodes: string[];
  activeNode: string;
}

export const api = new ApiClient();

export interface HealthInfo {
  drbdInstalled: boolean;
  drbdVersion: string;
  drbdReactorInstalled: boolean;
  drbdReactorVersion: string;
  drbdReactorRunning: boolean;
  resourceAgentsInstalled: boolean;
  availableAgents: string[];
}
