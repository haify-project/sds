package controller

import (
	"testing"

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
