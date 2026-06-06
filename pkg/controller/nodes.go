package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/liliang-cn/sds/pkg/database"
	"go.uber.org/zap"
)

// NodeState represents the state of a node
type NodeState string

const (
	NodeStateOnline   NodeState = "online"
	NodeStateOffline  NodeState = "offline"
	NodeStateDegraded NodeState = "degraded"
)

// NodeInfo represents node information
type NodeInfo struct {
	Name     string                 `json:"name"`
	Address  string                 `json:"address"`
	Hostname string                 `json:"hostname"`
	State    NodeState              `json:"state"`
	LastSeen time.Time              `json:"last_seen"`
	Capacity map[string]interface{} `json:"capacity"`
	Version  string                 `json:"version"`
}

// NodeManager manages cluster nodes
type NodeManager struct {
	controller *Controller
	mu         sync.RWMutex
	nodes      map[string]*NodeInfo
}

// NewNodeManager creates a new node manager
func NewNodeManager(ctrl *Controller) *NodeManager {
	return &NodeManager{
		controller: ctrl,
		nodes:      make(map[string]*NodeInfo),
	}
}

// RegisterNode registers a new node
func (nm *NodeManager) RegisterNode(ctx context.Context, name, address string) (*NodeInfo, error) {
	nm.controller.logger.Info("Registering node", zap.String("name", name), zap.String("address", address))

	// Check node health by executing hostname command
	result, err := nm.controller.deployment.Exec(ctx, []string{address}, "hostname")
	if err != nil {
		return nil, fmt.Errorf("failed to connect to node: %w", err)
	}

	if !result.AllSuccess() {
		return nil, fmt.Errorf("health check failed for node: %s", address)
	}

	// Get hostname
	hostname := name // fallback to provided name
	for _, r := range result.Hosts {
		if r.Success && r.Output != "" {
			hostname = strings.TrimSpace(r.Output)
			break
		}
	}

	// Create node info
	nodeInfo := &NodeInfo{
		Name:     name,
		Address:  address,
		Hostname: hostname,
		State:    NodeStateOnline,
		LastSeen: time.Now(),
		Version:  nm.detectNodeVersion(ctx, address),
		Capacity: make(map[string]interface{}),
	}

	// Save to in-memory cache
	nm.mu.Lock()
	nm.nodes[address] = nodeInfo
	nm.mu.Unlock()

	// Update controller's hosts list if not already present
	nm.controller.hostsLock.Lock()
	found := false
	for _, h := range nm.controller.hosts {
		if h == address {
			found = true
			break
		}
	}
	if !found {
		nm.controller.hosts = append(nm.controller.hosts, address)
	}
	// Update hostsMap for resolution
	nm.controller.hostsMap[name] = address
	if hostname != "" {
		nm.controller.hostsMap[hostname] = address
	}
	nm.controller.gateway.SetHosts(nm.controller.hosts)
	nm.controller.hostsLock.Unlock()

	// Add hosts entry to all existing nodes for new node
	nm.addHostsEntry(ctx, address, hostname)

	// Save to database
	if nm.controller.db != nil {
		dbNode := &database.Node{
			Name:     nodeInfo.Name,
			Address:  nodeInfo.Address,
			Hostname: nodeInfo.Hostname,
			State:    string(nodeInfo.State),
			LastSeen: nodeInfo.LastSeen,
			Version:  nodeInfo.Version,
		}
		if err := nm.controller.db.SaveNode(ctx, dbNode); err != nil {
			nm.controller.logger.Error("Failed to save node to database", zap.Error(err))
		}
	}

	nm.controller.logger.Info("Node registered successfully",
		zap.String("name", name),
		zap.String("address", address),
		zap.String("hostname", hostname))

	return nodeInfo, nil
}

// UnregisterNode unregisters a node
func (nm *NodeManager) UnregisterNode(ctx context.Context, address string) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	resolved := nm.controller.ResolveHost(address)
	node := nm.nodes[resolved]
	if node == nil {
		return fmt.Errorf("node not found: %s", address)
	}

	nm.controller.logger.Info("Unregistering node",
		zap.String("address", address),
		zap.String("resolved", resolved),
		zap.String("name", node.Name))

	delete(nm.nodes, resolved)

	nm.controller.hostsLock.Lock()
	filteredHosts := make([]string, 0, len(nm.controller.hosts))
	for _, host := range nm.controller.hosts {
		if host != resolved {
			filteredHosts = append(filteredHosts, host)
		}
	}
	nm.controller.hosts = filteredHosts

	delete(nm.controller.hostsMap, resolved)
	if node.Name != "" {
		delete(nm.controller.hostsMap, node.Name)
	}
	if node.Hostname != "" {
		delete(nm.controller.hostsMap, node.Hostname)
	}
	nm.controller.gateway.SetHosts(nm.controller.hosts)
	nm.controller.hostsLock.Unlock()

	// Delete from database
	if nm.controller.db != nil {
		if err := nm.controller.db.DeleteNode(ctx, resolved); err != nil {
			nm.controller.logger.Error("Failed to delete node from database", zap.Error(err))
		}
	}

	return nil
}

// GetNodeAddressByName gets node address by node name
func (nm *NodeManager) GetNodeAddressByName(name string) string {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	for addr, node := range nm.nodes {
		if node.Name == name || node.Hostname == name || addr == name {
			return addr
		}
	}
	return ""
}

// GetNode gets node information
func (nm *NodeManager) GetNode(ctx context.Context, address string) (*NodeInfo, error) {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	resolved := address
	if node := nm.nodes[resolved]; node != nil {
		return node, nil
	}

	for addr, node := range nm.nodes {
		if node.Name == address || node.Hostname == address {
			resolved = addr
			break
		}
	}

	node := nm.nodes[resolved]
	if node == nil {
		return nil, fmt.Errorf("node not found: %s", address)
	}

	return node, nil
}

// ListNodes lists all nodes
func (nm *NodeManager) ListNodes(ctx context.Context) ([]*NodeInfo, error) {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	nodes := make([]*NodeInfo, 0, len(nm.nodes))
	for _, node := range nm.nodes {
		nodes = append(nodes, node)
	}

	return nodes, nil
}

// GetNodeStatus gets detailed node status
func (nm *NodeManager) GetNodeStatus(ctx context.Context, address string) (map[string]interface{}, error) {
	nm.mu.RLock()
	node := nm.nodes[address]
	nm.mu.RUnlock()

	if node == nil {
		return nil, fmt.Errorf("node not found: %s", address)
	}

	// Get DRBD status
	drbdResult, err := nm.controller.deployment.Exec(ctx, []string{address}, "sudo drbdadm status")
	if err != nil {
		return nil, fmt.Errorf("failed to get DRBD status: %w", err)
	}

	// Get VG info
	vgResult, err := nm.controller.deployment.Exec(ctx, []string{address}, "sudo vgs --noheadings -o vg_name")
	if err != nil {
		return nil, fmt.Errorf("failed to get VG info: %w", err)
	}

	resourceCount := 0
	if drbdResult.AllSuccess() {
		// Count resources by counting lines
		for _, r := range drbdResult.Hosts {
			if r.Success {
				lines := strings.Split(strings.TrimSpace(r.Output), "\n")
				resourceCount = len(lines)
			}
		}
	}

	poolCount := 0
	if vgResult.AllSuccess() {
		for _, r := range vgResult.Hosts {
			if r.Success {
				lines := strings.Split(strings.TrimSpace(r.Output), "\n")
				for _, line := range lines {
					if strings.TrimSpace(line) != "" {
						poolCount++
					}
				}
			}
		}
	}

	status := map[string]interface{}{
		"address":   address,
		"state":     node.State,
		"last_seen": node.LastSeen,
		"version":   node.Version,
		"drbd": map[string]interface{}{
			"resources": resourceCount,
		},
		"storage": map[string]interface{}{
			"pools": poolCount,
		},
	}

	return status, nil
}

// CheckNodeHealth checks health of a specific node
func (nm *NodeManager) CheckNodeHealth(ctx context.Context, address string) error {
	nm.mu.RLock()
	node := nm.nodes[address]
	nm.mu.RUnlock()

	if node == nil {
		return fmt.Errorf("node not found: %s", address)
	}

	result, err := nm.controller.deployment.Exec(ctx, []string{address}, "echo ok")
	if err != nil {
		nm.mu.Lock()
		if n := nm.nodes[address]; n != nil {
			n.State = NodeStateOffline
		}
		nm.mu.Unlock()
		return fmt.Errorf("health check failed: %w", err)
	}

	if !result.AllSuccess() {
		nm.mu.Lock()
		if n := nm.nodes[address]; n != nil {
			n.State = NodeStateOffline
		}
		nm.mu.Unlock()
		return fmt.Errorf("health check failed")
	}

	nm.mu.Lock()
	if n := nm.nodes[address]; n != nil {
		n.State = NodeStateOnline
		n.LastSeen = time.Now()
	}
	nm.mu.Unlock()

	return nil
}

// NodeHealthInfo represents the health status of a node
type NodeHealthInfo struct {
	DrbdInstalled           bool     `json:"drbd_installed"`
	DrbdVersion             string   `json:"drbd_version"`
	DrbdReactorInstalled    bool     `json:"drbd_reactor_installed"`
	DrbdReactorVersion      string   `json:"drbd_reactor_version"`
	DrbdReactorRunning      bool     `json:"drbd_reactor_running"`
	ResourceAgentsInstalled bool     `json:"resource_agents_installed"`
	AvailableAgents         []string `json:"available_agents"`
}

// HealthCheck performs a comprehensive health check on a node
func (nm *NodeManager) HealthCheck(ctx context.Context, nodeName string) (*NodeHealthInfo, error) {
	info := &NodeHealthInfo{
		AvailableAgents: make([]string, 0),
	}

	// Resolve the most reliable SSH target we have: address first, then hostname/name.
	sshTarget := nm.controller.ResolveHost(nodeName)
	nm.mu.RLock()
	for _, node := range nm.nodes {
		if node.Name == nodeName || node.Hostname == nodeName || node.Address == nodeName {
			if node.Address != "" {
				sshTarget = node.Address
			} else if node.Hostname != "" {
				sshTarget = node.Hostname
			} else {
				sshTarget = node.Name
			}
			break
		}
	}
	nm.mu.RUnlock()

	if sshTarget == "" {
		// Fallback to node name/address if not found
		sshTarget = nodeName
	}

	// Check DRBD installation
	drbdResult, err := nm.controller.deployment.Exec(ctx, []string{sshTarget}, "drbdadm --version 2>/dev/null || echo 'not found'")
	if err == nil && drbdResult.AllSuccess() {
		for _, r := range drbdResult.Hosts {
			if r.Success && r.Output != "" {
				output := strings.TrimSpace(r.Output)
				if !strings.Contains(output, "not found") && !strings.Contains(output, "command not found") {
					info.DrbdInstalled = true
					info.DrbdVersion = parseVersion(output)
					break
				}
			}
		}
	}

	// Check drbd-reactor installation
	reactorResult, err := nm.controller.deployment.Exec(ctx, []string{sshTarget}, "drbd-reactor --version 2>/dev/null || echo 'not found'")
	if err == nil && reactorResult.AllSuccess() {
		for _, r := range reactorResult.Hosts {
			if r.Success && r.Output != "" {
				output := strings.TrimSpace(r.Output)
				if !strings.Contains(output, "not found") && !strings.Contains(output, "command not found") {
					info.DrbdReactorInstalled = true
					info.DrbdReactorVersion = parseVersion(output)
					break
				}
			}
		}
	}

	// Check drbd-reactor service status
	if info.DrbdReactorInstalled {
		serviceResult, err := nm.controller.deployment.Exec(ctx, []string{sshTarget}, "systemctl is-active drbd-reactor")
		if err == nil && serviceResult.AllSuccess() {
			for _, r := range serviceResult.Hosts {
				if r.Success && strings.TrimSpace(r.Output) == "active" {
					info.DrbdReactorRunning = true
					break
				}
			}
		}
	}

	// Check resource-agents-extra (OCF agents)
	// Recursively find all executable OCF resource agents
	agentsResult, err := nm.controller.deployment.Exec(ctx, []string{sshTarget}, "find /usr/lib/ocf/resource.d -type f -executable -exec basename {} \\; 2>/dev/null | sort -u")
	if err == nil && agentsResult.AllSuccess() {
		agents := make([]string, 0)
		seen := make(map[string]bool)
		for _, r := range agentsResult.Hosts {
			if r.Success && r.Output != "" {
				output := strings.TrimSpace(r.Output)
				if !strings.Contains(output, "not found") && output != "" {
					info.ResourceAgentsInstalled = true
					lines := strings.Split(output, "\n")
					for _, line := range lines {
						agent := strings.TrimSpace(line)
						// Filter out helper scripts and common non-agents
						if agent != "" && !seen[agent] && !strings.HasSuffix(agent, ".sh") && !strings.HasPrefix(agent, ".") {
							seen[agent] = true
							agents = append(agents, agent)
						}
					}
					break
				}
			}
		}
		info.AvailableAgents = agents
	}

	return info, nil
}

func (nm *NodeManager) detectNodeVersion(ctx context.Context, address string) string {
	result, err := nm.controller.deployment.Exec(ctx, []string{address}, "cat /etc/os-release 2>/dev/null || uname -r")
	if err != nil || !result.AllSuccess() {
		return "unknown"
	}

	for _, r := range result.Hosts {
		if !r.Success || strings.TrimSpace(r.Output) == "" {
			continue
		}
		return parseNodeEnvironmentVersion(r.Output)
	}

	return "unknown"
}

// parseVersion extracts version string from command output
func parseVersion(output string) string {
	// Look for version patterns like "v1.2.3", "1.2.3", "DRBDADM_VERSION=9.33.0"
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Try DRBDADM_VERSION= format first
		if strings.Contains(line, "DRBDADM_VERSION=") {
			parts := strings.Split(line, "=")
			if len(parts) >= 2 {
				return strings.TrimSpace(parts[1])
			}
		}
		if strings.Contains(line, "DRBD_KERNEL_VERSION=") {
			parts := strings.Split(line, "=")
			if len(parts) >= 2 {
				return strings.TrimSpace(parts[1])
			}
		}
		// Generic pattern: v1.2.3, 1.2.3, DRBD 9.2.3
		if strings.HasPrefix(line, "v") || strings.Contains(line, ".") {
			parts := strings.Fields(line)
			for _, p := range parts {
				p = strings.TrimSuffix(p, "\\")
				if strings.HasPrefix(p, "v") || (strings.Count(p, ".") >= 1 && !strings.Contains(p, "GIT-hash")) {
					return strings.TrimPrefix(p, "v")
				}
			}
		}
	}
	return "unknown"
}

func parseNodeEnvironmentVersion(output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return "unknown"
	}

	if strings.Contains(trimmed, "=") {
		osRelease := make(map[string]string)
		for _, line := range strings.Split(trimmed, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			key := strings.TrimSpace(parts[0])
			value := strings.Trim(strings.TrimSpace(parts[1]), `"`)
			osRelease[key] = value
		}

		switch {
		case osRelease["PRETTY_NAME"] != "":
			return osRelease["PRETTY_NAME"]
		case osRelease["NAME"] != "" && osRelease["VERSION"] != "":
			return strings.TrimSpace(osRelease["NAME"] + " " + osRelease["VERSION"])
		case osRelease["NAME"] != "" && osRelease["VERSION_ID"] != "":
			return strings.TrimSpace(osRelease["NAME"] + " " + osRelease["VERSION_ID"])
		}
	}

	if version := parseVersion(trimmed); version != "unknown" {
		return version
	}

	return trimmed
}

// addHostsEntry adds the new node's hostname and IP to /etc/hosts on all existing nodes
func (nm *NodeManager) addHostsEntry(ctx context.Context, ip, hostname string) {
	if hostname == "" || hostname == ip {
		return
	}

	// Prepare hosts entry
	hostsEntry := fmt.Sprintf("%s\t%s", ip, hostname)

	// Get list of all existing nodes (excluding the new one)
	nm.mu.RLock()
	var existingNodes []string
	for _, node := range nm.nodes {
		if node.Hostname != hostname && node.Hostname != "" {
			existingNodes = append(existingNodes, node.Hostname)
		}
	}
	nm.mu.RUnlock()

	if len(existingNodes) == 0 {
		return
	}

	// Add to each existing node's /etc/hosts
	hostsCmd := fmt.Sprintf(
		"grep -q '%s' /etc/hosts || echo '%s' | sudo tee -a /etc/hosts > /dev/null",
		hostsEntry, hostsEntry,
	)

	result, err := nm.controller.deployment.Exec(ctx, existingNodes, hostsCmd)
	if err != nil {
		nm.controller.logger.Warn("Failed to add hosts entry", zap.Error(err))
		return
	}

	for nodeHost, r := range result.Hosts {
		if !r.Success {
			nm.controller.logger.Warn("Failed to add hosts entry",
				zap.String("node", nodeHost),
				zap.String("output", r.Output))
		}
	}
}
