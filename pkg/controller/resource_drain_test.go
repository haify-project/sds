package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// drainPeer is how n1 (the node being drained, Primary) sees one peer.
type drainPeer struct{ name, conn, disk string }

// drainStatusJSON is `drbdsetup status --json` for res1 as n1 reports it.
func drainStatusJSON(peers ...drainPeer) string {
	var conns []string
	for _, p := range peers {
		conns = append(conns, fmt.Sprintf(
			`{"name":%q,"peer-role":"Secondary","connection-state":%q,"peer_devices":[{"volume":0,"peer-disk-state":%q}]}`,
			p.name, p.conn, p.disk))
	}
	return `[{"name":"res1","role":"Primary","devices":[{"volume":0,"minor":100,"disk-state":"UpToDate"}],"connections":[` +
		strings.Join(conns, ",") + `]}]`
}

// drainCluster is a controller with n1..n3 registered and res1 Primary on n1,
// recording every demote, promote and remote command.
type drainCluster struct {
	ctrl       *Controller
	secondary  []string
	primary    []string
	execs      []string
	failOnHost map[string]bool // promote fails on these addresses
}

func newDrainCluster(t *testing.T, res *database.Resource, status string) *drainCluster {
	t.Helper()
	ctx := context.Background()
	c := &drainCluster{failOnHost: map[string]bool{}}
	dep := &fakeDeploymentClient{}
	dep.drbdSecondaryFunc = func(_ context.Context, host, _ string) (*deployment.HostResult, error) {
		c.secondary = append(c.secondary, host)
		return &deployment.HostResult{Host: host, Success: true}, nil
	}
	dep.drbdPrimaryFunc = func(_ context.Context, host, _ string, _ bool) (*deployment.HostResult, error) {
		c.primary = append(c.primary, host)
		if c.failOnHost[host] {
			return &deployment.HostResult{Host: host, Success: false, Output: "State change failed"}, nil
		}
		return &deployment.HostResult{Host: host, Success: true}, nil
	}
	dep.drbdStatusFunc = func(_ context.Context, hosts []string, _ string) (*deployment.ExecResult, error) {
		return successExecResult(hosts, "res1 role:Primary\n  volume:0 minor:100 disk:UpToDate"), nil
	}
	dep.drbdStatusJSONFunc = func(_ context.Context, hosts []string, _ string) (*deployment.ExecResult, error) {
		return successExecResult(hosts, status), nil
	}
	dep.reactorPromoterStatusByResourceFunc = func(_ context.Context, _, _ string) (*deployment.ReactorPromoterStatus, error) {
		return &deployment.ReactorPromoterStatus{PrimaryOn: "n1"}, nil
	}
	dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		c.execs = append(c.execs, strings.Join(hosts, ",")+": "+cmd)
		return successExecResult(hosts, ""), nil
	}
	c.ctrl = newBasicTestController(dep)
	c.ctrl.db = newTestDB(t)
	for i, name := range []string{"n1", "n2", "n3"} {
		addr := fmt.Sprintf("10.0.0.%d", i+1)
		_, err := c.ctrl.nodes.RegisterNode(ctx, name, addr)
		require.NoError(t, err)
		c.ctrl.hostsMap[name] = addr
	}
	c.execs = nil
	require.NoError(t, c.ctrl.db.SaveResource(ctx, res))
	require.NoError(t, c.ctrl.db.SaveVolume(ctx, &database.Volume{ResourceName: res.Name, VolumeName: "data", VolumeID: 0, Pool: "vg0", SizeGB: 1}))
	return c
}

func (c *drainCluster) state(t *testing.T, addr string) NodeState {
	t.Helper()
	c.ctrl.nodes.mu.RLock()
	defer c.ctrl.nodes.mu.RUnlock()
	return c.ctrl.nodes.nodes[addr].State
}

// Drain marks the node maintenance where placement reads it (the registry),
// not just in the database, and the health check leaves it that way.
func TestDrainNodeStateIsAuthoritative(t *testing.T) {
	ctx := context.Background()
	c := newDrainCluster(t, &database.Resource{Name: "res1", Nodes: "n2,n3"}, drainStatusJSON())

	_, err := c.ctrl.resources.DrainNode(ctx, "n1")
	require.NoError(t, err)
	assert.Equal(t, NodeStateMaintenance, c.state(t, "10.0.0.1"))
	rec, err := c.ctrl.db.GetNode(ctx, "10.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, string(NodeStateMaintenance), rec.State)

	// A passing health check, a failing one, and a re-registration all keep it drained.
	require.NoError(t, c.ctrl.nodes.CheckNodeHealth(ctx, "10.0.0.1"))
	assert.Equal(t, NodeStateMaintenance, c.state(t, "10.0.0.1"))
	c.ctrl.deployment.(*fakeDeploymentClient).execFunc = func(_ context.Context, _ []string, _ string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		return nil, errors.New("ssh: connection refused")
	}
	require.Error(t, c.ctrl.nodes.CheckNodeHealth(ctx, "10.0.0.1"))
	assert.Equal(t, NodeStateMaintenance, c.state(t, "10.0.0.1"))
	c.ctrl.deployment.(*fakeDeploymentClient).execFunc = nil
	_, err = c.ctrl.nodes.RegisterNode(ctx, "n1", "10.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, NodeStateMaintenance, c.state(t, "10.0.0.1"))

	// Tiebreaker selection passes over it: n1 would win on name order.
	assert.Equal(t, "n3", c.ctrl.resources.selectTiebreaker(ctx, []string{"n2"}))

	require.NoError(t, c.ctrl.resources.UndrainNode(ctx, "n1"))
	assert.Equal(t, NodeStateOnline, c.state(t, "10.0.0.1"))
	assert.Equal(t, "n1", c.ctrl.resources.selectTiebreaker(ctx, []string{"n2"}))
}

// Auto-placement never puts a new replica on a drained node.
func TestSelectPlacementNodesSkipsDrainedNode(t *testing.T) {
	ctx := context.Background()
	ctrl := newPlacementTestCluster(t, "  haify_vg0|214748364800|214748364800|/dev/vdb", "")

	_, err := ctrl.resources.DrainNode(ctx, "n1")
	require.NoError(t, err)

	got, _, err := ctrl.resources.selectPlacementNodes(ctx, "vg0", 10, 1, nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"n2"}, got)
	_, _, err = ctrl.resources.selectPlacementNodes(ctx, "vg0", 10, 2, nil, nil, nil)
	assert.Error(t, err, "only one undrained node is left")

	require.NoError(t, ctrl.resources.UndrainNode(ctx, "n1"))
	got, _, err = ctrl.resources.selectPlacementNodes(ctx, "vg0", 10, 2, nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "n1,n2", join(got))
}

// The takeover node must be UpToDate and connected; an Outdated replica that
// comes first in the node list is passed over.
func TestDrainNodePicksUpToDateConnectedTarget(t *testing.T) {
	c := newDrainCluster(t, &database.Resource{Name: "res1", Nodes: "n1,n2,n3"},
		drainStatusJSON(drainPeer{"n2", "Connected", "Outdated"}, drainPeer{"n3", "Connected", "UpToDate"}))

	moved, err := c.ctrl.resources.DrainNode(context.Background(), "n1")
	require.NoError(t, err)
	assert.Equal(t, []string{"res1"}, moved)
	assert.Equal(t, []string{"10.0.0.1"}, c.secondary)
	assert.Equal(t, []string{"10.0.0.3"}, c.primary)
}

// A WAN resource's DR node never takes over, a disconnected or drained peer
// neither; with no fit target the resource is refused untouched.
func TestDrainNodeRefusesWithoutSafeTarget(t *testing.T) {
	ctx := context.Background()
	c := newDrainCluster(t,
		&database.Resource{Name: "res1", Nodes: "n1,n2,n3", WANMode: true, DRNode: "n2"},
		drainStatusJSON(drainPeer{"n2", "Connected", "UpToDate"}, drainPeer{"n3", "Connecting", ""}))

	moved, err := c.ctrl.resources.DrainNode(ctx, "n1")
	require.Error(t, err)
	assert.Empty(t, moved)
	assert.Contains(t, err.Error(), "res1: no replica can take over safely")
	assert.Contains(t, err.Error(), "n2 (DR node")
	assert.Contains(t, err.Error(), "n3 (disk unknown)")
	assert.Empty(t, c.secondary, "nothing may be demoted when no target qualifies")
	assert.Empty(t, c.primary)
	assert.Equal(t, NodeStateMaintenance, c.state(t, "10.0.0.1"), "the node stays drained")

	// n3 now healthy but itself drained: still not a target.
	c2 := newDrainCluster(t, &database.Resource{Name: "res1", Nodes: "n1,n3"},
		drainStatusJSON(drainPeer{"n3", "Connected", "UpToDate"}))
	require.NoError(t, c2.ctrl.nodes.setMaintenance(ctx, "10.0.0.3", true))
	_, err = c2.ctrl.resources.DrainNode(ctx, "n1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "n3 (node is maintenance)")
	assert.Empty(t, c2.secondary)
}

// A promote that fails after the demote puts the Primary back where it was.
func TestDrainNodeRepromotesOriginalWhenPromoteFails(t *testing.T) {
	c := newDrainCluster(t, &database.Resource{Name: "res1", Nodes: "n1,n2"},
		drainStatusJSON(drainPeer{"n2", "Connected", "UpToDate"}))
	c.failOnHost["10.0.0.2"] = true

	moved, err := c.ctrl.resources.DrainNode(context.Background(), "n1")
	require.Error(t, err)
	assert.Empty(t, moved)
	assert.Contains(t, err.Error(), "n1 was promoted back and is Primary again")
	assert.Equal(t, []string{"10.0.0.1"}, c.secondary)
	assert.Equal(t, []string{"10.0.0.2", "10.0.0.1"}, c.primary)

	c.failOnHost["10.0.0.1"] = true
	c.secondary, c.primary = nil, nil
	_, err = c.ctrl.resources.DrainNode(context.Background(), "n1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the resource has no Primary")
}

// HA and gateway resources move through the promoter's eviction, never a raw
// demote/promote, which drbd-reactor would undo or the mount would block.
func TestDrainNodeEvictsReactorManagedResources(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"ha", "gateway"} {
		t.Run(kind, func(t *testing.T) {
			c := newDrainCluster(t, &database.Resource{Name: "res1", Nodes: "n1,n2"},
				drainStatusJSON(drainPeer{"n2", "Connected", "UpToDate"}))
			if kind == "ha" {
				require.NoError(t, c.ctrl.db.SaveHaConfig(ctx, &database.HaConfig{Resource: "res1"}))
			} else {
				require.NoError(t, c.ctrl.db.SaveGateway(ctx, &database.Gateway{Name: "gw1", Resource: "res1", Type: database.GatewayTypeNFS}))
			}

			moved, err := c.ctrl.resources.DrainNode(ctx, "n1")
			require.NoError(t, err)
			assert.Equal(t, []string{"res1"}, moved)
			assert.Empty(t, c.secondary)
			assert.Empty(t, c.primary)
			require.Len(t, c.execs, 1)
			assert.Contains(t, c.execs[0], "10.0.0.1: echo "+base64Std(evictScript("res1")))
		})
	}

	// A failed eviction is reported with the command to finish it.
	c := newDrainCluster(t, &database.Resource{Name: "res1", Nodes: "n1,n2"},
		drainStatusJSON(drainPeer{"n2", "Connected", "UpToDate"}))
	require.NoError(t, c.ctrl.db.SaveHaConfig(ctx, &database.HaConfig{Resource: "res1"}))
	c.ctrl.deployment.(*fakeDeploymentClient).execFunc = func(_ context.Context, hosts []string, _ string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		return &deployment.ExecResult{Hosts: map[string]*deployment.HostResult{
			hosts[0]: {Host: hosts[0], Success: false, Output: "no other node took over res1"}}}, nil
	}
	moved, err := c.ctrl.resources.DrainNode(ctx, "n1")
	require.Error(t, err)
	assert.Empty(t, moved)
	assert.Contains(t, err.Error(), "run `haify ha evict res1`")
	assert.Empty(t, c.secondary)
}
