package csi

import (
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	sdspb "github.com/haify-project/sds/api/proto/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cand is a shorthand for a candidate node with `gb` gibibytes free.
func cand(node string, gb uint64) replicaCandidate {
	return replicaCandidate{node: node, freeBytes: gb * giB}
}

func TestSelectReplicaNodesPrefersRequisite(t *testing.T) {
	got, err := selectReplicaNodes([]replicaCandidate{cand("n1", 100), cand("n2", 100), cand("n3", 100)},
		[]string{"n3"}, 2, 10*giB)
	require.NoError(t, err)
	assert.Equal(t, "n3", got[0], "requisite node must be included first")
	assert.Len(t, got, 2)
}

func TestSelectReplicaNodesNoRequisite(t *testing.T) {
	got, err := selectReplicaNodes([]replicaCandidate{cand("n1", 100), cand("n2", 100), cand("n3", 100)}, nil, 2, 10*giB)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestSelectReplicaNodesInsufficient(t *testing.T) {
	_, err := selectReplicaNodes([]replicaCandidate{cand("n1", 100)}, nil, 2, 10*giB)
	assert.Error(t, err)
}

// The emptiest nodes win, regardless of where they sit in the candidate list.
// This is the whole point of capacity-aware placement: before it, a PVC always
// took the first two nodes ListNodes returned and piled onto a filling node.
func TestSelectReplicaNodesPicksMostFreeSpace(t *testing.T) {
	got, err := selectReplicaNodes([]replicaCandidate{
		cand("n1", 12), // fits, but is the tightest of the three
		cand("n2", 500),
		cand("n3", 300),
	}, nil, 2, 10*giB)
	require.NoError(t, err)
	assert.Equal(t, []string{"n2", "n3"}, got)
	assert.NotContains(t, got, "n1", "the node with the least free space must not be chosen")
}

// A pinned node is where the Pod already is, so it is seated before ranking
// even though it is the tightest node in the cluster.
func TestSelectReplicaNodesRequisiteWinsOverCapacity(t *testing.T) {
	got, err := selectReplicaNodes([]replicaCandidate{
		cand("n1", 500),
		cand("n2", 500),
		cand("n3", 11), // barely enough, and last by free space
	}, []string{"n3"}, 2, 10*giB)
	require.NoError(t, err)
	assert.Equal(t, []string{"n3", "n1"}, got)
}

// A node whose pool cannot hold the volume is not a candidate at all.
func TestSelectReplicaNodesSkipsNodesWithoutRoom(t *testing.T) {
	got, err := selectReplicaNodes([]replicaCandidate{
		cand("n1", 5),
		cand("n2", 500),
		cand("n3", 300),
	}, nil, 2, 100*giB)
	require.NoError(t, err)
	assert.Equal(t, []string{"n2", "n3"}, got)
}

func TestSelectReplicaNodesAllTooSmall(t *testing.T) {
	_, err := selectReplicaNodes([]replicaCandidate{
		cand("n1", 5), cand("n2", 8), cand("n3", 2),
	}, nil, 2, 100*giB)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "100GiB free")
	assert.Contains(t, err.Error(), "0 of 3 node(s) hosting the pool qualify")
}

// Refusing a pin that cannot fit is deliberate: silently placing the volume on
// other nodes would leave the Pod bound to this one unable to mount it.
func TestSelectReplicaNodesRequisiteTooSmallIsAnError(t *testing.T) {
	_, err := selectReplicaNodes([]replicaCandidate{
		cand("n1", 5), cand("n2", 500), cand("n3", 300),
	}, []string{"n1"}, 2, 100*giB)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `node "n1" must hold a replica`)
}

// Zero free space means "the controller told us nothing", not "full" — a pool
// served from the database carries no byte counts, and thin pools report zero
// for life. Those nodes stay eligible and sort after the ones with real room.
func TestSelectReplicaNodesTreatsZeroFreeAsUnknown(t *testing.T) {
	got, err := selectReplicaNodes([]replicaCandidate{
		cand("n1", 0), cand("n2", 0), cand("n3", 50),
	}, nil, 2, 40*giB)
	require.NoError(t, err)
	assert.Equal(t, []string{"n3", "n1"}, got)
}

// With no capacity information anywhere, placement must still be deterministic
// rather than dependent on map iteration.
func TestSelectReplicaNodesDeterministicWithoutCapacity(t *testing.T) {
	for i := 0; i < 20; i++ {
		got, err := selectReplicaNodes([]replicaCandidate{
			cand("n3", 0), cand("n1", 0), cand("n2", 0),
		}, nil, 2, 10*giB)
		require.NoError(t, err)
		assert.Equal(t, []string{"n1", "n2"}, got)
	}
}

func TestSelectReplicaNodesEqualCapacityTieBreaksOnName(t *testing.T) {
	got, err := selectReplicaNodes([]replicaCandidate{
		cand("n3", 100), cand("n1", 100), cand("n2", 100),
	}, nil, 2, 10*giB)
	require.NoError(t, err)
	assert.Equal(t, []string{"n1", "n2"}, got)
}

func TestNodesWithPoolCarriesFreeSpace(t *testing.T) {
	nodes := []*sdspb.NodeInfo{
		{Name: "n1", Address: "10.0.0.1"},
		{Name: "n2", Address: "10.0.0.2"},
		{Name: "n3", Address: "10.0.0.3"},
	}
	pools := []*sdspb.PoolInfo{
		{Name: "sds_vg0", Node: "10.0.0.1", FreeBytes: 20 * giB},
		{Name: "sds_vg0", Node: "10.0.0.2", FreeGb: 90}, // DB-served pool: no byte counts
		{Name: "sds_other", Node: "10.0.0.3", FreeBytes: 900 * giB},
	}
	got := nodesWithPool(nodes, pools, "vg0", "host")
	require.Len(t, got, 2, "n3 hosts a different pool")
	assert.Equal(t, replicaCandidate{node: "n1", freeBytes: 20 * giB, domain: "node:n1"}, got[0])
	assert.Equal(t, replicaCandidate{node: "n2", freeBytes: 90 * giB, domain: "node:n2"}, got[1])
}

// A thin pool's volume group reports zero free for life, so ranking has to read
// the thin pool's own utilisation or every thin node looks equally full.
func TestPoolFreeBytesUsesThinUtilisation(t *testing.T) {
	thin := &sdspb.PoolInfo{
		Name: "sds_vg0", Node: "10.0.0.1",
		FreeGb:          0,
		ThinPoolLv:      "sds_vg0_tpool",
		ThinSizeBytes:   100 * giB,
		ThinDataPercent: 25,
	}
	assert.Equal(t, uint64(75*giB), poolFreeBytes(thin))

	full := &sdspb.PoolInfo{ThinPoolLv: "tp", ThinSizeBytes: 100 * giB, ThinDataPercent: 100}
	assert.Zero(t, poolFreeBytes(full))

	// No thin pool: exact bytes when present, rounded gibibytes otherwise.
	assert.Equal(t, uint64(7*giB), poolFreeBytes(&sdspb.PoolInfo{FreeBytes: 7 * giB, FreeGb: 3}))
	assert.Equal(t, uint64(3*giB), poolFreeBytes(&sdspb.PoolInfo{FreeGb: 3}))
}

func TestRequisiteNodes(t *testing.T) {
	req := &csi.TopologyRequirement{Requisite: []*csi.Topology{
		{Segments: map[string]string{TopologyKeyNode: "n2"}},
	}}
	assert.Equal(t, []string{"n2"}, requisiteNodes(req))
	assert.Nil(t, requisiteNodes(nil))
}

func TestAccessibleTopology(t *testing.T) {
	got := accessibleTopology([]string{"n1", "n2"})
	require.Len(t, got, 2)
	assert.Equal(t, "n1", got[0].Segments[TopologyKeyNode])
}

func TestSelectReplicaNodesSpreadsAcrossFaultDomains(t *testing.T) {
	cands := []replicaCandidate{
		{node: "a", freeBytes: 100 * giB, domain: "host=h1"},
		{node: "b", freeBytes: 90 * giB, domain: "host=h1"},
		{node: "c", freeBytes: 10 * giB, domain: "host=h2"},
	}
	got, err := selectReplicaNodes(cands, nil, 2, giB)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "c"}, got)

	// A pinned node's machine counts as taken too.
	got, err = selectReplicaNodes(cands, []string{"b"}, 2, giB)
	require.NoError(t, err)
	assert.Equal(t, []string{"b", "c"}, got)

	// All on one machine: still placed.
	one := []replicaCandidate{cands[0], cands[1]}
	got, err = selectReplicaNodes(one, nil, 2, giB)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, got)
}

// A drained node must not receive a replica of a new PVC.
func TestNodesWithPoolSkipsDrainedNode(t *testing.T) {
	nodes := []*sdspb.NodeInfo{
		{Name: "n1", Address: "10.0.0.1", State: "online"},
		{Name: "n2", Address: "10.0.0.2", State: "maintenance"},
		{Name: "n3", Address: "10.0.0.3", State: "offline"},
	}
	pools := []*sdspb.PoolInfo{
		{Name: "sds_vg0", Node: "10.0.0.1", FreeBytes: 10 * giB},
		{Name: "sds_vg0", Node: "10.0.0.2", FreeBytes: 900 * giB},
		{Name: "sds_vg0", Node: "10.0.0.3", FreeBytes: 10 * giB},
	}
	got := nodesWithPool(nodes, pools, "vg0", "")
	var names []string
	for _, c := range got {
		names = append(names, c.node)
	}
	assert.Equal(t, []string{"n1", "n3"}, names)
}
