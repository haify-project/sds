package controller

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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
	// DisklessNodes are nodes that join the resource purely as quorum
	// tiebreakers: they vote but store no data.
	DisklessNodes []string
	// QuorumRisk is true when the resource has exactly two diskful nodes and
	// no tiebreaker, so losing either node suspends I/O (no quorum majority).
	QuorumRisk bool
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

// disklessHosts returns the resolved addresses of a resource's diskless quorum
// tiebreaker nodes, or nil when it has none. Kept separate from resourceHosts
// because tiebreakers must never be treated as data-bearing nodes (e.g. as
// failover Primary candidates) — only teardown needs them.
func (rm *ResourceManager) disklessHosts(ctx context.Context, resource string) []string {
	if rm.controller.db == nil {
		return nil
	}
	dbRes, err := rm.controller.db.GetResource(ctx, resource)
	if err != nil || dbRes == nil || dbRes.DisklessNodes == "" {
		return nil
	}
	var hosts []string
	for _, node := range strings.Split(dbRes.DisklessNodes, ",") {
		node = strings.TrimSpace(node)
		if node == "" {
			continue
		}
		hosts = append(hosts, rm.controller.ResolveHost(node))
	}
	return hosts
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

func findVolumeRecord(volumes []*database.Volume, volumeID uint32) *database.Volume {
	for _, volume := range volumes {
		if volume.VolumeID == int(volumeID) {
			return volume
		}
	}
	return nil
}

// VolumeSpec describes one DRBD volume to create: its size and (optionally) the
// pool it is backed by. The storage type is a resource-level property shared by
// all volumes.
type VolumeSpec struct {
	SizeGB uint32
	Pool   string
}

// resolvedVolume is a VolumeSpec with its pool auto-selected/normalized, a
// concrete backing-volume name and (later) an allocated device minor.
type resolvedVolume struct {
	id         int
	volumeName string
	pool       string
	sizeGB     uint32
	minor      int
}

// CreateResource creates a single-volume DRBD resource. It is a thin wrapper
// over CreateResourceWithVolumes retained for existing callers (CLI, self-HA,
// CSI) that only ever create one volume.
func (rm *ResourceManager) CreateResource(ctx context.Context, name string, port uint32, nodes []string, protocol string, sizeGB uint32, pool string, storageType string, drbdOptions map[string]string) error {
	return rm.CreateResourceWithVolumes(ctx, name, port, nodes, protocol, storageType, drbdOptions,
		[]VolumeSpec{{SizeGB: sizeGB, Pool: pool}})
}

// CreateResourceWithVolumes creates a DRBD resource with one or more volumes
// (volume 0..N) atomically across the given nodes. All volumes share the
// resource's storage type; each may target its own pool.
func (rm *ResourceManager) CreateResourceWithVolumes(ctx context.Context, name string, port uint32, nodes []string, protocol string, storageType string, drbdOptions map[string]string, volumes []VolumeSpec) error {
	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}
	if len(volumes) == 0 {
		return fmt.Errorf("at least one volume is required")
	}

	if storageType == "" {
		storageType = "lvm"
	}
	if protocol == "" {
		protocol = "C"
	}

	// Resolve every volume: auto-select+normalize its pool and derive a backing
	// volume name. Volume 0 keeps the historical "<name>_data" name (so existing
	// resources and callers are unaffected); extra volumes use "<name>_vol<K>".
	resolved := make([]resolvedVolume, len(volumes))
	for i, v := range volumes {
		if v.SizeGB == 0 {
			return fmt.Errorf("volume %d: size must be greater than 0 GB", i)
		}
		pool := v.Pool
		if pool == "" {
			// Auto-select the pool when none was given: with exactly one
			// registered pool name the choice is unambiguous; otherwise the
			// caller must decide.
			selected, err := rm.autoSelectPool(ctx)
			if err != nil {
				return err
			}
			pool = selected
		}
		volumeName := fmt.Sprintf("%s_data", name)
		if i > 0 {
			volumeName = fmt.Sprintf("%s_vol%d", name, i)
		}
		resolved[i] = resolvedVolume{
			id:         i,
			volumeName: volumeName,
			pool:       normalizeManagedName(pool),
			sizeGB:     v.SizeGB,
		}
	}

	rm.controller.logger.Info("Creating DRBD resource",
		zap.String("name", name),
		zap.Uint32("port", port),
		zap.Strings("nodes", nodes),
		zap.String("protocol", protocol),
		zap.Int("volumes", len(resolved)),
		zap.String("storage_type", storageType),
		zap.Any("options", drbdOptions))

	// Quorum tiebreaker: a 2-node resource under quorum=majority loses its
	// majority the moment either node fails (the survivor is only 1/2), so
	// DRBD suspends I/O. Add a third diskless node — it votes in quorum but
	// stores no data — when one is available, so the survivor keeps a 2/3
	// majority through any single-node failure. Mirrors LINSTOR's
	// auto-add-quorum-tiebreaker. If no spare node exists we proceed with a
	// bare 2-node resource but flag the quorum risk loudly.
	var disklessNodes []string
	if len(nodes) == 2 {
		if rm.controller.config != nil && rm.controller.config.Resource.AutoTiebreaker {
			if tb := rm.selectTiebreaker(ctx, nodes); tb != "" {
				disklessNodes = []string{tb}
				rm.controller.logger.Info("Adding diskless quorum tiebreaker to 2-node resource",
					zap.String("resource", name),
					zap.String("tiebreaker", tb))
			}
		}
		if len(disklessNodes) == 0 {
			rm.controller.logger.Warn("2-node resource has no quorum tiebreaker: a single node failure will suspend I/O (no quorum majority). Register a third node, or it stays a degraded 2-node resource.",
				zap.String("resource", name))
		}
	}

	// Convert diskful node names to IP addresses for deployment.
	nodeIPs := make([]string, len(nodes))
	for i, node := range nodes {
		ip := rm.controller.nodes.GetNodeAddressByName(node)
		if ip == "" {
			ip = node // fallback to node name
		}
		nodeIPs[i] = ip
	}

	// Diskless tiebreaker IPs, and the union of all participating node IPs.
	// Config, `drbdadm up` and teardown reach every node; LV creation and
	// create-md touch diskful nodes only.
	disklessIPs := make([]string, len(disklessNodes))
	for i, node := range disklessNodes {
		ip := rm.controller.nodes.GetNodeAddressByName(node)
		if ip == "" {
			ip = node
		}
		disklessIPs[i] = ip
	}
	allIPs := append(append([]string{}, nodeIPs...), disklessIPs...)

	if port == 0 {
		p, err := rm.nextGlobalPort(ctx, nodeIPs[0])
		if err != nil {
			return fmt.Errorf("allocate port: %w", err)
		}
		port = p
		rm.controller.logger.Info("auto-allocated DRBD port", zap.Uint32("port", port), zap.String("resource", name))
	}

	// Pre-flight: reject a port already bound by another DRBD resource on the
	// nodes (including ones SDS does not manage) with a clear message, rather
	// than letting `drbdadm create-md` fail later with an opaque error.
	if conflict, err := rm.findPortConflict(ctx, nodeIPs[0], port, name); err != nil {
		rm.controller.logger.Warn("port conflict pre-check failed; continuing",
			zap.Uint32("port", port), zap.Error(err))
	} else if conflict != "" {
		return fmt.Errorf("DRBD port %d is already in use by resource %q; choose a different port", port, conflict)
	}

	// Roll back partial state if a later step fails: a half-created resource
	// (e.g. LVs made but create-md failed) otherwise leaves orphaned backing
	// volumes and a stray .res that block a clean retry.
	committed := false
	defer func() {
		if committed {
			return
		}
		rm.controller.logger.Warn("Resource create failed; rolling back partial state",
			zap.String("name", name))
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, _ = rm.deployment.DRBDDown(cleanupCtx, allIPs, name)
		_, _ = rm.deployment.Exec(cleanupCtx, allIPs, fmt.Sprintf("sudo rm -f /etc/drbd.d/%s.res", name))
		// Backing volumes exist on diskful nodes only.
		for _, v := range resolved {
			if storageType == "zfs" || storageType == "zfs-thin" {
				_, _ = rm.deployment.ZFSDestroyDataset(cleanupCtx, nodeIPs, fmt.Sprintf("%s/%s", v.pool, v.volumeName))
			} else {
				_, _ = rm.deployment.LVRemove(cleanupCtx, nodeIPs, fmt.Sprintf("/dev/%s/%s", v.pool, v.volumeName))
			}
		}
	}()

	// 1. Create the backing storage for every volume on all diskful nodes.
	for _, v := range resolved {
		if err := rm.createBackingVolume(ctx, nodeIPs, nodes, storageType, v.pool, v.volumeName, v.sizeGB); err != nil {
			return err
		}
	}

	// 2. Generate DRBD config.
	// Allocate node-global device minors: minors are shared across every DRBD
	// resource on a node. nextGlobalMinor returns (max existing minor)+1, so a
	// run of len(resolved) consecutive minors from that base is collision-free.
	baseMinor, err := rm.nextGlobalMinor(ctx, nodeIPs[0])
	if err != nil {
		return fmt.Errorf("failed to allocate device minor: %w", err)
	}
	for i := range resolved {
		resolved[i].minor = baseMinor + i
	}
	drbdConfig := rm.generateDrbdConfig(name, port, resolved, nodes, disklessNodes, protocol, storageType, drbdOptions)

	// 3. Distribute config to all nodes (diskful + diskless tiebreaker)
	configResult, err := rm.deployment.DistributeConfig(ctx, allIPs, drbdConfig, fmt.Sprintf("/etc/drbd.d/%s.res", name))
	if err != nil {
		return fmt.Errorf("failed to distribute config: %w", err)
	}
	if !configResult.Success {
		return fmt.Errorf("config distribution failed on some hosts")
	}

	// 4. Create metadata on diskful nodes only. A diskless tiebreaker has no
	// backing disk, so `drbdadm create-md` does not apply to it.
	mdResult, err := rm.deployment.DRBDCreateMD(ctx, nodeIPs, name)
	if err != nil {
		return fmt.Errorf("failed to create metadata: %w", err)
	}
	if !mdResult.AllSuccess() {
		return fmt.Errorf("metadata creation failed on hosts: %v", mdResult.FailedHosts())
	}

	// 5. Bring up resource on all nodes. The diskless node comes up Diskless
	// and connects; it only participates in quorum.
	upResult, err := rm.deployment.DRBDUp(ctx, allIPs, name)
	if err != nil {
		return fmt.Errorf("failed to bring up resource: %w", err)
	}
	if !upResult.AllSuccess() {
		return fmt.Errorf("resource up failed on hosts: %v", upResult.FailedHosts())
	}

	// 6. Save to database
	if rm.controller.db != nil {
		dbRes := &database.Resource{
			Name:          name,
			Port:          int(port),
			Nodes:         strings.Join(nodes, ","),
			Protocol:      protocol,
			Replicas:      len(nodes),
			DisklessNodes: strings.Join(disklessNodes, ","),
		}
		if err := rm.controller.db.SaveResource(ctx, dbRes); err != nil {
			rm.controller.logger.Warn("Failed to save resource to database", zap.Error(err))
		}

		for _, v := range resolved {
			volumeRecord := &database.Volume{
				ResourceName: name,
				VolumeName:   v.volumeName,
				VolumeID:     v.id,
				Pool:         v.pool,
				SizeGB:       int(v.sizeGB),
				Device:       backingPathForVolume(v.pool, v.volumeName, storageType),
			}
			if err := rm.controller.db.SaveVolume(ctx, volumeRecord); err != nil {
				rm.controller.logger.Warn("Failed to save volume to database",
					zap.String("resource", name),
					zap.Int("volume", v.id),
					zap.Error(err))
			}
		}
	}

	rm.controller.logger.Info("DRBD resource created successfully",
		zap.String("name", name))

	committed = true
	return nil
}

// createBackingVolume creates one volume's backing storage (ZFS zvol, LVM thin
// LV or plain LVM LV per storageType) on every diskful node. nodeIPs and nodes
// are parallel (IP for the command, name for error messages).
func (rm *ResourceManager) createBackingVolume(ctx context.Context, nodeIPs, nodes []string, storageType, pool, volumeName string, sizeGB uint32) error {
	size := fmt.Sprintf("%dG", sizeGB)
	for i, nodeIP := range nodeIPs {
		var result *deployment.ExecResult
		var err error
		switch storageType {
		case "zfs", "zfs-thin":
			result, err = rm.deployment.ZFSCreateThinDataset(ctx, []string{nodeIP}, pool, volumeName, size)
		case "lvm-thin":
			// Convention: the thin pool is named "<pool>_thin".
			result, err = rm.deployment.LVCreateThinVolume(ctx, []string{nodeIP}, pool, pool+"_thin", volumeName, size)
		default:
			result, err = rm.deployment.LVCreate(ctx, []string{nodeIP}, pool, volumeName, size)
		}
		if err != nil {
			return fmt.Errorf("failed to create backing volume %s/%s on %s: %w", pool, volumeName, nodes[i], err)
		}
		if !result.AllSuccess() {
			for host, hres := range result.Hosts {
				if !hres.Success {
					return fmt.Errorf("backing volume %s/%s creation failed on %s: %s", pool, volumeName, host, hres.Output)
				}
			}
		}
	}
	return nil
}

// selectTiebreaker picks a registered node, not already part of the resource,
// to serve as a diskless quorum tiebreaker. Online nodes are preferred;
// selection is deterministic (lowest node name) so repeated creations are
// stable. Returns "" when no spare node is available — the caller then keeps
// the resource as a bare 2-node configuration.
func (rm *ResourceManager) selectTiebreaker(ctx context.Context, nodes []string) string {
	inUse := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		inUse[n] = true
	}

	all, err := rm.controller.nodes.ListNodes(ctx)
	if err != nil {
		rm.controller.logger.Warn("Failed to list nodes for tiebreaker selection", zap.Error(err))
		return ""
	}

	var online, offline []string
	for _, n := range all {
		if n == nil || inUse[n.Name] {
			continue
		}
		if n.State == NodeStateOnline {
			online = append(online, n.Name)
		} else {
			offline = append(offline, n.Name)
		}
	}

	candidates := online
	if len(candidates) == 0 {
		candidates = offline
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.Strings(candidates)
	return candidates[0]
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

// generateDrbdConfig generates a DRBD resource configuration file for one or
// more volumes (volume 0..N). Diskful nodes share the resource-level volume
// blocks; diskless tiebreaker nodes override each with `disk none`.
func (rm *ResourceManager) generateDrbdConfig(name string, port uint32, volumes []resolvedVolume, nodes, disklessNodes []string, protocol, storageType string, options map[string]string) string {
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

	// Gather disk options once; they are applied to every volume block.
	var diskOptKeys []string
	if diskOpts, ok := sections["disk"]; ok && len(diskOpts) > 0 {
		for k := range diskOpts {
			diskOptKeys = append(diskOptKeys, k)
		}
		sort.Strings(diskOptKeys)
	}

	// Generate a resource-level block for each volume (volume 0..N).
	for _, v := range volumes {
		config.WriteString(fmt.Sprintf("\n    volume %d {\n", v.id))
		config.WriteString(fmt.Sprintf("        device    minor %d;\n", v.minor))

		// Use the ZFS or LVM device path based on storage type.
		var diskPath string
		if storageType == "zfs" || storageType == "zfs-thin" {
			diskPath = fmt.Sprintf("/dev/zvol/%s/%s", v.pool, v.volumeName)
		} else {
			diskPath = fmt.Sprintf("/dev/%s/%s", v.pool, v.volumeName)
		}
		config.WriteString(fmt.Sprintf("        disk      %s;\n", diskPath))
		config.WriteString("        meta-disk internal;\n")

		if len(diskOptKeys) > 0 {
			diskOpts := sections["disk"]
			config.WriteString("        disk {\n")
			for _, k := range diskOptKeys {
				config.WriteString(fmt.Sprintf("            %s %s;\n", k, diskOpts[k]))
			}
			config.WriteString("        }\n")
		}

		config.WriteString("    }\n")
	}

	// Generate on sections for each node. Diskful nodes come first and share
	// the resource-level volume blocks above; diskless tiebreaker nodes follow
	// and override every volume with `disk none` so they join quorum without
	// storing data. node-id is the position in this combined ordering.
	allNodes := append(append([]string{}, nodes...), disklessNodes...)
	diskless := make(map[string]bool, len(disklessNodes))
	for _, n := range disklessNodes {
		diskless[n] = true
	}

	for i, node := range allNodes {
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

		config.WriteString(fmt.Sprintf("\n    on %s {\n", node))
		config.WriteString(fmt.Sprintf("        address   %s:%d;\n", ip, port))
		config.WriteString(fmt.Sprintf("        node-id   %d;\n", i))
		if diskless[node] {
			for _, v := range volumes {
				config.WriteString(fmt.Sprintf("        volume %d {\n", v.id))
				config.WriteString(fmt.Sprintf("            device    minor %d;\n", v.minor))
				config.WriteString("            disk      none;\n")
				config.WriteString("        }\n")
			}
		}
		config.WriteString("    }\n")
	}

	// Add connection-mesh for multi-node DRBD 9
	// DRBD 9 requires a full mesh of connections between all nodes
	if len(allNodes) > 2 {
		config.WriteString("\n    connection-mesh {\n")
		config.WriteString("        hosts")
		for _, node := range allNodes {
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

	var disklessNodes []string
	if dbRes.DisklessNodes != "" {
		disklessNodes = strings.Split(dbRes.DisklessNodes, ",")
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
		Name:          dbRes.Name,
		Port:          uint32(dbRes.Port),
		Protocol:      dbRes.Protocol,
		Nodes:         nodeAddresses,
		Role:          localRole, // Local node's role
		Volumes:       volumes,
		NodeStates:    nodeStates,
		DisklessNodes: disklessNodes,
		// Two diskful nodes with no tiebreaker means a single failure drops
		// below quorum majority and suspends I/O.
		QuorumRisk: len(nodeAddresses) == 2 && len(disklessNodes) == 0,
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

		// Volume metadata comes from the database: listing must not fan out
		// SSH status calls per resource, and the persisted records carry
		// everything the API exposes (id, size, pool, backing volume).
		var volumes []*ResourceVolumeInfo
		if dbVolumes, err := rm.controller.db.ListVolumes(ctx, dbRes.Name); err == nil {
			for _, volume := range dbVolumes {
				volumes = append(volumes, &ResourceVolumeInfo{
					VolumeID:      uint32(volume.VolumeID),
					Device:        fmt.Sprintf("/dev/drbd/by-res/%s/%d", dbRes.Name, volume.VolumeID),
					SizeGB:        uint64(max(volume.SizeGB, 0)),
					Pool:          volume.Pool,
					BackingVolume: volume.VolumeName,
				})
			}
		}

		resources = append(resources, &ResourceInfo{
			Name:       dbRes.Name,
			Port:       uint32(dbRes.Port),
			Protocol:   dbRes.Protocol,
			Nodes:      nodeAddresses,
			Role:       "Unknown", // Live role comes from GetResource/ResourceStatus
			Volumes:    volumes,
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

	// Auto-select the pool when none was given: with exactly one registered
	// pool name the choice is unambiguous; otherwise the caller must decide.
	// (A hardcoded fallback name here used to send lvcreate at a volume
	// group that doesn't exist.)
	if pool == "" {
		selected, err := rm.autoSelectPool(ctx)
		if err != nil {
			return err
		}
		pool = selected
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

	// Volume numbers are scoped to this resource's config.
	maxVolNum := -1
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
	}
	newVolNum := maxVolNum + 1

	// Device minors are GLOBAL on a node: scanning only this resource's
	// config hands out minors already claimed by other resources and
	// drbdadm rejects the whole config with "conflicting use of
	// device-minor". Collect minors across every resource file instead.
	// (The previous in-file scan was additionally broken — it required 4
	// fields on a 3-field line and always allocated minor 0.)
	newMinor, err := rm.nextGlobalMinor(ctx, hosts[0])
	if err != nil {
		return fmt.Errorf("failed to allocate device minor: %w", err)
	}

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

	// The new volume's backing device has no DRBD metadata yet; without
	// create-md the subsequent adjust attaches it Diskless.
	createMDCmd := fmt.Sprintf("sudo drbdadm create-md --force %s/%d", resource, newVolNum)
	mdResult, err := rm.deployment.Exec(ctx, hosts, createMDCmd)
	if err != nil {
		return fmt.Errorf("failed to create metadata for new volume: %w", err)
	}
	if !mdResult.AllSuccess() {
		return fmt.Errorf("metadata creation for new volume failed on hosts: %v", mdResult.FailedHosts())
	}

	adjustCmd := fmt.Sprintf("sudo drbdadm adjust %s", resource)
	adjustResult, err := rm.deployment.Exec(ctx, hosts, adjustCmd)
	if err != nil {
		return fmt.Errorf("failed to adjust resource after volume add: %w", err)
	}
	if !adjustResult.AllSuccess() {
		return fmt.Errorf("resource adjust failed on hosts: %v", adjustResult.FailedHosts())
	}

	// A brand-new volume is Inconsistent on every node with no UpToDate
	// peer to sync from, so DRBD refuses to open it ("Could not open")
	// until an initial sync source exists. The volume is empty, so skip
	// the pointless full sync the LINSTOR way: declare a new current UUID
	// with a cleared bitmap on one node, which marks all replicas UpToDate.
	skipSyncCmd := fmt.Sprintf("sudo drbdadm new-current-uuid --clear-bitmap %s/%d", resource, newVolNum)
	if err := rm.execAllSuccess(ctx, []string{hosts[0]}, skipSyncCmd,
		"failed to initialize new volume sync state"); err != nil {
		return err
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

// SetOptions updates DRBD options on an existing resource. It rewrites the
// shared .res config in place — preserving volumes and node sections — and runs
// `drbdadm adjust` so the changes take effect without recreating the resource.
// Keys use the "section/key" form (e.g. "net/max-buffers", "disk/on-io-error");
// a bare key defaults to the resource-level "options" section.
func (rm *ResourceManager) SetOptions(ctx context.Context, resource string, options map[string]string) error {
	if len(options) == 0 {
		return fmt.Errorf("no options provided")
	}
	if rm.deployment == nil {
		return fmt.Errorf("deployment client not set")
	}
	hosts, err := rm.resourceHosts(ctx, resource)
	if err != nil {
		return err
	}
	if len(hosts) == 0 {
		return fmt.Errorf("resource %q has no nodes", resource)
	}

	// Read the current config from one node; the mesh keeps them identical.
	result, err := rm.deployment.Exec(ctx, []string{hosts[0]}, fmt.Sprintf("cat /etc/drbd.d/%s.res", resource))
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}
	var current string
	var found bool
	for _, hr := range result.Hosts {
		current, found = hr.Output, hr.Success
		break
	}
	if !found || strings.TrimSpace(current) == "" {
		return fmt.Errorf("resource %q config not found on %s", resource, hosts[0])
	}

	updated, err := applyDrbdOptions(current, options)
	if err != nil {
		return fmt.Errorf("failed to apply options: %w", err)
	}

	if _, err := rm.deployment.DistributeConfig(ctx, hosts, updated, fmt.Sprintf("/etc/drbd.d/%s.res", resource)); err != nil {
		return fmt.Errorf("failed to distribute updated config: %w", err)
	}

	if err := rm.execAllSuccess(ctx, hosts, fmt.Sprintf("sudo drbdadm adjust %s", resource),
		"failed to apply DRBD options"); err != nil {
		return err
	}

	rm.controller.logger.Info("Updated DRBD options",
		zap.String("resource", resource),
		zap.Any("options", options))
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

	// If this resource has an HA config (a drbd-reactor promoter + VIP), tear it
	// down first — otherwise deleting the resource orphans the HA reactor config
	// and its DB record, which then lingers in the UI referencing a gone
	// resource.
	if rm.controller.db != nil {
		if ha, herr := rm.controller.db.GetHaConfig(ctx, name); herr == nil && ha != nil {
			if err := rm.RemoveHa(ctx, name); err != nil {
				rm.controller.logger.Warn("Failed to remove HA config during resource delete (continuing)",
					zap.String("resource", name), zap.Error(err))
			}
		}
	}

	hosts, err := rm.resourceHosts(ctx, name)
	if err != nil {
		return err
	}

	// Diskless quorum tiebreakers carry the config and a kernel resource but no
	// backing volume. They must be torn down too, or the config and minor/port
	// linger in the kernel as an orphan that blocks reusing them later.
	allHosts := append(append([]string(nil), hosts...), rm.disklessHosts(ctx, name)...)

	// Best-effort unmount of the resource's DRBD devices so a mounted resource
	// can be brought down — drbdadm down fails on a busy (mounted) device,
	// which would otherwise leave the mount and block teardown.
	_, _ = rm.deployment.Exec(ctx, allHosts,
		fmt.Sprintf("for d in /dev/drbd/by-res/%s/*; do sudo umount \"$d\" 2>/dev/null; done; true", name))

	// 1. Down resource on all nodes (diskful + diskless tiebreaker)
	downResult, err := rm.deployment.DRBDDown(ctx, allHosts, name)
	if err != nil {
		return fmt.Errorf("failed to bring down resource: %w", err)
	}

	if !downResult.AllSuccess() && !force {
		return fmt.Errorf("resource down failed on hosts: %v", downResult.FailedHosts())
	}

	// 2. Delete config file from all nodes
	err = rm.deployment.DeleteConfig(ctx, allHosts, fmt.Sprintf("/etc/drbd.d/%s.res", name))
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

// nextGlobalMinor returns the lowest unused DRBD device minor on host,
// derived from every resource config present: minors are a node-global
// namespace and drbdadm rejects configs that reuse one.
// findPortConflict returns the name of an existing DRBD resource on host that
// already binds the given TCP port, or "" if the port is free. The resource
// being created (selfName) is ignored so re-runs don't flag themselves.
func (rm *ResourceManager) findPortConflict(ctx context.Context, host string, port uint32, selfName string) (string, error) {
	cmd := fmt.Sprintf("grep -lE 'address[^;]*:%d;' /etc/drbd.d/*.res 2>/dev/null || true", port)
	result, err := rm.deployment.Exec(ctx, []string{host}, cmd)
	if err != nil {
		return "", err
	}
	for _, hr := range result.Hosts {
		for _, line := range strings.Split(hr.Output, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			base := strings.TrimSuffix(filepath.Base(line), ".res")
			if base != selfName {
				return base, nil
			}
		}
	}
	return "", nil
}

func (rm *ResourceManager) nextGlobalMinor(ctx context.Context, host string) (int, error) {
	// Minors are a node-global namespace. Scanning only .res files misses
	// minors still held by the kernel from a previously-removed resource: its
	// config is gone but the /dev/drbdN node lingers and the minor stays
	// "configured", so reusing it makes `drbdadm create-md` fail with
	// "Device 'N' is configured". Take the max over both the configs and the
	// live /dev/drbd* device nodes so a fresh minor never collides.
	result, err := rm.deployment.Exec(ctx, []string{host},
		"cat /etc/drbd.d/*.res 2>/dev/null; ls -1d /dev/drbd[0-9]* 2>/dev/null || true")
	if err != nil {
		return 0, err
	}
	maxMinor := -1
	for _, hr := range result.Hosts {
		for _, line := range strings.Split(hr.Output, "\n") {
			if minor, ok := parseDeviceMinor(line); ok && minor > maxMinor {
				maxMinor = minor
			}
			if minor, ok := parseDevNodeMinor(line); ok && minor > maxMinor {
				maxMinor = minor
			}
		}
	}
	return maxMinor + 1, nil
}

var portLineRe = regexp.MustCompile(`:(\d+);`)

// parsePortsFromResConfigs extracts DRBD ports from the `address ...:<port>;`
// lines of concatenated .res file contents.
func parsePortsFromResConfigs(text string) []uint32 {
	var ports []uint32
	for _, m := range portLineRe.FindAllStringSubmatch(text, -1) {
		if p, err := strconv.Atoi(m[1]); err == nil {
			ports = append(ports, uint32(p))
		}
	}
	return ports
}

// lowestFreePort returns the lowest port >= base not present in used.
func lowestFreePort(used []uint32, base uint32) uint32 {
	set := map[uint32]bool{}
	for _, p := range used {
		set[p] = true
	}
	for p := base; ; p++ {
		if !set[p] {
			return p
		}
	}
}

// nextGlobalPort scans existing .res files on host and returns the lowest free
// DRBD port at or above 7000.
func (rm *ResourceManager) nextGlobalPort(ctx context.Context, host string) (uint32, error) {
	result, err := rm.deployment.Exec(ctx, []string{host}, "cat /etc/drbd.d/*.res 2>/dev/null || true")
	if err != nil {
		return 0, err
	}
	text := ""
	for _, hr := range result.Hosts {
		text += hr.Output
	}
	return lowestFreePort(parsePortsFromResConfigs(text), 7000), nil
}

// parseDevNodeMinor extracts N from a DRBD device node path like
// "/dev/drbd1005", ignoring the symlink tree under /dev/drbd/.
func parseDevNodeMinor(line string) (int, bool) {
	trimmed := strings.TrimSpace(line)
	const prefix = "/dev/drbd"
	if !strings.HasPrefix(trimmed, prefix) {
		return 0, false
	}
	rest := strings.TrimPrefix(trimmed, prefix)
	if rest == "" || !isAllDigits(rest) {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return n, true
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseDeviceMinor extracts N from DRBD config lines like
// "device minor 7;" or "device /dev/drbd7 minor 7;" regardless of spacing.
func parseDeviceMinor(line string) (int, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "device") || !strings.Contains(trimmed, "minor") {
		return 0, false
	}
	fields := strings.Fields(trimmed)
	for i, f := range fields {
		if f == "minor" && i+1 < len(fields) {
			if minor, err := strconv.Atoi(strings.TrimSuffix(fields[i+1], ";")); err == nil {
				return minor, true
			}
		}
	}
	return 0, false
}

// autoSelectPool returns the single registered pool name, or an error when
// the choice would be ambiguous (zero or multiple distinct pool names).
func (rm *ResourceManager) autoSelectPool(ctx context.Context) (string, error) {
	if rm.controller.db == nil {
		return "", fmt.Errorf("no pool specified and database not available for auto-selection")
	}
	pools, err := rm.controller.db.ListPools(ctx)
	if err != nil {
		return "", fmt.Errorf("no pool specified and pool lookup failed: %w", err)
	}
	names := make(map[string]bool)
	for _, p := range pools {
		names[p.Name] = true
	}
	if len(names) == 1 {
		for name := range names {
			return name, nil
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no pool specified and no pools are registered; create one with 'pool create'")
	}
	choices := make([]string, 0, len(names))
	for name := range names {
		choices = append(choices, name)
	}
	return "", fmt.Errorf("no pool specified and multiple pools exist (%v); pass --pool", choices)
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
		// Two things block removal of the origin LV: (1) any LVM snapshot of it
		// (e.g. scheduled snapshots) must go first, or lvremove reports the
		// origin "is used by another device"; (2) a DRBD minor may still hold
		// it, and by this point the config may be gone so drbdadm cannot help —
		// drbdsetup operates on kernel state directly. Remove snapshots, then
		// the origin, releasing the minor on the retry.
		cmd = fmt.Sprintf("for s in $(sudo lvs --noheadings -o lv_name -S origin=%s %s 2>/dev/null); do sudo lvremove -f %s/$s; done; "+
			"sudo lvremove -f %s/%s || { sudo drbdsetup down %s 2>/dev/null; sudo lvremove -f %s/%s; }",
			volume.VolumeName, volume.Pool, volume.Pool,
			volume.Pool, volume.VolumeName, volume.ResourceName, volume.Pool, volume.VolumeName)
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

	adjustRes, err := rm.deployment.Exec(ctx, hosts, fmt.Sprintf("sudo drbdadm adjust %s", resource))
	if err != nil {
		return fmt.Errorf("failed to adjust resource after config update: %w", err)
	}
	if !adjustRes.AllSuccess() {
		return fmt.Errorf("drbdadm adjust failed on %v after removing volume %d", adjustRes.FailedHosts(), volumeID)
	}

	if strings.HasPrefix(target.DiskPath, "/dev/zvol/") {
		dataset := strings.TrimPrefix(target.DiskPath, "/dev/zvol/")
		zfsRes, err := rm.deployment.ZFSDestroyDataset(ctx, hosts, dataset)
		if err != nil {
			return fmt.Errorf("failed to delete ZFS backing volume: %w", err)
		}
		if !zfsRes.AllSuccess() {
			return fmt.Errorf("ZFS backing volume removal failed on %v", zfsRes.FailedHosts())
		}
	} else {
		// A failed lvremove on any node leaves an orphan and a lopsided DRBD
		// resource, so surface per-host failures instead of only transport
		// errors. Detaching the just-removed volume's minor releases the LV if
		// the kernel still holds it after the adjust.
		removeCmd := fmt.Sprintf("sudo lvremove -f %s || { sudo drbdsetup detach %s/%d 2>/dev/null; sudo lvremove -f %s; }",
			target.DiskPath, resource, volumeID, target.DiskPath)
		rmRes, err := rm.deployment.Exec(ctx, hosts, removeCmd)
		if err != nil {
			return fmt.Errorf("failed to delete LVM backing volume: %w", err)
		}
		if !rmRes.AllSuccess() {
			return fmt.Errorf("LVM backing volume removal failed on %v", rmRes.FailedHosts())
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
		zfsRes, err := rm.deployment.ZFSResizeVolume(ctx, hosts, volumePath, sizeArg)
		if err != nil {
			return fmt.Errorf("failed to resize ZFS backing volume: %w", err)
		}
		if !zfsRes.AllSuccess() {
			return fmt.Errorf("ZFS backing volume resize failed on %v", zfsRes.FailedHosts())
		}
	} else {
		resizeCmd := fmt.Sprintf("sudo lvresize -L %s -y %s", sizeArg, target.DiskPath)
		lvRes, err := rm.deployment.Exec(ctx, hosts, resizeCmd)
		if err != nil {
			return fmt.Errorf("failed to resize LVM backing volume: %w", err)
		}
		if !lvRes.AllSuccess() {
			return fmt.Errorf("LVM backing volume resize failed on %v", lvRes.FailedHosts())
		}
	}

	drbdRes, err := rm.deployment.Exec(ctx, []string{hosts[0]}, fmt.Sprintf("sudo drbdadm resize %s/%d", resource, volumeID))
	if err != nil {
		return fmt.Errorf("failed to resize DRBD volume: %w", err)
	}
	if !drbdRes.AllSuccess() {
		return fmt.Errorf("DRBD volume resize failed on %v", drbdRes.FailedHosts())
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
	// Resolve node to address; empty means the current Primary.
	address, err := rm.resolveNodeOrPrimary(ctx, resource, node)
	if err != nil {
		return fmt.Errorf("resolve target node: %w", err)
	}

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
	_, err = rm.deployment.Exec(ctx, []string{address}, mkdirCmd)
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
	// Resolve node to address; empty means the current Primary.
	address, err := rm.resolveNodeOrPrimary(ctx, resource, node)
	if err != nil {
		return fmt.Errorf("resolve target node: %w", err)
	}

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

	// A VIP is served by the service-ip@ systemd template, which depends on the
	// service-ip helper. Without it the promoter target fails to start ("Unit
	// service-ip@... not found") and the HA config is silently broken. Check up
	// front on every node — the same guard Self-HA already uses.
	if strings.TrimSpace(vip) != "" {
		if err := rm.execAllSuccess(ctx, hosts, "test -x /usr/local/bin/service-ip",
			"service-ip helper is not installed at /usr/local/bin/service-ip on all nodes (required for the HA VIP)"); err != nil {
			return "", err
		}
	}

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
	configContent := rm.generatePromoterConfig(resource, services, mountPoint, vip)

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
			// Only an ExitError carries Stderr; a missing binary yields an
			// *exec.Error, so guard the type assertion (it used to panic when
			// drbdsetup was absent, e.g. in unit tests).
			var stderr string
			if ee, ok := err.(*exec.ExitError); ok {
				stderr = string(ee.Stderr)
			}
			rm.controller.logger.Warn("Failed to check local DRBD status",
				zap.Error(err),
				zap.String("stderr", stderr))
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

	// 3b. Explicitly bring the VIP down. Removing the promoter config and
	// reloading drbd-reactor does NOT reliably stop the units reactor already
	// started, and the VIP's service-ip@ unit is Type=oneshot with
	// RemainAfterExit=yes, so without an explicit stop the floating IP lingers
	// on whichever node was Primary. Run it on every resource node (we do not
	// know which one held the VIP) after the config is gone so reactor cannot
	// restart it. Idempotent: stopping an inactive/absent template instance is
	// a no-op, and any failure is only a warning so teardown still completes.
	if inst := vipServiceIPInstance(haCfg.VIP); inst != "" {
		stopCmd := fmt.Sprintf("systemctl stop service-ip@%s.service", inst)
		result, err := rm.deployment.Exec(ctx, hosts, stopCmd)
		if err != nil {
			rm.controller.logger.Warn("Failed to stop VIP service-ip unit",
				zap.String("vip", haCfg.VIP), zap.Error(err))
		} else if result != nil && !result.AllSuccess() {
			rm.controller.logger.Warn("VIP service-ip unit may still be up on some nodes",
				zap.String("vip", haCfg.VIP), zap.Strings("failed_hosts", result.FailedHosts()))
		}
	}

	// 4. Remove from database
	if err := rm.controller.db.DeleteHaConfig(ctx, resource); err != nil {
		return fmt.Errorf("failed to delete HA config from database: %w", err)
	}

	return nil
}

// vipServiceIPInstance derives the systemd template instance name for a VIP's
// service-ip@ unit from its (CIDR) string, matching how the promoter config is
// generated so teardown can target the exact same unit:
//
//	"192.168.1.50/24" -> "192.168.1.50-24"   (service-ip@192.168.1.50-24.service)
//	"192.168.1.50"     -> "192.168.1.50-32"   (bare IP defaults to /32)
//
// It returns "" for an empty vip so callers can skip VIP handling. This is the
// single source of truth for the instance name; generatePromoterConfig,
// RemoveHa's VIP-down step and the self-HA disable script all go through it.
func vipServiceIPInstance(vip string) string {
	vip = strings.TrimSpace(vip)
	if vip == "" {
		return ""
	}
	inst := strings.ReplaceAll(vip, "/", "-")
	if !strings.Contains(inst, "-") {
		inst = inst + "-32"
	}
	return inst
}

// generatePromoterConfig generates drbd-reactor promoter TOML config
func (rm *ResourceManager) generatePromoterConfig(resource string, services []string, mountPoint, vip string) string {
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
	if inst := vipServiceIPInstance(vip); inst != "" {
		// Use service-ip systemd unit: service-ip@<IP>-<MASK>.service
		serviceIPUnit := fmt.Sprintf("\"service-ip@%s.service\"", inst)
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
// primaryAddress returns the address of the node currently Primary for the
// resource, from live DRBD status.
func (rm *ResourceManager) primaryAddress(ctx context.Context, resource string) (string, error) {
	info, err := rm.GetResource(ctx, resource)
	if err != nil {
		return "", err
	}
	for addr, st := range info.NodeStates {
		if st.Role == "Primary" {
			return addr, nil
		}
	}
	return "", fmt.Errorf("no Primary node for resource %q; specify a node explicitly", resource)
}

// resolveNodeOrPrimary resolves a node selection to a host address. An empty
// selection ("Auto (Primary)") resolves to the current Primary; if there is no
// Primary that is a hard error rather than a silent no-op on an empty host.
func (rm *ResourceManager) resolveNodeOrPrimary(ctx context.Context, resource, node string) (string, error) {
	if strings.TrimSpace(node) == "" {
		return rm.primaryAddress(ctx, resource)
	}
	addr := rm.controller.ResolveHost(node)
	if strings.TrimSpace(addr) == "" {
		return "", fmt.Errorf("unknown node %q", node)
	}
	return addr, nil
}

func (rm *ResourceManager) CreateFilesystemOnly(ctx context.Context, resource string, volumeID uint32, fsType string, node string) error {
	// Resolve node to address; empty means the current Primary.
	address, err := rm.resolveNodeOrPrimary(ctx, resource, node)
	if err != nil {
		return fmt.Errorf("resolve target node: %w", err)
	}

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
				matched := false
				for _, node := range nodeAddresses {
					if node == nodeAddresses[0] {
						continue // Skip local node
					}
					if parts[0] == node {
						currentNode = node
						matched = true
						if _, exists := nodeStates[currentNode]; !exists {
							nodeStates[currentNode] = &ResourceNodeState{Role: role}
						} else {
							nodeStates[currentNode].Role = role
						}
						break
					}
				}
				// A role-carrying line whose name is not a tracked node is
				// either the local resource line or a peer absent from
				// nodeAddresses (e.g. a diskless quorum tiebreaker). Reset
				// currentNode so its following peer-disk line is not
				// misattributed to the previously matched node.
				if !matched {
					currentNode = ""
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
