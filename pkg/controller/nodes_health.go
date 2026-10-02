package controller

import (
	"context"
	"fmt"
	"strings"
	"time"
)

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
		nm.markHealth(address, NodeStateOffline)
		return fmt.Errorf("health check failed: %w", err)
	}

	if !result.AllSuccess() {
		nm.markHealth(address, NodeStateOffline)
		// Why it failed is what an operator needs: an SSH host key that
		// changed, a refused connection and a timeout are fixed differently.
		return fmt.Errorf("health check failed: %s", result.FailureDetails())
	}

	nm.markHealth(address, NodeStateOnline)
	return nil
}

// markHealth records what a health check found. Maintenance is an operator
// decision, not a health reading, so a drained node keeps it either way —
// placement reads this state, and a check that flipped it back to online would
// hand a drained node new replicas. Unreachability still reaches the alert
// detector through CheckNodeHealth's error.
func (nm *NodeManager) markHealth(address string, state NodeState) {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	n := nm.nodes[address]
	if n == nil {
		return
	}
	if state == NodeStateOnline {
		n.LastSeen = time.Now()
	}
	if n.State != NodeStateMaintenance {
		n.State = state
	}
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
					version := parseVersion(output)
					// drbdadm reports DRBD_KERNEL_VERSION=0 when the drbd kernel
					// module is absent — the utils package alone installs fine.
					// Such a node cannot carry a resource at all, so calling it
					// "DRBD installed, version 0" hides the one thing that is
					// broken: health-check passes and `drbdadm up` then fails
					// with "Module drbd not found".
					if version == "" || version == "0" {
						break
					}
					info.DrbdInstalled = true
					info.DrbdVersion = version
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
