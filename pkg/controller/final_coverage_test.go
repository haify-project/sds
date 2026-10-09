package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestControllerLoadFromDatabase(t *testing.T) {
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.db = newTestDB(t)
	ctx := context.Background()
	require.NoError(t, ctrl.db.SaveNode(ctx, &database.Node{
		Name: "node1", Address: "10.0.0.1", Hostname: "node1.local", State: "online",
		Version: "1.2.3", Labels: `{"zone":"west"}`, LastSeen: time.Now(),
	}))
	require.NoError(t, ctrl.db.SaveNode(ctx, &database.Node{
		Name: "node2", Address: "10.0.0.2", Hostname: "node2.local", State: "offline", Labels: `{invalid`,
	}))
	require.NoError(t, ctrl.db.SaveGateway(ctx, &database.Gateway{Name: "gw", Resource: "res", Type: database.GatewayTypeNFS}))

	require.NoError(t, ctrl.loadFromDatabase(ctx))
	node, err := ctrl.nodes.GetNode(ctx, "10.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, "west", node.Labels["zone"])
	assert.Equal(t, "10.0.0.1", ctrl.ResolveHost("node1.local"))
	assert.Equal(t, "10.0.0.1", ctrl.ResolveHost("node1"))
	node2, err := ctrl.nodes.GetNode(ctx, "10.0.0.2")
	require.NoError(t, err)
	assert.Nil(t, node2.Labels)
}

func TestGatewayResourceManagerAdapter(t *testing.T) {
	ctx := context.Background()
	dep := &fakeDeploymentClient{}
	dep.drbdStatusFunc = func(_ context.Context, hosts []string, _ string) (*deployment.ExecResult, error) {
		return successExecResult(hosts, "res1 role:Secondary\n  volume:0 minor:100 disk:UpToDate"), nil
	}
	dep.drbdStatusJSONFunc = func(_ context.Context, hosts []string, _ string) (*deployment.ExecResult, error) {
		return successExecResult(hosts, `[{"name":"res1","role":"Secondary","devices":[{"volume":0,"minor":100,"disk-state":"UpToDate"}]}]`), nil
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	ctrl.hostsMap["n1"] = "10.0.0.1"
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "res1", Nodes: "n1", Port: 7001, Protocol: "C"}))
	require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{ResourceName: "res1", VolumeName: "data", VolumeID: 0, Pool: "fast", SizeGB: 10, Device: "/dev/drbd100"}))

	adapter := NewGatewayResourceManager(ctrl.resources, true, 0).(*GatewayResourceManager)
	assert.Equal(t, uint32(1), adapter.stateVolumeSizeGB)
	info, err := adapter.GetResource(ctx, "res1")
	require.NoError(t, err)
	assert.Equal(t, "res1", info.Name)
	require.Len(t, info.Volumes, 1)
	assert.Equal(t, "/dev/drbd100", info.Volumes[0].Device)
	assert.Equal(t, "Secondary", info.NodeStates["n1"].Role)
	require.NoError(t, adapter.EnsureGatewayVolumes(ctx, "res1", 1))

	disabled := NewGatewayResourceManager(ctrl.resources, false, 1).(*GatewayResourceManager)
	require.NoError(t, disabled.EnsureGatewayVolumes(ctx, "missing", 2))
	require.NoError(t, adapter.SetPrimary(ctx, "res1", "n1", false))

	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "empty", Nodes: "n1"}))
	err = adapter.EnsureGatewayVolumes(ctx, "empty", 2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot determine storage pool")
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "nopool", Nodes: "n1"}))
	require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{ResourceName: "nopool", VolumeName: "data", VolumeID: 0, SizeGB: 1}))
	err = adapter.EnsureGatewayVolumes(ctx, "nopool", 2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot determine storage pool")
}

func TestDrainNodeMovesPrimary(t *testing.T) {
	ctx := context.Background()
	dep := &fakeDeploymentClient{}
	var secondaryHost, primaryHost string
	dep.drbdSecondaryFunc = func(_ context.Context, host, _ string) (*deployment.HostResult, error) {
		secondaryHost = host
		return &deployment.HostResult{Host: host, Success: true}, nil
	}
	dep.drbdPrimaryFunc = func(_ context.Context, host, _ string, _ bool) (*deployment.HostResult, error) {
		primaryHost = host
		return &deployment.HostResult{Host: host, Success: true}, nil
	}
	dep.drbdStatusFunc = func(_ context.Context, hosts []string, _ string) (*deployment.ExecResult, error) {
		return successExecResult(hosts, "res1 role:Primary\n  volume:0 minor:100 disk:UpToDate"), nil
	}
	dep.drbdStatusJSONFunc = func(_ context.Context, hosts []string, _ string) (*deployment.ExecResult, error) {
		return successExecResult(hosts, `[{"name":"res1","role":"Primary","devices":[{"volume":0,"minor":100,"disk-state":"UpToDate"}],"connections":[{"name":"n2","peer-role":"Secondary","connection-state":"Connected","peer_devices":[{"volume":0,"peer-disk-state":"UpToDate"}]}]}]`), nil
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	for _, node := range []struct{ name, addr string }{{"n1", "10.0.0.1"}, {"n2", "10.0.0.2"}} {
		_, err := ctrl.nodes.RegisterNode(ctx, node.name, node.addr)
		require.NoError(t, err)
		ctrl.hostsMap[node.name] = node.addr
	}
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "res1", Nodes: "n1,n2"}))
	require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{ResourceName: "res1", VolumeName: "data", VolumeID: 0, Pool: "fast", SizeGB: 1}))

	moved, err := ctrl.resources.DrainNode(ctx, "n1")
	require.NoError(t, err)
	assert.Equal(t, []string{"res1"}, moved)
	assert.Equal(t, "10.0.0.1", secondaryHost)
	assert.Equal(t, "10.0.0.2", primaryHost)
	node, err := ctrl.db.GetNode(ctx, "10.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, string(NodeStateMaintenance), node.State)
}

func TestWANStatusBranches(t *testing.T) {
	ctx := context.Background()
	dep := &fakeDeploymentClient{execFunc: func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		if strings.Contains(cmd, "systemctl is-active") {
			return successExecResult(hosts, "active"), nil
		}
		return successExecResult(hosts, ""), nil
	}}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	ctrl.hostsMap["n1"] = "10.0.0.1"
	ctrl.hostsMap["n2"] = "10.0.0.2"
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{
		Name: "wan1", Nodes: "n1,n2", WANMode: true, DRNode: "n2",
		DREndpoint: "203.0.113.2", WANPort: 43516, Port: 7001,
	}))

	status, err := ctrl.resources.WANStatus(ctx, "wan1")
	require.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, "active", status.ProxyState["n1"])
	assert.Equal(t, "active", status.ProxyState["n2"])
	assert.True(t, status.WANReachable)
	status, err = ctrl.resources.WANStatus(ctx, "missing")
	require.NoError(t, err)
	assert.Nil(t, status)
	status, err = (&ResourceManager{controller: &Controller{}}).WANStatus(ctx, "missing")
	require.NoError(t, err)
	assert.Nil(t, status)
}

func TestNodeStatusFailureBranches(t *testing.T) {
	ctx := context.Background()
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	_, err := ctrl.nodes.GetNodeStatus(ctx, "missing")
	assert.Error(t, err)
	assert.Error(t, ctrl.nodes.CheckNodeHealth(ctx, "missing"))

	dep := &fakeDeploymentClient{execFunc: func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		if strings.Contains(cmd, "drbdadm status") {
			return nil, assert.AnError
		}
		return successExecResult(hosts, ""), nil
	}}
	ctrl = newBasicTestController(dep)
	_, err = ctrl.nodes.RegisterNode(ctx, "n1", "10.0.0.1")
	require.NoError(t, err)
	_, err = ctrl.nodes.GetNodeStatus(ctx, "10.0.0.1")
	assert.Error(t, err)

	dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		if strings.Contains(cmd, "vgs") {
			return nil, assert.AnError
		}
		return successExecResult(hosts, "r1"), nil
	}
	_, err = ctrl.nodes.GetNodeStatus(ctx, "10.0.0.1")
	assert.Error(t, err)

	dep.execFunc = func(_ context.Context, hosts []string, _ string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		return failedResult(hosts, "not ok"), nil
	}
	assert.Error(t, ctrl.nodes.CheckNodeHealth(ctx, "10.0.0.1"))
}

func TestScheduleManagerValidationBranches(t *testing.T) {
	ctx := context.Background()
	bare := NewScheduleManager(&Controller{logger: zap.NewNop()})
	assert.Error(t, bare.CreateSchedule(ctx, "r", "0 * * * *", database.GFSPolicy{Hourly: 1}, true, nil))
	assert.Error(t, bare.DeleteSchedule(ctx, "r"))
	_, err := bare.ListSchedules(ctx)
	assert.Error(t, err)

	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.db = newTestDB(t)
	assert.Error(t, ctrl.schedules.CreateSchedule(ctx, "r", "0 * * * *", database.GFSPolicy{}, true, nil))
	assert.Error(t, ctrl.schedules.CreateSchedule(ctx, "missing", "0 * * * *", database.GFSPolicy{Hourly: 1}, true, nil))
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "r", Nodes: "n1"}))
	require.NoError(t, ctrl.schedules.Start(ctx))
	require.NoError(t, ctrl.schedules.CreateSchedule(ctx, "r", "0 * * * *", database.GFSPolicy{Hourly: 1}, false, nil))
	assert.True(t, ctrl.schedules.started)
	require.NoError(t, ctrl.schedules.DeleteSchedule(ctx, "r"))
	ctrl.schedules.Stop()
}

func TestGatewayRuntimeNilResources(t *testing.T) {
	srv := &Server{}
	state, node, options := srv.gatewayRuntimeInfo(context.Background(), "r")
	assert.Equal(t, "configured", state)
	assert.Empty(t, node)
	assert.Empty(t, options)
}
