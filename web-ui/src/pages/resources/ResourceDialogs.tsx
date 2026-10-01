import { Resource } from '../../services/api';
import { SnapshotsDialog } from '@/components/SnapshotsDialog';
import { type NodeOpt, type PoolOpt, type RowDialog } from './types';
import { isCsiManaged } from './replication';
import { EditOptionsDialog } from './EditOptionsDialog';
import { ScheduleDialog } from './ScheduleDialog';
import { AddDRDialog, DRFailoverDialog } from './DisasterRecoveryDialogs';
import { SetRoleDialog } from './SetRoleDialog';
import { VolumesDialog } from './VolumesDialog';
import { MountDialog } from './MountDialog';
import { DeleteResourceDialog } from './DeleteResourceDialog';

/**
 * Every dialog a resource can open, mounted once for that resource whichever
 * view asked. `kind` is null while nothing is open, which is also how each
 * dialog stays mounted through its own close animation.
 */
export function ResourceDialogs({
  resource,
  kind,
  onSelect,
  pools,
  nodes,
}: {
  resource: Resource;
  kind: RowDialog | null;
  onSelect: (d: RowDialog | null) => void;
  pools: PoolOpt[];
  nodes: NodeOpt[];
}) {
  const close = () => onSelect(null);

  return (
    <>
      <AddDRDialog
        open={kind === 'add-dr'}
        onOpenChange={(o) => (o ? onSelect('add-dr') : close())}
        resource={resource}
        nodes={nodes}
      />
      <DRFailoverDialog
        open={kind === 'dr-failover'}
        onOpenChange={(o) => (o ? onSelect('dr-failover') : close())}
        resource={resource}
      />
      <SetRoleDialog
        open={kind === 'primary'}
        onOpenChange={(o) => (o ? onSelect('primary') : close())}
        resource={resource}
        mode="primary"
      />
      <SetRoleDialog
        open={kind === 'secondary'}
        onOpenChange={(o) => (o ? onSelect('secondary') : close())}
        resource={resource}
        mode="secondary"
      />
      <SnapshotsDialog
        resource={resource.name}
        open={kind === 'snapshots'}
        onOpenChange={(o) => (o ? onSelect('snapshots') : close())}
      />
      <VolumesDialog
        open={kind === 'volumes' || kind === 'add-volume'}
        onOpenChange={(o) => (o ? onSelect('volumes') : close())}
        defaultTab={kind === 'add-volume' ? 'add' : 'volumes'}
        resource={resource}
        pools={pools}
      />
      <MountDialog
        open={kind === 'mount'}
        onOpenChange={(o) => (o ? onSelect('mount') : close())}
        resource={resource}
        nodes={nodes}
      />
      <EditOptionsDialog
        open={kind === 'options'}
        onOpenChange={(o) => (o ? onSelect('options') : close())}
        resource={resource}
      />
      <ScheduleDialog
        open={kind === 'schedule'}
        onOpenChange={(o) => (o ? onSelect('schedule') : close())}
        resource={resource}
      />
      <DeleteResourceDialog
        open={kind === 'delete'}
        onOpenChange={(o) => (o ? onSelect('delete') : close())}
        resourceName={resource.name}
        csiManaged={isCsiManaged(resource)}
      />
    </>
  );
}
