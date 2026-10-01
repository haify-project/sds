package controller

import (
	"context"
	"fmt"
	"strings"

	"github.com/liliang-cn/sds/pkg/deployment"
	"go.uber.org/zap"
)

// CreateLvmSnapshot creates an LVM snapshot
func (sm *StorageManager) CreateLvmSnapshot(ctx context.Context, vgName, lvName, snapshotName, node, size string) error {
	vgName = normalizeManagedName(vgName)

	sm.controller.logger.Info("Creating LVM snapshot",
		zap.String("vg_name", vgName),
		zap.String("lv_name", lvName),
		zap.String("snapshot", snapshotName),
		zap.String("node", node),
		zap.String("size", size))

	// Resolve node address
	address := sm.controller.ResolveHost(node)

	// Check if LV is thin
	isThin, err := sm.controller.deployment.LVIsThin(ctx, address, vgName, lvName)
	if err != nil {
		sm.controller.logger.Warn("Failed to check if LV is thin", zap.Error(err))
		// Fallback to standard snapshot if check fails (safest default?) or error?
		// Default to standard
	}

	var result *deployment.ExecResult
	if isThin {
		sm.controller.logger.Info("Creating Thin Snapshot", zap.String("origin", lvName))
		result, err = sm.controller.deployment.LVCreateThinSnapshot(ctx, []string{address}, vgName, lvName, snapshotName)
	} else {
		sm.controller.logger.Info("Creating Standard Snapshot", zap.String("origin", lvName))
		result, err = sm.controller.deployment.LVCreateSnapshot(ctx, []string{address}, vgName, lvName, snapshotName, size)
	}

	if err != nil {
		return fmt.Errorf("failed to create LVM snapshot: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to create LVM snapshot: %s", result.FailureDetails())
	}

	return nil
}

// ListLvmSnapshots lists LVM snapshots in a volume group.
//
// resource narrows the result to that DRBD resource's volumes; empty lists the
// whole group.
func (sm *StorageManager) ListLvmSnapshots(ctx context.Context, vgName, node, resource string) ([]*SnapshotInfo, error) {
	vgName = normalizeManagedName(vgName)

	// Resolve node address
	address := sm.controller.ResolveHost(node)

	result, err := sm.controller.deployment.LVListSnapshots(ctx, []string{address}, vgName)
	if err != nil {
		return nil, fmt.Errorf("failed to list LVM snapshots: %w", err)
	}
	if !result.AllSuccess() {
		// Reporting an empty list on a failed command is how this went unnoticed
		// for so long: a pool holding 27 snapshots read as a pool holding none,
		// and nothing said why.
		return nil, fmt.Errorf("failed to list LVM snapshots in %s: %s", vgName, result.FailureDetails())
	}

	// An empty resource lists the whole group. Otherwise only snapshots whose
	// origin is one of the resource's backing volumes are returned — the
	// `--resource` flag used to be accepted, printed in the heading, and then
	// ignored, so `snapshot list --resource a` and `--resource b` returned
	// identical lists of everything in the pool.
	var wanted map[string]bool
	if resource != "" {
		wanted, err = sm.backingVolumesOf(ctx, resource)
		if err != nil {
			return nil, err
		}
	}

	var snapshots []*SnapshotInfo
	for _, r := range result.Hosts {
		for _, line := range strings.Split(strings.TrimSpace(r.Output), "\n") {
			snap, ok := parseSnapshotLine(line, vgName)
			if !ok {
				continue
			}
			if wanted != nil && !wanted[snap.Origin] {
				continue
			}
			snapshots = append(snapshots, snap)
		}
	}

	return snapshots, nil
}

// parseSnapshotLine reads one row of LVListSnapshots output:
// "name|size_bytes|time|origin".
func parseSnapshotLine(line, vgName string) (*SnapshotInfo, bool) {
	fields := strings.Split(strings.TrimSpace(line), "|")
	if len(fields) < 4 {
		return nil, false
	}
	name := strings.TrimSpace(fields[0])
	if name == "" {
		return nil, false
	}
	return &SnapshotInfo{
		Name:      name,
		Volume:    vgName,
		SizeGB:    bytesToGB(parseThinUint(fields[1])),
		CreatedAt: strings.TrimSpace(fields[2]),
		Origin:    strings.TrimSpace(fields[3]),
	}, true
}

// backingVolumesOf resolves a resource to the set of LV names its snapshots can
// have as an origin.
//
// Read from the database rather than rebuilt from the "<name>_data" /
// "<name>_vol<K>" convention: the convention lives in resource creation, and a
// second copy of it here would keep working right up until the day the first
// one changed.
func (sm *StorageManager) backingVolumesOf(ctx context.Context, resource string) (map[string]bool, error) {
	if sm.controller.db == nil {
		return nil, fmt.Errorf("database not available, cannot resolve volumes of resource %s", resource)
	}
	volumes, err := sm.controller.db.ListVolumes(ctx, resource)
	if err != nil {
		return nil, fmt.Errorf("list volumes of resource %s: %w", resource, err)
	}
	if len(volumes) == 0 {
		return nil, fmt.Errorf("resource %s has no volumes, or does not exist", resource)
	}
	wanted := make(map[string]bool, len(volumes))
	for _, v := range volumes {
		if v != nil && v.VolumeName != "" {
			wanted[v.VolumeName] = true
		}
	}
	return wanted, nil
}

// DeleteLvmSnapshot deletes an LVM snapshot
func (sm *StorageManager) DeleteLvmSnapshot(ctx context.Context, vgName, snapshotName, node string) error {
	vgName = normalizeManagedName(vgName)

	sm.controller.logger.Info("Deleting LVM snapshot",
		zap.String("vg_name", vgName),
		zap.String("snapshot", snapshotName),
		zap.String("node", node))

	// Resolve node address
	address := sm.controller.ResolveHost(node)

	result, err := sm.controller.deployment.LVRemoveSnapshot(ctx, []string{address}, vgName, snapshotName)
	if err != nil {
		return fmt.Errorf("failed to delete LVM snapshot: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to delete LVM snapshot: %s", result.FailureDetails())
	}

	return nil
}

// RestoreLvmSnapshot restores an LVM snapshot (merges it back to the origin).
// A snapshot of a DRBD backing volume is restored the way that keeps the
// resource's replicas in step; see snapshot_restore.go.
func (sm *StorageManager) RestoreLvmSnapshot(ctx context.Context, vgName, snapshotName, node string) error {
	vgName = normalizeManagedName(vgName)
	sm.controller.logger.Info("Restoring LVM snapshot",
		zap.String("vg_name", vgName),
		zap.String("snapshot", snapshotName),
		zap.String("node", node))
	return sm.controller.snapshots.RestoreLVMSnapshotByName(ctx, vgName, snapshotName, node)
}
