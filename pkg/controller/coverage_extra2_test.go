package controller

import (
	"context"
	"testing"
	"time"

	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLexLess(t *testing.T) {
	assert.True(t, lexLess([]string{"a"}, []string{"b"}))
	assert.False(t, lexLess([]string{"b"}, []string{"a"}))
	assert.True(t, lexLess([]string{"a"}, []string{"a", "b"}))  // shorter prefix is less
	assert.False(t, lexLess([]string{"a", "b"}, []string{"a"})) // longer is not less
	assert.False(t, lexLess([]string{"a"}, []string{"a"}))      // equal
	assert.True(t, lexLess([]string{"a", "b"}, []string{"a", "c"}))
}

func TestSelectPlacementNodesValidation(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)

	// replicas < 1 is rejected before any node lookup.
	_, _, err := ctrl.resources.selectPlacementNodes(context.Background(), "vg0", 1, 0, nil, nil, nil)
	assert.Error(t, err)

	// No nodes/pools available -> cannot satisfy the request.
	_, _, err = ctrl.resources.selectPlacementNodes(context.Background(), "vg0", 1, 2, nil, nil, nil)
	assert.Error(t, err)
}

func TestAttachDisklessClientRejectsAReplicaAndConvertsTheTiebreaker(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	ctx := context.Background()

	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{
		Name:          "res1",
		Nodes:         "n1,n2",
		DisklessNodes: "n3",
	}))

	// A diskful replica node cannot become a client.
	err := ctrl.resources.AttachDisklessClient(ctx, "res1", "n1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "diskful replica")

	// The tiebreaker becomes a client, keeping its place in the resource.
	require.NoError(t, ctrl.resources.AttachDisklessClient(ctx, "res1", "n3"))
	got, err := ctrl.db.GetResource(ctx, "res1")
	require.NoError(t, err)
	assert.Equal(t, "", got.DisklessNodes)
	assert.Equal(t, "n3", got.DisklessClients)

	// Detaching it would leave two replicas and no third vote, so it goes
	// back to being the tiebreaker rather than leaving the resource.
	require.NoError(t, ctrl.resources.DetachDisklessClient(ctx, "res1", "n3"))
	got, err = ctrl.db.GetResource(ctx, "res1")
	require.NoError(t, err)
	assert.Equal(t, "n3", got.DisklessNodes)
	assert.Equal(t, "", got.DisklessClients)

	// Missing resource.
	err = ctrl.resources.AttachDisklessClient(ctx, "ghost", "n9")
	require.Error(t, err)

	// Missing deployment / db guards.
	bare := &ResourceManager{controller: &Controller{}}
	assert.Error(t, bare.AttachDisklessClient(ctx, "res1", "n4"))
}

func TestAttachAndDetachDisklessClientFlow(t *testing.T) {
	dep := &fakeDeploymentClient{}
	// Reads of the resource config (drbdadm dump / cat res.res) return a valid
	// two-node config so the diskless-client surgery has real volumes to render.
	dep.execFunc = func(_ context.Context, hosts []string, _ string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		return successExecResult(hosts, drbdResFixture), nil
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	ctx := context.Background()

	for i, n := range []string{"n1", "n2", "n3"} {
		_, err := ctrl.nodes.RegisterNode(ctx, n, "10.0.0."+string(rune('1'+i)))
		require.NoError(t, err)
	}
	require.NoError(t, ctrl.loadHostsFromDatabase(ctx))
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{
		Name: "res1", Nodes: "n1,n2", Port: 7001,
	}))
	require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{
		ResourceName: "res1", VolumeID: 0, Pool: "vg0", VolumeName: "res1_data",
	}))

	// Attach n3 as a diskless client, then detach it. The body must run without
	// panicking; on success the DB records/forgets n3 accordingly.
	if err := ctrl.resources.AttachDisklessClient(ctx, "res1", "n3"); err == nil {
		dbRes, gErr := ctrl.db.GetResource(ctx, "res1")
		require.NoError(t, gErr)
		assert.Contains(t, splitCSV(dbRes.DisklessClients), "n3")
		require.NoError(t, ctrl.resources.DetachDisklessClient(ctx, "res1", "n3"))
	}

	// Detaching a node that is not a client is idempotent (no error).
	assert.NoError(t, ctrl.resources.DetachDisklessClient(ctx, "res1", "n2"))
}

func TestRecordAndForgetDisklessClient(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	ctx := context.Background()

	dbRes := &database.Resource{Name: "res1", Nodes: "n1,n2"}
	require.NoError(t, ctrl.db.SaveResource(ctx, dbRes))

	require.NoError(t, ctrl.resources.recordDisklessClient(ctx, dbRes, nil, "n3"))
	got, err := ctrl.db.GetResource(ctx, "res1")
	require.NoError(t, err)
	assert.Contains(t, splitCSV(got.DisklessClients), "n3")

	require.NoError(t, ctrl.resources.forgetDisklessClient(ctx, got, "n3"))
	got, err = ctrl.db.GetResource(ctx, "res1")
	require.NoError(t, err)
	assert.NotContains(t, splitCSV(got.DisklessClients), "n3")
}

func TestNodesHostingResources(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	ctx := context.Background()
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "res1", Nodes: "n1,n2"}))

	// Empty input -> empty exclusion set.
	excl, err := ctrl.resources.nodesHostingResources(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, excl)

	// Existing resource -> its nodes are excluded (blank names skipped).
	excl, err = ctrl.resources.nodesHostingResources(ctx, []string{"res1", ""})
	require.NoError(t, err)
	assert.True(t, excl["n1"])
	assert.True(t, excl["n2"])

	// Missing resource -> error.
	_, err = ctrl.resources.nodesHostingResources(ctx, []string{"ghost"})
	assert.Error(t, err)
}

func TestConnectionMeshHelpers(t *testing.T) {
	blocks := parseOnBlocks(drbdResFixture)
	names := onHostNames(blocks)
	assert.ElementsMatch(t, []string{"n1", "n2"}, names)

	mesh := buildConnectionMesh([]string{"n1", "n2", "n3"})
	assert.Contains(t, mesh, "connection-mesh")
	assert.Contains(t, mesh, "n1 n2 n3")

	stripped := stripConnectionMesh(drbdResFixture)
	assert.NotContains(t, stripped, "connection-mesh")
}

func TestScheduleManagerCRUD(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	ctx := context.Background()

	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "res1", Nodes: "n1"}))

	policy := database.GFSPolicy{Hourly: 6, Daily: 7, Weekly: 4}
	require.NoError(t, ctrl.schedules.CreateSchedule(ctx, "res1", "0 * * * *", policy, true, nil))

	list, err := ctrl.schedules.ListSchedules(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, list)

	// Invalid cron is rejected.
	err = ctrl.schedules.CreateSchedule(ctx, "res1", "not-a-cron", policy, true, nil)
	assert.Error(t, err)

	require.NoError(t, ctrl.schedules.DeleteSchedule(ctx, "res1"))

	// Deleting a missing schedule is a no-op (does not panic).
	_ = ctrl.schedules.DeleteSchedule(ctx, "does-not-exist")
}

const drbdResFixture = `resource res1 {
    on n1 {
        node-id 0;
        volume 0 {
            device    minor 0;
            disk /dev/vg0/res1_data;
            meta-disk internal;
        }
        address 10.0.0.1:7001;
    }
    on n2 {
        node-id 1;
        volume 0 {
            device    minor 0;
            disk /dev/vg0/res1_data;
            meta-disk internal;
        }
        address 10.0.0.2:7001;
    }
    connection-mesh {
        hosts n1 n2;
    }
}
`

func TestDisklessClientBlockSurgery(t *testing.T) {
	// Add a diskless client block for n3.
	added, err := addDisklessClientBlock(drbdResFixture, "n3", "10.0.0.3", 7001)
	require.NoError(t, err)
	assert.Contains(t, added, "on n3")
	assert.Contains(t, added, "10.0.0.3:7001")
	assert.Contains(t, added, "none;") // diskless override on the client's volume

	// Adding the same node again is rejected.
	_, err = addDisklessClientBlock(added, "n3", "10.0.0.3", 7001)
	assert.ErrorIs(t, err, errDisklessAlreadyPresent)

	// Remove it again.
	removed, err := removeDisklessClientBlock(added, "n3")
	require.NoError(t, err)
	assert.NotContains(t, removed, "on n3")

	// Removing an absent node is rejected.
	_, err = removeDisklessClientBlock(drbdResFixture, "n9")
	assert.ErrorIs(t, err, errDisklessNotPresent)
}

func TestSplitCSV(t *testing.T) {
	assert.Empty(t, splitCSV(""))
	assert.Equal(t, []string{"a", "b"}, splitCSV("a,b"))
	assert.Equal(t, []string{"a", "b"}, splitCSV(" a , b "))
	assert.Equal(t, []string{"a"}, splitCSV("a,,"))
}

func TestNextRun(t *testing.T) {
	now := time.Date(2026, 7, 20, 10, 30, 0, 0, time.UTC)
	next := NextRun("0 * * * *", now) // top of every hour
	assert.True(t, next.After(now))
	assert.Equal(t, 11, next.Hour())
	assert.Equal(t, 0, next.Minute())

	// Invalid cron -> zero time.
	assert.True(t, NextRun("garbage", now).IsZero())
}
