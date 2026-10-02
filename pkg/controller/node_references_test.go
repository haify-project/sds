package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/sds/pkg/database"
)

func TestNodeReferencesEveryRole(t *testing.T) {
	resources := []*database.Resource{
		{Name: "data", Nodes: "n1,n2"},
		{Name: "logs", Nodes: "n2,n3", DisklessNodes: "n1"},
		{Name: "vm", Nodes: "n2,n3", DisklessClients: "n4, n1"},
		{Name: "wan", Nodes: "n2", WANMode: true, DRNode: "n1"},
		{Name: "lan", Nodes: "n2", DRNode: "n1"}, // stale DRNode on a LAN record
		{Name: "old", Nodes: "10.0.0.1,10.0.0.2"},
		{Name: "elsewhere", Nodes: "n2,n3", DisklessNodes: "n4"},
	}
	gateways := []*database.Gateway{
		{Resource: "data", Type: database.GatewayTypeNFS},
		{Resource: "elsewhere", Type: database.GatewayTypeISCSI},
		{Resource: "moved", Type: database.GatewayTypeNVMEOF, ActiveNode: "n1"},
	}

	got := nodeReferences(resources, gateways, "n1", "n1.local", "10.0.0.1")
	assert.Equal(t, []string{
		"data (replica, NFS gateway)",
		"logs (tiebreaker)",
		"moved (NVMe-oF gateway)",
		"old (replica)",
		"vm (diskless client)",
		"wan (DR node)",
	}, got)

	assert.Empty(t, nodeReferences(resources, gateways, "n9", "10.0.0.9"))
}

func TestUnregisterNodeRefusesWhileReferenced(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	registerNodes(ctrl, map[string]string{"n1": "10.0.0.1", "n2": "10.0.0.2", "n3": "10.0.0.3"})
	db := newTestDB(t)
	ctrl.db = db
	ctx := context.Background()
	require.NoError(t, db.SaveResource(ctx, &database.Resource{
		Name: "data", Nodes: "n1,n2", DisklessNodes: "n3", Port: 7100, Protocol: "C", Replicas: 2,
	}))
	require.NoError(t, db.SaveGateway(ctx, &database.Gateway{
		Name: "data", Resource: "data", Type: database.GatewayTypeISCSI,
	}))

	err := ctrl.nodes.UnregisterNode(ctx, "n1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "data (replica, iSCSI gateway)")
	_, err = ctrl.nodes.GetNode(ctx, "n1")
	assert.NoError(t, err, "a refused unregister must leave the node registered")

	err = ctrl.nodes.UnregisterNode(ctx, "10.0.0.3")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "data (tiebreaker)")

	// Once nothing names it, the node goes.
	require.NoError(t, db.DeleteGatewayByResource(ctx, "data"))
	require.NoError(t, db.DeleteResource(ctx, "data"))
	require.NoError(t, ctrl.nodes.UnregisterNode(ctx, "n1"))
	_, err = ctrl.nodes.GetNode(ctx, "n1")
	assert.ErrorContains(t, err, "node not found")
}
