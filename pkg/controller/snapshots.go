package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

// SnapshotInfo represents snapshot information
type SnapshotInfo struct {
	Name      string
	Volume    string
	SizeGB    uint64
	CreatedAt string
}

// SnapshotManager manages volume snapshots
type SnapshotManager struct {
	controller *Controller
}

// NewSnapshotManager creates a new snapshot manager
func NewSnapshotManager(ctrl *Controller) *SnapshotManager {
	return &SnapshotManager{
		controller: ctrl,
	}
}

// CreateSnapshot creates a snapshot
func (sm *SnapshotManager) CreateSnapshot(ctx context.Context, volume, snapshotName, node string) error {
	address := sm.controller.ResolveHost(node)

	sm.controller.logger.Info("Creating snapshot",
		zap.String("volume", volume),
		zap.String("snapshot", snapshotName),
		zap.String("node", node))

	// Parse volume path (e.g., "ubuntu-vg/lv0" -> vg="ubuntu-vg", lv="lv0")
	vg, lv := parseVolumePath(volume)
	originPath := fmt.Sprintf("/dev/%s/%s", vg, lv)

	// Create snapshot using lvcreate. Thick LVM snapshots require a COW
	// size or they are rejected outright ("Please specify either size or
	// extents"); 20% of the origin is a sane default for the
	// backup-then-delete workflow this API serves. Callers needing exact
	// sizing use the LVM-specific RPC, which takes an explicit size.
	cmd := fmt.Sprintf("sudo lvcreate -s -l 20%%ORIGIN -n %s %s", snapshotName, originPath)
	result, err := sm.controller.deployment.Exec(ctx, []string{address}, cmd)
	if err != nil {
		return fmt.Errorf("failed to create snapshot: %w", err)
	}

	if !result.AllSuccess() {
		for host, hr := range result.Hosts {
			if !hr.Success {
				return fmt.Errorf("failed to create snapshot on %s: %s", host, strings.TrimSpace(hr.Output))
			}
		}
		return fmt.Errorf("failed to create snapshot: %s", result.FailureDetails())
	}

	sm.controller.logger.Info("Snapshot created successfully",
		zap.String("volume", volume),
		zap.String("snapshot", snapshotName))

	return nil
}

// DeleteSnapshot deletes a snapshot
func (sm *SnapshotManager) DeleteSnapshot(ctx context.Context, volume, snapshotName, node string) error {
	address := sm.controller.ResolveHost(node)

	sm.controller.logger.Info("Deleting snapshot",
		zap.String("volume", volume),
		zap.String("snapshot", snapshotName),
		zap.String("node", node))

	// Parse volume path and build snapshot LV path
	vg, _ := parseVolumePath(volume)
	snapshotPath := fmt.Sprintf("/dev/%s/%s", vg, snapshotName)

	// Remove snapshot
	cmd := fmt.Sprintf("sudo lvremove -f %s", snapshotPath)
	result, err := sm.controller.deployment.Exec(ctx, []string{address}, cmd)
	if err != nil {
		return fmt.Errorf("failed to delete snapshot: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to delete snapshot: %s", result.FailureDetails())
	}

	sm.controller.logger.Info("Snapshot deleted successfully",
		zap.String("snapshot", snapshotName))

	return nil
}

// ListSnapshots lists snapshots for a volume
func (sm *SnapshotManager) ListSnapshots(ctx context.Context, volume, node string) ([]*SnapshotInfo, error) {
	// Parse volume path
	vg, lv := parseVolumePath(volume)
	address := sm.controller.ResolveHost(node)

	// List snapshots using lvs
	cmd := fmt.Sprintf("sudo lvs --noheadings --separator '|' -o lv_name,lv_size,origin %s", vg)
	result, err := sm.controller.deployment.Exec(ctx, []string{address}, cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to list snapshots: %w", err)
	}

	if !result.AllSuccess() {
		return nil, fmt.Errorf("failed to list snapshots: %s", result.FailureDetails())
	}

	var snapshots []*SnapshotInfo
	for _, r := range result.Hosts {
		if r.Success {
			lines := strings.Split(strings.TrimSpace(r.Output), "\n")
			for _, line := range lines {
				fields := strings.Split(line, "|")
				if len(fields) >= 3 {
					lvName := strings.TrimSpace(fields[0])
					origin := strings.TrimSpace(fields[2])
					// Check if this is a snapshot of the requested LV
					if origin == lv {
						sizeStr := strings.TrimSpace(fields[1])
						// Parse size (e.g., "4.00g" or "4.00G")
						sizeStr = strings.TrimSuffix(sizeStr, "g")
						sizeStr = strings.TrimSuffix(sizeStr, "G")
						sizeFloat, _ := strconv.ParseFloat(sizeStr, 64)
						snapshots = append(snapshots, &SnapshotInfo{
							Name:      lvName,
							Volume:    volume,
							SizeGB:    uint64(sizeFloat),
							CreatedAt: "",
						})
					}
				}
			}
		}
	}

	return snapshots, nil
}

// RestoreSnapshot restores a snapshot
func (sm *SnapshotManager) RestoreSnapshot(ctx context.Context, volume, snapshotName, node string) error {
	address := sm.controller.ResolveHost(node)

	sm.controller.logger.Info("Restoring snapshot",
		zap.String("volume", volume),
		zap.String("snapshot", snapshotName),
		zap.String("node", node))

	// Parse volume path and build paths
	vg, _ := parseVolumePath(volume)
	snapshotPath := fmt.Sprintf("/dev/%s/%s", vg, snapshotName)

	// Merge snapshot back into origin
	// First, unmount if mounted (caller should handle this)
	// Then use lvconvert --merge
	cmd := fmt.Sprintf("sudo lvconvert --merge %s", snapshotPath)
	result, err := sm.controller.deployment.Exec(ctx, []string{address}, cmd)
	if err != nil {
		return fmt.Errorf("failed to restore snapshot: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to restore snapshot: %s", result.FailureDetails())
	}

	sm.controller.logger.Info("Snapshot restored successfully",
		zap.String("snapshot", snapshotName))

	return nil
}

func parseVolumePath(volume string) (vg, lv string) {
	parts := strings.Split(volume, "/")
	if len(parts) >= 2 {
		return parts[0], parts[1]
	}
	return "", volume
}
