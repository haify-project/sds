package controller

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	pb "github.com/liliang-cn/drbd-agent/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/client"
)

// PoolManager manages storage pools
type PoolManager struct {
	controller *Controller
}

// NewPoolManager creates a new pool manager
func NewPoolManager(ctrl *Controller) *PoolManager {
	return &PoolManager{controller: ctrl}
}

// ListPools lists all pools across all agents
func (pm *PoolManager) ListPools(ctx context.Context) ([]*PoolInfo, error) {
	var pools []*PoolInfo

	pm.controller.agentsLock.RLock()
	agents := make(map[string]*client.AgentClient)
	for k, v := range pm.controller.agents {
		agents[k] = v
	}
	pm.controller.agentsLock.RUnlock()

	for endpoint, agent := range agents {
		resp, err := agent.VGS(ctx, &pb.VGSRequest{})
		if err != nil {
			pm.controller.logger.Warn("Failed to list pools",
				zap.String("endpoint", endpoint),
				zap.Error(err))
			continue
		}

		for _, vg := range resp.Vgs {
			pools = append(pools, &PoolInfo{
				Name:    vg.VgName,
				Type:    "vg",
				Node:    endpoint,
				TotalGB: vg.VgSize / 1024 / 1024 / 1024,
				FreeGB:  vg.VgFree / 1024 / 1024 / 1024,
			})
		}
	}

	return pools, nil
}

// CreatePool creates a pool on a specific agent
func (pm *PoolManager) CreatePool(ctx context.Context, endpoint, name string, devices []string) error {
	pm.controller.agentsLock.RLock()
	agent := pm.controller.agents[endpoint]
	pm.controller.agentsLock.RUnlock()

	if agent == nil {
		return fmt.Errorf("agent not found: %s", endpoint)
	}

	req := &pb.VGCreateRequest{
		VgName:         name,
		PhysicalVolumes: devices,
	}

	resp, err := agent.VGCreate(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create pool: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("failed to create pool: %s", resp.Message)
	}

	pm.controller.logger.Info("Pool created",
		zap.String("name", name),
		zap.String("endpoint", endpoint))

	return nil
}

// PoolInfo represents pool information
type PoolInfo struct {
	Name    string
	Type    string
	Node    string
	TotalGB uint64
	FreeGB  uint64
	Devices []string
}
