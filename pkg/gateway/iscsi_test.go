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
		Resource:          "data",
		Iqn:               "iqn.2024-01.com.example:sds.data",
		ServiceIp:         "192.168.1.200/24",
		Username:          "admin",
		Password:          "secret",
		Implementation:    "lio-t",
		AllowedInitiators: []string{"iqn.2024-01.com.example:initiator"},
	}

	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)

	config, err := iscsiManager.generateISCSIGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
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
		Resource:  "data",
		Iqn:       "iqn.2024-01.com.example:sds.multi-lun",
		ServiceIp: "10.0.0.50/24",
	}

	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)

	config, err := iscsiManager.generateISCSIGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(4))
	require.NoError(t, err)

	// Only volumes 1+ are exposed as LUNs.
	lunCount := strings.Count(config, "iSCSILogicalUnit")
	assert.Equal(t, 3, lunCount)
	assert.NotContains(t, config, "lun=0")
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
		Resource:  "data",
		Iqn:       "iqn.2024-01.com.example:sds.data",
		ServiceIp: "192.168.1.200/24",
		// No username/password/implementation specified
	}

	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)

	config, err := iscsiManager.generateISCSIGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)

	// Should have default values
	assert.Contains(t, config, "incoming_username=username") // default username
	assert.Contains(t, config, "incoming_password=password") // default password
	assert.Contains(t, config, "implementation=lio-t")       // default implementation
	// Empty allowed_initiators stays empty: the OCF agent validates each
	// token as an initiator WWN, so an invented "ALL" breaks target start.
	assert.Contains(t, config, "allowed_initiators= ")
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
		portal       string
		expectedHost string
		expectedPort int
		hasError     bool
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

func TestAddLUNUpdatesConfig(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	baseManager := New(nil, mockDeployment, logger, []string{"node1"})
	iscsiManager := NewISCSIManager(baseManager)

	req := &v1.CreateISCSIGatewayRequest{
		Resource:  "resource",
		Iqn:       "iqn.2024-01.com.example:sds.resource",
		ServiceIp: "192.168.1.200/24",
	}
	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)
	config, err := iscsiManager.generateISCSIGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)
	mockDeployment.SetConfig(gatewayConfigPath("sds-iscsi-resource"), config)

	ctx := context.Background()
	err = iscsiManager.AddLUN(ctx, "resource", 3, "/dev/drbd3")

	require.NoError(t, err)
	updated, ok := mockDeployment.GetConfig(gatewayConfigPath("sds-iscsi-resource"))
	require.True(t, ok)
	assert.Contains(t, updated, "lun=3")
	assert.Contains(t, updated, "path=/dev/drbd3")
}

func TestListLUNs(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	baseManager := New(nil, mockDeployment, logger, []string{"node1"})
	iscsiManager := NewISCSIManager(baseManager)

	req := &v1.CreateISCSIGatewayRequest{
		Resource:  "resource",
		Iqn:       "iqn.2024-01.com.example:sds.resource",
		ServiceIp: "192.168.1.200/24",
	}
	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)
	config, err := iscsiManager.generateISCSIGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(3))
	require.NoError(t, err)
	mockDeployment.SetConfig(gatewayConfigPath("sds-iscsi-resource"), config)

	luns, err := iscsiManager.ListLUNs(context.Background(), "resource")
	require.NoError(t, err)
	require.Len(t, luns, 2)
	assert.Equal(t, "1", luns[0]["lun"])
	assert.Equal(t, "/dev/drbd1", luns[0]["device"])
	assert.Equal(t, "2", luns[1]["lun"])
	assert.Equal(t, "/dev/drbd2", luns[1]["device"])
}

func TestRemoveLUNUpdatesConfig(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	baseManager := New(nil, mockDeployment, logger, []string{"node1"})
	iscsiManager := NewISCSIManager(baseManager)

	req := &v1.CreateISCSIGatewayRequest{
		Resource:  "resource",
		Iqn:       "iqn.2024-01.com.example:sds.resource",
		ServiceIp: "192.168.1.200/24",
	}
	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)
	config, err := iscsiManager.generateISCSIGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(3))
	require.NoError(t, err)
	mockDeployment.SetConfig(gatewayConfigPath("sds-iscsi-resource"), config)

	ctx := context.Background()
	err = iscsiManager.RemoveLUN(ctx, "resource", 2)

	require.NoError(t, err)
	updated, ok := mockDeployment.GetConfig(gatewayConfigPath("sds-iscsi-resource"))
	require.True(t, ok)
	assert.NotContains(t, updated, "lun=2")
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

func TestAddAndListInitiators(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	baseManager := New(nil, mockDeployment, logger, []string{"node1"})
	iscsiManager := NewISCSIManager(baseManager)

	req := &v1.CreateISCSIGatewayRequest{
		Resource:          "resource",
		Iqn:               "iqn.2024-01.com.example:sds.resource",
		ServiceIp:         "192.168.1.200/24",
		AllowedInitiators: []string{"iqn.2024-01.com.example:init-a"},
	}
	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)
	config, err := iscsiManager.generateISCSIGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)
	mockDeployment.SetConfig(gatewayConfigPath("sds-iscsi-resource"), config)

	ctx := context.Background()
	err = iscsiManager.AddInitiator(ctx, "resource", "iqn.2024-01.com.example:init-b")
	require.NoError(t, err)

	initiators, err := iscsiManager.ListInitiators(ctx, "resource")
	require.NoError(t, err)
	assert.Contains(t, initiators, "iqn.2024-01.com.example:init-a")
	assert.Contains(t, initiators, "iqn.2024-01.com.example:init-b")
}

func TestSetAndGetCHAP(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	baseManager := New(nil, mockDeployment, logger, []string{"node1"})
	iscsiManager := NewISCSIManager(baseManager)

	req := &v1.CreateISCSIGatewayRequest{
		Resource:  "resource",
		Iqn:       "iqn.2024-01.com.example:sds.resource",
		ServiceIp: "192.168.1.200/24",
	}
	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)
	config, err := iscsiManager.generateISCSIGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)
	mockDeployment.SetConfig(gatewayConfigPath("sds-iscsi-resource"), config)

	ctx := context.Background()
	err = iscsiManager.SetCHAP(ctx, "resource", "user", "pass", false)
	require.NoError(t, err)

	username, password, mutual, err := iscsiManager.GetCHAP(ctx, "resource")
	require.NoError(t, err)
	assert.Equal(t, "user", username)
	assert.Equal(t, "pass", password)
	assert.False(t, mutual)
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

	config, err := iscsiManager.generateISCSIGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)

	// Only the exposed data volume should have a serial.
	serialCount := strings.Count(config, "scsi_sn=")
	assert.Equal(t, 1, serialCount)
}
