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
	Name string `json:"name"`
	Type string `json:"type"` // "vg" or "zfs"
	Node string `json:"node"`
	// TotalGB and FreeGB are rounded, not truncated. A 20 GiB disk carries PV
	// metadata and leaves the group at 19.996 GiB; truncating reported that as
	// 19 and made a freshly created pool look like it had lost a gigabyte.
	// TotalBytes and FreeBytes carry the exact figures for anything that needs
	// to do arithmetic or render a precise size.
	TotalGB     uint64   `json:"total_gb"`
	FreeGB      uint64   `json:"free_gb"`
	TotalBytes  uint64   `json:"total_bytes"`
	FreeBytes   uint64   `json:"free_bytes"`
	Devices     []string `json:"devices"`
	Thin        bool     `json:"thin"`
	Compression string   `json:"compression,omitempty"`
	// Cache describes the pool's fast tier, or is nil when it has none. See
	// poolcache.go — this is what makes a tiered pool distinguishable from a
	// plain one without a second call.
	Cache *PoolCacheInfo `json:"cache,omitempty"`
	// ThinUsage is the utilisation of the thin pool inside this group, or nil
	// when the group holds none.
	//
	// It is the only capacity figure here that reflects whether writes will
	// succeed. TotalGB and FreeGB describe the *volume group*, and SDS creates
	// its pool with every free extent, so FreeGB is zero for the whole life of
	// such a pool no matter how empty it is. Prefer this when it is present;
	// see poolthin.go.
	ThinUsage *PoolThinInfo `json:"thin_usage,omitempty"`
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
		Thin:    pool.Type == "thin_pool",
	}
}

func isZFSPoolType(poolType string) bool {
	return poolType == "zfs"
}

// bytesToGB converts to whole gibibytes by rounding to nearest rather than
// truncating. See the comment on PoolInfo.TotalGB.
func bytesToGB(b uint64) uint64 {
	const giB = 1024 * 1024 * 1024
	return (b + giB/2) / giB
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

// defaultedPoolType resolves an omitted pool type from the controller's
// configuration.
//
// The policy belongs here rather than in a client. It used to live in sds-cli,
// which substituted "lvm-thin" before the request ever left the process — so the
// CLI created thin pools while every other caller that omitted the type (the
// REST gateway, MCP, the web UI) reached normalizeLVMPoolType with an empty
// string and got a thick group instead. Two entry points, two silently
// different defaults, and storage.default_pool_type — the setting that exists to
// say which one — read by nobody.
//
// An unset configuration falls through to normalizeLVMPoolType's own handling of
// "", which is how a Controller built without config (tests, embedded uses)
// keeps working.
func (sm *StorageManager) defaultedPoolType(poolType string) string {
	if strings.TrimSpace(poolType) != "" {
		return poolType
	}
	if sm.controller == nil || sm.controller.config == nil {
		return poolType
	}
	return sm.controller.config.Storage.DefaultPoolType
}

// CreatePool creates a storage pool
func (sm *StorageManager) CreatePool(ctx context.Context, name, poolType, node string, disks []string, sizeGB uint64) error {
	normalizedType, err := normalizeLVMPoolType(sm.defaultedPoolType(poolType))
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
			return fmt.Errorf("PV creation failed on %s for disk %s: %s", node, disk, result.FailureDetails())
		}
	}

	// Create VG
	result, err := sm.controller.deployment.VGCreate(ctx, []string{address}, name, disks)
	if err != nil {
		return fmt.Errorf("failed to create pool: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to create pool: %s", result.FailureDetails())
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
			return fmt.Errorf("failed to create thin pool: %s", tpResult.FailureDetails())
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
		return nil, fmt.Errorf("failed to get pool: %s", result.FailureDetails())
	}

	// Parse VGS output
	for _, r := range result.Hosts {
		if r.Success {
			lines := strings.Split(strings.TrimSpace(r.Output), "\n")
			for _, line := range lines {
				name, totalSize, freeSize, _, ok := parseLVMPoolLine(line)
				if ok && name == poolName {
					info := &PoolInfo{
						Name:       poolName,
						Type:       "vg",
						Node:       node,
						TotalGB:    bytesToGB(totalSize),
						FreeGB:     bytesToGB(freeSize),
						TotalBytes: totalSize,
						FreeBytes:  freeSize,
						Devices:    []string{},
					}
					// A pool that cannot report its cache is still a pool; the
					// capacity figures above are the reason this call exists.
					if cache, cerr := sm.readPoolCache(ctx, address, poolName); cerr == nil {
						info.Cache = cache
					} else {
						sm.controller.logger.Warn("Failed to read pool cache state",
							zap.String("pool", poolName), zap.Error(cerr))
					}
					// Same tolerance for utilisation, and the same reason it is
					// worth a second call: the vgs figures above cannot say
					// whether a thin pool is about to refuse writes.
					if usage, uerr := sm.readThinUsage(ctx, address, poolName); uerr == nil {
						info.ThinUsage = usage
					} else {
						sm.controller.logger.Warn("Failed to read thin pool usage",
							zap.String("pool", poolName), zap.Error(uerr))
					}
					return info, nil
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

	// 1. Get LVM pools. Adding pv_name to the vgs output makes vgs emit one row
	// per physical volume (vg_name/size/free repeated), so a single SSH round
	// yields both the capacity AND the devices backing each VG — no separate
	// pvs call. Rows for the same VG are folded into one PoolInfo, collecting
	// its devices.
	poolByKey := make(map[string]*PoolInfo)
	result, err := sm.controller.deployment.Exec(ctx, hosts, "sudo vgs --noheadings --units b --separator '|' -o vg_name,vg_size,vg_free,pv_name")
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
					vgName, totalSize, freeSize, pv, ok := parseLVMPoolLine(line)
					if !ok || !strings.HasPrefix(vgName, managedNamePrefix) {
						continue
					}
					key := normalizedHost + "/lvm/" + vgName
					pool, exists := poolByKey[key]
					if !exists {
						pool = &PoolInfo{
							Name:       vgName,
							Type:       "vg",
							Node:       normalizedHost,
							TotalGB:    bytesToGB(totalSize),
							FreeGB:     bytesToGB(freeSize),
							TotalBytes: totalSize,
							FreeBytes:  freeSize,
						}
						poolByKey[key] = pool
						seen[key] = true
						pools = append(pools, pool)
					}
					if pv != "" {
						pool.Devices = append(pool.Devices, pv)
					}
				}
			}
		}
		for _, pool := range poolByKey {
			slices.Sort(pool.Devices)
		}

		// Two lvs calls for every host, folded into the rows above by the same
		// normalized host name they were keyed under.
		if len(poolByKey) > 0 {
			caches := sm.cacheByPool(ctx, hosts)
			usage := sm.thinUsageByPool(ctx, hosts)
			for _, pool := range poolByKey {
				if byVG, ok := caches[pool.Node]; ok {
					pool.Cache = byVG[pool.Name]
				}
				if byVG, ok := usage[pool.Node]; ok {
					pool.ThinUsage = byVG[pool.Name]
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
		return fmt.Errorf("PV creation failed: %s", result.FailureDetails())
	}

	// Extend VG
	cmd := fmt.Sprintf("sudo vgextend %s %s", pool, disk)
	result, err = sm.controller.deployment.Exec(ctx, []string{address}, cmd)
	if err != nil {
		return fmt.Errorf("failed to add disk: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to add disk: %s", result.FailureDetails())
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
		return fmt.Errorf("failed to delete pool: %s", result.FailureDetails())
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

// CreateZFSPool creates a ZFS storage pool. A zpool has no thin/thick mode;
// thin vs thick provisioning is a per-zvol property applied at volume creation.
func (sm *StorageManager) CreateZFSPool(ctx context.Context, name, node string, vdevs []string) error {
	name = normalizeManagedName(name)

	sm.controller.logger.Info("Creating ZFS pool",
		zap.String("name", name),
		zap.String("node", node),
		zap.Strings("vdevs", vdevs))

	// Convert node name to address
	address := sm.controller.ResolveHost(node)

	// Create ZFS pool
	result, err := sm.controller.deployment.ZFSCreatePool(ctx, []string{address}, name, vdevs)
	if err != nil {
		return fmt.Errorf("failed to create ZFS pool: %w", err)
	}

	if !result.AllSuccess() {
		return fmt.Errorf("failed to create ZFS pool: %s", result.FailureDetails())
	}

	sm.controller.logger.Info("ZFS pool created successfully",
		zap.String("name", name),
		zap.String("node", node))

	if sm.controller.db != nil {
		dbPool := &database.Pool{
			Name:    name,
			Type:    "zfs",
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
		return nil, fmt.Errorf("failed to get ZFS pool: %s", result.FailureDetails())
	}

	for _, r := range result.Hosts {
		if r.Success && r.Output != "" {
			fields := strings.Fields(r.Output)
			if len(fields) >= 4 {
				totalSize, _ := strconv.ParseUint(fields[1], 10, 64)
				freeSize, _ := strconv.ParseUint(fields[2], 10, 64)
				return &PoolInfo{
					Name:       poolName,
					Type:       "zfs",
					Node:       node,
					TotalGB:    bytesToGB(totalSize),
					FreeGB:     bytesToGB(freeSize),
					TotalBytes: totalSize,
					FreeBytes:  freeSize,
					Devices:    []string{},
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
				if isZFSPoolType(pool.Type) {
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
						Name:       poolName,
						Type:       "zfs",
						Node:       normalizedHost,
						TotalGB:    bytesToGB(totalSize),
						FreeGB:     bytesToGB(freeSize),
						TotalBytes: totalSize,
						FreeBytes:  freeSize,
					})
				}
			}
		}
	}

	if len(pools) == 0 {
		if persisted, err := sm.listPersistedPools(ctx); err == nil {
			var zfsPools []*PoolInfo
			for _, pool := range persisted {
				if isZFSPoolType(pool.Type) {
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
		return fmt.Errorf("failed to delete ZFS pool: %s", result.FailureDetails())
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
		return fmt.Errorf("failed to create ZFS dataset: %s", result.FailureDetails())
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
		for host, hr := range result.Hosts {
			if !hr.Success {
				return fmt.Errorf("failed to delete ZFS dataset on %s: %s", host, strings.TrimSpace(hr.Output+hr.Error.Error()))
			}
		}
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
		return fmt.Errorf("failed to create ZFS thin volume: %s", result.FailureDetails())
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
		return fmt.Errorf("failed to create ZFS snapshot: %s", result.FailureDetails())
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
		for host, hr := range result.Hosts {
			if !hr.Success {
				return fmt.Errorf("failed to delete ZFS snapshot on %s: %s", host, strings.TrimSpace(hr.Output+hr.Error.Error()))
			}
		}
	}

	return nil
}

// ZFSRestoreSnapshot restores a ZFS snapshot (rollback)
func (sm *StorageManager) ZFSRestoreSnapshot(ctx context.Context, dataset, snapshotName, node string) error {
	dataset = normalizeManagedZFSPath(dataset)
	sm.controller.logger.Info("Restoring ZFS snapshot",
		zap.String("dataset", dataset),
		zap.String("snapshot", snapshotName),
		zap.String("node", node))
	return sm.controller.snapshots.RestoreZFSSnapshot(ctx, dataset, snapshotName, node)
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
		return fmt.Errorf("failed to clone ZFS snapshot: %s", result.FailureDetails())
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
		return fmt.Errorf("failed to create LVM snapshot: %s", result.FailureDetails())
	}

	return nil
}

// ListLvmSnapshots lists LVM snapshots in a volume group.
//
// resource narrows the result to that DRBD resource's volumes; empty lists the
// whole group.
func (sm *StorageManager) ListLvmSnapshots(ctx context.Context, vgName, node, resource string) ([]*SnapshotInfo, error) {
	vgName = normalizeManagedName(vgName)

	// Resolve node address
	address := sm.controller.ResolveHost(node)

	result, err := sm.controller.deployment.LVListSnapshots(ctx, []string{address}, vgName)
	if err != nil {
		return nil, fmt.Errorf("failed to list LVM snapshots: %w", err)
	}
	if !result.AllSuccess() {
		// Reporting an empty list on a failed command is how this went unnoticed
		// for so long: a pool holding 27 snapshots read as a pool holding none,
		// and nothing said why.
		return nil, fmt.Errorf("failed to list LVM snapshots in %s: %s", vgName, result.FailureDetails())
	}

	// An empty resource lists the whole group. Otherwise only snapshots whose
	// origin is one of the resource's backing volumes are returned — the
	// `--resource` flag used to be accepted, printed in the heading, and then
	// ignored, so `snapshot list --resource a` and `--resource b` returned
	// identical lists of everything in the pool.
	var wanted map[string]bool
	if resource != "" {
		wanted, err = sm.backingVolumesOf(ctx, resource)
		if err != nil {
			return nil, err
		}
	}

	var snapshots []*SnapshotInfo
	for _, r := range result.Hosts {
		for _, line := range strings.Split(strings.TrimSpace(r.Output), "\n") {
			snap, ok := parseSnapshotLine(line, vgName)
			if !ok {
				continue
			}
			if wanted != nil && !wanted[snap.Origin] {
				continue
			}
			snapshots = append(snapshots, snap)
		}
	}

	return snapshots, nil
}

// parseSnapshotLine reads one row of LVListSnapshots output:
// "name|size_bytes|time|origin".
func parseSnapshotLine(line, vgName string) (*SnapshotInfo, bool) {
	fields := strings.Split(strings.TrimSpace(line), "|")
	if len(fields) < 4 {
		return nil, false
	}
	name := strings.TrimSpace(fields[0])
	if name == "" {
		return nil, false
	}
	return &SnapshotInfo{
		Name:      name,
		Volume:    vgName,
		SizeGB:    bytesToGB(parseThinUint(fields[1])),
		CreatedAt: strings.TrimSpace(fields[2]),
		Origin:    strings.TrimSpace(fields[3]),
	}, true
}

// backingVolumesOf resolves a resource to the set of LV names its snapshots can
// have as an origin.
//
// Read from the database rather than rebuilt from the "<name>_data" /
// "<name>_vol<K>" convention: the convention lives in resource creation, and a
// second copy of it here would keep working right up until the day the first
// one changed.
func (sm *StorageManager) backingVolumesOf(ctx context.Context, resource string) (map[string]bool, error) {
	if sm.controller.db == nil {
		return nil, fmt.Errorf("database not available, cannot resolve volumes of resource %s", resource)
	}
	volumes, err := sm.controller.db.ListVolumes(ctx, resource)
	if err != nil {
		return nil, fmt.Errorf("list volumes of resource %s: %w", resource, err)
	}
	if len(volumes) == 0 {
		return nil, fmt.Errorf("resource %s has no volumes, or does not exist", resource)
	}
	wanted := make(map[string]bool, len(volumes))
	for _, v := range volumes {
		if v != nil && v.VolumeName != "" {
			wanted[v.VolumeName] = true
		}
	}
	return wanted, nil
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
		return fmt.Errorf("failed to delete LVM snapshot: %s", result.FailureDetails())
	}

	return nil
}

// RestoreLvmSnapshot restores an LVM snapshot (merges it back to the origin).
// A snapshot of a DRBD backing volume is restored the way that keeps the
// resource's replicas in step; see snapshot_restore.go.
func (sm *StorageManager) RestoreLvmSnapshot(ctx context.Context, vgName, snapshotName, node string) error {
	vgName = normalizeManagedName(vgName)
	sm.controller.logger.Info("Restoring LVM snapshot",
		zap.String("vg_name", vgName),
		zap.String("snapshot", snapshotName),
		zap.String("node", node))
	return sm.controller.snapshots.RestoreLVMSnapshotByName(ctx, vgName, snapshotName, node)
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
		return fmt.Errorf("failed to resize ZFS volume: %s", result.FailureDetails())
	}

	return nil
}
