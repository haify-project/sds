package controller

import (
	"context"
	"strings"
	"testing"

	haifypb "github.com/haify-project/haify/api/proto/v1"
	"github.com/haify-project/haify/pkg/database"
	"github.com/haify-project/haify/pkg/wanproxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withTempPKI points wanproxy's controller-side PKI cache at a temp dir so
// Provision can generate mTLS material without touching /var/lib/haify.
func withTempPKI(t *testing.T) {
	t.Helper()
	prev := wanproxy.PKIDir
	wanproxy.PKIDir = t.TempDir()
	t.Cleanup(func() { wanproxy.PKIDir = prev })
}

// findDistributedConfig returns the content distributed to remotePath, or "".
func findDistributedConfig(dep *fakeDeploymentClient, remotePath string) (string, bool) {
	for _, c := range dep.distributedConfigs {
		if c.remotePath == remotePath {
			return c.content, true
		}
	}
	return "", false
}

// execCmdIssued reports whether any exec call ran a command containing substr.
func execCmdIssued(dep *fakeDeploymentClient, substr string) bool {
	for _, c := range dep.execCalls {
		if strings.Contains(c.cmd, substr) {
			return true
		}
	}
	return false
}

// TestCreateResourceWANProvisionsAndPersists verifies the WAN create branch:
// it forces protocol A, participates on [primary, dr] with no tiebreaker,
// provisions the haify-proxy pair (config + enabled unit), and persists the WAN
// metadata onto the saved resource record.
func TestCreateResourceWANProvisionsAndPersists(t *testing.T) {
	withTempPKI(t)
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	registerNodes(ctrl, map[string]string{"primary": "10.0.0.1", "dr": "10.0.0.2"})

	err := ctrl.resources.CreateResourceWithVolumes(context.Background(),
		"wanres", 7100, []string{"primary"}, "C", "lvm", nil,
		[]VolumeSpec{{SizeGB: 10, Pool: "data-pool"}},
		&WANSpec{DRNode: "dr", DREndpoint: "dr.example.com", WANPort: 34567})
	require.NoError(t, err)

	// Persisted WAN metadata + forced protocol A + two-endpoint node set.
	stored, err := ctrl.db.GetResource(context.Background(), "wanres")
	require.NoError(t, err)
	assert.True(t, stored.WANMode, "WANMode must be persisted")
	assert.Equal(t, "dr", stored.DRNode)
	assert.Equal(t, "dr.example.com", stored.DREndpoint)
	assert.Equal(t, 34567, stored.WANPort)
	assert.Equal(t, "A", stored.Protocol, "WAN forces protocol A")
	assert.Equal(t, "primary,dr", stored.Nodes, "WAN participates on [primary, dr]")
	assert.Empty(t, stored.DisklessNodes, "WAN must not add a diskless tiebreaker")

	// The DRBD .res was rendered in WAN mode (loopback routing, protocol A).
	res, ok := findDistributedConfig(dep, "/etc/drbd.d/wanres.res")
	require.True(t, ok, "DRBD config was not distributed")
	assert.Contains(t, res, "protocol A;")
	assert.Contains(t, res, "127.0.0.1:7100;") // DR binds the DRBD port
	assert.NotContains(t, res, "10.0.0.1")     // no real peer IP leaks

	// The haify-proxy per-resource config was distributed to both endpoints.
	_, ok = findDistributedConfig(dep, wanproxy.NodeConfigPath("wanres"))
	require.True(t, ok, "per-resource haify-proxy config was not distributed")

	// The per-resource proxy unit was enabled and restarted (Provision ran
	// before up). Restart rather than `enable --now`: the latter no-ops on a
	// running leg, which would leave it on stale config and mTLS material.
	assert.True(t, execCmdIssued(dep, "systemctl enable "+wanproxy.UnitInstance("wanres")),
		"haify-proxy@wanres unit was not enabled; exec calls: %+v", dep.execCalls)
	assert.True(t, execCmdIssued(dep, "systemctl restart "+wanproxy.UnitInstance("wanres")),
		"haify-proxy@wanres unit was not restarted; exec calls: %+v", dep.execCalls)
}

// TestCreateResourceWANAutoPort auto-picks a random high port when WANPort is 0.
func TestCreateResourceWANAutoPort(t *testing.T) {
	withTempPKI(t)
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	registerNodes(ctrl, map[string]string{"primary": "10.0.0.1", "dr": "10.0.0.2"})

	err := ctrl.resources.CreateResourceWithVolumes(context.Background(),
		"wanres", 7100, []string{"primary"}, "C", "lvm", nil,
		[]VolumeSpec{{SizeGB: 10, Pool: "data-pool"}},
		&WANSpec{DRNode: "dr", DREndpoint: "dr.example.com"})
	require.NoError(t, err)

	stored, err := ctrl.db.GetResource(context.Background(), "wanres")
	require.NoError(t, err)
	assert.Greater(t, stored.WANPort, 3000, "auto WAN port must be > 3000")
}

// TestCreateResourceWANRejectsUnregisteredDRNode rejects a DR node that is not
// a registered node.
func TestCreateResourceWANRejectsUnregisteredDRNode(t *testing.T) {
	withTempPKI(t)
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	registerNodes(ctrl, map[string]string{"primary": "10.0.0.1"})

	err := ctrl.resources.CreateResourceWithVolumes(context.Background(),
		"wanres", 7100, []string{"primary"}, "C", "lvm", nil,
		[]VolumeSpec{{SizeGB: 10, Pool: "data-pool"}},
		&WANSpec{DRNode: "ghost", DREndpoint: "dr.example.com", WANPort: 34567})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a registered node")
	// Nothing was provisioned.
	assert.False(t, execCmdIssued(dep, "systemctl enable --now"))
}

// --nodes lists the primary SITE; naming the DR node there too is a mistake
// (it would ask the DR to replicate to itself), and is rejected.
func TestCreateResourceWANRejectsDRNodeAmongPrimaries(t *testing.T) {
	withTempPKI(t)
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	registerNodes(ctrl, map[string]string{"primary": "10.0.0.1", "dr": "10.0.0.2"})

	err := ctrl.resources.CreateResourceWithVolumes(context.Background(),
		"wanres", 7100, []string{"primary", "dr"}, "C", "lvm", nil,
		[]VolumeSpec{{SizeGB: 10, Pool: "data-pool"}},
		&WANSpec{DRNode: "dr", DREndpoint: "dr.example.com", WANPort: 34567})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not also be a primary-site node")
}

// A duplicated primary would provision two WAN legs for one node, fighting over
// the same loopback ports.
func TestCreateResourceWANRejectsDuplicatePrimary(t *testing.T) {
	withTempPKI(t)
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	registerNodes(ctrl, map[string]string{"a": "10.0.0.1", "dr": "10.0.0.2"})

	err := ctrl.resources.CreateResourceWithVolumes(context.Background(),
		"wanres", 7100, []string{"a", "a"}, "C", "lvm", nil,
		[]VolumeSpec{{SizeGB: 10, Pool: "data-pool"}},
		&WANSpec{DRNode: "dr", DREndpoint: "dr.example.com", WANPort: 34567})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "twice")
}

// 两地三中心: several synchronous primary-site replicas plus one async DR copy
// is now a supported shape, and provisions one WAN leg per primary.
func TestCreateResourceWANMultiReplicaPrimarySite(t *testing.T) {
	withTempPKI(t)
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	registerNodes(ctrl, map[string]string{"a": "10.0.0.1", "b": "10.0.0.2", "dr": "10.0.0.3"})

	err := ctrl.resources.CreateResourceWithVolumes(context.Background(),
		"wanres", 7300, []string{"a", "b"}, "C", "lvm", nil,
		[]VolumeSpec{{SizeGB: 10, Pool: "data-pool"}},
		&WANSpec{DRNode: "dr", DREndpoint: "dr.example.com", WANPort: 34567})
	require.NoError(t, err)

	cfg, ok := findDistributedConfig(dep, "/etc/drbd.d/wanres.res")
	require.True(t, ok, "expected the resource config to be distributed")
	// The primary site keeps real addresses and a synchronous mesh; only the
	// two WAN legs are async.
	assert.Contains(t, cfg, "10.0.0.1:7300")
	assert.Contains(t, cfg, "10.0.0.2:7300")
	assert.Equal(t, 2, strings.Count(cfg, "connection {"), "one WAN leg per primary")

	// Each leg gets its own proxy config on the DR, which terminates both.
	//
	// Legs are named after the NODE, not its address. Naming them after the
	// address bakes a mutable fact into a systemd instance name, a config
	// filename and a metrics path, so renumbering the node orphans a tunnel
	// that is still replicating: status reports a phantom outage and
	// deprovisioning misses it entirely.
	_, aLeg := findDistributedConfig(dep, wanproxy.NodeConfigPath("wanres_a"))
	_, bLeg := findDistributedConfig(dep, wanproxy.NodeConfigPath("wanres_b"))
	assert.True(t, aLeg, "expected a proxy config for node a's leg")
	assert.True(t, bLeg, "expected a proxy config for node b's leg")

	_, byAddr := findDistributedConfig(dep, wanproxy.NodeConfigPath("wanres_10-0-0-1"))
	assert.False(t, byAddr, "a leg must not be named after an address")
}

// A WAN leg is a systemd instance named "<resource>_<node>", so a resource
// named like one of those would be indistinguishable from another resource's
// leg — and leg reconciliation would treat one's tunnels as the other's litter.
func TestCreateResourceWANRejectsLegShapedName(t *testing.T) {
	withTempPKI(t)
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	registerNodes(ctrl, map[string]string{"a": "10.0.0.1", "b": "10.0.0.2", "dr": "10.0.0.3"})
	ctx := context.Background()

	require.NoError(t, ctrl.resources.CreateResourceWithVolumes(ctx,
		"wanres", 7300, []string{"a", "b"}, "C", "lvm", nil,
		[]VolumeSpec{{SizeGB: 10, Pool: "data-pool"}},
		&WANSpec{DRNode: "dr", DREndpoint: "dr.example.com", WANPort: 34567}))

	err := ctrl.resources.CreateResourceWithVolumes(ctx,
		"wanres_a", 7400, []string{"a", "b"}, "C", "lvm", nil,
		[]VolumeSpec{{SizeGB: 10, Pool: "data-pool"}},
		&WANSpec{DRNode: "dr", DREndpoint: "dr.example.com", WANPort: 34600})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides with WAN leg names")

	// A name that merely shares a prefix is fine — only the "_" shape collides.
	assert.NoError(t, ctrl.resources.CreateResourceWithVolumes(ctx,
		"wanresdata", 7500, []string{"a", "b"}, "C", "lvm", nil,
		[]VolumeSpec{{SizeGB: 10, Pool: "data-pool"}},
		&WANSpec{DRNode: "dr", DREndpoint: "dr.example.com", WANPort: 34700}))
}

// TestServerCreateResourceWANSucceeds drives the gRPC handler with a WAN request
// and a registered DR node; it must reach the resource layer and provision.
func TestServerCreateResourceWANSucceeds(t *testing.T) {
	withTempPKI(t)
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	registerNodes(ctrl, map[string]string{"primary": "10.0.0.1", "dr": "10.0.0.2"})
	srv := NewServer(ctrl)

	resp, err := srv.CreateResource(context.Background(), &haifypb.CreateResourceRequest{
		Name:        "wanres",
		Port:        7100,
		Nodes:       []string{"primary"},
		Protocol:    "A",
		SizeGb:      10,
		Pool:        "data-pool",
		StorageType: "lvm",
		Wan:         true,
		DrNode:      "dr",
		DrEndpoint:  "dr.example.com",
		WanPort:     34567,
	})
	require.NoError(t, err)
	require.True(t, resp.Success, "WAN create should succeed: %s", resp.Message)

	stored, err := ctrl.db.GetResource(context.Background(), "wanres")
	require.NoError(t, err)
	assert.True(t, stored.WANMode)
	assert.True(t, execCmdIssued(dep, "systemctl restart "+wanproxy.UnitInstance("wanres")))
}

// TestServerCreateResourceRejectsDRFieldsWithoutWan enforces the master switch:
// dr_* fields without --wan are rejected before touching the resource layer.
func TestServerCreateResourceRejectsDRFieldsWithoutWan(t *testing.T) {
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	registerNodes(ctrl, map[string]string{"primary": "10.0.0.1", "dr": "10.0.0.2"})
	srv := NewServer(ctrl)

	resp, err := srv.CreateResource(context.Background(), &haifypb.CreateResourceRequest{
		Name:        "wanres",
		Port:        7100,
		Nodes:       []string{"primary"},
		SizeGb:      10,
		Pool:        "data-pool",
		StorageType: "lvm",
		Wan:         false,
		DrNode:      "dr",
	})
	require.NoError(t, err)
	require.False(t, resp.Success)
	assert.Contains(t, resp.Message, "require --wan")
	// Rejected before the resource layer: nothing was distributed.
	assert.Empty(t, dep.distributedConfigs)
}

// TestServerCreateResourceWANRejectsUnregisteredDRNode surfaces the resource
// layer's validation through the handler.
func TestServerCreateResourceWANRejectsUnregisteredDRNode(t *testing.T) {
	withTempPKI(t)
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	registerNodes(ctrl, map[string]string{"primary": "10.0.0.1"})
	srv := NewServer(ctrl)

	resp, err := srv.CreateResource(context.Background(), &haifypb.CreateResourceRequest{
		Name:        "wanres",
		Port:        7100,
		Nodes:       []string{"primary"},
		SizeGb:      10,
		Pool:        "data-pool",
		StorageType: "lvm",
		Wan:         true,
		DrNode:      "ghost",
		DrEndpoint:  "dr.example.com",
	})
	require.NoError(t, err)
	require.False(t, resp.Success)
	assert.Contains(t, resp.Message, "not a registered node")
}

// TestDeleteResourceWANDeprovisions confirms the delete path tears down the
// per-resource haify-proxy after DRBD down when the stored resource is WAN.
func TestDeleteResourceWANDeprovisions(t *testing.T) {
	withTempPKI(t)
	dep := &fakeDeploymentClient{}
	ctrl := newBasicTestController(dep)
	ctrl.db = newTestDB(t)
	registerNodes(ctrl, map[string]string{"primary": "10.0.0.1", "dr": "10.0.0.2"})

	require.NoError(t, ctrl.db.SaveResource(context.Background(), &database.Resource{
		Name:       "wanres",
		Port:       7100,
		Nodes:      "primary,dr",
		Protocol:   "A",
		Replicas:   2,
		WANMode:    true,
		DRNode:     "dr",
		DREndpoint: "dr.example.com",
		WANPort:    34567,
	}))

	err := ctrl.resources.DeleteResource(context.Background(), "wanres", true)
	require.NoError(t, err)
	assert.True(t, execCmdIssued(dep, "systemctl disable --now "+wanproxy.UnitInstance("wanres")),
		"WAN delete must disable the haify-proxy@wanres unit; exec calls: %+v", dep.execCalls)
}
