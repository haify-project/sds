package controller

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"go.uber.org/zap"
	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/deployment"
)

// ResourceInfo represents DRBD resource information
type ResourceInfo struct {
	Name       string
	Port       uint32
	Protocol   string
	Nodes      []string
	Role       string
	Volumes    []*ResourceVolumeInfo
	NodeStates map[string]*ResourceNodeState
}

// ResourceNodeState represents detailed state of a node for a resource
type ResourceNodeState struct {
	Role        string
	DiskState   string
	Replication string
}

// ResourceVolumeInfo represents DRBD volume information
type ResourceVolumeInfo struct {
	VolumeID uint32
	Device   string
	SizeGB   uint64
}

// ResourceManager manages DRBD resources using dispatch
type ResourceManager struct {
	controller *Controller
	deployment *deployment.Client
	hosts      []string
	hostMap    map[string]string // hostname -> IP for config generation
	mu         sync.RWMutex
}

// NewResourceManager creates a new resource manager
func NewResourceManager(ctrl *Controller) *ResourceManager {
	return &ResourceManager{
		controller: ctrl,
		hosts:      make([]string, 0),
		hostMap:    make(map[string]string),
	}
}

// SetDeployment sets the deployment client
func (rm *ResourceManager) SetDeployment(client *deployment.Client) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.deployment = client
}

// SetHosts sets the list of hosts for resource operations
func (rm *ResourceManager) SetHosts(hosts []string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.hosts = hosts
	// Build hostMap for IP/hostname resolution
	rm.hostMap = make(map[string]string)
	for _, host := range hosts {
		// Try to resolve hostname to IP
		parts := strings.Split(host, ":")
		if len(parts) > 1 {
			rm.hostMap[parts[0]] = parts[0]
		} else {
			rm.hostMap[host] = host
		}
	}
}

// GetHosts returns the list of hosts
func (rm *ResourceManager) GetHosts() []string {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.hosts
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

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	if pool == "" {
		pool = "data-pool"
	}

	if protocol == "" {
		protocol = "C"
	}

	lvName := fmt.Sprintf("%s_data", name)

	// 1. Create LVs on all nodes
	for _, node := range nodes {
		result, err := rm.deployment.LVCreate(ctx, []string{node}, pool, lvName, fmt.Sprintf("%dG", sizeGB))
		if err != nil {
			return fmt.Errorf("failed to create LV on %s: %w", node, err)
		}
		if !result.AllSuccess() {
			for host, hres := range result.Hosts {
				if !hres.Success {
					return fmt.Errorf("LV creation failed on %s: %s", host, hres.Output)
				}
			}
		}
	}

	// 2. Generate DRBD config
	drbdConfig := rm.generateDrbdConfig(name, port, nodes, protocol, pool, lvName)

	// 3. Distribute config to all nodes
	configResult, err := rm.deployment.DistributeConfig(ctx, nodes, drbdConfig, fmt.Sprintf("/etc/drbd.d/%s.res", name))
	if err != nil {
		return fmt.Errorf("failed to distribute config: %w", err)
	}
	if !configResult.Success {
		return fmt.Errorf("config distribution failed on some hosts")
	}

	// 4. Create metadata on all nodes
	mdResult, err := rm.deployment.DRBDCreateMD(ctx, nodes, name)
	if err != nil {
		return fmt.Errorf("failed to create metadata: %w", err)
	}
	if !mdResult.AllSuccess() {
		return fmt.Errorf("metadata creation failed on hosts: %v", mdResult.FailedHosts())
	}

	// 5. Bring up resource on all nodes
	upResult, err := rm.deployment.DRBDUp(ctx, nodes, name)
	if err != nil {
		return fmt.Errorf("failed to bring up resource: %w", err)
	}
	if !upResult.AllSuccess() {
		return fmt.Errorf("resource up failed on hosts: %v", upResult.FailedHosts())
	}

	// 6. Save to database
	if rm.controller.db != nil {
		dbRes := &database.Resource{
			Name:     name,
			Port:     int(port),
			Nodes:    strings.Join(nodes, ","),
			Protocol: protocol,
			Replicas: len(nodes),
		}
		if err := rm.controller.db.SaveResource(ctx, dbRes); err != nil {
			rm.controller.logger.Warn("Failed to save resource to database", zap.Error(err))
		}
	}

	rm.controller.logger.Info("DRBD resource created successfully",
		zap.String("name", name))

	return nil
}

// generateDrbdConfig generates a DRBD resource configuration file
func (rm *ResourceManager) generateDrbdConfig(name string, port uint32, nodes []string, protocol, pool, lvName string) string {
	var config strings.Builder

	config.WriteString(fmt.Sprintf("resource %s {\n", name))
	config.WriteString("    options {\n")
	config.WriteString("        auto-promote no;\n")
	config.WriteString("        quorum majority;\n")
	config.WriteString("        on-no-quorum io-error;\n")
	config.WriteString("        on-no-data-accessible io-error;\n")
	config.WriteString("        on-suspended-primary-outdated force-secondary;\n")
	config.WriteString("    }\n\n")
	config.WriteString(fmt.Sprintf("    protocol %s;\n", protocol))

	// Net options for handling conflicts
	config.WriteString("    net {\n")
	config.WriteString("        rr-conflict retry-connect;\n")
	config.WriteString("    }\n")

	// Generate volume 0 block
	config.WriteString("\n    volume 0 {\n")
	config.WriteString(fmt.Sprintf("        device    minor %d;\n", port-7000))
	config.WriteString(fmt.Sprintf("        disk      /dev/%s/%s;\n", pool, lvName))
	config.WriteString("        meta-disk internal;\n")
	config.WriteString("    }\n")

	// Generate on sections for each node
	var hostnames []string
	for i, node := range nodes {
		// Get IP address from controller's hostsMap
		rm.controller.hostsLock.RLock()
		ip := rm.controller.hostsMap[node]
		rm.controller.hostsLock.RUnlock()

		// Fallback to node name if not in map
		if ip == "" {
			ip = node
		}

		hostnames = append(hostnames, node)
		config.WriteString(fmt.Sprintf("\n    on %s {\n", node))
		config.WriteString(fmt.Sprintf("        address   %s:%d;\n", ip, port))
		config.WriteString(fmt.Sprintf("        node-id   %d;\n", i))
		config.WriteString("    }\n")
	}

	// Use connection-mesh for DRBD 9
	if len(hostnames) > 0 {
		config.WriteString("\n    connection-mesh {\n")
		config.WriteString("        hosts")
		for _, hostname := range hostnames {
			config.WriteString(fmt.Sprintf(" %s", hostname))
		}
		config.WriteString(";\n")
		config.WriteString("    }\n")
	}

	config.WriteString("}\n")

	return config.String()
}

// GetResource gets resource information from database with live status
func (rm *ResourceManager) GetResource(ctx context.Context, name string) (*ResourceInfo, error) {
	if rm.controller.db == nil {
		return nil, fmt.Errorf("database not available")
	}

	rm.mu.RLock()
	hosts := rm.hosts
	rm.mu.RUnlock()

	if len(hosts) == 0 {
		return nil, fmt.Errorf("no hosts configured")
	}

	// Get resource info from database
	dbRes, err := rm.controller.db.GetResource(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("resource not found: %s", name)
	}

	// Parse nodes from comma-separated string
	var nodes []string
	if dbRes.Nodes != "" {
		nodes = strings.Split(dbRes.Nodes, ",")
	}

	rm.controller.logger.Debug("GetResource",
		zap.String("name", name),
		zap.String("dbRes.Nodes", dbRes.Nodes),
		zap.Strings("parsed_nodes", nodes))

	// Query live DRBD status from first available host
	result, err := rm.deployment.DRBDStatus(ctx, []string{hosts[0]}, name)

	var volumes []*ResourceVolumeInfo
	nodeStates := make(map[string]*ResourceNodeState)
	localRole := "Unknown"

	if err == nil {
		for _, r := range result.Hosts {
			if r.Success {
				rm.controller.logger.Debug("DRBD status output",
					zap.String("output", r.Output))

				// Parse local node role
				localRole = parseRoleFromStatus(r.Output)

				// Parse volumes
				volInfo := parseVolumesFromStatus(r.Output)
				for _, v := range volInfo {
					volumes = append(volumes, &ResourceVolumeInfo{
						VolumeID: uint32(v.id),
						Device:   v.device,
						SizeGB:   v.sizeGB,
					})
				}

				// Parse node states from status output
				nodeStates = parseNodeStatesFromStatus(r.Output, nodes)

				rm.controller.logger.Debug("Parsed node states",
					zap.Int("count", len(nodeStates)))

				break
			}
		}
	}

	info := &ResourceInfo{
		Name:       dbRes.Name,
		Port:       uint32(dbRes.Port),
		Protocol:   dbRes.Protocol,
		Nodes:      nodes,
		Role:       localRole, // Local node's role
		Volumes:    volumes,
		NodeStates: nodeStates,
	}

	return info, nil
}

// ListResources lists all resources from database with live status
func (rm *ResourceManager) ListResources(ctx context.Context) ([]*ResourceInfo, error) {
	if rm.controller.db == nil {
		return nil, fmt.Errorf("database not available")
	}

	// Get resources from database
	dbResources, err := rm.controller.db.ListResources(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list resources from database: %w", err)
	}

	var resources []*ResourceInfo
	for _, dbRes := range dbResources {
		// Parse nodes from comma-separated string
		var nodes []string
		if dbRes.Nodes != "" {
			nodes = strings.Split(dbRes.Nodes, ",")
		}

		resources = append(resources, &ResourceInfo{
			Name:     dbRes.Name,
			Port:     uint32(dbRes.Port),
			Protocol: dbRes.Protocol,
			Nodes:    nodes,
			Role:     "Unknown", // Will be updated by GetResource if needed
			Volumes:  []*ResourceVolumeInfo{},
			NodeStates: make(map[string]*ResourceNodeState),
		})
	}

	return resources, nil
}

// AddVolume adds a volume to an existing DRBD resource
func (rm *ResourceManager) AddVolume(ctx context.Context, resource, volume, pool string, sizeGB uint32) error {
	rm.controller.logger.Info("Adding volume to resource",
		zap.String("resource", resource),
		zap.String("volume", volume),
		zap.String("pool", pool),
		zap.Uint32("size_gb", sizeGB))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	if pool == "" {
		pool = "data-pool"
	}

	rm.mu.RLock()
	hosts := rm.hosts
	rm.mu.RUnlock()

	// Get current config to find next volume number and minor
	result, err := rm.deployment.Exec(ctx, []string{hosts[0]}, fmt.Sprintf("cat /etc/drbd.d/%s.res", resource))
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	var hostResult *deployment.HostResult
	for _, r := range result.Hosts {
		hostResult = r
		break
	}

	if hostResult == nil || !hostResult.Success {
		return fmt.Errorf("failed to get config")
	}

	maxVolNum := -1
	maxMinor := -1

	// Parse volume numbers from config
	lines := strings.Split(hostResult.Output, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "volume ") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				if volNum, err := strconv.Atoi(strings.TrimSuffix(parts[1], "{")); err == nil {
					if volNum > maxVolNum {
						maxVolNum = volNum
					}
				}
			}
		}
		if strings.Contains(trimmed, "device    minor") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 4 {
				if minor, err := strconv.Atoi(strings.TrimSuffix(parts[3], ";")); err == nil {
					if minor > maxMinor {
						maxMinor = minor
					}
				}
			}
		}
	}

	newVolNum := maxVolNum + 1

	// Simple strategy: use maxMinor + 1
	newMinor := maxMinor + 1

	// Generate volume block for new volume
	volumeBlock := fmt.Sprintf("    volume %d {\n        device    minor %d;\n        disk      /dev/%s/%s;\n        meta-disk internal;\n    }",
		newVolNum, newMinor, pool, volume)

	// Create LVs on all nodes
	for _, host := range hosts {
		_, err := rm.deployment.LVCreate(ctx, []string{host}, pool, volume, fmt.Sprintf("%dG", sizeGB))
		if err != nil {
			return fmt.Errorf("failed to create LV on %s: %w", host, err)
		}
	}

	// Add volume block to config on all nodes
	for _, host := range hosts {
		updateCmd := fmt.Sprintf("sed -i '/^}/i %s' /etc/drbd.d/%s.res", volumeBlock, resource)
		_, err := rm.deployment.Exec(ctx, []string{host}, updateCmd)
		if err != nil {
			return fmt.Errorf("failed to update config on %s: %w", host, err)
		}
	}

	// Down resource, create metadata for new volume, up resource
	for _, host := range hosts {
		_, _ = rm.deployment.DRBDDown(ctx, []string{host}, resource)
	}

	// Create metadata for new volume only
	for _, host := range hosts {
		createMetaCmd := fmt.Sprintf("sudo drbdmeta --force %d v09 /dev/%s/%s internal create-md %d",
			newMinor, pool, volume, len(hosts)*3)
		_, err := rm.deployment.Exec(ctx, []string{host}, createMetaCmd)
		if err != nil {
			return fmt.Errorf("failed to create metadata on %s: %w", host, err)
		}
	}

	// Up resource
	upResult, err := rm.deployment.DRBDUp(ctx, hosts, resource)
	if err != nil {
		return fmt.Errorf("failed to bring up resource: %w", err)
	}
	if !upResult.AllSuccess() {
		return fmt.Errorf("resource up failed on hosts: %v", upResult.FailedHosts())
	}

	rm.controller.logger.Info("Volume added successfully",
		zap.String("resource", resource),
		zap.String("volume", volume))

	return nil
}

// DeleteResource deletes a DRBD resource from all nodes
func (rm *ResourceManager) DeleteResource(ctx context.Context, name string, force bool) error {
	rm.controller.logger.Info("Deleting DRBD resource",
		zap.String("name", name),
		zap.Bool("force", force))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	rm.mu.RLock()
	hosts := rm.hosts
	rm.mu.RUnlock()

	// 1. Down resource on all nodes
	downResult, err := rm.deployment.DRBDDown(ctx, hosts, name)
	if err != nil {
		return fmt.Errorf("failed to bring down resource: %w", err)
	}

	if !downResult.AllSuccess() && !force {
		return fmt.Errorf("resource down failed on hosts: %v", downResult.FailedHosts())
	}

	// 2. Delete config file from all nodes
	err = rm.deployment.DeleteConfig(ctx, hosts, fmt.Sprintf("/etc/drbd.d/%s.res", name))
	if err != nil {
		return fmt.Errorf("failed to delete config: %w", err)
	}

	// 3. Delete LVs (optional, depends on use case)
	// This is left for the caller to decide

	rm.controller.logger.Info("Resource deleted successfully",
		zap.String("name", name))

	return nil
}

// SetPrimary sets a resource to Primary on the specified node
func (rm *ResourceManager) SetPrimary(ctx context.Context, resource, node string, force bool) error {
	rm.controller.logger.Info("Setting resource primary",
		zap.String("resource", resource),
		zap.String("node", node),
		zap.Bool("force", force))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	result, err := rm.deployment.DRBDPrimary(ctx, node, resource, force)
	if err != nil {
		return fmt.Errorf("failed to set primary: %w", err)
	}

	if !result.Success {
		return fmt.Errorf("failed to set primary on %s", node)
	}

	return nil
}

// SetSecondary sets a resource to Secondary on the specified node
func (rm *ResourceManager) SetSecondary(ctx context.Context, resource, node string) error {
	rm.controller.logger.Info("Setting resource secondary",
		zap.String("resource", resource),
		zap.String("node", node))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	result, err := rm.deployment.DRBDSecondary(ctx, node, resource)
	if err != nil {
		return fmt.Errorf("failed to set secondary: %w", err)
	}

	if !result.Success {
		return fmt.Errorf("failed to set secondary on %s", node)
	}

	return nil
}

// parseDrbdConfig parses DRBD config file to get port and protocol
func (rm *ResourceManager) parseDrbdConfig(ctx context.Context, name, node string) (uint32, string, error) {
	if rm.deployment == nil {
		return 0, "", fmt.Errorf("deployment client not set")
	}

	result, err := rm.deployment.Exec(ctx, []string{node}, fmt.Sprintf("cat /etc/drbd.d/%s.res", name))
	if err != nil {
		return 0, "", err
	}

	var hostResult *deployment.HostResult
	for _, r := range result.Hosts {
		hostResult = r
		break
	}

	if hostResult == nil || !hostResult.Success {
		return 0, "", fmt.Errorf("failed to read config")
	}

	output := hostResult.Output

	// Parse port
	portRe := regexp.MustCompile(`address\s+[\d.]+:(\d+)`)
	portMatches := portRe.FindStringSubmatch(output)
	var port uint32
	if len(portMatches) > 1 {
		p, _ := strconv.ParseUint(portMatches[1], 10, 32)
		port = uint32(p)
	}

	// Parse protocol
	protocolRe := regexp.MustCompile(`protocol\s+(\w+)`)
	protocolMatches := protocolRe.FindStringSubmatch(output)
	protocol := "C" // default
	if len(protocolMatches) > 1 {
		protocol = protocolMatches[1]
	}

	return port, protocol, nil
}

// RemoveVolume removes a volume from a DRBD resource
func (rm *ResourceManager) RemoveVolume(ctx context.Context, resource string, volumeID uint32) error {
	rm.controller.logger.Info("Removing volume from resource",
		zap.String("resource", resource),
		zap.Uint32("volume_id", volumeID))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	// For now, this requires deleting the volume block from config
	// and bringing the resource down and up
	// This is complex and may need to be implemented carefully

	return fmt.Errorf("RemoveVolume not yet implemented")
}

// ResizeVolume resizes a DRBD volume
func (rm *ResourceManager) ResizeVolume(ctx context.Context, resource string, volumeID uint32, newSizeGB uint64) error {
	rm.controller.logger.Info("Resizing volume",
		zap.String("resource", resource),
		zap.Uint32("volume_id", volumeID),
		zap.Uint64("new_size_gb", newSizeGB))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	// Resize LV on all nodes first
	// Then call drbdadm resize

	return fmt.Errorf("ResizeVolume not yet implemented")
}

// Mount mounts a DRBD device
func (rm *ResourceManager) Mount(ctx context.Context, resource, mountPoint string, volumeID uint32, fsType string) error {
	return fmt.Errorf("Mount not yet implemented")
}

// Unmount unmounts a DRBD device
func (rm *ResourceManager) Unmount(ctx context.Context, resource, mountPoint string) error {
	return fmt.Errorf("Unmount not yet implemented")
}

// MakeHa creates a drbd-reactor promoter config for HA failover
func (rm *ResourceManager) MakeHa(ctx context.Context, resource string, services []string, mountPoint, fsType, vip string) (string, error) {
	rm.controller.logger.Info("Making resource HA",
		zap.String("resource", resource),
		zap.Strings("services", services),
		zap.String("mount_point", mountPoint),
		zap.String("fstype", fsType),
		zap.String("vip", vip))

	if rm.deployment == nil {
		return "", fmt.Errorf("deployment client not set")
	}

	// Get hosts for deployment
	rm.mu.RLock()
	hosts := rm.hosts
	rm.mu.RUnlock()

	if len(hosts) == 0 {
		return "", fmt.Errorf("no hosts configured")
	}

	// Get resource info to find nodes
	dbResource, err := rm.controller.db.GetResource(ctx, resource)
	if err != nil {
		return "", fmt.Errorf("failed to get resource from database: %w", err)
	}

	if dbResource == nil {
		return "", fmt.Errorf("resource not found: %s", resource)
	}

	nodes := strings.Split(dbResource.Nodes, ",")
	if len(nodes) == 0 {
		return "", fmt.Errorf("no nodes found for resource")
	}

	// Validate that all services exist on all nodes
	// This prevents failover failures when a service is missing on a standby node
	if len(services) > 0 {
		for _, svc := range services {
			// Check if service unit file exists on all nodes
			checkCmd := fmt.Sprintf("systemctl list-unit-files | grep '^%s.service' || systemctl list-unit-files | grep '^%s$'", svc, svc)
			result, err := rm.deployment.Exec(ctx, nodes, checkCmd)
			if err != nil {
				return "", fmt.Errorf("failed to check service %s on nodes: %w", svc, err)
			}

			var missingNodes []string
			for node, hr := range result.Hosts {
				if !hr.Success || hr.Output == "" {
					missingNodes = append(missingNodes, node)
				}
			}

			if len(missingNodes) > 0 {
				return "", fmt.Errorf("service %s not found on nodes: %v. Please install the service on all nodes before configuring HA", svc, missingNodes)
			}

			rm.controller.logger.Info("Service validated on all nodes",
				zap.String("service", svc))
		}
	}

	// Generate drbd-reactor promoter config
	configPath := fmt.Sprintf("/etc/drbd-reactor.d/sds-ha-%s.toml", resource)
	configContent := rm.generatePromoterConfig(resource, nodes, services, mountPoint, fsType, vip)

	rm.controller.logger.Debug("Generated promoter config",
		zap.String("config", configContent))

	// Distribute config to all hosts using DistributeConfig
	_, err = rm.deployment.DistributeConfig(ctx, hosts, configContent, configPath)
	if err != nil {
		return "", fmt.Errorf("failed to distribute promoter config: %w", err)
	}

	// Reload drbd-reactor on all hosts
	_, err = rm.deployment.ReactorReload(ctx, hosts)
	if err != nil {
		rm.controller.logger.Warn("Failed to reload drbd-reactor", zap.Error(err))
	}

	return configPath, nil
}

// EvictHa evicts the HA resource from the active node
// drbd-reactor will handle the complete failover process:
// 1. Mask the target on active node
// 2. Stop all services (mount, VIP, etc.)
// 3. Demote DRBD to Secondary
// 4. Wait for another node to promote to Primary
func (rm *ResourceManager) EvictHa(ctx context.Context, resource string) error {
	rm.controller.logger.Info("Evicting HA resource",
		zap.String("resource", resource))

	// Get hosts for deployment
	rm.mu.RLock()
	hosts := rm.hosts
	rm.mu.RUnlock()

	if len(hosts) == 0 {
		return fmt.Errorf("no hosts configured")
	}

	rm.controller.logger.Info("Hosts configured",
		zap.Strings("hosts", hosts))

	// Find the active (Primary) node
	activeNode, err := rm.findActiveNode(ctx, resource, hosts)
	if err != nil {
		return fmt.Errorf("failed to find active node: %w", err)
	}

	rm.controller.logger.Info("Found active node for eviction",
		zap.String("resource", resource),
		zap.String("active_node", activeNode))

	// The config name for drbd-reactorctl (without .toml extension)
	configName := fmt.Sprintf("sds-ha-%s", resource)

	// Get local hostname to check if active node is local
	hostnameBytes, _ := exec.Command("hostname").Output()
	localHostname := strings.TrimSpace(string(hostnameBytes))

	var errExec error
	var output []byte

	if activeNode == localHostname {
		// Execute locally using os/exec
		rm.controller.logger.Info("Executing evict locally",
			zap.String("hostname", activeNode))
		cmd := exec.Command("drbd-reactorctl", "evict", configName)
		output, errExec = cmd.CombinedOutput()
		if errExec != nil {
			rm.controller.logger.Error("Local evict failed",
				zap.String("output", string(output)),
				zap.Error(errExec))
			return fmt.Errorf("failed to evict HA resource: %w, output: %s", errExec, string(output))
		}
		rm.controller.logger.Info("Local evict output",
			zap.String("output", string(output)))
	} else {
		// Execute on remote node via dispatch
		evictCmd := fmt.Sprintf("sudo drbd-reactorctl evict %s", configName)
		rm.controller.logger.Debug("Executing evict command remotely",
			zap.String("host", activeNode),
			zap.String("command", evictCmd))

		result, err := rm.deployment.Exec(ctx, []string{activeNode}, evictCmd)
		if err != nil {
			return fmt.Errorf("failed to evict HA resource: %w", err)
		}

		// Log result for debugging
		for host, hr := range result.Hosts {
			rm.controller.logger.Debug("Evict command result",
				zap.String("host", host),
				zap.Bool("success", hr.Success),
				zap.String("output", hr.Output),
				zap.Any("error", hr.Error))
		}

		if !result.AllSuccess() {
			return fmt.Errorf("evict failed: %v", result.FailedHosts())
		}
	}

	rm.controller.logger.Info("HA resource evicted successfully",
		zap.String("resource", resource))

	return nil
}

// findActiveNode finds the node where the DRBD resource is currently Primary
func (rm *ResourceManager) findActiveNode(ctx context.Context, resource string, hosts []string) (string, error) {
	rm.controller.logger.Info("findActiveNode called",
		zap.String("resource", resource),
		zap.Int("hosts_count", len(hosts)))

	var localHostname string

	// First, check local node using os/exec (not dispatch)
	// Get local hostname
	hostnameBytes, err := exec.Command("hostname").Output()
	if err != nil {
		rm.controller.logger.Warn("Failed to get local hostname", zap.Error(err))
		localHostname = ""
	} else {
		localHostname = strings.TrimSpace(string(hostnameBytes))
		rm.controller.logger.Info("Local hostname", zap.String("hostname", localHostname))

		// Check if local node is Primary
		// No need for sudo since sds-controller runs as root
		checkCmd := exec.Command("drbdsetup", "status", resource)
		output, err := checkCmd.Output()
		if err != nil {
			rm.controller.logger.Warn("Failed to check local DRBD status",
				zap.Error(err),
				zap.String("stderr", string(err.(*exec.ExitError).Stderr)))
		} else {
			lines := strings.Split(string(output), "\n")
			rm.controller.logger.Info("Local DRBD status",
				zap.String("first_line", lines[0]),
				zap.Int("line_count", len(lines)))
			if len(lines) > 0 && strings.Contains(lines[0], "role:Primary") {
				rm.controller.logger.Info("Local node is Primary",
					zap.String("hostname", localHostname))
				return localHostname, nil
			}
			rm.controller.logger.Info("Local node is not Primary, checking remote hosts")
		}
	}

	rm.controller.logger.Info("Checking remote hosts",
		zap.Strings("hosts", hosts),
		zap.String("local_hostname", localHostname))

	for _, host := range hosts {
		// Skip if this is the local host
		if host == localHostname {
			rm.controller.logger.Info("Skipping local host",
				zap.String("host", host))
			continue
		}

		rm.controller.logger.Info("Checking remote host via direct SSH",
			zap.String("host", host),
			zap.String("resource", resource))

		// Get DRBD role - check if this host is Primary
		// Use direct SSH instead of dispatch due to output capture bug
		checkCmd := exec.Command("ssh", host, "drbdsetup", "status", resource)
		output, err := checkCmd.Output()
		if err != nil {
			rm.controller.logger.Debug("Failed to check remote DRBD status",
				zap.String("host", host),
				zap.Error(err))
			continue
		}

		outputStr := string(output)
		rm.controller.logger.Debug("SSH DRBD status",
			zap.String("host", host),
			zap.String("output", outputStr))

		if strings.Contains(outputStr, "role:Primary") {
			rm.controller.logger.Info("Found active node via SSH",
				zap.String("host", host))
			return host, nil
		}
	}

	return "", fmt.Errorf("no active (Primary) node found for resource %s", resource)
}

// generatePromoterConfig generates drbd-reactor promoter TOML config
func (rm *ResourceManager) generatePromoterConfig(resource string, nodes, services []string, mountPoint, fsType, vip string) string {
	var startActions []string

	// Add mount unit if mount point specified
	if mountPoint != "" {
		// Generate systemd mount unit name from path
		// e.g., /var/lib/sds -> var-lib-sds.mount
		mountUnit := strings.TrimPrefix(mountPoint, "/")
		mountUnit = strings.ReplaceAll(mountUnit, "/", "-")
		mountUnit = fmt.Sprintf("\"%s.mount\"", mountUnit)

		startActions = append(startActions, mountUnit)
	}

	// Add VIP if specified
	if vip != "" {
		// Parse CIDR to get IP and netmask
		ip := vip
		cidr := "32"
		if strings.Contains(vip, "/") {
			parts := strings.Split(vip, "/")
			ip = parts[0]
			cidr = parts[1]
		}

		// OCF agent as single-line string (like gateway format)
		vipOCF := fmt.Sprintf("\"ocf:heartbeat:IPaddr2 vip_%s ip=%s cidr_netmask=%s\"", resource, ip, cidr)
		startActions = append(startActions, vipOCF)
	}

	// Add systemd services
	for _, svc := range services {
		startActions = append(startActions, fmt.Sprintf(`  "%s"`, svc))
	}

	// Generate TOML config
	toml := fmt.Sprintf(`# drbd-reactor promoter configuration for HA resource: %s
# Generated by sds-controller

[[promoter]]
[promoter.resources.%s]
runner = "systemd"
start = [
%s
]
on-drbd-demote-failure = "reboot"

`, resource, resource, strings.Join(startActions, ",\n"))

	return toml
}

// CreateFilesystemOnly creates a filesystem on a DRBD device
func (rm *ResourceManager) CreateFilesystemOnly(ctx context.Context, resource string, volumeID uint32, fsType string) error {
	rm.controller.logger.Info("Creating filesystem",
		zap.String("resource", resource),
		zap.Uint32("volume_id", volumeID),
		zap.String("fstype", fsType))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	rm.mu.RLock()
	hosts := rm.hosts
	rm.mu.RUnlock()

	if len(hosts) == 0 {
		return fmt.Errorf("no hosts configured")
	}

	// Determine DRBD device path
	drbdDevice := fmt.Sprintf("/dev/drbd/by-res/%s/%d", resource, volumeID)

	// Create filesystem on the first node (should be Primary)
	mkfsCmd := fmt.Sprintf("sudo mkfs.%s -F %s", fsType, drbdDevice)
	result, err := rm.deployment.Exec(ctx, []string{hosts[0]}, mkfsCmd)
	if err != nil {
		return fmt.Errorf("failed to create filesystem: %w", err)
	}

	if !result.AllSuccess() {
		var errMsg string
		for host, h := range result.Hosts {
			if !h.Success {
				errMsg = fmt.Sprintf("%s: %s", host, h.Error)
				break
			}
		}
		return fmt.Errorf("filesystem creation failed: %s", errMsg)
	}

	rm.controller.logger.Info("Filesystem created successfully",
		zap.String("resource", resource),
		zap.String("device", drbdDevice),
		zap.String("fstype", fsType))

	return nil
}

// Helper functions for parsing DRBD status output

type volumeInfo struct {
	id      int
	device  string
	sizeGB  uint64
}

func parseRoleFromStatus(output string) string {
	if strings.Contains(output, "role:Primary") {
		return "Primary"
	}
	if strings.Contains(output, "role:Secondary") {
		return "Secondary"
	}
	return "Unknown"
}

// parseNodeStatesFromStatus parses each node's role and disk state from DRBD status output
// Format:
//   ha_res role:Primary
//     disk:UpToDate open:no
//   orange2 role:Secondary
//     peer-disk:UpToDate
func parseNodeStatesFromStatus(output string, nodes []string) map[string]*ResourceNodeState {
	nodeStates := make(map[string]*ResourceNodeState)
	lines := strings.Split(output, "\n")

	// Get local node's role (first line with role:)
	localRole := "Unknown"
	localDiskState := "Unknown"

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		// First line: "resource_name role:Primary"
		if i == 0 && strings.Contains(trimmed, "role:") {
			parts := strings.Fields(trimmed)
			for _, p := range parts {
				if strings.HasPrefix(p, "role:") {
					localRole = strings.TrimPrefix(p, "role:")
					localRole = strings.TrimSuffix(localRole, ",")
					break
				}
			}
		}
		// Local disk: "  disk:UpToDate open:no"
		if strings.HasPrefix(trimmed, "disk:") && !strings.Contains(trimmed, "peer-disk:") {
			parts := strings.Fields(trimmed)
			for _, p := range parts {
				if strings.HasPrefix(p, "disk:") {
					localDiskState = strings.TrimPrefix(p, "disk:")
					localDiskState = strings.TrimSuffix(localDiskState, ",")
					break
				}
			}
		}
	}

	// Set local node state (first node in list)
	if len(nodes) > 0 {
		nodeStates[nodes[0]] = &ResourceNodeState{
			Role:      localRole,
			DiskState: localDiskState,
		}
	}

	// Parse peer nodes: "  orange2 role:Secondary"
	currentNode := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		parts := strings.Fields(trimmed)

		// Check if this line starts with a node name followed by "role:"
		// This matches "orange2 role:Secondary" pattern
		if len(parts) >= 2 && strings.HasPrefix(parts[1], "role:") {
			// Find which node this is
			for _, node := range nodes {
				if node == nodes[0] {
					continue // Skip local node
				}
				if parts[0] == node {
					currentNode = node
					// Parse role from parts[1] which is "role:Secondary"
					role := strings.TrimPrefix(parts[1], "role:")
					role = strings.TrimSuffix(role, ",")
					if _, exists := nodeStates[currentNode]; !exists {
						nodeStates[currentNode] = &ResourceNodeState{Role: role}
					} else {
						nodeStates[currentNode].Role = role
					}
					break
				}
			}
		}

		// Check for peer-disk state (belongs to currentNode)
		if strings.Contains(trimmed, "peer-disk:") && currentNode != "" {
			parts := strings.Fields(trimmed)
			for j, p := range parts {
				if p == "peer-disk:" && j+1 < len(parts) {
					diskState := strings.TrimSuffix(parts[j+1], ",")
					if _, exists := nodeStates[currentNode]; !exists {
						nodeStates[currentNode] = &ResourceNodeState{DiskState: diskState}
					} else {
						nodeStates[currentNode].DiskState = diskState
					}
					break
				}
			}
		}
	}

	return nodeStates
}

func parseVolumesFromStatus(output string) []volumeInfo {
	var volumes []volumeInfo

	lines := strings.Split(output, "\n")
	currentVol := -1

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "volume:") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				if num, err := strconv.Atoi(parts[1]); err == nil {
					currentVol = num
				}
			}
		}
		if currentVol >= 0 && strings.Contains(trimmed, "disk:") {
			// Found disk line for current volume
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				device := strings.TrimSuffix(parts[1], ",")
				volumes = append(volumes, volumeInfo{
					id:     currentVol,
					device: device,
					sizeGB: 0, // Would need to query LVM for actual size
				})
			}
			currentVol = -1
		}
	}

	return volumes
}

func parseResourcesFromStatus(output string) []*ResourceInfo {
	lines := strings.Split(output, "\n")
	resources := make(map[string]*ResourceInfo)

	currentResource := ""

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// Resource line (without role)
		if !strings.Contains(trimmed, "role:") && !strings.Contains(trimmed, "volume:") &&
			!strings.HasPrefix(trimmed, "on ") && !strings.HasPrefix(trimmed, "connection-") {
			// This is likely a resource name
			currentResource = trimmed
			if resources[currentResource] == nil {
				resources[currentResource] = &ResourceInfo{
					Name:     currentResource,
					Volumes:  []*ResourceVolumeInfo{},
					Role:     "Unknown",
					NodeStates: make(map[string]*ResourceNodeState),
				}
			}
		}

		// Role line
		if strings.Contains(trimmed, "role:") && currentResource != "" {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				resources[currentResource].Role = strings.TrimSuffix(parts[1], ",")
			}
		}
	}

	// Convert map to slice
	result := make([]*ResourceInfo, 0, len(resources))
	for _, r := range resources {
		result = append(result, r)
	}

	return result
}
