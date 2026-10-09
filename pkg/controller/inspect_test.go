package controller

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	haifypb "github.com/haify-project/haify/api/proto/v1"
	"github.com/haify-project/haify/pkg/config"
	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/deployment"
	"github.com/haify-project/haify/pkg/event"
	"github.com/haify-project/haify/pkg/inspect"
)

// probeOutput is what inspect.ProbeScript prints on a node holding blk
// Secondary and Outdated.
func probeOutput(host, addr string) string {
	drbd := `[{"name":"blk","role":"Secondary","devices":[{"volume":0,"disk-state":"Outdated"}],"connections":[]}]`
	return strings.Join([]string{
		"probe=1", "hostname=" + host, "now=" + strconv.FormatInt(time.Now().Unix(), 10) + ".0", "rootfs=30",
		"addr=" + addr, "drbd_kmod=9.2.12", "drbd_utils=9.29.0",
		"reactor=1.5.0", "reactor_active=active", "reactor_conf=haify-iscsi-blk.toml",
		"lvs=", "drbd=" + base64.StdEncoding.EncodeToString([]byte(drbd)), "end=1",
	}, "\n")
}

// inspectTestController is two nodes and an iSCSI gateway on blk whose
// replicas are both Outdated; n2 does not answer SSH.
func inspectTestController(t *testing.T) (*Controller, *fakeDeploymentClient) {
	t.Helper()
	dep := &fakeDeploymentClient{}
	var mu sync.Mutex
	dep.execFunc = func(_ context.Context, hosts []string, cmd string, _ ...deployment.ExecOption) (*deployment.ExecResult, error) {
		mu.Lock()
		defer mu.Unlock()
		res := &deployment.ExecResult{Hosts: map[string]*deployment.HostResult{}}
		for _, h := range hosts {
			if h == "10.0.0.2" {
				res.Hosts[h] = &deployment.HostResult{Host: h, Error: errors.New("ssh: i/o timeout")}
				continue
			}
			res.Hosts[h] = &deployment.HostResult{Host: h, Success: true, Output: probeOutput("n1", h)}
		}
		return res, nil
	}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	ctrl.config = &config.Config{
		Alert:    config.AlertConfig{Enabled: true},
		Schedule: config.ScheduleConfig{Enabled: true},
		Inspect:  config.InspectConfig{Enabled: true, Schedule: "0 1 * * *", Keep: 2, NotifyMin: "warn"},
	}
	ctrl.events = event.NewBus(0)
	registerNodes(ctrl, map[string]string{"n1": "10.0.0.1", "n2": "10.0.0.2"})
	ctx := context.Background()
	require.NoError(t, ctrl.db.SaveResource(ctx, &database.Resource{Name: "blk", Nodes: "n1,n2", Port: 7001}))
	require.NoError(t, ctrl.db.SaveGateway(ctx, &database.Gateway{Name: "blk", Resource: "blk",
		Type: database.GatewayTypeISCSI, Status: "started"}))
	return ctrl, dep
}

func TestInspectionRunStoresReportAndPublishes(t *testing.T) {
	ctrl, dep := inspectTestController(t)
	ctx := context.Background()

	r, err := ctrl.inspections.Run(ctx, inspect.TriggerManual, nil)
	require.NoError(t, err)
	assert.Equal(t, "1", r.ID)

	// One SSH round to every node, base64-wrapped.
	probes := execCmdsMatching(dep, base64Std(inspect.ProbeScript))
	require.Len(t, probes, 1)
	assert.ElementsMatch(t, []string{"10.0.0.1", "10.0.0.2"}, probes[0].hosts)
	assert.Contains(t, probes[0].cmd, "| base64 -d | sudo /bin/bash")

	byID := map[string]inspect.Check{}
	for _, c := range r.Checks {
		byID[c.ID] = c
	}
	assert.Equal(t, inspect.StatusFail, byID["gateway.no_primary"].Status)
	assert.Equal(t, "n2", byID["nodes.ssh"].Subject)
	assert.Equal(t, inspect.StatusFail, byID["alerts.no_channel"].Status)
	assert.NotContains(t, byID, "nodes.clock", "n1's clock agrees with the controller's")
	assert.Positive(t, r.Summary.Fail)

	evs := ctrl.events.Recent(event.Filter{Types: []event.Type{event.TypeInspectionCompleted}}, 0, 0)
	require.Len(t, evs, 1)
	assert.Equal(t, event.SeverityCritical, evs[0].Severity)
	assert.Equal(t, "1", evs[0].Details["report"])
	assert.Contains(t, evs[0].Message, "gateway.no_primary blk")

	got, err := ctrl.inspections.Get(ctx, "latest")
	require.NoError(t, err)
	assert.Equal(t, r.ID, got.ID)
	assert.Len(t, got.Checks, len(r.Checks))
	_, err = ctrl.inspections.Get(ctx, "nope")
	assert.Error(t, err)
}

func TestInspectionKeepsOnlyTheConfiguredNumber(t *testing.T) {
	ctrl, _ := inspectTestController(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, err := ctrl.inspections.Run(ctx, inspect.TriggerSchedule, []inspect.Area{inspect.AreaNodes})
		require.NoError(t, err)
	}
	reports, err := ctrl.inspections.List(ctx, 0)
	require.NoError(t, err)
	require.Len(t, reports, 2)
	assert.Equal(t, "3", reports[0].ID)
	assert.Equal(t, "2", reports[1].ID)
	_, err = ctrl.inspections.Get(ctx, "1")
	assert.Error(t, err, "the oldest report is dropped")
}

func TestInspectionDoesNotOverlap(t *testing.T) {
	ctrl, _ := inspectTestController(t)
	ctrl.inspections.running.Store(true)
	_, err := ctrl.inspections.Run(context.Background(), inspect.TriggerManual, nil)
	assert.ErrorIs(t, err, ErrInspectionRunning)

	ctrl.inspections.running.Store(false)
	_, err = ctrl.inspections.Run(context.Background(), inspect.TriggerManual, []inspect.Area{"bogus"})
	assert.ErrorContains(t, err, "unknown area")
}

// Below notify_min nothing is published: a clean daily run pages nobody.
func TestInspectionNotifyMin(t *testing.T) {
	ctrl, _ := inspectTestController(t)
	ctrl.config.Inspect.NotifyMin = "fail"
	_, err := ctrl.inspections.Run(context.Background(), inspect.TriggerManual, []inspect.Area{inspect.AreaTLS})
	require.NoError(t, err)
	assert.Empty(t, ctrl.events.Recent(event.Filter{Types: []event.Type{event.TypeInspectionCompleted}}, 0, 0))
}

// A channel whose deliveries fail is named, and the critical it lost is
// listed — the dead-webhook outage.
func TestInspectionReadsDeliveryRecords(t *testing.T) {
	ctrl, _ := inspectTestController(t)
	ctx := context.Background()
	require.NoError(t, ctrl.db.SaveNotifyChannel(ctx, &database.NotifyChannel{Name: "ops", Kind: "generic",
		URL: "http://192.0.2.1/hook", MinSeverity: "warning", Enabled: true}))
	e := ctrl.events.Publish(event.Event{Type: event.TypeResourceNoPrimary, Severity: event.SeverityCritical,
		Status: event.StatusFiring, Resource: "blk", Message: "blk has no Primary"})
	ctrl.recordDelivery("ops")(e, errors.New("dial tcp 192.0.2.1:80: connect: no route to host"))

	r, err := ctrl.inspections.Run(ctx, inspect.TriggerManual, []inspect.Area{inspect.AreaAlerts})
	require.NoError(t, err)
	byID := map[string]inspect.Check{}
	for _, c := range r.Checks {
		byID[c.ID] = c
	}
	assert.Equal(t, "ops", byID["alerts.channel_failing"].Subject)
	assert.Contains(t, byID["alerts.channel_failing"].Message, "no route to host")
	assert.Equal(t, inspect.StatusFail, byID["alerts.undelivered"].Status)

	// A later success clears the channel.
	ctrl.recordDelivery("ops")(event.Event{ID: e.ID + 1}, nil)
	recs, err := ctrl.db.ListNotifyDeliveries(ctx)
	require.NoError(t, err)
	assert.Zero(t, recs["ops"].ConsecutiveFailures)
	assert.Equal(t, []uint64{e.ID}, recs["ops"].FailedEvents)
}

func TestInspectionIsScheduledOnTheCron(t *testing.T) {
	ctrl, _ := inspectTestController(t)
	sm := ctrl.schedules
	require.NoError(t, sm.rebuildLocked(context.Background()))
	assert.Len(t, sm.cron.Entries(), 1)

	ctrl.config.Inspect.Enabled = false
	require.NoError(t, sm.rebuildLocked(context.Background()))
	assert.Empty(t, sm.cron.Entries())
}

func TestInspectionRPCs(t *testing.T) {
	ctrl, _ := inspectTestController(t)
	srv := NewServer(ctrl)
	ctx := context.Background()

	resp, err := srv.GetInspection(ctx, &haifypb.GetInspectionRequest{Id: "latest"})
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Message, "haify inspect run")

	run, err := srv.RunInspection(ctx, &haifypb.RunInspectionRequest{Areas: []string{"gateways", "nodes"}})
	require.NoError(t, err)
	require.True(t, run.Success, run.Message)
	assert.Equal(t, []string{"gateways", "nodes"}, run.Report.Areas)
	assert.NotEmpty(t, run.Report.Checks)
	assert.Equal(t, "fail", run.Report.Checks[0].Status)

	list, err := srv.ListInspections(ctx, &haifypb.ListInspectionsRequest{})
	require.NoError(t, err)
	require.Len(t, list.Reports, 1)
	assert.Empty(t, list.Reports[0].Checks, "listings omit the checks")
	assert.Equal(t, run.Report.Summary.Fail, list.Reports[0].Summary.Fail)

	bad, err := srv.RunInspection(ctx, &haifypb.RunInspectionRequest{Areas: []string{"everything"}})
	require.NoError(t, err)
	assert.False(t, bad.Success)
}
