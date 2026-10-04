package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestReactorStatusJSONParsing(t *testing.T) {
	// Test JSON from drbd-reactorctl status --json
	jsonInput := `{
		"promoter": [
			{
				"drbd_resource": "ha_mysql",
				"path": "/etc/drbd-reactor.d/mysql_config.toml",
				"primary_on": "gui01",
				"target": {
					"name": "drbd-services@ha_mysql.target",
					"status": "active",
					"freezer": "running"
				},
				"dependencies": [
					{
						"name": "drbd-promote@ha_mysql.service",
						"status": "active",
						"freezer": "running"
					}
				],
				"status": "active"
			}
		],
		"prometheus": [
			{
				"path": "/etc/drbd-reactor.d/prometheus.toml",
				"address": "0.0.0.0:9942",
				"status": "active"
			}
		],
		"debugger": [],
		"umh": [],
		"agentx": []
	}`

	var status ReactorStatus
	err := json.Unmarshal([]byte(jsonInput), &status)
	assert.NoError(t, err)

	assert.Len(t, status.Promoter, 1)
	assert.Equal(t, "ha_mysql", status.Promoter[0].DRBDResource)
	assert.Equal(t, "/etc/drbd-reactor.d/mysql_config.toml", status.Promoter[0].Path)
	assert.Equal(t, "gui01", status.Promoter[0].PrimaryOn)
	assert.Equal(t, "active", status.Promoter[0].Status)
	assert.Equal(t, "drbd-services@ha_mysql.target", status.Promoter[0].Target.Name)
	assert.Equal(t, "active", status.Promoter[0].Target.Status)
	assert.Len(t, status.Promoter[0].Dependencies, 1)
	assert.Equal(t, "drbd-promote@ha_mysql.service", status.Promoter[0].Dependencies[0].Name)

	assert.Len(t, status.Prometheus, 1)
	assert.Equal(t, "/etc/drbd-reactor.d/prometheus.toml", status.Prometheus[0].Path)
	assert.Equal(t, "0.0.0.0:9942", status.Prometheus[0].Address)
	assert.Equal(t, "active", status.Prometheus[0].Status)
}

func TestReactorStatusEmptyPromoter(t *testing.T) {
	jsonInput := `{
		"promoter": [],
		"prometheus": [],
		"debugger": [],
		"umh": [],
		"agentx": []
	}`

	var status ReactorStatus
	err := json.Unmarshal([]byte(jsonInput), &status)
	assert.NoError(t, err)
	assert.Empty(t, status.Promoter)
}

func TestReactorPromoterStatusInactive(t *testing.T) {
	jsonInput := `{
		"promoter": [
			{
				"drbd_resource": "ha_fs2",
				"path": "/etc/drbd-reactor.d/ha_fs2.toml",
				"primary_on": "gui03",
				"target": {
					"name": "drbd-services@ha_fs2.target",
					"status": "inactive",
					"freezer": "running"
				},
				"dependencies": [
					{
						"name": "drbd-promote@ha_fs2.service",
						"status": "failed",
						"freezer": "running"
					}
				],
				"status": "inactive"
			}
		]
	}`

	var status ReactorStatus
	err := json.Unmarshal([]byte(jsonInput), &status)
	assert.NoError(t, err)

	assert.Len(t, status.Promoter, 1)
	assert.Equal(t, "ha_fs2", status.Promoter[0].DRBDResource)
	assert.Equal(t, "inactive", status.Promoter[0].Status)
	assert.Equal(t, "failed", status.Promoter[0].Dependencies[0].Status)
}

func TestReactorPromoterStatusByResource(t *testing.T) {
	mock := &MockClient{
		ExecFunc: func(ctx context.Context, hosts []string, cmd string) (*ExecResult, error) {
			return &ExecResult{
				Hosts: map[string]*HostResult{
					"localhost": {
						Host:    "localhost",
						Success: true,
						Output: `{
							"promoter": [
								{
									"drbd_resource": "resource1",
									"path": "/etc/drbd-reactor.d/resource1.toml",
									"primary_on": "node1",
									"target": {"name": "target1", "status": "active", "freezer": "running"},
									"dependencies": [],
									"status": "active"
								},
								{
									"drbd_resource": "resource2",
									"path": "/etc/drbd-reactor.d/resource2.toml",
									"primary_on": "node2",
									"target": {"name": "target2", "status": "inactive", "freezer": "running"},
									"dependencies": [],
									"status": "inactive"
								}
							]
						}`,
					},
				},
			}, nil
		},
	}

	promoter, err := mock.ReactorPromoterStatusByResource(context.Background(), "localhost", "resource1")
	assert.NoError(t, err)
	assert.Equal(t, "resource1", promoter.DRBDResource)
	assert.Equal(t, "node1", promoter.PrimaryOn)
	assert.Equal(t, "active", promoter.Status)

	promoter, err = mock.ReactorPromoterStatusByResource(context.Background(), "localhost", "resource2")
	assert.NoError(t, err)
	assert.Equal(t, "resource2", promoter.DRBDResource)
	assert.Equal(t, "inactive", promoter.Status)

	// Test non-existent resource
	_, err = mock.ReactorPromoterStatusByResource(context.Background(), "localhost", "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestExecResultEmptyHosts(t *testing.T) {
	result := &ExecResult{
		Hosts: make(map[string]*HostResult),
	}

	assert.True(t, result.AllSuccess())
	assert.Empty(t, result.FailedHosts())
}

func TestExecResultAllFailed(t *testing.T) {
	result := &ExecResult{
		Hosts: map[string]*HostResult{
			"host1": {Host: "host1", Success: false, Output: "error 1"},
			"host2": {Host: "host2", Success: false, Output: "error 2"},
		},
	}

	assert.False(t, result.AllSuccess())
	failed := result.FailedHosts()
	assert.Len(t, failed, 2)
}

func TestHostResultWithError(t *testing.T) {
	testErr := errors.New("command failed")
	result := &HostResult{
		Host:    "test-host",
		Output:  "error output",
		Success: false,
		Error:   testErr,
	}

	assert.Equal(t, "test-host", result.Host)
	assert.Equal(t, "error output", result.Output)
	assert.False(t, result.Success)
	assert.Equal(t, testErr, result.Error)
}

func TestConfigResultWithFailures(t *testing.T) {
	result := &ConfigResult{
		Path:    "/etc/drbd-reactor.d/test.toml",
		Success: false,
		Hosts: map[string]*HostResult{
			"host1": {Host: "host1", Success: true},
			"host2": {Host: "host2", Success: false, Error: errors.New("permission denied")},
		},
	}

	assert.False(t, result.Success)
	assert.Len(t, result.Hosts, 2)
}

func TestMockClientDRBDUp(t *testing.T) {
	mock := &MockClient{}

	result, err := mock.DRBDUp(context.Background(), []string{"host1"}, "resource1")
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Contains(t, result.Hosts, "host1")
}

func TestMockClientDRBDDown(t *testing.T) {
	mock := &MockClient{}

	result, err := mock.DRBDDown(context.Background(), []string{"host1"}, "resource1")
	assert.NoError(t, err)
	assert.NotNil(t, result)
}

func TestMockClientDRBDSecondary(t *testing.T) {
	mock := &MockClient{}

	result, err := mock.DRBDSecondary(context.Background(), "host1", "resource1")
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.True(t, result.Success)
}

func TestMockClientDRBDCreateMD(t *testing.T) {
	mock := &MockClient{}

	result, err := mock.DRBDCreateMD(context.Background(), []string{"host1"}, "resource1", 7)
	assert.NoError(t, err)
	assert.NotNil(t, result)
}

func TestMockClientDRBDStatusCustom(t *testing.T) {
	mock := &MockClient{
		DRBDStatusFunc: func(ctx context.Context, hosts []string, resource string) (*ExecResult, error) {
			return &ExecResult{
				Hosts: map[string]*HostResult{
					"host1": {Host: "host1", Success: true, Output: "resource role:Secondary disk:Inconsistent"},
				},
			}, nil
		},
	}

	result, err := mock.DRBDStatus(context.Background(), []string{"host1"}, "resource1")
	assert.NoError(t, err)
	assert.Contains(t, result.Hosts["host1"].Output, "role:Secondary")
}

func TestMockClientDRBDPrimaryCustom(t *testing.T) {
	mock := &MockClient{
		DRBDPrimaryFunc: func(ctx context.Context, host, resource string, force bool) (*HostResult, error) {
			if force {
				return &HostResult{Host: host, Success: true, Output: "forced primary"}, nil
			}
			return &HostResult{Host: host, Success: true, Output: "primary"}, nil
		},
	}

	result, err := mock.DRBDPrimary(context.Background(), "host1", "resource1", true)
	assert.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Output, "forced")
}

func TestMockClientDistributeConfigWithOpts(t *testing.T) {
	mock := &MockClient{
		DistributeConfigFunc: func(ctx context.Context, hosts []string, content, path string, opts ...ConfigOption) (*ConfigResult, error) {
			options := &configOptions{}
			for _, opt := range opts {
				opt(options)
			}
			assert.Equal(t, "systemctl reload drbd-reactor", options.postCommand)
			return &ConfigResult{Success: true, Path: path}, nil
		},
	}

	result, err := mock.DistributeConfig(
		context.Background(),
		[]string{"host1"},
		"content",
		"/etc/drbd.d/test.res",
		WithPostCommand("systemctl reload drbd-reactor"),
	)
	assert.NoError(t, err)
	assert.True(t, result.Success)
}

func TestMockClientDeleteConfig(t *testing.T) {
	mock := &MockClient{}

	err := mock.DeleteConfig(context.Background(), []string{"host1"}, "/etc/drbd.d/test.res")
	assert.NoError(t, err)
}

func TestMockClientLVMOperations(t *testing.T) {
	mock := &MockClient{}

	// Test PVCreate
	result, err := mock.PVCreate(context.Background(), []string{"host1"}, "/dev/sda")
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Test VGCreate
	result, err = mock.VGCreate(context.Background(), []string{"host1"}, "vg0", []string{"/dev/sda"})
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Test LVCreate
	result, err = mock.LVCreate(context.Background(), []string{"host1"}, "vg0", "lv0", "10G")
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Test LVCreateThinPool
	result, err = mock.LVCreateThinPool(context.Background(), []string{"host1"}, "vg0", "thinpool", "100G")
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Test LVCreateThinVolume
	result, err = mock.LVCreateThinVolume(context.Background(), []string{"host1"}, "vg0", "thinpool", "lv0", "10G")
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Test LVCreateSnapshot
	result, err = mock.LVCreateSnapshot(context.Background(), []string{"host1"}, "vg0", "lv0", "snap0", "1G")
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Test LVCreateThinSnapshot
	result, err = mock.LVCreateThinSnapshot(context.Background(), []string{"host1"}, "vg0", "lv0", "snap0")
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Test LVIsThin
	isThin, err := mock.LVIsThin(context.Background(), "host1", "vg0", "lv0")
	assert.NoError(t, err)
	assert.False(t, isThin)

	// Test LVRemoveSnapshot
	result, err = mock.LVRemoveSnapshot(context.Background(), []string{"host1"}, "vg0", "snap0")
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Test LVListSnapshots
	result, err = mock.LVListSnapshots(context.Background(), []string{"host1"}, "vg0")
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Test LVMergeSnapshot
	result, err = mock.LVMergeSnapshot(context.Background(), []string{"host1"}, "vg0", "snap0")
	assert.NoError(t, err)
	assert.NotNil(t, result)
}

func TestMockClientZFSOperations(t *testing.T) {
	mock := &MockClient{}

	// Test ZFSCreatePool
	result, err := mock.ZFSCreatePool(context.Background(), []string{"host1"}, "pool0", []string{"/dev/sda"})
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Test ZFSDestroyPool
	result, err = mock.ZFSDestroyPool(context.Background(), []string{"host1"}, "pool0")
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Test ZFSListPools
	result, err = mock.ZFSListPools(context.Background(), []string{"host1"})
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Test ZFSCreateDataset
	result, err = mock.ZFSCreateDataset(context.Background(), []string{"host1"}, "pool0/dataset")
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Test ZFSCreateThinDataset
	result, err = mock.ZFSCreateThinDataset(context.Background(), []string{"host1"}, "pool0", "vol0", "10G")
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Test ZFSDestroyDataset
	result, err = mock.ZFSDestroyDataset(context.Background(), []string{"host1"}, "pool0/dataset")
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Test ZFSSnapshot
	result, err = mock.ZFSSnapshot(context.Background(), []string{"host1"}, "pool0/dataset", "snap0")
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Test ZFSRollback
	result, err = mock.ZFSRollback(context.Background(), []string{"host1"}, "pool0/dataset", "snap0")
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Test ZFSClone
	result, err = mock.ZFSClone(context.Background(), []string{"host1"}, "pool0/dataset@snap0", "pool0/clone")
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Test ZFSListSnapshots
	result, err = mock.ZFSListSnapshots(context.Background(), []string{"host1"}, "pool0/dataset")
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Test ZFSDestroySnapshot
	result, err = mock.ZFSDestroySnapshot(context.Background(), []string{"host1"}, "pool0/dataset@snap0")
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Test ZFSSetQuota
	result, err = mock.ZFSSetQuota(context.Background(), []string{"host1"}, "pool0/dataset", "10G")
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Test ZFSResizeVolume
	result, err = mock.ZFSResizeVolume(context.Background(), []string{"host1"}, "pool0/vol0", "20G")
	assert.NoError(t, err)
	assert.NotNil(t, result)
}

func TestMockClientReactorReload(t *testing.T) {
	mock := &MockClient{}

	result, err := mock.ReactorReload(context.Background(), []string{"host1"})
	assert.NoError(t, err)
	assert.NotNil(t, result)
}

func TestExecResultSingleHost(t *testing.T) {
	result := &ExecResult{
		Hosts: map[string]*HostResult{
			"host1": {Host: "host1", Success: true, Output: "success output"},
		},
	}

	assert.True(t, result.AllSuccess())
	assert.Empty(t, result.FailedHosts())
}

func TestExecResultMixedResults(t *testing.T) {
	result := &ExecResult{
		Hosts: map[string]*HostResult{
			"host1": {Host: "host1", Success: true, Output: "ok"},
			"host2": {Host: "host2", Success: false, Output: "error"},
			"host3": {Host: "host3", Success: true, Output: "ok"},
			"host4": {Host: "host4", Success: false, Output: "timeout"},
		},
	}

	assert.False(t, result.AllSuccess())
	failed := result.FailedHosts()
	assert.Len(t, failed, 2)
	assert.Contains(t, failed, "host2")
	assert.Contains(t, failed, "host4")
}

func TestConfigOptionsDefault(t *testing.T) {
	opts := &configOptions{}
	assert.False(t, opts.backup)
	assert.Empty(t, opts.postCommand)
}

func TestExecOptionsDefault(t *testing.T) {
	opts := &execOptions{}
	assert.Equal(t, 0, opts.parallel)
	assert.Equal(t, time.Duration(0), opts.timeout)
}

func TestLVMOptionsDefault(t *testing.T) {
	opts := &lvmOptions{}
	assert.False(t, opts.force)
}

func TestZFSOptionsDefault(t *testing.T) {
	opts := &zfsOptions{}
	assert.Empty(t, opts.compression)
	assert.False(t, opts.dedup)
}

func TestWithBackup(t *testing.T) {
	opts := &configOptions{}
	WithBackup(true)(opts)
	assert.True(t, opts.backup)

	WithBackup(false)(opts)
	assert.False(t, opts.backup)
}

func TestWithLVMForce(t *testing.T) {
	opts := &lvmOptions{}
	WithLVMForce(true)(opts)
	assert.True(t, opts.force)
}

func TestWithZFSCompression(t *testing.T) {
	opts := &zfsOptions{}
	WithZFSCompression("zstd")(opts)
	assert.Equal(t, "zstd", opts.compression)
}

func TestWithZFSDedup(t *testing.T) {
	opts := &zfsOptions{}
	WithZFSDedup(true)(opts)
	assert.True(t, opts.dedup)
}

func TestHostResultNilError(t *testing.T) {
	result := &HostResult{
		Host:    "host1",
		Success: true,
		Error:   nil,
	}

	assert.Nil(t, result.Error)
	assert.True(t, result.Success)
}

func TestConfigResultPath(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"drbd config", "/etc/drbd.d/resource.res"},
		{"reactor config", "/etc/drbd-reactor.d/promoter.toml"},
		{"lvm config", "/etc/lvm/lvm.conf"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &ConfigResult{Path: tt.path, Success: true}
			assert.Equal(t, tt.path, result.Path)
		})
	}
}

func TestMockClientExecWithEmptyHosts(t *testing.T) {
	mock := &MockClient{
		ExecFunc: func(ctx context.Context, hosts []string, cmd string) (*ExecResult, error) {
			return &ExecResult{Hosts: make(map[string]*HostResult)}, nil
		},
	}

	result, err := mock.Exec(context.Background(), []string{}, "echo test")
	assert.NoError(t, err)
	assert.Empty(t, result.Hosts)
}

func TestMockClientExecError(t *testing.T) {
	expectedErr := errors.New("connection refused")
	mock := &MockClient{
		ExecFunc: func(ctx context.Context, hosts []string, cmd string) (*ExecResult, error) {
			return nil, expectedErr
		},
	}

	result, err := mock.Exec(context.Background(), []string{"host1"}, "echo test")
	assert.Error(t, err)
	assert.Equal(t, expectedErr, err)
	assert.Nil(t, result)
}
