import { useQuery } from '@tanstack/react-query';
import { api } from '@/services/api';
import { type GwKind } from './protocol';

// ==================== Row ====================

/**
 * What the row and its expansion both need. The query keys are the ones the
 * manage dialog already uses, so opening the dialog reuses this cache instead
 * of re-fetching, and a mutation there invalidates the row too. Only the
 * queries this gateway's protocol has are enabled; the rest never fire.
 */
export function useGatewayDetail(kind: GwKind, resource: string) {
  const nfs = kind === 'nfs';
  const iscsi = kind === 'iscsi';
  const nvme = kind === 'nvme';
  const smb = kind === 'smb';
  const exports = useQuery({
    queryKey: ['nfs-exports', resource],
    queryFn: () => api.listNFSExports(resource),
    enabled: nfs,
    retry: false,
  });
  const luns = useQuery({
    queryKey: ['iscsi-luns', resource],
    queryFn: () => api.listISCSILUNs(resource),
    enabled: iscsi,
    retry: false,
  });
  const initiators = useQuery({
    queryKey: ['iscsi-initiators', resource],
    queryFn: () => api.listISCSIInitiators(resource),
    enabled: iscsi,
    retry: false,
  });
  const chap = useQuery({
    queryKey: ['iscsi-chap', resource],
    queryFn: () => api.getISCSIChap(resource),
    enabled: iscsi,
    retry: false,
  });
  const namespaces = useQuery({
    queryKey: ['nvme-namespaces', resource],
    queryFn: () => api.listNVMeNamespaces(resource),
    enabled: nvme,
    retry: false,
  });
  const hosts = useQuery({
    queryKey: ['nvme-hosts', resource],
    queryFn: () => api.listNVMeHosts(resource),
    enabled: nvme,
    retry: false,
  });
  const shares = useQuery({
    queryKey: ['smb-shares', resource],
    queryFn: () => api.listSMBShares(resource),
    enabled: smb,
    retry: false,
  });
  return { exports, luns, initiators, chap, namespaces, hosts, shares };
}

export type GatewayDetail = ReturnType<typeof useGatewayDetail>;
