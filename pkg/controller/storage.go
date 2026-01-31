package controller

import (
	"context"
	"fmt"
	"sync"

	"go.uber.org/zap"
	pb "github.com/liliang-cn/drbd-agent/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/client"
)

// StorageManager manages all storage operations
type StorageManager struct {
	controller *Controller
	agents     map[string]*client.AgentClient
	mu         sync.RWMutex
}

// NewStorageManager creates a new storage manager
func NewStorageManager(ctrl *Controller) *StorageManager {
	return &StorageManager{
		controller: ctrl,
		agents:     make(map[string]*client.AgentClient),
	}
}

// AddAgent adds an agent connection
func (sm *StorageManager) AddAgent(node string, agent *client.AgentClient) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.agents[node] = agent
	sm.controller.logger.Info("Agent added", zap.String("node", node))
}

// ==================== POOL OPERATIONS ====================

// CreatePool creates a storage pool
func (sm *StorageManager) CreatePool(ctx context.Context, name, poolType, node string, disks []string, sizeGB uint64) error {
	// Use controller's agents map (shared with NodeManager)
	sm.controller.agentsLock.RLock()
	agent := sm.controller.agents[node]
	sm.controller.agentsLock.RUnlock()

	if agent == nil {
		return fmt.Errorf("node not found: %s", node)
	}

	sm.controller.logger.Info("Creating pool",
		zap.String("name", name),
		zap.String("type", poolType),
		zap.String("node", node),
		zap.Strings("disks", disks))

	req := &pb.VGCreateRequest{
		VgName:         name,
		PhysicalVolumes: disks,
	}

	resp, err := agent.VGCreate(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create pool: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("failed to create pool: %s", resp.Message)
	}

	sm.controller.logger.Info("Pool created successfully",
		zap.String("name", name),
		zap.String("node", node))

	return nil
}

// GetPool gets pool information
func (sm *StorageManager) GetPool(ctx context.Context, poolName, node string) (*PoolInfo, error) {
	sm.controller.agentsLock.RLock()
	agent := sm.controller.agents[node]
	sm.controller.agentsLock.RUnlock()

	if agent == nil {
		return nil, fmt.Errorf("node not found: %s", node)
	}

	req := &pb.VGSRequest{}

	resp, err := agent.VGS(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get pool: %w", err)
	}

	if !resp.Success {
		return nil, fmt.Errorf("failed to get pool: %s", resp.Message)
	}

	// Find the requested pool
	for _, vg := range resp.Vgs {
		if vg.VgName == poolName {
			return &PoolInfo{
				Name:     vg.VgName,
				Type:     "vg",
				Node:     node,
				TotalGB:  vg.VgSize / 1024 / 1024 / 1024,
				FreeGB:   vg.VgFree / 1024 / 1024 / 1024,
				Devices:  []string{}, // In production, call PVDisplay to get actual PV names
			}, nil
		}
	}

	return nil, fmt.Errorf("pool not found: %s", poolName)
}

// ListPools lists all pools across all nodes
func (sm *StorageManager) ListPools(ctx context.Context) ([]*PoolInfo, error) {
	var pools []*PoolInfo

	sm.controller.agentsLock.RLock()
	defer sm.controller.agentsLock.RUnlock()

	for node, agent := range sm.controller.agents {
		req := &pb.VGSRequest{}

		resp, err := agent.VGS(ctx, req)
		if err != nil {
			sm.controller.logger.Warn("Failed to list pools",
				zap.String("node", node),
				zap.Error(err))
			continue
		}

		if !resp.Success {
			sm.controller.logger.Warn("Failed to list pools",
				zap.String("node", node),
				zap.String("error", resp.Message))
			continue
		}

		for _, vg := range resp.Vgs {
			pools = append(pools, &PoolInfo{
				Name:    vg.VgName,
				Type:    "vg",
				Node:    node,
				TotalGB: vg.VgSize / 1024 / 1024 / 1024,
				FreeGB:  vg.VgFree / 1024 / 1024 / 1024,
			})
		}
	}

	return pools, nil
}

// AddDiskToPool adds a disk to a pool
func (sm *StorageManager) AddDiskToPool(ctx context.Context, pool, disk, node string) error {
	sm.controller.agentsLock.RLock()
	agent := sm.controller.agents[node]
	sm.controller.agentsLock.RUnlock()

	if agent == nil {
		return fmt.Errorf("node not found: %s", node)
	}

	req := &pb.VGExtendRequest{
		VgName:         pool,
		PhysicalVolumes: []string{disk},
	}

	resp, err := agent.VGExtend(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to add disk: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("failed to add disk: %s", resp.Message)
	}

	sm.controller.logger.Info("Disk added to pool",
		zap.String("pool", pool),
		zap.String("disk", disk),
		zap.String("node", node))

	return nil
}

// DeletePool deletes a storage pool
func (sm *StorageManager) DeletePool(ctx context.Context, name, node string) error {
	sm.controller.agentsLock.RLock()
	agent := sm.controller.agents[node]
	sm.controller.agentsLock.RUnlock()

	if agent == nil {
		return fmt.Errorf("node not found: %s", node)
	}

	sm.controller.logger.Info("Deleting pool",
		zap.String("name", name),
		zap.String("node", node))

	// Remove VG using LVM
	req := &pb.VGRemoveRequest{
		VgName: name,
		Force:  true,
	}

	resp, err := agent.VGRemove(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to delete pool: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("failed to delete pool: %s", resp.Message)
	}

	sm.controller.logger.Info("Pool deleted successfully",
		zap.String("name", name),
		zap.String("node", node))

	return nil
}
