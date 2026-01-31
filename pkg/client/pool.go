// Package client provides high-level client operations
package client

import (
	"context"
	"fmt"

	pb "github.com/liliang-cn/drbd-agent/api/proto/v1"
)

// PoolInfo represents storage pool information
type PoolInfo struct {
	Name    string
	Type    string // "vg" or "thin_pool"
	TotalGB uint64
	UsedGB  uint64
	FreeGB  uint64
	Devices []string
}

// VolumeInfo represents volume information
type VolumeInfo struct {
	Name   string
	Path   string // e.g., /dev/vg0/lv1
	Pool   string
	SizeGB uint64
	Thin   bool
}

// CreatePool creates a storage pool (volume group)
func (c *AgentClient) CreatePool(ctx context.Context, name string, poolType string, devices []string) error {
	if poolType == "vg" {
		req := &pb.VGCreateRequest{
			VgName:         name,
			PhysicalVolumes: devices,
		}
		resp, err := c.VGCreate(ctx, req)
		if err != nil {
			return fmt.Errorf("failed to create VG %s: %w", name, err)
		}
		if !resp.Success {
			return fmt.Errorf("failed to create VG %s: %s", name, resp.Message)
		}
		return nil
	}
	return fmt.Errorf("unsupported pool type: %s", poolType)
}

// ListPools lists all storage pools
func (c *AgentClient) ListPools(ctx context.Context) ([]PoolInfo, error) {
	req := &pb.VGSRequest{}
	resp, err := c.VGS(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to list VGs: %w", err)
	}

	if !resp.Success {
		return nil, fmt.Errorf("failed to list VGs: %s", resp.Message)
	}

	pools := make([]PoolInfo, len(resp.Vgs))
	for i, vg := range resp.Vgs {
		pools[i] = PoolInfo{
			Name:    vg.VgName,
			Type:    "vg",
			TotalGB: vg.VgSize / 1024 / 1024 / 1024,
			FreeGB:  vg.VgFree / 1024 / 1024 / 1024,
		}
	}
	return pools, nil
}

// CreateVolume creates a volume in a pool
func (c *AgentClient) CreateVolume(ctx context.Context, pool, name string, sizeGB uint64, thin bool) (string, error) {
	req := &pb.LVCreateRequest{
		VgName:     pool,
		LvName:     name,
		SizeSuffix: fmt.Sprintf("%dG", sizeGB),
		Thin:       thin,
	}

	resp, err := c.LVCreate(ctx, req)
	if err != nil {
		return "", fmt.Errorf("failed to create LV %s/%s: %w", pool, name, err)
	}

	if !resp.Success {
		return "", fmt.Errorf("failed to create LV %s/%s: %s", pool, name, resp.Message)
	}

	// Return the expected path
	return fmt.Sprintf("/dev/%s/%s", pool, name), nil
}

// DeleteVolume deletes a volume
func (c *AgentClient) DeleteVolume(ctx context.Context, path string) error {
	req := &pb.LVRemoveRequest{
		LvPath: path,
		Force:  true,
	}

	resp, err := c.LVRemove(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to remove LV %s: %w", path, err)
	}

	if !resp.Success {
		return fmt.Errorf("failed to remove LV %s: %s", path, resp.Message)
	}

	return nil
}

// ListVolumes lists all volumes in a pool
func (c *AgentClient) ListVolumes(ctx context.Context, pool string) ([]VolumeInfo, error) {
	req := &pb.LVSRequest{
		VgName: pool,
	}

	resp, err := c.LVS(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to list LVs: %w", err)
	}

	if !resp.Success {
		return nil, fmt.Errorf("failed to list LVs: %s", resp.Message)
	}

	volumes := make([]VolumeInfo, len(resp.Lvs))
	for i, lv := range resp.Lvs {
		sizeGB := lv.LvSize / 1024 / 1024 / 1024
		volumes[i] = VolumeInfo{
			Name:   lv.LvName,
			Path:   lv.LvPath,
			Pool:   lv.VgName,
			SizeGB: sizeGB,
			Thin:   lv.PoolLv != "",
		}
	}
	return volumes, nil
}
