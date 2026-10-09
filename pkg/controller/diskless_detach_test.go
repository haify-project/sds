package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/deployment"
)

// detachFixture is resource "data" on node1 and node2 with diskless clients
// c1 and c2; once registered, every command on a host in down fails as an
// unreachable host does.
func detachFixture(t *testing.T, config string, downAfterSetup map[string]bool) (*Controller, *[]string) {
	t.Helper()
	var cmds []string
	down := map[string]bool{}
	dep := &fakeDeploymentClient{}
	dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		res := successExecResult(hosts, config)
		for _, h := range hosts {
			cmds = append(cmds, h+": "+cmd)
			if down[h] {
				res.Hosts[h] = &deployment.HostResult{Host: h, Output: "ssh: handshake failed: host key changed"}
			}
		}
		return res, nil
	}
	dep.distributeConfigFunc = func(_ context.Context, hosts []string, _, _ string, _ ...deployment.ConfigOption) (*deployment.ConfigResult, error) {
		for _, h := range hosts {
			cmds = append(cmds, h+": distribute")
		}
		return &deployment.ConfigResult{}, nil
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	ctx := context.Background()
	for name, ip := range map[string]string{"node1": "10.0.0.1", "node2": "10.0.0.2", "c1": "10.0.0.8", "c2": "10.0.0.9"} {
		_, err := ctrl.nodes.RegisterNode(ctx, name, ip)
		require.NoError(t, err)
	}
	require.NoError(t, ctrl.loadHostsFromDatabase(ctx))
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{
		Name: "data", Nodes: "node1,node2", Port: 7000, DisklessClients: "c1,c2",
	}))
	for h := range downAfterSetup {
		down[h] = true
	}
	cmds = nil
	return ctrl, &cmds
}

// A client whose machine is gone is detached although the other client cannot
// be reached either: the replicas take the new config, and the unreachable
// client is only a warning.
func TestDetachDisklessClientWithAnotherUnreachable(t *testing.T) {
	cfg, err := addDisklessClientBlock(twoNodeConfig, "c1", "10.0.0.8", 7000)
	require.NoError(t, err)
	cfg, err = addDisklessClientBlock(cfg, "c2", "10.0.0.9", 7000)
	require.NoError(t, err)
	ctrl, cmds := detachFixture(t, cfg, map[string]bool{"10.0.0.8": true, "10.0.0.9": true})

	require.NoError(t, ctrl.resources.DetachDisklessClient(context.Background(), "data", "c1"))
	joined := strings.Join(*cmds, "\n")
	for _, h := range []string{"10.0.0.1", "10.0.0.2"} {
		assert.Contains(t, joined, h+": sudo drbdadm adjust data", "the replicas are adjusted")
	}
	assert.Contains(t, joined, "10.0.0.9: sudo drbdadm adjust data", "the other client is tried")
	got, err := ctrl.db.GetResource(context.Background(), "data")
	require.NoError(t, err)
	assert.Equal(t, "c2", got.DisklessClients)
}

// A retry after a detach that wrote the config out and then failed still
// adjusts the replicas, so they drop the connection to the client.
func TestDetachDisklessClientRetryAdjustsReplicas(t *testing.T) {
	cfg, err := addDisklessClientBlock(twoNodeConfig, "c2", "10.0.0.9", 7000)
	require.NoError(t, err)
	ctrl, cmds := detachFixture(t, cfg, map[string]bool{"10.0.0.8": true})

	require.NoError(t, ctrl.resources.DetachDisklessClient(context.Background(), "data", "c1"))
	joined := strings.Join(*cmds, "\n")
	for _, h := range []string{"10.0.0.1", "10.0.0.2"} {
		assert.Contains(t, joined, h+": sudo drbdadm adjust data")
	}
	got, err := ctrl.db.GetResource(context.Background(), "data")
	require.NoError(t, err)
	assert.Equal(t, "c2", got.DisklessClients)
}

// A replica that cannot take the new config fails the detach, and the client
// stays on record so a retry finishes it.
func TestDetachDisklessClientReplicaDown(t *testing.T) {
	cfg, err := addDisklessClientBlock(twoNodeConfig, "c1", "10.0.0.8", 7000)
	require.NoError(t, err)
	ctrl, _ := detachFixture(t, cfg, map[string]bool{"10.0.0.2": true})

	require.Error(t, ctrl.resources.DetachDisklessClient(context.Background(), "data", "c1"))
	got, err := ctrl.db.GetResource(context.Background(), "data")
	require.NoError(t, err)
	assert.Contains(t, got.DisklessClients, "c1")
}
