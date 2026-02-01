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
	NodeStateOnline  NodeState = "online"
	NodeStateOffline NodeState = "offline"
	NodeStateDegraded NodeState = "degraded"
)

// NodeInfo represents node information
type NodeInfo struct {
	Name       string                 `json:"name"`
	Address    string                 `json:"address"`
	Hostname   string                 `json:"hostname"`
	State      NodeState              `json:"state"`
	LastSeen   time.Time              `json:"last_seen"`
	Capacity   map[string]interface{} `json:"capacity"`
	Version    string                 `json:"version"`
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
	nm.mu.Lock()
	defer nm.mu.Unlock()

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
		Version:  "1.0.0", // TODO: detect version from node environment
		Capacity: make(map[string]interface{}),
	}

	// Save to in-memory cache
	nm.nodes[address] = nodeInfo

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
	nm.controller.hostsLock.Unlock()

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

	nm.controller.logger.Info("Unregistering node", zap.String("address", address))

	// Mark node as offline
	if node := nm.nodes[address]; node != nil {
		node.State = NodeStateOffline
	}

	// Delete from database
	if nm.controller.db != nil {
		if err := nm.controller.db.DeleteNode(ctx, address); err != nil {
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
		if node.Name == name {
			return addr
		}
	}
	return ""
}

// GetNode gets node information
func (nm *NodeManager) GetNode(ctx context.Context, address string) (*NodeInfo, error) {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	node := nm.nodes[address]
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
func (nm *NodeManager) HealthCheck(ctx context.Context, address string) (*NodeHealthInfo, error) {
	info := &NodeHealthInfo{
		AvailableAgents: make([]string, 0),
	}

	// Check DRBD installation
	drbdResult, err := nm.controller.deployment.Exec(ctx, []string{address}, "drbdadm --version 2>/dev/null || echo 'not found'")
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
	reactorResult, err := nm.controller.deployment.Exec(ctx, []string{address}, "drbd-reactor --version 2>/dev/null || echo 'not found'")
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
		serviceResult, err := nm.controller.deployment.Exec(ctx, []string{address}, "systemctl is-active drbd-reactor")
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
	agentsResult, err := nm.controller.deployment.Exec(ctx, []string{address}, "ls /usr/lib/ocf/resource.d/heartbeat/ 2>/dev/null || echo 'not found'")
	if err == nil && agentsResult.AllSuccess() {
		agents := make([]string, 0)
		for _, r := range agentsResult.Hosts {
			if r.Success && r.Output != "" {
				output := strings.TrimSpace(r.Output)
				if !strings.Contains(output, "not found") {
					info.ResourceAgentsInstalled = true
					// List common OCF agents
					lines := strings.Split(output, "\n")
					for _, line := range lines {
						agent := strings.TrimSpace(line)
						if agent != "" {
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
