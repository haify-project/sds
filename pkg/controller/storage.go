package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"go.uber.org/zap"
)

// PoolInfo represents pool information
type PoolInfo struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Node    string   `json:"node"`
	TotalGB uint64   `json:"total_gb"`
	FreeGB  uint64   `json:"free_gb"`
	Devices []string `json:"devices"`
}

// StorageManager manages all storage operations
type StorageManager struct {
	controller *Controller
	mu         sync.RWMutex
}

// NewStorageManager creates a new storage manager
func NewStorageManager(ctrl *Controller) *StorageManager {
	return &StorageManager{
		controller: ctrl,
	}
}

// ==================== POOL OPERATIONS ====================

// CreatePool creates a storage pool
func (sm *StorageManager) CreatePool(ctx context.Context, name, poolType, node string, disks []string, sizeGB uint64) error {
	sm.controller.logger.Info("Creating pool",
		zap.String("name", name),
		zap.String("type", poolType),
		zap.String("node", node),
		zap.Strings("disks", disks))

	// Convert node name to address
	address := sm.controller.nodes.GetNodeAddressByName(node)
	if address == "" {
		return fmt.Errorf("node not found: %s", node)
	}

	// Create PVs first
	for _, disk := range disks {
		result, err := sm.controller.deployment.PVCreate(ctx, []string{address}, disk)
		if err != nil {
			return fmt.Errorf("failed to create PV on %s: %w", disk, err)
		}
		if !result.AllSuccess() {
			return fmt.Errorf("PV creation failed on %s for disk %s: %v", node, disk, result.FailedHosts())
		}
	}

	// Create VG
	result, err := sm.controller.deployment.VGCreate(ctx, []string{address}, name, disks)
	if err != nil {
		return fmt.Errorf("failed to create pool: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to create pool: %v", result.FailedHosts())
	}

	sm.controller.logger.Info("Pool created successfully",
		zap.String("name", name),
		zap.String("node", node))

	return nil
}

// GetPool gets pool information
func (sm *StorageManager) GetPool(ctx context.Context, poolName, node string) (*PoolInfo, error) {
	result, err := sm.controller.deployment.Exec(ctx, []string{node}, "sudo vgs --noheadings --units b --separator '|' -o vg_name,vg_size,vg_free")
	if err != nil {
		return nil, fmt.Errorf("failed to get pool: %w", err)
	}

	if !result.AllSuccess() {
		return nil, fmt.Errorf("failed to get pool: %v", result.FailedHosts())
	}

	// Parse VGS output
	for _, r := range result.Hosts {
		if r.Success {
			lines := strings.Split(strings.TrimSpace(r.Output), "\n")
			for _, line := range lines {
				fields := strings.Split(line, "|")
				if len(fields) >= 4 && strings.TrimSpace(fields[0]) == poolName {
					totalSize, _ := strconv.ParseUint(strings.TrimSpace(fields[1]), 10, 64)
					freeSize, _ := strconv.ParseUint(strings.TrimSpace(fields[2]), 10, 64)
					return &PoolInfo{
						Name:    poolName,
						Type:    "vg",
						Node:    node,
						TotalGB: totalSize / 1024 / 1024 / 1024,
						FreeGB:  freeSize / 1024 / 1024 / 1024,
						Devices: []string{},
					}, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("pool not found: %s", poolName)
}

// ListPools lists all pools across all nodes
func (sm *StorageManager) ListPools(ctx context.Context) ([]*PoolInfo, error) {
	var pools []*PoolInfo
	// Use map to deduplicate by normalized node name
	seen := make(map[string]bool)

	hosts := sm.controller.GetHosts()
	if len(hosts) == 0 {
		return pools, nil
	}

	result, err := sm.controller.deployment.Exec(ctx, hosts, "sudo vgs --noheadings --units b --separator '|' -o vg_name,vg_size,vg_free")
	if err != nil {
		return nil, fmt.Errorf("failed to list pools: %w", err)
	}

	for host, r := range result.Hosts {
		if r.Success {
			// Normalize host: use hostname if available, otherwise use the host as-is
			normalizedHost := sm.controller.NormalizeHost(host)
			if normalizedHost == "" {
				normalizedHost = host
			}

			lines := strings.Split(strings.TrimSpace(r.Output), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				fields := strings.Split(line, "|")
				if len(fields) >= 3 {
					vgName := strings.TrimSpace(fields[0])
					// Create unique key for deduplication
					key := normalizedHost + "/" + vgName
					if seen[key] {
						continue
					}
					seen[key] = true

					// Parse size, remove 'B' suffix if present
					totalSizeStr := strings.TrimSpace(strings.TrimSuffix(fields[1], "B"))
					freeSizeStr := strings.TrimSpace(strings.TrimSuffix(fields[2], "B"))
					totalSize, _ := strconv.ParseUint(totalSizeStr, 10, 64)
					freeSize, _ := strconv.ParseUint(freeSizeStr, 10, 64)
					pools = append(pools, &PoolInfo{
						Name:    vgName,
						Type:    "vg",
						Node:    normalizedHost,
						TotalGB: totalSize / 1024 / 1024 / 1024,
						FreeGB:  freeSize / 1024 / 1024 / 1024,
					})
				}
			}
		}
	}

	return pools, nil
}

// AddDiskToPool adds a disk to a pool
func (sm *StorageManager) AddDiskToPool(ctx context.Context, pool, disk, node string) error {
	// Create PV first
	result, err := sm.controller.deployment.PVCreate(ctx, []string{node}, disk)
	if err != nil {
		return fmt.Errorf("failed to create PV: %w", err)
	}
	if !result.AllSuccess() {
		return fmt.Errorf("PV creation failed: %v", result.FailedHosts())
	}

	// Extend VG
	cmd := fmt.Sprintf("sudo vgextend %s %s", pool, disk)
	result, err = sm.controller.deployment.Exec(ctx, []string{node}, cmd)
	if err != nil {
		return fmt.Errorf("failed to add disk: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to add disk: %v", result.FailedHosts())
	}

	sm.controller.logger.Info("Disk added to pool",
		zap.String("pool", pool),
		zap.String("disk", disk),
		zap.String("node", node))

	return nil
}

// DeletePool deletes a storage pool
func (sm *StorageManager) DeletePool(ctx context.Context, name, node string) error {
	sm.controller.logger.Info("Deleting pool",
		zap.String("name", name),
		zap.String("node", node))

	// Remove VG using LVM
	cmd := fmt.Sprintf("sudo vgremove -f %s", name)
	result, err := sm.controller.deployment.Exec(ctx, []string{node}, cmd)
	if err != nil {
		return fmt.Errorf("failed to delete pool: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to delete pool: %v", result.FailedHosts())
	}

	sm.controller.logger.Info("Pool deleted successfully",
		zap.String("name", name),
		zap.String("node", node))

	return nil
}
