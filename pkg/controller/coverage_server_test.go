package controller

import (
	"context"
	"testing"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newServerWithNodes builds a Server backed by a fake deployment client and an
// in-memory DB, with the given nodes registered.
func newServerWithNodes(t *testing.T, nodes ...string) (*Server, *Controller) {
	t.Helper()
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	for i, n := range nodes {
		_, err := ctrl.nodes.RegisterNode(context.Background(), n, "10.0.0."+string(rune('1'+i)))
		require.NoError(t, err)
	}
	if len(nodes) > 0 {
		require.NoError(t, ctrl.loadHostsFromDatabase(context.Background()))
	}
	return NewServer(ctrl), ctrl
}

func TestServerNodeHandlers(t *testing.T) {
	srv, ctrl := newServerWithNodes(t)
	ctx := context.Background()

	// Register.
	rr, err := srv.RegisterNode(ctx, &sdspb.RegisterNodeRequest{Name: "n1", Address: "10.0.0.1"})
	require.NoError(t, err)
	assert.True(t, rr.Success)

	// Get existing + missing (lookup is by address).
	gr, err := srv.GetNode(ctx, &sdspb.GetNodeRequest{Address: "10.0.0.1"})
	require.NoError(t, err)
	assert.True(t, gr.Success)
	gr2, err := srv.GetNode(ctx, &sdspb.GetNodeRequest{Address: "10.9.9.9"})
	require.NoError(t, err)
	assert.False(t, gr2.Success)

	// Labels.
	slr, err := srv.SetNodeLabels(ctx, &sdspb.SetNodeLabelsRequest{
		Node: "n1", Labels: map[string]string{"rack": "A"}, Replace: true,
	})
	require.NoError(t, err)
	assert.True(t, slr.Success)

	// List.
	lr, err := srv.ListNodes(ctx, &sdspb.ListNodesRequest{})
	require.NoError(t, err)
	assert.True(t, lr.Success)
	assert.NotEmpty(t, lr.Nodes)

	// Unregister.
	ur, err := srv.UnregisterNode(ctx, &sdspb.UnregisterNodeRequest{Address: "10.0.0.1"})
	require.NoError(t, err)
	assert.True(t, ur.Success)

	_ = ctrl
}

func TestServerHealthCheck(t *testing.T) {
	srv, _ := newServerWithNodes(t, "n1", "n2")
	resp, err := srv.HealthCheck(context.Background(), &sdspb.HealthCheckRequest{})
	require.NoError(t, err)
	require.NotNil(t, resp)
}

func TestServerZFSHandlers(t *testing.T) {
	srv, _ := newServerWithNodes(t, "n1")
	ctx := context.Background()

	cp, err := srv.CreateZFSPool(ctx, &sdspb.CreateZFSPoolRequest{Name: "tank", Node: "n1", Vdevs: []string{"/dev/sdb"}})
	require.NoError(t, err)
	assert.True(t, cp.Success)

	_, err = srv.ListZFSpools(ctx, &sdspb.ListZFSPoolsRequest{})
	require.NoError(t, err)

	_, err = srv.CreateZFSDataset(ctx, &sdspb.CreateZFSDatasetRequest{DatasetPath: "tank/ds1", Node: "n1"})
	require.NoError(t, err)

	_, err = srv.CreateZFSVolume(ctx, &sdspb.CreateZFSVolumeRequest{PoolName: "tank", VolumeName: "vol1", Size: "1G", Node: "n1"})
	require.NoError(t, err)

	_, err = srv.CreateZFSSnapshot(ctx, &sdspb.CreateZFSSnapshotRequest{Dataset: "tank/ds1", SnapshotName: "snap1", Node: "n1"})
	require.NoError(t, err)

	_, err = srv.ListZFSSnapshots(ctx, &sdspb.ListZFSSnapshotsRequest{Dataset: "tank/ds1", Node: "n1"})
	require.NoError(t, err)

	_, err = srv.DeleteZFSSnapshot(ctx, &sdspb.DeleteZFSSnapshotRequest{Snapshot: "tank/ds1@snap1", Node: "n1"})
	require.NoError(t, err)

	_, err = srv.ResizeZFSVolume(ctx, &sdspb.ResizeZFSVolumeRequest{VolumePath: "tank/vol1", NewSize: "2G", Node: "n1"})
	require.NoError(t, err)

	_, err = srv.CloneZFSSnapshot(ctx, &sdspb.CloneZFSSnapshotRequest{Snapshot: "tank/ds1@snap1", ClonePath: "tank/clone1", Node: "n1"})
	require.NoError(t, err)

	_, err = srv.RestoreZFSSnapshot(ctx, &sdspb.RestoreZFSSnapshotRequest{Dataset: "tank/ds1", SnapshotName: "snap1", Node: "n1"})
	require.NoError(t, err)

	_, err = srv.DeleteZFSDataset(ctx, &sdspb.DeleteZFSDatasetRequest{DatasetPath: "tank/ds1", Node: "n1"})
	require.NoError(t, err)

	_, err = srv.DeleteZFSPool(ctx, &sdspb.DeleteZFSPoolRequest{Name: "tank", Node: "n1"})
	require.NoError(t, err)
}

func TestServerLvmSnapshotHandlers(t *testing.T) {
	srv, ctrl := newServerWithNodes(t, "n1")
	ctx := context.Background()
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "res1", Nodes: "n1"}))
	require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{ResourceName: "res1", VolumeID: 0, Pool: "vg0"}))

	_, err := srv.CreateLvmSnapshot(ctx, &sdspb.CreateLvmSnapshotRequest{
		Resource: "res1", LvName: "res1_data", SnapshotName: "snap1", Node: "n1", Size: "1G",
	})
	require.NoError(t, err)

	_, err = srv.ListLvmSnapshots(ctx, &sdspb.ListLvmSnapshotsRequest{LvName: "res1_data", Node: "n1"})
	require.NoError(t, err)

	_, err = srv.DeleteLvmSnapshot(ctx, &sdspb.DeleteLvmSnapshotRequest{LvName: "res1_data", SnapshotName: "snap1", Node: "n1"})
	require.NoError(t, err)
}

func TestServerSnapshotScheduleHandlers(t *testing.T) {
	srv, _ := newServerWithNodes(t, "n1")
	ctx := context.Background()

	cr, err := srv.CreateSnapshotSchedule(ctx, &sdspb.CreateSnapshotScheduleRequest{
		Resource: "res1", Cron: "0 * * * *", Enabled: true,
		Keep: &sdspb.GFSRetention{Hourly: 6, Daily: 7},
	})
	require.NoError(t, err)
	require.NotNil(t, cr)

	lr, err := srv.ListSnapshotSchedules(ctx, &sdspb.ListSnapshotSchedulesRequest{})
	require.NoError(t, err)
	assert.True(t, lr.Success)

	dr, err := srv.DeleteSnapshotSchedule(ctx, &sdspb.DeleteSnapshotScheduleRequest{Name: "res1"})
	require.NoError(t, err)
	require.NotNil(t, dr)
}

func TestServerHaAndSelfHaReads(t *testing.T) {
	srv, ctrl := newServerWithNodes(t, "n1", "n2")
	ctx := context.Background()

	require.NoError(t, ctrl.db.SaveHaConfig(ctx, &database.HaConfig{Resource: "res1", VIP: "10.0.0.100/24"}))
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "res1", Nodes: "n1,n2"}))

	lr, err := srv.ListHa(ctx, &sdspb.ListHaRequest{})
	require.NoError(t, err)
	assert.True(t, lr.Success)

	gr, err := srv.GetHa(ctx, &sdspb.GetHaRequest{Resource: "res1"})
	require.NoError(t, err)
	require.NotNil(t, gr)

	sr, err := srv.GetSelfHaStatus(ctx, &sdspb.GetSelfHaStatusRequest{})
	require.NoError(t, err)
	require.NotNil(t, sr)
}

func TestServerGatewayReadHandlers(t *testing.T) {
	srv, ctrl := newServerWithNodes(t, "n1")
	ctx := context.Background()
	require.NoError(t, ctrl.db.SaveGateway(ctx, &database.Gateway{
		Name: "gw1", Resource: "res1", Type: database.GatewayType("nfs"),
		Config: map[string]any{"export_directory": "/exports/res1"},
	}))

	lr, err := srv.ListGateways(ctx, &sdspb.ListGatewaysRequest{})
	require.NoError(t, err)
	require.NotNil(t, lr)

	gr, err := srv.GetGateway(ctx, &sdspb.GetGatewayRequest{Id: "res1"})
	require.NoError(t, err)
	require.NotNil(t, gr)
}

func TestServerResourceOpHandlers(t *testing.T) {
	srv, ctrl := newServerWithNodes(t, "n1", "n2")
	ctx := context.Background()
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "res1", Nodes: "n1,n2", Port: 7001}))
	require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{ResourceName: "res1", VolumeID: 0, Pool: "vg0", VolumeName: "res1_data"}))

	// These delegate to the resource manager over the fake deployment; assert
	// only that the handler runs and returns a response (no gRPC-level error).
	_, err := srv.AddVolume(ctx, &sdspb.AddVolumeRequest{Resource: "res1", Volume: "res1_data2", Pool: "vg0", SizeGb: 1})
	require.NoError(t, err)

	_, err = srv.UpdateResourceOptions(ctx, &sdspb.UpdateResourceOptionsRequest{Name: "res1", Options: map[string]string{"c-max-rate": "100M"}})
	require.NoError(t, err)

	_, err = srv.ResizeVolume(ctx, &sdspb.ResizeVolumeRequest{Resource: "res1", VolumeId: 0, SizeGb: 5})
	require.NoError(t, err)

	_, err = srv.CreateFilesystem(ctx, &sdspb.CreateFilesystemRequest{Resource: "res1", VolumeId: 0, Fstype: "ext4", Node: "n1"})
	require.NoError(t, err)

	_, err = srv.MountResource(ctx, &sdspb.MountResourceRequest{Resource: "res1", VolumeId: 0, Path: "/mnt/res1", Node: "n1", Fstype: "ext4"})
	require.NoError(t, err)

	_, err = srv.UnmountResource(ctx, &sdspb.UnmountResourceRequest{Resource: "res1", VolumeId: 0, Node: "n1"})
	require.NoError(t, err)

	_, err = srv.RemoveVolume(ctx, &sdspb.RemoveVolumeRequest{Resource: "res1", VolumeId: 0})
	require.NoError(t, err)
}

func TestServerListResourceAgents(t *testing.T) {
	srv, _ := newServerWithNodes(t, "n1")
	resp, err := srv.ListResourceAgents(context.Background(), &sdspb.ListResourceAgentsRequest{})
	require.NoError(t, err)
	require.NotNil(t, resp)
}

func TestServerAddDiskToPool(t *testing.T) {
	srv, _ := newServerWithNodes(t, "n1")
	resp, err := srv.AddDiskToPool(context.Background(), &sdspb.AddDiskToPoolRequest{Pool: "vg0", Disk: "/dev/sdc", Node: "n1"})
	require.NoError(t, err)
	require.NotNil(t, resp)
}

// TestServerGatewaySubResourceHandlers exercises the NFS export / iSCSI LUN &
// initiator & CHAP / NVMe namespace & host RPCs. With no live reactor config
// they take the error path, but the handler bodies (manager construction,
// request marshalling, response mapping) all execute. Each must return a
// non-nil response and no gRPC-level error.
func TestServerGatewaySubResourceHandlers(t *testing.T) {
	srv, _ := newServerWithNodes(t, "n1")
	ctx := context.Background()
	res := "res1"

	calls := []func() (any, error){
		func() (any, error) {
			return srv.AddNFSExport(ctx, &sdspb.AddNFSExportRequest{Resource: res, ExportPath: "/exp", Fsid: 1, ClientSpec: "*", Options: "rw"})
		},
		func() (any, error) {
			return srv.RemoveNFSExport(ctx, &sdspb.RemoveNFSExportRequest{Resource: res, ExportPath: "/exp"})
		},
		func() (any, error) {
			return srv.ListNFSExports(ctx, &sdspb.ListNFSExportsRequest{Resource: res})
		},
		func() (any, error) {
			return srv.AddISCSILUN(ctx, &sdspb.AddISCSILUNRequest{Resource: res, Lun: 1, Device: "/dev/drbd0"})
		},
		func() (any, error) {
			return srv.RemoveISCSILUN(ctx, &sdspb.RemoveISCSILUNRequest{Resource: res, Lun: 1})
		},
		func() (any, error) {
			return srv.ListISCSILUNs(ctx, &sdspb.ListISCSILUNsRequest{Resource: res})
		},
		func() (any, error) {
			return srv.AddISCSIInitiator(ctx, &sdspb.AddISCSIInitiatorRequest{Resource: res, Initiator: "iqn.init"})
		},
		func() (any, error) {
			return srv.RemoveISCSIInitiator(ctx, &sdspb.RemoveISCSIInitiatorRequest{Resource: res, Initiator: "iqn.init"})
		},
		func() (any, error) {
			return srv.ListISCSIInitiators(ctx, &sdspb.ListISCSIInitiatorsRequest{Resource: res})
		},
		func() (any, error) {
			return srv.SetISCSIChap(ctx, &sdspb.SetISCSIChapRequest{Resource: res, Username: "u", Password: "p", Mutual: true})
		},
		func() (any, error) {
			return srv.GetISCSIChap(ctx, &sdspb.GetISCSIChapRequest{Resource: res})
		},
		func() (any, error) {
			return srv.AddNVMeNamespace(ctx, &sdspb.AddNVMeNamespaceRequest{Resource: res, Device: "/dev/drbd0"})
		},
		func() (any, error) {
			return srv.RemoveNVMeNamespace(ctx, &sdspb.RemoveNVMeNamespaceRequest{Resource: res, NamespaceId: 1})
		},
		func() (any, error) {
			return srv.ListNVMeNamespaces(ctx, &sdspb.ListNVMeNamespacesRequest{Resource: res})
		},
		func() (any, error) {
			return srv.AddNVMeHost(ctx, &sdspb.AddNVMeHostRequest{Resource: res, HostNqn: "nqn.host"})
		},
		func() (any, error) {
			return srv.RemoveNVMeHost(ctx, &sdspb.RemoveNVMeHostRequest{Resource: res, HostNqn: "nqn.host"})
		},
		func() (any, error) {
			return srv.ListNVMeHosts(ctx, &sdspb.ListNVMeHostsRequest{Resource: res})
		},
	}
	for i, call := range calls {
		resp, err := call()
		require.NoErrorf(t, err, "call %d", i)
		require.NotNilf(t, resp, "call %d", i)
	}
}
