package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

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
	mu         sync.RWMutex
}

// NewSnapshotManager creates a new snapshot manager
func NewSnapshotManager(ctrl *Controller) *SnapshotManager {
	return &SnapshotManager{
		controller: ctrl,
	}
}

// CreateSnapshot creates a snapshot
func (sm *SnapshotManager) CreateSnapshot(ctx context.Context, volume, snapshotName, node string) error {
	sm.controller.logger.Info("Creating snapshot",
		zap.String("volume", volume),
		zap.String("snapshot", snapshotName),
		zap.String("node", node))

	// Parse volume path (e.g., "ubuntu-vg/lv0" -> vg="ubuntu-vg", lv="lv0")
	vg, lv := parseVolumePath(volume)
	originPath := fmt.Sprintf("/dev/%s/%s", vg, lv)

	// Create snapshot using lvcreate
	cmd := fmt.Sprintf("sudo lvcreate -s -n %s %s", snapshotName, originPath)
	result, err := sm.controller.deployment.Exec(ctx, []string{node}, cmd)
	if err != nil {
		return fmt.Errorf("failed to create snapshot: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to create snapshot: %v", result.FailedHosts())
	}

	sm.controller.logger.Info("Snapshot created successfully",
		zap.String("volume", volume),
		zap.String("snapshot", snapshotName))

	return nil
}

// DeleteSnapshot deletes a snapshot
func (sm *SnapshotManager) DeleteSnapshot(ctx context.Context, volume, snapshotName, node string) error {
	sm.controller.logger.Info("Deleting snapshot",
		zap.String("volume", volume),
		zap.String("snapshot", snapshotName),
		zap.String("node", node))

	// Parse volume path and build snapshot LV path
	vg, _ := parseVolumePath(volume)
	snapshotPath := fmt.Sprintf("/dev/%s/%s", vg, snapshotName)

	// Remove snapshot
	cmd := fmt.Sprintf("sudo lvremove -f %s", snapshotPath)
	result, err := sm.controller.deployment.Exec(ctx, []string{node}, cmd)
	if err != nil {
		return fmt.Errorf("failed to delete snapshot: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to delete snapshot: %v", result.FailedHosts())
	}

	sm.controller.logger.Info("Snapshot deleted successfully",
		zap.String("snapshot", snapshotName))

	return nil
}

// ListSnapshots lists snapshots for a volume
func (sm *SnapshotManager) ListSnapshots(ctx context.Context, volume, node string) ([]*SnapshotInfo, error) {
	// Parse volume path
	vg, lv := parseVolumePath(volume)

	// List snapshots using lvs
	cmd := fmt.Sprintf("sudo lvs --noheadings --separator '|' -o lv_name,lv_size,origin %s", vg)
	result, err := sm.controller.deployment.Exec(ctx, []string{node}, cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to list snapshots: %w", err)
	}

	if !result.AllSuccess() {
		return nil, fmt.Errorf("failed to list snapshots: %v", result.FailedHosts())
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
	result, err := sm.controller.deployment.Exec(ctx, []string{node}, cmd)
	if err != nil {
		return fmt.Errorf("failed to restore snapshot: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to restore snapshot: %v", result.FailedHosts())
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
