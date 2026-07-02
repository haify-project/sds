package controller

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// A representative .res for a foreign, hand-configured DRBD resource ("kaiwudb")
// that SDS does not yet know about. Two diskful nodes, port 7789, one volume
// backed by an LVM logical volume.
const foreignKaiwudbRes = `resource kaiwudb {
    net {
        protocol C;
    }

    volume 0 {
        device    minor 12;
        disk      /dev/vg0/kaiwudb_data;
        meta-disk internal;
    }

    on node1 {
        address   10.0.0.1:7789;
        node-id   0;
    }

    on node2 {
        address   10.0.0.2:7789;
        node-id   1;
    }
}
`

func TestResourceManagerAdoptResourceAutoDiscovers(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.Contains(cmd, "cat /etc/drbd.d/kaiwudb.res") {
				return successExecResult(hosts, foreignKaiwudbRes), nil
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.nodes.nodes["10.0.0.2"] = &NodeInfo{Name: "node2", Address: "10.0.0.2"}
	ctrl.hostsMap["node1"] = "10.0.0.1"
	ctrl.hostsMap["node2"] = "10.0.0.2"

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	// No flags: everything is auto-discovered from the live .res.
	result, err := ctrl.resources.AdoptResource(context.Background(), "kaiwudb", nil, 0, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []string{"node1", "node2"}, result.Nodes)
	assert.Equal(t, uint32(7789), result.Port)
	assert.Equal(t, "C", result.Protocol)
	assert.Equal(t, 1, result.Volumes)

	// The SDS database now carries the resource record so ops like MakeHa work.
	stored, err := ctrl.db.GetResource(context.Background(), "kaiwudb")
	require.NoError(t, err)
	assert.Equal(t, "node1,node2", stored.Nodes)
	assert.Equal(t, 7789, stored.Port)
	assert.Equal(t, "C", stored.Protocol)
	assert.Equal(t, 2, stored.Replicas)

	// ...and a volume record with the parsed backing path/pool.
	volumes, err := ctrl.db.ListVolumes(context.Background(), "kaiwudb")
	require.NoError(t, err)
	require.Len(t, volumes, 1)
	assert.Equal(t, 0, volumes[0].VolumeID)
	assert.Equal(t, "kaiwudb_data", volumes[0].VolumeName)
	assert.Equal(t, "vg0", volumes[0].Pool)
	assert.Equal(t, "/dev/vg0/kaiwudb_data", volumes[0].Device)

	// Adoption MUST NOT touch the DRBD resource or its data: no create-md, no
	// drbdadm up, no lvcreate, no mkfs anywhere.
	assert.Empty(t, dep.drbdCreateMDCalls, "adopt must not create DRBD metadata")
	assert.Empty(t, dep.drbdUpCalls, "adopt must not bring DRBD up")
	assert.Empty(t, dep.lvCreateCalls, "adopt must not create logical volumes")
	assert.Empty(t, dep.lvCreateThinVolumeCalls, "adopt must not create thin volumes")
	for _, call := range dep.execCalls {
		assert.NotContains(t, call.cmd, "create-md", "adopt must not run drbdadm create-md")
		assert.NotContains(t, call.cmd, "lvcreate", "adopt must not run lvcreate")
		assert.NotContains(t, call.cmd, "mkfs", "adopt must not run mkfs")
		assert.NotContains(t, call.cmd, "drbdadm up", "adopt must not run drbdadm up")
	}
}

func TestResourceManagerAdoptResourceFlagsOverride(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.Contains(cmd, "cat /etc/drbd.d/kaiwudb.res") {
				return successExecResult(hosts, foreignKaiwudbRes), nil
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.hostsMap["node1"] = "10.0.0.1"

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	// Explicit flags override the discovered nodes/port/protocol.
	result, err := ctrl.resources.AdoptResource(context.Background(), "kaiwudb",
		[]string{"node1", "node2", "node3"}, 8000, "A")
	require.NoError(t, err)
	assert.Equal(t, []string{"node1", "node2", "node3"}, result.Nodes)
	assert.Equal(t, uint32(8000), result.Port)
	assert.Equal(t, "A", result.Protocol)

	stored, err := ctrl.db.GetResource(context.Background(), "kaiwudb")
	require.NoError(t, err)
	assert.Equal(t, "node1,node2,node3", stored.Nodes)
	assert.Equal(t, 8000, stored.Port)
	assert.Equal(t, "A", stored.Protocol)
	assert.Equal(t, 3, stored.Replicas)
}

func TestResourceManagerAdoptResourceMissingIsRejected(t *testing.T) {
	// The .res is absent on every node: `cat ... || true` yields empty output.
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.hostsMap["node1"] = "10.0.0.1"

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	_, err = ctrl.resources.AdoptResource(context.Background(), "ghost", nil, 0, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// Nothing was fabricated in the database.
	_, err = ctrl.db.GetResource(context.Background(), "ghost")
	assert.Error(t, err)
}

func TestResourceManagerAdoptResourceIsIdempotent(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.Contains(cmd, "cat /etc/drbd.d/kaiwudb.res") {
				return successExecResult(hosts, foreignKaiwudbRes), nil
			}
			return successExecResult(hosts, ""), nil
		},
	}
	ctrl := newBasicTestController(dep)
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{Name: "node1", Address: "10.0.0.1"}
	ctrl.hostsMap["node1"] = "10.0.0.1"

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	_, err = ctrl.resources.AdoptResource(context.Background(), "kaiwudb", nil, 0, "")
	require.NoError(t, err)
	// Re-adopting updates the record rather than erroring, and does not
	// accumulate duplicate volume records.
	_, err = ctrl.resources.AdoptResource(context.Background(), "kaiwudb", nil, 0, "")
	require.NoError(t, err)

	volumes, err := ctrl.db.ListVolumes(context.Background(), "kaiwudb")
	require.NoError(t, err)
	assert.Len(t, volumes, 1)
}
