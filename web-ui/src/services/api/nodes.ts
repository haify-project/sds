import { type ApiResponse, type RequestFn } from './client';

export interface Node {
  name: string;
  address: string;
  hostname: string;
  state: string;
  lastSeen: string;
  version: string;
}

export interface NodesResponse extends ApiResponse {
  nodes: Node[];
}

export interface HealthInfo {
  drbdInstalled: boolean;
  drbdVersion: string;
  drbdReactorInstalled: boolean;
  drbdReactorVersion: string;
  drbdReactorRunning: boolean;
  resourceAgentsInstalled: boolean;
  availableAgents: string[];
}

export const nodesApi = (request: RequestFn) => ({
  // ==================== Nodes ====================
  getNodes: () => request<NodesResponse>('/nodes'),

  getNode: (address: string) =>
    request<ApiResponse & { node: Node }>(`/nodes/${address}`),

  registerNode: (data: { name: string; address: string }) =>
    request<ApiResponse & { node: Node }>('/nodes', {
      method: 'POST',
      body: JSON.stringify(data),
    }),

  unregisterNode: (address: string) =>
    request<ApiResponse>(`/nodes/${address}`, { method: 'DELETE' }),

  healthCheck: (node: string) =>
    request<ApiResponse & { health: HealthInfo }>(`/nodes/${node}/health`),
});
