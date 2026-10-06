export const POOL_TYPE_OPTIONS = [
  { value: 'vg', label: 'LVM VG' },
  { value: 'thin_pool', label: 'LVM Thin Pool' },
  { value: 'thin_vdo', label: 'LVM Thin on VDO' },
  { value: 'zfs', label: 'ZFS' },
];

export function poolTypeBadgeClass(type: string): string {
  switch (type) {
    case 'zfs':
      return 'bg-blue-100 text-blue-700 border-blue-200 dark:bg-blue-950 dark:text-blue-300 dark:border-blue-900';
    case 'thin_pool':
      return 'bg-amber-100 text-amber-700 border-amber-200 dark:bg-amber-950 dark:text-amber-300 dark:border-amber-900';
    case 'vg':
    default:
      return 'bg-purple-100 text-purple-700 border-purple-200 dark:bg-purple-950 dark:text-purple-300 dark:border-purple-900';
  }
}

export function poolTypeLabel(type: string): string {
  const opt = POOL_TYPE_OPTIONS.find((o) => o.value === type);
  return opt ? opt.label : type.toUpperCase();
}
