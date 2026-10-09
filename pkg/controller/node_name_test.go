package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The hosts map carries a node's hostname as an alias of its address. NodeName
// must still answer with the registered name, every time.
func TestNodeNameIsStableWithHostnameAliases(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	registerNodes(ctrl, map[string]string{"node-b": "10.0.0.2"})
	ctrl.hostsMap["storage-b"] = "10.0.0.2"
	for i := 0; i < 200; i++ {
		assert.Equal(t, "node-b", ctrl.NodeName("10.0.0.2"))
	}
	delete(ctrl.nodes.nodes, "10.0.0.2")
	for i := 0; i < 200; i++ {
		assert.Equal(t, "node-b", ctrl.NodeName("10.0.0.2"), "without a registry entry the choice is still fixed")
	}
}
