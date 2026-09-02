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
	"github.com/liliang-cn/sds/pkg/alert"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/config"
	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/deployment"
	"github.com/liliang-cn/sds/pkg/event"
	"github.com/liliang-cn/sds/pkg/gateway"
	"github.com/liliang-cn/sds/pkg/logbuf"
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
	restServer *http.Server
	ctx        context.Context
	cancel     context.CancelFunc
	// Metrics
	metrics       *metrics.Metrics
	metricsServer *http.Server
	// UI
	uiServer *UIServer
	// events carries operational notifications (degrade, failover, node loss)
	// from the health detector to Webhook receivers and watch streams. Nil when
	// notifications are disabled, which is what the API surfaces report on.
	events       *event.Bus
	alertMonitor *alert.Monitor
	// logRing holds the controller's recent log output for the API to serve.
	// Nil when the process was started without one, in which case the log view
	// reports that rather than showing an empty buffer.
	logRing *logbuf.Ring
	// Managers
	storage   *StorageManager
	resources *ResourceManager
	snapshots *SnapshotManager
	nodes     *NodeManager
	gateway   *gateway.Manager
	schedules *ScheduleManager
	backups   *BackupManager
	notify    *NotifyManager
}

// SetLogRing attaches the buffer the log view reads from. The ring has to exist
// before the logger that tees into it, which happens in main, so it is handed
// over here rather than built by New.
func (c *Controller) SetLogRing(r *logbuf.Ring) { c.logRing = r }

// closeDBAfterFailedStart hands the metadata database back when New gives up
// part way through.
//
// It matters because bolt holds an exclusive flock on the file: a controller
// that aborts with the database still open leaves the next start attempt to
// fail at "timeout" on a file nothing is really using — a far more confusing
// error than the one New is about to return. The close error itself is logged
// rather than returned, because the caller needs to see why the controller
// could not be built, not how the cleanup went.
func closeDBAfterFailedStart(logger *zap.Logger, db *database.DB) {
	if db == nil {
		return
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	if err := db.Close(); err != nil {
		logger.Warn("Failed to close database after an aborted controller start", zap.Error(err))
	}
}

// New creates a new controller
func New(cfg *config.Config, logger *zap.Logger) (*Controller, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// Open database
	db, err := database.Open(&database.Config{Path: cfg.Database.Path}, logger)
	if err != nil {
		// A schema this binary cannot safely write is the one open failure that
		// must not degrade into "continue without persistence". Running on with
		// db == nil means the controller reports an empty cluster and then
		// starts writing that view back over a database whose real content it
		// never understood — which is precisely the data loss the version check
		// exists to prevent. Refuse to start and let the operator roll forward
		// or restore.
		if database.IsSchemaIncompatible(err) {
			cancel()
			return nil, fmt.Errorf("cannot open database: %w", err)
		}
		logger.Warn("Failed to open database, continuing without persistence", zap.Error(err))
		db = nil
	}

	// WAN mTLS material lives next to the rest of the controller's state.
	// Without this the PKI cache is pinned to /var/lib/sds, so a non-root
	// controller fails resource creation at "mkdir /var/lib/sds: permission
	// denied" — after it has already created the backing volumes.
	if cfg.WAN.PKIDir != "" {
		wanproxy.PKIDir = cfg.WAN.PKIDir
	}

	// Create deployment client. The dispatch config path comes from
	// controller.toml so a controller running under a different HOME (systemd,
	// non-root operator) still finds the operator's SSH settings.
	deploymentClient, err := deployment.NewWithOptions(logger, deployment.Options{
		ConfigPath: cfg.Dispatch.ConfigPath,
		Parallel:   cfg.Dispatch.Parallel,
	})
	if err != nil {
		cancel()
		closeDBAfterFailedStart(logger, db)
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
	ctrl.backups = NewBackupManager(ctrl)

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
			closeDBAfterFailedStart(logger, db)
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

	// Bring up the event bus, health detector and Webhook receivers before the
	// API, so a watcher that connects the instant the port opens does not miss
	// the first poll's findings.
	c.startNotifications()

	// Start gRPC server
	if err := c.startGRPCServer(); err != nil {
		return fmt.Errorf("failed to start gRPC server: %w", err)
	}

	// Start UI server on whatever [ui] asks for.
	if c.config.UI.UIEnabled() {
		uiAddr, uiPort := c.config.UI.UIAddress(c.config.Server.ListenAddress)
		// The UI proxies its own-origin /v1 and /ai to these, so that publishing
		// the UI port alone is enough to use it from outside the LAN.
		uiServer, err := NewUIServer(c.logger, uiAddr, uiPort, defaultRESTPort, defaultAIPort)
		if err != nil {
			return fmt.Errorf("failed to create UI server: %w", err)
		}
		c.uiServer = uiServer
		if err := c.uiServer.Start(); err != nil {
			return fmt.Errorf("failed to start UI server: %w", err)
		}
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

	// A backup left "running" belongs to a controller that died mid-transfer;
	// only the active controller ships backups, so nothing can still be in
	// flight here. Resolve it now so an incomplete copy is never listed as
	// something that could be restored.
	if c.db != nil && c.backups != nil {
		if err := c.backups.ReconcileInterrupted(context.Background()); err != nil {
			c.logger.Warn("Failed to reconcile interrupted backups", zap.Error(err))
		}
	}

	c.logger.Info("SDS controller started",
		zap.String("address", c.config.Server.ListenAddress),
		zap.Int("port", c.config.Server.Port),
		zap.Strings("hosts", c.hosts))

	return nil
}

// Events returns the notification bus, or nil when notifications are disabled.
func (c *Controller) Events() *event.Bus { return c.events }

// alertOptions assembles what the health detector is asked to watch.
//
// Split out of startNotifications so the wiring can be asserted directly. The
// observer in particular is the kind of connection that goes missing without
// anything failing: the detector keeps raising events, the metrics endpoint
// keeps answering, and only the numbers on it are quietly empty.
func (c *Controller) alertOptions() alert.Options {
	opts := alert.Options{
		Interval:  time.Duration(c.config.Alert.CheckIntervalSec) * time.Second,
		Resources: c.resources,
		Logger:    c.logger,
	}
	// Node reachability costs an SSH round trip per node per poll, so it is a
	// separate switch from the resource checks, which are served from state the
	// controller already gathers.
	if c.config.Alert.CheckNodes {
		opts.Nodes = c.nodes
	}
	if c.config.Alert.CheckPools {
		opts.Pools = c.storage
		opts.NearFullPercent = c.config.Alert.PoolNearFullPercent
		opts.FullPercent = c.config.Alert.PoolFullPercent
	}
	// One poll, two consumers. The state the detector gathers to raise events is
	// the same state a dashboard needs, and collecting it twice would double the
	// SSH round trips to every node while letting the alert and the panel
	// disagree about the same instant — the disagreement an operator notices
	// first and trusts least.
	if c.metrics != nil {
		opts.Observer = newMetricsObserver(c)
	}
	return opts
}

// startNotifications brings up the event bus, the health detector that feeds
// it, and any configured Webhook receivers.
//
// A Webhook is no longer required to enable this: the bus also backs the watch
// stream and the SSE endpoint, so an operator who wants to tail events without
// standing up an HTTP receiver just sets enabled = true.
func (c *Controller) startNotifications() {
	if !c.config.Alert.Enabled {
		// The health poll is the only thing that reads cluster state on a
		// schedule, so it is also the only source the storage and DRBD gauges
		// have. Disabling alerts silently empties half of /metrics, which is
		// precisely the "a flat zero looks like a healthy cluster" failure the
		// gauges were wired up to end — so say it once, out loud, at startup.
		if c.config.Metrics.Enabled {
			c.logger.Warn("Metrics are enabled but alerts are not; the pool, gateway and DRBD replication gauges stay empty because they are fed by the health poll. Set [alert] enabled = true to populate them.")
		}
		return
	}

	c.events = event.NewBus(c.config.Alert.HistorySize)

	opts := c.alertOptions()
	c.alertMonitor = alert.NewMonitor(c.events, opts)
	c.alertMonitor.Start(c.ctx)

	// Database-backed channels come up before the file-based ones so a UI-added
	// channel is delivering by the time the first poll finishes.
	c.notify = NewNotifyManager(c)
	if err := c.notify.Reload(c.ctx); err != nil {
		c.logger.Warn("Failed to load notification channels", zap.Error(err))
	}

	for _, wh := range c.config.Alert.Receivers() {
		event.NewWebhook(event.WebhookConfig{
			URL:     wh.URL,
			Headers: wh.Headers,
			Filter:  event.Filter{MinSeverity: event.ParseSeverity(wh.MinSeverity)},
		}, c.logger).Start(c.ctx, c.events)
		c.logger.Info("Alert webhook registered",
			zap.String("url", wh.URL),
			zap.String("min_severity", wh.MinSeverity))
	}

	c.logger.Info("Notifications started",
		zap.Duration("interval", opts.Interval),
		zap.Bool("node_checks", opts.Nodes != nil),
		zap.Bool("feeding_metrics", opts.Observer != nil),
		zap.Int("webhooks", len(c.config.Alert.Receivers())),
		zap.Int("channels", c.notify.Active()))
}

// Stop stops the controller
func (c *Controller) Stop() {
	c.logger.Info("Stopping SDS controller")

	c.cancel()

	// Stop the snapshot scheduler
	if c.schedules != nil {
		c.schedules.Stop()
	}

	// Stop the health detector. Subscribers (Webhooks, watch streams) unwind on
	// their own once c.cancel above propagates.
	if c.alertMonitor != nil {
		c.alertMonitor.Stop()
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

	// Stop REST gateway server
	if c.restServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.restServer.Shutdown(ctx); err != nil {
			c.logger.Error("Failed to shutdown REST gateway server", zap.Error(err))
		}
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
		sink := c.auditSink()
		unaryInterceptors = append(unaryInterceptors,
			auditUnaryInterceptor(auditLog, c.config.Audit.IncludeReads, auditUser, sink))
		streamInterceptors = append(streamInterceptors,
			auditStreamInterceptor(auditLog, c.config.Audit.IncludeReads, auditUser, sink))
		c.logger.Info("API audit log enabled",
			zap.Bool("include_reads", c.config.Audit.IncludeReads),
			zap.Bool("persisted", sink != nil))
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
	// Transport security. Until this was wired up the whole [tls] section was
	// decoration: the listener stayed plaintext while the startup log reported
	// TLS as enabled, so the bearer token checked just above crossed the
	// network in the clear on a cluster whose operator believed otherwise.
	tlsSetup, err := newTLSSetup(c.config.TLS)
	if err != nil {
		return fmt.Errorf("failed to configure TLS: %w", err)
	}

	var opts []grpc.ServerOption
	if tlsSetup != nil {
		opts = append(opts, grpc.Creds(tlsSetup.serverCreds))
		c.logger.Info("API transport TLS enabled",
			zap.String("cert_file", c.config.TLS.CertFile),
			zap.Bool("mutual_tls", tlsSetup.mutual))
		if !tlsSetup.mutual {
			c.logger.Info("Client certificates are not required; set tls.client_ca_file for mutual TLS")
		}
	} else {
		c.logger.Warn("API transport is PLAINTEXT; bearer tokens cross the network unencrypted — enable [tls] in controller.toml for production")
	}
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

	// Start gRPC server
	go func() {
		c.logger.Info("gRPC server listening", zap.String("address", grpcAddr))
		if err := c.server.Serve(grpcLis); err != nil {
			c.logger.Error("gRPC server error", zap.Error(err))
		}
	}()

	// Start HTTP REST API gateway
	restPort := defaultRESTPort
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

	// Dial loopback explicitly — grpcAddr above is a LISTEN address and is
	// normally "0.0.0.0:3374", which is not a destination. See loopbackTarget.
	if err := sdspb.RegisterSDSControllerHandlerFromEndpoint(
		context.Background(), gatewayMux, loopbackTarget(c.config), loopbackDialOptions(tlsSetup)); err != nil {
		return fmt.Errorf("failed to register gateway handler: %w", err)
	}

	// Read-only RBAC introspection endpoints for the UI/CLI (whoami / policies).
	c.registerRBACRoutes(gatewayMux, rbacEngine)

	// Browser-facing SSE event stream, alongside the generated
	// /v1/events/watch that serves newline-delimited JSON.
	c.registerEventRoutes(gatewayMux, rbacEngine)

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
	c.restServer = gatewayServer

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
			// The gateway tells its own auto-provisioned "<res>_state<N>"
			// volume from the operator's "<res>_data" by this name. Without
			// it, it can only go by position — which is what used to make it
			// export the wrong one.
			BackingVolume: v.BackingVolume,
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
			Name:               dbNode.Name,
			Address:            dbNode.Address,
			ReplicationAddress: dbNode.ReplicationAddress,
			Hostname:           dbNode.Hostname,
			State:              NodeState(dbNode.State),
			LastSeen:           dbNode.LastSeen,
			Version:            dbNode.Version,
			Capacity:           make(map[string]interface{}),
			Labels:             labels,
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
