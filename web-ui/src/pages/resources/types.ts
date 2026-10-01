export interface NodeOpt {
  name: string;
  address: string;
}

export interface PoolOpt {
  name: string;
  node: string;
  type: string;
  freeGb: string;
}

export type FilterKey = 'all' | 'healthy' | 'syncing' | 'offsite';

/** Every dialog a row can open. One at a time, so one nullable holds it. */
export type RowDialog =
  | 'primary'
  | 'secondary'
  | 'volumes'
  | 'add-volume'
  | 'snapshots'
  | 'mount'
  | 'options'
  | 'schedule'
  | 'add-dr'
  | 'dr-failover'
  | 'delete';
