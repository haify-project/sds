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

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/config"
	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/deployment"
	"github.com/liliang-cn/sds/pkg/gateway"
)

// Controller represents the SDS controller
type Controller struct {
	config     *config.Config
	logger     *zap.Logger
	db         *database.DB
	deployment *deployment.Client
	hosts      []string
	hostsMap   map[string]string // hostname -> address mapping
	hostsLock  sync.RWMutex
	server     *grpc.Server
	ctx        context.Context
	cancel     context.CancelFunc
	// Managers
	storage   *StorageManager
	resources *ResourceManager
	snapshots *SnapshotManager
	nodes     *NodeManager
	gateway   *gateway.Manager
}

// New creates a new controller
func New(cfg *config.Config, logger *zap.Logger) (*Controller, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// Open database
	db, err := database.Open(&database.Config{Path: cfg.Database.Path}, logger)
	if err != nil {
		logger.Warn("Failed to open database, continuing without persistence", zap.Error(err))
		db = nil
	}

	// Create deployment client
	deploymentClient, err := deployment.New(&deployment.Config{
		DispatchConfig: cfg.Dispatch.ConfigPath,
		Parallel:       cfg.Dispatch.Parallel,
		SSHUser:        cfg.Dispatch.SSHUser,
		SSHKeyPath:     cfg.Dispatch.SSHKeyPath,
	}, logger)
	if err != nil {
		cancel()
		if db != nil {
			db.Close()
		}
		return nil, fmt.Errorf("failed to create deployment client: %w", err)
	}

	ctrl := &Controller{
		config:     cfg,
		logger:     logger,
		db:         db,
		deployment: deploymentClient,
		hosts:      cfg.Dispatch.Hosts,
		hostsMap:   make(map[string]string),
		ctx:        ctx,
		cancel:     cancel,
	}

	// Initialize managers
	ctrl.storage = NewStorageManager(ctrl)
	ctrl.resources = NewResourceManager(ctrl)
	ctrl.snapshots = NewSnapshotManager(ctrl)
	ctrl.nodes = NewNodeManager(ctrl)

	// Initialize gateway with adapters
	gwResourceManager := NewGatewayResourceManager(ctrl.resources)
	gwDeploymentClient := NewGatewayDeploymentClient(deploymentClient)
	ctrl.gateway = gateway.New(gwResourceManager, gwDeploymentClient, logger, cfg.Dispatch.Hosts)

	// Initialize hosts mapping
	ctrl.initHostsMapping()

	// Load data from database
	if db != nil {
		if err := ctrl.loadFromDatabase(ctx); err != nil {
			logger.Warn("Failed to load data from database", zap.Error(err))
		}
	}

	return ctrl, nil
}

// initHostsMapping initializes hostname to address mapping
func (c *Controller) initHostsMapping() {
	// Get hostnames from all hosts
	if len(c.hosts) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, host := range c.hosts {
		result, err := c.deployment.Exec(ctx, []string{host}, "hostname")
		if err == nil && result.AllSuccess() {
			for h, r := range result.Hosts {
				if r.Success && r.Output != "" {
					hostname := r.Output
					c.hostsMap[hostname] = h
					c.logger.Debug("Host mapping",
						zap.String("hostname", hostname),
						zap.String("address", h))
				}
			}
		}
	}
}

// loadHostsFromDatabase loads hosts from registered nodes in database
func (c *Controller) loadHostsFromDatabase(ctx context.Context) error {
	nodes, err := c.nodes.ListNodes(ctx)
	if err != nil {
		return err
	}

	if len(nodes) == 0 {
		return fmt.Errorf("no nodes found in database")
	}

	var hosts []string
	for _, node := range nodes {
		hosts = append(hosts, node.Name)
	}

	c.hosts = hosts
	c.logger.Info("Loaded hosts from database", zap.Strings("hosts", hosts))
	return nil
}

// Start starts the controller
func (c *Controller) Start() error {
	c.logger.Info("Starting SDS controller")

	// Load hosts from registered nodes in database
	if c.db != nil {
		if err := c.loadHostsFromDatabase(context.Background()); err != nil {
			c.logger.Warn("Failed to load hosts from database, using config", zap.Error(err))
		}
	}

	// Fallback to config hosts if no nodes in database
	if len(c.hosts) == 0 {
		c.hosts = c.config.Dispatch.Hosts
	}

	// Initialize deployment client with hosts
	c.resources.SetDeployment(c.deployment)
	c.resources.SetHosts(c.hosts)

	// Start gRPC server
	if err := c.startGRPCServer(); err != nil {
		return fmt.Errorf("failed to start gRPC server: %w", err)
	}

	c.logger.Info("SDS controller started",
		zap.String("address", c.config.Server.ListenAddress),
		zap.Int("port", c.config.Server.Port),
		zap.Strings("hosts", c.hosts))

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

	c.logger.Info("SDS controller stopped")
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

	c.logger.Info("Registered SDS controller service")

	go func() {
		c.logger.Info("gRPC server listening", zap.String("address", addr))
		if err := c.server.Serve(lis); err != nil {
			c.logger.Error("gRPC server error", zap.Error(err))
		}
	}()

	return nil
}

// GetHosts returns the list of hosts
func (c *Controller) GetHosts() []string {
	return c.hosts
}

// GetDeployment returns the deployment client
func (c *Controller) GetDeployment() *deployment.Client {
	return c.deployment
}

// ResolveHost resolves a hostname to an address
func (c *Controller) ResolveHost(hostOrAddr string) string {
	c.hostsLock.RLock()
	defer c.hostsLock.RUnlock()

	// If it's already an address, return as is
	for _, addr := range c.hosts {
		if addr == hostOrAddr {
			return hostOrAddr
		}
	}

	// Try to resolve hostname to address
	if addr, ok := c.hostsMap[hostOrAddr]; ok {
		return addr
	}

	// Return as-is (might be a hostname that SSH can resolve)
	return hostOrAddr
}

// NormalizeHost converts an address to hostname if available
// Used for display purposes to avoid showing duplicates
func (c *Controller) NormalizeHost(addrOrHost string) string {
	c.hostsLock.RLock()
	defer c.hostsLock.RUnlock()

	// If it's already in hosts list, return as is (prefer hostnames over IPs)
	for _, host := range c.hosts {
		if host == addrOrHost {
			return host
		}
	}

	// Reverse lookup: check if this address maps to a hostname
	for hostname, addr := range c.hostsMap {
		if addr == addrOrHost {
			return hostname
		}
	}

	// Return as-is
	return addrOrHost
}

// ==================== Gateway Adapter ====================

// GatewayResourceManager adapts ResourceManager to gateway.ResourceManager interface
type GatewayResourceManager struct {
	rm *ResourceManager
}

// NewGatewayResourceManager creates a new gateway resource manager adapter
func NewGatewayResourceManager(rm *ResourceManager) gateway.ResourceManager {
	return &GatewayResourceManager{rm: rm}
}

func (a *GatewayResourceManager) GetResource(ctx context.Context, name string) (*gateway.ResourceInfo, error) {
	info, err := a.rm.GetResource(ctx, name)
	if err != nil {
		return nil, err
	}

	// Convert controller.ResourceInfo to gateway.ResourceInfo
	gwVolumes := make([]*gateway.ResourceVolumeInfo, len(info.Volumes))
	for i, v := range info.Volumes {
		gwVolumes[i] = &gateway.ResourceVolumeInfo{
			VolumeID: v.VolumeID,
			Device:   v.Device,
			SizeGB:   v.SizeGB,
		}
	}

	gwNodeStates := make(map[string]*gateway.ResourceNodeState)
	for k, v := range info.NodeStates {
		gwNodeStates[k] = &gateway.ResourceNodeState{
			Role:        v.Role,
			DiskState:   v.DiskState,
			Replication: v.Replication,
		}
	}

	return &gateway.ResourceInfo{
		Name:       info.Name,
		Port:       info.Port,
		Protocol:   info.Protocol,
		Nodes:      info.Nodes,
		Role:       info.Role,
		Volumes:    gwVolumes,
		NodeStates: gwNodeStates,
	}, nil
}

func (a *GatewayResourceManager) SetPrimary(ctx context.Context, resource, node string, force bool) error {
	return a.rm.SetPrimary(ctx, resource, node, force)
}

// GatewayDeploymentClient adapts deployment.Client to gateway.DeploymentClient interface
type GatewayDeploymentClient struct {
	dc *deployment.Client
}

// NewGatewayDeploymentClient creates a new gateway deployment client adapter
func NewGatewayDeploymentClient(dc *deployment.Client) gateway.DeploymentClient {
	return &GatewayDeploymentClient{dc: dc}
}

func (a *GatewayDeploymentClient) DistributeConfig(ctx context.Context, hosts []string, content, remotePath string) error {
	_, err := a.dc.DistributeConfig(ctx, hosts, content, remotePath)
	return err
}

func (a *GatewayDeploymentClient) Exec(ctx context.Context, hosts []string, cmd string) error {
	_, err := a.dc.Exec(ctx, hosts, cmd)
	return err
}

// ==================== DATABASE ====================

// loadFromDatabase loads nodes and gateways from database
func (c *Controller) loadFromDatabase(ctx context.Context) error {
	// Load nodes
	dbNodes, err := c.db.ListNodes(ctx)
	if err != nil {
		return fmt.Errorf("failed to load nodes: %w", err)
	}

	for _, dbNode := range dbNodes {
		c.nodes.mu.Lock()
		c.nodes.nodes[dbNode.Address] = &NodeInfo{
			Name:     dbNode.Name,
			Address:  dbNode.Address,
			Hostname: dbNode.Hostname,
			State:    NodeState(dbNode.State),
			LastSeen: dbNode.LastSeen,
			Version:  dbNode.Version,
			Capacity: make(map[string]interface{}),
		}
		c.nodes.mu.Unlock()

		// Build hostname -> IP address mapping for DRBD config
		if dbNode.Hostname != "" && dbNode.Address != "" {
			c.hostsLock.Lock()
			c.hostsMap[dbNode.Hostname] = dbNode.Address
			c.hostsLock.Unlock()
		}
		// Also map name -> IP if different from hostname
		if dbNode.Name != "" && dbNode.Name != dbNode.Hostname && dbNode.Address != "" {
			c.hostsLock.Lock()
			c.hostsMap[dbNode.Name] = dbNode.Address
			c.hostsLock.Unlock()
		}

		c.logger.Debug("Loaded node from database",
			zap.String("name", dbNode.Name),
			zap.String("address", dbNode.Address))
	}

	// Load gateways
	dbGateways, err := c.db.ListGateways(ctx)
	if err != nil {
		return fmt.Errorf("failed to load gateways: %w", err)
	}

	for _, dbGateway := range dbGateways {
		c.logger.Debug("Loaded gateway from database",
			zap.String("name", dbGateway.Name),
			zap.String("type", string(dbGateway.Type)),
			zap.String("resource", dbGateway.Resource))
	}

	c.logger.Info("Loaded data from database",
		zap.Int("nodes", len(dbNodes)),
		zap.Int("gateways", len(dbGateways)))

	return nil
}

// Close closes the controller and its resources
func (c *Controller) Close() error {
	c.logger.Info("Closing controller")

	// Stop gRPC server
	if c.server != nil {
		c.server.GracefulStop()
	}

	// Close database
	if c.db != nil {
		if err := c.db.Close(); err != nil {
			c.logger.Error("Failed to close database", zap.Error(err))
		}
	}

	// Cancel context
	c.cancel()

	return nil
}
