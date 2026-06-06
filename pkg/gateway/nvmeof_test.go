package gateway

import (
	"context"
	"strings"
	"testing"

	v1 "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestNewNVMeManager(t *testing.T) {
	logger := zap.NewNop()
	mockResources := &MockResourceManager{}
	mockDeployment := &MockDeploymentClient{}
	hosts := []string{"node1", "node2"}

	baseManager := New(mockResources, mockDeployment, logger, hosts)
	nvmeManager := NewNVMeManager(baseManager)

	require.NotNil(t, nvmeManager)
	assert.Equal(t, baseManager, nvmeManager.Manager)
}

func TestGenerateNVMeGatewayConfig(t *testing.T) {
	logger := zap.NewNop()
	mockResources := &MockResourceManager{
		Resources: map[string]*ResourceInfo{
			"data": {
				Name: "data",
				Volumes: []*ResourceVolumeInfo{
					{VolumeID: 0, Device: "/dev/drbd0", SizeGB: 100},
					{VolumeID: 1, Device: "/dev/drbd1", SizeGB: 100},
				},
			},
		},
	}
	mockDeployment := &MockDeploymentClient{}
	hosts := []string{"node1", "node2"}

	baseManager := New(mockResources, mockDeployment, logger, hosts)
	nvmeManager := NewNVMeManager(baseManager)

	req := &v1.CreateNVMeGatewayRequest{
		Resource:      "data",
		Nqn:           "nqn.2024-01.com.example:sds.data",
		ServiceIp:     "192.168.1.150/24",
		TransportType: "tcp",
	}

	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)

	config, err := nvmeManager.generateNVMeGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)
	require.NotEmpty(t, config)

	// Verify config contains expected elements
	assert.Contains(t, config, "[[promoter]]")
	assert.Contains(t, config, "promoter.resources")
	assert.Contains(t, config, req.Nqn)
	assert.Contains(t, config, "ocf:heartbeat:IPaddr2")
	assert.Contains(t, config, "ocf:heartbeat:nvmet-subsystem")
	assert.Contains(t, config, "ocf:heartbeat:nvmet-namespace")
	assert.Contains(t, config, "ocf:heartbeat:nvmet-port")
	assert.Contains(t, config, "ocf:heartbeat:Filesystem")
	assert.Contains(t, config, "ocf:heartbeat:portblock")
	assert.Contains(t, config, "tcp")  // Transport type
	assert.Contains(t, config, "4420") // Default NVMe port
}

func TestGenerateNVMeGatewayConfigMultipleNamespaces(t *testing.T) {
	logger := zap.NewNop()
	mockResources := &MockResourceManager{
		Resources: map[string]*ResourceInfo{
			"data": {
				Name: "data",
				Volumes: []*ResourceVolumeInfo{
					{VolumeID: 0, Device: "/dev/drbd0", SizeGB: 100},
					{VolumeID: 1, Device: "/dev/drbd1", SizeGB: 100},
					{VolumeID: 2, Device: "/dev/drbd2", SizeGB: 200},
					{VolumeID: 3, Device: "/dev/drbd3", SizeGB: 300},
				},
			},
		},
	}
	mockDeployment := &MockDeploymentClient{}
	hosts := []string{"node1"}

	baseManager := New(mockResources, mockDeployment, logger, hosts)
	nvmeManager := NewNVMeManager(baseManager)

	req := &v1.CreateNVMeGatewayRequest{
		Resource:      "data",
		Nqn:           "nqn.2024-01.com.example:sds.multi-ns",
		ServiceIp:     "10.0.0.50/24",
		TransportType: "tcp",
	}

	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)

	config, err := nvmeManager.generateNVMeGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(4))
	require.NoError(t, err)

	// Only volumes 1+ are exposed as namespaces.
	nsCount := strings.Count(config, "nvmet-namespace")
	assert.Equal(t, 3, nsCount)
	assert.NotContains(t, config, "namespace_id=0")
	assert.Contains(t, config, "namespace_id=1")
	assert.Contains(t, config, "namespace_id=2")
	assert.Contains(t, config, "namespace_id=3")
}

func TestGenerateNVMeGatewayConfigRDMA(t *testing.T) {
	logger := zap.NewNop()
	mockResources := &MockResourceManager{
		Resources: map[string]*ResourceInfo{
			"data": {
				Name: "data",
				Volumes: []*ResourceVolumeInfo{
					{VolumeID: 0, Device: "/dev/drbd0", SizeGB: 100},
					{VolumeID: 1, Device: "/dev/drbd1", SizeGB: 100},
				},
			},
		},
	}
	mockDeployment := &MockDeploymentClient{}
	hosts := []string{"node1"}

	baseManager := New(mockResources, mockDeployment, logger, hosts)
	nvmeManager := NewNVMeManager(baseManager)

	req := &v1.CreateNVMeGatewayRequest{
		Resource:      "data",
		Nqn:           "nqn.2024-01.com.example:sds.rdma",
		ServiceIp:     "192.168.1.150/24",
		TransportType: "rdma",
	}

	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)

	config, err := nvmeManager.generateNVMeGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)

	// Verify RDMA transport type is used
	assert.Contains(t, config, "rdma")
}

func TestGenerateNVMeGatewayConfigDefaultTransport(t *testing.T) {
	logger := zap.NewNop()
	mockResources := &MockResourceManager{
		Resources: map[string]*ResourceInfo{
			"data": {
				Name: "data",
				Volumes: []*ResourceVolumeInfo{
					{VolumeID: 0, Device: "/dev/drbd0", SizeGB: 100},
					{VolumeID: 1, Device: "/dev/drbd1", SizeGB: 100},
				},
			},
		},
	}
	mockDeployment := &MockDeploymentClient{}
	hosts := []string{"node1"}

	baseManager := New(mockResources, mockDeployment, logger, hosts)
	nvmeManager := NewNVMeManager(baseManager)

	req := &v1.CreateNVMeGatewayRequest{
		Resource:  "data",
		Nqn:       "nqn.2024-01.com.example:sds.default",
		ServiceIp: "192.168.1.150/24",
		// No TransportType specified
	}

	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)

	config, err := nvmeManager.generateNVMeGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)

	// Default should be TCP
	assert.Contains(t, config, "tcp")
}

func TestGenerateNQN(t *testing.T) {
	tests := []struct {
		resource string
		expected string
	}{
		{"data", "nqn.2024-01.com.example:sds.data"},
		{"my-resource", "nqn.2024-01.com.example:sds.my-resource"},
		{"storage_01", "nqn.2024-01.com.example:sds.storage_01"},
	}

	for _, tt := range tests {
		t.Run(tt.resource, func(t *testing.T) {
			result := generateNQN(tt.resource)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestValidateNQN(t *testing.T) {
	tests := []struct {
		nqn      string
		hasError bool
	}{
		{"nqn.2024-01.com.example:storage", false},
		{"nqn.2024-01.com.example:sds.data", false},
		{"iqn.2024-01.com.example:storage", true}, // Wrong prefix
		{"nqn.invalid", true},                     // Missing colon
		{"", true},                                // Empty
	}

	for _, tt := range tests {
		t.Run(tt.nqn, func(t *testing.T) {
			err := validateNQN(tt.nqn)
			if tt.hasError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestParseTransportType(t *testing.T) {
	tests := []struct {
		transport string
		hasError  bool
	}{
		{"tcp", false},
		{"rdma", false},
		{"fc", false},
		{"invalid", true},
		{"", true},
	}

	for _, tt := range tests {
		t.Run(tt.transport, func(t *testing.T) {
			err := parseTransportType(tt.transport)
			if tt.hasError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestNVMeGatewayRequiresTwoVolumes(t *testing.T) {
	logger := zap.NewNop()
	mockResources := &MockResourceManager{
		Resources: map[string]*ResourceInfo{
			"single-volume": {
				Name: "single-volume",
				Volumes: []*ResourceVolumeInfo{
					{VolumeID: 0, Device: "/dev/drbd0", SizeGB: 100},
				},
			},
		},
	}
	mockDeployment := &MockDeploymentClient{}
	hosts := []string{"node1"}

	baseManager := New(mockResources, mockDeployment, logger, hosts)
	nvmeManager := NewNVMeManager(baseManager)

	ctx := context.Background()
	req := &v1.CreateNVMeGatewayRequest{
		Resource:  "single-volume",
		Nqn:       "nqn.2024-01.com.example:sds.single",
		ServiceIp: "192.168.1.150/24",
	}

	resp, err := nvmeManager.CreateNVMeGateway(ctx, req)

	// Should fail because only 1 volume
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "insufficient volumes")
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Message, "at least 2 volumes")
}

func TestGetNVMeGatewayStatus(t *testing.T) {
	logger := zap.NewNop()
	mockResources := &MockResourceManager{}
	mockDeployment := &MockDeploymentClient{}
	hosts := []string{"node1"}

	baseManager := New(mockResources, mockDeployment, logger, hosts)
	nvmeManager := NewNVMeManager(baseManager)

	ctx := context.Background()
	status, err := nvmeManager.GetNVMeGatewayStatus(ctx, "nonexistent-resource")

	require.NoError(t, err)
	assert.Equal(t, "nvmeof", status["type"])
	assert.Equal(t, "nonexistent-resource", status["resource"])
	// Status depends on config file existence
	assert.Contains(t, []string{"not_configured", "unknown", "configured"}, status["status"])
}

func TestDeleteNVMeGateway(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	hosts := []string{"node1", "node2"}

	baseManager := New(nil, mockDeployment, logger, hosts)
	nvmeManager := NewNVMeManager(baseManager)

	ctx := context.Background()
	err := nvmeManager.DeleteNVMeGateway(ctx, "test-resource")

	require.NoError(t, err)
	// Verify deletion commands were executed
	assert.NotEmpty(t, mockDeployment.ExecCommands)
}

func TestAddNamespaceUpdatesConfig(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	baseManager := New(nil, mockDeployment, logger, []string{"node1"})
	nvmeManager := NewNVMeManager(baseManager)

	req := &v1.CreateNVMeGatewayRequest{
		Resource:  "resource",
		Nqn:       "nqn.2024-01.com.example:sds.resource",
		ServiceIp: "192.168.1.150/24",
	}
	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)
	config, err := nvmeManager.generateNVMeGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)
	mockDeployment.SetConfig(gatewayConfigPath("sds-nvmeof-resource"), config)

	ctx := context.Background()
	err = nvmeManager.AddNamespace(ctx, "resource", "/dev/drbd3")

	require.NoError(t, err)
	updated, ok := mockDeployment.GetConfig(gatewayConfigPath("sds-nvmeof-resource"))
	require.True(t, ok)
	assert.Contains(t, updated, "namespace_id=2")
	assert.Contains(t, updated, "backing_path=/dev/drbd3")
}

func TestRemoveNamespaceUpdatesConfig(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	baseManager := New(nil, mockDeployment, logger, []string{"node1"})
	nvmeManager := NewNVMeManager(baseManager)

	req := &v1.CreateNVMeGatewayRequest{
		Resource:  "resource",
		Nqn:       "nqn.2024-01.com.example:sds.resource",
		ServiceIp: "192.168.1.150/24",
	}
	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)
	config, err := nvmeManager.generateNVMeGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(3))
	require.NoError(t, err)
	mockDeployment.SetConfig(gatewayConfigPath("sds-nvmeof-resource"), config)

	ctx := context.Background()
	err = nvmeManager.RemoveNamespace(ctx, "resource", 2)

	require.NoError(t, err)
	updated, ok := mockDeployment.GetConfig(gatewayConfigPath("sds-nvmeof-resource"))
	require.True(t, ok)
	assert.NotContains(t, updated, "namespace_id=2")
}

func TestCreateSubsystemManagedByOCF(t *testing.T) {
	logger := zap.NewNop()
	baseManager := New(nil, nil, logger, nil)
	nvmeManager := NewNVMeManager(baseManager)

	ctx := context.Background()
	err := nvmeManager.CreateSubsystem(ctx, "resource", "nqn.2024-01.com.example:sds.test")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "OCF resource agent")
}

func TestAddAndRemoveNVMeHost(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	baseManager := New(nil, mockDeployment, logger, []string{"node1"})
	nvmeManager := NewNVMeManager(baseManager)

	req := &v1.CreateNVMeGatewayRequest{
		Resource:  "resource",
		Nqn:       "nqn.2024-01.com.example:sds.resource",
		ServiceIp: "192.168.1.150/24",
	}
	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)
	config, err := nvmeManager.generateNVMeGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)
	mockDeployment.SetConfig(gatewayConfigPath("sds-nvmeof-resource"), config)

	ctx := context.Background()
	err = nvmeManager.AddHost(ctx, "resource", "nqn.2024-01.com.example:host1")
	require.NoError(t, err)

	hosts, err := nvmeManager.ListHosts(ctx, "resource")
	require.NoError(t, err)
	assert.Contains(t, hosts, "nqn.2024-01.com.example:host1")

	err = nvmeManager.RemoveHost(ctx, "resource", "nqn.2024-01.com.example:host1")
	require.NoError(t, err)
	hosts, err = nvmeManager.ListHosts(ctx, "resource")
	require.NoError(t, err)
	assert.Equal(t, []string{"ALL"}, hosts)
}

func TestDeleteAndListNVMePort(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	baseManager := New(nil, mockDeployment, logger, []string{"node1"})
	nvmeManager := NewNVMeManager(baseManager)

	req := &v1.CreateNVMeGatewayRequest{
		Resource:  "resource",
		Nqn:       "nqn.2024-01.com.example:sds.resource",
		ServiceIp: "192.168.1.150/24",
	}
	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)
	config, err := nvmeManager.generateNVMeGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)
	mockDeployment.SetConfig(gatewayConfigPath("sds-nvmeof-resource"), config)

	ctx := context.Background()
	ports, err := nvmeManager.ListPorts(ctx, "resource")
	require.NoError(t, err)
	require.Len(t, ports, 1)
	assert.Equal(t, "192.168.1.150", ports[0]["addr"])
	assert.Equal(t, "4420", ports[0]["port"])

	err = nvmeManager.DeletePort(ctx, "resource", "192.168.1.150", 4420)
	require.NoError(t, err)

	updated, ok := mockDeployment.GetConfig(gatewayConfigPath("sds-nvmeof-resource"))
	require.True(t, ok)
	assert.NotContains(t, updated, "ocf:heartbeat:nvmet-port")
}

func TestNVMeDefaultPort(t *testing.T) {
	assert.Equal(t, 4420, DefaultNVMePort)
}

func TestNVMeGatewayUUIDGeneration(t *testing.T) {
	// Test that UUIDs are generated for namespaces
	logger := zap.NewNop()
	mockResources := &MockResourceManager{
		Resources: map[string]*ResourceInfo{
			"data": {
				Name: "data",
				Volumes: []*ResourceVolumeInfo{
					{VolumeID: 0, Device: "/dev/drbd0", SizeGB: 100},
					{VolumeID: 1, Device: "/dev/drbd1", SizeGB: 100},
				},
			},
		},
	}
	mockDeployment := &MockDeploymentClient{}
	hosts := []string{"node1"}

	baseManager := New(mockResources, mockDeployment, logger, hosts)
	nvmeManager := NewNVMeManager(baseManager)

	nqn := "nqn.2024-01.com.example:sds.data"
	req := &v1.CreateNVMeGatewayRequest{
		Resource:  "data",
		Nqn:       nqn,
		ServiceIp: "192.168.1.150/24",
	}

	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)

	config, err := nvmeManager.generateNVMeGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)

	// Only the exposed namespace should have UUID metadata.
	uuidCount := strings.Count(config, "uuid=")
	nguidCount := strings.Count(config, "nguid=")
	assert.Equal(t, 1, uuidCount)
	assert.Equal(t, 1, nguidCount)
}

func TestNVMeGatewaySerialGeneration(t *testing.T) {
	logger := zap.NewNop()
	mockResources := &MockResourceManager{
		Resources: map[string]*ResourceInfo{
			"data": {
				Name: "data",
				Volumes: []*ResourceVolumeInfo{
					{VolumeID: 0, Device: "/dev/drbd0", SizeGB: 100},
					{VolumeID: 1, Device: "/dev/drbd1", SizeGB: 100},
				},
			},
		},
	}
	mockDeployment := &MockDeploymentClient{}
	hosts := []string{"node1"}

	baseManager := New(mockResources, mockDeployment, logger, hosts)
	nvmeManager := NewNVMeManager(baseManager)

	nqn := "nqn.2024-01.com.example:sds.data"
	req := &v1.CreateNVMeGatewayRequest{
		Resource:  "data",
		Nqn:       nqn,
		ServiceIp: "192.168.1.150/24",
	}

	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)

	config, err := nvmeManager.generateNVMeGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)

	// Verify serial is present (16 hex chars derived from SHA256)
	assert.Contains(t, config, "serial=")
}
