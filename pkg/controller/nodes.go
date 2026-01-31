package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
	pb "github.com/liliang-cn/drbd-agent/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/client"
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
	agents     map[string]*client.AgentClient
	mu         sync.RWMutex
	nodes      map[string]*NodeInfo
}

// NewNodeManager creates a new node manager
func NewNodeManager(ctrl *Controller) *NodeManager {
	return &NodeManager{
		controller: ctrl,
		agents:     make(map[string]*client.AgentClient),
		nodes:      make(map[string]*NodeInfo),
	}
}

// AddAgent adds an agent connection
func (nm *NodeManager) AddAgent(node string, agent *client.AgentClient) {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	nm.agents[node] = agent

	// Initialize node info if not exists
	if nm.nodes[node] == nil {
		nm.nodes[node] = &NodeInfo{
			Address: node,
			State:   NodeStateOnline,
		}
	}

	nm.nodes[node].State = NodeStateOnline
	nm.nodes[node].LastSeen = time.Now()
}

// RegisterNode registers a new node
func (nm *NodeManager) RegisterNode(ctx context.Context, address string) (*NodeInfo, error) {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	nm.controller.logger.Info("Registering node", zap.String("address", address))

	// Connect to node
	agent, err := client.NewAgentClient(address)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to node: %w", err)
	}

	// Check node health
	_, err = agent.HealthCheck(ctx, &pb.HealthCheckRequest{})
	if err != nil {
		agent.Close()
		return nil, fmt.Errorf("health check failed: %w", err)
	}

	// Create node info
	nodeInfo := &NodeInfo{
		Address:  address,
		Hostname: address, // In production, get via ExecCommand("hostname")
		State:    NodeStateOnline,
		LastSeen: time.Now(),
		Version:  "1.0.0", // In production, get from build info or agent version endpoint
		Capacity: make(map[string]interface{}),
	}

	nm.agents[address] = agent
	nm.nodes[address] = nodeInfo

	nm.controller.logger.Info("Node registered successfully",
		zap.String("address", address))

	return nodeInfo, nil
}

// UnregisterNode unregisters a node
func (nm *NodeManager) UnregisterNode(ctx context.Context, address string) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	nm.controller.logger.Info("Unregistering node", zap.String("address", address))

	// Close agent connection
	if agent := nm.agents[address]; agent != nil {
		agent.Close()
		delete(nm.agents, address)
	}

	// Mark node as offline
	if node := nm.nodes[address]; node != nil {
		node.State = NodeStateOffline
	}

	return nil
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
	agent := nm.agents[address]
	node := nm.nodes[address]
	nm.mu.RUnlock()

	if agent == nil {
		return nil, fmt.Errorf("node not found: %s", address)
	}

	// Get DRBD status
	statusResp, err := agent.Status(ctx, &pb.StatusRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to get status: %w", err)
	}

	// Get VG info
	vgsResp, err := agent.VGS(ctx, &pb.VGSRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to get VG info: %w", err)
	}

	status := map[string]interface{}{
		"address":    address,
		"state":      node.State,
		"last_seen":  node.LastSeen,
		"version":    node.Version,
		"drbd": map[string]interface{}{
			"resources": len(statusResp.Resources),
		},
		"storage": map[string]interface{}{
			"pools": len(vgsResp.Vgs),
		},
	}

	return status, nil
}

// CheckNodeHealth checks health of a specific node
func (nm *NodeManager) CheckNodeHealth(ctx context.Context, address string) error {
	nm.mu.RLock()
	agent := nm.agents[address]
	nm.mu.RUnlock()

	if agent == nil {
		return fmt.Errorf("node not found: %s", address)
	}

	_, err := agent.HealthCheck(ctx, &pb.HealthCheckRequest{})
	if err != nil {
		nm.mu.Lock()
		if node := nm.nodes[address]; node != nil {
			node.State = NodeStateOffline
		}
		nm.mu.Unlock()
		return fmt.Errorf("health check failed: %w", err)
	}

	nm.mu.Lock()
	if node := nm.nodes[address]; node != nil {
		node.State = NodeStateOnline
		node.LastSeen = time.Now()
	}
	nm.mu.Unlock()

	return nil
}

// GetAgent returns the agent client for a node
func (nm *NodeManager) GetAgent(address string) (*client.AgentClient, error) {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	agent := nm.agents[address]
	if agent == nil {
		return nil, fmt.Errorf("node not found: %s", address)
	}

	return agent, nil
}

// GetAllAgents returns all agent clients
func (nm *NodeManager) GetAllAgents() map[string]*client.AgentClient {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	agents := make(map[string]*client.AgentClient, len(nm.agents))
	for k, v := range nm.agents {
		agents[k] = v
	}

	return agents
}
