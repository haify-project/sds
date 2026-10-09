package controller

import (
	"context"
	"path/filepath"
	"testing"

	haifypb "github.com/haify-project/haify/api/proto/v1"
	"github.com/haify-project/haify/pkg/config"
	"github.com/haify-project/haify/pkg/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// newTestDB opens a throwaway metadata database under the test's own temp
// directory and closes it when the test ends.
//
// The close is registered after t.TempDir's removal and so runs before it
// (Cleanup is LIFO): the bolt file is released while the directory it lives in
// still exists. Its error is dropped on purpose — every assertion the test
// makes has already run against the open handle, and bolt fsyncs at commit
// rather than at close, so a failure to unmap the file cannot invalidate
// anything the test observed.
func newTestDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "haify.db")}, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestControllerNormalizeAndResolveHost(t *testing.T) {
	dep := &fakeDeploymentClient{}
	c := newBasicTestController(dep)
	c.db = newTestDB(t)
	c.config = &config.Config{Server: config.ServerConfig{Port: 3374}}

	assert.Equal(t, "10.0.0.1:3374", c.NormalizeHost("10.0.0.1:3374"))
	assert.Equal(t, "node1", c.NormalizeHost("node1"))

	// Register node via NodeManager
	_, err := c.nodes.RegisterNode(context.Background(), "node1", "192.168.1.10")
	require.NoError(t, err)

	require.NoError(t, c.loadHostsFromDatabase(context.Background()))
	assert.Equal(t, "192.168.1.10", c.ResolveHost("node1"))
	assert.Equal(t, "10.0.0.99", c.ResolveHost("10.0.0.99"))
}

func TestResourceManagerDrainAndUndrain(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)

	// Register node n1 and n2 via NodeManager
	_, err := ctrl.nodes.RegisterNode(context.Background(), "n1", "10.0.0.1")
	require.NoError(t, err)
	_, err = ctrl.nodes.RegisterNode(context.Background(), "n2", "10.0.0.2")
	require.NoError(t, err)

	// Drain unregistered node -> error
	_, err = ctrl.resources.DrainNode(context.Background(), "unknown-node")
	assert.Error(t, err)

	// Drain registered node with no resources
	moved, err := ctrl.resources.DrainNode(context.Background(), "n1")
	require.NoError(t, err)
	assert.Empty(t, moved)

	// Verify node state is now maintenance
	n1, err := ctrl.db.GetNode(context.Background(), "10.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, string(NodeStateMaintenance), n1.State)

	// Undrain n1
	require.NoError(t, ctrl.resources.UndrainNode(context.Background(), "n1"))
	n1, err = ctrl.db.GetNode(context.Background(), "10.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, string(NodeStateOnline), n1.State)

	// Undrain unregistered node -> error
	err = ctrl.resources.UndrainNode(context.Background(), "unknown-node")
	assert.Error(t, err)
}

func TestResourceManagerGetHaStatus(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)

	// Empty HA status
	statuses, err := ctrl.resources.GetHaStatus(context.Background(), "")
	require.NoError(t, err)
	assert.Empty(t, statuses)

	// Save HA config
	require.NoError(t, ctrl.db.SaveHaConfig(context.Background(), &database.HaConfig{
		Resource: "res1",
		VIP:      "10.0.0.100/24",
	}))

	// Save Resource
	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name:  "res1",
		Nodes: "n1,n2",
	}))

	statuses, err = ctrl.resources.GetHaStatus(context.Background(), "res1")
	require.NoError(t, err)
	assert.Empty(t, statuses)
}

func TestServerDrainAndUndrainHandlers(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	srv := NewServer(ctrl)

	_, err := ctrl.nodes.RegisterNode(context.Background(), "n1", "10.0.0.1")
	require.NoError(t, err)

	// DrainNode handler
	drainResp, err := srv.DrainNode(context.Background(), &haifypb.DrainNodeRequest{Name: "n1"})
	require.NoError(t, err)
	assert.True(t, drainResp.Success)

	// UndrainNode handler
	undrainResp, err := srv.UndrainNode(context.Background(), &haifypb.UndrainNodeRequest{Name: "n1"})
	require.NoError(t, err)
	assert.True(t, undrainResp.Success)

	// GetHaStatus handler
	require.NoError(t, ctrl.db.SaveHaConfig(context.Background(), &database.HaConfig{Resource: "res1"}))
	haStatusResp, err := srv.GetHaStatus(context.Background(), &haifypb.GetHaStatusRequest{Resource: "res1"})
	require.NoError(t, err)
	assert.True(t, haStatusResp.Success)
}

func TestDisklessClientHelpers(t *testing.T) {
	vols := dedupResourceVolumes([]resourceConfigVolume{
		{VolumeID: 0, Minor: 100},
		{VolumeID: 0, Minor: 100},
		{VolumeID: 1, Minor: 101},
	})
	assert.Len(t, vols, 2)

	content := "resource res1 {\n}"
	res, err := insertBeforeResourceClose(content, "    # test\n")
	require.NoError(t, err)
	assert.Contains(t, res, "# test")

	_, err = insertBeforeResourceClose("invalid file without closing bracket", "test")
	assert.Error(t, err)
}

func TestServerPoolAndResourceHandlers(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	srv := NewServer(ctrl)

	ctx := context.Background()
	_, err := ctrl.nodes.RegisterNode(ctx, "n1", "10.0.0.1")
	require.NoError(t, err)

	// Pool Handlers
	cResp, err := srv.CreatePool(ctx, &haifypb.CreatePoolRequest{Name: "vg0", Type: "lvm", Node: "n1", Disks: []string{"/dev/sdb"}, SizeGb: 100})
	require.NoError(t, err)
	assert.True(t, cResp.Success)

	gResp, err := srv.GetPool(ctx, &haifypb.GetPoolRequest{Name: "vg0", Node: "n1"})
	require.NoError(t, err)
	assert.True(t, gResp.Success)

	lResp, err := srv.ListPools(ctx, &haifypb.ListPoolsRequest{})
	require.NoError(t, err)
	assert.True(t, lResp.Success)

	dResp, err := srv.DeletePool(ctx, &haifypb.DeletePoolRequest{Name: "vg0", Node: "n1"})
	require.NoError(t, err)
	assert.True(t, dResp.Success)

	// Resource Handlers
	crResp, err := srv.CreateResource(ctx, &haifypb.CreateResourceRequest{
		Name: "res1", Port: 7001, Nodes: []string{"n1"}, Protocol: "C", SizeGb: 10, Pool: "vg0", StorageType: "lvm",
	})
	require.NoError(t, err)
	assert.True(t, crResp.Success)

	grResp, err := srv.GetResource(ctx, &haifypb.GetResourceRequest{Name: "res1"})
	require.NoError(t, err)
	assert.True(t, grResp.Success)

	lrResp, err := srv.ListResources(ctx, &haifypb.ListResourcesRequest{})
	require.NoError(t, err)
	assert.True(t, lrResp.Success)

	rsResp, err := srv.ResourceStatus(ctx, &haifypb.ResourceStatusRequest{Name: "res1"})
	require.NoError(t, err)
	assert.True(t, rsResp.Success)

	spResp, err := srv.SetPrimary(ctx, &haifypb.SetPrimaryRequest{Resource: "res1", Node: "n1"})
	require.NoError(t, err)
	assert.True(t, spResp.Success)

	ssResp, err := srv.SetSecondary(ctx, &haifypb.SetSecondaryRequest{Resource: "res1", Node: "n1"})
	require.NoError(t, err)
	assert.True(t, ssResp.Success)

	drResp, err := srv.DeleteResource(ctx, &haifypb.DeleteResourceRequest{Name: "res1"})
	require.NoError(t, err)
	assert.True(t, drResp.Success)
}

func TestServerSnapshotAndGatewayHandlers(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	srv := NewServer(ctrl)

	ctx := context.Background()
	_, err := ctrl.nodes.RegisterNode(ctx, "n1", "10.0.0.1")
	require.NoError(t, err)

	// Save dummy resource and volume
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "res1", Nodes: "n1"}))
	require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{ResourceName: "res1", VolumeID: 0, Pool: "vg0"}))

	// Snapshot Handlers
	csResp, err := srv.CreateSnapshot(ctx, &haifypb.CreateSnapshotRequest{Volume: "vg0/res1_data", SnapshotName: "snap1", Node: "n1"})
	require.NoError(t, err)
	assert.True(t, csResp.Success)

	lsResp, err := srv.ListSnapshots(ctx, &haifypb.ListSnapshotsRequest{Volume: "vg0/res1_data", Node: "n1"})
	require.NoError(t, err)
	assert.True(t, lsResp.Success)

	dsResp, err := srv.DeleteSnapshot(ctx, &haifypb.DeleteSnapshotRequest{Volume: "vg0/res1_data", SnapshotName: "snap1", Node: "n1"})
	require.NoError(t, err)
	assert.True(t, dsResp.Success)
}

func TestNamingHelpers(t *testing.T) {
	assert.Equal(t, "haify_vg0", normalizeManagedName("vg0"))
	assert.Equal(t, "haify_vg0", normalizeManagedName("haify_vg0"))

	pt, err := normalizeLVMPoolType("lvm-thin")
	require.NoError(t, err)
	assert.Equal(t, "thin_pool", pt)

	_, err = normalizeLVMPoolType("invalid-type")
	assert.Error(t, err)

	assert.Equal(t, "haify_ds1", normalizeManagedZFSPath("ds1"))

	bytes, err := parseByteCount("1073741824")
	require.NoError(t, err)
	assert.Equal(t, uint64(1073741824), bytes)

	_, err = parseByteCount("invalid")
	assert.Error(t, err)

	name, total, free, pv, ok := parseLVMPoolLine("vg0 | 10000000 | 5000000 | /dev/sdb")
	assert.True(t, ok)
	assert.Equal(t, "vg0", name)
	assert.Equal(t, "/dev/sdb", pv)
	assert.True(t, total > 0)
	assert.True(t, free > 0)

	snap, ds, cr, ok := parseZFSSnapshotLine("tank/ds@snap1 10G 5G 2026-07-20")
	assert.True(t, ok)
	assert.Equal(t, "snap1", snap)
	assert.Equal(t, "tank/ds", ds)
	assert.Equal(t, "2026-07-20", cr)
}

func TestPlacementConstraintsHelpers(t *testing.T) {
	c := placementConstraints{
		onDifferent: []string{"rack"},
	}
	assert.False(t, c.empty())
	assert.Equal(t, []string{"rack"}, c.referencedKeys())

	nodes := []placementNode{
		{node: "n1", labels: map[string]string{"rack": "A"}, freeGB: 100},
		{node: "n2", labels: map[string]string{"rack": "B"}, freeGB: 100},
	}
	selected, err := selectConstrained(nodes, 2, c)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"n1", "n2"}, selected)

	// Insufficient nodes
	_, err = selectConstrained(nodes, 3, c)
	assert.Error(t, err)
}
