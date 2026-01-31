package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"go.uber.org/zap"
	pb "github.com/liliang-cn/drbd-agent/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/client"
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
	agents     map[string]*client.AgentClient
	mu         sync.RWMutex
}

// NewSnapshotManager creates a new snapshot manager
func NewSnapshotManager(ctrl *Controller) *SnapshotManager {
	return &SnapshotManager{
		controller: ctrl,
		agents:     make(map[string]*client.AgentClient),
	}
}

// AddAgent adds an agent connection
func (sm *SnapshotManager) AddAgent(node string, agent *client.AgentClient) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.agents[node] = agent
}

// CreateSnapshot creates a snapshot
func (sm *SnapshotManager) CreateSnapshot(ctx context.Context, volume, snapshotName, node string) error {
	sm.mu.RLock()
	agent := sm.agents[node]
	sm.mu.RUnlock()

	if agent == nil {
		return fmt.Errorf("node not found: %s", node)
	}

	sm.controller.logger.Info("Creating snapshot",
		zap.String("volume", volume),
		zap.String("snapshot", snapshotName),
		zap.String("node", node))

	// Parse volume path (e.g., "ubuntu-vg/lv0" -> vg="ubuntu-vg", lv="lv0")
	vg, lv := parseVolumePath(volume)
	originPath := fmt.Sprintf("/dev/%s/%s", vg, lv)

	req := &pb.LVSnapshotCreateRequest{
		OriginLvPath: originPath,
		SnapshotName: snapshotName,
	}

	resp, err := agent.LVSnapshotCreate(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create snapshot: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("failed to create snapshot: %s", resp.Message)
	}

	sm.controller.logger.Info("Snapshot created successfully",
		zap.String("volume", volume),
		zap.String("snapshot", snapshotName))

	return nil
}

// DeleteSnapshot deletes a snapshot
func (sm *SnapshotManager) DeleteSnapshot(ctx context.Context, volume, snapshotName, node string) error {
	sm.mu.RLock()
	agent := sm.agents[node]
	sm.mu.RUnlock()

	if agent == nil {
		return fmt.Errorf("node not found: %s", node)
	}

	sm.controller.logger.Info("Deleting snapshot",
		zap.String("volume", volume),
		zap.String("snapshot", snapshotName),
		zap.String("node", node))

	// Parse volume path and build snapshot LV path
	vg, _ := parseVolumePath(volume)
	snapshotPath := fmt.Sprintf("/dev/%s/%s", vg, snapshotName)

	req := &pb.LVRemoveRequest{
		LvPath: snapshotPath,
		Force:  false,
	}

	resp, err := agent.LVRemove(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to delete snapshot: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("failed to delete snapshot: %s", resp.Message)
	}

	sm.controller.logger.Info("Snapshot deleted successfully",
		zap.String("snapshot", snapshotName))

	return nil
}

// ListSnapshots lists snapshots for a volume
func (sm *SnapshotManager) ListSnapshots(ctx context.Context, volume, node string) ([]*SnapshotInfo, error) {
	sm.mu.RLock()
	agent := sm.agents[node]
	sm.mu.RUnlock()

	if agent == nil {
		return nil, fmt.Errorf("node not found: %s", node)
	}

	// Parse volume path
	vg, lv := parseVolumePath(volume)

	req := &pb.LVListSnapshotsRequest{
		VgName:      vg,
		OriginLvName: lv,
	}

	resp, err := agent.LVListSnapshots(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to list snapshots: %w", err)
	}

	if !resp.Success {
		return nil, fmt.Errorf("failed to list snapshots: %s", resp.Message)
	}

	var snapshots []*SnapshotInfo
	for _, snapInfo := range resp.Snapshots {
		snapshots = append(snapshots, &SnapshotInfo{
			Name:      snapInfo.LvName,
			Volume:    volume,
			SizeGB:    snapInfo.LvSize / 1024 / 1024 / 1024,
			CreatedAt: "", // In production, parse from LV attrs or call lvs with -o time format
		})
	}

	return snapshots, nil
}

// RestoreSnapshot restores a snapshot
func (sm *SnapshotManager) RestoreSnapshot(ctx context.Context, volume, snapshotName, node string) error {
	sm.mu.RLock()
	agent := sm.agents[node]
	sm.mu.RUnlock()

	if agent == nil {
		return fmt.Errorf("node not found: %s", node)
	}

	sm.controller.logger.Info("Restoring snapshot",
		zap.String("volume", volume),
		zap.String("snapshot", snapshotName),
		zap.String("node", node))

	// Parse volume path and build paths
	vg, lv := parseVolumePath(volume)
	originPath := fmt.Sprintf("/dev/%s/%s", vg, lv)
	snapshotPath := fmt.Sprintf("/dev/%s/%s", vg, snapshotName)

	req := &pb.LVSnapshotRestoreRequest{
		OriginLvPath:   originPath,
		SnapshotLvPath: snapshotPath,
		Force:          false,
	}

	resp, err := agent.LVSnapshotRestore(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to restore snapshot: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("failed to restore snapshot: %s", resp.Message)
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

func isSnapshotLV(lvName, originalLV string) bool {
	// Check if this LV is a snapshot of the original LV
	// Snapshots typically have names like "lv0_snap1" or similar
	return strings.Contains(lvName, "_snap") || strings.Contains(lvName, originalLV+"_")
}
