package controller

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	pb "github.com/liliang-cn/drbd-agent/api/proto/v1"
)

// CreatePoolOnNode creates a pool on a specific node via drbd-agent
func (pm *PoolManager) CreatePoolOnNode(ctx context.Context, nodeName, name string, poolType string, disks []string, sizeGB uint64) error {
	pm.controller.agentsLock.RLock()
	agent := pm.controller.agents[nodeName]
	pm.controller.agentsLock.RUnlock()

	if agent == nil {
		return fmt.Errorf("agent not found for node: %s", nodeName)
	}

	if poolType == "vg" {
		req := &pb.VGCreateRequest{
			VgName:         name,
			PhysicalVolumes: disks,
		}

		resp, err := agent.VGCreate(ctx, req)
		if err != nil {
			return fmt.Errorf("VGCreate call failed: %w", err)
		}

		if !resp.Success {
			return fmt.Errorf("failed to create VG: %s", resp.Message)
		}

		pm.controller.logger.Info("Pool created",
			zap.String("name", name),
			zap.String("node", nodeName),
			zap.Strings("disks", disks))

		return nil
	}

	return fmt.Errorf("unsupported pool type: %s", poolType)
}

// GetPoolOnNode gets pool info from a specific node
func (pm *PoolManager) GetPoolOnNode(ctx context.Context, nodeName, name string) (*PoolInfo, error) {
	pm.controller.agentsLock.RLock()
	agent := pm.controller.agents[nodeName]
	pm.controller.agentsLock.RUnlock()

	if agent == nil {
		return nil, fmt.Errorf("agent not found for node: %s", nodeName)
	}

	req := &pb.VGSRequest{}
	resp, err := agent.VGS(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("VGS call failed: %w", err)
	}

	if !resp.Success {
		return nil, fmt.Errorf("failed to list VGs: %s", resp.Message)
	}

	for _, vg := range resp.Vgs {
		if vg.VgName == name {
			return &PoolInfo{
				Name:    vg.VgName,
				Type:    "vg",
				Node:    nodeName,
				TotalGB: vg.VgSize / 1024 / 1024 / 1024,
				FreeGB:  vg.VgFree / 1024 / 1024 / 1024,
				Devices: []string{}, // In production, call PVDisplay to get actual PV names
			}, nil
		}
	}

	return nil, fmt.Errorf("pool not found: %s", name)
}
