package controller

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/liliang-cn/sds/pkg/config"
	"github.com/liliang-cn/sds/pkg/database"
	"github.com/liliang-cn/sds/pkg/deployment"
)

// registerNodes adds online nodes to the controller's node manager and host map.
func registerNodes(ctrl *Controller, nodes map[string]string) {
	for name, addr := range nodes {
		ctrl.nodes.nodes[addr] = &NodeInfo{Name: name, Address: addr, State: NodeStateOnline}
		ctrl.hostsMap[name] = addr
	}
}

func TestGenerateDrbdConfigDisklessTiebreaker(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	registerNodes(ctrl, map[string]string{
		"node1": "10.0.0.1",
		"node2": "10.0.0.2",
		"node3": "10.0.0.3",
	})

	cfg := ctrl.resources.generateDrbdConfig(
		"res1", 7001, 0,
		[]string{"node1", "node2"}, []string{"node3"},
		"C", "vg0", "res1_data", "lvm", nil)

	// The diskful nodes share the resource-level data disk.
	assert.Equal(t, 1, strings.Count(cfg, "disk      /dev/vg0/res1_data;"),
		"backing disk should appear exactly once, at resource level")

	// The tiebreaker carries a volume override that makes it diskless.
	assert.Contains(t, cfg, "on node3 {")
	assert.Contains(t, cfg, "disk      none;")
	assert.Equal(t, 1, strings.Count(cfg, "disk      none;"),
		"only the tiebreaker should be diskless")

	// node-id is assigned by combined ordering; tiebreaker comes last.
	assert.Contains(t, cfg, "node-id   2;")

	// A 3-node resource must declare a full connection mesh covering all nodes.
	assert.Contains(t, cfg, "connection-mesh {")
	assert.Contains(t, cfg, "hosts node1 node2 node3;")
}

func TestParseDevNodeMinor(t *testing.T) {
	cases := map[string]struct {
		minor int
		ok    bool
	}{
		"/dev/drbd1005":         {1005, true},
		"/dev/drbd0":            {0, true},
		"  /dev/drbd42  ":       {42, true},
		"/dev/drbd/by-res/data": {0, false},
		"/dev/drbdmanage":       {0, false},
		"/dev/sda1":             {0, false},
		"":                      {0, false},
	}
	for in, want := range cases {
		got, ok := parseDevNodeMinor(in)
		assert.Equal(t, want.ok, ok, "ok for %q", in)
		if want.ok {
			assert.Equal(t, want.minor, got, "minor for %q", in)
		}
	}
}

func TestNextGlobalMinorAvoidsStaleKernelMinor(t *testing.T) {
	// .res files only mention up to minor 1004, but stale /dev/drbd nodes go
	// up to 2000 — the next minor must clear the kernel-held ones too.
	dep := &fakeDeploymentClient{
		execFunc: func(ctx context.Context, hosts []string, cmd string, opts ...deployment.ExecOption) (*deployment.ExecResult, error) {
			out := "        device    minor 1004;\n" +
				"/dev/drbd0\n/dev/drbd1004\n/dev/drbd1005\n/dev/drbd2000\n/dev/drbd/by-res/data\n"
			return successExecResult(hosts, out), nil
		},
	}
	ctrl := newBasicTestController(dep)
	minor, err := ctrl.resources.nextGlobalMinor(context.Background(), "10.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, 2001, minor)
}

func TestSelectTiebreaker(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	registerNodes(ctrl, map[string]string{
		"node1": "10.0.0.1",
		"node2": "10.0.0.2",
		"node3": "10.0.0.3",
	})

	// A spare node not in the resource is chosen.
	tb := ctrl.resources.selectTiebreaker(context.Background(), []string{"node1", "node2"})
	assert.Equal(t, "node3", tb)

	// No spare node available -> empty.
	tb = ctrl.resources.selectTiebreaker(context.Background(), []string{"node1", "node2", "node3"})
	assert.Equal(t, "", tb)
}

func TestSelectTiebreakerPrefersOnline(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	registerNodes(ctrl, map[string]string{
		"node1": "10.0.0.1",
		"node2": "10.0.0.2",
	})
	// node3 offline, node4 online: the online node wins even though node3 sorts first.
	ctrl.nodes.nodes["10.0.0.3"] = &NodeInfo{Name: "node3", Address: "10.0.0.3", State: NodeStateOffline}
	ctrl.nodes.nodes["10.0.0.4"] = &NodeInfo{Name: "node4", Address: "10.0.0.4", State: NodeStateOnline}

	tb := ctrl.resources.selectTiebreaker(context.Background(), []string{"node1", "node2"})
	assert.Equal(t, "node4", tb)
}

func TestCreateResourceAutoAddsTiebreaker(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.config = &config.Config{Resource: config.ResourceConfig{AutoTiebreaker: true}}
	registerNodes(ctrl, map[string]string{
		"node1": "10.0.0.1",
		"node2": "10.0.0.2",
		"node3": "10.0.0.3",
	})

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	err = ctrl.resources.CreateResource(context.Background(), "res1", 7001,
		[]string{"node1", "node2"}, "", 10, "vg0", "lvm", nil)
	require.NoError(t, err)

	// Backing volumes and metadata are created on diskful nodes only.
	require.Len(t, dep.lvCreateCalls, 2)
	require.Len(t, dep.drbdCreateMDCalls, 1)
	assert.Len(t, dep.drbdCreateMDCalls[0].hosts, 2)

	// Config distribution and bring-up reach all three nodes.
	require.Len(t, dep.distributedConfigs, 1)
	require.Len(t, dep.drbdUpCalls, 1)
	assert.Len(t, dep.drbdUpCalls[0].hosts, 3)
	assert.Contains(t, dep.distributedConfigs[0].content, "disk      none;")

	// The tiebreaker is recorded; the resource is no longer at quorum risk.
	res, err := ctrl.resources.GetResource(context.Background(), "res1")
	require.NoError(t, err)
	assert.Equal(t, []string{"node3"}, res.DisklessNodes)
	assert.False(t, res.QuorumRisk)
}

func TestDeleteResourceTearsDownTiebreaker(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	registerNodes(ctrl, map[string]string{
		"orange1": "10.0.0.1",
		"orange2": "10.0.0.2",
		"orange3": "10.0.0.3",
	})

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	require.NoError(t, db.SaveResource(context.Background(), &database.Resource{
		Name:          "tb",
		Port:          7070,
		Nodes:         "orange1,orange2",
		DisklessNodes: "orange3",
		Protocol:      "C",
		Replicas:      2,
	}))

	require.NoError(t, ctrl.resources.DeleteResource(context.Background(), "tb", true))

	// The tiebreaker node must be torn down too, or its config and kernel
	// resource leak as an orphan.
	require.Len(t, dep.drbdDownCalls, 1)
	assert.ElementsMatch(t, []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}, dep.drbdDownCalls[0].hosts)
	require.Len(t, dep.deleteConfigCalls, 1)
	assert.ElementsMatch(t, []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}, dep.deleteConfigCalls[0].hosts)
}

func TestCreateResourceNoTiebreakerWhenDisabled(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.config = &config.Config{Resource: config.ResourceConfig{AutoTiebreaker: false}}
	registerNodes(ctrl, map[string]string{
		"node1": "10.0.0.1",
		"node2": "10.0.0.2",
		"node3": "10.0.0.3",
	})

	db, err := database.Open(&database.Config{Path: filepath.Join(t.TempDir(), "sds.db")}, zap.NewNop())
	require.NoError(t, err)
	defer db.Close()
	ctrl.db = db

	err = ctrl.resources.CreateResource(context.Background(), "res1", 7001,
		[]string{"node1", "node2"}, "", 10, "vg0", "lvm", nil)
	require.NoError(t, err)

	// No diskless node added: all operations stay on the two diskful nodes.
	require.Len(t, dep.distributedConfigs, 1)
	assert.NotContains(t, dep.distributedConfigs[0].content, "disk      none;")
	require.Len(t, dep.drbdUpCalls, 1)
	assert.Len(t, dep.drbdUpCalls[0].hosts, 2)

	res, err := ctrl.resources.GetResource(context.Background(), "res1")
	require.NoError(t, err)
	assert.Empty(t, res.DisklessNodes)
	assert.True(t, res.QuorumRisk, "bare 2-node resource should report quorum risk")
}
