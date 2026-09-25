package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	sdspb "github.com/liliang-cn/sds/api/proto/v1"
	"github.com/liliang-cn/sds/pkg/config"
	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestControllerNewAndAccessors(t *testing.T) {
	cfg := &config.Config{
		Database: config.DatabaseConfig{Path: t.TempDir() + "/sds.db"},
		Gateway:  config.GatewayConfig{AutoStateVolume: true, StateVolumeSizeGB: 2},
	}
	ctrl, err := New(cfg, zap.NewNop())
	require.NoError(t, err)
	require.NotNil(t, ctrl.storage)
	require.NotNil(t, ctrl.resources)
	require.NotNil(t, ctrl.snapshots)
	require.NotNil(t, ctrl.nodes)
	require.NotNil(t, ctrl.gateway)
	assert.Equal(t, ctrl.deployment, ctrl.GetDeployment())
	assert.Nil(t, ctrl.GetMetrics())
	assert.Empty(t, ctrl.GetHosts())
	require.NoError(t, ctrl.Close())
}

func TestControllerInitHostsMapping(t *testing.T) {
	dep := &fakeDeploymentClient{execFunc: func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		assert.Equal(t, "hostname", cmd)
		return successExecResult(hosts, "node-a"), nil
	}}
	ctrl := newBasicTestController(dep)
	ctrl.hosts = []string{"10.0.0.1"}
	ctrl.initHostsMapping()
	assert.Equal(t, "10.0.0.1", ctrl.hostsMap["node-a"])
}

func TestServerProfileValidationAndConversions(t *testing.T) {
	srv, _ := newServerWithNodes(t)
	ctx := context.Background()

	for _, req := range []*sdspb.CreateResourceProfileRequest{
		{},
		{Profile: &sdspb.ResourceProfile{}},
	} {
		resp, err := srv.CreateResourceProfile(ctx, req)
		require.NoError(t, err)
		assert.False(t, resp.Success)
	}

	profile := &database.ResourceProfile{
		Name: "p", Protocol: "C", StorageType: "lvm", Pool: "fast", Replicas: 2,
		OnDifferent: []string{"zone"}, OnSame: []string{"rack"},
		DRBDOptions: map[string]string{"net/max-buffers": "8000"}, Labels: map[string]string{"env": "prod"},
	}
	pb := profileToProto(profile)
	assert.Equal(t, "p", pb.Name)
	assert.Equal(t, []string{"zone"}, pb.ReplicasOnDifferent)
	assert.Equal(t, profile, profileFromProto(pb))
	assert.Nil(t, profileToProto(nil))
	assert.Nil(t, profileFromProto(nil))

	missing, err := srv.DeleteResourceProfile(ctx, &sdspb.DeleteResourceProfileRequest{Name: "missing"})
	require.NoError(t, err)
	assert.True(t, missing.Success, "deleting a missing profile is idempotent")

	policy := database.GFSPolicy{Hourly: 1, Daily: 2, Weekly: 3, Monthly: 4, Yearly: 5}
	assert.Equal(t, policy, gfsFromProto(gfsToProto(policy)))
	assert.Equal(t, database.GFSPolicy{}, gfsFromProto(nil))
}

func TestServerSnapshotRestoreBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		dep := &fakeDeploymentClient{}
		var merged bool
		dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.Contains(decodeWrapped(cmd), "lvconvert --merge /dev/vg0/snap1") {
				merged = true
			}
			// No resource config names the volume: it is a plain LV.
			return successExecResult(hosts, ""), nil
		}
		defer func() { assert.True(t, merged, "the snapshot was never merged") }()
		ctrl := newBasicTestController(dep)
		resp, err := NewServer(ctrl).RestoreSnapshot(ctx, &sdspb.RestoreSnapshotRequest{Volume: "vg0/data", SnapshotName: "snap1", Node: "n1"})
		require.NoError(t, err)
		assert.True(t, resp.Success)
	})

	t.Run("exec error", func(t *testing.T) {
		dep := &fakeDeploymentClient{execFunc: func(context.Context, []string, string, ...deployment.ExecOption) (*deployment.ExecResult, error) {
			return nil, errors.New("ssh failed")
		}}
		resp, err := NewServer(newBasicTestController(dep)).RestoreSnapshot(ctx, &sdspb.RestoreSnapshotRequest{Volume: "vg0/data", SnapshotName: "snap1", Node: "n1"})
		require.NoError(t, err)
		assert.False(t, resp.Success)
		assert.Contains(t, resp.Message, "ssh failed")
	})

	t.Run("host failure", func(t *testing.T) {
		dep := &fakeDeploymentClient{execFunc: func(_ context.Context, hosts []string, _ string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
			result := &deployment.ExecResult{Hosts: map[string]*deployment.HostResult{}}
			for _, host := range hosts {
				result.Hosts[host] = &deployment.HostResult{Host: host, Success: false, Output: "merge refused"}
			}
			return result, nil
		}}
		resp, err := NewServer(newBasicTestController(dep)).RestoreSnapshot(ctx, &sdspb.RestoreSnapshotRequest{Volume: "data", SnapshotName: "snap1", Node: "n1"})
		require.NoError(t, err)
		assert.False(t, resp.Success)
	})
}

func TestServerDisklessHandlers(t *testing.T) {
	srv, ctrl := newServerWithNodes(t, "n1", "n2")
	ctx := context.Background()
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "res1", Nodes: "n1,n2"}))

	attach, err := srv.AttachDisklessClient(ctx, &sdspb.AttachDisklessClientRequest{Resource: "res1", Node: "n1"})
	require.NoError(t, err)
	assert.False(t, attach.Success)
	assert.Contains(t, attach.Message, "diskful replica")

	detach, err := srv.DetachDisklessClient(ctx, &sdspb.DetachDisklessClientRequest{Resource: "missing", Node: "n3"})
	require.NoError(t, err)
	assert.False(t, detach.Success)
}

func TestServerSnapshotScheduleDetails(t *testing.T) {
	srv, ctrl := newServerWithNodes(t, "n1")
	ctx := context.Background()
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	require.NoError(t, ctrl.db.SaveSnapshotSchedule(ctx, &database.SnapshotSchedule{
		Name: "res1", Resource: "res1", Cron: "0 * * * *", Enabled: true,
		Keep: database.GFSPolicy{Hourly: 6, Daily: 7}, LastRun: now,
	}))

	resp, err := srv.ListSnapshotSchedules(ctx, &sdspb.ListSnapshotSchedulesRequest{})
	require.NoError(t, err)
	require.True(t, resp.Success)
	require.Len(t, resp.Schedules, 1)
	assert.NotEmpty(t, resp.Schedules[0].LastRun)
	assert.NotEmpty(t, resp.Schedules[0].NextRun)
	assert.Equal(t, int32(6), resp.Schedules[0].Keep.Hourly)
}

func TestServerPreviouslyUntouchedErrorHandlers(t *testing.T) {
	srv, _ := newServerWithNodes(t)
	ctx := context.Background()

	makeHa, err := srv.MakeHa(ctx, &sdspb.MakeHaRequest{
		Resource:   "missing",
		OcfAgents:  []*sdspb.OcfAgent{nil, {Provider: "heartbeat", Name: "IPaddr2", Instance: "vip"}},
		StartItems: []*sdspb.HaStartItem{nil, {Item: &sdspb.HaStartItem_SystemdUnit{SystemdUnit: "postgresql.service"}}},
	})
	require.NoError(t, err)
	assert.False(t, makeHa.Success)

	for _, check := range []func() (bool, error){
		func() (bool, error) { r, e := srv.EnableSelfHa(ctx, &sdspb.EnableSelfHaRequest{}); return r.Success, e },
		func() (bool, error) {
			r, e := srv.DisableSelfHa(ctx, &sdspb.DisableSelfHaRequest{})
			return r.Success, e
		},
		func() (bool, error) {
			r, e := srv.EvictHa(ctx, &sdspb.EvictHaRequest{Resource: "missing"})
			return r.Success, e
		},
		func() (bool, error) {
			r, e := srv.DeleteHa(ctx, &sdspb.DeleteHaRequest{Resource: "missing"})
			return r.Success, e
		},
	} {
		success, err := check()
		require.NoError(t, err)
		assert.False(t, success)
	}

	_, err = srv.GetResourceAgentMetadata(ctx, &sdspb.GetResourceAgentMetadataRequest{Provider: "missing", Name: "missing"})
	assert.Error(t, err)
	_, err = srv.GetHaToml(ctx, &sdspb.GetHaTomlRequest{Resource: "missing"})
	assert.Error(t, err)
	syncResp, err := srv.SyncHaToml(ctx, &sdspb.SyncHaTomlRequest{Resource: "missing", Content: "[[promoter]]"})
	require.NoError(t, err)
	assert.False(t, syncResp.Success)
}
