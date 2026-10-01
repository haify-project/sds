import { type ApiResponse, type RequestFn } from './client';

export interface SelfHaStatus {
  success: boolean;
  message: string;
  enabled: boolean;
  resource: string;
  vip: string;
  nodes: string[];
  activeNode: string;
}

export const selfHaApi = (request: RequestFn) => ({
  // ==================== Controller Self-HA ====================
  getSelfHaStatus: () => request<SelfHaStatus>('/selfha'),

  enableSelfHa: (data: {
    vip: string;
    pool: string;
    sizeGb?: number;
    port?: number;
    nodes?: string[];
  }) =>
    request<ApiResponse & { resource: string; handoffLog: string }>('/selfha/enable', {
      method: 'POST',
      body: JSON.stringify(data),
    }),

  disableSelfHa: (node: string) =>
    request<ApiResponse>('/selfha/disable', {
      method: 'POST',
      body: JSON.stringify({ node }),
    }),
});
