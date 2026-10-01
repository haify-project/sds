import { type ApiResponse, type RequestFn } from './client';

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

export interface GatewaysResponse extends ApiResponse {
  gateways: Gateway[];
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

export const gatewaysApi = (request: RequestFn) => ({
  // ==================== Gateways ====================
  getGateways: () => request<GatewaysResponse>('/gateways'),

  getGateway: (id: string) =>
    request<ApiResponse & { gateway: Gateway }>(`/gateways/${id}`),

  // NFS Gateway
  createNFSGateway: (data: {
    resource: string;
    serviceIp: string;
    exportPath: string;
    allowedIps?: string[];
    fsType?: string;
    options?: Record<string, string>;
  }) =>
    request<ApiResponse & { configPath: string }>('/gateways/nfs', {
      method: 'POST',
      body: JSON.stringify(data),
    }),

  // iSCSI Gateway
  createISCSIGateway: (data: {
    resource: string;
    serviceIp: string;
    iqn: string;
    allowedInitiators?: string[];
    username?: string;
    password?: string;
    implementation?: string;
    options?: Record<string, string>;
  }) =>
    request<ApiResponse & { configPath: string }>('/gateways/iscsi', {
      method: 'POST',
      body: JSON.stringify(data),
    }),

  // NVMe Gateway
  createNVMeGateway: (data: {
    resource: string;
    serviceIp: string;
    nqn: string;
    transportType?: string;
    options?: Record<string, string>;
  }) =>
    request<ApiResponse & { configPath: string }>('/gateways/nvme', {
      method: 'POST',
      body: JSON.stringify(data),
    }),

  deleteGateway: (id: string) =>
    request<ApiResponse>(`/gateways/${id}`, { method: 'DELETE' }),

  startGateway: (id: string) =>
    request<ApiResponse>(`/gateways/${id}/start`, { method: 'POST' }),

  stopGateway: (id: string) =>
    request<ApiResponse>(`/gateways/${id}/stop`, { method: 'POST' }),

  // ==================== NFS Export Management ====================
  listNFSExports: (resource: string) =>
    request<ApiResponse & { exports: NFSExport[] }>('/gateways/nfs/exports:list', {
      method: 'POST',
      body: JSON.stringify({ resource }),
    }),

  addNFSExport: (data: {
    resource: string;
    exportPath: string;
    fsid?: number;
    clientSpec?: string;
    options?: string;
  }) =>
    request<ApiResponse>('/gateways/nfs/exports:add', {
      method: 'POST',
      body: JSON.stringify(data),
    }),

  removeNFSExport: (resource: string, exportPath: string) =>
    request<ApiResponse>('/gateways/nfs/exports:remove', {
      method: 'POST',
      body: JSON.stringify({ resource, exportPath }),
    }),

  // ==================== iSCSI LUN / Initiator / CHAP ====================
  listISCSILUNs: (resource: string) =>
    request<ApiResponse & { luns: ISCSILUN[] }>('/gateways/iscsi/luns:list', {
      method: 'POST',
      body: JSON.stringify({ resource }),
    }),

  addISCSILUN: (data: { resource: string; lun: number; device: string }) =>
    request<ApiResponse>('/gateways/iscsi/luns:add', {
      method: 'POST',
      body: JSON.stringify(data),
    }),

  removeISCSILUN: (resource: string, lun: number) =>
    request<ApiResponse>('/gateways/iscsi/luns:remove', {
      method: 'POST',
      body: JSON.stringify({ resource, lun }),
    }),

  listISCSIInitiators: (resource: string) =>
    request<ApiResponse & { initiators: string[] }>('/gateways/iscsi/initiators:list', {
      method: 'POST',
      body: JSON.stringify({ resource }),
    }),

  addISCSIInitiator: (resource: string, initiator: string) =>
    request<ApiResponse>('/gateways/iscsi/initiators:add', {
      method: 'POST',
      body: JSON.stringify({ resource, initiator }),
    }),

  removeISCSIInitiator: (resource: string, initiator: string) =>
    request<ApiResponse>('/gateways/iscsi/initiators:remove', {
      method: 'POST',
      body: JSON.stringify({ resource, initiator }),
    }),

  getISCSIChap: (resource: string) =>
    request<ApiResponse & { username: string; password: string; mutual: boolean }>(
      '/gateways/iscsi/chap:get',
      { method: 'POST', body: JSON.stringify({ resource }) }
    ),

  setISCSIChap: (data: {
    resource: string;
    username: string;
    password: string;
    mutual?: boolean;
  }) =>
    request<ApiResponse>('/gateways/iscsi/chap:set', {
      method: 'POST',
      body: JSON.stringify(data),
    }),

  // ==================== NVMe Namespace / Host ====================
  listNVMeNamespaces: (resource: string) =>
    request<ApiResponse & { namespaces: NVMeNamespace[] }>('/gateways/nvme/namespaces:list', {
      method: 'POST',
      body: JSON.stringify({ resource }),
    }),

  addNVMeNamespace: (resource: string, device: string) =>
    request<ApiResponse>('/gateways/nvme/namespaces:add', {
      method: 'POST',
      body: JSON.stringify({ resource, device }),
    }),

  removeNVMeNamespace: (resource: string, namespaceId: number) =>
    request<ApiResponse>('/gateways/nvme/namespaces:remove', {
      method: 'POST',
      body: JSON.stringify({ resource, namespaceId }),
    }),

  listNVMeHosts: (resource: string) =>
    request<ApiResponse & { hosts: string[] }>('/gateways/nvme/hosts:list', {
      method: 'POST',
      body: JSON.stringify({ resource }),
    }),

  addNVMeHost: (resource: string, hostNqn: string) =>
    request<ApiResponse>('/gateways/nvme/hosts:add', {
      method: 'POST',
      body: JSON.stringify({ resource, hostNqn }),
    }),

  removeNVMeHost: (resource: string, hostNqn: string) =>
    request<ApiResponse>('/gateways/nvme/hosts:remove', {
      method: 'POST',
      body: JSON.stringify({ resource, hostNqn }),
    }),
});
