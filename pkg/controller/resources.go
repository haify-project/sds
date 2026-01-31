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

// ResourceInfo represents DRBD resource information
type ResourceInfo struct {
	Name     string
	Port     uint32
	Protocol string
	Nodes    []string
	Role     string
	Volumes  []*ResourceVolumeInfo
}

// ResourceVolumeInfo represents DRBD volume information
type ResourceVolumeInfo struct {
	VolumeID uint32
	Device   string
	SizeGB   uint64
}

// ResourceManager manages DRBD resources
type ResourceManager struct {
	controller *Controller
	agents     map[string]*client.AgentClient
	mu         sync.RWMutex
}

// NewResourceManager creates a new resource manager
func NewResourceManager(ctrl *Controller) *ResourceManager {
	return &ResourceManager{
		controller: ctrl,
		agents:     make(map[string]*client.AgentClient),
	}
}

// AddAgent adds an agent connection
func (rm *ResourceManager) AddAgent(node string, agent *client.AgentClient) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.agents[node] = agent
}

// CreateResource creates a DRBD resource across multiple nodes
func (rm *ResourceManager) CreateResource(ctx context.Context, name string, port uint32, nodes []string, protocol string, sizeGB uint32, pool string) error {
	rm.controller.logger.Info("Creating DRBD resource",
		zap.String("name", name),
		zap.Uint32("port", port),
		zap.Strings("nodes", nodes),
		zap.String("protocol", protocol),
		zap.Uint32("size_gb", sizeGB),
		zap.String("pool", pool))

	// DRBD resource creation involves:
	// 1. Creating LVs on each node
	// 2. Generating DRBD configuration
	// 3. Calling drbdsetup create-md
	// 4. Calling drbdsetup up

	// Use provided pool or default
	if pool == "" {
		pool = "data-pool"
	}

	for _, node := range nodes {
		rm.mu.RLock()
		agent := rm.agents[node]
		rm.mu.RUnlock()

		if agent == nil {
			return fmt.Errorf("node not found: %s", node)
		}

		// 1. Create LV for the data volume
		sizeSuffix := fmt.Sprintf("%dG", sizeGB)
		lvName := fmt.Sprintf("%s_data", name)

		lvReq := &pb.LVCreateRequest{
			VgName:     pool,
			LvName:     lvName,
			SizeSuffix: sizeSuffix,
		}

		lvResp, err := agent.LVCreate(ctx, lvReq)
		if err != nil {
			return fmt.Errorf("failed to create LV on %s: %w", node, err)
		}

		if !lvResp.Success {
			return fmt.Errorf("failed to create LV on %s: %s", node, lvResp.Message)
		}

		// 2. Create DRBD metadata
		mdReq := &pb.CreateMDRequest{
			Resources: []string{name},
			Force:     false,
		}

		mdResp, err := agent.CreateMD(ctx, mdReq)
		if err != nil {
			return fmt.Errorf("failed to create metadata on %s: %w", node, err)
		}

		if !mdResp.Success {
			return fmt.Errorf("failed to create metadata on %s: %s", node, mdResp.Message)
		}

		// 3. Bring DRBD resource up
		upReq := &pb.UpRequest{
			Resources:      []string{name},
			Force:          false,
			DryRun:         false,
			DiscardMyData:  false,
		}

		upResp, err := agent.Up(ctx, upReq)
		if err != nil {
			return fmt.Errorf("failed to bring up resource on %s: %w", node, err)
		}

		if !upResp.Success {
			return fmt.Errorf("failed to bring up resource on %s: %s", node, upResp.Message)
		}

		rm.controller.logger.Info("DRBD resource created on node",
			zap.String("resource", name),
			zap.String("node", node))
	}

	return nil
}

// GetResource gets resource information
func (rm *ResourceManager) GetResource(ctx context.Context, name string) (*ResourceInfo, error) {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	// Query first available agent for resource status
	for node, agent := range rm.agents {
		req := &pb.StatusRequest{
			Resources: []string{name},
		}

		resp, err := agent.Status(ctx, req)
		if err != nil {
			rm.controller.logger.Warn("Failed to get resource status",
				zap.String("node", node),
				zap.Error(err))
			continue
		}

		if len(resp.Resources) == 0 {
			continue
		}

		r := resp.Resources[0]
		info := &ResourceInfo{
			Name:     r.Name,
			Port:     7000, // Default port - in production parse from DRBD config
			Protocol: "C", // Default protocol - in production parse from DRBD config
			Nodes:    []string{node},
			Role:     r.Role,
		}

		for _, vol := range r.Volumes {
			info.Volumes = append(info.Volumes, &ResourceVolumeInfo{
				VolumeID:  uint32(vol.VolumeNumber),
				Device:    vol.Device,
				SizeGB:    vol.Size / 1024 / 1024 / 1024,
			})
		}

		return info, nil
	}

	return nil, fmt.Errorf("resource not found: %s", name)
}

// ListResources lists all resources
func (rm *ResourceManager) ListResources(ctx context.Context) ([]*ResourceInfo, error) {
	var resources []*ResourceInfo

	rm.mu.RLock()
	defer rm.mu.RUnlock()

	seen := make(map[string]bool)

	for node, agent := range rm.agents {
		req := &pb.StatusRequest{
			Resources: []string{},
		}

		resp, err := agent.Status(ctx, req)
		if err != nil {
			rm.controller.logger.Warn("Failed to list resources",
				zap.String("node", node),
				zap.Error(err))
			continue
		}

		for _, r := range resp.Resources {
			if seen[r.Name] {
				continue
			}
			seen[r.Name] = true

			info := &ResourceInfo{
				Name:     r.Name,
				Role:     r.Role,
				Protocol: "C",
				Nodes:    []string{node},
			}

			for _, vol := range r.Volumes {
				info.Volumes = append(info.Volumes, &ResourceVolumeInfo{
					VolumeID: uint32(vol.VolumeNumber),
					Device:   vol.Device,
					SizeGB:   vol.Size / 1024 / 1024 / 1024,
				})
			}

			resources = append(resources, info)
		}
	}

	return resources, nil
}

// AddVolume adds a volume to a resource
func (rm *ResourceManager) AddVolume(ctx context.Context, resource, volume, pool string, sizeGB uint32) error {
	rm.controller.logger.Info("Adding volume to resource",
		zap.String("resource", resource),
		zap.String("volume", volume),
		zap.String("pool", pool),
		zap.Uint32("size_gb", sizeGB))

	// Get all agents
	rm.mu.RLock()
	var nodes []string
	for node := range rm.agents {
		nodes = append(nodes, node)
	}
	rm.mu.RUnlock()

	if len(nodes) == 0 {
		return fmt.Errorf("no agents available")
	}

	// Get first agent to query current resource configuration
	firstNode := nodes[0]
	rm.mu.RLock()
	agent := rm.agents[firstNode]
	rm.mu.RUnlock()

	// Query resource configuration to find existing volumes
	getVolReq := &pb.GetResourceVolumesRequest{
		Resource: resource,
	}

	getVolResp, err := agent.GetResourceVolumes(ctx, getVolReq)
	if err != nil {
		return fmt.Errorf("failed to get resource volumes: %w", err)
	}

	if !getVolResp.Success {
		return fmt.Errorf("failed to get resource volumes: %s", getVolResp.Message)
	}

	// Find max volume number and minor from existing volumes
	var maxVolumeNumber int32 = -1
	var maxDeviceMinor int32 = -1

	for _, vol := range getVolResp.Volumes {
		if vol.VolumeNumber > maxVolumeNumber {
			maxVolumeNumber = vol.VolumeNumber
		}
		if vol.DeviceMinor > maxDeviceMinor {
			maxDeviceMinor = vol.DeviceMinor
		}
	}

	// Calculate new volume number and minor
	newVolumeNumber := maxVolumeNumber + 1
	newDeviceMinor := maxDeviceMinor + 1

	// Default to vg0 if pool not specified
	if pool == "" {
		pool = "vg0"
	}

	rm.controller.logger.Info("Calculated new volume parameters",
		zap.String("resource", resource),
		zap.Int32("volume_number", newVolumeNumber),
		zap.Int32("device_minor", newDeviceMinor))

	// Create LV on all nodes
	nodeDisk := make(map[string]string)

	for _, node := range nodes {
		rm.mu.RLock()
		agent := rm.agents[node]
		rm.mu.RUnlock()

		if agent == nil {
			return fmt.Errorf("node not found: %s", node)
		}

		// Create LV
		sizeSuffix := fmt.Sprintf("%dG", sizeGB)
		lvReq := &pb.LVCreateRequest{
			VgName:     pool,
			LvName:     volume,
			SizeSuffix: sizeSuffix,
		}

		lvResp, err := agent.LVCreate(ctx, lvReq)
		if err != nil {
			return fmt.Errorf("failed to create LV on %s: %w", node, err)
		}

		if !lvResp.Success {
			return fmt.Errorf("failed to create LV on %s: %s", node, lvResp.Message)
		}

		diskPath := fmt.Sprintf("/dev/%s/%s", pool, volume)
		nodeDisk[node] = diskPath

		rm.controller.logger.Info("Volume LV created",
			zap.String("node", node),
			zap.String("disk", diskPath))
	}

	// Add volume to DRBD resource on all nodes
	for _, node := range nodes {
		rm.mu.RLock()
		agent := rm.agents[node]
		rm.mu.RUnlock()

		addVolReq := &pb.AddVolumeRequest{
			Resource:       resource,
			VolumeNumber:   newVolumeNumber,
			DeviceMinor:    newDeviceMinor,
			Disk:           nodeDisk[node],
			MetaDisk:       "internal",
			NodeDisk:       nodeDisk,
			CreateMetadata: true,
			AdjustResource: true,
			BackupConfig:   true,
		}

		addVolResp, err := agent.AddVolume(ctx, addVolReq)
		if err != nil {
			return fmt.Errorf("failed to add volume on %s: %w", node, err)
		}

		if !addVolResp.Success {
			return fmt.Errorf("failed to add volume on %s: %s", node, addVolResp.Message)
		}

		rm.controller.logger.Info("Volume added to DRBD resource",
			zap.String("node", node),
			zap.String("device", addVolResp.DevicePath))
	}

	rm.controller.logger.Info("Volume added successfully",
		zap.String("resource", resource),
		zap.Int32("volume_number", newVolumeNumber),
		zap.Int32("device_minor", newDeviceMinor))

	return nil
}

// DeleteResource deletes a resource
func (rm *ResourceManager) DeleteResource(ctx context.Context, name string) error {
	rm.controller.logger.Info("Deleting resource", zap.String("name", name))

	// Resource deletion involves:
	// 1. Stop resource on all nodes
	// 2. Delete DRBD configuration
	// 3. Remove LVs

	// Get all agents
	var nodes []string
	rm.mu.RLock()
	for node := range rm.agents {
		nodes = append(nodes, node)
	}
	rm.mu.RUnlock()

	for _, node := range nodes {
		rm.mu.RLock()
		agent := rm.agents[node]
		rm.mu.RUnlock()

		if agent == nil {
			rm.controller.logger.Warn("Node not found during deletion",
				zap.String("node", node))
			continue
		}

		// 1. Bring resource down
		downReq := &pb.DownRequest{
			Resources: []string{name},
			Force:     false,
		}

		downResp, err := agent.Down(ctx, downReq)
		if err != nil {
			rm.controller.logger.Warn("Failed to bring down resource",
				zap.String("resource", name),
				zap.String("node", node),
				zap.Error(err))
		} else if !downResp.Success {
			rm.controller.logger.Warn("Failed to bring down resource",
				zap.String("resource", name),
				zap.String("node", node),
				zap.String("message", downResp.Message))
		}

		// 2. Delete DRBD metadata would be done via ExecCommand
		// For now, we just bring the resource down
		// Metadata deletion and LV removal would require additional steps

		rm.controller.logger.Info("Resource deleted on node",
			zap.String("resource", name),
			zap.String("node", node))
	}

	rm.controller.logger.Info("Resource deleted",
		zap.String("resource", name),
		zap.Strings("nodes", nodes))

	return nil
}

// SetPrimary sets a node as Primary for a resource
func (rm *ResourceManager) SetPrimary(ctx context.Context, resource, node string, force bool) error {
	rm.mu.RLock()

	// Debug: log available agents
	var availableNodes []string
	for endpoint := range rm.agents {
		availableNodes = append(availableNodes, endpoint)
	}

	var agent *client.AgentClient

	// Try exact match first
	agent = rm.agents[node]

	// If not found, try matching without port (e.g., "orange1" matches "orange1:50051")
	if agent == nil {
		for endpoint, a := range rm.agents {
			// Extract node name from endpoint
			parts := strings.Split(endpoint, ":")
			if len(parts) > 0 && parts[0] == node {
				agent = a
				break
			}
		}
	}
	rm.mu.RUnlock()

	if agent == nil {
		rm.controller.logger.Warn("Node not found in ResourceManager",
			zap.String("requested_node", node),
			zap.Strings("available_agents", availableNodes))
		return fmt.Errorf("node not found: %s", node)
	}

	rm.controller.logger.Info("Setting Primary",
		zap.String("resource", resource),
		zap.String("node", node),
		zap.Bool("force", force))

	req := &pb.PrimaryRequest{
		Resources: []string{resource},
		Force:     force,
	}

	resp, err := agent.Primary(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to set primary: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("failed to set primary: %s", resp.Message)
	}

	rm.controller.logger.Info("Primary set successfully",
		zap.String("resource", resource),
		zap.String("node", node))

	return nil
}

// Mount mounts a DRBD device
func (rm *ResourceManager) Mount(ctx context.Context, resource string, volumeID uint32, path, node, fstype string) error {
	rm.mu.RLock()
	agent := rm.agents[node]
	rm.mu.RUnlock()

	if agent == nil {
		return fmt.Errorf("node not found: %s", node)
	}

	rm.controller.logger.Info("Mounting DRBD device",
		zap.String("resource", resource),
		zap.Uint32("volume", volumeID),
		zap.String("path", path),
		zap.String("node", node),
		zap.String("fstype", fstype))

	// Get DRBD device path
	drbdDevice := fmt.Sprintf("/dev/drbd%d", volumeID)

	// 1. Create filesystem if specified
	if fstype != "" {
		// Use ExecCommand to create filesystem
		mkfsReq := &pb.ExecCommandRequest{
			Command: fmt.Sprintf("mkfs.%s", fstype),
			Args:    []string{"-F", drbdDevice},
		}

		mkfsResp, err := agent.ExecCommand(ctx, mkfsReq)
		if err != nil {
			return fmt.Errorf("failed to create filesystem: %w", err)
		}

		if !mkfsResp.Success {
			return fmt.Errorf("failed to create filesystem: %s", mkfsResp.Message)
		}

		rm.controller.logger.Info("Filesystem created",
			zap.String("device", drbdDevice),
			zap.String("fstype", fstype))
	}

	// 2. Create mount point using ExecCommand
	mkdirReq := &pb.ExecCommandRequest{
		Command: "mkdir",
		Args:    []string{"-p", path},
	}

	mkdirResp, err := agent.ExecCommand(ctx, mkdirReq)
	if err != nil {
		return fmt.Errorf("failed to create mount point: %w", err)
	}

	if !mkdirResp.Success {
		return fmt.Errorf("failed to create mount point: %s", mkdirResp.Message)
	}

	// 3. Mount the device
	mountReq := &pb.ExecCommandRequest{
		Command: "mount",
		Args:    []string{drbdDevice, path},
	}

	mountResp, err := agent.ExecCommand(ctx, mountReq)
	if err != nil {
		return fmt.Errorf("failed to mount: %w", err)
	}

	if !mountResp.Success {
		return fmt.Errorf("failed to mount: %s", mountResp.Message)
	}

	rm.controller.logger.Info("DRBD device mounted",
		zap.String("device", drbdDevice),
		zap.String("path", path),
		zap.String("node", node))

	return nil
}

// SetSecondary sets a node as Secondary for a resource
func (rm *ResourceManager) SetSecondary(ctx context.Context, resource, node string) error {
	rm.mu.RLock()
	agent := rm.agents[node]
	rm.mu.RUnlock()

	if agent == nil {
		return fmt.Errorf("node not found: %s", node)
	}

	rm.controller.logger.Info("Setting Secondary",
		zap.String("resource", resource),
		zap.String("node", node))

	req := &pb.SecondaryRequest{
		Resources: []string{resource},
	}

	resp, err := agent.Secondary(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to set secondary: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("failed to set secondary: %s", resp.Message)
	}

	rm.controller.logger.Info("Secondary set successfully",
		zap.String("resource", resource),
		zap.String("node", node))

	return nil
}

// RemoveVolume removes a volume from a resource
func (rm *ResourceManager) RemoveVolume(ctx context.Context, resource string, volumeID uint32, node string) error {
	rm.mu.RLock()
	agent := rm.agents[node]
	rm.mu.RUnlock()

	if agent == nil {
		return fmt.Errorf("node not found: %s", node)
	}

	rm.controller.logger.Info("Removing volume",
		zap.String("resource", resource),
		zap.Uint32("volume_id", volumeID),
		zap.String("node", node))

	// Remove LV using LVM
	// For now, we would need to know the LV name to remove it
	// This requires tracking LV names separately

	rm.controller.logger.Info("Volume removal requires manual LV cleanup",
		zap.String("resource", resource),
		zap.Uint32("volume_id", volumeID))

	return nil
}

// ResizeVolume resizes a volume
func (rm *ResourceManager) ResizeVolume(ctx context.Context, resource string, volumeID uint32, node string, sizeGB uint32) error {
	rm.mu.RLock()
	agent := rm.agents[node]
	rm.mu.RUnlock()

	if agent == nil {
		return fmt.Errorf("node not found: %s", node)
	}

	rm.controller.logger.Info("Resizing DRBD volume",
		zap.String("resource", resource),
		zap.Uint32("volume_id", volumeID),
		zap.String("node", node),
		zap.Uint32("size_gb", sizeGB))

	// Resize DRBD resource first
	// Note: sizeGB is in GiB (binary), convert to bytes
	sizeBytes := int64(sizeGB) * 1024 * 1024 * 1024
	resizeReq := &pb.ResizeRequest{
		Resources:           []string{resource},
		SizeAll:             false,
		SizeBytes:           sizeBytes,
		AssumePeerHasSpace:  false,
	}

	resizeResp, err := agent.Resize(ctx, resizeReq)
	if err != nil {
		return fmt.Errorf("failed to resize DRBD: %w", err)
	}

	if !resizeResp.Success {
		return fmt.Errorf("failed to resize DRBD: %s", resizeResp.Message)
	}

	rm.controller.logger.Info("Volume resized",
		zap.String("resource", resource),
		zap.Uint32("volume_id", volumeID),
		zap.Int64("size_bytes", sizeBytes),
		zap.String("node", node))

	return nil
}

// CreateFilesystemOnly creates a filesystem without mounting
func (rm *ResourceManager) CreateFilesystemOnly(ctx context.Context, resource string, volumeID uint32, node, fstype string) error {
	rm.mu.RLock()
	agent := rm.agents[node]
	rm.mu.RUnlock()

	if agent == nil {
		return fmt.Errorf("node not found: %s", node)
	}

	rm.controller.logger.Info("Creating filesystem",
		zap.String("resource", resource),
		zap.Uint32("volume", volumeID),
		zap.String("node", node),
		zap.String("fstype", fstype))

	// Get DRBD device path
	drbdDevice := fmt.Sprintf("/dev/drbd%d", volumeID)

	// Use ExecCommand to create filesystem
	mkfsReq := &pb.ExecCommandRequest{
		Command: fmt.Sprintf("mkfs.%s", fstype),
		Args:    []string{"-F", drbdDevice},
	}

	mkfsResp, err := agent.ExecCommand(ctx, mkfsReq)
	if err != nil {
		return fmt.Errorf("failed to create filesystem: %w", err)
	}

	if !mkfsResp.Success {
		return fmt.Errorf("failed to create filesystem: %s", mkfsResp.Message)
	}

	rm.controller.logger.Info("Filesystem created",
		zap.String("device", drbdDevice),
		zap.String("fstype", fstype))

	return nil
}

// Unmount unmounts a DRBD device
func (rm *ResourceManager) Unmount(ctx context.Context, resource string, volumeID uint32, node string) error {
	rm.mu.RLock()
	agent := rm.agents[node]
	rm.mu.RUnlock()

	if agent == nil {
		return fmt.Errorf("node not found: %s", node)
	}

	rm.controller.logger.Info("Unmounting DRBD device",
		zap.String("resource", resource),
		zap.Uint32("volume", volumeID),
		zap.String("node", node))

	// Get DRBD device path
	drbdDevice := fmt.Sprintf("/dev/drbd%d", volumeID)

	// Unmount using ExecCommand
	umountReq := &pb.ExecCommandRequest{
		Command: "umount",
		Args:    []string{drbdDevice},
	}

	umountResp, err := agent.ExecCommand(ctx, umountReq)
	if err != nil {
		return fmt.Errorf("failed to unmount: %w", err)
	}

	if !umountResp.Success {
		return fmt.Errorf("failed to unmount: %s", umountResp.Message)
	}

	rm.controller.logger.Info("DRBD device unmounted",
		zap.String("device", drbdDevice),
		zap.String("node", node))

	return nil
}
