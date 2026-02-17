package gateway

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	v1 "github.com/liliang-cn/sds/api/proto/v1"
)

func TestNewISCSIManager(t *testing.T) {
	logger := zap.NewNop()
	mockResources := &MockResourceManager{}
	mockDeployment := &MockDeploymentClient{}
	hosts := []string{"node1", "node2"}

	baseManager := New(mockResources, mockDeployment, logger, hosts)
	iscsiManager := NewISCSIManager(baseManager)

	require.NotNil(t, iscsiManager)
	assert.Equal(t, baseManager, iscsiManager.Manager)
}

func TestGenerateISCSIGatewayConfig(t *testing.T) {
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
	iscsiManager := NewISCSIManager(baseManager)

	req := &v1.CreateISCSIGatewayRequest{
		Resource:   "data",
		Iqn:        "iqn.2024-01.com.example:sds.data",
		ServiceIp:  "192.168.1.200/24",
		Username:   "admin",
		Password:   "secret",
		Implementation: "lio-t",
		AllowedInitiators: []string{"iqn.2024-01.com.example:initiator"},
	}

	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)

	config, err := iscsiManager.generateISCSIGatewayConfig(req, serviceIP, "/dev/drbd0", 2)
	require.NoError(t, err)
	require.NotEmpty(t, config)

	// Verify config contains expected elements
	assert.Contains(t, config, "[[promoter]]")
	assert.Contains(t, config, "promoter.resources")
	assert.Contains(t, config, req.Iqn)
	assert.Contains(t, config, "ocf:heartbeat:IPaddr2")
	assert.Contains(t, config, "ocf:heartbeat:iSCSITarget")
	assert.Contains(t, config, "ocf:heartbeat:iSCSILogicalUnit")
	assert.Contains(t, config, "ocf:heartbeat:Filesystem")
	assert.Contains(t, config, "ocf:heartbeat:portblock")
	assert.Contains(t, config, req.Implementation)
	assert.Contains(t, config, req.Username)
	assert.Contains(t, config, "3260") // Default iSCSI port
}

func TestGenerateISCSIGatewayConfigMultipleLUNs(t *testing.T) {
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
	iscsiManager := NewISCSIManager(baseManager)

	req := &v1.CreateISCSIGatewayRequest{
		Resource:   "data",
		Iqn:        "iqn.2024-01.com.example:sds.multi-lun",
		ServiceIp:  "10.0.0.50/24",
	}

	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)

	config, err := iscsiManager.generateISCSIGatewayConfig(req, serviceIP, "/dev/drbd0", 4)
	require.NoError(t, err)

	// Should have multiple LUN entries
	lunCount := strings.Count(config, "iSCSILogicalUnit")
	assert.Equal(t, 4, lunCount) // 4 volumes

	// Should have LUN numbers 0, 1, 2, 3 (format: lun=N without spaces around =)
	assert.Contains(t, config, "lun=0")
	assert.Contains(t, config, "lun=1")
	assert.Contains(t, config, "lun=2")
	assert.Contains(t, config, "lun=3")
}

func TestGenerateISCSIGatewayConfigDefaults(t *testing.T) {
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
	iscsiManager := NewISCSIManager(baseManager)

	req := &v1.CreateISCSIGatewayRequest{
		Resource:   "data",
		Iqn:        "iqn.2024-01.com.example:sds.data",
		ServiceIp:  "192.168.1.200/24",
		// No username/password/implementation specified
	}

	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)

	config, err := iscsiManager.generateISCSIGatewayConfig(req, serviceIP, "/dev/drbd0", 2)
	require.NoError(t, err)

	// Should have default values
	assert.Contains(t, config, "incoming_username=username") // default username
	assert.Contains(t, config, "incoming_password=password") // default password
	assert.Contains(t, config, "implementation=lio-t")       // default implementation
	assert.Contains(t, config, "allowed_initiators=ALL")     // default initiators
}

func TestGenerateIQN(t *testing.T) {
	tests := []struct {
		resource string
		expected string
	}{
		{"data", "iqn.2024-01.com.example:sds.data"},
		{"my-resource", "iqn.2024-01.com.example:sds.my-resource"},
		{"storage_01", "iqn.2024-01.com.example:sds.storage_01"},
	}

	for _, tt := range tests {
		t.Run(tt.resource, func(t *testing.T) {
			result := generateIQN(tt.resource)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestValidateIQN(t *testing.T) {
	tests := []struct {
		iqn      string
		hasError bool
	}{
		{"iqn.2024-01.com.example:storage", false},
		{"iqn.2024-01.com.example:sds.data", false},
		{"nqn.2024-01.com.example:storage", true}, // Wrong prefix
		{"iqn.invalid", true},                     // Missing colon
		{"", true},                                // Empty
	}

	for _, tt := range tests {
		t.Run(tt.iqn, func(t *testing.T) {
			err := validateIQN(tt.iqn)
			if tt.hasError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestParsePortal(t *testing.T) {
	tests := []struct {
		portal        string
		expectedHost  string
		expectedPort  int
		hasError      bool
	}{
		{"192.168.1.100:3260", "192.168.1.100", 3260, false},
		{"10.0.0.1:3260", "10.0.0.1", 3260, false},
		{"[::1]:3260", "::1", 3260, false},
	}

	for _, tt := range tests {
		t.Run(tt.portal, func(t *testing.T) {
			host, port, err := parsePortal(tt.portal)
			if tt.hasError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedHost, host)
				assert.Equal(t, tt.expectedPort, port)
			}
		})
	}
}

func TestISCSIGatewayRequiresTwoVolumes(t *testing.T) {
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
	iscsiManager := NewISCSIManager(baseManager)

	ctx := context.Background()
	req := &v1.CreateISCSIGatewayRequest{
		Resource:  "single-volume",
		Iqn:       "iqn.2024-01.com.example:sds.single",
		ServiceIp: "192.168.1.200/24",
	}

	resp, err := iscsiManager.CreateISCSIGateway(ctx, req)

	// Should fail because only 1 volume
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "insufficient volumes")
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Message, "at least 2 volumes")
}

func TestGetISCSIGatewayStatus(t *testing.T) {
	logger := zap.NewNop()
	mockResources := &MockResourceManager{}
	mockDeployment := &MockDeploymentClient{}
	hosts := []string{"node1"}

	baseManager := New(mockResources, mockDeployment, logger, hosts)
	iscsiManager := NewISCSIManager(baseManager)

	ctx := context.Background()
	status, err := iscsiManager.GetISCSIGatewayStatus(ctx, "nonexistent-resource")

	require.NoError(t, err)
	assert.Equal(t, "iscsi", status["type"])
	assert.Equal(t, "nonexistent-resource", status["resource"])
	// Status depends on config file existence
	assert.Contains(t, []string{"not_configured", "unknown", "configured"}, status["status"])
}

func TestDeleteISCSIGateway(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	hosts := []string{"node1", "node2"}

	baseManager := New(nil, mockDeployment, logger, hosts)
	iscsiManager := NewISCSIManager(baseManager)

	ctx := context.Background()
	err := iscsiManager.DeleteISCSIGateway(ctx, "test-resource")

	require.NoError(t, err)
	// Verify deletion commands were executed
	assert.NotEmpty(t, mockDeployment.ExecCommands)
}

func TestAddLUNNotImplemented(t *testing.T) {
	logger := zap.NewNop()
	baseManager := New(nil, nil, logger, nil)
	iscsiManager := NewISCSIManager(baseManager)

	ctx := context.Background()
	err := iscsiManager.AddLUN(ctx, "resource", 1, "/dev/drbd1")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not yet implemented")
}

func TestRemoveLUNNotImplemented(t *testing.T) {
	logger := zap.NewNop()
	baseManager := New(nil, nil, logger, nil)
	iscsiManager := NewISCSIManager(baseManager)

	ctx := context.Background()
	err := iscsiManager.RemoveLUN(ctx, "resource", 1)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not yet implemented")
}

func TestCreateTargetManagedByOCF(t *testing.T) {
	logger := zap.NewNop()
	baseManager := New(nil, nil, logger, nil)
	iscsiManager := NewISCSIManager(baseManager)

	ctx := context.Background()
	err := iscsiManager.CreateTarget(ctx, "resource")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "OCF resource agent")
}

func TestAddInitiatorNotImplemented(t *testing.T) {
	logger := zap.NewNop()
	baseManager := New(nil, nil, logger, nil)
	iscsiManager := NewISCSIManager(baseManager)

	ctx := context.Background()
	err := iscsiManager.AddInitiator(ctx, "resource", "iqn.2024-01.com.example:initiator")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not yet implemented")
}

func TestSetCHAPNotImplemented(t *testing.T) {
	logger := zap.NewNop()
	baseManager := New(nil, nil, logger, nil)
	iscsiManager := NewISCSIManager(baseManager)

	ctx := context.Background()
	err := iscsiManager.SetCHAP(ctx, "resource", "user", "pass", false)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not yet implemented")
}

func TestISCSIDefaultPort(t *testing.T) {
	assert.Equal(t, 3260, DefaultISCSIPort)
}

func TestISCSIGatewaySerialGeneration(t *testing.T) {
	// Test that serial numbers are generated correctly for LUNs
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
	iscsiManager := NewISCSIManager(baseManager)

	iqn := "iqn.2024-01.com.example:sds.data"
	req := &v1.CreateISCSIGatewayRequest{
		Resource:  "data",
		Iqn:       iqn,
		ServiceIp: "192.168.1.200/24",
	}

	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)

	config, err := iscsiManager.generateISCSIGatewayConfig(req, serviceIP, "/dev/drbd0", 2)
	require.NoError(t, err)

	// Verify serial numbers are present (16 hex chars)
	serialCount := strings.Count(config, "scsi_sn=")
	assert.Equal(t, 2, serialCount)
}
