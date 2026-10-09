package gateway

import (
	"context"
	"strings"
	"testing"

	v1 "github.com/haify-project/haify/api/proto/v1"
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
		Nqn:           "nqn.2024-01.com.example:haify.data",
		ServiceIp:     "192.168.1.150/24",
		TransportType: "tcp",
	}

	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)

	config, err := nvmeManager.generateNVMeGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)
	require.NotEmpty(t, config)
	assertSafeUnmount(t, config)

	// Verify config contains expected elements
	assert.Contains(t, config, "[[promoter]]")
	assert.Contains(t, config, "promoter.resources")
	assert.Contains(t, config, req.Nqn)
	assert.Contains(t, config, "ocf:heartbeat:IPaddr2")
	assert.Contains(t, config, "ocf:heartbeat:nvmet-subsystem")
	assert.Contains(t, config, "ocf:heartbeat:nvmet-namespace")
	assert.Contains(t, config, "ocf:heartbeat:nvmet-port")
	assert.Contains(t, config, "ocf:heartbeat:Filesystem")
	// portblock removed: it left a stale DROP on the active node after failover.
	assert.NotContains(t, config, "portblock")
	assert.Contains(t, config, "tcp") // Transport type
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
		Nqn:           "nqn.2024-01.com.example:haify.multi-ns",
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
		Nqn:           "nqn.2024-01.com.example:haify.rdma",
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
		Nqn:       "nqn.2024-01.com.example:haify.default",
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
		{"data", "nqn.2024-01.com.example:haify.data"},
		{"my-resource", "nqn.2024-01.com.example:haify.my-resource"},
		{"storage_01", "nqn.2024-01.com.example:haify.storage_01"},
	}

	for _, tt := range tests {
		t.Run(tt.resource, func(t *testing.T) {
			result := generateNQN(tt.resource)
			assert.Equal(t, tt.expected, result)
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
		Nqn:       "nqn.2024-01.com.example:haify.single",
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

	nqn := "nqn.2024-01.com.example:haify.data"
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

	nqn := "nqn.2024-01.com.example:haify.data"
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
