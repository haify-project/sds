package controller

import (
	"context"
	"testing"

	"github.com/liliang-cn/sds/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A node's management address (where the controller SSHes) and its replication
// address (what DRBD talks over) used to be the same single field, which forced
// replication onto the management network. These tests pin the separation.

func TestGetReplicationAddressFallsBackToManagementAddress(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	registerNodes(ctrl, map[string]string{"n1": "10.0.0.1"})

	// No replication address registered: every node from before this feature
	// existed must keep behaving exactly as it did.
	assert.Equal(t, "10.0.0.1", ctrl.nodes.GetReplicationAddressByName("n1"))
	assert.Equal(t, "10.0.0.1", ctrl.nodes.GetNodeAddressByName("n1"))
}

func TestGetReplicationAddressPrefersTheDedicatedNetwork(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{
		Name:               "n1",
		Address:            "10.0.0.1",  // management / SSH
		ReplicationAddress: "10.99.0.1", // dedicated replication NIC
		State:              NodeStateOnline,
	}
	ctrl.hostsMap["n1"] = "10.0.0.1"

	assert.Equal(t, "10.99.0.1", ctrl.nodes.GetReplicationAddressByName("n1"),
		"DRBD must use the replication network")
	assert.Equal(t, "10.0.0.1", ctrl.nodes.GetNodeAddressByName("n1"),
		"SSH must keep using the management network")
}

// The point of the whole feature: the generated .res must carry the replication
// addresses, while the controller still reaches the nodes for SSH on their
// management addresses.
func TestGeneratedConfigUsesReplicationAddresses(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	for name, addrs := range map[string][2]string{
		"n1": {"10.0.0.1", "10.99.0.1"},
		"n2": {"10.0.0.2", "10.99.0.2"},
	} {
		ctrl.nodes.nodes[addrs[0]] = &NodeInfo{
			Name: name, Address: addrs[0], ReplicationAddress: addrs[1], State: NodeStateOnline,
		}
		ctrl.hostsMap[name] = addrs[0]
	}

	cfg := ctrl.resources.generateDrbdConfig("res1", 7001,
		[]resolvedVolume{{id: 0, minor: 100, pool: "vg0", volumeName: "res1_data", sizeGB: 1}},
		[]string{"n1", "n2"}, nil, "C", "lvm", nil, nil)

	assert.Contains(t, cfg, "address   10.99.0.1:7001;", "n1 should replicate over its dedicated NIC")
	assert.Contains(t, cfg, "address   10.99.0.2:7001;", "n2 should replicate over its dedicated NIC")
	assert.NotContains(t, cfg, "10.0.0.1:7001", "the management address must not appear in the .res")
	assert.NotContains(t, cfg, "10.0.0.2:7001")
}

// A partially-migrated cluster (some nodes given a replication NIC, some not)
// must still produce a valid config rather than an empty address.
func TestGeneratedConfigHandlesMixedNodes(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.nodes.nodes["10.0.0.1"] = &NodeInfo{
		Name: "n1", Address: "10.0.0.1", ReplicationAddress: "10.99.0.1", State: NodeStateOnline,
	}
	ctrl.nodes.nodes["10.0.0.2"] = &NodeInfo{ // no replication address yet
		Name: "n2", Address: "10.0.0.2", State: NodeStateOnline,
	}
	ctrl.hostsMap["n1"], ctrl.hostsMap["n2"] = "10.0.0.1", "10.0.0.2"

	cfg := ctrl.resources.generateDrbdConfig("res1", 7001,
		[]resolvedVolume{{id: 0, minor: 100, pool: "vg0", volumeName: "res1_data", sizeGB: 1}},
		[]string{"n1", "n2"}, nil, "C", "lvm", nil, nil)

	assert.Contains(t, cfg, "address   10.99.0.1:7001;", "the migrated node uses its replication NIC")
	assert.Contains(t, cfg, "address   10.0.0.2:7001;", "the un-migrated node falls back to management")
	// Whatever happens, no node may end up with an empty address.
	assert.NotContains(t, cfg, "address   :7001;")
}

// Registration must persist the replication address and restore it, or a
// controller restart would silently move replication back onto the management
// network on the next config generation.
func TestReplicationAddressSurvivesRestart(t *testing.T) {
	dep := &fakeDeploymentClient{
		execFunc: hostnameExecFunc("n1"),
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = openTestDB(t)

	_, err := ctrl.nodes.RegisterNodeWithReplicationAddress(
		context.Background(), "n1", "10.0.0.1", "10.99.0.1")
	require.NoError(t, err)

	stored, err := ctrl.db.ListNodes(context.Background())
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Equal(t, "10.0.0.1", stored[0].Address)
	assert.Equal(t, "10.99.0.1", stored[0].ReplicationAddress, "must be persisted")

	// Restore into a fresh controller, as a restart would.
	fresh := newBasicTestController(&fakeDeploymentClient{})
	fresh.db = ctrl.db
	require.NoError(t, fresh.loadFromDatabase(context.Background()))
	assert.Equal(t, "10.99.0.1", fresh.nodes.GetReplicationAddressByName("n1"),
		"replication address must survive a restart")
}

// Registering without a replication address must leave it empty rather than
// storing a copy of the management address, so "unset" stays distinguishable.
func TestRegistrationWithoutReplicationAddressLeavesItEmpty(t *testing.T) {
	dep := &fakeDeploymentClient{execFunc: hostnameExecFunc("n1")}
	ctrl := newBasicTestController(dep)
	ctrl.db = openTestDB(t)

	node, err := ctrl.nodes.RegisterNode(context.Background(), "n1", "10.0.0.1")
	require.NoError(t, err)
	assert.Empty(t, node.ReplicationAddress)

	stored, err := ctrl.db.ListNodes(context.Background())
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Empty(t, stored[0].ReplicationAddress)
	// ...and resolution still works, falling back to management.
	assert.Equal(t, "10.0.0.1", ctrl.nodes.GetReplicationAddressByName("n1"))
}

// Whitespace-only input is the same as unset; otherwise a stray space would end
// up in a .res file as an unusable address.
func TestRegistrationTrimsReplicationAddress(t *testing.T) {
	dep := &fakeDeploymentClient{execFunc: hostnameExecFunc("n1")}
	ctrl := newBasicTestController(dep)
	ctrl.db = openTestDB(t)

	node, err := ctrl.nodes.RegisterNodeWithReplicationAddress(
		context.Background(), "n1", "10.0.0.1", "   ")
	require.NoError(t, err)
	assert.Empty(t, node.ReplicationAddress)
	assert.Equal(t, "10.0.0.1", ctrl.nodes.GetReplicationAddressByName("n1"))
}

// hostnameExecFunc answers the `hostname` probe RegisterNode runs, so a fake
// node registers successfully without any SSH.
func hostnameExecFunc(hostname string) func(context.Context, []string, string, ...deployment.ExecOption) (*deployment.ExecResult, error) {
	return func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		if cmd == "hostname" {
			return successExecResult(hosts, hostname+"\n"), nil
		}
		return successExecResult(hosts, ""), nil
	}
}
