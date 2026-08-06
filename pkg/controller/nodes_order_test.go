package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ListNodes ranged over a map, so Go handed the caller a different order on
// almost every call. The UI redraws its node list on each poll, which made the
// rows jump around while being read — and it is impossible to tell a reordered
// list from one where something actually changed.
func TestListNodesIsOrderedByName(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	registerNodes(ctrl, map[string]string{
		"node-e": "10.0.0.5", "node-a": "10.0.0.1",
		"node-c": "10.0.0.3", "node-b": "10.0.0.2",
	})

	want := []string{"node-a", "node-b", "node-c", "node-e"}

	// Repeated because a map range can accidentally agree with sorted order
	// once; the point is that it agrees every time.
	for i := 0; i < 20; i++ {
		nodes, err := ctrl.nodes.ListNodes(context.Background())
		require.NoError(t, err)

		got := make([]string, 0, len(nodes))
		for _, n := range nodes {
			got = append(got, n.Name)
		}
		assert.Equal(t, want, got, "call %d returned a different order", i)
	}
}
