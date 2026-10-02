// Package deployment handles the execution of commands on storage nodes
// using the dispatch SSH library.
package deployment

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"

	"github.com/liliang-cn/dispatch/pkg/dispatch"
	"go.uber.org/zap"
)

// getLocalIPs returns all local IP addresses
func getLocalIPs() []string {
	var ips []string
	interfaces, err := net.Interfaces()
	if err != nil {
		return ips
	}

	for _, iface := range interfaces {
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
			if ip != nil && !ip.IsLoopback() {
				ips = append(ips, ip.String())
			}
		}
	}
	return ips
}

// asRootShim makes `sudo` a pass-through for a controller already running as
// root. The commands are written for remote nodes and say `sudo`; run locally
// they gain nothing from it, and each one put three lines (COMMAND, session
// opened, session closed) into the controller's own journal — on a polling
// controller about 97% of it, drowning every line the controller wrote.
// Exported so the `bash -c` scripts some commands start see it too.
const asRootShim = `sudo() { "$@"; }; export -f sudo; `

// localCommand runs a node command on this machine.
func localCommand(ctx context.Context, cmd string) *exec.Cmd {
	if os.Geteuid() == 0 {
		return exec.CommandContext(ctx, "bash", "-c", asRootShim+cmd)
	}
	return exec.CommandContext(ctx, "sh", "-c", cmd)
}

// isLocalIP checks if an IP address is local
func isLocalIP(host string, localAddrs []string) bool {
	for _, localIP := range localAddrs {
		if host == localIP {
			return true
		}
	}
	return false
}

// Client handles DRBD resource management via dispatch
type Client struct {
	dispatch *dispatch.Dispatch
	logger   *zap.Logger
	parallel int
	// configPath is the dispatch config the client was built from, which
	// StreamLines reads again for the SSH settings dispatch keeps to itself.
	configPath string
}

// Options tunes how the deployment client reaches storage nodes.
type Options struct {
	// ConfigPath is the dispatch TOML config to use. Empty keeps dispatch's
	// own default (~/.dispatch/config.toml, then ~/.ssh/config).
	ConfigPath string
	// Parallel caps the fan-out of a single dispatch call. 0 uses the default.
	Parallel int
}

const defaultParallel = 10

// New creates a new deployment Client using dispatch's default config
// discovery (~/.dispatch/config.toml, falling back to ~/.ssh/config).
func New(logger *zap.Logger) (*Client, error) {
	return NewWithOptions(logger, Options{})
}

// NewWithOptions creates a deployment Client honouring an explicit dispatch
// config path and parallelism.
//
// Passing the config path through matters because the controller may run as a
// user whose home directory is not where the operator put the dispatch config
// (systemd unit with its own HOME, non-root operator, test harness). Before
// this existed the `[dispatch] config_path` setting in controller.toml was
// parsed by nobody and silently ignored, so the controller kept using
// ~/.dispatch/config.toml and failed with opaque SSH auth errors.
func NewWithOptions(logger *zap.Logger, opts Options) (*Client, error) {
	var dispatchCfg *dispatch.Config
	if opts.ConfigPath != "" {
		// dispatch silently falls back to its own default config
		// (~/.dispatch/config.toml, then ~/.ssh/config) when the path it is
		// handed does not exist. A typo in controller.toml would therefore put
		// the controller back on somebody else's SSH settings and fail later
		// with an opaque "unable to authenticate" — the exact dead end wiring
		// this path was meant to end. Refuse to start instead.
		if _, err := os.Stat(opts.ConfigPath); err != nil {
			return nil, fmt.Errorf("dispatch config %q: %w", opts.ConfigPath, err)
		}
		dispatchCfg = &dispatch.Config{ConfigPath: opts.ConfigPath}
	}

	client, err := dispatch.New(dispatchCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create dispatch client: %w", err)
	}

	parallel := opts.Parallel
	if parallel <= 0 {
		parallel = defaultParallel
	}

	if logger != nil && opts.ConfigPath != "" {
		logger.Info("Using dispatch config", zap.String("path", opts.ConfigPath))
	}

	return &Client{
		dispatch:   client,
		logger:     logger,
		parallel:   parallel,
		configPath: opts.ConfigPath,
	}, nil
}

// isLocalHost checks if a host is the local machine
func isLocalHost(host string) bool {
	hostname, _ := os.Hostname()

	// Check if host matches local hostname
	if host == hostname || host == "localhost" || host == "127.0.0.1" {
		return true
	}

	// Check if host matches any local IP address
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}

	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}

		if ip != nil && ip.String() == host {
			return true
		}
	}

	return false
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

	c.logger.Debug("deployment.Exec called",
		zap.Strings("hosts", hosts),
		zap.String("cmd", cmd),
		zap.Duration("timeout", timeout))

	// Separate local and remote hosts
	var localHosts []string
	var remoteHosts []string
	localAddrs := getLocalIPs()
	for _, host := range hosts {
		if isLocalIP(host, localAddrs) {
			localHosts = append(localHosts, host)
		} else {
			remoteHosts = append(remoteHosts, host)
		}
	}

	c.logger.Debug("Host classification",
		zap.Strings("local", localHosts),
		zap.Strings("remote", remoteHosts))

	// Initialize result
	result := &dispatch.ExecResult{
		Hosts: make(map[string]*dispatch.HostResult),
	}

	// Execute on local hosts using os/exec
	for _, host := range localHosts {
		start := time.Now()
		output, err := localCommand(ctx, cmd).CombinedOutput()
		end := time.Now()
		exitCode := 0
		var errorMsg error = nil
		if err != nil {
			errorMsg = err
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() >= 0 {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = 1
			}
		}
		result.Hosts[host] = &dispatch.HostResult{
			Host:      host,
			Output:    output,
			StartTime: start,
			EndTime:   end,
			Duration:  end.Sub(start),
			ExitCode:  exitCode,
			ErrorMsg:  errorMsg,
			Success:   exitCode == 0 && errorMsg == nil,
		}
	}

	// Execute on remote hosts using dispatch
	if len(remoteHosts) > 0 {
		dispatchResult, dispatchErr := c.dispatch.Exec(ctx, remoteHosts, cmd,
			dispatch.WithParallel(parallel),
			dispatch.WithTimeout(timeout),
		)
		if dispatchErr != nil {
			c.logger.Warn("Remote dispatch.Exec failed", zap.Error(dispatchErr))
			return nil, dispatchErr
		}
		for host, r := range dispatchResult.Hosts {
			result.Hosts[host] = r
		}
	}

	c.logger.Debug("deployment.Exec completed",
		zap.Int("result_hosts_count", len(result.Hosts)),
		zap.Strings("requested_hosts", hosts))

	execResult := &ExecResult{
		Hosts: make(map[string]*HostResult),
	}

	for host, r := range result.Hosts {
		// Log stderr too. drbdadm/lvcreate/zfs report their real reason there,
		// so a stdout-only debug line shows `output_len: 0` for a command that
		// failed with a perfectly good explanation — the exact dead end this
		// log exists to prevent.
		c.logger.Debug("deployment.Exec result",
			zap.String("host", host),
			zap.Bool("success", r.Success),
			zap.Int("exit_code", r.ExitCode),
			zap.String("error_msg", fmt.Sprintf("%v", r.ErrorMsg)),
			zap.Int("output_len", len(r.Output)),
			zap.String("output", string(r.Output)),
			zap.String("stderr", string(r.Error)))
		// dispatch keeps stdout and stderr apart, but almost everything worth
		// reporting from lvcreate/drbdadm/zfs goes to STDERR — "already exists",
		// "insufficient free space", "Refusing to be resized". Exposing only
		// stdout is why failures used to surface as an empty message ("creation
		// failed on 10.0.0.1: "), leaving callers nothing to act on. Callers
		// treat Output as "what the command said", so give them both streams.
		hr := &HostResult{
			Host:    host,
			Output:  combineStreams(string(r.Output), string(r.Error)),
			Success: r.Success,
			Error:   fmt.Errorf("%s", string(r.Error)),
		}
		// A command that never ran has no output at all: the reason is the
		// connection's — a changed SSH host key, a refused or timed-out
		// connection — and dispatch reports it in ErrorMsg. Dropping it made
		// an unreachable node read as "no output" in every error and alert.
		if r.ErrorMsg != nil {
			hr.Error = r.ErrorMsg
			if hr.Output == "" {
				hr.Output = r.ErrorMsg.Error()
			}
		}
		execResult.Hosts[host] = hr
	}

	return execResult, nil
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

// ============ Options ============

// ExecOption configures command execution
type ExecOption func(*execOptions)

type execOptions struct {
	parallel int
	timeout  time.Duration
}

// ExecTimeoutOf reports the timeout an option list would apply, or 0 when it
// sets none and Exec's own default would be used.
//
// Exported so a caller can assert on what it is about to ask for. The default
// suits short queries and is badly wrong for a command that streams a volume,
// which is not visible at the call site without this.
func ExecTimeoutOf(opts ...ExecOption) time.Duration {
	o := &execOptions{}
	for _, opt := range opts {
		opt(o)
	}
	return o.timeout
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
