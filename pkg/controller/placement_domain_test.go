package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func hostNode(name string, freeGB uint64, host string) placementNode {
	labels := map[string]string{}
	if host != "" {
		labels["host"] = host
	}
	return placementNode{node: name, freeGB: freeGB, labels: labels}
}

// Two VMs on one machine with the most free space must not both be chosen when
// a node on another machine can take the second copy.
func TestPlacementSpreadsAcrossHostsBeforeFreeSpace(t *testing.T) {
	nodes := []placementNode{hostNode("a", 100, "h1"), hostNode("b", 90, "h1"), hostNode("c", 10, "h2")}
	got, warn, err := spreadAcrossDomains(nodes, 2, placementConstraints{}, "host")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"a", "c"}, got)
	assert.Empty(t, warn)
}

// A lab of VMs on one machine can still create resources; it is told why that
// is fragile.
func TestPlacementOnOneHostWarnsInsteadOfFailing(t *testing.T) {
	nodes := []placementNode{hostNode("a", 100, "h1"), hostNode("b", 90, "h1"), hostNode("c", 80, "h1")}
	got, warn, err := spreadAcrossDomains(nodes, 2, placementConstraints{}, "host")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"a", "b"}, got)
	assert.Contains(t, warn, "host=h1")
}

func TestUnlabelledNodesPlaceByFreeSpaceAsBefore(t *testing.T) {
	nodes := []placementNode{hostNode("a", 10, ""), hostNode("b", 90, ""), hostNode("c", 80, "")}
	got, warn, err := spreadAcrossDomains(nodes, 2, placementConstraints{}, "host")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"b", "c"}, got)
	assert.Empty(t, warn)
}

// The explicit constraint stays hard: the soft spread never loosens it.
func TestSpreadKeepsHardConstraints(t *testing.T) {
	nodes := []placementNode{
		{node: "a", freeGB: 100, labels: map[string]string{"host": "h1", "rack": "r1"}},
		{node: "b", freeGB: 90, labels: map[string]string{"host": "h2", "rack": "r1"}},
		{node: "c", freeGB: 10, labels: map[string]string{"host": "h1", "rack": "r2"}},
	}
	got, _, err := spreadAcrossDomains(nodes, 2, placementConstraints{onDifferent: []string{"rack"}}, "host")
	require.NoError(t, err)
	assert.Contains(t, got, "c", "rack r2 is the only way to satisfy replicas-on-different rack")
}

func TestFaultDomainRisk(t *testing.T) {
	labels := map[string]map[string]string{
		"a": {"host": "h1"}, "b": {"host": "h1"}, "c": {"host": "h2"}, "d": {"host": "h3"},
	}
	// Both copies on one machine: its loss is the loss of the data.
	assert.Equal(t, "host=h1", faultDomainRisk([]string{"a", "b"}, []string{"c"}, labels, "host"))
	// Copies apart, but the tiebreaker beside one of them: that machine holds
	// two of three votes, so losing it costs the survivor its quorum.
	assert.Equal(t, "host=h1", faultDomainRisk([]string{"a", "c"}, []string{"b"}, labels, "host"))
	assert.Empty(t, faultDomainRisk([]string{"a", "c"}, []string{"d"}, labels, "host"))
	// Without labels every node is its own domain and nothing is flagged.
	assert.Empty(t, faultDomainRisk([]string{"x", "y"}, []string{"z"}, nil, "host"))
}

func TestTiebreakerAvoidsTheReplicasHost(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	registerNodes(ctrl, map[string]string{"a": "10.0.0.1", "b": "10.0.0.2", "c": "10.0.0.3", "d": "10.0.0.4"})
	for addr, host := range map[string]string{"10.0.0.1": "h1", "10.0.0.2": "h1", "10.0.0.3": "h2", "10.0.0.4": "h3"} {
		ctrl.nodes.nodes[addr].Labels = map[string]string{"host": host}
	}
	// b sorts first, but shares a's machine; d does not share either replica's.
	assert.Equal(t, "d", ctrl.resources.selectTiebreaker(context.Background(), []string{"a", "c"}))
}
