import { type RequestFn } from './client';

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

export const rbacApi = (request: RequestFn) => ({
  // ==================== RBAC ====================
  getRbacWhoami: () => request<RbacWhoami>('/rbac/whoami'),

  getRbacPolicies: () => request<RbacPolicies>('/rbac/policies'),

  getRbacRoles: () =>
    request<{ enabled: boolean; roles?: string[] }>('/rbac/roles'),

  createRbacUser: (data: { name: string; role: string; token?: string }) =>
    request<{ name: string; role: string; token: string }>('/rbac/users', {
      method: 'POST',
      body: JSON.stringify(data),
    }),

  setRbacUserRole: (name: string, role: string) =>
    request<{ ok: boolean }>(
      `/rbac/users/${encodeURIComponent(name)}/role`,
      { method: 'PUT', body: JSON.stringify({ role }) }
    ),

  deleteRbacUser: (name: string) =>
    request<{ ok: boolean }>(
      `/rbac/users/${encodeURIComponent(name)}`,
      { method: 'DELETE' }
    ),
});
