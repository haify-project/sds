package controller

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	pb "github.com/liliang-cn/drbd-agent/api/proto/v1"
)

// VolumeManager manages volumes
type VolumeManager struct {
	controller *Controller
}

// NewVolumeManager creates a new volume manager
func NewVolumeManager(ctrl *Controller) *VolumeManager {
	return &VolumeManager{controller: ctrl}
}

// ListVolumes lists volumes in a pool
func (vm *VolumeManager) ListVolumes(ctx context.Context, endpoint, pool string) ([]*VolumeInfo, error) {
	vm.controller.agentsLock.RLock()
	agent := vm.controller.agents[endpoint]
	vm.controller.agentsLock.RUnlock()

	if agent == nil {
		return nil, fmt.Errorf("agent not found: %s", endpoint)
	}

	req := &pb.LVSRequest{
		VgName: pool,
	}

	resp, err := agent.LVS(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to list volumes: %w", err)
	}

	if !resp.Success {
		return nil, fmt.Errorf("failed to list volumes: %s", resp.Message)
	}

	var volumes []*VolumeInfo
	for _, lv := range resp.Lvs {
		volumes = append(volumes, &VolumeInfo{
			Name:    lv.LvName,
			Path:    lv.LvPath,
			Pool:    lv.VgName,
			SizeGB:  lv.LvSize / 1024 / 1024 / 1024,
			Thin:    lv.PoolLv != "",
		})
	}

	return volumes, nil
}

// CreateVolume creates a volume
func (vm *VolumeManager) CreateVolume(ctx context.Context, endpoint, pool, name string, sizeGB uint64, thin bool) (*VolumeInfo, error) {
	vm.controller.agentsLock.RLock()
	agent := vm.controller.agents[endpoint]
	vm.controller.agentsLock.RUnlock()

	if agent == nil {
		return nil, fmt.Errorf("agent not found: %s", endpoint)
	}

	req := &pb.LVCreateRequest{
		VgName:     pool,
		LvName:     name,
		SizeSuffix: fmt.Sprintf("%dG", sizeGB),
		Thin:       thin,
	}

	resp, err := agent.LVCreate(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create volume: %w", err)
	}

	if !resp.Success {
		return nil, fmt.Errorf("failed to create volume: %s", resp.Message)
	}

	// Build the expected device path
	devicePath := fmt.Sprintf("/dev/%s/%s", pool, name)

	vm.controller.logger.Info("Volume created",
		zap.String("pool", pool),
		zap.String("name", name),
		zap.String("path", devicePath),
		zap.Uint64("size_gb", sizeGB))

	return &VolumeInfo{
		Name:   name,
		Path:   devicePath,
		Pool:   pool,
		SizeGB: sizeGB,
		Thin:   thin,
	}, nil
}

// DeleteVolume deletes a volume
func (vm *VolumeManager) DeleteVolume(ctx context.Context, endpoint, path string) error {
	vm.controller.agentsLock.RLock()
	agent := vm.controller.agents[endpoint]
	vm.controller.agentsLock.RUnlock()

	if agent == nil {
		return fmt.Errorf("agent not found: %s", endpoint)
	}

	req := &pb.LVRemoveRequest{
		LvPath: path,
		Force:  true,
	}

	resp, err := agent.LVRemove(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to delete volume: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("failed to delete volume: %s", resp.Message)
	}

	vm.controller.logger.Info("Volume deleted", zap.String("path", path))

	return nil
}

// VolumeInfo represents volume information
type VolumeInfo struct {
	Name   string
	Path   string
	Pool   string
	SizeGB uint64
	Thin   bool
}
