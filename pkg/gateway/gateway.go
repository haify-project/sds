// Package gateway provides DRBD-based storage gateway functionality
// using drbd-reactor for HA/failover with NFS, iSCSI, and NVMe-oF protocols.
package gateway

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"text/template"

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
	// BackingVolume is the logical volume name inside the pool, e.g.
	// "<resource>_data" or "<resource>_state1". It is how a gateway tells its
	// own scratch volume apart from the one the operator asked to export —
	// see clusterPrivateAndPayload.
	BackingVolume string
}

// ResourceInfo represents DRBD resource information
type ResourceInfo struct {
	Name     string
	Port     uint32
	Protocol string
	Nodes    []string
	// Hosts are the managed hosts of the diskful replicas: the only machines
	// a gateway's promoter may run on. Empty when the controller cannot say,
	// in which case Nodes is used.
	Hosts      []string
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
	// EnsureGatewayVolumes makes sure a resource has at least minVolumes
	// volumes, auto-provisioning the small cluster-private state volume(s) a
	// gateway needs. A no-op when the resource already qualifies or when
	// auto-provisioning is disabled (in which case the caller's own volume
	// check still reports a clear error).
	EnsureGatewayVolumes(ctx context.Context, resource string, minVolumes int) error
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
