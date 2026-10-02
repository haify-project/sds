package controller

import (
	"context"
	"testing"

	"github.com/haify-project/sds/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A conversion destroys the backing volumes on one node and rebuilds them from
// its peers. Every one of these refusals is the difference between a routine
// operation and losing the only good copy, so they are asserted individually
// rather than through one happy path.

func thickPool(t *testing.T, node string) *thinConversionInput {
	t.Helper()
	return &thinConversionInput{
		Node:        node,
		Pool:        "sds_sdspool",
		VGFreeBytes: 3 * gib,
		Resources: []*ResourceInfo{{
			Name:  "openclaw",
			Nodes: []string{"node-a", "node-b", "node-e"},
			Volumes: []*ResourceVolumeInfo{{
				VolumeID: 0, Pool: "sds_sdspool", BackingVolume: "openclaw_data", SizeGB: 6,
			}},
			NodeStates: map[string]*ResourceNodeState{
				"node-a": {Role: "Primary", DiskState: "UpToDate", SyncPercent: 100},
				"node-b": {Role: "Secondary", DiskState: "UpToDate", SyncPercent: 100},
				"node-e": {Role: "Secondary", DiskState: "UpToDate", SyncPercent: 100},
			},
		}},
		BackingBytes: map[string]uint64{"openclaw_data": 6442450944},
		AlreadyThin:  map[string]bool{"openclaw_data": false},
	}
}

// NodeStates is keyed by whatever name DRBD reports, which is the node's
// hostname — "sds-b", "iZ2vca1rjuuxbqtpm9hy7zZ" — not the name SDS knows it by.
// Looking it up with the SDS name found nothing for every node whose two names
// differ, and the planner refused every one of them with "no live DRBD state".
// It failed closed, so nothing was damaged; the feature simply never worked.
func TestFindsLiveStateWhenDRBDKnowsTheNodeByAnotherName(t *testing.T) {
	in := thickPool(t, "node-e")
	// Re-key exactly as a real cluster does: DRBD hostnames, not SDS names.
	states := in.Resources[0].NodeStates
	in.Resources[0].NodeStates = map[string]*ResourceNodeState{
		"lima-sds-a": states["node-a"],
		"sds-b":      states["node-b"],
		"sds-e":      states["node-e"],
	}
	in.DRBDName = map[string]string{
		"node-a": "lima-sds-a", "node-b": "sds-b", "node-e": "sds-e",
	}
	plan, err := planThinConversion(in)
	require.NoError(t, err, "the node is Secondary and its peers are UpToDate")
	require.Len(t, plan.Volumes, 1)
}

// And the refusal must still fire when the state genuinely is missing, rather
// than being papered over by the lookup above.
func TestStillRefusesWhenThereIsNoStateAtAll(t *testing.T) {
	in := thickPool(t, "node-e")
	delete(in.Resources[0].NodeStates, "node-e")
	_, err := planThinConversion(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no live DRBD state")
}

func TestRefusesToConvertTheNodeHoldingPrimary(t *testing.T) {
	in := thickPool(t, "node-a") // node-a is Primary above
	_, err := planThinConversion(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Primary")
	assert.Contains(t, err.Error(), "openclaw")
}

func TestRefusesWhileAPeerIsNotUpToDate(t *testing.T) {
	in := thickPool(t, "node-e")
	in.Resources[0].NodeStates["node-b"].DiskState = "Inconsistent"
	_, err := planThinConversion(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "node-b")
}

// Mid-resync is the trap: every disk reads UpToDate on the peers that finished,
// and tearing down another replica right then can leave the resource with one
// copy it is still catching up from.
func TestRefusesWhileAResyncIsRunning(t *testing.T) {
	in := thickPool(t, "node-e")
	in.Resources[0].NodeStates["node-b"].Replication = "SyncSource"
	in.Resources[0].NodeStates["node-b"].SyncPercent = 62
	_, err := planThinConversion(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resync")
}

// Two diskful replicas means removing one leaves a single copy — no redundancy
// at all for the length of a full resync.
func TestRefusesWhenOnlyOneDiskfulCopyWouldRemain(t *testing.T) {
	in := thickPool(t, "node-e")
	r := in.Resources[0]
	r.Nodes = []string{"node-b", "node-e"}
	delete(r.NodeStates, "node-a")
	r.NodeStates["node-b"].Role = "Primary"
	_, err := planThinConversion(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "one diskful")
}

func TestRefusesWhenTheVolumeIsAlreadyThin(t *testing.T) {
	in := thickPool(t, "node-e")
	in.AlreadyThin["openclaw_data"] = true
	_, err := planThinConversion(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already thin")
}

// The whole point of converting is snapshot headroom. A pool with exactly
// enough room for the origin buys nothing, so say so instead of producing a
// pool that cannot hold a single snapshot.
func TestRefusesWhenNothingWouldBeLeftForSnapshots(t *testing.T) {
	in := thickPool(t, "node-e")
	in.VGFreeBytes = 0 // freeing 6 GiB from the origin leaves exactly the origin
	_, err := planThinConversion(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "headroom")
}

func TestPlanUsesExactByteSizes(t *testing.T) {
	in := thickPool(t, "node-e")
	plan, err := planThinConversion(in)
	require.NoError(t, err)
	require.Len(t, plan.Volumes, 1)
	// DRBD records the device size in its metadata; a peer rebuilt at a rounded
	// "6G" is a different number of bytes and refuses to attach.
	assert.Equal(t, uint64(6442450944), plan.Volumes[0].SizeBytes)
	assert.Equal(t, "openclaw_data", plan.Volumes[0].LV)
	assert.Equal(t, "openclaw", plan.Volumes[0].Resource)
}

// The origin ends up fully allocated: DRBD's full resync writes every block,
// including the zeroes, and nothing in that path issues discards. Sizing the
// pool to the live data instead of the origin would fill it mid-resync.
func TestPoolIsSizedForAFullyAllocatedOriginPlusHeadroom(t *testing.T) {
	in := thickPool(t, "node-e")
	plan, err := planThinConversion(in)
	require.NoError(t, err)
	assert.Greater(t, plan.PoolBytes, uint64(6442450944),
		"a pool merely the size of the origin leaves no room for snapshots")
	assert.LessOrEqual(t, plan.PoolBytes, 6442450944+3*gib,
		"the pool cannot exceed the origin plus the free extents")
	assert.Positive(t, plan.MetadataBytes, "thin metadata must be sized explicitly")
}

func TestPlanCoversEveryVolumeInThePool(t *testing.T) {
	in := thickPool(t, "node-e")
	in.Resources = append(in.Resources, &ResourceInfo{
		Name:  "sds-meta",
		Nodes: []string{"node-b", "node-e"},
		Volumes: []*ResourceVolumeInfo{{
			VolumeID: 0, Pool: "sds_sdspool", BackingVolume: "sds-meta_data", SizeGB: 1,
		}},
		NodeStates: map[string]*ResourceNodeState{
			"node-b": {Role: "Primary", DiskState: "UpToDate", SyncPercent: 100},
			"node-e": {Role: "Secondary", DiskState: "UpToDate", SyncPercent: 100},
		},
	})
	in.BackingBytes["sds-meta_data"] = 1073741824
	in.AlreadyThin["sds-meta_data"] = false

	// Only two diskful copies of sds-meta, so it must be refused for the same
	// reason as the single-copy case above — the pool is converted whole.
	_, err := planThinConversion(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sds-meta")
}

// A conversion that dies partway leaves the node diskless with its thick volume
// already removed, and the error tells the operator to rerun. That promise only
// holds if a rerun can see the half-finished state: the backing LV is gone, so
// its size has to come from a peer, and nothing may try to tear it down again.
func TestResumesAfterTheThickVolumeIsAlreadyGone(t *testing.T) {
	in := thickPool(t, "node-e")
	in.Present = map[string]bool{"openclaw_data": false}
	// The extents came back to the volume group when it was removed.
	in.VGFreeBytes = 3*gib + 6442450944

	plan, err := planThinConversion(in)
	require.NoError(t, err, "a half-converted node must be resumable")
	require.Len(t, plan.Volumes, 1)
	assert.False(t, plan.Volumes[0].NeedsTeardown, "there is nothing left to detach or remove")
	assert.True(t, plan.CreatePool)
	// The freed extents are already counted in VGFreeBytes; counting the origin
	// again would size the pool against space that does not exist.
	assert.LessOrEqual(t, plan.PoolBytes+2*plan.MetadataBytes, in.VGFreeBytes)
}

// And if it died after the pool was built, the pool must not be built twice —
// lvcreate refuses, which would wedge the resume permanently.
func TestResumeDoesNotRebuildAnExistingThinPool(t *testing.T) {
	in := thickPool(t, "node-e")
	in.Present = map[string]bool{"openclaw_data": false}
	in.ThinPoolExists = true
	in.VGFreeBytes = 0 // the pool already holds every extent

	plan, err := planThinConversion(in)
	require.NoError(t, err)
	assert.False(t, plan.CreatePool)
	assert.False(t, plan.Volumes[0].NeedsTeardown)
}

func TestRebuildSkipsTeardownAndPoolCreationOnResume(t *testing.T) {
	var detached, removed, poolsCreated int
	dep := &fakeDeploymentClient{
		drbdDetachFunc: func(_ context.Context, host, _ string) (*deployment.ExecResult, error) {
			detached++
			return successExecResult([]string{host}, ""), nil
		},
		lvRemoveFunc: func(_ context.Context, hosts []string, _ string) (*deployment.ExecResult, error) {
			removed++
			return successExecResult(hosts, ""), nil
		},
		lvCreateThinPoolAllFreeFunc: func(_ context.Context, hosts []string, _, _ string, _ uint64) (*deployment.ExecResult, error) {
			poolsCreated++
			return successExecResult(hosts, ""), nil
		},
	}
	plan := testConversionPlan()
	plan.CreatePool = false
	plan.Volumes[0].NeedsTeardown = false

	require.NoError(t, convertTestManager(dep).applyThinConversion(context.Background(), "10.0.0.1", plan))
	assert.Zero(t, detached)
	assert.Zero(t, removed)
	assert.Zero(t, poolsCreated)
}

// A pool half-converted by hand has thin volumes and thick ones side by side.
// Refusing the whole pool because one volume is already thin leaves the rest
// permanently unconvertible — which is the state node-e sat in: openclaw_data
// thin, sds-meta_data thick, and every conversion attempt answered "already
// thin; nothing to convert".
func TestConvertsTheThickVolumesInAHalfThinPool(t *testing.T) {
	in := thickPool(t, "node-e")
	in.Resources = append(in.Resources, &ResourceInfo{
		Name:  "sds-meta",
		Nodes: []string{"node-b", "node-e", "node-d"},
		Volumes: []*ResourceVolumeInfo{{
			VolumeID: 0, Pool: "sds_sdspool", BackingVolume: "sds-meta_data", SizeGB: 1,
		}},
		NodeStates: map[string]*ResourceNodeState{
			"node-b": {Role: "Secondary", DiskState: "UpToDate", SyncPercent: 100},
			"node-d": {Role: "Secondary", DiskState: "UpToDate", SyncPercent: 100},
			"node-e": {Role: "Secondary", DiskState: "UpToDate", SyncPercent: 100},
		},
	})
	in.BackingBytes["sds-meta_data"] = 1077936128
	in.AlreadyThin = map[string]bool{"openclaw_data": true, "sds-meta_data": false}
	in.ThinPoolExists = true
	in.ThinPoolMetadataBytes = 8 << 20
	in.VGFreeBytes = 1048576000

	plan, err := planThinConversion(in)
	require.NoError(t, err)
	require.Len(t, plan.Volumes, 1, "only the thick one is work")
	assert.Equal(t, "sds-meta_data", plan.Volumes[0].LV)
	assert.False(t, plan.CreatePool, "the pool is already there")
	assert.True(t, plan.ExtendPool, "the freed extents should go into it")
	// 8 MiB of metadata is what LVM's default gives; it is the size that leaves
	// a converted pool with almost no room for snapshot mappings.
	assert.Greater(t, plan.MetadataGrowTo, uint64(8<<20), "undersized metadata must be grown")
}

func TestSaysThereIsNothingToDoWhenEveryVolumeIsAlreadyThin(t *testing.T) {
	in := thickPool(t, "node-e")
	in.AlreadyThin["openclaw_data"] = true
	in.ThinPoolExists = true
	_, err := planThinConversion(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already thin")
}

// Metadata has to grow before the data area does. `lvextend -l +100%FREE` on
// the pool takes every free extent, and the metadata extension — which needs
// extents of its own, plus as many again for its spare copy — then has nothing
// left to take.
func TestRebuildGrowsMetadataBeforeTheDataArea(t *testing.T) {
	var order []string
	dep := &fakeDeploymentClient{
		lvExtendThinPoolMetadataFunc: func(_ context.Context, hosts []string, _, _ string, _ uint64) (*deployment.ExecResult, error) {
			order = append(order, "metadata")
			return successExecResult(hosts, ""), nil
		},
		lvExtendThinPoolAllFreeFunc: func(_ context.Context, hosts []string, _, _ string) (*deployment.ExecResult, error) {
			order = append(order, "data")
			return successExecResult(hosts, ""), nil
		},
	}
	plan := testConversionPlan()
	plan.CreatePool = false
	plan.ExtendPool = true
	plan.MetadataGrowTo = 128 << 20

	require.NoError(t, convertTestManager(dep).applyThinConversion(context.Background(), "10.0.0.1", plan))
	assert.Equal(t, []string{"metadata", "data"}, order)
}

// LVM allocates a *second* metadata area — the pmspare LV — the same size as
// the one asked for, so a pool sized to "everything minus one metadata area"
// overshoots the volume group by exactly that spare. On the real cluster this
// came out as "Insufficient free space: 2559 extents needed, but only 2527
// available", 32 extents short, the metadata size to the byte.
func TestLeavesRoomForLVMsSpareMetadataCopy(t *testing.T) {
	in := thickPool(t, "node-e")
	plan, err := planThinConversion(in)
	require.NoError(t, err)

	usable := uint64(6442450944) + in.VGFreeBytes
	assert.LessOrEqual(t, plan.PoolBytes+2*plan.MetadataBytes, usable,
		"the data area, its metadata, and LVM's spare copy all come out of the same extents")
}

// applyThinConversion runs destructive steps in order; a step that fails after
// the thick volume is gone leaves the node diskless. Reporting that as success
// is worse than the failure itself — it is how a conversion "completed" on
// node-a and left an empty volume group behind.
func TestRebuildReportsAFailedThinPoolCreation(t *testing.T) {
	dep := &fakeDeploymentClient{
		lvCreateThinPoolAllFreeFunc: func(_ context.Context, hosts []string, _, _ string, _ uint64) (*deployment.ExecResult, error) {
			// lvcreate exits 5 and says why on stderr; the SSH call itself is fine,
			// so the error arrives in the result, never in the returned error.
			return failedResult(hosts, "  Insufficient free space: 2559 extents needed, but only 2527 available"), nil
		},
	}
	err := convertTestManager(dep).applyThinConversion(context.Background(), "10.0.0.1", testConversionPlan())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Insufficient free space")
}

// The attach is the last step, and the one whose silent failure looks healthiest
// from the outside: the pool and volume both exist, only the replica is missing.
func TestRebuildReportsAFailedAttach(t *testing.T) {
	dep := &fakeDeploymentClient{
		drbdAttachFunc: func(_ context.Context, host, _ string) (*deployment.ExecResult, error) {
			return failedResult([]string{host}, "  Device size mismatch"), nil
		},
	}
	err := convertTestManager(dep).applyThinConversion(context.Background(), "10.0.0.1", testConversionPlan())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Device size mismatch")
}

func TestRebuildSucceedsWhenEveryStepDoes(t *testing.T) {
	require.NoError(t, convertTestManager(&fakeDeploymentClient{}).
		applyThinConversion(context.Background(), "10.0.0.1", testConversionPlan()))
}

func testConversionPlan() *thinConversionPlan {
	return &thinConversionPlan{
		Node: "node-e", Pool: "sds_sdspool", ThinPoolName: thinPoolName,
		PoolBytes: 10464788480, MetadataBytes: 134217728, CreatePool: true,
		Volumes: []thinVolumePlan{{
			Resource: "openclaw", LV: "openclaw_data", SizeBytes: 6442450944, NeedsTeardown: true,
		}},
	}
}

func convertTestManager(dep deploymentClient) *ResourceManager {
	ctrl := newBasicTestController(dep)
	return ctrl.resources
}

// A volume in a different pool on the same node is none of this operation's
// business.
func TestPlanIgnoresVolumesInOtherPools(t *testing.T) {
	in := thickPool(t, "node-e")
	in.Resources = append(in.Resources, &ResourceInfo{
		Name:  "elsewhere",
		Nodes: []string{"node-e"},
		Volumes: []*ResourceVolumeInfo{{
			VolumeID: 0, Pool: "other_vg", BackingVolume: "elsewhere_data", SizeGB: 1,
		}},
		NodeStates: map[string]*ResourceNodeState{
			"node-e": {Role: "Primary", DiskState: "UpToDate", SyncPercent: 100},
		},
	})
	plan, err := planThinConversion(in)
	require.NoError(t, err, "a Primary in an unrelated pool must not block this one")
	require.Len(t, plan.Volumes, 1)
	assert.Equal(t, "openclaw_data", plan.Volumes[0].LV)
}

// The pool that a conversion builds and the pool that `pool create` builds have
// different names, so extending "the" thin pool by a constant name would build
// a second pool beside the first.
func TestExtendsWhicheverThinPoolTheNodeActuallyHas(t *testing.T) {
	in := thickPool(t, "node-e")
	in.ThinPoolExists = true
	in.ExistingThinPool = "sds_sdspool_thin"
	in.ThinPoolMetadataBytes = 128 << 20

	plan, err := planThinConversion(in)
	require.NoError(t, err)
	assert.Equal(t, "sds_sdspool_thin", plan.ThinPoolName)
	assert.Zero(t, plan.MetadataGrowTo, "128 MiB is already the floor")
}
