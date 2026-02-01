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
}

export interface Volume {
  volumeId: number;
  device: string;
  sizeGb: number;
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

class ApiClient {
  private baseUrl: string;

  constructor(baseUrl: string = API_BASE) {
    this.baseUrl = baseUrl;
  }

  private async request<T>(
    endpoint: string,
    options?: RequestInit
  ): Promise<T> {
    const url = `${this.baseUrl}${endpoint}`;
    const response = await fetch(url, {
      headers: {
        'Content-Type': 'application/json',
        ...options?.headers,
      },
      mode: 'cors',
      ...options,
    });

    if (!response.ok) {
      throw new Error(`API error: ${response.status} ${response.statusText}`);
    }

    return response.json() as Promise<T>;
  }

  // Nodes
  getNodes = () => this.request<NodesResponse>('/nodes');

  getNode = (address: string) =>
    this.request<ApiResponse & { node: Node }>(`/nodes/${address}`);

  healthCheck = (node: string) =>
    this.request<ApiResponse & { health: HealthInfo }>(`/nodes/${node}/health`);

  // Pools
  getPools = () => this.request<PoolsResponse>('/pools');

  getPool = (name: string) =>
    this.request<ApiResponse & { pool: Pool }>(`/pools/${name}`);

  createPool = (data: {
    name: string;
    type: string;
    node: string;
    disks?: string[];
  }) =>
    this.request<ApiResponse & { pool: Pool }>('/pools', {
      method: 'POST',
      body: JSON.stringify(data),
    });

  deletePool = (name: string) =>
    this.request<ApiResponse>(`/pools/${name}`, { method: 'DELETE' });

  addDisk = (pool: string, disk: string, node?: string) =>
    this.request<ApiResponse>(`/pools/${pool}/disks`, {
      method: 'POST',
      body: JSON.stringify({ disk, node }),
    });

  // Resources
  getResources = () => this.request<ResourcesResponse>('/resources');

  getResource = (name: string) =>
    this.request<ApiResponse & { resource: Resource }>(`/resources/${name}`);

  createResource = (data: {
    name: string;
    port: number;
    protocol: string;
    nodes: string[];
  }) =>
    this.request<ApiResponse & { resource: Resource }>('/resources', {
      method: 'POST',
      body: JSON.stringify(data),
    });

  deleteResource = (name: string) =>
    this.request<ApiResponse>(`/resources/${name}`, { method: 'DELETE' });

  setPrimary = (resource: string, node: string, force = false) =>
    this.request<ApiResponse>(`/resources/${resource}/primary`, {
      method: 'POST',
      body: JSON.stringify({ node, force }),
    });

  // Gateways
  getGateways = () => this.request<GatewaysResponse>('/gateways');

  // HA
  getHaConfigs = () => this.request<HaConfigsResponse>('/ha');
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
