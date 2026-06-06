package controller

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/deployment"
	"go.uber.org/zap"
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
	// Pool is the storage pool (volume group) backing this volume.
	Pool string
	// BackingVolume is the logical volume name inside the pool
	// (e.g. "<resource>_data"); "<pool>/<backing_volume>" is the path
	// consumed by snapshot operations.
	BackingVolume string
}

// ResourceManager manages DRBD resources using dispatch
type ResourceManager struct {
	controller *Controller
	deployment deploymentClient
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
func (rm *ResourceManager) SetDeployment(client deploymentClient) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.deployment = client
}

// SetHosts sets the list of hosts for resource operations
func (rm *ResourceManager) SetHosts(hosts []string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	// Clean hosts list and build map
	var cleanHosts []string
	rm.hostMap = make(map[string]string)

	for _, host := range hosts {
		// Try to resolve hostname to IP
		parts := strings.Split(host, ":")
		if len(parts) > 1 {
			// Format: "hostname:ip"
			hostname := parts[0]
			ip := parts[1]

			rm.hostMap[hostname] = ip
			cleanHosts = append(cleanHosts, ip)
		} else {
			// Format: "hostname"
			rm.hostMap[host] = host
			cleanHosts = append(cleanHosts, host)
		}
	}
	rm.hosts = cleanHosts
}

// GetHosts returns the list of hosts
func (rm *ResourceManager) GetHosts() []string {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.hosts
}

func (rm *ResourceManager) resourceHosts(ctx context.Context, resource string) ([]string, error) {
	if rm.controller.db != nil {
		dbRes, err := rm.controller.db.GetResource(ctx, resource)
		if err == nil && dbRes != nil {
			var hosts []string
			for _, node := range strings.Split(dbRes.Nodes, ",") {
				node = strings.TrimSpace(node)
				if node == "" {
					continue
				}
				hosts = append(hosts, rm.controller.ResolveHost(node))
			}
			if len(hosts) > 0 {
				return hosts, nil
			}
		}
	}

	rm.mu.RLock()
	defer rm.mu.RUnlock()
	if len(rm.hosts) == 0 {
		return nil, fmt.Errorf("no hosts configured")
	}
	return append([]string(nil), rm.hosts...), nil
}

type resourceConfigVolume struct {
	VolumeID  int
	Minor     int
	DiskPath  string
	StartLine int
	EndLine   int
}

func parseResourceConfigVolumes(content string) []resourceConfigVolume {
	lines := strings.Split(content, "\n")
	var volumes []resourceConfigVolume
	var current *resourceConfigVolume
	depth := 0

	for idx, line := range lines {
		trimmed := strings.TrimSpace(line)

		if current == nil && strings.HasPrefix(trimmed, "volume ") && strings.Contains(trimmed, "{") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				volID, err := strconv.Atoi(strings.TrimSuffix(parts[1], "{"))
				if err == nil {
					current = &resourceConfigVolume{VolumeID: volID, Minor: -1, StartLine: idx}
					depth = strings.Count(line, "{") - strings.Count(line, "}")
					if depth <= 0 {
						current.EndLine = idx
						volumes = append(volumes, *current)
						current = nil
						depth = 0
					}
					continue
				}
			}
		}

		if current == nil {
			continue
		}

		if strings.Contains(trimmed, "device") && strings.Contains(trimmed, "minor") {
			parts := strings.Fields(trimmed)
			for i, part := range parts {
				if part == "minor" && i+1 < len(parts) {
					if minor, err := strconv.Atoi(strings.TrimSuffix(parts[i+1], ";")); err == nil {
						current.Minor = minor
					}
					break
				}
			}
		}

		if strings.HasPrefix(trimmed, "disk") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				current.DiskPath = strings.TrimSuffix(parts[1], ";")
			}
		}

		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth <= 0 {
			current.EndLine = idx
			volumes = append(volumes, *current)
			current = nil
			depth = 0
		}
	}

	return volumes
}

func backingPathForVolume(pool, volumeName, storageType string) string {
	if storageType == "zfs" || storageType == "zfs-thin" {
		return fmt.Sprintf("/dev/zvol/%s/%s", pool, volumeName)
	}
	return fmt.Sprintf("/dev/%s/%s", pool, volumeName)
}

func poolAndVolumeFromDiskPath(diskPath string) (pool, volume string) {
	trimmed := strings.TrimSpace(diskPath)
	if strings.HasPrefix(trimmed, "/dev/zvol/") {
		parts := strings.Split(strings.TrimPrefix(trimmed, "/dev/zvol/"), "/")
		if len(parts) >= 2 {
			return parts[0], parts[len(parts)-1]
		}
		return "", ""
	}
	if strings.HasPrefix(trimmed, "/dev/") {
		parts := strings.Split(strings.TrimPrefix(trimmed, "/dev/"), "/")
		if len(parts) >= 2 {
			return parts[0], parts[len(parts)-1]
		}
	}
	return "", ""
}

func findVolumeRecord(volumes []*database.Volume, volumeID uint32) *database.Volume {
	for _, volume := range volumes {
		if volume.VolumeID == int(volumeID) {
			return volume
		}
	}
	return nil
}

// CreateResource creates a DRBD resource across multiple nodes
func (rm *ResourceManager) CreateResource(ctx context.Context, name string, port uint32, nodes []string, protocol string, sizeGB uint32, pool string, storageType string, drbdOptions map[string]string) error {
	rm.controller.logger.Info("Creating DRBD resource",
		zap.String("name", name),
		zap.Uint32("port", port),
		zap.Strings("nodes", nodes),
		zap.String("protocol", protocol),
		zap.Uint32("size_gb", sizeGB),
		zap.String("pool", pool),
		zap.String("storage_type", storageType),
		zap.Any("options", drbdOptions))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	if pool == "" {
		pool = "data-pool"
	}
	pool = normalizeManagedName(pool)

	if storageType == "" {
		storageType = "lvm"
	}

	if protocol == "" {
		protocol = "C"
	}

	// For both LVM and ZFS, we use a consistent volume name
	volumeName := fmt.Sprintf("%s_data", name)

	// Convert node names to IP addresses for deployment
	nodeIPs := make([]string, len(nodes))
	for i, node := range nodes {
		ip := rm.controller.nodes.GetNodeAddressByName(node)
		if ip == "" {
			ip = node // fallback to node name
		}
		nodeIPs[i] = ip
	}

	// 1. Create storage volumes on all nodes (LVM or ZFS)
	if storageType == "zfs" || storageType == "zfs-thin" {
		// Create ZFS zvol on all nodes
		// For zfs-thin, ZFSCreateThinDataset handles sparse creation (which is default for ZVOLs created with -s)
		for i, nodeIP := range nodeIPs {
			zvolPath := fmt.Sprintf("%s/%s", pool, volumeName)
			result, err := rm.deployment.ZFSCreateThinDataset(ctx, []string{nodeIP}, pool, volumeName, fmt.Sprintf("%dG", sizeGB))
			if err != nil {
				return fmt.Errorf("failed to create ZFS zvol on %s: %w", nodes[i], err)
			}
			if !result.AllSuccess() {
				for host, hres := range result.Hosts {
					if !hres.Success {
						return fmt.Errorf("ZFS zvol creation failed on %s: %s", host, hres.Output)
					}
				}
			}
			rm.controller.logger.Info("Created ZFS zvol",
				zap.String("zvol", zvolPath),
				zap.String("node", nodes[i]))
		}
	} else if storageType == "lvm-thin" {
		// Create LVM Thin LV
		// Convention: Thin Pool name is pool + "_thin"
		thinPoolName := pool + "_thin"
		for i, nodeIP := range nodeIPs {
			result, err := rm.deployment.LVCreateThinVolume(ctx, []string{nodeIP}, pool, thinPoolName, volumeName, fmt.Sprintf("%dG", sizeGB))
			if err != nil {
				return fmt.Errorf("failed to create Thin LV on %s: %w", nodes[i], err)
			}
			if !result.AllSuccess() {
				for host, hres := range result.Hosts {
					if !hres.Success {
						return fmt.Errorf("Thin LV creation failed on %s: %s", host, hres.Output)
					}
				}
			}
		}
	} else {
		// Create LVM LV on all nodes (default)
		for i, nodeIP := range nodeIPs {
			result, err := rm.deployment.LVCreate(ctx, []string{nodeIP}, pool, volumeName, fmt.Sprintf("%dG", sizeGB))
			if err != nil {
				return fmt.Errorf("failed to create LV on %s: %w", nodes[i], err)
			}
			if !result.AllSuccess() {
				for host, hres := range result.Hosts {
					if !hres.Success {
						return fmt.Errorf("LV creation failed on %s: %s", host, hres.Output)
					}
				}
			}
		}
	}

	// 2. Generate DRBD config
	drbdConfig := rm.generateDrbdConfig(name, port, nodes, protocol, pool, volumeName, storageType, drbdOptions)

	// 3. Distribute config to all nodes
	configResult, err := rm.deployment.DistributeConfig(ctx, nodeIPs, drbdConfig, fmt.Sprintf("/etc/drbd.d/%s.res", name))
	if err != nil {
		return fmt.Errorf("failed to distribute config: %w", err)
	}
	if !configResult.Success {
		return fmt.Errorf("config distribution failed on some hosts")
	}

	// 4. Create metadata on all nodes
	mdResult, err := rm.deployment.DRBDCreateMD(ctx, nodeIPs, name)
	if err != nil {
		return fmt.Errorf("failed to create metadata: %w", err)
	}
	if !mdResult.AllSuccess() {
		return fmt.Errorf("metadata creation failed on hosts: %v", mdResult.FailedHosts())
	}

	// 5. Bring up resource on all nodes
	upResult, err := rm.deployment.DRBDUp(ctx, nodeIPs, name)
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

		volumeRecord := &database.Volume{
			ResourceName: name,
			VolumeName:   volumeName,
			VolumeID:     0,
			Pool:         pool,
			SizeGB:       int(sizeGB),
			Device:       backingPathForVolume(pool, volumeName, storageType),
		}
		if err := rm.controller.db.SaveVolume(ctx, volumeRecord); err != nil {
			rm.controller.logger.Warn("Failed to save initial volume to database",
				zap.String("resource", name),
				zap.Error(err))
		}
	}

	rm.controller.logger.Info("DRBD resource created successfully",
		zap.String("name", name))

	return nil
}

// resolveToIP resolves a hostname to an IP address. If the input is already
// an IP address, it returns it unchanged. If resolution fails or returns a
// loopback address, it tries to find a non-loopback IP from network interfaces.
func resolveToIP(host string) string {
	// Check if it's already an IP address
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() {
			return host
		}
		// If it's a loopback IP, try to find a real IP
		return getFirstNonLoopbackIP()
	}

	// Try to resolve hostname to IP
	addrs, err := net.LookupHost(host)
	if err != nil || len(addrs) == 0 {
		return getFirstNonLoopbackIP()
	}

	// Prefer non-loopback IPv4 addresses
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip != nil && ip.To4() != nil && !ip.IsLoopback() {
			return addr
		}
	}

	// Fall back to first non-loopback address
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip != nil && !ip.IsLoopback() {
			return addr
		}
	}

	// If all resolved addresses are loopback, get IP from interfaces
	return getFirstNonLoopbackIP()
}

// getFirstNonLoopbackIP returns the first non-loopback IPv4 address from network interfaces
func getFirstNonLoopbackIP() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}

	for _, iface := range interfaces {
		// Skip loopback and down interfaces
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}

			if ip != nil && ip.To4() != nil && !ip.IsLoopback() {
				return ip.String()
			}
		}
	}

	return "127.0.0.1"
}

// generateDrbdConfig generates a DRBD resource configuration file
func (rm *ResourceManager) generateDrbdConfig(name string, port uint32, nodes []string, protocol, pool, volumeName, storageType string, options map[string]string) string {
	var config strings.Builder

	// Organize options by section -> key -> value
	sections := make(map[string]map[string]string)

	// Helper to set option
	setOption := func(section, key, value string) {
		if sections[section] == nil {
			sections[section] = make(map[string]string)
		}
		sections[section][key] = value
	}

	// Add defaults
	setOption("options", "auto-promote", "no")
	setOption("options", "quorum", "majority")
	setOption("options", "on-no-quorum", "io-error")
	setOption("options", "on-no-data-accessible", "io-error")
	setOption("options", "on-suspended-primary-outdated", "force-secondary")

	setOption("net", "rr-conflict", "retry-connect")

	// Process user options
	for k, v := range options {
		parts := strings.SplitN(k, "/", 2)
		if len(parts) == 2 {
			// section/key format (e.g. disk/on-io-error)
			section := strings.ToLower(parts[0])
			key := parts[1]
			setOption(section, key, v)
		} else {
			// default to options section
			setOption("options", k, v)
		}
	}

	config.WriteString(fmt.Sprintf("resource %s {\n", name))

	// Write configuration sections
	knownSections := []string{"options", "net", "startup", "handlers"} // disk handled separately inside volume
	processed := make(map[string]bool)

	for _, s := range knownSections {
		opts, ok := sections[s]

		// Always write net section to include protocol
		if s == "net" {
			config.WriteString("\n    net {\n")
			config.WriteString(fmt.Sprintf("        protocol %s;\n", protocol))
			if ok {
				var keys []string
				for k := range opts {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					config.WriteString(fmt.Sprintf("        %s %s;\n", k, opts[k]))
				}
			}
			config.WriteString("    }\n")
			processed[s] = true
			continue
		}

		if ok && len(opts) > 0 {
			config.WriteString(fmt.Sprintf("\n    %s {\n", s))

			// Sort keys for deterministic output
			var keys []string
			for k := range opts {
				keys = append(keys, k)
			}
			sort.Strings(keys)

			for _, k := range keys {
				config.WriteString(fmt.Sprintf("        %s %s;\n", k, opts[k]))
			}
			config.WriteString("    }\n")
			processed[s] = true
		}
	}

	// Write any other custom sections (excluding disk which is handled in volume)
	var customSections []string
	for s := range sections {
		if !processed[s] && s != "disk" {
			customSections = append(customSections, s)
		}
	}
	sort.Strings(customSections)

	for _, s := range customSections {
		// Generic write
		opts := sections[s]
		config.WriteString(fmt.Sprintf("\n    %s {\n", s))
		var keys []string
		for k := range opts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			config.WriteString(fmt.Sprintf("        %s %s;\n", k, opts[k]))
		}
		config.WriteString("    }\n")
	}

	// Generate volume 0 block
	config.WriteString("\n    volume 0 {\n")
	config.WriteString(fmt.Sprintf("        device    minor %d;\n", port-7000))

	// Use ZFS device path or LVM device path based on storage type
	var diskPath string
	if storageType == "zfs" || storageType == "zfs-thin" {
		diskPath = fmt.Sprintf("/dev/zvol/%s/%s", pool, volumeName)
	} else {
		diskPath = fmt.Sprintf("/dev/%s/%s", pool, volumeName)
	}
	config.WriteString(fmt.Sprintf("        disk      %s;\n", diskPath))
	config.WriteString("        meta-disk internal;\n")

	// Inject disk options here
	if diskOpts, ok := sections["disk"]; ok && len(diskOpts) > 0 {
		config.WriteString("        disk {\n")
		var keys []string
		for k := range diskOpts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			config.WriteString(fmt.Sprintf("            %s %s;\n", k, diskOpts[k]))
		}
		config.WriteString("        }\n")
	}

	config.WriteString("    }\n")

	// Generate on sections for each node
	var nodeIPs []string
	for i, node := range nodes {
		// Get IP address from NodeManager by node name
		ip := rm.controller.nodes.GetNodeAddressByName(node)

		// Fallback: try direct lookup in hostMap
		if ip == "" {
			rm.mu.RLock()
			ip = rm.hostMap[node]
			rm.mu.RUnlock()
		}

		// Final fallback to node name if still not found
		if ip == "" {
			ip = node
		}

		// Resolve hostname to IP address if not already an IP
		ip = resolveToIP(ip)

		nodeIPs = append(nodeIPs, ip)
		config.WriteString(fmt.Sprintf("\n    on %s {\n", node))
		config.WriteString(fmt.Sprintf("        address   %s:%d;\n", ip, port))
		config.WriteString(fmt.Sprintf("        node-id   %d;\n", i))
		config.WriteString("    }\n")
	}

	// Add connection-mesh for multi-node DRBD 9
	// DRBD 9 requires a full mesh of connections between all nodes
	if len(nodes) > 2 {
		config.WriteString("\n    connection-mesh {\n")
		config.WriteString("        hosts")
		for _, node := range nodes {
			config.WriteString(fmt.Sprintf(" %s", node))
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

	hosts, err := rm.resourceHosts(ctx, name)
	if err != nil {
		return nil, err
	}

	// Get resource info from database
	dbRes, err := rm.controller.db.GetResource(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("resource not found: %s", name)
	}

	dbVolumes, err := rm.controller.db.ListVolumes(ctx, name)
	if err != nil {
		rm.controller.logger.Warn("Failed to list resource volumes from database",
			zap.String("resource", name),
			zap.Error(err))
		dbVolumes = nil
	}
	dbVolumeByID := make(map[int]*database.Volume, len(dbVolumes))
	for _, volume := range dbVolumes {
		dbVolumeByID[volume.VolumeID] = volume
	}

	// Parse nodeAddresses from comma-separated string
	var nodeAddresses []string
	if dbRes.Nodes != "" {
		nodeAddresses = strings.Split(dbRes.Nodes, ",")
	}

	rm.controller.logger.Debug("GetResource",
		zap.String("name", name),
		zap.String("dbRes.Nodes", dbRes.Nodes),
		zap.Strings("parsed_nodeAddresses", nodeAddresses))

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
					sizeGB := v.sizeGB
					pool := ""
					backingVolume := ""
					if dbVol, ok := dbVolumeByID[v.id]; ok {
						if sizeGB == 0 {
							sizeGB = uint64(max(dbVol.SizeGB, 0))
						}
						pool = dbVol.Pool
						backingVolume = dbVol.VolumeName
					}
					volumes = append(volumes, &ResourceVolumeInfo{
						VolumeID:      uint32(v.id),
						Device:        v.device,
						SizeGB:        sizeGB,
						Pool:          pool,
						BackingVolume: backingVolume,
					})
				}

				// Parse node states from status output
				nodeStates = parseNodeStatesFromStatus(r.Output, nodeAddresses)

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
		Nodes:      nodeAddresses,
		Role:       localRole, // Local node's role
		Volumes:    volumes,
		NodeStates: nodeStates,
	}

	if len(info.Volumes) == 0 && len(dbVolumes) > 0 {
		for _, volume := range dbVolumes {
			info.Volumes = append(info.Volumes, &ResourceVolumeInfo{
				VolumeID:      uint32(volume.VolumeID),
				Device:        fmt.Sprintf("/dev/drbd/by-res/%s/%d", name, volume.VolumeID),
				SizeGB:        uint64(max(volume.SizeGB, 0)),
				Pool:          volume.Pool,
				BackingVolume: volume.VolumeName,
			})
		}
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
		// Parse nodeAddresses from comma-separated string
		var nodeAddresses []string
		if dbRes.Nodes != "" {
			nodeAddresses = strings.Split(dbRes.Nodes, ",")
		}

		resources = append(resources, &ResourceInfo{
			Name:       dbRes.Name,
			Port:       uint32(dbRes.Port),
			Protocol:   dbRes.Protocol,
			Nodes:      nodeAddresses,
			Role:       "Unknown", // Will be updated by GetResource if needed
			Volumes:    []*ResourceVolumeInfo{},
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
	pool = normalizeManagedName(pool)

	hosts, err := rm.resourceHosts(ctx, resource)
	if err != nil {
		return err
	}

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

	// Extend the synchronized DRBD resource config with the new volume block.
	// LINBIT recommends updating the config identically on all nodes and then
	// calling `drbdadm adjust <resource>` to let DRBD enable the new volume.
	volumeBlock := fmt.Sprintf("    volume %d {\n        device    minor %d;\n        disk      /dev/%s/%s;\n        meta-disk internal;\n    }",
		newVolNum, newMinor, pool, volume)

	// Create LVs on all nodes
	for _, host := range hosts {
		_, err := rm.deployment.LVCreate(ctx, []string{host}, pool, volume, fmt.Sprintf("%dG", sizeGB))
		if err != nil {
			return fmt.Errorf("failed to create LV on %s: %w", host, err)
		}
	}

	lines = strings.Split(hostResult.Output, "\n")
	insertIdx := len(lines)
	for idx := len(lines) - 1; idx >= 0; idx-- {
		if strings.TrimSpace(lines[idx]) == "}" {
			insertIdx = idx
			break
		}
	}
	updatedLines := append([]string{}, lines[:insertIdx]...)
	updatedLines = append(updatedLines, volumeBlock)
	updatedLines = append(updatedLines, lines[insertIdx:]...)
	updatedConfig := strings.Join(updatedLines, "\n")

	if _, err := rm.deployment.DistributeConfig(ctx, hosts, updatedConfig, fmt.Sprintf("/etc/drbd.d/%s.res", resource)); err != nil {
		return fmt.Errorf("failed to distribute updated config: %w", err)
	}

	adjustCmd := fmt.Sprintf("sudo drbdadm adjust %s", resource)
	adjustResult, err := rm.deployment.Exec(ctx, hosts, adjustCmd)
	if err != nil {
		return fmt.Errorf("failed to adjust resource after volume add: %w", err)
	}
	if !adjustResult.AllSuccess() {
		return fmt.Errorf("resource adjust failed on hosts: %v", adjustResult.FailedHosts())
	}

	rm.controller.logger.Info("Volume added successfully",
		zap.String("resource", resource),
		zap.String("volume", volume))

	if rm.controller.db != nil {
		if err := rm.controller.db.SaveVolume(ctx, &database.Volume{
			ResourceName: resource,
			VolumeName:   volume,
			VolumeID:     newVolNum,
			Pool:         pool,
			SizeGB:       int(sizeGB),
			Device:       fmt.Sprintf("/dev/%s/%s", pool, volume),
		}); err != nil {
			rm.controller.logger.Warn("Failed to save added volume to database",
				zap.String("resource", resource),
				zap.String("volume", volume),
				zap.Error(err))
		}
	}

	return nil
}

// DeleteResource deletes a DRBD resource from all nodeAddresses
func (rm *ResourceManager) DeleteResource(ctx context.Context, name string, force bool) error {
	rm.controller.logger.Info("Deleting DRBD resource",
		zap.String("name", name),
		zap.Bool("force", force))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	hosts, err := rm.resourceHosts(ctx, name)
	if err != nil {
		return err
	}

	// 1. Down resource on all nodeAddresses
	downResult, err := rm.deployment.DRBDDown(ctx, hosts, name)
	if err != nil {
		return fmt.Errorf("failed to bring down resource: %w", err)
	}

	if !downResult.AllSuccess() && !force {
		return fmt.Errorf("resource down failed on hosts: %v", downResult.FailedHosts())
	}

	// 2. Delete config file from all nodeAddresses
	err = rm.deployment.DeleteConfig(ctx, hosts, fmt.Sprintf("/etc/drbd.d/%s.res", name))
	if err != nil {
		return fmt.Errorf("failed to delete config: %w", err)
	}

	// 3. Delete backing volumes. This must happen BEFORE the database
	// records go away — they are the only remaining knowledge of which
	// LVs/zvols belong to this resource. Failures abort unless force is
	// set, so the records survive for a retry instead of leaking storage.
	if rm.controller.db != nil {
		volumes, listErr := rm.controller.db.ListVolumes(ctx, name)
		if listErr != nil {
			rm.controller.logger.Warn("Failed to list volumes for backing cleanup",
				zap.String("resource", name),
				zap.Error(listErr))
		}
		for _, volume := range volumes {
			if err := rm.deleteBackingVolume(ctx, hosts, volume); err != nil {
				if !force {
					return fmt.Errorf("failed to remove backing volume %s/%s (rerun with force to skip): %w",
						volume.Pool, volume.VolumeName, err)
				}
				rm.controller.logger.Warn("Failed to remove backing volume (force: continuing)",
					zap.String("resource", name),
					zap.String("volume", volume.VolumeName),
					zap.Error(err))
			}
		}

		for _, volume := range volumes {
			if err := rm.controller.db.DeleteVolume(ctx, name, volume.VolumeName); err != nil {
				rm.controller.logger.Warn("Failed to delete volume from database",
					zap.String("resource", name),
					zap.String("volume", volume.VolumeName),
					zap.Error(err))
			}
		}
		if err := rm.controller.db.DeleteResource(ctx, name); err != nil {
			rm.controller.logger.Warn("Failed to delete resource from database",
				zap.String("name", name),
				zap.Error(err))
		}
	}

	rm.controller.logger.Info("Resource deleted successfully",
		zap.String("name", name))

	return nil
}

// deleteBackingVolume removes a volume's backing LV or zvol on all hosts.
// The device path recorded at creation time identifies the storage type:
// "/dev/zvol/<pool>/<vol>" is ZFS, "/dev/<pool>/<vol>" is LVM.
func (rm *ResourceManager) deleteBackingVolume(ctx context.Context, hosts []string, volume *database.Volume) error {
	if volume.Pool == "" || volume.VolumeName == "" {
		return fmt.Errorf("volume record incomplete (pool=%q, volume=%q)", volume.Pool, volume.VolumeName)
	}
	var cmd string
	if strings.HasPrefix(volume.Device, "/dev/zvol/") {
		cmd = fmt.Sprintf("sudo zfs destroy %s/%s", volume.Pool, volume.VolumeName)
	} else {
		cmd = fmt.Sprintf("sudo lvremove -f %s/%s", volume.Pool, volume.VolumeName)
	}
	result, err := rm.deployment.Exec(ctx, hosts, cmd)
	if err != nil {
		return err
	}
	if !result.AllSuccess() {
		// Tolerate hosts where the volume is already gone.
		for host, hr := range result.Hosts {
			if !hr.Success &&
				!strings.Contains(hr.Output, "not found") &&
				!strings.Contains(hr.Output, "does not exist") {
				return fmt.Errorf("removal failed on %s: %s", host, strings.TrimSpace(hr.Output))
			}
		}
	}
	return nil
}

// SetPrimary sets a resource to Primary on the specified node
func (rm *ResourceManager) SetPrimary(ctx context.Context, resource, node string, force bool) error {
	// Resolve node name to address
	address := rm.controller.ResolveHost(node)

	rm.controller.logger.Info("Setting resource primary",
		zap.String("resource", resource),
		zap.String("node", node),
		zap.String("address", address),
		zap.Bool("force", force))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	result, err := rm.deployment.DRBDPrimary(ctx, address, resource, force)
	if err != nil {
		return fmt.Errorf("failed to set primary: %w", err)
	}

	if !result.Success {
		return fmt.Errorf("failed to set primary on %s: %s", node, result.Output)
	}

	return nil
}

// SetSecondary sets a resource to Secondary on the specified node
func (rm *ResourceManager) SetSecondary(ctx context.Context, resource, node string) error {
	address := rm.controller.ResolveHost(node)

	rm.controller.logger.Info("Setting resource secondary",
		zap.String("resource", resource),
		zap.String("node", node),
		zap.String("address", address))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	result, err := rm.deployment.DRBDSecondary(ctx, address, resource)
	if err != nil {
		return fmt.Errorf("failed to set secondary: %w", err)
	}

	if !result.Success {
		return fmt.Errorf("failed to set secondary on %s", node)
	}

	return nil
}

// RemoveVolume removes a volume from a DRBD resource
func (rm *ResourceManager) RemoveVolume(ctx context.Context, resource string, volumeID uint32) error {
	rm.controller.logger.Info("Removing volume from resource",
		zap.String("resource", resource),
		zap.Uint32("volume_id", volumeID))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	if volumeID == 0 {
		return fmt.Errorf("removing volume 0 is not supported")
	}

	hosts, err := rm.resourceHosts(ctx, resource)
	if err != nil {
		return err
	}

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

	configContent := hostResult.Output
	volumes := parseResourceConfigVolumes(configContent)
	var target *resourceConfigVolume
	for i := range volumes {
		if volumes[i].VolumeID == int(volumeID) {
			target = &volumes[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("volume %d not found", volumeID)
	}

	lines := strings.Split(configContent, "\n")
	start := target.StartLine
	if start > 0 && strings.TrimSpace(lines[start-1]) == "" {
		start--
	}
	updatedLines := append([]string{}, lines[:start]...)
	updatedLines = append(updatedLines, lines[target.EndLine+1:]...)
	newConfig := strings.Join(updatedLines, "\n")

	if _, err := rm.deployment.DistributeConfig(ctx, hosts, newConfig, fmt.Sprintf("/etc/drbd.d/%s.res", resource)); err != nil {
		return fmt.Errorf("failed to distribute updated config: %w", err)
	}

	if _, err := rm.deployment.Exec(ctx, hosts, fmt.Sprintf("sudo drbdadm adjust %s", resource)); err != nil {
		return fmt.Errorf("failed to adjust resource after config update: %w", err)
	}

	if strings.HasPrefix(target.DiskPath, "/dev/zvol/") {
		dataset := strings.TrimPrefix(target.DiskPath, "/dev/zvol/")
		if _, err := rm.deployment.ZFSDestroyDataset(ctx, hosts, dataset); err != nil {
			return fmt.Errorf("failed to delete ZFS backing volume: %w", err)
		}
	} else {
		removeCmd := fmt.Sprintf("sudo lvremove -f %s", target.DiskPath)
		if _, err := rm.deployment.Exec(ctx, hosts, removeCmd); err != nil {
			return fmt.Errorf("failed to delete LVM backing volume: %w", err)
		}
	}

	if rm.controller.db != nil {
		dbVolumes, err := rm.controller.db.ListVolumes(ctx, resource)
		if err == nil {
			if volume := findVolumeRecord(dbVolumes, volumeID); volume != nil {
				if err := rm.controller.db.DeleteVolume(ctx, resource, volume.VolumeName); err != nil {
					rm.controller.logger.Warn("Failed to delete volume metadata",
						zap.String("resource", resource),
						zap.String("volume", volume.VolumeName),
						zap.Error(err))
				}
			}
		}
	}

	return nil
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

	hosts, err := rm.resourceHosts(ctx, resource)
	if err != nil {
		return err
	}

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

	var target *resourceConfigVolume
	for _, volume := range parseResourceConfigVolumes(hostResult.Output) {
		if volume.VolumeID == int(volumeID) {
			v := volume
			target = &v
			break
		}
	}
	if target == nil {
		return fmt.Errorf("volume %d not found", volumeID)
	}

	sizeArg := fmt.Sprintf("%dG", newSizeGB)
	if strings.HasPrefix(target.DiskPath, "/dev/zvol/") {
		volumePath := strings.TrimPrefix(target.DiskPath, "/dev/zvol/")
		if _, err := rm.deployment.ZFSResizeVolume(ctx, hosts, volumePath, sizeArg); err != nil {
			return fmt.Errorf("failed to resize ZFS backing volume: %w", err)
		}
	} else {
		resizeCmd := fmt.Sprintf("sudo lvresize -L %s -y %s", sizeArg, target.DiskPath)
		if _, err := rm.deployment.Exec(ctx, hosts, resizeCmd); err != nil {
			return fmt.Errorf("failed to resize LVM backing volume: %w", err)
		}
	}

	if _, err := rm.deployment.Exec(ctx, []string{hosts[0]}, fmt.Sprintf("sudo drbdadm resize %s/%d", resource, volumeID)); err != nil {
		return fmt.Errorf("failed to resize DRBD volume: %w", err)
	}

	if rm.controller.db != nil {
		dbVolumes, err := rm.controller.db.ListVolumes(ctx, resource)
		if err == nil {
			if volume := findVolumeRecord(dbVolumes, volumeID); volume != nil {
				volume.SizeGB = int(newSizeGB)
				if err := rm.controller.db.SaveVolume(ctx, volume); err != nil {
					rm.controller.logger.Warn("Failed to update volume metadata",
						zap.String("resource", resource),
						zap.String("volume", volume.VolumeName),
						zap.Error(err))
				}
			}
		}
	}

	return nil
}

// Mount mounts a DRBD device
func (rm *ResourceManager) Mount(ctx context.Context, resource, mountPoint string, volumeID uint32, node, fsType string) error {
	// Resolve node to address
	address := rm.controller.ResolveHost(node)

	rm.controller.logger.Info("Mounting resource",
		zap.String("resource", resource),
		zap.String("mount_point", mountPoint),
		zap.Uint32("volume_id", volumeID),
		zap.String("node", node),
		zap.String("address", address),
		zap.String("fstype", fsType))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	drbdDevice := fmt.Sprintf("/dev/drbd/by-res/%s/%d", resource, volumeID)

	// Create mount point
	mkdirCmd := fmt.Sprintf("sudo mkdir -p %s", mountPoint)
	_, err := rm.deployment.Exec(ctx, []string{address}, mkdirCmd)
	if err != nil {
		return fmt.Errorf("failed to create mount point: %w", err)
	}

	// Mount
	mountCmd := fmt.Sprintf("sudo mount %s %s", drbdDevice, mountPoint)
	result, err := rm.deployment.Exec(ctx, []string{address}, mountCmd)
	if err != nil {
		return fmt.Errorf("failed to mount: %w", err)
	}
	if !result.AllSuccess() {
		return fmt.Errorf("mount failed on %s: %v", node, result.FailedHosts())
	}

	return nil
}

// Unmount unmounts a DRBD device
func (rm *ResourceManager) Unmount(ctx context.Context, resource string, volumeID uint32, node string) error {
	// Resolve node to address
	address := rm.controller.ResolveHost(node)

	rm.controller.logger.Info("Unmounting resource",
		zap.String("resource", resource),
		zap.Uint32("volume_id", volumeID),
		zap.String("node", node),
		zap.String("address", address))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	// Unmount by device path is safer if we know volume ID
	drbdDevice := fmt.Sprintf("/dev/drbd/by-res/%s/%d", resource, volumeID)

	umountCmd := fmt.Sprintf("sudo umount %s", drbdDevice)
	result, err := rm.deployment.Exec(ctx, []string{address}, umountCmd)
	if err != nil {
		return fmt.Errorf("failed to unmount: %w", err)
	}
	if !result.AllSuccess() {
		return fmt.Errorf("unmount failed on %s: %v", node, result.FailedHosts())
	}

	return nil
}

// generateSystemdMountUnit generates a systemd mount unit content
func (rm *ResourceManager) generateSystemdMountUnit(resource, mountPoint, fsType string) string {
	device := fmt.Sprintf("/dev/drbd/by-res/%s/0", resource)
	return fmt.Sprintf(`[Unit]
Description=Mount for %s
[Mount]
What=%s
Where=%s
Type=%s
[Install]
WantedBy=multi-user.target
`, resource, device, mountPoint, fsType)
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
	if rm.controller.db == nil {
		return "", fmt.Errorf("database not available")
	}

	// Get resource info to find nodeAddresses
	dbResource, err := rm.controller.db.GetResource(ctx, resource)
	if err != nil {
		return "", fmt.Errorf("failed to get resource from database: %w", err)
	}

	if dbResource == nil {
		return "", fmt.Errorf("resource not found: %s", resource)
	}

	nodeNames := strings.Split(dbResource.Nodes, ",")
	if len(nodeNames) == 0 {
		return "", fmt.Errorf("no nodes found for resource")
	}

	// Convert node names to addresses for deployment
	nodeAddresses := make([]string, len(nodeNames))
	for i, nodeName := range nodeNames {
		addr := rm.controller.nodes.GetNodeAddressByName(nodeName)
		if addr == "" {
			return "", fmt.Errorf("failed to resolve address for node: %s", nodeName)
		}
		nodeAddresses[i] = addr
	}
	hosts := nodeAddresses

	// Step 1: Check DRBD status and ensure resource is up
	rm.controller.logger.Info("Checking DRBD resource status",
		zap.String("resource", resource),
		zap.Strings("nodes", nodeAddresses))

	// First, ensure resource is up on all nodes
	rm.controller.logger.Info("Bringing up DRBD resource on all nodes")
	_, err = rm.deployment.Exec(ctx, nodeAddresses, "sudo drbdadm up "+resource)
	if err != nil {
		rm.controller.logger.Warn("Failed to bring up resource (continuing anyway)", zap.Error(err))
	}
	// Continue anyway - resource might already be up

	statusResult, err := rm.deployment.Exec(ctx, nodeAddresses, "sudo drbdadm status "+resource)
	if err != nil {
		return "", fmt.Errorf("failed to check DRBD status: %w", err)
	}

	// Check if any node is Primary, if not, set first node as Primary
	hasPrimary := false
	for _, r := range statusResult.Hosts {
		if r.Success && strings.Contains(string(r.Output), "role:Primary") {
			hasPrimary = true
			rm.controller.logger.Info("Found existing Primary node",
				zap.String("host", r.Host))
			break
		}
	}

	if !hasPrimary {
		rm.controller.logger.Info("No Primary node found, setting first node as Primary",
			zap.String("node", nodeNames[0]),
			zap.String("address", nodeAddresses[0]))
		if err := rm.SetPrimary(ctx, resource, nodeAddresses[0], true); err != nil {
			return "", fmt.Errorf("failed to set Primary: %w", err)
		}
		rm.controller.logger.Info("Primary set successfully",
			zap.String("node", nodeNames[0]))
	}

	// Step 2: Create filesystem if mount point and fs type are specified
	if mountPoint != "" && fsType != "" {
		rm.controller.logger.Info("Creating filesystem",
			zap.String("resource", resource),
			zap.String("fstype", fsType),
			zap.String("volume", "0"))

		// Check if filesystem already exists by checking if device can be read
		drbdDevice := fmt.Sprintf("/dev/drbd/by-res/%s/0", resource)
		checkFsCmd := fmt.Sprintf("sudo blkid -o value -s TYPE %s 2>/dev/null || echo 'none'", drbdDevice)
		checkResult, err := rm.deployment.Exec(ctx, []string{nodeAddresses[0]}, checkFsCmd)

		needsFs := true
		if err == nil {
			for _, r := range checkResult.Hosts {
				if r.Success {
					fsTypeFound := strings.TrimSpace(string(r.Output))
					if fsTypeFound != "none" && fsTypeFound != "" {
						rm.controller.logger.Info("Filesystem already exists",
							zap.String("existing_fstype", fsTypeFound))
						needsFs = false
						break
					}
				}
			}
		}

		if needsFs {
			if err := rm.CreateFilesystemOnly(ctx, resource, 0, fsType, nodeAddresses[0]); err != nil {
				return "", fmt.Errorf("failed to create filesystem: %w", err)
			}
			rm.controller.logger.Info("Filesystem created successfully")
		}
	}

	// Validate that all services exist on all nodes
	// This prevents failover failures when a service is missing on a standby node
	if len(services) > 0 {
		for _, svc := range services {
			// Check if service unit file exists on all nodes
			// Use systemctl show to check LoadState - "loaded" means unit file exists
			checkCmd := fmt.Sprintf("systemctl show %s -p LoadState 2>/dev/null || echo 'not-found'", svc)
			result, err := rm.deployment.Exec(ctx, nodeAddresses, checkCmd)
			if err != nil {
				return "", fmt.Errorf("failed to check service %s on nodes: %w", svc, err)
			}

			var missingNodes []string
			for node, hr := range result.Hosts {
				output := strings.TrimSpace(hr.Output)
				// Service exists if LoadState is "loaded"
				if !strings.Contains(output, "LoadState=loaded") {
					missingNodes = append(missingNodes, node)
				}
			}

			if len(missingNodes) > 0 {
				return "", fmt.Errorf("service %s not found on nodes: %v. Please install the service on all nodes before configuring HA", svc, missingNodes)
			}

			rm.controller.logger.Info("Service validated on all nodes",
				zap.String("service", svc))
		}

		// Stop and disable services on all nodes before HA takeover
		rm.controller.logger.Info("Stopping and disabling services on all nodes for HA takeover",
			zap.Strings("services", services))

		for _, svc := range services {
			// Stop service
			stopCmd := fmt.Sprintf("systemctl stop %s", svc)
			if _, err := rm.deployment.Exec(ctx, nodeAddresses, stopCmd); err != nil {
				rm.controller.logger.Warn("Failed to stop service", zap.String("service", svc), zap.Error(err))
			}

			// Disable service
			disableCmd := fmt.Sprintf("systemctl disable %s", svc)
			if _, err := rm.deployment.Exec(ctx, nodeAddresses, disableCmd); err != nil {
				rm.controller.logger.Warn("Failed to disable service", zap.String("service", svc), zap.Error(err))
			}
		}

		// Migrate existing data to /tmp before HA takeover
		if mountPoint != "" {
			rm.controller.logger.Info("Backing up existing data before HA takeover",
				zap.String("mount_point", mountPoint))

			backupDir := fmt.Sprintf("/tmp/ha_backup_%s", strings.ReplaceAll(mountPoint, "/", "_"))

			// Use rsync or cp -a to backup all files including hidden ones and subdirectories
			backupCmd := fmt.Sprintf("if [ -d \"%s\" ]; then mkdir -p %s && rsync -a %s/ %s/ 2>/dev/null || cp -a %s/. %s/. 2>/dev/null; fi",
				mountPoint, backupDir, mountPoint, backupDir, mountPoint, backupDir)

			if _, err := rm.deployment.Exec(ctx, nodeAddresses, backupCmd); err != nil {
				rm.controller.logger.Warn("Failed to backup data (continuing anyway)",
					zap.String("mount_point", mountPoint),
					zap.Error(err))
			} else {
				rm.controller.logger.Info("Data backup completed",
					zap.String("backup_dir", backupDir))
			}
		}
	}

	// Handle mount unit creation
	if mountPoint != "" {
		mountUnitName := strings.TrimPrefix(mountPoint, "/")
		mountUnitName = strings.ReplaceAll(mountUnitName, "/", "-")
		mountUnitName = fmt.Sprintf("%s.mount", mountUnitName)

		mountContent := rm.generateSystemdMountUnit(resource, mountPoint, fsType)
		mountPath := fmt.Sprintf("/etc/systemd/system/%s", mountUnitName)

		rm.controller.logger.Info("Distributing mount unit", zap.String("path", mountPath))

		if _, err := rm.deployment.DistributeConfig(ctx, hosts, mountContent, mountPath); err != nil {
			return "", fmt.Errorf("failed to distribute mount unit: %w", err)
		}

		// Reload systemd to pick up new unit
		if _, err := rm.deployment.Exec(ctx, hosts, "systemctl daemon-reload"); err != nil {
			rm.controller.logger.Warn("Failed to reload systemd", zap.Error(err))
		}
	}

	// Generate drbd-reactor promoter config
	configPath := fmt.Sprintf("/etc/drbd-reactor.d/sds-ha-%s.toml", resource)
	configContent := rm.generatePromoterConfig(resource, nodeAddresses, services, mountPoint, fsType, vip)

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

	// Restore backed up data after drbd-reactor takes over
	if mountPoint != "" {
		rm.controller.logger.Info("Restoring backed up data after HA takeover",
			zap.String("mount_point", mountPoint))

		backupDir := fmt.Sprintf("/tmp/ha_backup_%s", strings.ReplaceAll(mountPoint, "/", "_"))

		// Find the active (primary) node for restoration
		activeNode, err := rm.findActiveNode(ctx, resource, hosts)
		if err != nil {
			rm.controller.logger.Warn("Failed to find active node for data restore",
				zap.Error(err))
		} else {
			rm.controller.logger.Info("Restoring data on active node",
				zap.String("active_node", activeNode),
				zap.String("backup_dir", backupDir))

			// Restore data preserving original permissions with rsync/cp -a
			// Backup is kept at /tmp/ha_backup_* for manual recovery if needed
			restoreCmd := fmt.Sprintf(
				"if [ -d \"%s\" ] && [ \"$(ls -A %s 2>/dev/null)\" ]; then "+
					"mkdir -p %s && "+
					"rsync -a %s/ %s/ 2>/dev/null || cp -a %s/. %s/. && "+
					"echo 'Data restored successfully. Backup retained at %s for manual recovery if needed.'; "+
					"else echo 'No backup found or backup is empty'; fi",
				backupDir, backupDir, mountPoint, backupDir, mountPoint, mountPoint, backupDir, backupDir)

			result, err := rm.deployment.Exec(ctx, []string{activeNode}, restoreCmd)
			if err != nil {
				rm.controller.logger.Warn("Failed to restore data",
					zap.String("active_node", activeNode),
					zap.Error(err))
			} else {
				for host, hr := range result.Hosts {
					if hr.Output != "" {
						rm.controller.logger.Info("Restore result",
							zap.String("node", host),
							zap.String("output", hr.Output))
					}
				}
			}
		}
	}

	// Save HA config to database
	if rm.controller.db != nil {
		haCfg := &database.HaConfig{
			Resource:   resource,
			VIP:        vip,
			MountPoint: mountPoint,
			FsType:     fsType,
			Services:   services,
		}
		if err := rm.controller.db.SaveHaConfig(ctx, haCfg); err != nil {
			rm.controller.logger.Warn("Failed to save HA config to database", zap.Error(err))
		}
	}

	return configPath, nil
}

// ListHaConfigs lists all HA configurations from database
func (rm *ResourceManager) ListHaConfigs(ctx context.Context) ([]*database.HaConfig, error) {
	if rm.controller.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	return rm.controller.db.ListHaConfigs(ctx)
}

// GetHaConfig gets an HA configuration from database
func (rm *ResourceManager) GetHaConfig(ctx context.Context, resource string) (*database.HaConfig, error) {
	if rm.controller.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	return rm.controller.db.GetHaConfig(ctx, resource)
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

	hosts, err := rm.resourceHosts(ctx, resource)
	if err != nil {
		return err
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

	// Evicting the controller's own metadata resource stops this very
	// process mid-eviction: a synchronous drbd-reactorctl child (local or
	// SSH session) dies with us and aborts the eviction half-way. Launch it
	// detached through systemd-run on the active node and return.
	if resource == SelfHaResource {
		evictCmd := fmt.Sprintf(
			"sudo systemd-run --unit=sds-selfha-evict --collect drbd-reactorctl evict %s", configName)
		if err := rm.execAllSuccess(ctx, []string{rm.controller.ResolveHost(activeNode)}, evictCmd,
			"failed to launch detached self-eviction"); err != nil {
			return err
		}
		rm.controller.logger.Info("Detached self-eviction launched",
			zap.String("resource", resource),
			zap.String("active_node", activeNode))
		return nil
	}

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

	// Prefer drbd-reactor's structured JSON status for HA resources when available.
	for _, host := range hosts {
		promoter, err := rm.deployment.ReactorPromoterStatusByResource(ctx, host, resource)
		if err != nil || promoter == nil {
			continue
		}
		if promoter.PrimaryOn != "" {
			if primaryHost := rm.getNodeHost(promoter.PrimaryOn); primaryHost != "" {
				rm.controller.logger.Info("Found active node from reactor status",
					zap.String("resource", resource),
					zap.String("primary_on", promoter.PrimaryOn),
					zap.String("resolved_host", primaryHost))
				return primaryHost, nil
			}
			rm.controller.logger.Info("Using reactor primary_on directly",
				zap.String("resource", resource),
				zap.String("primary_on", promoter.PrimaryOn))
			return promoter.PrimaryOn, nil
		}
	}

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

		rm.controller.logger.Info("Checking remote host via dispatch",
			zap.String("host", host),
			zap.String("resource", resource))

		// Get DRBD role - check if this host is Primary
		cmd := fmt.Sprintf("drbdadm status %s", resource)
		result, err := rm.deployment.Exec(ctx, []string{host}, cmd)
		if err != nil {
			rm.controller.logger.Debug("Failed to check DRBD status",
				zap.String("host", host),
				zap.Error(err))
			continue
		}

		// Parse DRBD status output to find the Primary node
		if result != nil && len(result.Hosts) > 0 {
			for _, hr := range result.Hosts {
				if !hr.Success {
					continue
				}
				output := string(hr.Output)
				rm.controller.logger.Debug("Host result",
					zap.String("host", host),
					zap.Bool("success", hr.Success),
					zap.String("output", output))

				// Parse DRBD status to find which node is Primary
				// Output format:
				//   resource_name role:Secondary
				//     nodename1 role:Primary
				//     nodename2 role:Secondary
				lines := strings.Split(output, "\n")
				rm.controller.logger.Debug("Parsing DRBD status",
					zap.String("host", host),
					zap.Int("line_count", len(lines)))
				for i, line := range lines {
					trimmed := strings.TrimSpace(line)
					rm.controller.logger.Debug("Checking DRBD line",
						zap.String("host", host),
						zap.Int("line_index", i),
						zap.String("line", trimmed))
					// Check if this line defines a node's role
					// Format: "nodename role:Role" or "resource role:Role"
					if strings.Contains(trimmed, " role:Primary") {
						parts := strings.SplitN(trimmed, " ", 2)
						rm.controller.logger.Debug("Split result",
							zap.Int("parts_count", len(parts)),
							zap.String("part0", parts[0]),
							zap.String("part1", parts[1]))
						if len(parts) >= 2 && strings.HasPrefix(parts[1], "role:Primary") {
							primaryNode := strings.TrimSpace(parts[0])
							rm.controller.logger.Info("Found Primary node",
								zap.String("primary_node", primaryNode),
								zap.String("original_line", trimmed))

							// Get IP/hostname for the primary node
							primaryHost := rm.getNodeHost(primaryNode)
							if primaryHost != "" {
								rm.controller.logger.Info("Resolved primary node to host",
									zap.String("node", primaryNode),
									zap.String("host", primaryHost))
								return primaryHost, nil
							}
							// If not found in hosts map, return the node name directly
							rm.controller.logger.Info("Using node name directly",
								zap.String("node", primaryNode))
							return primaryNode, nil
						}
					}
				}
			}
		}
	}

	return "", fmt.Errorf("no active (Primary) node found for resource %s", resource)
}

// getNodeHost gets the host address for a node name
// hosts format is "nodename:ip" or just "nodename"
func (rm *ResourceManager) getNodeHost(nodeName string) string {
	if resolved := rm.controller.ResolveHost(nodeName); resolved != nodeName {
		return resolved
	}

	rm.mu.RLock()
	defer rm.mu.RUnlock()

	for _, host := range rm.hosts {
		// Check if host matches nodename:ip format
		if strings.Contains(host, ":") {
			parts := strings.SplitN(host, ":", 2)
			if parts[0] == nodeName {
				return host
			}
		} else if host == nodeName {
			return host
		}
	}
	return ""
}

// RemoveHa removes HA configuration for a resource
func (rm *ResourceManager) RemoveHa(ctx context.Context, resource string) error {
	rm.controller.logger.Info("Removing HA configuration", zap.String("resource", resource))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}
	if rm.controller.db == nil {
		return fmt.Errorf("database not available")
	}

	hosts, err := rm.resourceHosts(ctx, resource)
	if err != nil {
		return err
	}

	// Get HA config to know what to clean up
	haCfg, err := rm.controller.db.GetHaConfig(ctx, resource)
	if err != nil {
		return fmt.Errorf("failed to get HA config: %w", err)
	}

	// 1. Delete promoter config
	configPath := fmt.Sprintf("/etc/drbd-reactor.d/sds-ha-%s.toml", resource)
	if err := rm.deployment.DeleteConfig(ctx, hosts, configPath); err != nil {
		rm.controller.logger.Warn("Failed to delete promoter config", zap.Error(err))
	}

	// 2. Delete mount unit if it exists
	if haCfg.MountPoint != "" {
		mountUnitName := strings.TrimPrefix(haCfg.MountPoint, "/")
		mountUnitName = strings.ReplaceAll(mountUnitName, "/", "-")
		mountUnitName = fmt.Sprintf("%s.mount", mountUnitName)
		mountPath := fmt.Sprintf("/etc/systemd/system/%s", mountUnitName)

		if err := rm.deployment.DeleteConfig(ctx, hosts, mountPath); err != nil {
			rm.controller.logger.Warn("Failed to delete mount unit", zap.Error(err))
		}
	}

	// 3. Reload daemons
	if _, err := rm.deployment.Exec(ctx, hosts, "systemctl daemon-reload && systemctl reload drbd-reactor"); err != nil {
		rm.controller.logger.Warn("Failed to reload daemons", zap.Error(err))
	}

	// 4. Remove from database
	if err := rm.controller.db.DeleteHaConfig(ctx, resource); err != nil {
		return fmt.Errorf("failed to delete HA config from database: %w", err)
	}

	return nil
}

// generatePromoterConfig generates drbd-reactor promoter TOML config
func (rm *ResourceManager) generatePromoterConfig(resource string, nodeAddresses, services []string, mountPoint, fsType, vip string) string {
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
		// Use service-ip systemd unit
		// Format: service-ip@<IP>-<MASK>.service (replace / with -)
		vipParam := strings.ReplaceAll(vip, "/", "-")
		if !strings.Contains(vipParam, "-") {
			vipParam = vipParam + "-32"
		}

		serviceIPUnit := fmt.Sprintf("\"service-ip@%s.service\"", vipParam)
		startActions = append(startActions, serviceIPUnit)
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
func (rm *ResourceManager) CreateFilesystemOnly(ctx context.Context, resource string, volumeID uint32, fsType string, node string) error {
	// Resolve node to address
	address := rm.controller.ResolveHost(node)

	rm.controller.logger.Info("Creating filesystem",
		zap.String("resource", resource),
		zap.Uint32("volume_id", volumeID),
		zap.String("fstype", fsType),
		zap.String("node", node),
		zap.String("address", address))

	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}

	// Determine DRBD device path
	drbdDevice := fmt.Sprintf("/dev/drbd/by-res/%s/%d", resource, volumeID)

	// Create filesystem on the specified node (should be Primary)
	// Note: xfs/btrfs use -f (lowercase), ext4 uses -F (uppercase)
	fsType = strings.ToLower(strings.TrimSpace(fsType))
	forceFlag := "-F"
	if fsType == "xfs" || fsType == "btrfs" {
		forceFlag = "-f"
	}
	mkfsCmd := fmt.Sprintf("sudo mkfs.%s %s %s", fsType, forceFlag, drbdDevice)
	result, err := rm.deployment.Exec(ctx, []string{address}, mkfsCmd)
	if err != nil {
		return fmt.Errorf("failed to create filesystem: %w", err)
	}

	if !result.AllSuccess() {
		var errMsg string
		for host, h := range result.Hosts {
			if !h.Success {
				errMsg = fmt.Sprintf("%s: %s", host, h.Output)
				break
			}
		}
		return fmt.Errorf("filesystem creation failed: %s", errMsg)
	}

	return nil
}

// Helper functions for parsing DRBD status output

type volumeInfo struct {
	id     int
	device string
	sizeGB uint64
}

// isIndentedStatusLine reports whether a raw drbdadm/drbdsetup status line is
// indented. Peer and per-volume detail lines are indented; the local resource
// line is not.
func isIndentedStatusLine(line string) bool {
	return strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
}

// localStatusLine returns the trimmed local resource line from drbdadm or
// drbdsetup status output. The local line is the first unindented line that
// carries a "role:" field; unindented lines without one (such as the
// "drbdsetup status <res> --verbose" command echo printed by
// "drbdadm status --verbose") are skipped.
func localStatusLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if line == "" || isIndentedStatusLine(line) {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "role:") {
			return trimmed
		}
	}
	return ""
}

func parseRoleFromStatus(output string) string {
	for _, field := range strings.Fields(localStatusLine(output)) {
		if strings.HasPrefix(field, "role:") {
			role := strings.TrimSuffix(strings.TrimPrefix(field, "role:"), ",")
			switch role {
			case "Primary", "Secondary":
				return role
			}
		}
	}
	return "Unknown"
}

// parseNodeStatesFromStatus parses each node's role and disk state from DRBD status output
// Format:
//
//	ha_res role:Primary
//	  disk:UpToDate open:no
//	orange2 role:Secondary
//	  peer-disk:UpToDate
func parseNodeStatesFromStatus(output string, nodeAddresses []string) map[string]*ResourceNodeState {
	nodeStates := make(map[string]*ResourceNodeState)
	lines := strings.Split(output, "\n")

	// Get local node's role (first line with role:)
	localRole := "Unknown"
	localDiskState := "Unknown"

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Local resource line: the first unindented line carrying "role:".
		// Skips the "drbdsetup status <res> --verbose" command echo emitted
		// by "drbdadm status --verbose"; peer role lines are indented.
		if localRole == "Unknown" && !isIndentedStatusLine(line) && strings.Contains(trimmed, "role:") {
			parts := strings.Fields(trimmed)
			for _, p := range parts {
				if strings.HasPrefix(p, "role:") {
					localRole = strings.TrimPrefix(p, "role:")
					localRole = strings.TrimSuffix(localRole, ",")
					break
				}
			}
		}
		// Local disk can be a standalone "disk:UpToDate" line or a verbose
		// volume line such as "volume:0 minor:0 disk:UpToDate ..."
		if !strings.Contains(trimmed, "peer-disk:") && (strings.HasPrefix(trimmed, "disk:") || (strings.Contains(trimmed, "volume:") && strings.Contains(trimmed, "disk:"))) {
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
	if len(nodeAddresses) > 0 {
		nodeStates[nodeAddresses[0]] = &ResourceNodeState{
			Role:      localRole,
			DiskState: localDiskState,
		}
	}

	// Parse peer nodeAddresses: "  orange2 role:Secondary"
	currentNode := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		parts := strings.Fields(trimmed)

		// Check if this line starts with a node name followed by "role:"
		// This matches "orange2 role:Secondary" pattern
		if len(parts) >= 2 {
			role := ""
			for _, part := range parts[1:] {
				if strings.HasPrefix(part, "role:") {
					role = strings.TrimSuffix(strings.TrimPrefix(part, "role:"), ",")
					break
				}
			}
			if role != "" {
				// Find which node this is
				for _, node := range nodeAddresses {
					if node == nodeAddresses[0] {
						continue // Skip local node
					}
					if parts[0] == node {
						currentNode = node
						if _, exists := nodeStates[currentNode]; !exists {
							nodeStates[currentNode] = &ResourceNodeState{Role: role}
						} else {
							nodeStates[currentNode].Role = role
						}
						break
					}
				}
			}
		}

		// Check for peer-disk state (belongs to currentNode)
		if strings.Contains(trimmed, "peer-disk:") && currentNode != "" {
			parts := strings.Fields(trimmed)
			for _, p := range parts {
				if strings.HasPrefix(p, "peer-disk:") {
					diskState := strings.TrimSuffix(strings.TrimPrefix(p, "peer-disk:"), ",")
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
	seen := make(map[int]bool)

	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "volume:") || strings.Contains(trimmed, "peer-disk:") {
			continue
		}

		fields := strings.Fields(trimmed)
		volumeID := -1
		minor := -1
		var sizeGB uint64
		for i, field := range fields {
			switch {
			case strings.HasPrefix(field, "volume:"):
				value := strings.TrimPrefix(field, "volume:")
				if value == "" && i+1 < len(fields) {
					value = fields[i+1]
				}
				if parsed, err := strconv.Atoi(strings.TrimSuffix(value, ",")); err == nil {
					volumeID = parsed
				}
			case strings.HasPrefix(field, "minor:"):
				if parsed, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(field, "minor:"), ",")); err == nil {
					minor = parsed
				}
			case strings.HasPrefix(field, "size:"):
				if parsed, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(field, "size:"), ","), 10, 64); err == nil {
					sizeGB = parsed / 1024 / 1024
				}
			}
		}

		if volumeID < 0 || seen[volumeID] {
			continue
		}

		device := ""
		if minor >= 0 {
			device = fmt.Sprintf("/dev/drbd%d", minor)
		}
		volumes = append(volumes, volumeInfo{
			id:     volumeID,
			device: device,
			sizeGB: sizeGB,
		})
		seen[volumeID] = true
	}

	return volumes
}
