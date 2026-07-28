package controller

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/sds/pkg/config"
	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/deployment"
	"github.com/liliang-cn/sds/pkg/gateway"
	"github.com/liliang-cn/sds/pkg/metrics"
	"github.com/liliang-cn/sds/pkg/wanproxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestControllerStartAndStopServers(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.ctx, ctrl.cancel = context.WithCancel(context.Background())
	ctrl.db = newTestDB(t)
	ctrl.config = &config.Config{
		Server: config.ServerConfig{ListenAddress: "127.0.0.1", Port: 43510},
	}

	require.NoError(t, ctrl.Start())
	require.NotNil(t, ctrl.server)
	require.NotNil(t, ctrl.restServer)
	require.NotNil(t, ctrl.uiServer)
	assert.Empty(t, ctrl.resources.GetHosts())

	ctrl.Stop()
}

func TestControllerStartReportsOccupiedGRPCPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:43511")
	require.NoError(t, err)
	defer listener.Close()

	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.config = &config.Config{Server: config.ServerConfig{ListenAddress: "127.0.0.1", Port: 43511}}
	err = ctrl.Start()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start gRPC server")
}

func TestControllerMetricsServer(t *testing.T) {
	m, err := metrics.New(zap.NewNop())
	require.NoError(t, err)
	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.metrics = m
	ctrl.config = &config.Config{Metrics: config.MetricsConfig{ListenAddress: "127.0.0.1", Port: 43515}}
	require.NoError(t, ctrl.startMetricsServer())
	require.NotNil(t, ctrl.metricsServer)
	require.Eventually(t, func() bool {
		resp, err := http.Get("http://127.0.0.1:43515/metrics")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, time.Second, 20*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, ctrl.metricsServer.Shutdown(ctx))
}

func TestCORSMiddleware(t *testing.T) {
	called := false
	h := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusCreated)
	}))

	preflight := httptest.NewRecorder()
	h.ServeHTTP(preflight, httptest.NewRequest(http.MethodOptions, "/v1/resources", nil))
	assert.Equal(t, http.StatusNoContent, preflight.Code)
	assert.Equal(t, "*", preflight.Header().Get("Access-Control-Allow-Origin"))
	assert.False(t, called)

	request := httptest.NewRecorder()
	h.ServeHTTP(request, httptest.NewRequest(http.MethodPost, "/v1/resources", nil))
	assert.Equal(t, http.StatusCreated, request.Code)
	assert.True(t, called)
}

func TestScheduleManagerLifecycleAndExecution(t *testing.T) {
	dep := &fakeDeploymentClient{}
	dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		switch {
		case strings.Contains(cmd, "drbdsetup status --json"):
			return successExecResult(hosts, `[{"name":"res1","role":"Secondary","devices":[{"volume":0,"minor":100,"disk-state":"UpToDate"}]}]`), nil
		case strings.Contains(cmd, "drbdadm status"):
			return successExecResult(hosts, "res1 role:Secondary\n  disk:UpToDate"), nil
		default:
			return successExecResult(hosts, ""), nil
		}
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	ctx := context.Background()
	_, err := ctrl.nodes.RegisterNode(ctx, "n1", "10.0.0.1")
	require.NoError(t, err)
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "res1", Nodes: "n1", Port: 7001}))
	require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{
		ResourceName: "res1", VolumeName: "res1_data", VolumeID: 0,
		Pool: "sds_vg0", SizeGB: 10, Device: "/dev/drbd100",
	}))
	require.NoError(t, ctrl.db.SaveSnapshotSchedule(ctx, &database.SnapshotSchedule{
		Name: "res1", Resource: "res1", Cron: "0 * * * *", Enabled: true,
		Keep: database.GFSPolicy{Hourly: 2},
	}))

	require.NoError(t, ctrl.schedules.Start(ctx))
	assert.True(t, ctrl.schedules.started)
	require.NoError(t, ctrl.schedules.Start(ctx))
	ctrl.schedules.runSchedule("missing")
	ctrl.schedules.runSchedule("res1")

	schedule, err := ctrl.db.GetSnapshotSchedule(ctx, "res1")
	require.NoError(t, err)
	assert.False(t, schedule.LastRun.IsZero())
	ctrl.schedules.Stop()
	assert.False(t, ctrl.schedules.started)
	ctrl.schedules.Stop()
}

func TestScheduleSnapshotAndPruneZFS(t *testing.T) {
	dep := &fakeDeploymentClient{}
	dep.zfsListSnapshotsFunc = func(_ context.Context, hosts []string, dataset string) (*deployment.ExecResult, error) {
		old := buildSnapName("data", time.Now().UTC().Add(-48*time.Hour))
		current := buildSnapName("other", time.Now().UTC())
		return successExecResult(hosts, dataset+"@"+old+" 1 1 now\n"+dataset+"@"+current+" 1 1 now"), nil
	}
	ctrl := newBasicTestController(dep)
	vol := &ResourceVolumeInfo{Device: "/dev/zvol/tank/data", Pool: "tank", BackingVolume: "data", SizeGB: 10}
	ctx := context.Background()

	ctrl.schedules.snapshotVolume(ctx, "n1", "node1", vol, time.Now().UTC())
	snaps, err := ctrl.schedules.listScheduledSnaps(ctx, "n1", vol)
	require.NoError(t, err)
	require.Len(t, snaps, 1)
	assert.Equal(t, "data", strings.Split(snaps[0].Name, "_sched_")[0])
	ctrl.schedules.pruneVolume(ctx, "n1", "node1", vol, database.GFSPolicy{Hourly: 0, Daily: 1})
}

func TestScheduleSnapshotThickFallbackAndListError(t *testing.T) {
	type scheduleDeploy struct{ fakeDeploymentClient }
	dep := &scheduleDeploy{}
	ctrl := newBasicTestController(dep)
	vol := &ResourceVolumeInfo{Device: "/dev/sds_vg/data", Pool: "sds_vg", BackingVolume: "data", SizeGB: 10}
	ctrl.schedules.snapshotVolume(context.Background(), "n1", "node1", vol, time.Now().UTC())

	dep.lvListSnapshotsFunc = func(context.Context, []string, string) (*deployment.ExecResult, error) {
		return nil, errors.New("list failed")
	}
	_, err := ctrl.schedules.listScheduledSnaps(context.Background(), "n1", vol)
	assert.Error(t, err)
	ctrl.schedules.pruneVolume(context.Background(), "n1", "node1", vol, database.GFSPolicy{Hourly: 1})
	assert.Empty(t, execLines(nil, "n1"))
}

func TestMakeHaSuccessAndRemove(t *testing.T) {
	dep := &fakeDeploymentClient{}
	dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		switch {
		case strings.Contains(cmd, "drbdadm status"):
			return successExecResult(hosts, "res1 role:Primary"), nil
		case strings.Contains(cmd, "systemctl show"):
			return successExecResult(hosts, "LoadState=loaded"), nil
		case strings.Contains(cmd, "blkid"):
			return successExecResult(hosts, "ext4"), nil
		default:
			return successExecResult(hosts, "ok"), nil
		}
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	ctx := context.Background()
	for _, node := range []struct{ name, address string }{{"n1", "10.0.0.1"}, {"n2", "10.0.0.2"}} {
		_, err := ctrl.nodes.RegisterNode(ctx, node.name, node.address)
		require.NoError(t, err)
	}
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "res1", Nodes: "n1,n2", Port: 7001}))

	path, err := ctrl.resources.MakeHa(ctx, "res1", []string{"postgresql.service"}, "/data", "ext4", "10.0.0.50/24", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "/etc/drbd-reactor.d/sds-ha-res1.toml", path)
	require.Len(t, dep.distributedConfigs, 2)
	assert.Contains(t, dep.distributedConfigs[0].remotePath, "data.mount")
	assert.Contains(t, dep.distributedConfigs[1].content, "service-ip@10.0.0.50-24.service")
	assert.Contains(t, dep.distributedConfigs[1].content, "postgresql.service")

	ha, err := ctrl.db.GetHaConfig(ctx, "res1")
	require.NoError(t, err)
	assert.Equal(t, "/data", ha.MountPoint)
	assert.Equal(t, "10.0.0.50/24", ha.VIP)

	require.NoError(t, ctrl.resources.RemoveHa(ctx, "res1"))
	_, err = ctrl.db.GetHaConfig(ctx, "res1")
	assert.Error(t, err)
	assert.Len(t, dep.deleteConfigCalls, 2)
}

func TestMakeHaValidationFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("vip helper", func(t *testing.T) {
		dep := &fakeDeploymentClient{execFunc: func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.Contains(cmd, "service-ip") {
				return failedResult(hosts, "missing"), nil
			}
			return successExecResult(hosts, ""), nil
		}}
		ctrl := newBasicTestController(dep)
		ctrl.db = newTestDB(t)
		_, err := ctrl.nodes.RegisterNode(ctx, "n1", "10.0.0.1")
		require.NoError(t, err)
		require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "res1", Nodes: "n1"}))
		_, err = ctrl.resources.MakeHa(ctx, "res1", nil, "", "", "10.0.0.50/24", nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "service-ip helper")
	})

	t.Run("missing service", func(t *testing.T) {
		dep := &fakeDeploymentClient{execFunc: func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
			if strings.Contains(cmd, "drbdadm status") {
				return successExecResult(hosts, "res1 role:Primary"), nil
			}
			if strings.Contains(cmd, "systemctl show") {
				return successExecResult(hosts, "LoadState=not-found"), nil
			}
			return successExecResult(hosts, ""), nil
		}}
		ctrl := newBasicTestController(dep)
		ctrl.db = newTestDB(t)
		_, err := ctrl.nodes.RegisterNode(ctx, "n1", "10.0.0.1")
		require.NoError(t, err)
		require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "res1", Nodes: "n1"}))
		_, err = ctrl.resources.MakeHa(ctx, "res1", []string{"missing.service"}, "", "", "", nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found on nodes")
	})
}

func failedResult(hosts []string, output string) *deployment.ExecResult {
	result := &deployment.ExecResult{Hosts: make(map[string]*deployment.HostResult, len(hosts))}
	for _, host := range hosts {
		result.Hosts[host] = &deployment.HostResult{Host: host, Success: false, Output: output, Error: errors.New(output)}
	}
	return result
}

func TestWANStatusHelpersAndResourceList(t *testing.T) {
	assert.Equal(t, "WAN status unavailable", wanStatusMessage(nil))
	assert.Equal(t, "primary proxy inactive; DR proxy inactive; DR WAN endpoint unreachable", wanStatusMessage(&wanproxy.ProxyStatus{}))

	ctrl := newBasicTestController(&fakeDeploymentClient{})
	ctrl.db = newTestDB(t)
	ctx := context.Background()
	ctrl.hostsMap["n1"] = "10.0.0.1"
	ctrl.gateway = gateway.New(NewGatewayResourceManager(ctrl.resources, false, 1), &gatewayAdapterDeployment{}, zap.NewNop(), nil)
	ctrl.hostsMap["n2"] = "10.0.0.2"
	res := &database.Resource{Name: "wan1", Nodes: "n1,n2", DRNode: "n2", DREndpoint: "203.0.113.2", WANPort: 43512, Port: 7001, WANMode: true}
	spec := ctrl.resources.wanProxySpecFor(res)
	assert.Equal(t, "10.0.0.1", spec.PrimaryNodeAddr)
	assert.Equal(t, "10.0.0.2", spec.DRNodeAddr)
	assert.Equal(t, 43512, spec.WANPort)

	_, err := (&ResourceManager{controller: &Controller{}}).GetResourceStatusList(ctx)
	assert.Error(t, err)
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "missing-live", Nodes: "n1"}))
	statuses, err := ctrl.resources.GetResourceStatusList(ctx)
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.Equal(t, "missing-live", statuses[0].Name)
	assert.Equal(t, "Unknown", statuses[0].NodeStates["n1"].DiskState)
}

func TestNodeStatusAndHealthBranches(t *testing.T) {
	dep := &fakeDeploymentClient{}
	dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		if strings.Contains(cmd, "drbdadm status") {
			return successExecResult(hosts, "r1\nr2"), nil
		}
		if strings.Contains(cmd, "vgs") {
			return successExecResult(hosts, "vg1\nvg2"), nil
		}
		return successExecResult(hosts, "ok"), nil
	}
	ctrl := newBasicTestController(dep)
	ctx := context.Background()
	_, err := ctrl.nodes.RegisterNode(ctx, "n1", "10.0.0.1")
	require.NoError(t, err)

	status, err := ctrl.nodes.GetNodeStatus(ctx, "10.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, 2, status["drbd"].(map[string]interface{})["resources"])
	assert.Equal(t, 2, status["storage"].(map[string]interface{})["pools"])
	require.NoError(t, ctrl.nodes.CheckNodeHealth(ctx, "10.0.0.1"))
	node, err := ctrl.nodes.GetNode(ctx, "10.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, NodeStateOnline, node.State)

	dep.execFunc = func(context.Context, []string, string, ...deployment.ExecOption) (*deployment.ExecResult, error) {
		return nil, errors.New("offline")
	}
	require.Error(t, ctrl.nodes.CheckNodeHealth(ctx, "10.0.0.1"))
	node, err = ctrl.nodes.GetNode(ctx, "10.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, NodeStateOffline, node.State)
}

func TestGatewayRuntimeInfoLiveStates(t *testing.T) {
	ctx := context.Background()
	dep := &fakeDeploymentClient{}
	dep.drbdStatusFunc = func(_ context.Context, hosts []string, _ string) (*deployment.ExecResult, error) {
		return successExecResult(hosts, "res1 role:Primary\n  volume:0 minor:100 disk:UpToDate"), nil
	}
	dep.drbdStatusJSONFunc = func(_ context.Context, hosts []string, _ string) (*deployment.ExecResult, error) {
		return successExecResult(hosts, `[{"name":"res1","role":"Primary","devices":[{"volume":0,"minor":100,"disk-state":"UpToDate"}]}]`), nil
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	ctrl.hostsMap["n1"] = "10.0.0.1"
	ctrl.gateway = gateway.New(NewGatewayResourceManager(ctrl.resources, false, 1), &gatewayAdapterDeployment{}, zap.NewNop(), nil)
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "res1", Nodes: "n1", Port: 7001}))
	require.NoError(t, ctrl.db.SaveVolume(ctx, &database.Volume{ResourceName: "res1", VolumeName: "data", VolumeID: 0, Pool: "vg", SizeGB: 10}))
	srv := NewServer(ctrl)

	state, node, options := srv.gatewayRuntimeInfo(ctx, "res1")
	assert.Equal(t, "started", state)
	assert.Equal(t, "n1", node)
	assert.Equal(t, "Primary", options["role"])
	assert.Equal(t, "1", options["volumes"])

	srv.gateway = nil
	state, node, _ = srv.gatewayRuntimeInfo(ctx, "res1")
	assert.Equal(t, "failed", state)
	assert.Equal(t, "n1", node)
}
