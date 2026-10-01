import { type ApiResponse, type RequestFn } from './client';

export interface ResourceProfile {
  name: string;
  protocol: string;
  storageType: string;
  pool: string;
  replicas: number;
  replicasOnDifferent: string[];
  replicasOnSame: string[];
  drbdOptions: Record<string, string>;
  labels: Record<string, string>;
}

export interface ResourceProfilesResponse extends ApiResponse {
  profiles: ResourceProfile[];
}

export const profilesApi = (request: RequestFn) => ({
  // ==================== Resource profiles ====================
  getResourceProfiles: () =>
    request<ResourceProfilesResponse>('/resource-profiles'),

  getResourceProfile: (name: string) =>
    request<ApiResponse & { profile: ResourceProfile }>(
      `/resource-profiles/${encodeURIComponent(name)}`,
    ),

  createResourceProfile: (profile: ResourceProfile) =>
    request<ApiResponse & { profile: ResourceProfile }>('/resource-profiles', {
      method: 'POST',
      body: JSON.stringify({ profile }),
    }),

  deleteResourceProfile: (name: string) =>
    request<ApiResponse>(
      `/resource-profiles/${encodeURIComponent(name)}`,
      { method: 'DELETE' },
    ),
});
