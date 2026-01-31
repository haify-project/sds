// Package deployment provides DRBD resource management using dispatch
// for SSH-based operations without drbd-agent.
//
// Architecture:
//   - dispatch: config distribution + command execution
//   - drbd-reactor: continues to run on nodes for HA/failover
package deployment

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liliang-cn/dispatch/pkg/dispatch"
	"go.uber.org/zap"
)

// Client handles DRBD resource management via dispatch
type Client struct {
	dispatch *dispatch.Dispatch
	logger   *zap.Logger
	parallel int
}

// Config creates a new Client
type Config struct {
	// DispatchConfig is the path to dispatch config (~/.dispatch/config.toml)
	DispatchConfig string
	// Parallel is the default parallelism for operations
	Parallel int
}

// New creates a new deployment Client
func New(cfg *Config, logger *zap.Logger) (*Client, error) {
	if cfg == nil {
		cfg = &Config{}
	}
	if cfg.Parallel == 0 {
		cfg.Parallel = 10
	}

	client, err := dispatch.New(&dispatch.Config{
		ConfigPath: cfg.DispatchConfig,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create dispatch client: %w", err)
	}

	return &Client{
		dispatch: client,
		logger:   logger,
		parallel: cfg.Parallel,
	}, nil
}

// ============ Config Distribution ============

// DistributeConfig distributes a configuration file to multiple nodes
func (c *Client) DistributeConfig(ctx context.Context, hosts []string, content, remotePath string, opts ...ConfigOption) (*ConfigResult, error) {
	options := &configOptions{}
	for _, opt := range opts {
		opt(options)
	}

	// Create temp file
	tempFile, err := os.CreateTemp("", "sds-config-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tempFile.Name())

	if _, err := tempFile.WriteString(content); err != nil {
		tempFile.Close()
		return nil, fmt.Errorf("failed to write config: %w", err)
	}
	tempFile.Close()

	c.logger.Info("Distributing config",
		zap.Strings("hosts", hosts),
		zap.String("path", remotePath))

	// First, copy to /tmp/ on all nodes
	tempRemotePath := "/tmp/" + filepath.Base(remotePath) + ".tmp"

	copyOpts := []dispatch.CopyOption{
		dispatch.WithCopyMode(0644),
	}
	if options.backup {
		copyOpts = append(copyOpts, dispatch.WithBackup(true))
	}

	result, err := c.dispatch.Copy(ctx, hosts, tempFile.Name(), tempRemotePath, copyOpts...)
	if err != nil {
		return nil, fmt.Errorf("copy failed: %w", err)
	}

	// Then, move with sudo to final location
	moveResult, err := c.Exec(ctx, hosts, fmt.Sprintf("sudo mkdir -p %s && sudo mv %s %s", filepath.Dir(remotePath), tempRemotePath, remotePath))
	if err != nil {
		return nil, fmt.Errorf("sudo move failed: %w", err)
	}

	// Combine results
	configResult := &ConfigResult{
		Path:    remotePath,
		Success: true,
		Hosts:   make(map[string]*HostResult),
	}

	for _, host := range hosts {
		copyOK := result.Hosts[host] != nil && result.Hosts[host].Success
		moveOK := moveResult.Hosts[host] != nil && moveResult.Hosts[host].Success

		var combinedErr error
		if !copyOK {
			if result.Hosts[host] != nil && result.Hosts[host].Error != nil {
				combinedErr = result.Hosts[host].Error
			} else {
				combinedErr = fmt.Errorf("copy failed")
			}
		}
		if !moveOK && combinedErr == nil {
			if moveResult.Hosts[host] != nil && moveResult.Hosts[host].Error != nil {
				combinedErr = moveResult.Hosts[host].Error
			} else {
				combinedErr = fmt.Errorf("move failed")
			}
		}

		configResult.Hosts[host] = &HostResult{
			Host:    host,
			Success: copyOK && moveOK,
			Error:   combinedErr,
		}
		if !configResult.Hosts[host].Success {
			configResult.Success = false
		}
	}

	// Run post-command if specified
	if options.postCommand != "" {
		_, _ = c.Exec(ctx, hosts, options.postCommand)
	}

	return configResult, nil
}

// DeleteConfig removes a config file from all nodes
func (c *Client) DeleteConfig(ctx context.Context, hosts []string, remotePath string) error {
	c.logger.Info("Deleting config", zap.String("path", remotePath))
	
	_, err := c.Exec(ctx, hosts, fmt.Sprintf("sudo rm -f %s", remotePath))
	return err
}

// ============ Command Execution ============

// Exec executes a command on multiple hosts
func (c *Client) Exec(ctx context.Context, hosts []string, cmd string, opts ...ExecOption) (*ExecResult, error) {
	options := &execOptions{}
	for _, opt := range opts {
		opt(options)
	}

	parallel := c.parallel
	if options.parallel > 0 {
		parallel = options.parallel
	}
	timeout := 30 * time.Second
	if options.timeout > 0 {
		timeout = options.timeout
	}

	// Debug: log before calling dispatch
	c.logger.Debug("deployment.Exec called",
		zap.Strings("hosts", hosts),
		zap.String("cmd", cmd),
		zap.Duration("timeout", timeout))

	result, err := c.dispatch.Exec(ctx, hosts, cmd,
		dispatch.WithParallel(parallel),
		dispatch.WithTimeout(timeout),
	)

	if err != nil {
		return nil, err
	}

	execResult := &ExecResult{
		Hosts: make(map[string]*HostResult),
	}

	for host, r := range result.Hosts {
		c.logger.Debug("deployment.Exec result",
			zap.String("host", host),
			zap.Bool("success", r.Success),
			zap.Int("output_len", len(r.Output)),
			zap.String("output", string(r.Output)))
		execResult.Hosts[host] = &HostResult{
			Host:    host,
			Output:  string(r.Output),
			Success: r.Success,
			Error:   fmt.Errorf("%s", string(r.Error)),
		}
	}

	return execResult, nil
}

// ============ LVM Operations ============

// PVCreate creates physical volumes
func (c *Client) PVCreate(ctx context.Context, hosts []string, device string, opts ...LVMOption) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo pvcreate %s", device)
	return c.Exec(ctx, hosts, cmd)
}

// VGCreate creates volume groups
func (c *Client) VGCreate(ctx context.Context, hosts []string, vgName string, devices []string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo vgcreate %s %s", vgName, strings.Join(devices, " "))
	return c.Exec(ctx, hosts, cmd)
}

// LVCreate creates logical volumes
func (c *Client) LVCreate(ctx context.Context, hosts []string, vgName, lvName, size string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo lvcreate -y -L %s -n %s %s", size, lvName, vgName)
	return c.Exec(ctx, hosts, cmd)
}

// LVRemove removes logical volumes
func (c *Client) LVRemove(ctx context.Context, hosts []string, lvPath string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo lvremove -f %s", lvPath)
	return c.Exec(ctx, hosts, cmd)
}

// ============ DRBD Operations ============

// DRBDUp brings up a DRBD resource
func (c *Client) DRBDUp(ctx context.Context, hosts []string, resource string) (*ExecResult, error) {
	return c.Exec(ctx, hosts, fmt.Sprintf("sudo drbdadm up %s", resource))
}

// DRBDDown brings down a DRBD resource
func (c *Client) DRBDDown(ctx context.Context, hosts []string, resource string) (*ExecResult, error) {
	return c.Exec(ctx, hosts, fmt.Sprintf("sudo drbdadm down %s", resource))
}

// DRBDPrimary sets resource to Primary
func (c *Client) DRBDPrimary(ctx context.Context, host, resource string, force bool) (*HostResult, error) {
	cmd := fmt.Sprintf("sudo drbdadm primary %s", resource)
	if force {
		cmd += " --force"
	}
	result, err := c.Exec(ctx, []string{host}, cmd)
	if err != nil {
		return nil, err
	}
	return result.Hosts[host], nil
}

// DRBDSecondary sets resource to Secondary
func (c *Client) DRBDSecondary(ctx context.Context, host, resource string) (*HostResult, error) {
	result, err := c.Exec(ctx, []string{host}, fmt.Sprintf("sudo drbdadm secondary %s", resource))
	if err != nil {
		return nil, err
	}
	return result.Hosts[host], nil
}

// DRBDCreateMD creates DRBD metadata
func (c *Client) DRBDCreateMD(ctx context.Context, hosts []string, resource string) (*ExecResult, error) {
	return c.Exec(ctx, hosts, fmt.Sprintf("sudo drbdadm create-md %s", resource))
}

// DRBDAdjust adjusts DRBD configuration
func (c *Client) DRBDAdjust(ctx context.Context, hosts []string, resource string) (*ExecResult, error) {
	return c.Exec(ctx, hosts, fmt.Sprintf("sudo drbdadm adjust %s", resource))
}

// DRBDStatus gets DRBD resource status
func (c *Client) DRBDStatus(ctx context.Context, hosts []string, resource string) (*ExecResult, error) {
	return c.Exec(ctx, hosts, fmt.Sprintf("sudo drbdadm status %s", resource))
}

// ============ Reactor Operations ============

// ReactorWriteConfig writes reactor plugin config
func (c *Client) ReactorWriteConfig(ctx context.Context, hosts []string, pluginID, content string) (*ConfigResult, error) {
	remotePath := fmt.Sprintf("/etc/drbd-reactor.d/%s.toml", pluginID)
	return c.DistributeConfig(ctx, hosts, content, remotePath,
		WithPostCommand("sudo systemctl reload drbd-reactor || sudo systemctl restart drbd-reactor"))
}

// ReactorEnablePlugin enables a promoter plugin
func (c *Client) ReactorEnablePlugin(ctx context.Context, hosts []string, pluginID string) (*ExecResult, error) {
	return c.Exec(ctx, hosts, fmt.Sprintf("sudo drbd-reactorctl prom enable %s", pluginID))
}

// ReactorDisablePlugin disables a promoter plugin
func (c *Client) ReactorDisablePlugin(ctx context.Context, hosts []string, pluginID string) (*ExecResult, error) {
	return c.Exec(ctx, hosts, fmt.Sprintf("sudo drbd-reactorctl prom disable %s", pluginID))
}

// ReactorEvict evicts a promoter from the current node
func (c *Client) ReactorEvict(ctx context.Context, config string) (*ExecResult, error) {
	return c.Exec(ctx, []string{"localhost"}, fmt.Sprintf("sudo drbd-reactorctl evict %s", config))
}

// ReactorReload reloads drbd-reactor
func (c *Client) ReactorReload(ctx context.Context, hosts []string) (*ExecResult, error) {
	return c.Exec(ctx, hosts, "sudo systemctl reload drbd-reactor || sudo systemctl restart drbd-reactor")
}

// ============ Service Management ============

// ServiceStart starts a systemd service
func (c *Client) ServiceStart(ctx context.Context, hosts []string, service string) (*ExecResult, error) {
	return c.Exec(ctx, hosts, fmt.Sprintf("sudo systemctl start %s", service))
}

// ServiceStop stops a systemd service
func (c *Client) ServiceStop(ctx context.Context, hosts []string, service string) (*ExecResult, error) {
	return c.Exec(ctx, hosts, fmt.Sprintf("sudo systemctl stop %s", service))
}

// ServiceRestart restarts a systemd service
func (c *Client) ServiceRestart(ctx context.Context, hosts []string, service string) (*ExecResult, error) {
	return c.Exec(ctx, hosts, fmt.Sprintf("sudo systemctl restart %s", service))
}

// ============ Result Types ============

// ConfigResult represents config distribution result
type ConfigResult struct {
	Path    string
	Success bool
	Hosts   map[string]*HostResult
}

// ExecResult represents command execution result
type ExecResult struct {
	Hosts map[string]*HostResult
}

// HostResult represents result for a single host
type HostResult struct {
	Host    string
	Output  string
	Success bool
	Error   error
}

// AllSuccess returns true if all operations succeeded
func (r *ExecResult) AllSuccess() bool {
	for _, h := range r.Hosts {
		if !h.Success {
			return false
		}
	}
	return true
}

// FailedHosts returns list of failed hosts
func (r *ExecResult) FailedHosts() []string {
	var failed []string
	for host, h := range r.Hosts {
		if !h.Success {
			failed = append(failed, host)
		}
	}
	return failed
}

// ============ Options ============

// ConfigOption configures config distribution
type ConfigOption func(*configOptions)

type configOptions struct {
	backup      bool
	postCommand string
}

// WithBackup enables backup of existing config
func WithBackup(backup bool) ConfigOption {
	return func(o *configOptions) {
		o.backup = backup
	}
}

// WithPostCommand sets a command to run after distribution
func WithPostCommand(cmd string) ConfigOption {
	return func(o *configOptions) {
		o.postCommand = cmd
	}
}

// ExecOption configures command execution
type ExecOption func(*execOptions)

type execOptions struct {
	parallel int
	timeout  time.Duration
}

// WithExecParallel sets parallelism
func WithExecParallel(n int) ExecOption {
	return func(o *execOptions) {
		o.parallel = n
	}
}

// WithExecTimeout sets timeout
func WithExecTimeout(d time.Duration) ExecOption {
	return func(o *execOptions) {
		o.timeout = d
	}
}

// LVMOption configures LVM operations
type LVMOption func(*lvmOptions)

type lvmOptions struct {
	force bool
}

// WithLVMForce enables force flag for LVM operations
func WithLVMForce(force bool) LVMOption {
	return func(o *lvmOptions) {
		o.force = force
	}
}
