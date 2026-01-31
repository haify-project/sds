// Package controller provides the SDS controller
package controller

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"go.uber.org/zap"

	pb "github.com/liliang-cn/drbd-agent/api/proto/v1"
	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/client"
	"github.com/liliang-cn/sds/pkg/config"
)

// Controller represents the SDS controller
type Controller struct {
	config     *config.Config
	logger     *zap.Logger
	agents     map[string]*client.AgentClient
	agentsLock sync.RWMutex
	server     *grpc.Server
	ctx        context.Context
	cancel     context.CancelFunc
	// Managers
	storage   *StorageManager
	resources *ResourceManager
	snapshots *SnapshotManager
	gateways  *GatewayManager
	nodes     *NodeManager
}

// New creates a new controller
func New(cfg *config.Config, logger *zap.Logger) *Controller {
	ctx, cancel := context.WithCancel(context.Background())

	ctrl := &Controller{
		config:     cfg,
		logger:     logger,
		agents:     make(map[string]*client.AgentClient),
		ctx:        ctx,
		cancel:     cancel,
	}

	// Initialize managers
	ctrl.storage = NewStorageManager(ctrl)
	ctrl.resources = NewResourceManager(ctrl)
	ctrl.snapshots = NewSnapshotManager(ctrl)
	ctrl.gateways = NewGatewayManager(ctrl)
	ctrl.nodes = NewNodeManager(ctrl)

	return ctrl
}

// Start starts the controller
func (c *Controller) Start() error {
	c.logger.Info("Starting SDS controller")

	// Connect to drbd-agent endpoints
	if err := c.connectToAgents(); err != nil {
		return fmt.Errorf("failed to connect to agents: %w", err)
	}

	// Start gRPC server
	if err := c.startGRPCServer(); err != nil {
		return fmt.Errorf("failed to start gRPC server: %w", err)
	}

	// Start health monitoring
	go c.monitorAgents()

	c.logger.Info("SDS controller started",
		zap.String("address", c.config.Server.ListenAddress),
		zap.Int("port", c.config.Server.Port))

	return nil
}

// Stop stops the controller
func (c *Controller) Stop() {
	c.logger.Info("Stopping SDS controller")

	c.cancel()

	// Stop gRPC server
	if c.server != nil {
		c.server.GracefulStop()
	}

	// Close agent connections
	c.agentsLock.Lock()
	for addr, agent := range c.agents {
		c.logger.Debug("Closing agent connection", zap.String("address", addr))
		agent.Close()
	}
	c.agents = make(map[string]*client.AgentClient)
	c.agentsLock.Unlock()

	c.logger.Info("SDS controller stopped")
}

// connectToAgents connects to all drbd-agent endpoints
func (c *Controller) connectToAgents() error {
	c.logger.Info("Connecting to drbd-agent endpoints")

	for _, endpoint := range c.config.DrbdAgent.Endpoints {
		client, err := client.NewAgentClient(endpoint)
		if err != nil {
			c.logger.Error("Failed to connect to drbd-agent",
				zap.String("endpoint", endpoint),
				zap.Error(err))
			continue
		}

		c.agentsLock.Lock()
		c.agents[endpoint] = client
		c.agentsLock.Unlock()

		c.logger.Info("Connected to drbd-agent", zap.String("endpoint", endpoint))
	}

	if len(c.agents) == 0 {
		return fmt.Errorf("failed to connect to any drbd-agent")
	}

	return nil
}

// startGRPCServer starts the gRPC server
func (c *Controller) startGRPCServer() error {
	addr := fmt.Sprintf("%s:%d", c.config.Server.ListenAddress, c.config.Server.Port)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	c.server = grpc.NewServer()

	// Register health service
	healthServer := health.NewServer()
	grpc_health_v1.RegisterHealthServer(c.server, healthServer)
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	// Register SDS controller service
	sdsServer := NewServer(c)
	sdspb.RegisterSDSControllerServer(c.server, sdsServer)

	// Register all connected agents with managers
	c.agentsLock.Lock()
	for endpoint, agent := range c.agents {
		sdsServer.RegisterAgents(endpoint, agent)
	}
	c.agentsLock.Unlock()

	c.logger.Info("Registered SDS controller service")

	go func() {
		c.logger.Info("gRPC server listening", zap.String("address", addr))
		if err := c.server.Serve(lis); err != nil {
			c.logger.Error("gRPC server error", zap.Error(err))
		}
	}()

	return nil
}

// monitorAgents monitors agent health
func (c *Controller) monitorAgents() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.checkAgentHealth()
		}
	}
}

// checkAgentHealth checks health of all agents
func (c *Controller) checkAgentHealth() {
	c.agentsLock.RLock()
	endpoints := make([]string, 0, len(c.agents))
	for endpoint := range c.agents {
		endpoints = append(endpoints, endpoint)
	}
	c.agentsLock.RUnlock()

	for _, endpoint := range endpoints {
		ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
		_, err := c.agents[endpoint].HealthCheck(ctx, &pb.HealthCheckRequest{})
		cancel()

		if err != nil {
			c.logger.Warn("Agent health check failed",
				zap.String("endpoint", endpoint),
				zap.Error(err))

			// Try to reconnect
			c.reconnectAgent(endpoint)
		} else {
			c.logger.Debug("Agent healthy", zap.String("endpoint", endpoint))
		}
	}
}

// reconnectAgent reconnects to an agent
func (c *Controller) reconnectAgent(endpoint string) {
	c.logger.Info("Reconnecting to agent", zap.String("endpoint", endpoint))

	client, err := client.NewAgentClient(endpoint)
	if err != nil {
		c.logger.Error("Failed to reconnect to agent",
			zap.String("endpoint", endpoint),
			zap.Error(err))
		return
	}

	c.agentsLock.Lock()
	oldClient := c.agents[endpoint]
	if oldClient != nil {
		oldClient.Close()
	}
	c.agents[endpoint] = client
	c.agentsLock.Unlock()

	c.logger.Info("Reconnected to agent", zap.String("endpoint", endpoint))
}
