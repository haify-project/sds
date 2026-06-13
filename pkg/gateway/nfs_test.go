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

func TestNFSFormatCIDR(t *testing.T) {
	tests := []struct {
		cidr     string
		expected string
	}{
		{"192.168.1.0/24", "192.168.1.0/24"},
		{"10.0.0.0/8", "10.0.0.0/8"},
		{"0.0.0.0/0", "0.0.0.0/0.0.0.0"},
		{"172.16.0.0/16", "172.16.0.0/16"},
	}

	for _, tt := range tests {
		t.Run(tt.cidr, func(t *testing.T) {
			result := nfsFormatCIDR(tt.cidr)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParseCIDR(t *testing.T) {
	tests := []struct {
		cidr         string
		expectedIP   string
		expectedMask int
		hasError     bool
	}{
		{"192.168.1.0/24", "192.168.1.0", 24, false},
		{"10.0.0.0/8", "10.0.0.0", 8, false},
		{"172.16.0.0/16", "172.16.0.0", 16, false},
		{"192.168.1.100/32", "192.168.1.100", 32, false},
		{"invalid", "", 0, true},
		{"", "", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.cidr, func(t *testing.T) {
			ip, prefix, err := parseCIDR(tt.cidr)
			if tt.hasError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedIP, ip)
				assert.Equal(t, tt.expectedMask, prefix)
			}
		})
	}
}

func TestNewNFSManager(t *testing.T) {
	logger := zap.NewNop()
	mockResources := &MockResourceManager{}
	mockDeployment := &MockDeploymentClient{}
	hosts := []string{"node1", "node2"}

	baseManager := New(mockResources, mockDeployment, logger, hosts)
	nfsManager := NewNFSManager(baseManager)

	require.NotNil(t, nfsManager)
	assert.Equal(t, baseManager, nfsManager.Manager)
}

func TestGenerateNFSGatewayConfig(t *testing.T) {
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
	nfsManager := NewNFSManager(baseManager)

	req := &v1.CreateNFSGatewayRequest{
		Resource:   "data",
		ServiceIp:  "192.168.1.100/24",
		ExportPath: "/export/data",
		FsType:     "ext4",
		AllowedIps: []string{"192.168.1.0/24", "10.0.0.0/8"},
	}

	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)

	config, err := nfsManager.generateNFSGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)
	require.NotEmpty(t, config)

	// Verify config contains expected elements
	assert.Contains(t, config, "[[promoter]]")
	assert.Contains(t, config, "promoter.resources")
	assert.Contains(t, config, req.Resource)
	assert.Contains(t, config, "ocf:heartbeat:Filesystem")
	assert.Contains(t, config, "ocf:heartbeat:IPaddr2")
	assert.Contains(t, config, "ocf:heartbeat:nfsserver")
	assert.Contains(t, config, "ocf:heartbeat:exportfs")
	// NFS deliberately omits portblock/portunblock: the floating IP already
	// fences the VIP, and the pair stranded a DROP rule on the new active node
	// after failover.
	assert.NotContains(t, config, "portblock")
	assert.Contains(t, config, req.ServiceIp)
	assert.Contains(t, config, "/dev/drbd0")
}

func TestGenerateNFSGatewayConfigWithCustomFSType(t *testing.T) {
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
	nfsManager := NewNFSManager(baseManager)

	req := &v1.CreateNFSGatewayRequest{
		Resource:   "data",
		ServiceIp:  "10.0.0.100/24",
		ExportPath: "/data",
		FsType:     "xfs",
		AllowedIps: []string{"0.0.0.0/0"},
	}

	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)

	config, err := nfsManager.generateNFSGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)

	// Verify custom filesystem type
	// Note: The fstype is used in the template
	assert.Contains(t, config, "fstype=xfs")
}

func TestGenerateNFSGatewayConfigDefaultClients(t *testing.T) {
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
	nfsManager := NewNFSManager(baseManager)

	req := &v1.CreateNFSGatewayRequest{
		Resource:   "data",
		ServiceIp:  "192.168.1.100/24",
		ExportPath: "/export/data",
		FsType:     "ext4",
		AllowedIps: []string{}, // Empty - should default to allow all
	}

	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)

	config, err := nfsManager.generateNFSGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)

	// When no clients specified, should default to 0.0.0.0/0.0.0.0
	assert.Contains(t, config, "0.0.0.0/0.0.0.0")
}

func TestGetNFSGatewayStatus(t *testing.T) {
	logger := zap.NewNop()
	mockResources := &MockResourceManager{}
	mockDeployment := &MockDeploymentClient{}
	hosts := []string{"node1"}

	baseManager := New(mockResources, mockDeployment, logger, hosts)
	nfsManager := NewNFSManager(baseManager)

	ctx := context.Background()
	status, err := nfsManager.GetNFSGatewayStatus(ctx, "nonexistent-resource")

	require.NoError(t, err)
	assert.Equal(t, "nfs", status["type"])
	assert.Equal(t, "nonexistent-resource", status["resource"])
	// Status depends on config file existence
	assert.Contains(t, []string{"not_configured", "unknown"}, status["status"])
}

func TestDeleteNFSGateway(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	hosts := []string{"node1", "node2"}

	baseManager := New(nil, mockDeployment, logger, hosts)
	nfsManager := NewNFSManager(baseManager)

	ctx := context.Background()
	err := nfsManager.DeleteNFSGateway(ctx, "test-resource")

	require.NoError(t, err)
	// Verify deletion commands were executed
	assert.NotEmpty(t, mockDeployment.ExecCommands)
}

func TestAddNFSExportUpdatesConfig(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	baseManager := New(nil, mockDeployment, logger, []string{"node1"})
	nfsManager := NewNFSManager(baseManager)

	req := &v1.CreateNFSGatewayRequest{
		Resource:   "resource",
		ServiceIp:  "192.168.1.100/24",
		ExportPath: "/data",
	}
	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)
	config, err := nfsManager.generateNFSGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)
	mockDeployment.SetConfig(gatewayConfigPath("sds-nfs-resource"), config)

	ctx := context.Background()
	err = nfsManager.AddNFSExport(ctx, "resource", "/backup", 99, "10.0.0.0/8", "ro")

	require.NoError(t, err)
	exports, err := nfsManager.ListNFSExports(ctx, "resource")
	require.NoError(t, err)
	assert.NotEmpty(t, exports)
	// Absolute paths are honored verbatim under the new semantics.
	assert.True(t, strings.Contains(mockDeployment.Configs[gatewayConfigPath("sds-nfs-resource")], "directory=/backup"))
}

func TestRemoveNFSExportUpdatesConfig(t *testing.T) {
	logger := zap.NewNop()
	mockDeployment := &MockDeploymentClient{}
	baseManager := New(nil, mockDeployment, logger, []string{"node1"})
	nfsManager := NewNFSManager(baseManager)

	req := &v1.CreateNFSGatewayRequest{
		Resource:   "resource",
		ServiceIp:  "192.168.1.100/24",
		ExportPath: "/data",
	}
	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)
	config, err := nfsManager.generateNFSGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)
	mockDeployment.SetConfig(gatewayConfigPath("sds-nfs-resource"), config)

	ctx := context.Background()
	err = nfsManager.RemoveNFSExport(ctx, "resource", "/data")

	require.NoError(t, err)
	exports, err := nfsManager.ListNFSExports(ctx, "resource")
	require.NoError(t, err)
	assert.Empty(t, exports)
}

func TestListNFSExportsWithNonexistentConfig(t *testing.T) {
	logger := zap.NewNop()
	baseManager := New(nil, nil, logger, nil)
	nfsManager := NewNFSManager(baseManager)

	ctx := context.Background()
	exports, err := nfsManager.ListNFSExports(ctx, "nonexistent-resource")

	assert.Error(t, err)
	assert.Nil(t, exports)
}

func TestNFSGatewayRequiresTwoVolumes(t *testing.T) {
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
	nfsManager := NewNFSManager(baseManager)

	ctx := context.Background()
	req := &v1.CreateNFSGatewayRequest{
		Resource:   "single-volume",
		ServiceIp:  "192.168.1.100/24",
		ExportPath: "/export/data",
	}

	resp, err := nfsManager.CreateNFSGateway(ctx, req)

	// Should fail because only 1 volume
	// The function returns both an error and a response with failure
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "insufficient volumes")
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Message, "at least 2 volumes")
}

func TestNFSExportPath(t *testing.T) {
	// Test that export paths are correctly constructed
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
	nfsManager := NewNFSManager(baseManager)

	req := &v1.CreateNFSGatewayRequest{
		Resource:   "data",
		ServiceIp:  "192.168.1.100/24",
		ExportPath: "/data",
	}

	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)

	config, err := nfsManager.generateNFSGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)

	// An absolute export path is honored verbatim: the admin asked for
	// /data, so clients mount <vip>:/data — not a path nested under the
	// gateway base directory.
	assert.Contains(t, config, "directory=/data ")
	assert.NotContains(t, config, DefaultExportBasePath)
}

func TestResolveNFSExportPath(t *testing.T) {
	// Empty defaults under the base path, grouped by resource.
	got, err := resolveNFSExportPath("res1", "")
	require.NoError(t, err)
	assert.Equal(t, DefaultExportBasePath+"/res1", got)

	// Relative paths are grouped under the base path too.
	got, err = resolveNFSExportPath("res1", "exports/a")
	require.NoError(t, err)
	assert.Equal(t, DefaultExportBasePath+"/res1/exports/a", got)

	// Absolute paths are honored verbatim (cleaned).
	got, err = resolveNFSExportPath("res1", "/srv/nfs-test/")
	require.NoError(t, err)
	assert.Equal(t, "/srv/nfs-test", got)

	// The root and system directories are refused: the export directory
	// becomes a gateway-owned mount point.
	_, err = resolveNFSExportPath("res1", "/")
	assert.Error(t, err)
	_, err = resolveNFSExportPath("res1", "/etc/exports")
	assert.Error(t, err)
	_, err = resolveNFSExportPath("res1", "/var/lib/sds/x")
	assert.Error(t, err)
}

func TestNFSClusterPrivatePath(t *testing.T) {
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
	nfsManager := NewNFSManager(baseManager)

	req := &v1.CreateNFSGatewayRequest{
		Resource:   "data",
		ServiceIp:  "192.168.1.100/24",
		ExportPath: "/data",
	}

	serviceIP, err := parseServiceIP(req.ServiceIp)
	require.NoError(t, err)

	config, err := nfsManager.generateNFSGatewayConfig(req, serviceIP, "/dev/drbd0", testVolumes(2))
	require.NoError(t, err)

	// Cluster private path should be under DefaultClusterPrivateMountPath
	assert.True(t, strings.Contains(config, DefaultClusterPrivateMountPath) || strings.Contains(config, "/var/lib/sds"))
}
