package controller

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/deployment"
	"go.uber.org/zap"
)

// PoolInfo represents pool information
type PoolInfo struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"` // "vg" or "zfs"
	Node        string   `json:"node"`
	TotalGB     uint64   `json:"total_gb"`
	FreeGB      uint64   `json:"free_gb"`
	Devices     []string `json:"devices"`
	Thin        bool     `json:"thin"`
	Compression string   `json:"compression,omitempty"`
}

// StorageManager manages all storage operations
type StorageManager struct {
	controller *Controller
}

// NewStorageManager creates a new storage manager
func NewStorageManager(ctrl *Controller) *StorageManager {
	return &StorageManager{
		controller: ctrl,
	}
}

func poolInfoFromDB(pool *database.Pool) *PoolInfo {
	var devices []string
	if pool.Devices != "" {
		for _, device := range strings.Split(pool.Devices, ",") {
			device = strings.TrimSpace(device)
			if device != "" {
				devices = append(devices, device)
			}
		}
	}

	return &PoolInfo{
		Name:    pool.Name,
		Type:    pool.Type,
		Node:    pool.Node,
		TotalGB: uint64(max(pool.TotalGB, 0)),
		FreeGB:  uint64(max(pool.FreeGB, 0)),
		Devices: devices,
		Thin:    pool.Type == "thin_pool" || pool.Type == "zfs-thin",
	}
}

func isZFSPoolType(poolType string) bool {
	return poolType == "zfs" || poolType == "zfs-thin"
}

func (sm *StorageManager) getPersistedPool(ctx context.Context, name string) (*PoolInfo, error) {
	if sm.controller.db == nil {
		return nil, fmt.Errorf("database not available")
	}

	pool, err := sm.controller.db.GetPool(ctx, name)
	if err != nil {
		return nil, err
	}

	return poolInfoFromDB(pool), nil
}

func (sm *StorageManager) listPersistedPools(ctx context.Context) ([]*PoolInfo, error) {
	if sm.controller.db == nil {
		return nil, fmt.Errorf("database not available")
	}

	pools, err := sm.controller.db.ListPools(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]*PoolInfo, 0, len(pools))
	for _, pool := range pools {
		result = append(result, poolInfoFromDB(pool))
	}
	return result, nil
}

func normalizeDeviceList(devices []string) []string {
	seen := make(map[string]struct{})
	var result []string
	for _, device := range devices {
		device = strings.TrimSpace(device)
		if device == "" {
			continue
		}
		if _, exists := seen[device]; exists {
			continue
		}
		seen[device] = struct{}{}
		result = append(result, device)
	}
	slices.Sort(result)
	return result
}

// ==================== POOL OPERATIONS ====================

// CreatePool creates a storage pool
func (sm *StorageManager) CreatePool(ctx context.Context, name, poolType, node string, disks []string, sizeGB uint64) error {
	normalizedType, err := normalizeLVMPoolType(poolType)
	if err != nil {
		return err
	}
	name = normalizeManagedName(name)

	sm.controller.logger.Info("Creating pool",
		zap.String("name", name),
		zap.String("type", normalizedType),
		zap.String("node", node),
		zap.Strings("disks", disks))

	address := sm.controller.ResolveHost(node)
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

	// If type is thin_pool, create a thin pool LV
	if normalizedType == "thin_pool" {
		// Use 95% of VG size for thin pool to leave metadata space
		// Since we don't know exact size here easily without querying, we might use the passed sizeGB if > 0
		// or default to 95%FREE if sizeGB is 0 (which implies full disk).
		// For now, let's assume sizeGB is passed or use "95%FREE" syntax if deployment supports it.
		// deployment.LVCreateThinPool takes a size string.

		thinPoolName := name + "_thin"
		thinSize := "95%FREE"
		if sizeGB > 0 {
			thinSize = fmt.Sprintf("%dG", sizeGB)
		}

		tpResult, err := sm.controller.deployment.LVCreateThinPool(ctx, []string{address}, name, thinPoolName, thinSize)
		if err != nil {
			return fmt.Errorf("failed to create thin pool: %w", err)
		}
		if !tpResult.AllSuccess() {
			return fmt.Errorf("failed to create thin pool: %v", tpResult.FailedHosts())
		}
	}

	sm.controller.logger.Info("Pool created successfully",
		zap.String("name", name),
		zap.String("node", node))

	if sm.controller.db != nil {
		dbPool := &database.Pool{
			Name:    name,
			Type:    normalizedType,
			Node:    node,
			Devices: strings.Join(disks, ","),
		}
		if err := sm.controller.db.SavePool(ctx, dbPool); err != nil {
			sm.controller.logger.Warn("Failed to save pool to database",
				zap.String("name", name),
				zap.Error(err))
		}
	}

	return nil
}

// GetPool gets pool information
func (sm *StorageManager) GetPool(ctx context.Context, poolName, node string) (*PoolInfo, error) {
	poolName = normalizeManagedName(poolName)
	persisted, persistedErr := sm.getPersistedPool(ctx, poolName)
	if persistedErr == nil && isZFSPoolType(persisted.Type) {
		return sm.GetZFSPool(ctx, poolName, node)
	}

	address := sm.controller.ResolveHost(node)
	result, err := sm.controller.deployment.Exec(ctx, []string{address}, "sudo vgs --noheadings --units b --separator '|' -o vg_name,vg_size,vg_free")
	if err != nil {
		if zfsPool, zfsErr := sm.GetZFSPool(ctx, poolName, node); zfsErr == nil {
			return zfsPool, nil
		}
		if persistedErr == nil {
			return persisted, nil
		}
		return nil, fmt.Errorf("failed to get pool: %w", err)
	}

	if !result.AllSuccess() {
		if zfsPool, zfsErr := sm.GetZFSPool(ctx, poolName, node); zfsErr == nil {
			return zfsPool, nil
		}
		if persistedErr == nil {
			return persisted, nil
		}
		return nil, fmt.Errorf("failed to get pool: %v", result.FailedHosts())
	}

	// Parse VGS output
	for _, r := range result.Hosts {
		if r.Success {
			lines := strings.Split(strings.TrimSpace(r.Output), "\n")
			for _, line := range lines {
				name, totalSize, freeSize, ok := parseLVMPoolLine(line)
				if ok && name == poolName {
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

	if zfsPool, zfsErr := sm.GetZFSPool(ctx, poolName, node); zfsErr == nil {
		return zfsPool, nil
	}

	if persistedErr == nil {
		return persisted, nil
	}

	return nil, fmt.Errorf("pool not found: %s", poolName)
}

// ListPools lists all pools across all nodes (LVM and ZFS)
func (sm *StorageManager) ListPools(ctx context.Context) ([]*PoolInfo, error) {
	var pools []*PoolInfo
	// Use map to deduplicate by normalized node name
	seen := make(map[string]bool)

	hosts := sm.controller.GetHosts()
	if len(hosts) == 0 {
		if persisted, err := sm.listPersistedPools(ctx); err == nil {
			return persisted, nil
		}
		return pools, nil
	}

	// 1. Get LVM pools
	result, err := sm.controller.deployment.Exec(ctx, hosts, "sudo vgs --noheadings --units b --separator '|' -o vg_name,vg_size,vg_free")
	if err != nil {
		// Log error but continue to try ZFS
		sm.controller.logger.Warn("Failed to list LVM pools", zap.Error(err))
	} else {
		for host, r := range result.Hosts {
			if r.Success {
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
					vgName, totalSize, freeSize, ok := parseLVMPoolLine(line)
					if !ok || !strings.HasPrefix(vgName, managedNamePrefix) {
						continue
					}
					key := normalizedHost + "/lvm/" + vgName
					if seen[key] {
						continue
					}
					seen[key] = true

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

	// 2. Get ZFS pools
	zfsPools, err := sm.ListZFSpools(ctx)
	if err != nil {
		sm.controller.logger.Warn("Failed to list ZFS pools", zap.Error(err))
	} else {
		pools = append(pools, zfsPools...)
	}

	if len(pools) == 0 {
		if persisted, err := sm.listPersistedPools(ctx); err == nil {
			return persisted, nil
		}
	}

	return pools, nil
}

// AddDiskToPool adds a disk to a pool
func (sm *StorageManager) AddDiskToPool(ctx context.Context, pool, disk, node string) error {
	pool = normalizeManagedName(pool)
	address := sm.controller.ResolveHost(node)

	// Create PV first
	result, err := sm.controller.deployment.PVCreate(ctx, []string{address}, disk)
	if err != nil {
		return fmt.Errorf("failed to create PV: %w", err)
	}
	if !result.AllSuccess() {
		return fmt.Errorf("PV creation failed: %v", result.FailedHosts())
	}

	// Extend VG
	cmd := fmt.Sprintf("sudo vgextend %s %s", pool, disk)
	result, err = sm.controller.deployment.Exec(ctx, []string{address}, cmd)
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

	if sm.controller.db != nil {
		if dbPool, err := sm.controller.db.GetPool(ctx, pool); err == nil {
			devices := strings.Split(strings.Trim(dbPool.Devices, ","), ",")
			devices = append(devices, disk)
			dbPool.Devices = strings.Join(normalizeDeviceList(devices), ",")
			if err := sm.controller.db.SavePool(ctx, dbPool); err != nil {
				sm.controller.logger.Warn("Failed to update pool devices in database",
					zap.String("pool", pool),
					zap.Error(err))
			}
		}
	}

	return nil
}

// DeletePool deletes a storage pool
func (sm *StorageManager) DeletePool(ctx context.Context, name, node string) error {
	name = normalizeManagedName(name)
	if persisted, err := sm.getPersistedPool(ctx, name); err == nil && isZFSPoolType(persisted.Type) {
		return sm.DeleteZFSPool(ctx, name, node)
	}

	address := sm.controller.ResolveHost(node)

	sm.controller.logger.Info("Deleting pool",
		zap.String("name", name),
		zap.String("node", node))

	// Remove VG using LVM
	cmd := fmt.Sprintf("sudo vgremove -f %s", name)
	result, err := sm.controller.deployment.Exec(ctx, []string{address}, cmd)
	if err != nil {
		if zfsErr := sm.DeleteZFSPool(ctx, name, node); zfsErr == nil {
			return nil
		}
		return fmt.Errorf("failed to delete pool: %w", err)
	}

	if !result.AllSuccess() {
		if zfsErr := sm.DeleteZFSPool(ctx, name, node); zfsErr == nil {
			return nil
		}
		return fmt.Errorf("failed to delete pool: %v", result.FailedHosts())
	}

	sm.controller.logger.Info("Pool deleted successfully",
		zap.String("name", name),
		zap.String("node", node))

	if sm.controller.db != nil {
		if err := sm.controller.db.DeletePool(ctx, name); err != nil {
			sm.controller.logger.Warn("Failed to delete pool from database",
				zap.String("name", name),
				zap.Error(err))
		}
	}

	return nil
}

// ==================== ZFS POOL OPERATIONS ====================

// CreateZFSPool creates a ZFS storage pool
func (sm *StorageManager) CreateZFSPool(ctx context.Context, name, node string, vdevs []string, thin bool) error {
	name = normalizeManagedName(name)

	sm.controller.logger.Info("Creating ZFS pool",
		zap.String("name", name),
		zap.String("node", node),
		zap.Strings("vdevs", vdevs),
		zap.Bool("thin", thin))

	// Convert node name to address
	address := sm.controller.ResolveHost(node)

	// Create ZFS pool
	var opts []deployment.ZFSOption
	if thin {
		opts = append(opts, deployment.WithZFSThin(true))
	}
	result, err := sm.controller.deployment.ZFSCreatePool(ctx, []string{address}, name, vdevs, opts...)
	if err != nil {
		return fmt.Errorf("failed to create ZFS pool: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to create ZFS pool: %v", result.FailedHosts())
	}

	sm.controller.logger.Info("ZFS pool created successfully",
		zap.String("name", name),
		zap.String("node", node))

	if sm.controller.db != nil {
		poolType := "zfs"
		if thin {
			poolType = "zfs-thin"
		}
		dbPool := &database.Pool{
			Name:    name,
			Type:    poolType,
			Node:    node,
			Devices: strings.Join(vdevs, ","),
		}
		if err := sm.controller.db.SavePool(ctx, dbPool); err != nil {
			sm.controller.logger.Warn("Failed to save ZFS pool to database",
				zap.String("name", name),
				zap.Error(err))
		}
	}

	return nil
}

// GetZFSPool gets ZFS pool information
func (sm *StorageManager) GetZFSPool(ctx context.Context, poolName, node string) (*PoolInfo, error) {
	poolName = normalizeManagedName(poolName)
	address := sm.controller.ResolveHost(node)
	result, err := sm.controller.deployment.Exec(ctx, []string{address},
		fmt.Sprintf("sudo zpool list -Hp -o name,size,free,cap %s", poolName))
	if err != nil {
		if persisted, dbErr := sm.getPersistedPool(ctx, poolName); dbErr == nil {
			return persisted, nil
		}
		return nil, fmt.Errorf("failed to get ZFS pool: %w", err)
	}

	if !result.AllSuccess() {
		if persisted, dbErr := sm.getPersistedPool(ctx, poolName); dbErr == nil {
			return persisted, nil
		}
		return nil, fmt.Errorf("failed to get ZFS pool: %v", result.FailedHosts())
	}

	for _, r := range result.Hosts {
		if r.Success && r.Output != "" {
			fields := strings.Fields(r.Output)
			if len(fields) >= 4 {
				totalSize, _ := strconv.ParseUint(fields[1], 10, 64)
				freeSize, _ := strconv.ParseUint(fields[2], 10, 64)
				return &PoolInfo{
					Name:    poolName,
					Type:    "zfs",
					Node:    node,
					TotalGB: totalSize / 1024 / 1024 / 1024,
					FreeGB:  freeSize / 1024 / 1024 / 1024,
					Devices: []string{},
				}, nil
			}
		}
	}

	if persisted, err := sm.getPersistedPool(ctx, poolName); err == nil {
		return persisted, nil
	}

	return nil, fmt.Errorf("ZFS pool not found: %s", poolName)
}

// ListZFSpools lists all ZFS pools across all nodes
func (sm *StorageManager) ListZFSpools(ctx context.Context) ([]*PoolInfo, error) {
	var pools []*PoolInfo
	seen := make(map[string]bool)

	hosts := sm.controller.GetHosts()
	if len(hosts) == 0 {
		if persisted, err := sm.listPersistedPools(ctx); err == nil {
			var zfsPools []*PoolInfo
			for _, pool := range persisted {
				if pool.Type == "zfs" || pool.Type == "zfs-thin" {
					zfsPools = append(zfsPools, pool)
				}
			}
			return zfsPools, nil
		}
		return pools, nil
	}

	result, err := sm.controller.deployment.ZFSListPools(ctx, hosts)
	if err != nil {
		return nil, fmt.Errorf("failed to list ZFS pools: %w", err)
	}

	for host, r := range result.Hosts {
		if r.Success {
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
				fields := strings.Fields(line)
				if len(fields) >= 4 {
					poolName := fields[0]
					// Only show SDS-managed pools (with sds_ prefix)
					if !strings.HasPrefix(poolName, "sds_") {
						continue
					}
					key := normalizedHost + "/" + poolName
					if seen[key] {
						continue
					}
					seen[key] = true

					totalSize, _ := strconv.ParseUint(fields[1], 10, 64)
					freeSize, _ := strconv.ParseUint(fields[2], 10, 64)
					pools = append(pools, &PoolInfo{
						Name:    poolName,
						Type:    "zfs",
						Node:    normalizedHost,
						TotalGB: totalSize / 1024 / 1024 / 1024,
						FreeGB:  freeSize / 1024 / 1024 / 1024,
					})
				}
			}
		}
	}

	if len(pools) == 0 {
		if persisted, err := sm.listPersistedPools(ctx); err == nil {
			var zfsPools []*PoolInfo
			for _, pool := range persisted {
				if pool.Type == "zfs" || pool.Type == "zfs-thin" {
					zfsPools = append(zfsPools, pool)
				}
			}
			return zfsPools, nil
		}
	}

	return pools, nil
}

// DeleteZFSPool deletes a ZFS storage pool
func (sm *StorageManager) DeleteZFSPool(ctx context.Context, name, node string) error {
	name = normalizeManagedName(name)
	address := sm.controller.ResolveHost(node)

	sm.controller.logger.Info("Deleting ZFS pool",
		zap.String("name", name),
		zap.String("node", node))

	result, err := sm.controller.deployment.ZFSDestroyPool(ctx, []string{address}, name)
	if err != nil {
		return fmt.Errorf("failed to delete ZFS pool: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to delete ZFS pool: %v", result.FailedHosts())
	}

	sm.controller.logger.Info("ZFS pool deleted successfully",
		zap.String("name", name),
		zap.String("node", node))

	if sm.controller.db != nil {
		if err := sm.controller.db.DeletePool(ctx, name); err != nil {
			sm.controller.logger.Warn("Failed to delete ZFS pool from database",
				zap.String("name", name),
				zap.Error(err))
		}
	}

	return nil
}

// CreateZFSDataset creates a ZFS dataset
func (sm *StorageManager) CreateZFSDataset(ctx context.Context, datasetPath, node string) error {
	datasetPath = normalizeManagedZFSPath(datasetPath)
	address := sm.controller.ResolveHost(node)

	sm.controller.logger.Info("Creating ZFS dataset",
		zap.String("dataset", datasetPath),
		zap.String("node", node))

	result, err := sm.controller.deployment.ZFSCreateDataset(ctx, []string{address}, datasetPath)
	if err != nil {
		return fmt.Errorf("failed to create ZFS dataset: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to create ZFS dataset: %v", result.FailedHosts())
	}

	return nil
}

// ZFSDeleteDataset destroys a ZFS dataset or volume
func (sm *StorageManager) ZFSDeleteDataset(ctx context.Context, datasetPath, node string) error {
	datasetPath = normalizeManagedZFSPath(datasetPath)
	address := sm.controller.ResolveHost(node)

	sm.controller.logger.Info("Deleting ZFS dataset",
		zap.String("dataset", datasetPath),
		zap.String("node", node))

	result, err := sm.controller.deployment.ZFSDestroyDataset(ctx, []string{address}, datasetPath)
	if err != nil {
		return fmt.Errorf("failed to delete ZFS dataset: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to delete ZFS dataset: %v", result.FailedHosts())
	}

	return nil
}

// CreateZFSThinVolume creates a thin-provisioned ZFS volume
func (sm *StorageManager) CreateZFSThinVolume(ctx context.Context, poolName, volumeName, size, node string) error {
	poolName = normalizeManagedName(poolName)
	address := sm.controller.ResolveHost(node)

	sm.controller.logger.Info("Creating ZFS thin volume",
		zap.String("pool", poolName),
		zap.String("volume", volumeName),
		zap.String("size", size),
		zap.String("node", node))

	volumePath := fmt.Sprintf("%s/%s", poolName, volumeName)
	result, err := sm.controller.deployment.ZFSCreateThinDataset(ctx, []string{address}, poolName, volumeName, size)
	if err != nil {
		return fmt.Errorf("failed to create ZFS thin volume: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to create ZFS thin volume: %v", result.FailedHosts())
	}

	// Set quota for thin provisioning
	_, _ = sm.controller.deployment.ZFSSetQuota(ctx, []string{address}, volumePath, size)

	return nil
}

// ZFSSnapshot creates a ZFS snapshot
func (sm *StorageManager) ZFSSnapshot(ctx context.Context, dataset, snapshotName, node string) error {
	dataset = normalizeManagedZFSPath(dataset)
	address := sm.controller.ResolveHost(node)

	sm.controller.logger.Info("Creating ZFS snapshot",
		zap.String("dataset", dataset),
		zap.String("snapshot", snapshotName),
		zap.String("node", node))

	result, err := sm.controller.deployment.ZFSSnapshot(ctx, []string{address}, dataset, snapshotName)
	if err != nil {
		return fmt.Errorf("failed to create ZFS snapshot: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to create ZFS snapshot: %v", result.FailedHosts())
	}

	return nil
}

// ZFSListSnapshots lists ZFS snapshots for a dataset
func (sm *StorageManager) ZFSListSnapshots(ctx context.Context, dataset, node string) ([]*SnapshotInfo, error) {
	// Resolve node name to address
	dataset = normalizeManagedZFSPath(dataset)
	address := sm.controller.ResolveHost(node)

	sm.controller.logger.Info("Listing ZFS snapshots",
		zap.String("dataset", dataset),
		zap.String("node", node),
		zap.String("address", address))

	result, err := sm.controller.deployment.ZFSListSnapshots(ctx, []string{address}, dataset)
	if err != nil {
		return nil, fmt.Errorf("failed to list ZFS snapshots: %w", err)
	}

	var snapshots []*SnapshotInfo
	for host, r := range result.Hosts {
		if r.Success {
			sm.controller.logger.Info("ZFS snapshot list output",
				zap.String("host", host),
				zap.String("output", r.Output))

			lines := strings.Split(strings.TrimSpace(r.Output), "\n")
			for _, line := range lines {
				if line == "" {
					continue
				}
				name, volume, createdAt, ok := parseZFSSnapshotLine(line)
				if !ok {
					continue
				}
				snapshots = append(snapshots, &SnapshotInfo{
					Name:      name,
					Volume:    volume,
					CreatedAt: createdAt,
				})
			}
		} else {
			sm.controller.logger.Warn("Failed to list ZFS snapshots on host",
				zap.String("host", host),
				zap.String("error", r.Output))
		}
	}

	return snapshots, nil
}

// ZFSDeleteSnapshot deletes a ZFS snapshot
func (sm *StorageManager) ZFSDeleteSnapshot(ctx context.Context, snapshot, node string) error {
	snapshot = normalizeManagedZFSPath(snapshot)
	address := sm.controller.ResolveHost(node)

	sm.controller.logger.Info("Deleting ZFS snapshot",
		zap.String("snapshot", snapshot),
		zap.String("node", node))

	result, err := sm.controller.deployment.ZFSDestroySnapshot(ctx, []string{address}, snapshot)
	if err != nil {
		return fmt.Errorf("failed to delete ZFS snapshot: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to delete ZFS snapshot: %v", result.FailedHosts())
	}

	return nil
}

// ZFSRestoreSnapshot restores a ZFS snapshot (rollback)
func (sm *StorageManager) ZFSRestoreSnapshot(ctx context.Context, dataset, snapshotName, node string) error {
	dataset = normalizeManagedZFSPath(dataset)
	address := sm.controller.ResolveHost(node)

	sm.controller.logger.Info("Restoring ZFS snapshot",
		zap.String("dataset", dataset),
		zap.String("snapshot", snapshotName),
		zap.String("node", node))

	result, err := sm.controller.deployment.ZFSRollback(ctx, []string{address}, dataset, snapshotName)
	if err != nil {
		return fmt.Errorf("failed to restore ZFS snapshot: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to restore ZFS snapshot: %v", result.FailedHosts())
	}

	return nil
}

// ZFSCloneSnapshot creates a clone from a ZFS snapshot
func (sm *StorageManager) ZFSCloneSnapshot(ctx context.Context, snapshot, clonePath, node string) error {
	snapshot = normalizeManagedZFSPath(snapshot)
	clonePath = normalizeManagedZFSPath(clonePath)
	address := sm.controller.ResolveHost(node)

	sm.controller.logger.Info("Cloning ZFS snapshot",
		zap.String("snapshot", snapshot),
		zap.String("clone", clonePath),
		zap.String("node", node))

	result, err := sm.controller.deployment.ZFSClone(ctx, []string{address}, snapshot, clonePath)
	if err != nil {
		return fmt.Errorf("failed to clone ZFS snapshot: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to clone ZFS snapshot: %v", result.FailedHosts())
	}

	return nil
}

// ==================== LVM SNAPSHOT OPERATIONS ====================

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
		return fmt.Errorf("failed to create LVM snapshot: %v", result.FailedHosts())
	}

	return nil
}

// ListLvmSnapshots lists LVM snapshots for a volume
func (sm *StorageManager) ListLvmSnapshots(ctx context.Context, vgName, node string) ([]*SnapshotInfo, error) {
	vgName = normalizeManagedName(vgName)

	// Resolve node address
	address := sm.controller.ResolveHost(node)

	result, err := sm.controller.deployment.LVListSnapshots(ctx, []string{address}, vgName)
	if err != nil {
		return nil, fmt.Errorf("failed to list LVM snapshots: %w", err)
	}

	var snapshots []*SnapshotInfo
	for _, r := range result.Hosts {
		if r.Success {
			lines := strings.Split(strings.TrimSpace(r.Output), "\n")
			for _, line := range lines {
				if line == "" {
					continue
				}
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					snapshots = append(snapshots, &SnapshotInfo{
						Name:   fields[0],
						Volume: vgName, // Using VG name as volume context
						SizeGB: 0,      // LVM list output needs parsing for size
					})
				}
			}
		}
	}

	return snapshots, nil
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
		return fmt.Errorf("failed to delete LVM snapshot: %v", result.FailedHosts())
	}

	return nil
}

// RestoreLvmSnapshot restores an LVM snapshot (merges it back to the origin)
func (sm *StorageManager) RestoreLvmSnapshot(ctx context.Context, vgName, snapshotName, node string) error {
	vgName = normalizeManagedName(vgName)

	sm.controller.logger.Info("Restoring LVM snapshot",
		zap.String("vg_name", vgName),
		zap.String("snapshot", snapshotName),
		zap.String("node", node))

	// Resolve node address
	address := sm.controller.ResolveHost(node)

	result, err := sm.controller.deployment.LVMergeSnapshot(ctx, []string{address}, vgName, snapshotName)
	if err != nil {
		return fmt.Errorf("failed to restore LVM snapshot: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to restore LVM snapshot: %v", result.FailedHosts())
	}

	return nil
}

// ZFSResizeVolume resizes a ZFS volume
func (sm *StorageManager) ZFSResizeVolume(ctx context.Context, volumePath, newSize, node string) error {
	volumePath = normalizeManagedZFSPath(volumePath)
	address := sm.controller.ResolveHost(node)

	sm.controller.logger.Info("Resizing ZFS volume",
		zap.String("volume", volumePath),
		zap.String("size", newSize),
		zap.String("node", node))

	result, err := sm.controller.deployment.ZFSResizeVolume(ctx, []string{address}, volumePath, newSize)
	if err != nil {
		return fmt.Errorf("failed to resize ZFS volume: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to resize ZFS volume: %v", result.FailedHosts())
	}

	return nil
}
