// Package gateway provides DRBD-based storage gateway functionality
// using drbd-reactor for HA/failover with NFS, iSCSI, and NVMe-oF protocols.
package gateway

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	v1 "github.com/liliang-cn/sds/api/proto/v1"
	"go.uber.org/zap"
)

const (
	// DrbdReactorConfigDir is the directory for drbd-reactor configuration snippets
	DrbdReactorConfigDir = "/etc/drbd-reactor.d"

	// Default ports
	DefaultISCSIPort = 3260
	DefaultNFSPort   = 2049
	DefaultNVMePort  = 4420

	// Default filesystem types
	DefaultFSType = "ext4"

	// Default export base path
	DefaultExportBasePath = "/srv/gateway-exports"

	// Default cluster private mount path
	DefaultClusterPrivateMountPath = "/var/lib/sds"
)

// ResourceVolumeInfo represents a DRBD volume
type ResourceVolumeInfo struct {
	VolumeID uint32
	Device   string
	SizeGB   uint64
}

// ResourceInfo represents DRBD resource information
type ResourceInfo struct {
	Name       string
	Port       uint32
	Protocol   string
	Nodes      []string
	Role       string
	Volumes    []*ResourceVolumeInfo
	NodeStates map[string]*ResourceNodeState
}

// ResourceNodeState represents node state for a resource
type ResourceNodeState struct {
	Role        string
	DiskState   string
	Replication string
}

// ResourceManager provides access to DRBD resource operations
type ResourceManager interface {
	GetResource(ctx context.Context, name string) (*ResourceInfo, error)
	SetPrimary(ctx context.Context, resource, node string, force bool) error
}

// DeploymentClient provides deployment operations
type DeploymentClient interface {
	DistributeConfig(ctx context.Context, hosts []string, content, remotePath string) error
	Exec(ctx context.Context, hosts []string, cmd string) error
}

// Manager handles gateway operations
type Manager struct {
	resources  ResourceManager
	deployment DeploymentClient
	logger     *zap.Logger
	hosts      []string
}

// New creates a new gateway manager
func New(resources ResourceManager, deployment DeploymentClient, logger *zap.Logger, hosts []string) *Manager {
	return &Manager{
		resources:  resources,
		deployment: deployment,
		logger:     logger,
		hosts:      append([]string(nil), hosts...),
	}
}

func (m *Manager) SetHosts(hosts []string) {
	m.hosts = append([]string(nil), hosts...)
}

func (m *Manager) Hosts() []string {
	return append([]string(nil), m.hosts...)
}

// GatewayInfo represents gateway information
type GatewayInfo struct {
	ID       string
	Name     string
	Type     string
	Resource string
	// State is "stopped" when the reactor config is disabled; empty means
	// reactor-managed (live status comes from drbd-reactorctl).
	State string
}

// ServiceIP represents a service IP with CIDR notation
type ServiceIP struct {
	IP     net.IP
	Prefix int
}

// CreateGatewayRequest is a common interface for all gateway creation requests
type CreateGatewayRequest interface {
	GetResource() string
	GetServiceIP() string
}

// ==================== Common Operations ====================

// ListGateways lists all storage gateways (NFS, iSCSI, NVMe-oF) by scanning drbd-reactor config directory
// HA/service gateways are filtered out from this list
func (m *Manager) ListGateways(ctx context.Context) ([]*GatewayInfo, error) {
	files, err := os.ReadDir(DrbdReactorConfigDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read config directory: %w", err)
	}

	// Storage gateway types only (exclude HA and service types)
	storageTypes := map[string]bool{
		"nfs":    true,
		"iscsi":  true,
		"nvmeof": true,
	}

	var gateways []*GatewayInfo
	for _, file := range files {
		name := file.Name()
		// A stopped gateway keeps its config as .toml.disabled so reactor
		// no longer manages it but the definition (and the VIP/port info
		// deletion relies on) is preserved.
		disabled := false
		if strings.HasSuffix(name, ".toml.disabled") {
			disabled = true
			name = strings.TrimSuffix(name, ".disabled")
		}
		if strings.HasPrefix(name, "sds-") && strings.HasSuffix(name, ".toml") {
			// Parse gateway type and name from filename
			// Format: sds-<type>-<resource>.toml
			parts := strings.TrimPrefix(name, "sds-")
			parts = strings.TrimSuffix(parts, ".toml")
			typeParts := strings.SplitN(parts, "-", 2)

			if len(typeParts) == 2 {
				gwType := typeParts[0]
				resource := typeParts[1]

				// Only include storage gateway types
				if storageTypes[gwType] {
					state := ""
					if disabled {
						state = "stopped"
					}
					gateways = append(gateways, &GatewayInfo{
						ID:       resource,
						Name:     resource,
						Type:     gwType,
						Resource: resource,
						State:    state,
					})
				}
			}
		}
	}

	return gateways, nil
}

// DeleteGateway deletes a gateway configuration
func (m *Manager) DeleteGateway(ctx context.Context, id string) error {
	m.logger.Info("Deleting gateway", zap.String("id", id))

	// Stop drbd-reactor services and remove config on all nodes
	for _, host := range m.hosts {
		m.logger.Info("Stopping gateway services on node",
			zap.String("node", host),
			zap.String("gateway", id))

		// 1. Stop drbd-reactor services for this gateway
		escapedID := strings.ReplaceAll(id, "-", "\\x2d")
		stopCmd := fmt.Sprintf("systemctl stop drbd-services@%s.target 2>/dev/null || true", escapedID)
		if err := m.deployment.Exec(ctx, []string{host}, stopCmd); err != nil {
			m.logger.Warn("Failed to stop gateway services", zap.String("host", host), zap.Error(err))
		}

		// 2. Flush leftover portblock DROP rules BEFORE removing the configs
		// (the config is the only place the VIP/port pair is recorded).
		m.flushPortblockRules(ctx, []string{host}, id)

		// 3. Delete reactor config files (all types: nfs, iscsi, nvmeof)
		configFiles := []string{
			fmt.Sprintf("sds-nfs-%s.toml", id),
			fmt.Sprintf("sds-iscsi-%s.toml", id),
			fmt.Sprintf("sds-nvmeof-%s.toml", id),
		}

		for _, configFile := range configFiles {
			configPath := filepath.Join(DrbdReactorConfigDir, configFile)
			// A stopped gateway keeps its config as .toml.disabled.
			rmCmd := fmt.Sprintf("sudo rm -f %s %s.disabled", configPath, configPath)
			if err := m.deployment.Exec(ctx, []string{host}, rmCmd); err != nil {
				m.logger.Warn("Failed to remove config file", zap.String("host", host), zap.String("file", configPath), zap.Error(err))
			}
		}

		// 4. Reload drbd-reactor to pick up changes
		if err := m.deployment.Exec(ctx, []string{host}, "sudo systemctl reload drbd-reactor || sudo systemctl restart drbd-reactor"); err != nil {
			m.logger.Warn("Failed to reload drbd-reactor", zap.String("host", host), zap.Error(err))
		}
	}

	m.logger.Info("Gateway deleted successfully", zap.String("id", id))
	return nil
}

// GetGateway retrieves gateway information
func (m *Manager) GetGateway(ctx context.Context, id string) (*GatewayInfo, error) {
	gateways, err := m.ListGateways(ctx)
	if err != nil {
		return nil, err
	}

	for _, gw := range gateways {
		if gw.ID == id {
			return gw, nil
		}
	}

	return nil, fmt.Errorf("gateway not found: %s", id)
}

// ==================== Helpers ====================

// parseServiceIP parses a service IP with CIDR notation
func parseServiceIP(serviceIP string) (*ServiceIP, error) {
	ip, ipNet, err := net.ParseCIDR(serviceIP)
	if err != nil {
		return nil, fmt.Errorf("failed to parse service IP %s: %w", serviceIP, err)
	}

	prefix, _ := ipNet.Mask.Size()

	return &ServiceIP{
		IP:     ip,
		Prefix: prefix,
	}, nil
}

// extractNodeName extracts node name from endpoint (e.g., "orange1:50051" -> "orange1")
func extractNodeName(endpoint string) string {
	parts := strings.Split(endpoint, ":")
	if len(parts) > 0 {
		return parts[0]
	}
	return endpoint
}

// writeReactorConfig writes drbd-reactor configuration to all nodes
func (m *Manager) writeReactorConfig(ctx context.Context, resource, pluginID, config string) error {
	remotePath := filepath.Join(DrbdReactorConfigDir, fmt.Sprintf("%s.toml", pluginID))

	m.logger.Debug("Writing reactor config to all nodes",
		zap.Strings("hosts", m.hosts),
		zap.String("path", remotePath))

	if err := m.deployment.DistributeConfig(ctx, m.hosts, config, remotePath); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	// Reload drbd-reactor on all nodes
	reloadCmd := "sudo systemctl reload drbd-reactor || sudo systemctl restart drbd-reactor"
	if err := m.deployment.Exec(ctx, m.hosts, reloadCmd); err != nil {
		m.logger.Warn("Failed to reload drbd-reactor", zap.Error(err))
	}

	return nil
}

// ensureGatewayPrerequisites promotes the resource on one of ITS OWN nodes
// and makes sure the cluster-private volume (volume 0) carries a filesystem;
// the promoter's Filesystem agent only mounts, it never formats. mkfs runs
// only when blkid finds no existing filesystem, so the call is idempotent
// and never destroys data.
func (m *Manager) ensureGatewayPrerequisites(ctx context.Context, resource string, nodes []string, devices ...string) error {
	if len(nodes) == 0 {
		return fmt.Errorf("resource %s has no nodes", resource)
	}
	node := nodes[0]
	if err := m.resources.SetPrimary(ctx, resource, node, false); err != nil {
		return fmt.Errorf("failed to promote %s on %s: %w", resource, node, err)
	}
	// A freshly promoted resource can lose Primary for a moment when a
	// previous reactor teardown is still settling, which makes mkfs race a
	// demote. Retry briefly instead of failing the whole gateway creation.
	for _, device := range devices {
		cmd := fmt.Sprintf("sudo blkid %s >/dev/null 2>&1 || sudo mkfs.ext4 -q %s", device, device)
		var lastErr error
		formatted := false
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				time.Sleep(2 * time.Second)
				if err := m.resources.SetPrimary(ctx, resource, node, false); err != nil {
					m.logger.Warn("Re-promote before mkfs retry failed",
						zap.String("resource", resource), zap.Error(err))
				}
			}
			if lastErr = m.deployment.Exec(ctx, []string{node}, cmd); lastErr == nil {
				formatted = true
				break
			}
		}
		if !formatted {
			return fmt.Errorf("failed to prepare filesystem on %s: %w", device, lastErr)
		}
	}
	return nil
}

// getDRBDDevice gets the DRBD device path for a resource
func (m *Manager) getDRBDDevice(ctx context.Context, resource string) (string, error) {
	// Try to get device from resource info
	resInfo, err := m.resources.GetResource(ctx, resource)
	if err == nil && len(resInfo.Volumes) > 0 && resInfo.Volumes[0].Device != "" {
		return resInfo.Volumes[0].Device, nil
	}

	// Fallback: read from DRBD config file
	configPath := fmt.Sprintf("/etc/drbd.d/%s.res", resource)
	content, err := os.ReadFile(configPath)
	if err == nil {
		deviceMinor := parseDeviceMinorFromConfig(string(content))
		if deviceMinor >= 0 {
			return fmt.Sprintf("/dev/drbd%d", deviceMinor), nil
		}
	}

	// Final fallback
	return "/dev/drbd0", nil
}

// parseDeviceMinorFromConfig extracts device minor number from DRBD config content
func parseDeviceMinorFromConfig(configContent string) int {
	lines := strings.Split(configContent, "\n")
	inVolumeBlock := false
	var deviceMinor string

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.Contains(line, "volume") && strings.Contains(line, "{") {
			inVolumeBlock = true
			continue
		}

		if line == "}" {
			if inVolumeBlock && deviceMinor != "" {
				// Found it
				var minor int
				if _, err := fmt.Sscanf(deviceMinor, "%d", &minor); err != nil {
					return -1
				}
				return minor
			}
			inVolumeBlock = false
			deviceMinor = ""
			continue
		}

		if inVolumeBlock && strings.Contains(line, "device") && strings.Contains(line, "minor") {
			parts := strings.Fields(line)
			for i, part := range parts {
				if part == "minor" && i+1 < len(parts) {
					deviceMinor = strings.TrimSuffix(parts[i+1], ";")
				}
			}
		}
	}

	return -1
}

// volumeDevice returns the device path for a volume, preferring the real
// device reported by the resource manager. The minor-arithmetic fallback
// (base minor + volume number) only holds when minors happen to be
// consecutive, which global minor allocation does not guarantee.
func volumeDevice(volumes []*ResourceVolumeInfo, baseDevice string, volumeNumber int) string {
	for _, v := range volumes {
		if int(v.VolumeID) == volumeNumber && v.Device != "" {
			return v.Device
		}
	}
	return getDRBDDeviceForVolume(baseDevice, volumeNumber)
}

// getDRBDDeviceForVolume returns the DRBD device path for a specific volume number
// Volume 0 uses the base device, volume N uses base minor + N
func getDRBDDeviceForVolume(baseDevice string, volumeNumber int) string {
	if volumeNumber == 0 {
		return baseDevice
	}
	// Extract minor number from base device (e.g., /dev/drbd0 -> 0)
	minor := 0
	if strings.HasPrefix(baseDevice, "/dev/drbd") {
		if _, err := fmt.Sscanf(baseDevice, "/dev/drbd%d", &minor); err != nil {
			// If parsing fails, return the base device
			return baseDevice
		}
	}
	return fmt.Sprintf("/dev/drbd%d", minor+volumeNumber)
}

// generateUUID generates a proper UUID v4 for use in configurations
func generateUUID() string {
	// Generate 16 random bytes
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback to MD5-based UUID if rand.Read fails
		hash := md5.Sum([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
			hash[:4], hash[4:6], hash[6:8], hash[8:10], hash[10:16])
	}

	// Set version 4 (random UUID) and variant
	b[6] = (b[6] & 0x0F) | 0x40
	b[8] = (b[8] & 0x3F) | 0x80

	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]))
}

// generateSerialFromIQN generates a unique serial from IQN and volume number
// Matches linstor-gateway behavior for iSCSI LUNs
func generateSerialFromIQN(iqn string, volumeNumber int) string {
	hash := md5.Sum([]byte(fmt.Sprintf("%s-%d", iqn, volumeNumber)))
	return hex.EncodeToString(hash[:8])
}

// generateFSID generates a proper filesystem ID as UUID (matches linstor-gateway)
func generateFSID(resourceUUID, volumeUUID string) string {
	// SHA1 hash of resource UUID + volume UUID to create unique FSID
	hash := md5.Sum([]byte(resourceUUID + ":" + volumeUUID))
	uuid := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		hash[:4], hash[4:6], hash[6:8], hash[8:10], hash[10:16])
	return uuid
}

// executeTemplate executes a template with the given data
func executeTemplate(tmplStr string, data interface{}) (string, error) {
	t, err := template.New("gateway").Parse(tmplStr)
	if err != nil {
		return "", err
	}

	var result strings.Builder
	if err := t.Execute(&result, data); err != nil {
		return "", err
	}

	return result.String(), nil
}

// ==================== gRPC Method Implementations ====================

// CreateNFSGateway creates an NFS gateway
func (m *Manager) CreateNFSGateway(ctx context.Context, req *v1.CreateNFSGatewayRequest) (*v1.CreateNFSGatewayResponse, error) {
	return &v1.CreateNFSGatewayResponse{
		Success: false,
		Message: "Use gateway/nfs package directly",
	}, nil
}

// CreateISCSIGateway creates an iSCSI gateway
func (m *Manager) CreateISCSIGateway(ctx context.Context, req *v1.CreateISCSIGatewayRequest) (*v1.CreateISCSIGatewayResponse, error) {
	return &v1.CreateISCSIGatewayResponse{
		Success: false,
		Message: "Use gateway/iscsi package directly",
	}, nil
}

// CreateNVMeGateway creates an NVMe-oF gateway
func (m *Manager) CreateNVMeGateway(ctx context.Context, req *v1.CreateNVMeGatewayRequest) (*v1.CreateNVMeGatewayResponse, error) {
	return &v1.CreateNVMeGatewayResponse{
		Success: false,
		Message: "Use gateway/nvmeof package directly",
	}, nil
}

// StartGateway starts a stopped gateway by re-enabling its reactor config
// (.toml.disabled -> .toml) and reloading drbd-reactor, which then promotes
// and starts the service chain on the best node.
func (m *Manager) StartGateway(ctx context.Context, id string) error {
	enableScript := fmt.Sprintf(`for f in /etc/drbd-reactor.d/sds-nfs-%s.toml /etc/drbd-reactor.d/sds-iscsi-%s.toml /etc/drbd-reactor.d/sds-nvmeof-%s.toml; do
  [ -f "$f.disabled" ] && mv "$f.disabled" "$f"
done
true`, id, id, id)
	if err := m.runScript(ctx, m.hosts, enableScript); err != nil {
		return fmt.Errorf("failed to re-enable gateway config: %w", err)
	}
	return m.reloadDrbdReactor(ctx)
}

// StopGateway stops a gateway. Simply stopping the systemd target is not
// enough: drbd-reactor sees the resource may promote again and restarts the
// whole chain within seconds. Mirroring `drbd-reactorctl disable`, the
// reactor config is renamed to .toml.disabled first so reactor drops the
// resource, then the target is stopped for real.
func (m *Manager) StopGateway(ctx context.Context, id string) error {
	disableScript := fmt.Sprintf(`for f in /etc/drbd-reactor.d/sds-nfs-%s.toml /etc/drbd-reactor.d/sds-iscsi-%s.toml /etc/drbd-reactor.d/sds-nvmeof-%s.toml; do
  [ -f "$f" ] && mv "$f" "$f.disabled"
done
true`, id, id, id)
	if err := m.runScript(ctx, m.hosts, disableScript); err != nil {
		return fmt.Errorf("failed to disable gateway config: %w", err)
	}
	if err := m.reloadDrbdReactor(ctx); err != nil {
		return err
	}

	escapedID := strings.ReplaceAll(id, "-", "\\x2d")
	stopCmd := fmt.Sprintf("systemctl stop drbd-services@%s.target 2>/dev/null || true", escapedID)
	if err := m.deployment.Exec(ctx, m.hosts, stopCmd); err != nil {
		return err
	}
	// With reactor management gone and the target down, any DROP rule left
	// for the gateway's VIP/port is stale residue from reload/failure
	// loops; flush it so a later start isn't silently firewalled.
	m.flushPortblockRules(ctx, m.hosts, id)
	return nil
}

// runScript executes a shell script on hosts. Dispatch wraps commands in
// sh -c "..." (double quotes), so $variables are expanded by the OUTER
// shell — i.e. silently emptied — before the script runs. Base64-encoding
// the script makes it immune to that quoting chain.
func (m *Manager) runScript(ctx context.Context, hosts []string, script string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	cmd := fmt.Sprintf("echo %s | base64 -d | sudo /bin/sh", encoded)
	return m.deployment.Exec(ctx, hosts, cmd)
}

// flushPortblockRules removes accumulated portblock DROP rules for the
// gateway's VIP/port on the given hosts. The OCF portblock/portunblock pair
// is not symmetric across drbd-reactor reloads and failed start loops, so
// rules pile up and leave a port firewalled while every service shows
// healthy. The VIP/port pair is extracted from the gateway's reactor config,
// which must therefore still exist when this is called. Best-effort: a flush
// failure is logged, never fatal.
func (m *Manager) flushPortblockRules(ctx context.Context, hosts []string, id string) {
	script := fmt.Sprintf(`for b in /etc/drbd-reactor.d/sds-nfs-%s.toml /etc/drbd-reactor.d/sds-iscsi-%s.toml /etc/drbd-reactor.d/sds-nvmeof-%s.toml; do
  f=$b; [ -f "$f" ] || f=$b.disabled; [ -f "$f" ] || continue
  ip=$(grep -oE 'ip=[0-9.]+' "$f" | head -1 | cut -d= -f2)
  port=$(grep -oE 'portno=[0-9]+' "$f" | head -1 | cut -d= -f2)
  [ -n "$ip" ] && [ -n "$port" ] || continue
  while iptables -D INPUT -d "$ip" -p tcp -m multiport --dports "$port" -j DROP 2>/dev/null; do :; done
done
true`, id, id, id)
	if err := m.runScript(ctx, hosts, script); err != nil {
		m.logger.Warn("Failed to flush leftover portblock rules",
			zap.String("gateway", id), zap.Error(err))
	}
}

// reloadDrbdReactor reloads drbd-reactor configuration
func (m *Manager) reloadDrbdReactor(ctx context.Context) error {
	reloadCmd := "sudo systemctl reload drbd-reactor || sudo systemctl restart drbd-reactor"
	return m.deployment.Exec(ctx, m.hosts, reloadCmd)
}
