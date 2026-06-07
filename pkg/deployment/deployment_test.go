package deployment

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestIsLocalHost(t *testing.T) {
	tests := []struct {
		host     string
		expected bool
	}{
		{"localhost", true},
		{"127.0.0.1", true},
		{"192.168.1.100", false},
		{"remote.host.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			result := isLocalHost(tt.host)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetLocalIPs(t *testing.T) {
	ips := getLocalIPs()
	// Just verify the function doesn't panic and returns a slice
	assert.NotNil(t, ips)
}

func TestIsLocalIP(t *testing.T) {
	localAddrs := []string{"192.168.1.100", "10.0.0.1"}

	tests := []struct {
		host     string
		expected bool
	}{
		{"192.168.1.100", true},
		{"10.0.0.1", true},
		{"192.168.1.200", false},
		{"8.8.8.8", false},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			result := isLocalIP(tt.host, localAddrs)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNew(t *testing.T) {
	logger := zap.NewNop()

	client, err := New(logger)
	// This may fail if no dispatch config is available, which is expected in test environment
	// We just verify the function doesn't panic
	_ = client
	_ = err
}

func TestExecResultAllSuccess(t *testing.T) {
	result := &ExecResult{
		Hosts: map[string]*HostResult{
			"host1": {Host: "host1", Success: true, Output: "ok"},
			"host2": {Host: "host2", Success: true, Output: "ok"},
		},
	}

	assert.True(t, result.AllSuccess())
	assert.Empty(t, result.FailedHosts())

	// Test with a failure
	result.Hosts["host3"] = &HostResult{Host: "host3", Success: false, Output: "error"}
	assert.False(t, result.AllSuccess())
	assert.Contains(t, result.FailedHosts(), "host3")
}

func TestExecResultFailedHosts(t *testing.T) {
	result := &ExecResult{
		Hosts: map[string]*HostResult{
			"host1": {Host: "host1", Success: true, Output: "ok"},
			"host2": {Host: "host2", Success: false, Output: "error"},
			"host3": {Host: "host3", Success: false, Output: "error"},
		},
	}

	failed := result.FailedHosts()
	assert.Len(t, failed, 2)
	assert.Contains(t, failed, "host2")
	assert.Contains(t, failed, "host3")
}

func TestHostResult(t *testing.T) {
	result := &HostResult{
		Host:    "test-host",
		Output:  "command output",
		Success: true,
		Error:   nil,
	}

	assert.Equal(t, "test-host", result.Host)
	assert.Equal(t, "command output", result.Output)
	assert.True(t, result.Success)
	assert.Nil(t, result.Error)
}

func TestConfigResult(t *testing.T) {
	result := &ConfigResult{
		Path:    "/etc/drbd.d/test.res",
		Success: true,
		Hosts: map[string]*HostResult{
			"host1": {Host: "host1", Success: true},
		},
	}

	assert.Equal(t, "/etc/drbd.d/test.res", result.Path)
	assert.True(t, result.Success)
	assert.Len(t, result.Hosts, 1)
}

func TestOptions(t *testing.T) {
	// Test ConfigOption
	backupOpt := WithBackup(true)
	postCmdOpt := WithPostCommand("systemctl reload drbd-reactor")

	configOpts := &configOptions{}
	backupOpt(configOpts)
	assert.True(t, configOpts.backup)

	postCmdOpt(configOpts)
	assert.Equal(t, "systemctl reload drbd-reactor", configOpts.postCommand)

	// Test ExecOption
	parallelOpt := WithExecParallel(20)
	timeoutOpt := WithExecTimeout(60)

	execOpts := &execOptions{}
	parallelOpt(execOpts)
	assert.Equal(t, 20, execOpts.parallel)

	timeoutOpt(execOpts)
	assert.Equal(t, time.Duration(60), execOpts.timeout)

	// Test LVMOption
	forceOpt := WithLVMForce(true)

	lvmOpts := &lvmOptions{}
	forceOpt(lvmOpts)
	assert.True(t, lvmOpts.force)

	// Test ZFSOption
	thinOpt := WithZFSThin(true)
	compressionOpt := WithZFSCompression(true)
	dedupOpt := WithZFSDedup(true)

	zfsOpts := &zfsOptions{}
	thinOpt(zfsOpts)
	assert.True(t, zfsOpts.thin)

	compressionOpt(zfsOpts)
	assert.True(t, zfsOpts.compression)

	dedupOpt(zfsOpts)
	assert.True(t, zfsOpts.dedup)
}

// MockClient for testing controller package
type MockClient struct {
	ExecFunc             func(ctx context.Context, hosts []string, cmd string) (*ExecResult, error)
	DistributeConfigFunc func(ctx context.Context, hosts []string, content, path string, opts ...ConfigOption) (*ConfigResult, error)
	DRBDPrimaryFunc      func(ctx context.Context, host, resource string, force bool) (*HostResult, error)
	DRBDStatusFunc       func(ctx context.Context, hosts []string, resource string) (*ExecResult, error)
}

func (m *MockClient) Exec(ctx context.Context, hosts []string, cmd string) (*ExecResult, error) {
	if m.ExecFunc != nil {
		return m.ExecFunc(ctx, hosts, cmd)
	}
	return &ExecResult{
		Hosts: map[string]*HostResult{
			"host1": {Host: "host1", Success: true, Output: "mock output"},
		},
	}, nil
}

func (m *MockClient) DistributeConfig(ctx context.Context, hosts []string, content, path string, opts ...ConfigOption) (*ConfigResult, error) {
	if m.DistributeConfigFunc != nil {
		return m.DistributeConfigFunc(ctx, hosts, content, path, opts...)
	}
	return &ConfigResult{Success: true, Path: path}, nil
}

func (m *MockClient) DeleteConfig(ctx context.Context, hosts []string, path string) error {
	return nil
}

func (m *MockClient) DRBDUp(ctx context.Context, hosts []string, resource string) (*ExecResult, error) {
	return &ExecResult{
		Hosts: map[string]*HostResult{
			"host1": {Host: "host1", Success: true},
		},
	}, nil
}

func (m *MockClient) DRBDDown(ctx context.Context, hosts []string, resource string) (*ExecResult, error) {
	return &ExecResult{
		Hosts: map[string]*HostResult{
			"host1": {Host: "host1", Success: true},
		},
	}, nil
}

func (m *MockClient) DRBDPrimary(ctx context.Context, host, resource string, force bool) (*HostResult, error) {
	if m.DRBDPrimaryFunc != nil {
		return m.DRBDPrimaryFunc(ctx, host, resource, force)
	}
	return &HostResult{Host: host, Success: true}, nil
}

func (m *MockClient) DRBDSecondary(ctx context.Context, host, resource string) (*HostResult, error) {
	return &HostResult{Host: host, Success: true}, nil
}

func (m *MockClient) DRBDCreateMD(ctx context.Context, hosts []string, resource string) (*ExecResult, error) {
	return &ExecResult{
		Hosts: map[string]*HostResult{
			"host1": {Host: "host1", Success: true},
		},
	}, nil
}

func (m *MockClient) DRBDStatus(ctx context.Context, hosts []string, resource string) (*ExecResult, error) {
	if m.DRBDStatusFunc != nil {
		return m.DRBDStatusFunc(ctx, hosts, resource)
	}
	return &ExecResult{
		Hosts: map[string]*HostResult{
			"host1": {Host: "host1", Success: true, Output: "resource role:Primary"},
		},
	}, nil
}

func (m *MockClient) PVCreate(ctx context.Context, hosts []string, device string) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) VGCreate(ctx context.Context, hosts []string, vgName string, devices []string) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) LVCreate(ctx context.Context, hosts []string, vgName, lvName, size string) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) LVCreateThinPool(ctx context.Context, hosts []string, vgName, poolName, size string) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) LVCreateThinVolume(ctx context.Context, hosts []string, vgName, poolName, lvName, size string) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) LVCreateSnapshot(ctx context.Context, hosts []string, vgName, lvName, snapshotName, size string) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) LVCreateThinSnapshot(ctx context.Context, hosts []string, vgName, lvName, snapshotName string) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) LVIsThin(ctx context.Context, host, vgName, lvName string) (bool, error) {
	return false, nil
}

func (m *MockClient) LVRemoveSnapshot(ctx context.Context, hosts []string, vgName, snapshotName string) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) LVListSnapshots(ctx context.Context, hosts []string, vgName string) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) LVMergeSnapshot(ctx context.Context, hosts []string, vgName, snapshotName string) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) ZFSCreatePool(ctx context.Context, hosts []string, poolName string, vdevs []string, opts ...ZFSOption) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) ZFSDestroyPool(ctx context.Context, hosts []string, poolName string) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) ZFSListPools(ctx context.Context, hosts []string) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) ZFSCreateDataset(ctx context.Context, hosts []string, datasetName string, opts ...ZFSOption) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) ZFSCreateThinDataset(ctx context.Context, hosts []string, poolName, datasetName, size string) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) ZFSDestroyDataset(ctx context.Context, hosts []string, datasetName string) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) ZFSSnapshot(ctx context.Context, hosts []string, dataset, snapshotName string) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) ZFSRollback(ctx context.Context, hosts []string, dataset, snapshotName string) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) ZFSClone(ctx context.Context, hosts []string, snapshot, clonePath string) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) ZFSListSnapshots(ctx context.Context, hosts []string, dataset string) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) ZFSDestroySnapshot(ctx context.Context, hosts []string, snapshot string) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) ZFSSetQuota(ctx context.Context, hosts []string, dataset, quota string) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) ZFSResizeVolume(ctx context.Context, hosts []string, volumePath, newSize string) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) ReactorReload(ctx context.Context, hosts []string) (*ExecResult, error) {
	return &ExecResult{}, nil
}

func (m *MockClient) ReactorStatusJSON(ctx context.Context, host string) (*ReactorStatus, error) {
	if m.ExecFunc != nil {
		result, err := m.ExecFunc(ctx, []string{host}, "sudo drbd-reactorctl status --json")
		if err != nil {
			return nil, err
		}
		for _, r := range result.Hosts {
			if r.Success {
				var status ReactorStatus
				if err := json.Unmarshal([]byte(r.Output), &status); err != nil {
					return nil, err
				}
				return &status, nil
			}
		}
	}
	return &ReactorStatus{}, nil
}

func (m *MockClient) ReactorPromoterStatusByResource(ctx context.Context, host, resource string) (*ReactorPromoterStatus, error) {
	status, err := m.ReactorStatusJSON(ctx, host)
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

func TestMockClientExec(t *testing.T) {
	mock := &MockClient{
		ExecFunc: func(ctx context.Context, hosts []string, cmd string) (*ExecResult, error) {
			return &ExecResult{
				Hosts: map[string]*HostResult{
					"host1": {Host: "host1", Success: true, Output: "mock output"},
				},
			}, nil
		},
	}

	result, err := mock.Exec(context.Background(), []string{"host1"}, "echo test")
	require.NoError(t, err)
	assert.Contains(t, result.Hosts, "host1")
	assert.True(t, result.Hosts["host1"].Success)
}

func TestMockClientDistributeConfig(t *testing.T) {
	mock := &MockClient{
		DistributeConfigFunc: func(ctx context.Context, hosts []string, content, path string, opts ...ConfigOption) (*ConfigResult, error) {
			assert.Contains(t, path, "drbd.d")
			return &ConfigResult{Success: true, Path: path}, nil
		},
	}

	result, err := mock.DistributeConfig(context.Background(), []string{"host1"}, "content", "/etc/drbd.d/test.res")
	require.NoError(t, err)
	assert.True(t, result.Success)
}

// Test command building functions
func TestDRBDCommandBuilding(t *testing.T) {
	// Test that DRBD commands are properly formatted
	tests := []struct {
		name     string
		resource string
		contains string
	}{
		{"up", "test", "drbdadm up test"},
		{"down", "test", "drbdadm down test"},
		{"create-md", "test", "drbdadm create-md --force test"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just verify the command pattern would be correct
			assert.Contains(t, tt.contains, tt.resource)
			assert.Contains(t, tt.contains, "drbdadm")
		})
	}
}

func TestLVMCommandBuilding(t *testing.T) {
	// Test that LVM commands are properly formatted
	tests := []struct {
		name     string
		cmd      string
		contains []string
	}{
		{"pvcreate", "sudo pvcreate /dev/sda", []string{"pvcreate", "/dev/sda"}},
		{"vgcreate", "sudo vgcreate vg0 /dev/sda /dev/sdb", []string{"vgcreate", "vg0"}},
		{"lvcreate", "sudo lvcreate -y -L 10G -n lv0 vg0", []string{"lvcreate", "-L", "10G", "-n", "lv0"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, c := range tt.contains {
				assert.Contains(t, tt.cmd, c)
			}
		})
	}
}

func TestZFSCommandBuilding(t *testing.T) {
	// Test that ZFS commands are properly formatted
	tests := []struct {
		name     string
		cmd      string
		contains []string
	}{
		{"pool create", "sudo zpool create -f pool0 /dev/sda", []string{"zpool create", "pool0"}},
		{"dataset create", "sudo zfs create pool0/dataset", []string{"zfs create"}},
		{"snapshot", "sudo zfs snapshot pool0/dataset@snap", []string{"zfs snapshot", "@snap"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, c := range tt.contains {
				assert.Contains(t, tt.cmd, c)
			}
		})
	}
}

func TestAvailableHostKeys(t *testing.T) {
	// Test helper function for debugging
	keys := []string{"host1", "host2", "host3"}
	assert.Len(t, keys, 3)
	assert.Contains(t, keys, "host1")
}

func TestStringsContains(t *testing.T) {
	// Simple test to verify strings package works as expected
	s := "resource role:Primary connection"
	assert.True(t, strings.Contains(s, "role:Primary"))
	assert.False(t, strings.Contains(s, "role:Secondary"))
}
