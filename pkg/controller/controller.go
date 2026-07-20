// Package controller provides the SDS controller
package controller

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"go.uber.org/zap"
	"github.com/liliang-cn/sds/pkg/alert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/config"
	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/deployment"
	"github.com/liliang-cn/sds/pkg/gateway"
	"github.com/liliang-cn/sds/pkg/metrics"
	"github.com/liliang-cn/sds/pkg/rbac"
	"github.com/liliang-cn/sds/pkg/wanproxy"
)

// Controller represents the SDS controller
type Controller struct {
	config     *config.Config
	logger     *zap.Logger
	db         *database.DB
	deployment deploymentClient
	hosts      []string
	hostsMap   map[string]string // hostname -> address mapping
	hostsLock  sync.RWMutex
	server     *grpc.Server
	ctx        context.Context
	cancel     context.CancelFunc
	// Metrics
	metrics       *metrics.Metrics
	metricsServer *http.Server
	// UI
	uiServer *UIServer
	alertMonitor *alert.Monitor
	// Managers
	storage   *StorageManager
	resources *ResourceManager
	snapshots *SnapshotManager
	nodes     *NodeManager
	gateway   *gateway.Manager
	schedules *ScheduleManager
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
	deploymentClient, err := deployment.New(logger)
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
		hosts:      []string{},
		hostsMap:   make(map[string]string),
		ctx:        ctx,
		cancel:     cancel,
	}

	// Initialize managers
	ctrl.storage = NewStorageManager(ctrl)
	ctrl.resources = NewResourceManager(ctrl)
	ctrl.snapshots = NewSnapshotManager(ctrl)
	ctrl.nodes = NewNodeManager(ctrl)
	ctrl.schedules = NewScheduleManager(ctrl)

	// Initialize gateway with adapters
	gwResourceManager := NewGatewayResourceManager(ctrl.resources,
		cfg.Gateway.AutoStateVolume, cfg.Gateway.StateVolumeSizeGB)
	gwDeploymentClient := NewGatewayDeploymentClient(deploymentClient)
	ctrl.gateway = gateway.New(gwResourceManager, gwDeploymentClient, logger, []string{})

	// Initialize metrics
	if cfg.Metrics.Enabled {
		metricsInstance, err := metrics.New(logger)
		if err != nil {
			cancel()
			if db != nil {
				db.Close()
			}
			return nil, fmt.Errorf("failed to initialize metrics: %w", err)
		}
		ctrl.metrics = metricsInstance
	}

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
		hosts = append(hosts, node.Address)
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
			c.logger.Warn("Failed to load hosts from database", zap.Error(err))
		}
	}

	// Initialize deployment client with hosts
	c.resources.SetDeployment(c.deployment)
	c.resources.SetHosts(c.hosts)
	c.gateway.SetHosts(c.hosts)

	// Start metrics server if enabled
	if c.config.Metrics.Enabled && c.metrics != nil {
		if err := c.startMetricsServer(); err != nil {
			return fmt.Errorf("failed to start metrics server: %w", err)
		}
	}

	// Start Alert Monitor if enabled
	if c.config.Alert.Enabled && c.config.Alert.WebhookURL != "" {
		interval := time.Duration(c.config.Alert.CheckIntervalSec) * time.Second
		c.alertMonitor = alert.NewMonitor(c.config.Alert.WebhookURL, interval, c.resources, c.logger)
		c.alertMonitor.Start(c.ctx)
		c.logger.Info("Alert monitor started", zap.String("webhook", c.config.Alert.WebhookURL))
	}

	// Start gRPC server
	if err := c.startGRPCServer(); err != nil {
		return fmt.Errorf("failed to start gRPC server: %w", err)
	}

	// Start UI server
	uiServer, err := NewUIServer(c.logger, c.config.Server.ListenAddress, 3376)
	if err != nil {
		return fmt.Errorf("failed to create UI server: %w", err)
	}
	c.uiServer = uiServer
	if err := c.uiServer.Start(); err != nil {
		return fmt.Errorf("failed to start UI server: %w", err)
	}

	// Start the snapshot scheduler. Only the active controller reaches here
	// (self-HA promotes a single node), so no distributed coordination is
	// needed. Schedules persist in the DB and reload on the next active node
	// after failover.
	if c.config.Schedule.Enabled && c.db != nil {
		if err := c.schedules.Start(context.Background()); err != nil {
			c.logger.Warn("Failed to start snapshot scheduler", zap.Error(err))
		}
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

	// Stop the snapshot scheduler
	if c.schedules != nil {
		c.schedules.Stop()
	}

	// Stop metrics server
	if c.metricsServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.metricsServer.Shutdown(ctx); err != nil {
			c.logger.Error("Failed to shutdown metrics server", zap.Error(err))
		}
	}

	// Stop gRPC server
	if c.server != nil {
		c.server.GracefulStop()
	}

	// Stop UI server
	if c.uiServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.uiServer.Shutdown(ctx); err != nil {
			c.logger.Error("Failed to shutdown UI server", zap.Error(err))
		}
	}

	c.logger.Info("SDS controller stopped")
}

// startGRPCServer starts the gRPC server with gRPC-Gateway on separate ports
func (c *Controller) startGRPCServer() error {
	// Start Alert Monitor if enabled
	if c.config.Alert.Enabled && c.config.Alert.WebhookURL != "" {
		interval := time.Duration(c.config.Alert.CheckIntervalSec) * time.Second
		c.alertMonitor = alert.NewMonitor(c.config.Alert.WebhookURL, interval, c.resources, c.logger)
		c.alertMonitor.Start(c.ctx)
		c.logger.Info("Alert monitor started", zap.String("webhook", c.config.Alert.WebhookURL))
	}

	// Start gRPC server on the configured port
	grpcAddr := fmt.Sprintf("%s:%d", c.config.Server.ListenAddress, c.config.Server.Port)
	grpcLis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		return fmt.Errorf("failed to listen for gRPC: %w", err)
	}

	// Create gRPC server. Interceptor order matters: metrics first so even
	// rejected requests are counted, then audit (so denied attempts are still
	// recorded), then authentication.
	var unaryInterceptors []grpc.UnaryServerInterceptor
	var streamInterceptors []grpc.StreamServerInterceptor
	if c.metrics != nil {
		unaryInterceptors = append(unaryInterceptors, c.metrics.UnaryServerInterceptor())
	}

	// Build the RBAC engine first so the audit interceptor — which runs ahead
	// of the identity check — can still attribute each call to a user.
	var rbacEngine *rbac.Engine
	var auditUser userResolver
	if c.config.RBAC.Enabled {
		var err error
		rbacEngine, err = rbac.New(dbRBACStore{c.db}, toRBACUsers(c.config.RBAC.Users), toRBACPolicies(c.config.RBAC.Policies))
		if err != nil {
			return fmt.Errorf("failed to initialize RBAC: %w", err)
		}
		auditUser = func(ctx context.Context) string {
			name, _ := rbacEngine.ResolveUser(bearerToken(ctx))
			return name
		}
	}

	if c.config.Audit.Enabled {
		auditLog := c.logger.Named("audit")
		unaryInterceptors = append(unaryInterceptors,
			auditUnaryInterceptor(auditLog, c.config.Audit.IncludeReads, auditUser))
		streamInterceptors = append(streamInterceptors,
			auditStreamInterceptor(auditLog, c.config.Audit.IncludeReads, auditUser))
		c.logger.Info("API audit log enabled",
			zap.Bool("include_reads", c.config.Audit.IncludeReads))
	}

	switch {
	case c.config.RBAC.Enabled:
		unaryInterceptors = append(unaryInterceptors,
			rbacIdentityUnaryInterceptor(rbacEngine), rbacAuthzUnaryInterceptor(rbacEngine))
		streamInterceptors = append(streamInterceptors,
			rbacIdentityStreamInterceptor(rbacEngine), rbacAuthzStreamInterceptor(rbacEngine))
		c.logger.Info("API authorization enabled (RBAC)",
			zap.Int("users", len(c.config.RBAC.Users)))
	case c.config.Auth.Enabled:
		unaryInterceptors = append(unaryInterceptors, authUnaryInterceptor(c.config.Auth.Token))
		streamInterceptors = append(streamInterceptors, authStreamInterceptor(c.config.Auth.Token))
		c.logger.Info("API authentication enabled (bearer token)")
	default:
		c.logger.Warn("API authentication is DISABLED; enable [auth] or [rbac] in controller.toml for production")
	}
	var opts []grpc.ServerOption
	if len(unaryInterceptors) > 0 {
		opts = append(opts, grpc.ChainUnaryInterceptor(unaryInterceptors...))
	}
	if len(streamInterceptors) > 0 {
		opts = append(opts, grpc.ChainStreamInterceptor(streamInterceptors...))
	}
	// The in-process REST gateway dials back with 10s keepalive pings; the
	// gRPC default enforcement (5 min) answers those with GOAWAY
	// "too_many_pings", and every REST request in the reconnect window
	// fails with a 500. Permit frequent pings explicitly.
	opts = append(opts, grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
		MinTime:             5 * time.Second,
		PermitWithoutStream: true,
	}))
	c.server = grpc.NewServer(opts...)

	// Register health service
	healthServer := health.NewServer()
	grpc_health_v1.RegisterHealthServer(c.server, healthServer)
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	// Register SDS controller service
	sdsServer := NewServer(c)
	sdspb.RegisterSDSControllerServer(c.server, sdsServer)

	c.logger.Info("Registered SDS controller service")

	// Start Alert Monitor if enabled
	if c.config.Alert.Enabled && c.config.Alert.WebhookURL != "" {
		interval := time.Duration(c.config.Alert.CheckIntervalSec) * time.Second
		c.alertMonitor = alert.NewMonitor(c.config.Alert.WebhookURL, interval, c.resources, c.logger)
		c.alertMonitor.Start(c.ctx)
		c.logger.Info("Alert monitor started", zap.String("webhook", c.config.Alert.WebhookURL))
	}

	// Start gRPC server
	go func() {
		c.logger.Info("gRPC server listening", zap.String("address", grpcAddr))
		if err := c.server.Serve(grpcLis); err != nil {
			c.logger.Error("gRPC server error", zap.Error(err))
		}
	}()

	// Start HTTP REST API gateway on port 3375
	restPort := 3375
	restAddr := fmt.Sprintf("%s:%d", c.config.Server.ListenAddress, restPort)
	restLis, err := net.Listen("tcp", restAddr)
	if err != nil {
		return fmt.Errorf("failed to listen for REST: %w", err)
	}

	// Create and register gRPC-Gateway. The default header matcher forwards
	// well-known headers (Authorization arrives as grpcgateway-authorization,
	// which the auth interceptor accepts). Forwarding ALL headers is not an
	// option: browsers send hop-by-hop headers like "Connection" that are
	// illegal in HTTP/2 and kill the loopback gRPC stream with
	// RST_STREAM PROTOCOL_ERROR.
	gatewayMux := runtime.NewServeMux()

	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(4 * 1024 * 1024)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                10 * time.Second,
			Timeout:             time.Second,
			PermitWithoutStream: true,
		}),
	}

	// Register gateway handler pointing to local gRPC server
	if err := sdspb.RegisterSDSControllerHandlerFromEndpoint(context.Background(), gatewayMux, grpcAddr, dialOpts); err != nil {
		return fmt.Errorf("failed to register gateway handler: %w", err)
	}

	// Read-only RBAC introspection endpoints for the UI/CLI (whoami / policies).
	c.registerRBACRoutes(gatewayMux, rbacEngine)

	// Wrap with CORS handler
	corsHandler := corsMiddleware(gatewayMux)

	// Create HTTP server for gateway. Plain-text HTTP/1.1: h2 negotiation
	// only happens over TLS (TLSNextProto kept empty), and browsers never
	// speak h2c without an explicit upgrade, which we don't offer.
	gatewayServer := &http.Server{
		Handler:           corsHandler,
		ReadHeaderTimeout: 5 * time.Second,
		TLSNextProto:      make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
	}

	go func() {
		c.logger.Info("HTTP REST API gateway listening", zap.String("address", restAddr))
		if err := gatewayServer.Serve(restLis); err != nil && err != http.ErrServerClosed {
			c.logger.Error("HTTP gateway server error", zap.Error(err))
		}
	}()

	c.logger.Info("Server listening",
		zap.String("grpc", grpcAddr),
		zap.String("rest", restAddr))

	return nil
}

// corsMiddleware adds CORS headers and answers preflight requests.
//
// Note: this deliberately does NOT force "Connection: close" or sniff for an
// HTTP/2 preface. A previous first-byte 'P' check meant to reject the h2c
// preface ("PRI ...") also killed every connection whose first request was a
// POST or PATCH, silently breaking all mutating REST calls from browsers.
func corsMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Type")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		h.ServeHTTP(w, r)
	})
}

// startMetricsServer starts the Prometheus metrics HTTP server
func (c *Controller) startMetricsServer() error {
	addr := fmt.Sprintf("%s:%d", c.config.Metrics.ListenAddress, c.config.Metrics.Port)
	c.metricsServer = &http.Server{
		Addr:    addr,
		Handler: c.metrics.Handler(),
	}

	go func() {
		c.logger.Info("Metrics server listening", zap.String("address", addr))
		if err := c.metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			c.logger.Error("Metrics server error", zap.Error(err))
		}
	}()

	return nil
}

// GetHosts returns the list of hosts
func (c *Controller) GetHosts() []string {
	return c.hosts
}

// GetDeployment returns the deployment client
func (c *Controller) GetDeployment() deploymentClient {
	return c.deployment
}

// GetMetrics returns the metrics instance
func (c *Controller) GetMetrics() *metrics.Metrics {
	return c.metrics
}

// ResolveHost resolves a hostname to an address
func (c *Controller) ResolveHost(hostOrAddr string) string {
	c.hostsLock.RLock()
	defer c.hostsLock.RUnlock()

	// Try to resolve hostname to address first
	if addr, ok := c.hostsMap[hostOrAddr]; ok {
		return addr
	}

	// If it's already an address in our hosts list, return as is
	for _, addr := range c.hosts {
		if addr == hostOrAddr {
			return hostOrAddr
		}
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
	rm                *ResourceManager
	autoStateVolume   bool
	stateVolumeSizeGB uint32
}

// NewGatewayResourceManager creates a new gateway resource manager adapter.
// autoStateVolume/stateVolumeSizeGB control whether a missing cluster-private
// state volume is provisioned automatically during gateway creation.
func NewGatewayResourceManager(rm *ResourceManager, autoStateVolume bool, stateVolumeSizeGB uint32) gateway.ResourceManager {
	if stateVolumeSizeGB == 0 {
		stateVolumeSizeGB = 1
	}
	return &GatewayResourceManager{
		rm:                rm,
		autoStateVolume:   autoStateVolume,
		stateVolumeSizeGB: stateVolumeSizeGB,
	}
}

// EnsureGatewayVolumes provisions the small cluster-private state volume(s) a
// gateway needs, so a single-volume resource can be exported directly. It is a
// no-op when the resource already has enough volumes or when auto-provisioning
// is disabled (the gateway's own check then surfaces a clear error).
func (a *GatewayResourceManager) EnsureGatewayVolumes(ctx context.Context, resource string, minVolumes int) error {
	if !a.autoStateVolume {
		return nil
	}
	info, err := a.rm.GetResource(ctx, resource)
	if err != nil {
		return err
	}
	if len(info.Volumes) >= minVolumes {
		return nil
	}
	if len(info.Volumes) == 0 {
		return fmt.Errorf("resource %q has no volumes to derive a pool from", resource)
	}
	pool := info.Volumes[0].Pool
	if pool == "" {
		return fmt.Errorf("cannot determine storage pool for resource %q", resource)
	}
	for n := len(info.Volumes); n < minVolumes; n++ {
		volName := fmt.Sprintf("%s_state%d", resource, n)
		a.rm.controller.logger.Info("Auto-provisioning gateway state volume",
			zap.String("resource", resource),
			zap.String("volume", volName),
			zap.String("pool", pool),
			zap.Uint32("size_gb", a.stateVolumeSizeGB))
		if err := a.rm.AddVolume(ctx, resource, volName, pool, a.stateVolumeSizeGB); err != nil {
			return fmt.Errorf("auto-provision state volume %q: %w", volName, err)
		}
	}
	return nil
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
	result, err := a.dc.Exec(ctx, hosts, cmd)
	if err != nil {
		return err
	}
	// Per-host command failures must surface: swallowing them let gateway
	// setup steps (e.g. formatting the cluster-private volume) fail
	// silently while the gateway reported success.
	for host, hr := range result.Hosts {
		if !hr.Success {
			return fmt.Errorf("command failed on %s: %s", host, strings.TrimSpace(hr.Output))
		}
	}
	return nil
}

// WanproxyDeploymentClient adapts the controller's deploymentClient to the
// wanproxy.DeploymentClient interface, converting deployment result types into
// wanproxy.Result. It mirrors GatewayDeploymentClient (the gateway adapter) and
// wraps the same interface, so the WAN provisioner runs over the exact SSH
// transport the rest of the controller uses (and is trivially fakeable in tests).
type WanproxyDeploymentClient struct {
	dc deploymentClient
}

// NewWanproxyDeploymentClient creates a wanproxy deployment client adapter over
// the controller's deployment client.
func NewWanproxyDeploymentClient(dc deploymentClient) wanproxy.DeploymentClient {
	return &WanproxyDeploymentClient{dc: dc}
}

func (a *WanproxyDeploymentClient) DistributeConfig(ctx context.Context, hosts []string, content, remotePath string) (*wanproxy.Result, error) {
	res, err := a.dc.DistributeConfig(ctx, hosts, content, remotePath)
	if err != nil {
		return nil, err
	}
	return configResultToWanproxy(res), nil
}

func (a *WanproxyDeploymentClient) Exec(ctx context.Context, hosts []string, cmd string) (*wanproxy.Result, error) {
	res, err := a.dc.Exec(ctx, hosts, cmd)
	if err != nil {
		return nil, err
	}
	return execResultToWanproxy(res), nil
}

// execResultToWanproxy converts a deployment.ExecResult into a wanproxy.Result.
func execResultToWanproxy(res *deployment.ExecResult) *wanproxy.Result {
	out := &wanproxy.Result{Hosts: make(map[string]*wanproxy.HostResult)}
	if res == nil {
		return out
	}
	for host, hr := range res.Hosts {
		out.Hosts[host] = &wanproxy.HostResult{Host: hr.Host, Output: hr.Output, Success: hr.Success, Err: hr.Error}
	}
	return out
}

// configResultToWanproxy converts a deployment.ConfigResult into a
// wanproxy.Result. ConfigResult carries a top-level Success flag that may be set
// with an empty per-host map (a fully-successful distribute), so when the map is
// empty we reflect the aggregate flag to keep Result.AllSuccess() accurate.
func configResultToWanproxy(res *deployment.ConfigResult) *wanproxy.Result {
	out := &wanproxy.Result{Hosts: make(map[string]*wanproxy.HostResult)}
	if res == nil {
		return out
	}
	for host, hr := range res.Hosts {
		out.Hosts[host] = &wanproxy.HostResult{Host: hr.Host, Output: hr.Output, Success: hr.Success, Err: hr.Error}
	}
	if len(out.Hosts) == 0 {
		out.Hosts["_"] = &wanproxy.HostResult{Host: "_", Success: res.Success}
	}
	return out
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
		var labels map[string]string
		if dbNode.Labels != "" {
			if err := json.Unmarshal([]byte(dbNode.Labels), &labels); err != nil {
				c.logger.Warn("Failed to decode node labels; ignoring",
					zap.String("node", dbNode.Name), zap.Error(err))
			}
		}
		c.nodes.mu.Lock()
		c.nodes.nodes[dbNode.Address] = &NodeInfo{
			Name:     dbNode.Name,
			Address:  dbNode.Address,
			Hostname: dbNode.Hostname,
			State:    NodeState(dbNode.State),
			LastSeen: dbNode.LastSeen,
			Version:  dbNode.Version,
			Capacity: make(map[string]interface{}),
			Labels:   labels,
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
