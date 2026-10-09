package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/haify-project/haify/pkg/config"
	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/deployment"
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
		"res1", 7001,
		[]resolvedVolume{{id: 0, minor: 0, pool: "vg0", volumeName: "res1_data"}},
		[]string{"node1", "node2"}, []string{"node3"},
		"C", "lvm", nil, nil)

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

func TestGenerateDrbdConfigMultipleVolumes(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	registerNodes(ctrl, map[string]string{
		"node1": "10.0.0.1",
		"node2": "10.0.0.2",
	})

	cfg := ctrl.resources.generateDrbdConfig(
		"res1", 7001,
		[]resolvedVolume{
			{id: 0, minor: 5, pool: "vg0", volumeName: "res1_data"},
			{id: 1, minor: 6, pool: "vg1", volumeName: "res1_vol1"},
		},
		[]string{"node1", "node2"}, nil,
		"C", "lvm", nil, nil)

	// Both volume blocks are present with their own minor and backing disk.
	assert.Contains(t, cfg, "volume 0 {")
	assert.Contains(t, cfg, "device    minor 5;")
	assert.Contains(t, cfg, "disk      /dev/vg0/res1_data;")
	assert.Contains(t, cfg, "volume 1 {")
	assert.Contains(t, cfg, "device    minor 6;")
	assert.Contains(t, cfg, "disk      /dev/vg1/res1_vol1;")

	// Each volume appears exactly once at the resource level (diskful nodes).
	assert.Equal(t, 1, strings.Count(cfg, "volume 0 {"))
	assert.Equal(t, 1, strings.Count(cfg, "volume 1 {"))
}

func TestGenerateDrbdConfigMultiVolumeDisklessOverridesEach(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	registerNodes(ctrl, map[string]string{
		"node1": "10.0.0.1",
		"node2": "10.0.0.2",
		"node3": "10.0.0.3",
	})

	cfg := ctrl.resources.generateDrbdConfig(
		"res1", 7001,
		[]resolvedVolume{
			{id: 0, minor: 0, pool: "vg0", volumeName: "res1_data"},
			{id: 1, minor: 1, pool: "vg0", volumeName: "res1_vol1"},
		},
		[]string{"node1", "node2"}, []string{"node3"},
		"C", "lvm", nil, nil)

	// The diskless tiebreaker must override BOTH volumes with disk none, or it
	// would try to attach a backing disk it does not have.
	assert.Equal(t, 2, strings.Count(cfg, "disk      none;"),
		"tiebreaker should mark every volume diskless")
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
	minor, err := ctrl.resources.nextGlobalMinor(context.Background(), []string{"10.0.0.1"})
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

// A compute-only node (no storage pool — e.g. a Proxmox hypervisor registered
// only to attach volumes as a diskless client) must NOT be chosen as a quorum
// tiebreaker when a real storage node is free, even though it sorts first by
// name. Regression for the real-hardware bug where "hp" (a PVE host) got pulled
// into every 2-replica resource's quorum mesh instead of the spare orange node.
func TestSelectTiebreakerPrefersStorageNodes(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	registerNodes(ctrl, map[string]string{
		"orange1": "10.0.0.1",
		"orange2": "10.0.0.2",
		"orange3": "10.0.0.3",
		"hp":      "10.0.0.9", // compute-only: no pool, and "hp" < "orange2"
	})
	// Pools live only on the orange nodes. One recorded by name, one by address,
	// to prove storageNodeSet normalizes both forms.
	require.NoError(t, ctrl.db.SavePool(context.Background(), &database.Pool{Name: "haify_vg0-o1", Type: "vg", Node: "orange1"}))
	require.NoError(t, ctrl.db.SavePool(context.Background(), &database.Pool{Name: "haify_vg0-o2", Type: "vg", Node: "10.0.0.2"}))
	require.NoError(t, ctrl.db.SavePool(context.Background(), &database.Pool{Name: "haify_vg0-o3", Type: "vg", Node: "orange3"}))

	// Replicas on orange1+orange3 → candidates are orange2 (storage) and hp
	// (compute). hp sorts first, but the storage node must win.
	tb := ctrl.resources.selectTiebreaker(context.Background(), []string{"orange1", "orange3"})
	assert.Equal(t, "orange2", tb, "a spare storage node beats a compute-only node")

	// When every storage node is a replica, fall back to the compute node —
	// a diskless tiebreaker on it still beats a bare 2-node resource.
	tb = ctrl.resources.selectTiebreaker(context.Background(), []string{"orange1", "orange2", "orange3"})
	assert.Equal(t, "hp", tb, "fall back to a compute node when no storage node is free")
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

	db := newTestDB(t)
	ctrl.db = db

	err := ctrl.resources.CreateResource(context.Background(), "res1", 7001,
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

	db := newTestDB(t)
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

	db := newTestDB(t)
	ctrl.db = db

	err := ctrl.resources.CreateResource(context.Background(), "res1", 7001,
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

// A WAN/DR node must never be auto-selected as a quorum tiebreaker: it would
// join the resource's DRBD connection mesh over the internet, and on a cloud
// instance its public address is not even configured on an interface, so
// `drbdadm up` fails with "IP <addr> not found on this host" — after the
// volumes have already been created. Labelling it opts it out.
func TestSelectTiebreakerSkipsLabelledNodes(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	registerNodes(ctrl, map[string]string{
		"node1": "10.0.0.1",
		"node2": "10.0.0.2",
		"dr":    "203.0.113.10",
	})
	ctrl.nodes.nodes["203.0.113.10"].Labels = map[string]string{TiebreakerLabel: "false"}

	// "dr" sorts before "node3"-style names, so without the opt-out it would win.
	tb := ctrl.resources.selectTiebreaker(context.Background(), []string{"node1", "node2"})
	assert.Equal(t, "", tb, "the only spare node opted out, so there is no tiebreaker")

	// A second spare that has NOT opted out is still selected.
	registerNodes(ctrl, map[string]string{"node3": "10.0.0.3"})
	tb = ctrl.resources.selectTiebreaker(context.Background(), []string{"node1", "node2"})
	assert.Equal(t, "node3", tb)
}

// Only "false" opts out; any other value (or none) leaves the node eligible, so
// a stray label cannot quietly shrink the candidate pool.
func TestSelectTiebreakerLabelOnlyExcludesFalse(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	registerNodes(ctrl, map[string]string{
		"node1": "10.0.0.1",
		"node2": "10.0.0.2",
		"node3": "10.0.0.3",
	})
	ctrl.nodes.nodes["10.0.0.3"].Labels = map[string]string{TiebreakerLabel: "true"}

	tb := ctrl.resources.selectTiebreaker(context.Background(), []string{"node1", "node2"})
	assert.Equal(t, "node3", tb)

	// Case-insensitive on the opt-out value.
	ctrl.nodes.nodes["10.0.0.3"].Labels[TiebreakerLabel] = "False"
	tb = ctrl.resources.selectTiebreaker(context.Background(), []string{"node1", "node2"})
	assert.Equal(t, "", tb)
}
