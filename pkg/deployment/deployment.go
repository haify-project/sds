// Package deployment handles the execution of commands on storage nodes
// using the dispatch SSH library.
package deployment

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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

// New creates a new deployment Client
func New(logger *zap.Logger) (*Client, error) {
	// Create dispatch client with default config
	// It will automatically look for ~/.dispatch/config.toml
	client, err := dispatch.New(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create dispatch client: %w", err)
	}

	return &Client{
		dispatch: client,
		logger:   logger,
		parallel: 10, // Default parallelism
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

			// Use base64 encoding to safely transfer content
			encodedContent := fmt.Sprintf("echo %s | base64 -d | sudo tee %s > /dev/null",
				fmt.Sprintf("%q", base64.StdEncoding.EncodeToString(fileContent)), remotePath)
			copyResult, err := c.Exec(ctx, []string{host}, encodedContent)
			if err != nil {
				c.logger.Error("Failed to copy config", zap.String("host", host), zap.Error(err))
				configResult.Hosts[host] = &HostResult{
					Host:    host,
					Success: false,
					Error:   err,
				}
				configResult.Success = false
				continue
			}
			if !copyResult.AllSuccess() {
				configResult.Hosts[host] = &HostResult{
					Host:    host,
					Success: false,
					Error:   fmt.Errorf("copy failed"),
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

// availableHostKeys returns the keys from the copy result hosts map for debugging
func availableHostKeys(hosts map[string]*dispatch.CopyHostResult) []string {
	var keys []string
	for k := range hosts {
		keys = append(keys, k)
	}
	return keys
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
			Host:     host,
			Output:   output,
			StartTime: start,
			EndTime:   end,
			Duration: end.Sub(start),
			ExitCode: exitCode,
			ErrorMsg: errorMsg,
			Success:  exitCode == 0 && errorMsg == nil,
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
		c.logger.Debug("deployment.Exec result",
			zap.String("host", host),
			zap.Bool("success", r.Success),
			zap.Int("exit_code", r.ExitCode),
			zap.String("error_msg", fmt.Sprintf("%v", r.ErrorMsg)),
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

// ============ ZFS Operations ============

// ZFSCreatePool creates a ZFS pool
func (c *Client) ZFSCreatePool(ctx context.Context, hosts []string, poolName string, vdevs []string, opts ...ZFSOption) (*ExecResult, error) {
	options := &zfsOptions{}
	for _, opt := range opts {
		opt(options)
	}

	cmd := fmt.Sprintf("sudo zpool create -f %s %s", poolName, strings.Join(vdevs, " "))
	if options.thin {
		cmd = fmt.Sprintf("sudo zpool create -f -o thinpool=%s %s %s", poolName+"/data", poolName, strings.Join(vdevs, " "))
	}
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
	cmd := fmt.Sprintf("sudo zfs destroy -f %s", datasetName)
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

// LVCreateThinPool creates a thin pool logical volume
func (c *Client) LVCreateThinPool(ctx context.Context, hosts []string, vgName, poolName, size string) (*ExecResult, error) {
	// lvcreate -L <size> -T <vg>/<pool>
	cmd := fmt.Sprintf("sudo lvcreate -y -L %s -T %s/%s", size, vgName, poolName)
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
	cmd := fmt.Sprintf("sudo drbdadm primary --force %s", resource)
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

// DRBDCreateMD creates DRBD metadata
func (c *Client) DRBDCreateMD(ctx context.Context, hosts []string, resource string) (*ExecResult, error) {
	return c.Exec(ctx, hosts, fmt.Sprintf("sudo drbdadm create-md --force %s", resource))
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

// ZFSOption configures ZFS operations
type ZFSOption func(*zfsOptions)

type zfsOptions struct {
	thin       bool
	compression bool
	dedup      bool
}

// WithZFSThin enables thin provisioning for ZFS
func WithZFSThin(thin bool) ZFSOption {
	return func(o *zfsOptions) {
		o.thin = thin
	}
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
