import { ResourceProfile } from '@/services/api';

export const unsetValue = '__unset__';

export type ProfileForm = {
  name: string;
  protocol: string;
  storageType: string;
  pool: string;
  replicas: string;
  replicasOnDifferent: string;
  replicasOnSame: string;
  drbdOptions: string;
  labels: string;
};

export const emptyForm: ProfileForm = {
  name: '',
  protocol: 'C',
  storageType: 'lvm',
  pool: '',
  replicas: '2',
  replicasOnDifferent: '',
  replicasOnSame: '',
  drbdOptions: '',
  labels: '',
};

function formatMap(values?: Record<string, string>): string {
  return Object.entries(values ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, value]) => `${key}=${value}`)
    .join(', ');
}

function parseMap(value: string, field: string): Record<string, string> {
  const result: Record<string, string> = {};
  for (const raw of value.split(',')) {
    const entry = raw.trim();
    if (!entry) continue;
    const separator = entry.indexOf('=');
    if (separator < 1) {
      throw new Error(`${field}: "${entry}" must use key=value`);
    }
    const key = entry.slice(0, separator).trim();
    const itemValue = entry.slice(separator + 1).trim();
    if (!key || !itemValue) {
      throw new Error(`${field}: "${entry}" must have a non-empty key and value`);
    }
    result[key] = itemValue;
  }
  return result;
}

function parseList(value: string): string[] {
  return value
    .split(',')
    .map((item) => item.trim())
    .filter(Boolean);
}

export function profileToForm(profile: ResourceProfile): ProfileForm {
  return {
    name: profile.name,
    protocol: profile.protocol || '',
    storageType: profile.storageType || '',
    pool: profile.pool || '',
    replicas: profile.replicas ? String(profile.replicas) : '',
    replicasOnDifferent: (profile.replicasOnDifferent ?? []).join(', '),
    replicasOnSame: (profile.replicasOnSame ?? []).join(', '),
    drbdOptions: formatMap(profile.drbdOptions),
    labels: formatMap(profile.labels),
  };
}

export function formToProfile(form: ProfileForm): ResourceProfile {
  const replicas = form.replicas.trim() ? Number(form.replicas) : 0;
  if (!Number.isInteger(replicas) || replicas < 0) {
    throw new Error('Replicas must be a non-negative integer');
  }
  return {
    name: form.name.trim(),
    protocol: form.protocol,
    storageType: form.storageType,
    pool: form.pool.trim(),
    replicas,
    replicasOnDifferent: parseList(form.replicasOnDifferent),
    replicasOnSame: parseList(form.replicasOnSame),
    drbdOptions: parseMap(form.drbdOptions, 'DRBD options'),
    labels: parseMap(form.labels, 'Labels'),
  };
}
