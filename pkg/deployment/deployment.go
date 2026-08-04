// Package deployment handles the execution of commands on storage nodes
// using the dispatch SSH library.
package deployment

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
		dispatch: client,
		logger:   logger,
		parallel: parallel,
	}, nil
}

// ============ Config Distribution ============

// DistributeConfig distributes a configuration file to multiple nodes
func (c *Client) DistributeConfig(ctx context.Context, hosts []string, content, remotePath string, opts ...ConfigOption) (*ConfigResult, error) {
	options := &configOptions{}
	for _, opt := range opts {
		opt(options)
	}

	c.logger.Info("Distributing config",
		zap.Strings("hosts", hosts),
		zap.String("path", remotePath))

	localTempFile := "/tmp/" + filepath.Base(remotePath) + ".tmp"

	configResult := &ConfigResult{
		Path:    remotePath,
		Success: true,
		Hosts:   make(map[string]*HostResult),
	}

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

	// Handle local hosts - write directly
	for _, host := range localHosts {
		// Write to temp path
		if err := os.WriteFile(localTempFile, []byte(content), 0644); err != nil {
			c.logger.Error("Failed to write local config", zap.String("host", host), zap.Error(err))
			configResult.Hosts[host] = &HostResult{
				Host:    host,
				Success: false,
				Error:   err,
			}
			configResult.Success = false
			continue
		}

		// Create directory and move to final location using local exec
		mkdirCmd := exec.Command("sudo", "mkdir", "-p", filepath.Dir(remotePath))
		if err := mkdirCmd.Run(); err != nil {
			c.logger.Error("Failed to create directory", zap.String("host", host), zap.Error(err))
			configResult.Hosts[host] = &HostResult{
				Host:    host,
				Success: false,
				Error:   err,
			}
			configResult.Success = false
			os.Remove(localTempFile)
			continue
		}

		mvCmd := exec.Command("sudo", "mv", "-f", localTempFile, remotePath)
		if err := mvCmd.Run(); err != nil {
			c.logger.Error("Failed to move local config", zap.String("host", host), zap.Error(err))
			configResult.Hosts[host] = &HostResult{
				Host:    host,
				Success: false,
				Error:   err,
			}
			configResult.Success = false
			os.Remove(localTempFile)
			continue
		}

		configResult.Hosts[host] = &HostResult{
			Host:    host,
			Success: true,
		}
		c.logger.Debug("Local config distributed", zap.String("host", host))
	}

	// Handle remote hosts - use dispatch.Copy
	if len(remoteHosts) > 0 {
		c.logger.Debug("Copying to remote hosts", zap.Strings("remote_hosts", remoteHosts))
		// For remote hosts, use cat + ssh + sudo tee to handle privileged paths
		for _, host := range remoteHosts {
			// First create directory
			mkdirCmd := fmt.Sprintf("sudo mkdir -p %s", filepath.Dir(remotePath))
			mkdirResult, err := c.Exec(ctx, []string{host}, mkdirCmd)
			if err != nil {
				c.logger.Error("Failed to create directory", zap.String("host", host), zap.Error(err))
				configResult.Hosts[host] = &HostResult{
					Host:    host,
					Success: false,
					Error:   err,
				}
				configResult.Success = false
				continue
			}
			if !mkdirResult.AllSuccess() {
				configResult.Hosts[host] = &HostResult{
					Host:    host,
					Success: false,
					Error:   fmt.Errorf("mkdir failed"),
				}
				configResult.Success = false
				continue
			}

			// Use cat | ssh | sudo tee to copy file with root permissions
			// Write content to temp file first
			if err := os.WriteFile(localTempFile, []byte(content), 0644); err != nil {
				c.logger.Error("Failed to write temp file", zap.Error(err))
				configResult.Hosts[host] = &HostResult{
					Host:    host,
					Success: false,
					Error:   err,
				}
				configResult.Success = false
				continue
			}

			// Copy using ssh with sudo tee (direct execution via dispatch)
			// First read file content
			fileContent, err := os.ReadFile(localTempFile)
			if err != nil {
				c.logger.Error("Failed to read temp file", zap.Error(err))
				configResult.Hosts[host] = &HostResult{
					Host:    host,
					Success: false,
					Error:   err,
				}
				configResult.Success = false
				continue
			}

			// Transfer via base64 so binary content survives intact. A single
			// `echo <base64>` breaks for large files: one shell argument is
			// capped at MAX_ARG_STRLEN (128 KiB on Linux), so a multi-MB binary
			// (e.g. the sds-proxy WAN binary) fails with "Argument list too long"
			// and the file is never written. Small content keeps the fast single
			// command; large content is streamed in sub-128 KiB base64 chunks.
			encoded := base64.StdEncoding.EncodeToString(fileContent)
			var copyErr error
			if len(encoded) <= maxInlineB64Len {
				cmd := fmt.Sprintf("echo %s | base64 -d | sudo tee %s > /dev/null",
					fmt.Sprintf("%q", encoded), remotePath)
				r, err := c.Exec(ctx, []string{host}, cmd)
				switch {
				case err != nil:
					copyErr = err
				case !r.AllSuccess():
					copyErr = fmt.Errorf("copy failed")
				}
			} else {
				copyErr = c.writeRemoteFileChunked(ctx, host, remotePath, encoded)
			}
			if copyErr != nil {
				c.logger.Error("Failed to copy config", zap.String("host", host), zap.Error(copyErr))
				configResult.Hosts[host] = &HostResult{
					Host:    host,
					Success: false,
					Error:   copyErr,
				}
				configResult.Success = false
				continue
			}

			configResult.Hosts[host] = &HostResult{
				Host:    host,
				Success: true,
			}
			c.logger.Debug("Remote config distributed", zap.String("host", host))
		}
		os.Remove(localTempFile)
	}

	// Run post-command if specified
	if options.postCommand != "" {
		_, _ = c.Exec(ctx, hosts, options.postCommand)
	}

	return configResult, nil
}

// maxInlineB64Len bounds the base64 payload sent as a single `echo` argument.
// Linux caps one argument at MAX_ARG_STRLEN (128 KiB); stay well under it so the
// fast single-command path never trips "Argument list too long".
const maxInlineB64Len = 100 * 1024

// writeRemoteFileChunked writes base64-encoded content to remotePath on host by
// streaming it in sub-128 KiB chunks (each a single safe argument), then decoding
// once server-side. This is what lets DistributeConfig ship multi-MB binaries
// (e.g. sds-proxy) that overflow a single-argument echo. base64's alphabet
// (A-Za-z0-9+/=) contains no single-quote, so each chunk is quote-safe.
func (c *Client) writeRemoteFileChunked(ctx context.Context, host, remotePath, encoded string) error {
	tmp := remotePath + ".b64.part"
	// Start from an empty temp file.
	if r, err := c.Exec(ctx, []string{host}, fmt.Sprintf("sudo sh -c ': > %s'", tmp)); err != nil {
		return fmt.Errorf("init temp file: %w", err)
	} else if !r.AllSuccess() {
		return fmt.Errorf("init temp file on %s failed", host)
	}
	const chunk = maxInlineB64Len
	for i := 0; i < len(encoded); i += chunk {
		end := i + chunk
		if end > len(encoded) {
			end = len(encoded)
		}
		cmd := fmt.Sprintf("printf '%%s' '%s' | sudo tee -a %s > /dev/null", encoded[i:end], tmp)
		if r, err := c.Exec(ctx, []string{host}, cmd); err != nil {
			return fmt.Errorf("append chunk: %w", err)
		} else if !r.AllSuccess() {
			return fmt.Errorf("append chunk on %s failed", host)
		}
	}
	// Decode into place and drop the temp file.
	cmd := fmt.Sprintf("sudo sh -c 'base64 -d %s | tee %s > /dev/null && rm -f %s'", tmp, remotePath, tmp)
	if r, err := c.Exec(ctx, []string{host}, cmd); err != nil {
		return fmt.Errorf("decode remote file: %w", err)
	} else if !r.AllSuccess() {
		return fmt.Errorf("decode remote file on %s failed", host)
	}
	return nil
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
		output, err := exec.CommandContext(ctx, "sh", "-c", cmd).CombinedOutput()
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
		execResult.Hosts[host] = &HostResult{
			Host:    host,
			Output:  combineStreams(string(r.Output), string(r.Error)),
			Success: r.Success,
			Error:   fmt.Errorf("%s", string(r.Error)),
		}
	}

	return execResult, nil
}

// ============ ZFS Operations ============

// ZFSCreatePool creates a ZFS pool
func (c *Client) ZFSCreatePool(ctx context.Context, hosts []string, poolName string, vdevs []string, opts ...ZFSOption) (*ExecResult, error) {
	// A zpool has no thin/thick mode; it is just the aggregation of vdevs.
	// Thin vs thick provisioning is a per-zvol property decided at volume
	// creation time (zfs create -s -V / refreservation), not at the pool level,
	// so there are currently no pool-level options to apply here.
	_ = opts
	cmd := fmt.Sprintf("sudo zpool create -f %s %s", poolName, strings.Join(vdevs, " "))
	return c.Exec(ctx, hosts, cmd)
}

// ZFSDestroyPool destroys a ZFS pool
func (c *Client) ZFSDestroyPool(ctx context.Context, hosts []string, poolName string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zpool destroy -f %s", poolName)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSListPools lists ZFS pools
func (c *Client) ZFSListPools(ctx context.Context, hosts []string) (*ExecResult, error) {
	cmd := "sudo zpool list -Hp -o name,size,free,alloc,cap"
	return c.Exec(ctx, hosts, cmd)
}

// ZFSGetPool gets ZFS pool status
func (c *Client) ZFSGetPool(ctx context.Context, hosts []string, poolName string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zpool status %s", poolName)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSCreateDataset creates a ZFS dataset
func (c *Client) ZFSCreateDataset(ctx context.Context, hosts []string, datasetName string, opts ...ZFSOption) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zfs create %s", datasetName)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSCreateThinDataset creates a thin-provisioned ZFS dataset (zvol)
func (c *Client) ZFSCreateThinDataset(ctx context.Context, hosts []string, poolName, datasetName, size string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zfs create -s -V %s %s/%s", size, poolName, datasetName)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSDestroyDataset destroys a ZFS dataset
func (c *Client) ZFSDestroyDataset(ctx context.Context, hosts []string, datasetName string) (*ExecResult, error) {
	// -r removes dependent snapshots too: a dataset delete through the
	// management API is an explicit teardown, and without -r any dataset
	// that was ever snapshotted becomes undeletable ("has children").
	cmd := fmt.Sprintf("sudo zfs destroy -r -f %s", datasetName)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSSnapshot creates a ZFS snapshot
func (c *Client) ZFSSnapshot(ctx context.Context, hosts []string, dataset, snapshotName string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zfs snapshot %s@%s", dataset, snapshotName)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSRollback rolls back to a ZFS snapshot
func (c *Client) ZFSRollback(ctx context.Context, hosts []string, dataset, snapshotName string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zfs rollback -r %s@%s", dataset, snapshotName)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSClone creates a clone from a snapshot
func (c *Client) ZFSClone(ctx context.Context, hosts []string, snapshot, clonePath string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zfs clone %s %s", snapshot, clonePath)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSListSnapshots lists ZFS snapshots for a dataset
func (c *Client) ZFSListSnapshots(ctx context.Context, hosts []string, dataset string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zfs list -t snapshot -o name,used,refer,creation -H %s", dataset)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSDestroySnapshot destroys a ZFS snapshot
func (c *Client) ZFSDestroySnapshot(ctx context.Context, hosts []string, snapshot string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zfs destroy -r %s", snapshot)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSSetQuota sets a quota on a ZFS dataset
func (c *Client) ZFSSetQuota(ctx context.Context, hosts []string, dataset, quota string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zfs set quota=%s %s", quota, dataset)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSSetReservation sets a reservation on a ZFS dataset
func (c *Client) ZFSSetReservation(ctx context.Context, hosts []string, dataset, reservation string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zfs set reservation=%s %s", reservation, dataset)
	return c.Exec(ctx, hosts, cmd)
}

// ZFSResizeVolume resizes a ZFS volume
func (c *Client) ZFSResizeVolume(ctx context.Context, hosts []string, volumePath, newSize string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo zfs set volsize=%s %s", newSize, volumePath)
	return c.Exec(ctx, hosts, cmd)
}

// ============ LVM Operations ============

// PVCreate creates physical volumes
func (c *Client) PVCreate(ctx context.Context, hosts []string, device string, opts ...LVMOption) (*ExecResult, error) {
	// Idempotent: a device that is already a PV (e.g. from a partially
	// completed earlier pool creation) is left alone instead of failing
	// the whole retry with "Can't initialize ... without -ff".
	cmd := fmt.Sprintf("sudo pvs %s >/dev/null 2>&1 || sudo pvcreate -y %s", device, device)
	return c.Exec(ctx, hosts, cmd)
}

// VGCreate creates volume groups
func (c *Client) VGCreate(ctx context.Context, hosts []string, vgName string, devices []string) (*ExecResult, error) {
	// Idempotent for retries: an existing VG with the target name is the
	// successful outcome of a previous attempt, not an error.
	cmd := fmt.Sprintf("sudo vgs %s >/dev/null 2>&1 || sudo vgcreate %s %s", vgName, vgName, strings.Join(devices, " "))
	return c.Exec(ctx, hosts, cmd)
}

// LVCreate creates logical volumes
func (c *Client) LVCreate(ctx context.Context, hosts []string, vgName, lvName, size string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo lvcreate -y -L %s -n %s %s", size, lvName, vgName)
	return c.Exec(ctx, hosts, cmd)
}

// LVCreateThinPool creates a thin pool logical volume
func (c *Client) LVCreateThinPool(ctx context.Context, hosts []string, vgName, poolName, size string) (*ExecResult, error) {
	// lvcreate only accepts absolute sizes with -L; percentage sizes like
	// "95%FREE" need the extents flag -l, otherwise creation fails with
	// "Invalid argument for --size".
	sizeFlag := "-L"
	if strings.Contains(size, "%") {
		sizeFlag = "-l"
	}
	cmd := fmt.Sprintf("sudo lvcreate -y %s %s -T %s/%s", sizeFlag, size, vgName, poolName)
	return c.Exec(ctx, hosts, cmd)
}

// LVCreateThinVolume creates a thin logical volume
func (c *Client) LVCreateThinVolume(ctx context.Context, hosts []string, vgName, poolName, lvName, size string) (*ExecResult, error) {
	// lvcreate -V <size> -T <vg>/<pool> -n <name>
	cmd := fmt.Sprintf("sudo lvcreate -y -V %s -T %s/%s -n %s", size, vgName, poolName, lvName)
	return c.Exec(ctx, hosts, cmd)
}

// LVRemove removes logical volumes
func (c *Client) LVRemove(ctx context.Context, hosts []string, lvPath string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo lvremove -f %s", lvPath)
	return c.Exec(ctx, hosts, cmd)
}

// LVCreateThinPoolAllFree creates a thin pool spanning every free extent in the
// volume group, with an explicit metadata area.
//
// LVM's default metadata size is generous for a pool holding a couple of
// volumes and far too small for one holding a snapshot history: a freshly
// converted 8 GiB pool with a single volume already showed 30% of the default
// area used. Exhausting metadata takes the whole pool read-only, which is a
// much worse failure than running out of data space, so the size is stated
// rather than inherited.
//
// The *data* size is not stated, deliberately. Asking for an exact byte count
// means reproducing LVM's allocator: the metadata area rounds up to an extent,
// a spare copy of it is allocated alongside, and the data area rounds up too —
// so "everything minus one metadata area" overshoots the group and lvcreate
// exits 5 with "Insufficient free space". `-l 100%FREE` asks for exactly the
// intent, "as large as the free extents allow", and leaves that arithmetic
// where the knowledge is.
func (c *Client) LVCreateThinPoolAllFree(ctx context.Context, hosts []string, vgName, poolName string, metadataBytes uint64) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo lvcreate -y -l 100%%FREE --poolmetadatasize %dB -T %s/%s",
		metadataBytes, vgName, poolName)
	return c.Exec(ctx, hosts, cmd)
}

// LVThinPoolIn returns the name of the thin pool in a volume group, or an
// empty string if the group has none.
//
// The name cannot be assumed: `pool create` builds "<pool>_thin", converting a
// thick pool in place builds something else, and a group adopted from
// elsewhere could use any name at all. Asking is one command and removes a
// whole class of "works on the nodes I tested" bug.
func (c *Client) LVThinPoolIn(ctx context.Context, host, vgName string) (string, error) {
	res, err := c.Exec(ctx, []string{host},
		fmt.Sprintf("sudo lvs --noheadings -o lv_name,segtype %s", vgName))
	if err != nil {
		return "", err
	}
	out, err := singleHostOutput(res, "list volumes in "+vgName)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == "thin-pool" {
			return f[0], nil
		}
	}
	return "", nil
}

// LVExists reports whether a logical volume is there at all.
//
// It distinguishes "no such volume" from "the query failed": lvs exits 5 both
// when the name is unknown and when LVM itself is unhappy, so the two are told
// apart by the message rather than the status. Only the volume being absent
// counts as absent — a missing *volume group* says `Volume group "x" not
// found`, and reporting that as a missing volume would turn a broken node into
// what looks like a half-finished conversion.
func (c *Client) LVExists(ctx context.Context, host, vgName, lvName string) (bool, error) {
	res, err := c.Exec(ctx, []string{host},
		fmt.Sprintf("sudo lvs --noheadings -o lv_name %s/%s", vgName, lvName))
	if err != nil {
		return false, err
	}
	for _, r := range res.Hosts {
		if r.Success {
			return true, nil
		}
		if strings.Contains(strings.ToLower(r.Output), "failed to find logical volume") {
			return false, nil
		}
		return false, fmt.Errorf("look for %s/%s: %s", vgName, lvName, strings.TrimSpace(r.Output))
	}
	return false, fmt.Errorf("look for %s/%s: no result", vgName, lvName)
}

// LVSizeBytes reports a logical volume's exact size.
//
// DRBD records the device size in its metadata, so a replica rebuilt from a
// rounded "6G" is a different device and refuses to attach. Every rebuild path
// has to carry bytes, never human sizes.
func (c *Client) LVSizeBytes(ctx context.Context, host, vgName, lvName string) (uint64, error) {
	res, err := c.Exec(ctx, []string{host},
		fmt.Sprintf("sudo lvs --noheadings --nosuffix --units b -o lv_size %s/%s", vgName, lvName))
	if err != nil {
		return 0, err
	}
	out, err := singleHostOutput(res, "read size of "+vgName+"/"+lvName)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("unexpected lvs output %q for %s/%s", out, vgName, lvName)
	}
	return n, nil
}

// VGFreeBytes reports a volume group's unallocated space.
func (c *Client) VGFreeBytes(ctx context.Context, host, vgName string) (uint64, error) {
	res, err := c.Exec(ctx, []string{host},
		fmt.Sprintf("sudo vgs --noheadings --nosuffix --units b -o vg_free %s", vgName))
	if err != nil {
		return 0, err
	}
	out, err := singleHostOutput(res, "read free space of "+vgName)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("unexpected vgs output %q for %s", out, vgName)
	}
	return n, nil
}

// DRBDDetach takes a node's local disk out of a resource, leaving it connected
// but diskless. The peers keep serving throughout.
func (c *Client) DRBDDetach(ctx context.Context, host, resource string) (*ExecResult, error) {
	return c.Exec(ctx, []string{host}, fmt.Sprintf("sudo drbdadm detach %s", resource))
}

// DRBDAttach puts a rebuilt backing device back into a resource, which starts a
// full resync from the peers.
func (c *Client) DRBDAttach(ctx context.Context, host, resource string) (*ExecResult, error) {
	return c.Exec(ctx, []string{host}, fmt.Sprintf("sudo drbdadm attach %s", resource))
}

// singleHostOutput unwraps a one-host ExecResult.
func singleHostOutput(res *ExecResult, what string) (string, error) {
	for _, hr := range res.Hosts {
		if !hr.Success {
			return "", fmt.Errorf("failed to %s: %s", what, strings.TrimSpace(hr.Output))
		}
		return hr.Output, nil
	}
	return "", fmt.Errorf("failed to %s: no result", what)
}

// LVCreateSnapshot creates a snapshot of a logical volume
func (c *Client) LVCreateSnapshot(ctx context.Context, hosts []string, vgName, lvName, snapshotName, size string) (*ExecResult, error) {
	lvPath := fmt.Sprintf("%s/%s", vgName, lvName)
	// Create snapshot volume
	cmd := fmt.Sprintf("sudo lvcreate -y -L %s -s -n %s %s", size, snapshotName, lvPath)
	return c.Exec(ctx, hosts, cmd)
}

// LVCreateThinSnapshot creates a snapshot of a thin logical volume
func (c *Client) LVCreateThinSnapshot(ctx context.Context, hosts []string, vgName, lvName, snapshotName string) (*ExecResult, error) {
	// Thin snapshots don't need size, they use the thin pool
	cmd := fmt.Sprintf("sudo lvcreate -s -n %s %s/%s", snapshotName, vgName, lvName)
	return c.Exec(ctx, hosts, cmd)
}

// LVIsThin checks if a logical volume is thin provisioned
func (c *Client) LVIsThin(ctx context.Context, host, vgName, lvName string) (bool, error) {
	// lvs -o segtype --noheadings
	cmd := fmt.Sprintf("sudo lvs -o segtype --noheadings %s/%s", vgName, lvName)
	result, err := c.Exec(ctx, []string{host}, cmd)
	if err != nil {
		return false, err
	}

	for _, r := range result.Hosts {
		if r.Success {
			segType := strings.TrimSpace(r.Output)
			if segType == "thin" {
				return true, nil
			}
		}
	}
	return false, nil
}

// LVRemoveSnapshot removes a snapshot volume
func (c *Client) LVRemoveSnapshot(ctx context.Context, hosts []string, vgName, snapshotName string) (*ExecResult, error) {
	snapPath := fmt.Sprintf("%s/%s", vgName, snapshotName)
	cmd := fmt.Sprintf("sudo lvremove -f %s", snapPath)
	return c.Exec(ctx, hosts, cmd)
}

// LVListSnapshots lists snapshots for a volume group
func (c *Client) LVListSnapshots(ctx context.Context, hosts []string, vgName string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo lvs -S lv_role=snapshot VG/LV -o lv_name,lv_size,lv_time --noheadings --separator=' ' %s", vgName)
	return c.Exec(ctx, hosts, cmd)
}

// LVMergeSnapshot merges a snapshot back into its origin volume
func (c *Client) LVMergeSnapshot(ctx context.Context, hosts []string, vgName, snapshotName string) (*ExecResult, error) {
	snapPath := fmt.Sprintf("%s/%s", vgName, snapshotName)
	cmd := fmt.Sprintf("sudo lvconvert --merge %s", snapPath)
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
	// Honor the force flag. A plain `drbdadm primary` refuses to promote when a
	// peer still holds Primary or is unreachable (no quorum) — this is the SAFE
	// default for graceful moves. `--force` overrides that and MUST only be used
	// once the caller has confirmed it is safe (e.g. this node holds DRBD
	// quorum); forcing blindly can create a dual-Primary split-brain.
	cmd := fmt.Sprintf("sudo drbdadm primary %s", resource)
	if force {
		cmd = fmt.Sprintf("sudo drbdadm primary --force %s", resource)
	}
	result, err := c.Exec(ctx, []string{host}, cmd)
	if err != nil {
		return nil, err
	}
	// Find result - the returned host key may differ (IP vs hostname)
	for _, r := range result.Hosts {
		return r, nil
	}
	return nil, fmt.Errorf("no result returned for host %s", host)
}

// DRBDSecondary sets resource to Secondary
func (c *Client) DRBDSecondary(ctx context.Context, host, resource string) (*HostResult, error) {
	result, err := c.Exec(ctx, []string{host}, fmt.Sprintf("sudo drbdadm secondary %s", resource))
	if err != nil {
		return nil, err
	}
	// Find result - the returned host key may differ (IP vs hostname)
	for _, r := range result.Hosts {
		return r, nil
	}
	return nil, fmt.Errorf("no result returned for host %s", host)
}

// DRBDCreateMD creates DRBD metadata with room for maxPeers peers.
//
// The peer count is not cosmetic: DRBD allocates one bitmap slot per peer when
// metadata is created and there is no way to add slots afterwards. Sizing to the
// peer count of the moment means the first node added later — an off-site DR, a
// third replica, a diskless client — fails with "Not enough free bitmap slots",
// and the only fix is to recreate metadata on every replica and resync. Passing
// a maxPeers of 0 uses the slot floor the volume was already sized for.
func (c *Client) DRBDCreateMD(ctx context.Context, hosts []string, resource string, maxPeers int) (*ExecResult, error) {
	if maxPeers <= 0 {
		maxPeers = DefaultMaxPeers
	}
	return c.Exec(ctx, hosts,
		fmt.Sprintf("sudo drbdadm create-md --max-peers=%d --force %s", maxPeers, resource))
}

// DefaultMaxPeers is the bitmap-slot count metadata is created with when the
// caller does not care. It matches the peer count backing volumes are sized for,
// so the slots always fit in the space already reserved.
const DefaultMaxPeers = 7

// DRBDAdjust adjusts DRBD configuration
func (c *Client) DRBDAdjust(ctx context.Context, hosts []string, resource string) (*ExecResult, error) {
	return c.Exec(ctx, hosts, fmt.Sprintf("sudo drbdadm adjust %s", resource))
}

// DRBDStatus gets DRBD resource status
func (c *Client) DRBDStatus(ctx context.Context, hosts []string, resource string) (*ExecResult, error) {
	cmd := fmt.Sprintf("sudo drbdadm status %s --verbose 2>/dev/null || sudo drbdadm status %s", resource, resource)
	return c.Exec(ctx, hosts, cmd)
}

// DRBDStatusJSON gets structured DRBD resource status via
// `drbdsetup status <res> --json`. The JSON is keyed by node name / node-id
// and carries per-peer replication state and resync completion (the `done`
// field), which the plain-text `drbdadm status` output does not expose in a
// machine-parseable way.
func (c *Client) DRBDStatusJSON(ctx context.Context, hosts []string, resource string) (*ExecResult, error) {
	return c.Exec(ctx, hosts, fmt.Sprintf("sudo drbdsetup status %s --json", resource))
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

// ============ Reactor Status ============

// ReactorStatus represents the status output from drbd-reactorctl status --json
type ReactorStatus struct {
	Promoter   []ReactorPromoterStatus `json:"promoter"`
	Prometheus []ReactorPluginStatus   `json:"prometheus"`
	Debugger   []ReactorPluginStatus   `json:"debugger"`
	UMH        []ReactorPluginStatus   `json:"umh"`
	AgentX     []ReactorPluginStatus   `json:"agentx"`
}

// ReactorPromoterStatus represents status of a promoter plugin
type ReactorPromoterStatus struct {
	DRBDResource string                 `json:"drbd_resource"`
	Path         string                 `json:"path"`
	PrimaryOn    string                 `json:"primary_on"`
	Target       ReactorServiceStatus   `json:"target"`
	Dependencies []ReactorServiceStatus `json:"dependencies"`
	Status       string                 `json:"status"`
}

// ReactorServiceStatus represents status of a systemd service
type ReactorServiceStatus struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Freezer string `json:"freezer"`
}

// ReactorPluginStatus represents status of a generic reactor plugin
type ReactorPluginStatus struct {
	Path    string `json:"path"`
	Address string `json:"address,omitempty"`
	Status  string `json:"status"`
}

// ReactorStatusJSON gets the reactor status using JSON output
func (c *Client) ReactorStatusJSON(ctx context.Context, host string) (*ReactorStatus, error) {
	result, err := c.Exec(ctx, []string{host}, "sudo drbd-reactorctl status --json")
	if err != nil {
		return nil, fmt.Errorf("failed to get reactor status: %w", err)
	}

	for _, r := range result.Hosts {
		if !r.Success {
			return nil, fmt.Errorf("reactor status command failed: %s", r.Output)
		}

		var status ReactorStatus
		if err := jsonUnmarshal([]byte(r.Output), &status); err != nil {
			return nil, fmt.Errorf("failed to parse reactor status JSON: %w", err)
		}
		return &status, nil
	}

	return nil, fmt.Errorf("no result from reactor status command")
}

// ReactorPromoterStatusByResource gets the promoter status for a specific DRBD resource
func (c *Client) ReactorPromoterStatusByResource(ctx context.Context, host, resource string) (*ReactorPromoterStatus, error) {
	status, err := c.ReactorStatusJSON(ctx, host)
	if err != nil {
		return nil, err
	}

	for _, promoter := range status.Promoter {
		if promoter.DRBDResource == resource {
			return &promoter, nil
		}
	}

	return nil, fmt.Errorf("promoter status for resource %s not found", resource)
}

// jsonUnmarshal is a local wrapper for json.Unmarshal
func jsonUnmarshal(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
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

// FailedHosts returns the hosts the config did not reach.
func (r *ConfigResult) FailedHosts() []string {
	return hostResultFailures(r.Hosts)
}

// FailureDetails renders failed hosts with the reason each one gave. See
// ExecResult.FailureDetails.
func (r *ConfigResult) FailureDetails() string {
	return hostResultDetails(r.Hosts)
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
// combineStreams joins a command's stdout and stderr into the single Output
// field callers inspect, keeping stdout first and skipping empty streams so the
// common (successful, silent) case stays an empty string rather than a newline.
func combineStreams(stdout, stderr string) string {
	out := strings.TrimRight(stdout, "\n")
	errOut := strings.TrimRight(stderr, "\n")
	switch {
	case out == "":
		return errOut
	case errOut == "":
		return out
	default:
		return out + "\n" + errOut
	}
}

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
	return hostResultFailures(r.Hosts)
}

func hostResultFailures(hosts map[string]*HostResult) []string {
	var failed []string
	for host, h := range hosts {
		if h != nil && !h.Success {
			failed = append(failed, host)
		}
	}
	return failed
}

func hostResultDetails(hosts map[string]*HostResult) string {
	failed := hostResultFailures(hosts)
	if len(failed) == 0 {
		return ""
	}
	sort.Strings(failed)

	parts := make([]string, 0, len(failed))
	for _, host := range failed {
		h := hosts[host]
		reason := ""
		if h != nil {
			reason = strings.TrimSpace(h.Output)
			if reason == "" && h.Error != nil {
				reason = strings.TrimSpace(h.Error.Error())
			}
		}
		if reason == "" {
			reason = "no output"
		}
		// Keep it to one line per host so the error stays greppable.
		reason = strings.Join(strings.Fields(reason), " ")
		parts = append(parts, fmt.Sprintf("%s: %s", host, reason))
	}
	return strings.Join(parts, "; ")
}

// FailureDetails renders the failed hosts *with what the command actually
// said*, e.g.
//
//	192.168.1.10: Device '/dev/sdb' not found; 192.168.1.11: already exists
//
// Error strings built from FailedHosts() alone ("failed on hosts: [10.0.0.1
// 10.0.0.2]") force whoever hit the failure to go SSH into the nodes and replay
// the command by hand, because the reason drbdadm/lvcreate/zfs printed is
// dropped on the floor. Prefer this in user-facing errors; hosts are sorted so
// the message is stable across runs.
func (r *ExecResult) FailureDetails() string {
	return hostResultDetails(r.Hosts)
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

// ZFSOption configures ZFS operations
type ZFSOption func(*zfsOptions)

type zfsOptions struct {
	compression bool
	dedup       bool
}

// WithZFSCompression enables compression for ZFS
func WithZFSCompression(compression bool) ZFSOption {
	return func(o *zfsOptions) {
		o.compression = compression
	}
}

// WithZFSDedup enables dedup for ZFS
func WithZFSDedup(dedup bool) ZFSOption {
	return func(o *zfsOptions) {
		o.dedup = dedup
	}
}
