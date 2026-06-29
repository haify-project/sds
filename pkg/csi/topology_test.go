package csi

import (
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectReplicaNodesPrefersRequisite(t *testing.T) {
	got, err := selectReplicaNodes([]string{"n1", "n2", "n3"}, []string{"n3"}, 2)
	require.NoError(t, err)
	assert.Equal(t, "n3", got[0], "requisite node must be included first")
	assert.Len(t, got, 2)
}

func TestSelectReplicaNodesNoRequisite(t *testing.T) {
	got, err := selectReplicaNodes([]string{"n1", "n2", "n3"}, nil, 2)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestSelectReplicaNodesInsufficient(t *testing.T) {
	_, err := selectReplicaNodes([]string{"n1"}, nil, 2)
	assert.Error(t, err)
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
